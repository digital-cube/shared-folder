package daemon

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/digital-cube/shared-folder/internal/update"
)

// ErrUpdated: Run installed a new shf and stopped for it; the caller
// starts the new binary (shf daemon re-executes itself).
var ErrUpdated = errors.New("shf was updated; restart to run the new version")

// Updater is the daemon's automatic update (update.Auto).
type Updater interface {
	// Prepare checks for a new version and stages it; a nil *Staged means
	// there is nothing to install, and the string says why.
	Prepare(ctx context.Context) (*update.Staged, string, error)
	// Install puts a staged binary in place.
	Install(ctx context.Context, st *update.Staged) error
}

// cycleGate lets the update in only between cycles: folders enter it for
// each cycle, the update holds it shut while it installs.
type cycleGate struct {
	mu      sync.Mutex
	running int
	shut    chan struct{} // non-nil while held; closed when released
	idle    chan struct{} // closed when the last cycle leaves a shut gate
}

// enter waits until the gate is open and counts a cycle in; false when
// stop came first.
func (g *cycleGate) enter(stop <-chan struct{}) bool {
	if g == nil {
		return true
	}
	for {
		g.mu.Lock()
		shut := g.shut
		if shut == nil {
			g.running++
			g.mu.Unlock()
			return true
		}
		g.mu.Unlock()
		select {
		case <-shut:
		case <-stop:
			return false
		}
	}
}

func (g *cycleGate) leave() {
	if g == nil {
		return
	}
	g.mu.Lock()
	g.running--
	if g.running == 0 && g.idle != nil {
		close(g.idle)
		g.idle = nil
	}
	g.mu.Unlock()
}

// hold shuts the gate and waits until no cycle runs; release opens it
// again.
func (g *cycleGate) hold(ctx context.Context) (release func(), err error) {
	g.mu.Lock()
	shut := make(chan struct{})
	g.shut = shut
	var idle chan struct{}
	if g.running > 0 {
		idle = make(chan struct{})
		g.idle = idle
	}
	g.mu.Unlock()
	release = func() {
		g.mu.Lock()
		if g.shut == shut {
			close(shut)
			g.shut, g.idle = nil, nil
		}
		g.mu.Unlock()
	}
	if idle != nil {
		select {
		case <-idle:
		case <-ctx.Done():
			release()
			return nil, ctx.Err()
		}
	}
	return release, nil
}

// busy says how many cycles are running (tests).
func (g *cycleGate) busy() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.running
}

// autoUpdate checks for a new version at start and every interval. When
// it installed one it closes restart, keeping the gate shut so that no
// cycle starts any more; it returns when ctx ends. Every attempt is one
// line in the log; a failure waits for the next interval.
func autoUpdate(ctx context.Context, cfg Config, gate *cycleGate, restart chan<- struct{}) {
	interval := cfg.UpdateInterval
	if interval <= 0 {
		interval = 24 * time.Hour
	}
	for {
		if attemptUpdate(ctx, cfg, gate, interval) {
			close(restart)
			return
		}
		t := time.NewTimer(interval)
		select {
		case <-ctx.Done():
			t.Stop()
			return
		case <-t.C:
		}
	}
}

// attemptUpdate is one check; true when a new shf is in place.
func attemptUpdate(ctx context.Context, cfg Config, gate *cycleGate, interval time.Duration) bool {
	st, what, err := cfg.Updater.Prepare(ctx)
	switch {
	case ctx.Err() != nil:
		st.Discard()
		return false
	case errors.Is(err, update.ErrNoServer):
		cfg.Log.Debug("auto-update: no joined folder, no server to follow")
		return false
	case err != nil:
		cfg.Log.Warn("auto-update failed; next try in "+interval.String(), "err", err)
		return false
	case st == nil:
		cfg.Log.Info("auto-update: " + what)
		return false
	}
	// between cycles: wait for the running ones, start no new one
	release, err := gate.hold(ctx)
	if err != nil {
		st.Discard()
		return false
	}
	if err := cfg.Updater.Install(ctx, st); err != nil {
		var ai *update.AfterInstallError
		if errors.As(err, &ai) {
			cfg.Log.Warn("auto-update: installed shf "+st.Version+"; restarting", "from", st.From.Server, "warning", err)
			return true
		}
		release()
		cfg.Log.Warn("auto-update failed; next try in "+interval.String(), "err", err)
		return false
	}
	cfg.Log.Info("auto-update: installed shf "+st.Version+" (was "+st.From.Current+"); restarting", "from", st.From.Server)
	return true
}
