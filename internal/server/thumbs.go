package server

import (
	"errors"
	"io/fs"
	"os"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/digital-cube/shared-folder/internal/thumbs"
)

// Thumbnails, the serve side (docs/specifikacija.md, section 3,
// "Thumbnails"). shfd thumbs makes them in a process of its own and never
// opens a database: it asks this server for jobs over admin.sock, reads the
// sources from storage/, writes only thumbnails/ and reports each outcome.
// The server keeps, per folder, the set of hashes whose thumbnail is ready
// and the set that failed, read once from thumbnails/ on first use and
// then kept up to date from those reports, so listing a directory never
// stats a thumbnail.

// ThumbPollWindow is how long after its last request for jobs the
// thumbnail service counts as running: within it, a file that has no
// thumbnail yet is "pending" in GET /admin/files.
const ThumbPollWindow = 60 * time.Second

// ThumbSkipDelay is how long a hash the service skipped (its file was not
// what the job said: changed, moved or gone meanwhile) is not handed out
// again. A real change brings a new hash at once; this only keeps a file
// that never matches its row from being offered in a loop.
const ThumbSkipDelay = 5 * time.Minute

// MaxThumbHashes bounds the hashes of one unreferenced request.
const MaxThumbHashes = 1000

// Thumbnail states of a FileEntry.
const (
	ThumbReady   = "ready"
	ThumbPending = "pending"
	ThumbFailed  = "failed"
	ThumbNone    = ""
)

// ThumbJob is one thumbnail to make: a file of a folder.
type ThumbJob struct {
	ShareID string      `json:"share_id"`
	Hash    string      `json:"hash"`
	Path    string      `json:"path"` // folder path; the file is storage/<path>
	Size    int64       `json:"size"`
	Kind    thumbs.Kind `json:"kind"`
}

// ThumbJobs is the body of GET /v1/thumbs/jobs.
type ThumbJobs struct {
	Jobs []ThumbJob `json:"jobs"`
	// Pending counts every hash still waiting, in all folders, of which
	// Jobs is the first page.
	Pending int `json:"pending"`
}

// ThumbResult is the outcome of one job, as the service reports it.
type ThumbResult struct {
	Hash string `json:"hash"`
	OK   bool   `json:"ok"`
	// Error is the reason of a failure (the text of its .fail marker).
	Error string `json:"error,omitempty"`
	// Skipped says the job was not done and left no trace: the file was
	// not what the job said. It is not handed out again for
	// ThumbSkipDelay.
	Skipped bool `json:"skipped,omitempty"`
}

// thumbService is what the server knows of the thumbnail service: when it
// last asked for jobs and of which kinds.
type thumbService struct {
	mu    sync.Mutex
	at    time.Time
	kinds map[thumbs.Kind]bool
}

func (p *thumbService) record(now time.Time, kinds []thumbs.Kind) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.at = now
	p.kinds = map[thumbs.Kind]bool{}
	for _, k := range kinds {
		p.kinds[k] = true
	}
}

