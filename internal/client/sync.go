// Package client is the shared folder client: the local state of a joined
// folder and one full two-way synchronization cycle
// (docs/specifikacija.md, sections 7 and 8).
//
// The state of a folder lives outside it, in <state home>/<share-id>/:
// config.json and token (0600), the member's SSH key, known_hosts and
// state.db with
//
//   - the manifest: every path as it was after its last successful sync;
//   - the remote table: the server's state, built from its change log up
//     to remote_seq;
//   - the applied seq, the problems found and a log of cycles.
//
// A cycle compares three states per path — the disk, the manifest and the
// server — with Plan, applies the result and records every applied step in
// the manifest at once, so an interrupted cycle resumes where it stopped.
package client

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"sort"
	"sync"
	"syscall"
	"time"

	"github.com/digital-cube/shared-folder/internal/ignore"
	"github.com/digital-cube/shared-folder/internal/transport"
	"github.com/digital-cube/shared-folder/internal/transport/rsyncssh"
)

// Options tune a Client. The zero value is what the CLI uses.
type Options struct {
	// Transport replaces rsync over SSH (tests use transport.Local).
	Transport transport.Transport
	// HTTPClient talks to the control API (default: 60 s timeout).
	HTTPClient *http.Client
	// Now is the clock of the local log.
	Now func() time.Time
	// AfterApply, when set, runs after every applied step that changed the
	// disk or the server; an error stops the cycle there (tests use it to
	// interrupt a cycle).
	AfterApply func(Action) error
	// MassDelete are the limits of the mass-delete guard (nil: the
	// defaults, DefaultMassDelete).
	MassDelete *MassDelete
	// Settle, when positive, holds back the upload of a file until its
	// size and mtime have not changed for that long (the daemon: 3 s), so
	// a file is not sent while it is still being written. Result.RetryAt
	// says when to look again.
	Settle time.Duration
	// OnWrite, when set, is called after the cycle itself changed a path
	// on disk (a download, a local removal, rename or new directory), with
	// what the path now is (Kind None: removed). The daemon uses it to
	// tell its own writes from the user's.
	OnWrite func(path string, now Entry)
}

// maxRounds bounds the rounds of one cycle. A round fetches the server's
// changes, scans and applies a plan; the cycle ends with the first round
// that has nothing left to do (normally the second, which also sees the
// cycle's own changes come back from the server's log).
const maxRounds = 5

// Client synchronizes one joined folder.
type Client struct {
	cfg  *Config
	st   *State
	api  *api
	tr   transport.Transport
	opts Options

	mu   sync.Mutex
	seen map[string]sighting // files held back by Settle
}

// Open opens a joined folder's state.
func Open(cfg *Config, opts Options) (*Client, error) {
	if opts.Now == nil {
		opts.Now = time.Now
	}
	tr := opts.Transport
	if tr == nil {
		if cfg.SSH == nil {
			return nil, errors.New("no SSH endpoint in the folder's config; join again")
		}
		var err error
		tr, err = rsyncssh.New(rsyncssh.Config{
			ShareID: cfg.ShareID, Host: cfg.SSH.Host, Port: cfg.SSH.Port, User: cfg.SSH.User,
			KeyFile: cfg.KeyFile(), StateDir: cfg.Dir,
		})
		if err != nil {
			return nil, err
		}
	}
	st, err := OpenState(cfg.Dir)
	if err != nil {
		return nil, err
	}
	return &Client{cfg: cfg, st: st, api: newAPI(cfg.URL, cfg.ShareID, cfg.Token, opts.HTTPClient), tr: tr, opts: opts}, nil
}

// Head asks the server for the folder's HEAD.
func (c *Client) Head(ctx context.Context) (int64, error) { return c.api.head(ctx) }

// Close releases the state.
func (c *Client) Close() error { return c.st.Close() }

// Config is the folder's configuration.
func (c *Client) Config() *Config { return c.cfg }

