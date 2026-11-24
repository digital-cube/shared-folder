package client

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/digital-cube/shared-folder/internal/ignore"
	"github.com/digital-cube/shared-folder/internal/transport/rsyncssh"
)

var shareIDRE = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

// DefaultSSHUser is the server's system user when neither the server nor
// the command line names one (docs/server-ssh.md).
const DefaultSSHUser = "shfd"

// JoinRequest are the arguments of shf join.
type JoinRequest struct {
	Home    string // the state home (StateHome)
	URL     string // the server's API, https://host[:port]
	ShareID string
	Token   string
	Folder  string
	// SSH overrides the endpoint for rsync over SSH. Unset fields come from
	// the server (shfd serve --ssh-endpoint), then from defaults: the host
	// of URL, port 22, user "shfd".
	SSH SSH
}

// Join connects a local folder to a shared folder: it checks the token
// with the server, creates the member's SSH key and registers it,
// remembers the server's host key, writes the local state and runs the
// first cycle. A folder that already has files is merged, never cleaned:
// files only here are sent, files only on the server are fetched, equal
// ones are recorded and different ones become conflict copies.
//
// When the first cycle fails the folder stays joined; Join returns the
// open client together with the error, and shf sync continues.
func Join(ctx context.Context, req JoinRequest, opts Options) (*Client, *Result, error) {
	if !shareIDRE.MatchString(req.ShareID) {
		return nil, nil, fmt.Errorf("invalid share id %q: want a UUID as printed by shfd share create", req.ShareID)
	}
	u, err := url.Parse(req.URL)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
		return nil, nil, fmt.Errorf("invalid server URL %q: want https://host[:port]", req.URL)
	}
	if strings.TrimSpace(req.Token) == "" {
		return nil, nil, errors.New("empty token")
	}
	if err := os.MkdirAll(req.Folder, 0o755); err != nil {
		return nil, nil, err
	}
	folder, err := absClean(req.Folder)
	if err != nil {
		return nil, nil, err
	}
	if fi, err := os.Stat(folder); err != nil || !fi.IsDir() {
		return nil, nil, fmt.Errorf("%s is not a directory", folder)
	}
	joined, err := Joined(req.Home)
	if err != nil {
		return nil, nil, err
	}
	for _, c := range joined {
		switch {
		case c.ShareID == req.ShareID && c.Folder == folder:
			// Joining again: the token is checked anew and this device's
			// key registered again (another device of the same member
			// replaced it); the folder's state is kept.
		case c.ShareID == req.ShareID:
			return nil, nil, fmt.Errorf("share %s is already joined at %s", req.ShareID, c.Folder)
		case within(folder, c.Folder) || within(c.Folder, folder):
			return nil, nil, fmt.Errorf("%s overlaps %s, which is joined to share %s; one folder on disk holds one shared folder", folder, c.Folder, c.ShareID)
		}
	}
	if within(req.Home, folder) {
		return nil, nil, fmt.Errorf("the client's state directory %s must not be inside the shared folder", req.Home)
	}

	a := newAPI(u.String(), req.ShareID, req.Token, opts.HTTPClient)
	if _, err := a.head(ctx); err != nil {
		if codeOf(err) == "unauthorized" {
			return nil, nil, errors.New("the server refused the token for this folder (wrong, revoked, or another folder's)")
		}
		return nil, nil, fmt.Errorf("contacting %s: %w", u.Redacted(), err)
	}

	cfg := &Config{URL: strings.TrimRight(u.String(), "/"), ShareID: req.ShareID, Folder: folder, Token: req.Token,
		Dir: filepath.Join(req.Home, req.ShareID)}
	if err := os.MkdirAll(cfg.Dir, 0o700); err != nil {
		return nil, nil, err
	}
	if opts.Transport == nil {
		if cfg.SSH, err = setupSSH(ctx, a, cfg, u, req.SSH); err != nil {
			return nil, nil, err
		}
	}
	cfg.Joined = time.Now().UTC()
	if err := cfg.save(); err != nil {
		return nil, nil, err
	}
	c, err := Open(cfg, opts)
	if err != nil {
		return nil, nil, err
	}
	// the key was just registered (and, over SSH, accepted)
	if err := clearKeyRefused(c.st.db); err != nil {
		c.Close()
		return nil, nil, err
	}
	res, err := c.Sync(ctx)
	return c, res, err
}