// serving reports whether a service that makes kind asked for jobs within
// ThumbPollWindow.
func (p *thumbService) serving(kind thumbs.Kind, now time.Time) bool {
	if p == nil {
		return false
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.kinds[kind] && !p.at.IsZero() && now.Sub(p.at) <= ThumbPollWindow
}

// ThumbServiceSeen is when the thumbnail service last asked for jobs and
// with which kinds; the zero time when it never did.
func (s *Server) ThumbServiceSeen() (time.Time, []thumbs.Kind) {
	p := &s.thumbSvc
	p.mu.Lock()
	defer p.mu.Unlock()
	var ks []thumbs.Kind
	for _, k := range thumbs.AllKinds {
		if p.kinds[k] {
			ks = append(ks, k)
		}
	}
	return p.at, ks
}

// thumbIndex is a folder's view of its thumbnails/.
type thumbIndex struct {
	mu       sync.Mutex
	loaded   bool
	ready    map[string]bool
	failed   map[string]bool
	deferred map[string]time.Time // skipped hashes and until when
	gen      uint64               // bumped by every change of the sets
	// idle remembers a scan of the files that found nothing to do, so
	// the service's poll every 2 s costs one query while nothing changes.
	idle      idleKey
	idleValid bool
}

type idleKey struct {
	head  int64
	gen   uint64
	kinds string
}

// ThumbStore is the folder's thumbnails directory.
func (s *Share) ThumbStore() thumbs.Store { return thumbs.NewStore(s.dir) }

// thumbsLocked loads the index on first use; t.mu is held.
func (s *Share) thumbsLocked() error {
	t := &s.thumbs
	if t.loaded {
		return nil
	}
	ready, failed, err := s.ThumbStore().Scan()
	if err != nil {
		return err
	}
	t.ready, t.failed, t.loaded = ready, failed, true
	if t.deferred == nil {
		t.deferred = map[string]time.Time{}
	}
	t.gen++
	return nil
}

// resetThumbs forgets the index; it is read again on next use.
func (s *Share) resetThumbs() {
	s.thumbs.mu.Lock()
	defer s.thumbs.mu.Unlock()
	s.thumbs.loaded, s.thumbs.ready, s.thumbs.failed = false, nil, nil
	s.thumbs.gen++
}

// thumbState is the thumb field of a listed entry.
func (s *Share) thumbState(e *FileEntry, now time.Time) string {
	if e.Kind != "file" || e.Size <= 0 {
		return ThumbNone
	}
	kind := thumbs.KindOf(e.Name)
	if kind == "" {
		return ThumbNone
	}
	switch {
	case s.thumbs.ready[e.Hash]:
		return ThumbReady
	case s.thumbs.failed[e.Hash]:
		return ThumbFailed
	case s.thumbSvc.serving(kind, now):
		return ThumbPending
	}
	return ThumbNone
}

// setThumbStates fills the thumb field of listed entries.
func (s *Share) setThumbStates(es []FileEntry) error {
	s.thumbs.mu.Lock()
	defer s.thumbs.mu.Unlock()
	if err := s.thumbsLocked(); err != nil {
		return err
	}
	now := s.now()
	for i := range es {
		es[i].Thumb = s.thumbState(&es[i], now)
	}
	return nil
}

// thumbJobs lists the folder's files that need a thumbnail of one of
// kinds, one per hash, newest change first, up to limit; and how many
// hashes wait in all.
func (s *Share) thumbJobs(kinds map[thumbs.Kind]bool, limit int, now time.Time) ([]ThumbJob, int, error) {
	var head int64
	if err := s.db.QueryRow(`SELECT coalesce(max(seq), 0) FROM events`).Scan(&head); err != nil {
		return nil, 0, err
	}
	var ks []string
	for k := range kinds {
		ks = append(ks, string(k))
	}
	sort.Strings(ks)
	t := &s.thumbs
	t.mu.Lock()
	if err := s.thumbsLocked(); err != nil {
		t.mu.Unlock()
		return nil, 0, err
	}
	key := idleKey{head: head, gen: t.gen, kinds: strings.Join(ks, ",")}
	if t.idleValid && t.idle == key {
		t.mu.Unlock()
		return nil, 0, nil
	}
	// The sets are copied so the scan runs without the lock.
	ready, failed := len(t.ready), len(t.failed)
	skip := make(map[string]bool, ready+failed+len(t.deferred))
	for h := range t.ready {
		skip[h] = true
	}
	for h := range t.failed {
		skip[h] = true
	}
	deferred := false
	for h, until := range t.deferred {
		if now.Before(until) {
			skip[h], deferred = true, true
		} else {
			delete(t.deferred, h)
		}
	}
	t.mu.Unlock()

	rows, err := s.db.Query(`SELECT path, hash, size FROM files WHERE kind = 'file' AND size > 0 ORDER BY seq DESC`)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	var jobs []ThumbJob
	pending := 0
	for rows.Next() {
		var j ThumbJob
		if err := rows.Scan(&j.Path, &j.Hash, &j.Size); err != nil {
			return nil, 0, err
		}
		j.Kind = thumbs.KindOf(j.Path)
		if j.Kind == "" || !kinds[j.Kind] || skip[j.Hash] || !thumbs.ValidHash(j.Hash) {
			continue
		}
		skip[j.Hash] = true
		pending++
		if len(jobs) < limit {
			j.ShareID = s.ID
			jobs = append(jobs, j)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, 0, err
	}
	if pending == 0 && !deferred {
		t.mu.Lock()
		if t.gen == key.gen {
			t.idle, t.idleValid = key, true
		}
		t.mu.Unlock()
	}
	return jobs, pending, nil
}

// ThumbJobs hands out up to limit thumbnail jobs of the given kinds,
// taking turns between the folders so a large backlog in one does not
// hold up another; within a folder the newest change comes first, so a
// new upload gets its thumbnail while old files are still being filled
// in. It records the request: GET /admin/files says "pending" only while
// a service asks. rescan drops every folder's index first (the service
// asks for it when it starts, after it turned crashed .work markers into
// .fail ones or removed .fail markers).
func (s *Server) ThumbJobs(kinds []thumbs.Kind, limit int, rescan bool) (*ThumbJobs, error) {
	now := s.opts.Now()
	s.thumbSvc.record(now, kinds)
	shares, err := s.Shares()
	if err != nil {
		return nil, err
	}
	want := map[thumbs.Kind]bool{}
	for _, k := range kinds {
		want[k] = true
	}
	out := &ThumbJobs{Jobs: []ThumbJob{}}
	var per [][]ThumbJob
	for _, si := range shares {
		sh, err := s.Share(si.ID)
		if err != nil {
			return nil, err
		}
		if rescan {
			sh.resetThumbs()
		}
		if len(want) == 0 {
			continue
		}
		jobs, n, err := sh.thumbJobs(want, limit, now)
		if err != nil {
			return nil, err
		}
		out.Pending += n
		if len(jobs) > 0 {
			per = append(per, jobs)
		}
	}
	for i := 0; len(out.Jobs) < limit; i++ {
		added := false
		for _, jobs := range per {
			if i < len(jobs) && len(out.Jobs) < limit {
				out.Jobs = append(out.Jobs, jobs[i])
				added = true
			}
		}
		if !added {
			break
		}
	}
	return out, nil
}

// ThumbsDone takes the service's outcomes into the index.
func (s *Share) ThumbsDone(results []ThumbResult) error {
	for _, r := range results {
		if !thumbs.ValidHash(r.Hash) {
			return errf(CodeInvalidRequest, "invalid hash %q", r.Hash)
		}
	}
	t := &s.thumbs
	t.mu.Lock()
	defer t.mu.Unlock()
	if err := s.thumbsLocked(); err != nil {
		return err
	}
	now := s.now()
	for _, r := range results {
		switch {
		case r.OK:
			t.ready[r.Hash] = true
			delete(t.failed, r.Hash)
			delete(t.deferred, r.Hash)
		case r.Skipped:
			t.deferred[r.Hash] = now.Add(ThumbSkipDelay)
		default:
			t.failed[r.Hash] = true
			delete(t.ready, r.Hash)
		}
	}
	t.gen++
	return nil
}

// UnreferencedThumbs returns those of hashes that no file and no trash
// entry that is not purged holds, and drops them from the index: the
// service deletes their thumbnails next. A hash that comes back later (an
// upload of the same content) is then a job again.
func (s *Share) UnreferencedThumbs(hashes []string) ([]string, error) {
	if len(hashes) > MaxThumbHashes {
		return nil, errf(CodeInvalidRequest, "at most %d hashes per request", MaxThumbHashes)
	}
	for _, h := range hashes {
		if !thumbs.ValidHash(h) {
			return nil, errf(CodeInvalidRequest, "invalid hash %q", h)
		}
	}
	out := []string{}
	if len(hashes) == 0 {
		return out, nil
	}
	// The writer lock keeps a commit from adding a reference between the
	// check and the index update.
	s.mu.Lock()
	defer s.mu.Unlock()
	held := map[string]bool{}
	args := make([]any, len(hashes))
	for i, h := range hashes {
		args[i] = h
	}
	in := "(" + strings.TrimSuffix(strings.Repeat("?,", len(hashes)), ",") + ")"
	for _, q := range []string{
		`SELECT DISTINCT hash FROM files WHERE kind = 'file' AND hash IN ` + in,
		`SELECT DISTINCT hash FROM trash WHERE purged_at IS NULL AND hash IN ` + in,
	} {
		rows, err := s.db.Query(q, args...)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var h string
			if err := rows.Scan(&h); err != nil {
				rows.Close()
				return nil, err
			}
			held[h] = true
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return nil, err
		}
	}
	for _, h := range hashes {
		if !held[h] && !slices.Contains(out, h) {
			out = append(out, h)
		}
	}
	t := &s.thumbs
	t.mu.Lock()
	for _, h := range out {
		delete(t.ready, h)
		delete(t.failed, h)
		delete(t.deferred, h)
	}
	t.gen++
	t.mu.Unlock()
	return out, nil
}

// OpenThumb opens one size of the thumbnail of hash, only from this
// folder's thumbnails/. hash and size are checked before the disk is
// touched; a missing thumbnail is CodeNotFound.
func (s *Share) OpenThumb(hash, size string) (*os.File, os.FileInfo, error) {
	if !thumbs.ValidHash(hash) {
		return nil, nil, errf(CodeInvalidRequest, "invalid thumbnail hash")
	}
	if !thumbs.ValidSize(size) {
		return nil, nil, errf(CodeInvalidRequest, "unknown thumbnail size %q", size)
	}
	f, err := os.Open(s.ThumbStore().Path(hash, size))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil, errf(CodeNotFound, "no thumbnail")
	}
	if err != nil {
		return nil, nil, err
	}
	fi, err := f.Stat()
	if err != nil || !fi.Mode().IsRegular() {
		f.Close()
		return nil, nil, errf(CodeNotFound, "no thumbnail")
	}
	return f, fi, nil
}
