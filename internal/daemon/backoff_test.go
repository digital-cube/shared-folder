package daemon_test

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/digital-cube/shared-folder/internal/client"
	"github.com/digital-cube/shared-folder/internal/daemon"
	"github.com/digital-cube/shared-folder/internal/transport"
	"github.com/digital-cube/shared-folder/internal/transport/rsyncssh"
)

// fakeClock is the time of one folder's loop: it moves only when the test
// fires the loop's wait.
type fakeClock struct {
	mu   sync.Mutex
	now  time.Time
	wait *fakeWait // the loop's pending wait, nil while it is busy
	arms int       // waits armed so far
}

type fakeWait struct {
	at time.Time
	ch chan time.Time
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) At(t time.Time) (<-chan time.Time, func()) {
	c.mu.Lock()
	defer c.mu.Unlock()
	ch := make(chan time.Time, 1)
	if !t.After(c.now) {
		ch <- c.now
		return ch, func() {}
	}
	w := &fakeWait{at: t, ch: ch}
	c.wait = w
	c.arms++
	return ch, func() {
		c.mu.Lock()
		if c.wait == w {
			c.wait = nil
		}
		c.mu.Unlock()
	}
}

func (c *fakeClock) armed() (time.Time, int, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.wait == nil {
		return time.Time{}, c.arms, false
	}
	return c.wait.at, c.arms, true
}

// idle waits until the loop waits for the clock and returns until when.
func (c *fakeClock) idle(t *testing.T) time.Time {
	t.Helper()
	var at time.Time
	waitFor(t, 10*time.Second, "the loop to wait", func() bool {
		var ok bool
		at, _, ok = c.armed()
		return ok
	})
	return at
}

// step moves the clock to the loop's deadline, lets it act on it and
// returns the deadline.
func (c *fakeClock) step(t *testing.T) time.Time {
	t.Helper()
	at := c.idle(t)
	c.mu.Lock()
	w := c.wait
	c.wait = nil
	c.now = w.at
	w.ch <- w.at
	c.mu.Unlock()
	c.idle(t) // armed again: what the deadline called for is done
	return at
}

// rearmed waits until the loop armed a new wait after act (a poke).
func (c *fakeClock) rearmed(t *testing.T, what string, act func()) {
	t.Helper()
	_, n, _ := c.armed()
	act()
	waitFor(t, 10*time.Second, what, func() bool {
		_, m, ok := c.armed()
		return ok && m > n
	})
}

// failing is the local transport, or a server that refuses the key or
// cannot be reached, as rsyncssh reports them.
type failing struct {
	transport.Transport
	fail atomic.Value // error, or nil
}

func (f *failing) set(err error) { f.fail.Store(&err) }

func (f *failing) err() error {
	if p, _ := f.fail.Load().(*error); p != nil {
		return *p
	}
	return nil
}

func (f *failing) Send(ctx context.Context, u transport.Upload, src string) error {
	if err := f.err(); err != nil {
		return err
	}
	return f.Transport.Send(ctx, u, src)
}

func (f *failing) Fetch(ctx context.Context, shareID, p, hash, dst string) error {
	if err := f.err(); err != nil {
		return err
	}
	return f.Transport.Fetch(ctx, shareID, p, hash, dst)
}

var (
	errRefused     = fmt.Errorf("rsync tmp/x: %w: ana@10.0.0.1: Permission denied (publickey).", rsyncssh.ErrAuth)
	errUnreachable = fmt.Errorf("rsync tmp/x: %w: exit status 255: kex_exchange_identification: Connection reset", rsyncssh.ErrUnreachable)
)

type attempt struct {
	at     time.Time
	reason string
	err    error
}

// clocked runs the peer's daemon on clk with transport tr.
func (p *peer) clocked(clk *fakeClock, tr transport.Transport, set client.Settings) func() []attempt {
	p.t.Helper()
	var mu sync.Mutex
	var log []attempt
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- daemon.Run(ctx, daemon.Config{
			Home: p.home, Settings: set, Clock: clk,
			Options: client.Options{Transport: tr, Now: clk.Now},
			Log:     slog.New(slog.NewTextHandler(syncWriter{&p.mu, &p.log}, &slog.HandlerOptions{Level: slog.LevelDebug})),
			OnCycle: func(_, reason string, _ *client.Result, err error) {
				mu.Lock()
				log = append(log, attempt{clk.Now(), reason, err})
				mu.Unlock()
			},
		})
	}()
	p.t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			p.t.Errorf("%s: daemon did not stop", p.user)
		}
		if p.t.Failed() {
			p.mu.Lock()
			p.t.Logf("%s daemon log:\n%s", p.user, p.log.String())
			p.mu.Unlock()
		}
	})
	attempts := func() []attempt {
		mu.Lock()
		defer mu.Unlock()
		return append([]attempt(nil), log...)
	}
	waitFor(p.t, 10*time.Second, "the first cycle", func() bool { return len(attempts()) > 0 })
	clk.idle(p.t)
	return attempts
}

func clockedSettings() client.Settings {
	set := fast()
	set.Settle = 0 // the fake clock and the files' mtimes are not comparable
	return set
}

