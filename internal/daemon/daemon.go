// Package daemon is the background client, shf daemon
// (docs/specifikacija.md, 8.3): one process for every folder joined under
// a state home. For each folder it
//
//   - keeps a WebSocket to the server and runs a cycle when the server's
//     HEAD passes the folder's seq; a broken connection is retried with a
//     growing, jittered pause (up to Settings.ReconnectMax);
//   - while the WebSocket is down, asks the server for its HEAD every
//     Settings.Poll;
//   - watches the folder (inotify, kqueue on macOS, through fsnotify),
//     honouring .shfignore, and runs a cycle Settings.Debounce after the
//     last change; a file is sent only once its size and mtime have been
//     still for Settings.Settle;
//   - compares the whole server state at start and every
//     Settings.FullSync;
//   - never runs two cycles at once: a request that comes during a cycle
//     runs another right after it;
//   - after a failed cycle waits before the next one, whatever asks for
//     it: a refused SSH key or an unreachable server (rsync exit 255
//     included) from Settings.TransportRetry doubling, with jitter, up to
//     Settings.TransportRetryMax; other failures from 2 s up to
//     Settings.RetryMax. News from the server never ends that pause, a
//     local change only for a failure other than a refused key, and not
//     before the schedule's first pause. A refused key is logged once,
//     when it starts;
//   - ignores the changes its own cycles make on disk (downloads), so they
//     are not sent back;
//   - with Config.Updater (settings auto_update), checks at start and every
//     Settings.UpdateInterval whether its server runs another version and
//     publishes a package of it (internal/update). A new shf is staged
//     while the folders keep synchronizing, installed between cycles (no
//     new cycle starts, the running ones finish), and then Run stops the
//     folders and returns ErrUpdated so that the caller starts the new
//     binary.
//
// On SIGTERM (the context ends) every folder finishes the step in progress
// and the daemon returns.
package daemon

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/digital-cube/shared-folder/internal/client"
)

const lockFile = "daemon.lock"

// ErrRunning: another daemon serves this state home.
var ErrRunning = errors.New("another shf daemon is running for this state home")

// Reasons a cycle runs, as passed to Config.OnCycle.
const (
	ReasonStart  = "start"  // full comparison at start
	ReasonFull   = "full"   // periodic full comparison
	ReasonRemote = "remote" // the server's HEAD passed the folder's seq
	ReasonLocal  = "local"  // the folder changed
	ReasonRetry  = "retry"  // after a failure, or for a file that was still being written
)

// Config is what a daemon runs with.
type Config struct {
	Home     string // the state home (client.StateHome)
	Settings client.Settings
	// Options are the base options of every folder's client (tests set a
	// transport). Settle, MassDelete and OnWrite are set from Settings
	// unless MassDelete is already set.
	Options client.Options
	Log     *slog.Logger
	// Grace is how long a stopping daemon waits for the step in progress
	// before it cancels it (default 60 s).
	Grace time.Duration
	// OnCycle, when set, is called after every cycle (tests).
	OnCycle func(shareID, reason string, res *client.Result, err error)
	// Clock schedules the cycles of the folders; nil is the system clock
	// (tests set a fake one).
	Clock Clock
	// Updater, when set, updates shf on its own (Settings.AutoUpdate):
	// at start and every UpdateInterval (default 24 h).
	Updater        Updater
	UpdateInterval time.Duration

	gate *cycleGate // set by Run
}

// Clock is the time as the folders' loops see it.
type Clock interface {
	Now() time.Time
	// At returns a channel that receives once the clock reaches t (at
	// once when it is there already), and a function that releases it.
	At(t time.Time) (c <-chan time.Time, stop func())
}

type systemClock struct{}

func (systemClock) Now() time.Time { return time.Now() }

func (systemClock) At(t time.Time) (<-chan time.Time, func()) {
	tm := time.NewTimer(max(time.Until(t), 0))
	return tm.C, func() { tm.Stop() }
}

// Run serves every joined folder until ctx ends. Folders joined while it
// runs are picked up within Settings.Poll. After an automatic update it
// returns ErrUpdated.
func Run(ctx context.Context, cfg Config) error {
	if cfg.Log == nil {
		cfg.Log = slog.Default()
	}
	if cfg.Grace <= 0 {
		cfg.Grace = 60 * time.Second
	}
	if cfg.Clock == nil {
		cfg.Clock = systemClock{}
	}
	unlock, err := lockHome(cfg.Home)
	if err != nil {
		return err
	}
	defer unlock()

	cfg.gate = &cycleGate{}
	restart := make(chan struct{})
	updCtx, cancelUpd := context.WithCancel(ctx)
	var updWG sync.WaitGroup
	if cfg.Updater != nil {
		updWG.Add(1)
		go func() {
			defer updWG.Done()
			autoUpdate(updCtx, cfg, cfg.gate, restart)
		}()
	}
	defer func() { cancelUpd(); updWG.Wait() }()

	stop := make(chan struct{})
	hard, cancelHard := context.WithCancel(context.Background())
	defer cancelHard()
	var wg sync.WaitGroup
	running := map[string]*folder{}
	rescan := func() {
		cfgs, err := client.Joined(cfg.Home)
		if err != nil {
			cfg.Log.Error("reading joined folders", "home", cfg.Home, "err", err)
			return
		}
		for _, fc := range cfgs {
			if running[fc.ShareID] != nil {
				continue
			}
			f := newFolder(cfg, fc)
			running[fc.ShareID] = f
			wg.Add(1)
			go func() {
				defer wg.Done()
				f.run(hard, stop)
			}()
		}
	}
	rescan()
	if len(running) == 0 {
		cfg.Log.Warn("no joined folders yet; waiting (shf join)", "home", cfg.Home)
	}
	t := time.NewTicker(time.Duration(cfg.Settings.Poll))
	defer t.Stop()
	updated := false
	for {
		select {
		case <-t.C:
			rescan()
			continue
		case <-ctx.Done():
		case <-restart:
			updated = true
		}
		break
	}
	if updated {
		// the gate stays shut: no cycle is running, none starts
		cfg.Log.Info("stopping for the update")
	} else {
		cfg.Log.Info("stopping: finishing the step in progress")
	}
	close(stop)
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(cfg.Grace):
		cfg.Log.Warn("step in progress did not finish in time; canceling it", "grace", cfg.Grace)
		cancelHard()
		<-done
	}
	cancelUpd()
	updWG.Wait()
	cfg.Log.Info("stopped")
	if updated {
		return ErrUpdated
	}
	return nil
}