// State is the folder's local state.
func (c *Client) State() *State { return c.st }

// Result is what a cycle did.
type Result struct {
	Cycle
	Problems []Problem
	// Pending are steps that were planned but not applied because the
	// disk or the server changed under them; the next cycle takes them up.
	Pending int
	// Unsettled counts uploads held back because the file is still being
	// written (Options.Settle); RetryAt is when the first may go.
	Unsettled int
	RetryAt   time.Time
	// Blocked: the mass-delete guard holds deletions back (State.HeldDeletes).
	Blocked bool
}

// SyncOptions tune one cycle.
type SyncOptions struct {
	// Full rebuilds the server's state from its whole change log instead
	// of reading only the events after the last one seen: the periodic
	// full comparison that catches anything a missed event left behind.
	Full bool
	// Stop, when closed, ends the cycle after the step in progress (the
	// daemon on SIGTERM). The cycle then fails with ErrStopped.
	Stop <-chan struct{}
}

// ErrStopped: the cycle ended early because SyncOptions.Stop was closed.
var ErrStopped = errors.New("stopped")

// ErrFolderUnavailable: the folder's root is missing or not readable (an
// unmounted disk). Nothing is synchronized, and nothing is taken for a
// deletion.
var ErrFolderUnavailable = errors.New("folder unavailable")

// errPending marks a step skipped because its ground moved: the disk
// changed since the scan, or the server since the change log was read.
// The next round plans it again from fresh states.
var errPending = errors.New("changed meanwhile; planned again")

// problemCodes are refusals that need the user, not a retry.
var problemCodes = map[string]bool{
	"invalid_path": true, "case_conflict": true, "kind_mismatch": true, "parent_not_dir": true, "is_dir": true,
}

// Sync runs one cycle. It fails with ErrBusy when another cycle holds the
// folder; any other error means something was not synchronized, and the
// result still says what was.
func (c *Client) Sync(ctx context.Context) (*Result, error) {
	return c.SyncWith(ctx, SyncOptions{})
}

// SyncWith runs one cycle with options.
func (c *Client) SyncWith(ctx context.Context, so SyncOptions) (*Result, error) {
	unlock, err := lock(c.cfg.Dir)
	if err != nil {
		return nil, err
	}
	defer unlock()
	return c.syncLocked(ctx, so)
}

// checkRoot fails with ErrFolderUnavailable unless the folder's root is a
// directory that can be listed.
func (c *Client) checkRoot() error {
	fi, err := os.Stat(c.cfg.Folder)
	if err == nil && !fi.IsDir() {
		err = errors.New("not a directory")
	}
	if err == nil {
		var f *os.File
		if f, err = os.Open(c.cfg.Folder); err == nil {
			_, err = f.Readdirnames(1)
			f.Close()
			if errors.Is(err, io.EOF) {
				err = nil
			}
		}
	}
	if err != nil {
		return fmt.Errorf("%w: %s: %v; not synchronizing (a missing folder would look like everything was deleted)", ErrFolderUnavailable, c.cfg.Folder, err)
	}
	return nil
}

