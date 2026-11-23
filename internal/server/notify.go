package server

import "sync"

// Watcher receives the HEAD of a folder after every write that added
// events to its log (docs/specifikacija.md, section 4: the WebSocket only
// carries {"head": N}; the events themselves are read from /changes).
//
// Heads is buffered with room for one value and always holds the newest
// HEAD: a slow reader skips intermediate values, never the last one.
// Revoked is closed when the member's token is revoked in this process
// (shfd member revoke runs in its own process; the WebSocket handler also
// re-checks the member from the database for that case).
type Watcher struct {
	MemberID int64
	Heads    chan int64
	Revoked  chan struct{}

	s    *Share
	once sync.Once
}

type watchers struct {
	mu  sync.Mutex
	set map[*Watcher]struct{}
}

// Watch subscribes to the folder's HEAD. Call Close when done. Subscribe
// before reading the current HEAD, so a write in between is not missed.
func (s *Share) Watch(memberID int64) *Watcher {
	w := &Watcher{MemberID: memberID, Heads: make(chan int64, 1), Revoked: make(chan struct{}), s: s}
	s.watch.mu.Lock()
	if s.watch.set == nil {
		s.watch.set = map[*Watcher]struct{}{}
	}
	s.watch.set[w] = struct{}{}
	s.watch.mu.Unlock()
	return w
}

// Close unsubscribes.
func (w *Watcher) Close() {
	w.s.watch.mu.Lock()
	delete(w.s.watch.set, w)
	w.s.watch.mu.Unlock()
}

// Watchers counts the open subscriptions (tests and statistics).
func (s *Share) Watchers() int {
	s.watch.mu.Lock()
	defer s.watch.mu.Unlock()
	return len(s.watch.set)
}

// notify hands head to every watcher, replacing a value it has not read.
func (s *Share) notify(head int64) {
	s.watch.mu.Lock()
	defer s.watch.mu.Unlock()
	for w := range s.watch.set {
		v := head
		select {
		case old := <-w.Heads:
			v = max(v, old)
		default:
		}
		w.Heads <- v // never blocks: notify is the only sender and runs under mu
	}
}

// revokeWatchers closes Revoked of every watcher of the member.
func (s *Share) revokeWatchers(memberID int64) {
	s.watch.mu.Lock()
	defer s.watch.mu.Unlock()
	for w := range s.watch.set {
		if w.MemberID == memberID {
			w.once.Do(func() { close(w.Revoked) })
		}
	}
}
