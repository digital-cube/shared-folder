package daemon

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/digital-cube/shared-folder/internal/client"
	"github.com/digital-cube/shared-folder/internal/transport/rsyncssh"
)

// folder runs one joined folder.
type folder struct {
	cfg  Config
	fc   *client.Config
	set  client.Settings
	log  *slog.Logger
	wake chan struct{} // cap 1: something changed, look again

	mu        sync.Mutex
	head      int64                // newest HEAD heard (WebSocket or poll)
	wsUp      bool                 // the WebSocket is connected
	dirty     map[string]time.Time // local paths changed since the last cycle, by first sight
	lastLocal time.Time            // newest local change
	selfw     map[string]selfWrite // what the cycles wrote on disk
}

// selfWrite is a path a cycle changed, and what it left there.
type selfWrite struct {
	e  client.Entry
	at time.Time
}

// selfWriteTTL: a cycle's own write is recognised this long.
const selfWriteTTL = 5 * time.Minute

func newFolder(cfg Config, fc *client.Config) *folder {
	return &folder{
		cfg: cfg, fc: fc, set: cfg.Settings,
		log:   cfg.Log.With("folder", fc.Folder, "share", fc.ShareID),
		wake:  make(chan struct{}, 1),
		dirty: map[string]time.Time{}, selfw: map[string]selfWrite{},
	}
}

func (f *folder) poke() {
	select {
	case f.wake <- struct{}{}:
	default:
	}
}

// heardHead records a HEAD from the server.
func (f *folder) heardHead(h int64) {
	f.mu.Lock()
	if h > f.head {
		f.head = h
	}
	f.mu.Unlock()
	f.poke()
}

func (f *folder) setWSUp(up bool) {
	f.mu.Lock()
	f.wsUp = up
	f.mu.Unlock()
}

func (f *folder) isWSUp() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.wsUp
}

// changed records a local change of p (a slash path below the root).
func (f *folder) changed(p string) {
	now := time.Now()
	f.mu.Lock()
	if _, ok := f.dirty[p]; !ok {
		f.dirty[p] = now
	}
	f.lastLocal = now
	f.mu.Unlock()
	f.poke()
}

// onWrite is client.Options.OnWrite: a cycle changed p on disk.
func (f *folder) onWrite(p string, e client.Entry) {
	f.mu.Lock()
	f.selfw[p] = selfWrite{e: e, at: time.Now()}
	f.mu.Unlock()
}

// isOwnWrite reports whether the disk at p still is what a cycle left
// there.
func (f *folder) isOwnWrite(p string, now time.Time) bool {
	sw, ok := f.selfw[p]
	if !ok || now.Sub(sw.at) > selfWriteTTL {
		return false
	}
	fi, err := os.Lstat(filepath.Join(f.fc.Folder, filepath.FromSlash(p)))
	switch {
	case errors.Is(err, os.ErrNotExist):
		return sw.e.Kind == client.None
	case err != nil:
		return false
	case sw.e.Kind == client.Dir:
		return fi.IsDir()
	case sw.e.Kind == client.File:
		return fi.Mode().IsRegular() && fi.Size() == sw.e.Size && fi.ModTime().UnixNano() == sw.e.Mtime
	}
	return false
}

// localDue says whether the local changes call for a cycle now: the last
// one is Debounce old (or the first is ten times that, so a folder that
// never stops changing is still synchronized). Changes the cycles made
// themselves are dropped. When not due yet, at is when to look again.
func (f *folder) localDue(now time.Time) (due bool, at time.Time) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for p, sw := range f.selfw {
		if now.Sub(sw.at) > selfWriteTTL {
			delete(f.selfw, p)
		}
	}
	if len(f.dirty) == 0 {
		return false, time.Time{}
	}
	deb := time.Duration(f.set.Debounce)
	first := now
	for _, t := range f.dirty {
		if t.Before(first) {
			first = t
		}
	}
	at = f.lastLocal.Add(deb)
	if limit := first.Add(10 * deb); limit.Before(at) {
		at = limit
	}
	if now.Before(at) {
		return false, at
	}
	for p := range f.dirty {
		if f.isOwnWrite(p, now) {
			delete(f.dirty, p)
		}
	}
	if len(f.dirty) == 0 {
		return false, time.Time{}
	}
	return true, time.Time{}
}

func (f *folder) clearDirty() {
	f.mu.Lock()
	clear(f.dirty)
	f.mu.Unlock()
}