func (c *Client) syncLocked(ctx context.Context, so SyncOptions) (*Result, error) {
	if err := c.checkRoot(); err != nil {
		return nil, err
	}
	var err error
	r := &Result{}
	r.StartedAt = c.opts.Now()
	if r.FromSeq, err = c.st.Seq(); err != nil {
		return nil, err
	}
	cs := &cycleState{c: c, res: r, rejected: map[string]string{}, so: so}
	var cycleErr error
	settled := false
	for round := 0; round < maxRounds && cycleErr == nil; round++ {
		var work int
		work, cycleErr = cs.round(ctx)
		if cycleErr == nil && work == 0 {
			settled = true
			break
		}
	}
	if cycleErr == nil && !settled {
		cycleErr = fmt.Errorf("the folder kept changing; %d steps wait for the next cycle", r.Pending)
	}
	if cycleErr == nil {
		r.Pending = 0
	}
	r.ToSeq, _ = c.st.metaInt(metaRemoteSeq)
	r.ToSeq = max(r.ToSeq, r.FromSeq)
	r.Head, _ = c.st.metaInt(metaHead)
	r.Problems = cs.problems
	r.Blocked, _ = c.st.Blocked()
	r.FinishedAt = c.opts.Now()
	if cycleErr != nil {
		r.Error = cycleErr.Error()
	}
	rep := syncReport{StartedAt: r.StartedAt.UTC(), FinishedAt: r.FinishedAt.UTC(), FromSeq: r.FromSeq, ToSeq: r.ToSeq,
		Sent: r.Sent, Received: r.Received, Conflicts: r.Conflicts, Error: r.Error}
	if err := c.api.report(ctx, rep); err != nil && cycleErr == nil {
		r.Items = append(r.Items, Item{Op: "warning", Detail: "sync-report not delivered: " + err.Error()})
	}
	if err := c.st.finishCycle(&r.Cycle, r.Problems, cycleErr == nil); err != nil {
		return r, errors.Join(cycleErr, err)
	}
	return r, cycleErr
}

// cycleState is one cycle in progress.
type cycleState struct {
	c        *Client
	res      *Result
	problems []Problem
	// rejected are paths the server refused this cycle; later rounds leave
	// them alone instead of asking again.
	rejected map[string]string
	so       SyncOptions
	fetched  bool // the first fetch (a full one with so.Full) is done
}

func (cs *cycleState) stopped() bool {
	select {
	case <-cs.so.Stop:
		return true
	default:
		return false
	}
}

// wrote tells Options.OnWrite about a change the cycle made on disk.
func (cs *cycleState) wrote(p string, e Entry) {
	if f := cs.c.opts.OnWrite; f != nil {
		f(p, e)
	}
}

func (cs *cycleState) log(op, p, detail string) {
	cs.res.Items = append(cs.res.Items, Item{Op: op, Path: p, Detail: detail})
}

// fetch reads the server's log after remote_seq into the remote table;
// the first fetch of a full cycle reads the whole log and replaces it.
func (cs *cycleState) fetch(ctx context.Context) error {
	c := cs.c
	full := cs.so.Full && !cs.fetched
	cs.fetched = true
	if full {
		evs, head, err := c.api.changes(ctx, 0)
		if err != nil {
			return fmt.Errorf("reading changes: %w", err)
		}
		remote := map[string]Entry{}
		applyEvents(remote, evs)
		to := int64(0)
		if len(evs) > 0 {
			to = evs[len(evs)-1].Seq
		}
		return c.st.replaceRemote(remote, to, head)
	}
	since, err := c.st.metaInt(metaRemoteSeq)
	if err != nil {
		return err
	}
	evs, head, err := c.api.changes(ctx, since)
	if err != nil {
		return fmt.Errorf("reading changes: %w", err)
	}
	if head < since {
		return fmt.Errorf("the server's log ends at %d, before this client's %d; was the server restored from a backup?", head, since)
	}
	to := since
	if len(evs) > 0 {
		to = evs[len(evs)-1].Seq
	}
	return c.st.ApplyEvents(evs, to, head)
}