// TestKeyRefusedBacksOff: while the server refuses the key, the attempts
// come 5 s, 10 s, 20 s … apart (with jitter) up to 5 min; neither news
// from the server nor a local change brings one sooner; the refusal is
// logged once; a cycle that goes through starts the schedule anew.
func TestKeyRefusedBacksOff(t *testing.T) {
	t.Parallel()
	w := newWorld(t, nil)
	ana := w.join("ana")
	clk := &fakeClock{now: time.Now()}
	tr := &failing{Transport: transport.Local{DataRoot: w.root}}
	attempts := ana.clocked(clk, tr, clockedSettings())
	if a := attempts(); len(a) != 1 || a[0].err != nil {
		t.Fatalf("start: %+v", a)
	}

	tr.set(errRefused)
	clk.rearmed(t, "the local change seen", func() { ana.write("a.txt", "a") })
	clk.step(t) // the debounce
	if a := attempts(); len(a) != 2 || a[1].reason != daemon.ReasonLocal || client.Classify(a[1].err) != client.FailKeyRefused {
		t.Fatalf("after the local change: %+v", a)
	}

	want := []time.Duration{5, 10, 20, 40, 80, 160, 300, 300, 300}
	for i, nominal := range want {
		nominal *= time.Second
		before := attempts()
		last := before[len(before)-1].at
		deadline, _, _ := clk.armed()
		if i == 2 {
			// news from the server and a local change during the pause:
			// no attempt, and the deadline stays where it was
			bob := w.join("bob")
			bob.write("b.txt", "b")
			c, err := client.Open(mustFind(t, bob), client.Options{Transport: transport.Local{DataRoot: w.root}})
			if err != nil {
				t.Fatal(err)
			}
			clk.rearmed(t, "the server's news heard", func() {
				if _, err := c.Sync(context.Background()); err != nil {
					t.Fatal(err)
				}
			})
			c.Close()
			clk.rearmed(t, "the local change seen", func() { ana.write("c.txt", "c") })
			if at, _, _ := clk.armed(); !at.Equal(deadline) {
				t.Errorf("the deadline moved from %v to %v", deadline.Sub(last), at.Sub(last))
			}
			if n := len(attempts()); n != len(before) {
				t.Fatalf("%d attempts during the pause", n-len(before))
			}
		}
		clk.step(t)
		a := attempts()
		if len(a) != len(before)+1 {
			t.Fatalf("pause %d: %d attempts, want 1", i+1, len(a)-len(before))
		}
		got := a[len(a)-1].at.Sub(last)
		lo, hi := max(nominal*4/5, 5*time.Second), min(nominal*6/5, 5*time.Minute)
		if got < lo || got > hi {
			t.Errorf("pause %d: %v, want %v ±20 %% (within [%v, %v])", i+1, got, nominal, lo, hi)
		}
		if a[len(a)-1].reason != daemon.ReasonRetry || client.Classify(a[len(a)-1].err) != client.FailKeyRefused {
			t.Errorf("pause %d: attempt %+v", i+1, a[len(a)-1])
		}
	}
	if n := countLog(ana, "key refused"); n != 1 {
		t.Errorf("the refused key was logged %d times, want once", n)
	}

	// the key is accepted again: the next attempt goes through and the
	// schedule starts anew
	tr.set(nil)
	clk.step(t)
	a := attempts()
	if a[len(a)-1].err != nil {
		t.Fatalf("after the key is accepted: %v", a[len(a)-1].err)
	}
	if got, _ := w.storage("c.txt"); got != "c" {
		t.Errorf("c.txt on the server: %q", got)
	}
	tr.set(errRefused)
	clk.rearmed(t, "the local change seen", func() { ana.write("d.txt", "d") })
	clk.step(t) // the debounce: refused again
	first := attempts()
	clk.step(t)
	a = attempts()
	if len(a) != len(first)+1 {
		t.Fatalf("%d attempts, want 1", len(a)-len(first))
	}
	if got := a[len(a)-1].at.Sub(first[len(first)-1].at); got < 5*time.Second || got > 6*time.Second {
		t.Errorf("first pause after a success: %v, want 5 s (+jitter)", got)
	}
}

// TestUnreachableLocalChangeShortensPause: after an unreachable server a
// local change may end the pause early, but never sooner than 5 s after
// the failure; news from the server may not.
func TestUnreachableLocalChangeShortensPause(t *testing.T) {
	t.Parallel()
	w := newWorld(t, nil)
	ana := w.join("ana")
	clk := &fakeClock{now: time.Now()}
	tr := &failing{Transport: transport.Local{DataRoot: w.root}}
	attempts := ana.clocked(clk, tr, clockedSettings())

	tr.set(errUnreachable)
	clk.rearmed(t, "the local change seen", func() { ana.write("a.txt", "a") })
	for range 5 { // the debounce, then pauses of 5, 10, 20, 40 s
		clk.step(t)
	}
	a := attempts()
	last := a[len(a)-1]
	if client.Classify(last.err) != client.FailUnreachable {
		t.Fatalf("attempt: %+v", last)
	}
	if deadline, _, _ := clk.armed(); deadline.Sub(last.at) < 64*time.Second {
		t.Fatalf("pause %v, want about 80 s", deadline.Sub(last.at))
	}
	clk.rearmed(t, "the local change seen", func() { ana.write("b.txt", "b") })
	clk.step(t)
	n := len(a)
	a = attempts()
	if len(a) != n+1 || a[len(a)-1].reason != daemon.ReasonLocal {
		t.Fatalf("after a local change: %+v", a[len(a)-1])
	}
	if got := a[len(a)-1].at.Sub(last.at); got != 5*time.Second {
		t.Errorf("a local change ended the pause after %v, want 5 s (not sooner)", got)
	}
}

func mustFind(t *testing.T, p *peer) *client.Config {
	t.Helper()
	cfg, err := client.Find(p.home, p.dir)
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}

func countLog(p *peer, s string) int {
	p.mu.Lock()
	defer p.mu.Unlock()
	n := 0
	for _, line := range strings.Split(p.log.String(), "\n") {
		if strings.Contains(line, "level=ERROR") && strings.Contains(line, s) {
			n++
		}
	}
	return n
}
