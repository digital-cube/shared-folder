package server

import (
	"context"
	"errors"
	"sort"
	"sync"
	"sync/atomic"
)

// ErrStuck is returned by Share.Probe when the folder's writer did not
// answer in time: its database did not respond to a read, or its writer
// lock did not come free. It is how a writer hanging in the kernel (an NFS
// client that does not finish I/O under SQLite's lock) shows up.
var ErrStuck = errors.New("writer stuck")

// probes keeps at most one check of each kind running per folder, so a
// health check polled while a folder hangs does not pile up goroutines
// blocked in the same system call: the next call waits on the same check.
type probes struct {
	mu   sync.Mutex
	db   *probeRun
	lock *probeRun

	// maint counts the long admin operations (verify) holding the writer
	// lock. While one does, a lock that stays taken is not a stuck writer;
	// the database still has to answer.
	maint atomic.Int32
}

type probeRun struct {
	done chan struct{}
	err  error
}

func (p *probes) start(slot **probeRun, fn func() error) *probeRun {
	p.mu.Lock()
	defer p.mu.Unlock()
	if r := *slot; r != nil {
		select {
		case <-r.done:
		default:
			return r // still running: wait on it
		}
	}
	r := &probeRun{done: make(chan struct{})}
	*slot = r
	go func() {
		defer close(r.done)
		r.err = fn()
	}()
	return r
}

// maintenance marks a long admin operation that holds the writer lock;
// call the returned func when it ends.
func (s *Share) maintenance() func() {
	s.probes.maint.Add(1)
	return func() { s.probes.maint.Add(-1) }
}

// Probe checks that the writer of the folder is alive: its database
// answers a read and its writer lock can be taken, before ctx ends. It
// returns ErrStuck when either does not; a database error is returned as
// it is. A lock held by a running verify is not counted as stuck.
func (s *Share) Probe(ctx context.Context) error {
	db := s.probes.start(&s.probes.db, func() error {
		var v int
		return s.db.QueryRow(`PRAGMA user_version`).Scan(&v)
	})
	lk := s.probes.start(&s.probes.lock, func() error {
		s.mu.Lock()
		s.mu.Unlock() //nolint:staticcheck // taking the lock is the check
		return nil
	})
	select {
	case <-db.done:
		if db.err != nil {
			return db.err
		}
	case <-ctx.Done():
		return ErrStuck
	}
	for {
		select {
		case <-lk.done:
			return nil
		default:
		}
		if s.probes.maint.Load() > 0 {
			return nil
		}
		select {
		case <-lk.done:
			return nil
		case <-ctx.Done():
			if s.probes.maint.Load() > 0 {
				return nil
			}
			return ErrStuck
		}
	}
}

// OpenShares returns the folders opened so far, by id.
func (s *Server) OpenShares() []*Share {
	s.mu.Lock()
	out := make([]*Share, 0, len(s.shares))
	for _, sh := range s.shares {
		out = append(out, sh)
	}
	s.mu.Unlock()
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// HoldWriter takes the folder's writer lock until release is called. It
// is for tests, standing in for a writer that hangs in a system call.
func (s *Share) HoldWriter() (release func()) {
	s.mu.Lock()
	return s.mu.Unlock
}