// round fetches, scans, plans and applies once. work counts the planned
// steps that change the disk or the server.
func (cs *cycleState) round(ctx context.Context) (work int, err error) {
	c := cs.c
	if err := cs.fetch(ctx); err != nil {
		return 0, err
	}
	manifest, err := c.st.Manifest()
	if err != nil {
		return 0, err
	}
	remote, err := c.st.Remote()
	if err != nil {
		return 0, err
	}
	m, err := ignore.FromDir(c.cfg.Folder)
	if err != nil {
		return 0, fmt.Errorf("reading .shfignore: %w", err)
	}
	// A .shfignore that arrived counts before the other changes of the
	// same batch.
	if n, err := cs.ignoreFilesFirst(ctx, manifest, remote, m); err != nil {
		return 0, err
	} else if n > 0 {
		work += n
		if manifest, err = c.st.Manifest(); err != nil {
			return 0, err
		}
		if m, err = ignore.FromDir(c.cfg.Folder); err != nil {
			return 0, fmt.Errorf("reading .shfignore: %w", err)
		}
	}
	sc, err := scan(c.cfg.Folder, m, manifest)
	if err != nil {
		return 0, fmt.Errorf("scanning %s: %w", c.cfg.Folder, err)
	}
	cs.problems = append([]Problem(nil), sc.Problems...)
	for p, why := range cs.rejected {
		cs.problems = append(cs.problems, Problem{Path: p, Reason: why})
	}
	untracked := func(p string) bool {
		_, ok := cs.rejected[p]
		return ok || sc.untracked(p)
	}
	acts := Plan(PlanInput{
		Manifest: manifest, Local: sc.Local, Remote: remote,
		Ignored: func(p string, isDir bool) bool { return untracked(p) || m.Match(p, isDir) },
	})
	if acts, err = cs.guard(acts, manifest); err != nil {
		return work, err
	}
	cs.res.Pending, cs.res.Unsettled, cs.res.RetryAt = 0, 0, time.Time{}
	acts = cs.holdUnsettled(acts)
	var hard []error
	for _, a := range acts {
		if err := ctx.Err(); err != nil {
			return work, err
		}
		if cs.stopped() {
			return work, ErrStopped
		}
		if !a.IsBookkeeping() {
			work++
		}
		err := cs.apply(ctx, a, sc)
		switch {
		case err == nil:
			if !a.IsBookkeeping() && c.opts.AfterApply != nil {
				if err := c.opts.AfterApply(a); err != nil {
					return work, err
				}
			}
		case errors.Is(err, errPending):
			cs.res.Pending++
			cs.log("pending", a.Path, string(a.Op)+": "+err.Error())
		case problemCodes[codeOf(err)]:
			cs.rejected[a.Path] = err.Error()
			cs.problems = append(cs.problems, Problem{Path: a.Path, Reason: err.Error()})
			cs.log("problem", a.Path, err.Error())
		default:
			if ctx.Err() != nil {
				return work, ctx.Err()
			}
			hard = append(hard, fmt.Errorf("%s %s: %w", a.Op, a.Path, err))
			cs.log("error", a.Path, string(a.Op)+": "+err.Error())
		}
	}
	sort.Slice(cs.problems, func(i, j int) bool { return cs.problems[i].Path < cs.problems[j].Path })
	if len(hard) > 0 {
		if len(hard) > 3 {
			hard = append(hard[:3], fmt.Errorf("and %d more", len(hard)-3))
		}
		return work, errors.Join(hard...)
	}
	return work, nil
}

