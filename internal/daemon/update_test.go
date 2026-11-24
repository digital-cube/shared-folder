package daemon_test

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/digital-cube/shared-folder/internal/client"
	"github.com/digital-cube/shared-folder/internal/daemon"
	"github.com/digital-cube/shared-folder/internal/transport"
	"github.com/digital-cube/shared-folder/internal/update"
)

// fakeUpdater stages an update once ready is closed (or at once when
// ready is nil), or fails with err.
type fakeUpdater struct {
	ready     chan struct{}
	err       error
	prepares  atomic.Int32
	installed chan struct{}
	onInstall func()
}

func (u *fakeUpdater) Prepare(ctx context.Context) (*update.Staged, string, error) {
	u.prepares.Add(1)
	if u.ready != nil {
		select {
		case <-u.ready:
		case <-ctx.Done():
			return nil, "", ctx.Err()
		}
	}
	if u.err != nil {
		return nil, "", u.err
	}
	return &update.Staged{Version: "v2", From: &update.Check{Server: "https://srv", Current: "v1", ServerVersion: "v2"}}, "update available", nil
}

func (u *fakeUpdater) Install(context.Context, *update.Staged) error {
	if u.onInstall != nil {
		u.onInstall()
	}
	close(u.installed)
	return nil
}

// run starts the peer's daemon with u; done is closed when Run returned,
// with its error in *runErr.
func (p *peer) run(set client.Settings, u daemon.Updater, interval time.Duration, opts client.Options) (done <-chan struct{}, runErr *error) {
	ch := make(chan struct{})
	var rerr error
	ctx, cancel := context.WithCancel(context.Background())
	opts.Transport = transport.Local{DataRoot: p.w.root}
	go func() {
		defer close(ch)
		rerr = daemon.Run(ctx, daemon.Config{
			Home: p.home, Settings: set, Options: opts,
			Log:     slog.New(slog.NewTextHandler(syncWriter{&p.mu, &p.log}, &slog.HandlerOptions{Level: slog.LevelDebug})),
			Updater: u, UpdateInterval: interval,
			OnCycle: func(_, reason string, res *client.Result, err error) {
				p.mu.Lock()
				p.cycles = append(p.cycles, cycle{reason, res, err})
				p.mu.Unlock()
			},
		})
	}()
	p.t.Cleanup(func() {
		cancel()
		select {
		case <-ch:
		case <-time.After(10 * time.Second):
			p.t.Error("daemon did not stop")
		}
		if p.t.Failed() {
			p.mu.Lock()
			p.t.Logf("%s daemon log:\n%s", p.user, p.log.String())
			p.mu.Unlock()
		}
	})
	return ch, &rerr
}

// TestUpdateWaitsForTheCycle: an update that is ready while a cycle
// transfers is installed only after that cycle finished, and the daemon
// then stops for the restart.
func TestUpdateWaitsForTheCycle(t *testing.T) {
	t.Parallel()
	w := newWorld(t, nil)
	p := w.join("ana")

	var inCycle, cycleDone atomic.Bool
	entered := make(chan struct{})
	unblock := make(chan struct{})
	var once sync.Once
	opts := client.Options{AfterApply: func(a client.Action) error {
		if strings.HasSuffix(a.Path, "big.bin") {
			once.Do(func() {
				inCycle.Store(true)
				close(entered)
				<-unblock
			})
		}
		return nil
	}}
	u := &fakeUpdater{ready: make(chan struct{}), installed: make(chan struct{})}
	u.onInstall = func() {
		if inCycle.Load() && !cycleDone.Load() {
			t.Error("installed while a cycle was running")
		}
	}
	done, runErr := p.run(fast(), u, time.Hour, opts)
	waitFor(t, 10*time.Second, "first cycle", func() bool { return len(p.cyclesOf(daemon.ReasonStart)) > 0 })

	p.write("big.bin", "a transfer in progress")
	select {
	case <-entered:
	case <-time.After(10 * time.Second):
		t.Fatal("the cycle did not start")
	}
	close(u.ready) // the update is staged while the cycle transfers
	select {
	case <-u.installed:
		t.Fatal("installed in the middle of a cycle")
	case <-time.After(500 * time.Millisecond):
	}
	cycleDone.Store(true)
	close(unblock)
	select {
	case <-u.installed:
	case <-time.After(10 * time.Second):
		t.Fatal("not installed after the cycle")
	}
	select {
	case <-done:
		if !errors.Is(*runErr, daemon.ErrUpdated) {
			t.Fatalf("Run: %v", *runErr)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the daemon did not stop for the update")
	}
	if got, ok := w.storage("big.bin"); !ok || got != "a transfer in progress" {
		t.Errorf("the cycle did not finish: %q %v", got, ok)
	}
	for _, c := range p.cyclesOf(daemon.ReasonLocal) {
		if c.err != nil {
			t.Errorf("cycle failed: %v", c.err)
		}
	}
	p.mu.Lock()
	log := p.log.String()
	p.mu.Unlock()
	if !strings.Contains(log, "auto-update: installed shf v2 (was v1); restarting") {
		t.Errorf("log:\n%s", log)
	}
}

// TestUpdateFailureWaitsForTheInterval: a failed check is one line in
// the log and is tried again only after the interval; the folders keep
// synchronizing.
func TestUpdateFailureWaitsForTheInterval(t *testing.T) {
	t.Parallel()
	w := newWorld(t, nil)
	p := w.join("ana")
	u := &fakeUpdater{err: errors.New("server unreachable"), installed: make(chan struct{})}
	p.run(fast(), u, 400*time.Millisecond, client.Options{})
	waitFor(t, 10*time.Second, "first cycle", func() bool { return len(p.cyclesOf(daemon.ReasonStart)) > 0 })
	if n := u.prepares.Load(); n != 1 {
		t.Errorf("%d checks at start", n)
	}
	time.Sleep(1100 * time.Millisecond)
	if n := u.prepares.Load(); n < 2 || n > 4 {
		t.Errorf("%d checks in about 1.1 s with an interval of 400 ms", n)
	}
	p.write("after.txt", "still synchronizing")
	waitFor(t, 10*time.Second, "after.txt on the server", func() bool { got, _ := w.storage("after.txt"); return got == "still synchronizing" })
	p.mu.Lock()
	log := p.log.String()
	p.mu.Unlock()
	if n := strings.Count(log, "auto-update failed"); n != int(u.prepares.Load()) {
		t.Errorf("%d log lines for %d attempts:\n%s", n, u.prepares.Load(), log)
	}
}