// setupSSH creates the member's key (or reuses the one an interrupted
// join left), registers it, settles the endpoint and connects once, which
// remembers the server's host key and proves the key is accepted.
func setupSSH(ctx context.Context, a *api, cfg *Config, u *url.URL, over SSH) (*SSH, error) {
	var pub string
	if b, err := os.ReadFile(cfg.KeyFile() + ".pub"); err == nil {
		pub = strings.TrimSpace(string(b))
	} else {
		host, _ := os.Hostname()
		if pub, err = rsyncssh.GenerateKey(ctx, cfg.KeyFile(), "shf@"+host+" "+cfg.ShareID); err != nil {
			return nil, fmt.Errorf("creating the SSH key: %w", err)
		}
	}
	os.Chmod(cfg.KeyFile(), 0o600)
	res, err := a.setSSHKey(ctx, pub)
	if err != nil {
		return nil, fmt.Errorf("registering the SSH key: %w", err)
	}
	ep := &SSH{User: over.User, Host: over.Host, Port: over.Port}
	if ep.User == "" {
		ep.User = res.SSH.User
	}
	if ep.Host == "" {
		ep.Host = res.SSH.Host
	}
	if ep.Port == 0 {
		ep.Port = res.SSH.Port
	}
	if ep.User == "" {
		ep.User = DefaultSSHUser
	}
	if ep.Host == "" {
		ep.Host = u.Hostname()
	}
	if ep.Port == 0 {
		ep.Port = 22
	}
	tr, err := rsyncssh.New(rsyncssh.Config{ShareID: cfg.ShareID, Host: ep.Host, Port: ep.Port, User: ep.User,
		KeyFile: cfg.KeyFile(), StateDir: cfg.Dir})
	if err != nil {
		return nil, err
	}
	if err := tr.Probe(ctx); err != nil {
		return nil, fmt.Errorf("SSH to %s@%s: %w", ep.User, net.JoinHostPort(ep.Host, fmt.Sprint(ep.Port)), err)
	}
	return ep, nil
}

// Status is what shf status shows.
type Status struct {
	Config    *Config
	Last      *Cycle // nil before the first cycle
	Seq       int64  // last applied seq
	RemoteSeq int64
	Head      int64 // the server's HEAD (as last seen when HeadErr is set)
	HeadErr   error
	// Pending counts the steps a cycle would take now; -1 when unknown
	// (PendingErr says why).
	Pending    int
	PendingErr error
	Problems   []Problem
	Busy       bool // a cycle is running
	// Held are the deletions the mass-delete guard holds back; Blocked
	// says some of them wait for shf confirm or shf restore-local.
	Held    []HeldDelete
	Blocked bool
	// KeyRefusedSince is set while the server refuses this device's SSH
	// key (the last cycles failed with it; KeyRefusedHint says what to do).
	KeyRefusedSince time.Time
}

// Status reports the folder's state without changing anything: it asks
// the server for new changes, scans the folder and plans, but applies and
// stores nothing.
func (c *Client) Status(ctx context.Context) (*Status, error) {
	s := &Status{Config: c.cfg, Pending: -1}
	var err error
	if s.Seq, err = c.st.Seq(); err != nil {
		return nil, err
	}
	if s.RemoteSeq, err = c.st.metaInt(metaRemoteSeq); err != nil {
		return nil, err
	}
	if s.Head, err = c.st.metaInt(metaHead); err != nil {
		return nil, err
	}
	cs, err := c.st.Cycles(1)
	if err != nil {
		return nil, err
	}
	if len(cs) > 0 {
		s.Last = &cs[0]
	}
	if s.Problems, err = c.st.Problems(); err != nil {
		return nil, err
	}
	if s.Held, err = c.st.HeldDeletes(); err != nil {
		return nil, err
	}
	if s.KeyRefusedSince, err = c.st.KeyRefusedSince(); err != nil {
		return nil, err
	}
	for _, h := range s.Held {
		s.Blocked = s.Blocked || !h.Confirmed
	}
	if unlock, err := lock(c.cfg.Dir); errors.Is(err, ErrBusy) {
		s.Busy = true
	} else if err == nil {
		unlock()
	}

	remote, err := c.st.Remote()
	if err != nil {
		return nil, err
	}
	evs, head, err := c.api.changes(ctx, s.RemoteSeq)
	if err != nil {
		s.HeadErr = err
		s.PendingErr = fmt.Errorf("server not reachable: %w", err)
		return s, nil
	}
	s.Head = head
	applyEvents(remote, evs)
	n, err := c.pending(remote, s.Held)
	if err != nil {
		s.PendingErr = err
		return s, nil
	}
	s.Pending = n
	return s, nil
}

func (c *Client) pending(remote map[string]Entry, held []HeldDelete) (int, error) {
	isHeld := map[string]bool{}
	for _, h := range held {
		isHeld[h.Path] = true
	}
	manifest, err := c.st.Manifest()
	if err != nil {
		return 0, err
	}
	m, err := ignore.FromDir(c.cfg.Folder)
	if err != nil {
		return 0, err
	}
	sc, err := scan(c.cfg.Folder, m, manifest)
	if err != nil {
		return 0, err
	}
	acts := Plan(PlanInput{Manifest: manifest, Local: sc.Local, Remote: remote,
		Ignored: func(p string, isDir bool) bool { return sc.untracked(p) || m.Match(p, isDir) }})
	n := 0
	for _, a := range acts {
		if !a.IsBookkeeping() && !(isDeletion(a) && isHeld[a.Path]) {
			n++
		}
	}
	return n, nil
}