// ignoreFilesFirst applies the server's changes to .shfignore files
// before anything else, shallowest first, so their rules already hold for
// the rest of the batch. A .shfignore changed locally as well is left to
// the plan (it becomes a conflict, and the local rules stay in force).
func (cs *cycleState) ignoreFilesFirst(ctx context.Context, manifest map[string]ManifestEntry, remote map[string]Entry, m *ignore.Matcher) (int, error) {
	var cands []string
	for p, r := range remote {
		if path.Base(p) == ignore.FileName && r.Kind == File {
			cands = append(cands, p)
		}
	}
	for p, me := range manifest {
		if path.Base(p) == ignore.FileName && me.State == Synced {
			if _, ok := remote[p]; !ok {
				cands = append(cands, p)
			}
		}
	}
	sort.Slice(cands, func(i, j int) bool {
		if di, dj := depth(cands[i]), depth(cands[j]); di != dj {
			return di < dj
		}
		return cands[i] < cands[j]
	})
	empty := &scanResult{Disk: map[string]string{}}
	n := 0
	for _, p := range cands {
		r := remote[p]
		me, hasM := manifest[p]
		var base Entry
		if hasM && me.State == Synced {
			base = me.Entry
		}
		if same(r, base) || m.Match(p, false) {
			continue
		}
		abs := filepath.Join(cs.c.cfg.Folder, filepath.FromSlash(p))
		l, err := localEntry(abs, base)
		if err != nil {
			return n, err
		}
		if !same(l, base) {
			if same(l, r) { // both sides already agree
				if err := cs.c.st.SetManifest(p, l, Synced); err != nil {
					return n, err
				}
			}
			continue
		}
		var a Action
		switch r.Kind {
		case File:
			a = Action{Op: OpDownload, Path: p, Local: l, Remote: r}
		default:
			if l.Kind != File {
				continue
			}
			a = Action{Op: OpRemoveLocal, Path: p, Local: l}
		}
		if err := cs.apply(ctx, a, empty); err != nil && !errors.Is(err, errPending) {
			return n, fmt.Errorf("%s %s: %w", a.Op, p, err)
		}
		n++
		if m2, err := ignore.FromDir(cs.c.cfg.Folder); err == nil {
			m = m2
		}
	}
	return n, nil
}

// ---- applying a step -----------------------------------------------------

