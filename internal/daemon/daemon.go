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
//   - ignores the changes its own cycles make on disk (downloads), so they
//     are not sent back.
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
}

// Run serves every joined folder until ctx ends. Folders joined while it
// runs are picked up within Settings.Poll.
func Run(ctx context.Context, cfg Config) error {
	if cfg.Log == nil {
		cfg.Log = slog.Default()
	}
	if cfg.Grace <= 0 {
		cfg.Grace = 60 * time.Second
	}
	unlock, err := lockHome(cfg.Home)
	if err != nil {
		return err
	}
	defer unlock()

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
	for {
		select {
		case <-t.C:
			rescan()
			continue
		case <-ctx.Done():
		}
		break
	}
	cfg.Log.Info("stopping: finishing the step in progress")
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
	cfg.Log.Info("stopped")
	return nil
}