// run is the folder's loop: the only place its cycles run.
func (f *folder) run(hard context.Context, stop <-chan struct{}) {
	opts := f.cfg.Options
	opts.Settle = time.Duration(f.set.Settle)
	if opts.MassDelete == nil {
		md := f.set.MassDelete
		opts.MassDelete = &md
	}
	opts.OnWrite = f.onWrite
	if opts.BwLimit == 0 {
		opts.BwLimit = f.set.BwLimit
	}
	if opts.OnTransfer == nil {
		opts.OnTransfer = func(st rsyncssh.Stats) {
			f.log.Debug("transfer", "direction", st.Direction, "path", st.Path, "size", st.Size,
				"sent", st.Literal, "matched", st.Matched)
		}
	}
	c, err := client.Open(f.fc, opts)
	if err != nil {
		f.log.Error("cannot open the folder's state; not synchronizing it", "err", err)
		return
	}
	defer c.Close()

	ctx, cancel := context.WithCancel(hard)
	defer cancel()
	go func() {
		select {
		case <-stop:
			cancel() // background work only; a cycle runs with hard
		case <-ctx.Done():
		}
	}()
	var bg sync.WaitGroup
	bg.Add(3)
	go func() { defer bg.Done(); f.watch(ctx) }()
	go func() { defer bg.Done(); f.websocket(ctx) }()
	go func() { defer bg.Done(); f.poll(ctx, c) }()
	defer bg.Wait()

	f.log.Info("serving folder")
	var (
		nextFull  = time.Now() // a full comparison at start
		retryAt   time.Time
		failures  int
		wasErr    string
		wasBlock  bool
		firstFull = true
	)
	timer := time.NewTimer(time.Hour)
	defer timer.Stop()
	for {
		now := time.Now()
		reason := ""
		localDue, localAt := f.localDue(now)
		seq, _ := c.State().Seq()
		f.mu.Lock()
		remoteDue := f.head > seq
		f.mu.Unlock()
		switch {
		case !now.Before(nextFull):
			reason = ReasonFull
			if firstFull {
				reason = ReasonStart
			}
		case !retryAt.IsZero() && !now.Before(retryAt):
			reason = ReasonRetry
		case remoteDue:
			reason = ReasonRemote
		case localDue:
			reason = ReasonLocal
		}
		if reason != "" {
			select {
			case <-stop:
				return
			default:
			}
			full := reason == ReasonFull || reason == ReasonStart
			f.clearDirty() // changes from now on call for the next cycle
			res, err := c.SyncWith(hard, client.SyncOptions{Full: full, Stop: stop})
			if f.cfg.OnCycle != nil {
				f.cfg.OnCycle(f.fc.ShareID, reason, res, err)
			}
			retryAt = time.Time{}
			switch {
			case errors.Is(err, client.ErrStopped):
				return
			case errors.Is(err, client.ErrBusy):
				// shf sync, confirm or restore-local holds the folder
				retryAt = time.Now().Add(time.Second)
			case err != nil:
				failures++
				retryAt = time.Now().Add(backoff(failures, time.Duration(f.set.RetryMax)))
				if msg := err.Error(); msg != wasErr {
					f.log.Error("cycle failed; will retry", "reason", reason, "err", err, "retry_in", time.Until(retryAt).Round(time.Second))
					wasErr = msg
				}
				if full {
					// keep the full comparison due until one succeeds
					nextFull = retryAt
				}
			default:
				if wasErr != "" {
					f.log.Info("synchronizing again")
				}
				failures, wasErr = 0, ""
				if full {
					firstFull = false
					nextFull = time.Now().Add(time.Duration(f.set.FullSync))
				}
				if !res.RetryAt.IsZero() {
					retryAt = res.RetryAt.Add(50 * time.Millisecond)
				}
				if res.Sent+res.Received+res.Conflicts > 0 {
					f.log.Info("synchronized", "reason", reason, "sent", res.Sent, "received", res.Received,
						"conflicts", res.Conflicts, "seq", res.ToSeq)
				}
				for _, p := range res.Problems {
					f.log.Warn("not synchronized", "path", p.Path, "reason", p.Reason)
				}
			}
			if res != nil && res.Blocked != wasBlock {
				wasBlock = res.Blocked
				if wasBlock {
					f.log.Warn("mass deletion held back: the folder is blocked; see shf status, then shf confirm or shf restore-local")
				} else {
					f.log.Info("no deletions held back any more")
				}
			}
			continue // look again at once: requests that came during the cycle
		}

		next := nextFull
		for _, t := range []time.Time{retryAt, localAt} {
			if !t.IsZero() && t.Before(next) {
				next = t
			}
		}
		timer.Reset(max(time.Until(next), time.Millisecond))
		select {
		case <-stop:
			return
		case <-f.wake:
		case <-timer.C:
		}
		if !timer.Stop() {
			select {
			case <-timer.C:
			default:
			}
		}
	}
}

// backoff is the pause before retry n (1, 2, ...): 2 s doubling up to
// limit.
func backoff(n int, limit time.Duration) time.Duration {
	d := 2 * time.Second
	for i := 1; i < n && d < limit; i++ {
		d *= 2
	}
	return min(d, limit)
}

// poll asks the server for its HEAD every Settings.Poll while the
// WebSocket is down.
func (f *folder) poll(ctx context.Context, c *client.Client) {
	t := time.NewTicker(time.Duration(f.set.Poll))
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		if f.isWSUp() {
			continue
		}
		hctx, cancel := context.WithTimeout(ctx, time.Duration(f.set.Poll))
		h, err := c.Head(hctx)
		cancel()
		if err != nil {
			if ctx.Err() == nil {
				f.log.Debug("poll failed", "err", err)
			}
			continue
		}
		f.heardHead(h)
	}
}