func (cs *cycleState) apply(ctx context.Context, a Action, sc *scanResult) error {
	c := cs.c
	abs := func(p string) string { return filepath.Join(c.cfg.Folder, sc.diskPath(p)) }
	switch a.Op {
	case OpRecord:
		e := a.Local
		if e.Kind == None {
			e = a.Remote
		}
		return c.st.SetManifest(a.Path, e, Synced)
	case OpForget:
		return c.st.SetManifest(a.Path, Entry{}, "")
	case OpSkip:
		return c.st.SetManifest(a.Path, a.Remote, Skipped)
	case OpProblem:
		cs.problems = append(cs.problems, Problem{Path: a.Path, Reason: a.Reason})
		return nil

	case OpUpload, OpConflict:
		return cs.upload(ctx, a, abs)
	case OpDelete:
		res, err := c.api.op(ctx, opRequest{Op: "delete", Path: a.Path, BaseHash: a.BaseHash})
		if code := codeOf(err); code == "stale_base" {
			return fmt.Errorf("%w (%s)", errPending, code)
		} else if err != nil {
			return err
		}
		cs.sent(res, "delete", a.Path, "")
		return c.st.SetManifest(a.Path, Entry{}, "")
	case OpMove:
		res, err := c.api.op(ctx, opRequest{Op: "move", Path: a.From, NewPath: a.Path, BaseHash: a.BaseHash})
		switch code := codeOf(err); {
		case code == "stale_base" || code == "not_found" || code == "dest_exists":
			// the server moved on: send the new file, delete the old one
			// (refused if it changed there, and then fetched)
			if err := cs.upload(ctx, Action{Op: OpUpload, Path: a.Path, Local: a.Local}, abs); err != nil {
				return err
			}
			return cs.apply(ctx, Action{Op: OpDelete, Path: a.From, BaseHash: a.BaseHash}, sc)
		case err != nil:
			return err
		}
		cs.sent(res, "move", a.Path, "from "+a.From)
		return c.st.MoveManifest(a.From, a.Path, a.Local)
	case OpMkdir:
		res, err := c.api.op(ctx, opRequest{Op: "mkdir", Path: a.Path})
		if err != nil {
			return err
		}
		cs.sent(res, "mkdir", a.Path, "")
		return c.st.SetManifest(a.Path, Entry{Kind: Dir}, Synced)
	case OpRmdir:
		res, err := c.api.op(ctx, opRequest{Op: "rmdir", Path: a.Path})
		if code := codeOf(err); code == "dir_not_empty" {
			return fmt.Errorf("%w (%s)", errPending, code)
		} else if err != nil {
			return err
		}
		cs.sent(res, "rmdir", a.Path, "")
		return c.st.SetManifest(a.Path, Entry{}, "")

	case OpDownload:
		e, err := cs.download(ctx, a.Path, a.Local, a.Remote, abs(a.Path))
		if err != nil {
			return err
		}
		cs.wrote(a.Path, e)
		cs.received("download", a.Path)
		return c.st.SetManifest(a.Path, e, Synced)
	case OpRemoveLocal:
		dst := abs(a.Path)
		if ok, err := unchanged(dst, a.Local); err != nil {
			return err
		} else if !ok {
			return fmt.Errorf("%w (changed on disk)", errPending)
		}
		if err := os.Remove(dst); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
		cs.wrote(a.Path, Entry{})
		cs.received("remove", a.Path)
		return c.st.SetManifest(a.Path, Entry{}, "")
	case OpRmdirLocal:
		err := os.Remove(abs(a.Path))
		switch {
		case err == nil || errors.Is(err, fs.ErrNotExist):
			cs.wrote(a.Path, Entry{})
			cs.received("rmdir", a.Path)
			return c.st.SetManifest(a.Path, Entry{}, "")
		case isNotEmpty(err):
			// it holds files that are not synchronized (ignored ones):
			// keep it, and do not send it back as new
			cs.log("keep", a.Path, "removed on the server, kept here: holds files that are not synchronized")
			return c.st.SetManifest(a.Path, Entry{Kind: Dir}, LocalOnly)
		default:
			return err
		}
	case OpMkdirLocal:
		dst := abs(a.Path)
		if err := os.MkdirAll(dst, 0o755); err != nil {
			return err
		}
		cs.wrote(a.Path, Entry{Kind: Dir})
		cs.received("mkdir", a.Path)
		return c.st.SetManifest(a.Path, Entry{Kind: Dir}, Synced)
	case OpMoveLocal:
		from, to := abs(a.From), abs(a.Path)
		if ok, err := unchanged(from, a.Local); err != nil {
			return err
		} else if !ok {
			return fmt.Errorf("%w (%s changed on disk)", errPending, a.From)
		}
		if ok, err := unchanged(to, Entry{}); err != nil {
			return err
		} else if !ok {
			return fmt.Errorf("%w (%s appeared on disk)", errPending, a.Path)
		}
		if err := os.MkdirAll(filepath.Dir(to), 0o755); err != nil {
			return err
		}
		if err := os.Rename(from, to); err != nil {
			return err
		}
		e, err := settle(to, a.Remote)
		if err != nil {
			return err
		}
		cs.wrote(a.From, Entry{})
		cs.wrote(a.Path, e)
		cs.res.Received++
		cs.log("↓ move", a.Path, "from "+a.From)
		return c.st.MoveManifest(a.From, a.Path, e)
	}
	return fmt.Errorf("unknown step %q", a.Op)
}

func (cs *cycleState) sent(res *result, op, p, detail string) {
	if res.Status == "unchanged" {
		return
	}
	cs.res.Sent++
	cs.log("↑ "+op, p, detail)
}

func (cs *cycleState) received(op, p string) {
	cs.res.Received++
	cs.log("↓ "+op, p, "")
}

// upload sends a local file. For a conflict (and whenever the server
// answers with one) the local file becomes the conflict copy the server
// made of it — renamed here, no transfer back — and the server's version
// is fetched to the original name.
func (cs *cycleState) upload(ctx context.Context, a Action, abs func(string) string) error {
	c := cs.c
	src := abs(a.Path)
	ann, err := c.api.announce(ctx, announceRequest{Path: a.Path, BaseHash: a.BaseHash, NewHash: a.Local.Hash, Size: a.Local.Size, Mtime: a.Local.Mtime})
	if err != nil {
		return err
	}
	res := &result{Status: "unchanged"}
	if ann.Status != "unchanged" {
		if err := c.tr.Send(ctx, transport.Upload{ShareID: c.cfg.ShareID, UploadID: ann.UploadID, Dest: ann.Dest}, src); err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				return fmt.Errorf("%w (removed on disk)", errPending)
			}
			return err
		}
		if res, err = c.api.commit(ctx, ann.UploadID); err != nil {
			if code := codeOf(err); code == "hash_mismatch" || code == "size_mismatch" {
				return fmt.Errorf("%w (changed on disk while sending)", errPending)
			}
			return err
		}
	}
	if res.Status != "conflict" {
		cs.sent(res, "put", a.Path, "")
		return c.st.SetManifest(a.Path, a.Local, Synced)
	}
	cs.res.Conflicts++
	cs.log("↑ conflict", a.Path, "kept by the server as "+res.ConflictPath)
	// The server holds our content as res.ConflictPath now.
	cp := abs(res.ConflictPath)
	if ok, err := unchanged(src, a.Local); err != nil || !ok {
		return err // edited again: both arrive by download next round
	}
	if ok, err := unchanged(cp, Entry{}); err != nil || !ok {
		return err
	}
	if err := os.Rename(src, cp); err != nil {
		return err
	}
	cs.wrote(a.Path, Entry{})
	cs.wrote(res.ConflictPath, a.Local)
	if err := c.st.MoveManifest(a.Path, res.ConflictPath, a.Local); err != nil {
		return err
	}
	if a.Op != OpConflict || a.Remote.Kind != File {
		return nil // the server's version comes with the next round
	}
	e, err := cs.download(ctx, a.Path, Entry{}, a.Remote, src)
	if err != nil {
		if errors.Is(err, errPending) {
			return nil
		}
		return err
	}
	cs.wrote(a.Path, e)
	cs.received("download", a.Path)
	return c.st.SetManifest(a.Path, e, Synced)
}

// download fetches the server's version r of p into dst: into a
// .shf-tmp-* file in the same directory, mtime set to the server's, then
// an atomic rename — but only if dst still is what the plan saw (local).
// It returns the entry to record.
func (cs *cycleState) download(ctx context.Context, p string, local, r Entry, dst string) (Entry, error) {
	c := cs.c
	if ok, err := unchanged(dst, local); err != nil {
		return Entry{}, err
	} else if !ok {
		return Entry{}, fmt.Errorf("%w (changed on disk)", errPending)
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return Entry{}, err
	}
	var rnd [8]byte
	rand.Read(rnd[:])
	tmp := filepath.Join(filepath.Dir(dst), ignore.ClientTempPrefix+"dl-"+hex.EncodeToString(rnd[:]))
	defer os.Remove(tmp)
	if err := c.tr.Fetch(ctx, c.cfg.ShareID, p, r.Hash, tmp); err != nil {
		if errors.Is(err, transport.ErrChanged) {
			return Entry{}, fmt.Errorf("%w (replaced on the server)", errPending)
		}
		return Entry{}, err
	}
	if r.Mtime > 0 {
		t := time.Unix(0, r.Mtime)
		if err := os.Chtimes(tmp, t, t); err != nil {
			return Entry{}, err
		}
	}
	if ok, err := unchanged(dst, local); err != nil {
		return Entry{}, err
	} else if !ok {
		return Entry{}, fmt.Errorf("%w (changed on disk during the download)", errPending)
	}
	if err := os.Rename(tmp, dst); err != nil {
		return Entry{}, err
	}
	return settle(dst, r)
}

// settle returns the entry to record for a file now at abs with the
// server's content r: its hash, and size and mtime as the disk has them.
func settle(abs string, r Entry) (Entry, error) {
	fi, err := os.Lstat(abs)
	if err != nil {
		return Entry{}, err
	}
	return Entry{Kind: File, Hash: r.Hash, Size: fi.Size(), Mtime: fi.ModTime().UnixNano()}, nil
}

func isNotEmpty(err error) bool {
	return errors.Is(err, syscall.ENOTEMPTY) || errors.Is(err, syscall.EEXIST)
}
