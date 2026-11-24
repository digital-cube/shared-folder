// Package shfcli holds the commands of the shf client (docs/specifikacija.md,
// section 8.5):
//
//	shf join <url> <share-id> <token> <folder> [--ssh USER@HOST[:PORT]]
//	shf sync [<folder>]
//	shf daemon
//	shf status [<folder>]
//	shf confirm [<folder>]
//	shf restore-local [<folder>]
//	shf log [<folder>] [-n N]
//	shf service install|uninstall|status
package shfcli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/digital-cube/shared-folder/internal/client"
	"github.com/digital-cube/shared-folder/internal/daemon"
	"github.com/digital-cube/shared-folder/internal/service"
	"github.com/digital-cube/shared-folder/internal/version"
)

const usage = `shf — shared folder client

Usage:
  shf join <url> <share-id> <token> <folder> [--ssh USER@HOST[:PORT]]
  shf sync [<folder>]
  shf daemon
  shf status [<folder>]
  shf confirm [<folder>]
  shf restore-local [<folder>]
  shf log [<folder>] [-n N]
  shf service install|uninstall|status
  shf version
  shf help

join connects a local folder (created if missing; existing files are
merged, nothing is deleted) to a shared folder and runs the first sync.
The SSH endpoint for file transfer comes from the server; --ssh overrides
it. sync runs one full two-way cycle and exits non-zero if anything was
not synchronized. status and log read the local state; <folder> defaults
to the current directory (or any folder inside a joined one).

daemon synchronizes every joined folder in the background: changes on
the server arrive over a WebSocket (polling while it is down), local
changes are seen as they happen. service install makes it start with
your session (systemd user unit on Linux, launchd agent on macOS); it
records the rsync and ssh found now (in PATH, or $SHF_RSYNC and
$SHF_SSH) and refuses an rsync older than 3.2.4, so run it again after
replacing rsync.

A cycle that would delete more than 20 files, or more than 10 % of the
folder (whichever is less), on the server sends none of the deletions:
the folder is blocked. status lists them, confirm sends them,
restore-local brings the files back from the server.

The intervals and limits are in $SHF_STATE_HOME/settings.json (see
README.md); missing keys keep their defaults.

The state of each joined folder is kept outside it, in
$XDG_STATE_HOME/shf/<share-id>/ (~/.local/state/shf, on macOS
~/Library/Application Support/shf); $SHF_STATE_HOME overrides the root.
`

// Env is what a command runs against; tests replace it.
type Env struct {
	Stdout, Stderr io.Writer
	Getenv         func(string) string
	// Ctx cancels a running command (SIGINT/SIGTERM when nil).
	Ctx context.Context
	// Options are passed to the client (tests inject a transport).
	Options client.Options
}

// Main runs with the process environment.
func Main(args []string) int {
	return Run(args, Env{Stdout: os.Stdout, Stderr: os.Stderr, Getenv: os.Getenv})
}

type usageErr struct{ msg string }

func (e usageErr) Error() string { return e.msg }

func usageError(format string, a ...any) error { return usageErr{fmt.Sprintf(format, a...)} }

// Run executes one command and returns the exit code: 0 on success, 1
// when something failed, 2 for a usage error.
func Run(args []string, env Env) int {
	if env.Getenv == nil {
		env.Getenv = func(string) string { return "" }
	}
	if len(args) == 0 {
		fmt.Fprint(env.Stdout, usage)
		return 0
	}
	ctx := env.Ctx
	if ctx == nil {
		var stop context.CancelFunc
		ctx, stop = signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer stop()
	}
	var err error
	switch args[0] {
	case "version", "-version", "--version", "-v":
		fmt.Fprintf(env.Stdout, "shf %s\n", version.Version)
		return 0
	case "help", "-help", "--help", "-h":
		fmt.Fprint(env.Stdout, usage)
		return 0
	case "join":
		err = join(ctx, args[1:], env)
	case "sync":
		err = syncCmd(ctx, args[1:], env)
	case "status":
		err = status(ctx, args[1:], env)
	case "confirm":
		err = resolveHeld(ctx, args[1:], env, "confirm")
	case "restore-local":
		err = resolveHeld(ctx, args[1:], env, "restore-local")
	case "daemon":
		err = daemonCmd(ctx, args[1:], env)
	case "service":
		err = serviceCmd(args[1:], env)
	case "log":
		err = logCmd(args[1:], env)
	default:
		err = usageError("unknown command %q", args[0])
	}
	var ue usageErr
	switch {
	case err == nil:
		return 0
	case errors.As(err, &ue):
		fmt.Fprintf(env.Stderr, "shf: %v\n\n%s", err, usage)
		return 2
	default:
		fmt.Fprintf(env.Stderr, "shf: %v\n", err)
		return 1
	}
}

// parse parses flags mixed with positionals and checks their number.
func parse(fs *flag.FlagSet, args []string, minPos, maxPos int) ([]string, error) {
	fs.SetOutput(io.Discard)
	var pos []string
	for {
		if err := fs.Parse(args); err != nil {
			return nil, usageError("%s: %v", fs.Name(), err)
		}
		args = fs.Args()
		if len(args) == 0 {
			break
		}
		pos = append(pos, args[0])
		args = args[1:]
	}
	if len(pos) < minPos || len(pos) > maxPos {
		if minPos == maxPos {
			return nil, usageError("%s: want %d arguments, got %d", fs.Name(), minPos, len(pos))
		}
		return nil, usageError("%s: want %d to %d arguments, got %d", fs.Name(), minPos, maxPos, len(pos))
	}
	return pos, nil
}

func parseSSH(s string) (client.SSH, error) {
	var e client.SSH
	if s == "" {
		return e, nil
	}
	if u, rest, ok := strings.Cut(s, "@"); ok {
		e.User, s = u, rest
	}
	host, port := s, ""
	if strings.HasPrefix(s, "[") {
		i := strings.Index(s, "]")
		if i < 0 {
			return e, usageError("--ssh %q: missing ]", s)
		}
		host, port = s[1:i], strings.TrimPrefix(s[i+1:], ":")
	} else if h, p, ok := strings.Cut(s, ":"); ok {
		host, port = h, p
	}
	e.Host = host
	if port != "" {
		n, err := strconv.Atoi(port)
		if err != nil || n < 1 || n > 65535 {
			return e, usageError("--ssh: invalid port %q", port)
		}
		e.Port = n
	}
	return e, nil
}

func join(ctx context.Context, args []string, env Env) error {
	fs := flag.NewFlagSet("join", flag.ContinueOnError)
	sshFlag := fs.String("ssh", "", "USER@HOST[:PORT] of the server's sshd (default: what the server says)")
	pos, err := parse(fs, args, 4, 4)
	if err != nil {
		return err
	}
	ep, err := parseSSH(*sshFlag)
	if err != nil {
		return err
	}
	home, err := client.StateHome(env.Getenv)
	if err != nil {
		return err
	}
	c, res, err := client.Join(ctx, client.JoinRequest{Home: home, URL: pos[0], ShareID: pos[1], Token: pos[2], Folder: pos[3], SSH: ep}, env.Options)
	if c != nil {
		defer c.Close()
		fmt.Fprintf(env.Stdout, "joined %s to share %s\nstate: %s\n", c.Config().Folder, c.Config().ShareID, c.Config().Dir)
	}
	if res != nil {
		printResult(env.Stdout, res)
	}
	return err
}

// open finds the joined folder named by args (default: the working
// directory).
func open(args []string, env Env) (*client.Client, error) {
	dir := "."
	if len(args) > 0 {
		dir = args[0]
	}
	home, err := client.StateHome(env.Getenv)
	if err != nil {
		return nil, err
	}
	cfg, err := client.Find(home, dir)
	if err != nil {
		return nil, err
	}
	opts := env.Options
	set, err := client.LoadSettings(home)
	if err != nil {
		return nil, err
	}
	if opts.MassDelete == nil {
		opts.MassDelete = &set.MassDelete
	}
	if opts.BwLimit == 0 {
		opts.BwLimit = set.BwLimit
	}
	return client.Open(cfg, opts)
}

func syncCmd(ctx context.Context, args []string, env Env) error {
	fs := flag.NewFlagSet("sync", flag.ContinueOnError)
	pos, err := parse(fs, args, 0, 1)
	if err != nil {
		return err
	}
	c, err := open(pos, env)
	if err != nil {
		return err
	}
	defer c.Close()
	res, err := c.Sync(ctx)
	if res != nil {
		printResult(env.Stdout, res)
	}
	return err
}

func printResult(w io.Writer, r *client.Result) {
	for _, it := range r.Items {
		printItem(w, "  ", it)
	}
	fmt.Fprintf(w, "sync: sent %d, received %d, conflicts %d, seq %d → %d (server %d), %s\n",
		r.Sent, r.Received, r.Conflicts, r.FromSeq, r.ToSeq, r.Head, r.FinishedAt.Sub(r.StartedAt).Round(time.Millisecond))
	if len(r.Problems) > 0 {
		fmt.Fprintf(w, "%d paths need your attention:\n", len(r.Problems))
		for _, p := range r.Problems {
			fmt.Fprintf(w, "  %s: %s\n", p.Path, p.Reason)
		}
	}
	if r.Blocked {
		fmt.Fprintln(w, "BLOCKED: deletions were held back (too many at once); shf status lists them,")
		fmt.Fprintln(w, "  shf confirm sends them, shf restore-local brings the files back")
	}
}

func printItem(w io.Writer, indent string, it client.Item) {
	line := indent + it.Op
	if it.Path != "" {
		line += " " + it.Path
	}
	if it.Detail != "" {
		line += " (" + it.Detail + ")"
	}
	fmt.Fprintln(w, line)
}

func status(ctx context.Context, args []string, env Env) error {
	fs := flag.NewFlagSet("status", flag.ContinueOnError)
	pos, err := parse(fs, args, 0, 1)
	if err != nil {
		return err
	}
	c, err := open(pos, env)
	if err != nil {
		return err
	}
	defer c.Close()
	s, err := c.Status(ctx)
	if err != nil {
		return err
	}
	w := env.Stdout
	fmt.Fprintf(w, "folder:     %s\nshare:      %s\nserver:     %s\n", s.Config.Folder, s.Config.ShareID, s.Config.URL)
	switch {
	case s.Last == nil:
		fmt.Fprintln(w, "last sync:  never")
	case s.Last.Error != "":
		fmt.Fprintf(w, "last sync:  %s, FAILED: %s\n", s.Last.FinishedAt.Local().Format(time.DateTime), s.Last.Error)
	default:
		fmt.Fprintf(w, "last sync:  %s, ok (sent %d, received %d, conflicts %d)\n",
			s.Last.FinishedAt.Local().Format(time.DateTime), s.Last.Sent, s.Last.Received, s.Last.Conflicts)
	}
	if s.HeadErr != nil {
		fmt.Fprintf(w, "seq:        local %d, server unreachable (%v); last seen %d\n", s.Seq, s.HeadErr, s.Head)
	} else {
		fmt.Fprintf(w, "seq:        local %d, server HEAD %d\n", s.Seq, s.Head)
	}
	if s.PendingErr != nil {
		fmt.Fprintf(w, "pending:    unknown (%v)\n", s.PendingErr)
	} else {
		fmt.Fprintf(w, "pending:    %d changes\n", s.Pending)
	}
	if s.Busy {
		fmt.Fprintln(w, "running:    a sync is in progress")
	}
	if len(s.Problems) > 0 {
		fmt.Fprintf(w, "problems:   %d paths need your attention (not synchronized):\n", len(s.Problems))
		for _, p := range s.Problems {
			fmt.Fprintf(w, "  %s: %s\n", p.Path, p.Reason)
		}
	}
	switch {
	case s.Blocked:
		fmt.Fprintf(w, "state:      blocked: %d deletions held back (too many at once)\n", len(s.Held))
		fmt.Fprintln(w, "            shf confirm sends them, shf restore-local brings the files back")
	case len(s.Held) > 0:
		fmt.Fprintf(w, "state:      %d confirmed deletions wait for the next cycle\n", len(s.Held))
	default:
		fmt.Fprintln(w, "state:      ok")
	}
	for _, h := range s.Held {
		what := "delete"
		if h.Kind == client.Dir {
			what = "rmdir "
		}
		mark := ""
		if h.Confirmed {
			mark = " (confirmed)"
		}
		fmt.Fprintf(w, "  %s %s  since %s%s\n", what, h.Path, h.Since.Local().Format(time.DateTime), mark)
	}
	return nil
}

// resolveHeld is shf confirm and shf restore-local. Both wait for a cycle
// in progress (the daemon's) and then run one.
func resolveHeld(ctx context.Context, args []string, env Env, cmd string) error {
	fs := flag.NewFlagSet(cmd, flag.ContinueOnError)
	pos, err := parse(fs, args, 0, 1)
	if err != nil {
		return err
	}
	c, err := open(pos, env)
	if err != nil {
		return err
	}
	defer c.Close()
	var n int
	var res *client.Result
	if cmd == "confirm" {
		n, res, err = c.Confirm(ctx)
	} else {
		n, res, err = c.RestoreLocal(ctx)
	}
	if errors.Is(err, client.ErrNothingHeld) {
		fmt.Fprintln(env.Stdout, "nothing to do: no deletions are held back")
		return nil
	}
	if n > 0 {
		if cmd == "confirm" {
			fmt.Fprintf(env.Stdout, "released %d held deletions\n", n)
		} else {
			fmt.Fprintf(env.Stdout, "restoring %d held paths from the server\n", n)
		}
	}
	if res != nil {
		printResult(env.Stdout, res)
	}
	return err
}

func daemonCmd(ctx context.Context, args []string, env Env) error {
	fs := flag.NewFlagSet("daemon", flag.ContinueOnError)
	verbose := fs.Bool("v", false, "log every cycle")
	if _, err := parse(fs, args, 0, 0); err != nil {
		return err
	}
	home, err := client.StateHome(env.Getenv)
	if err != nil {
		return err
	}
	set, err := client.LoadSettings(home)
	if err != nil {
		return err
	}
	level := slog.LevelInfo
	if *verbose {
		level = slog.LevelDebug
	}
	log := slog.New(slog.NewTextHandler(env.Stderr, &slog.HandlerOptions{Level: level}))
	log.Info("shf daemon starting", "version", version.Version, "state", home)
	return daemon.Run(ctx, daemon.Config{Home: home, Settings: set, Options: env.Options, Log: log})
}

func serviceCmd(args []string, env Env) error {
	if len(args) != 1 {
		return usageError("service: want install, uninstall or status")
	}
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	if r, err := filepath.EvalSymlinks(exe); err == nil {
		exe = r
	}
	senv := service.Env{GOOS: runtime.GOOS, Getenv: env.Getenv, Exe: exe}
	if d := env.Getenv(client.EnvStateHome); d != "" {
		if senv.StateHome, err = filepath.Abs(d); err != nil {
			return err
		}
	}
	if args[0] == "install" {
		// the tools the daemon will run; a bad rsync installs nothing
		if senv, err = senv.Resolve(); err != nil {
			return fmt.Errorf("service not installed: %w", err)
		}
	}
	m, err := service.For(senv)
	if err != nil {
		return err
	}
	w := env.Stdout
	switch args[0] {
	case "install":
		if err := m.Install(); err != nil {
			return err
		}
		fmt.Fprintf(w, "installed %s\nshf daemon runs now and with every login; it serves every joined folder\n", m.File())
		fmt.Fprintf(w, "rsync: %s\nssh:   %s\n(after replacing rsync or ssh, run \"shf service install\" again)\n", senv.Rsync, senv.SSH)
	case "uninstall":
		if err := m.Uninstall(); err != nil {
			return err
		}
		fmt.Fprintf(w, "removed %s; shf daemon stopped\n", m.File())
	case "status":
		report, running, err := m.Status()
		if errors.Is(err, service.ErrNotInstalled) {
			fmt.Fprintln(w, "not installed (shf service install)")
			return nil
		}
		if err != nil {
			return err
		}
		state := "not running"
		if running {
			state = "running"
		}
		fmt.Fprintf(w, "installed: %s\nstate:     %s\n\n%s\n", m.File(), state, report)
	default:
		return usageError("service: unknown subcommand %q", args[0])
	}
	return nil
}

func logCmd(args []string, env Env) error {
	fs := flag.NewFlagSet("log", flag.ContinueOnError)
	n := fs.Int("n", 10, "number of cycles to show")
	pos, err := parse(fs, args, 0, 1)
	if err != nil {
		return err
	}
	c, err := open(pos, env)
	if err != nil {
		return err
	}
	defer c.Close()
	cs, err := c.State().Cycles(*n)
	if err != nil {
		return err
	}
	if len(cs) == 0 {
		fmt.Fprintln(env.Stdout, "no sync yet")
		return nil
	}
	for i := len(cs) - 1; i >= 0; i-- {
		cy := cs[i]
		outcome := "ok"
		if cy.Error != "" {
			outcome = "FAILED: " + cy.Error
		}
		fmt.Fprintf(env.Stdout, "%s  seq %d → %d  sent %d  received %d  conflicts %d  %s  %s\n",
			cy.StartedAt.Local().Format(time.DateTime), cy.FromSeq, cy.ToSeq, cy.Sent, cy.Received, cy.Conflicts,
			cy.FinishedAt.Sub(cy.StartedAt).Round(time.Millisecond), outcome)
		for _, it := range cy.Items {
			printItem(env.Stdout, "    ", it)
		}
	}
	return nil
}
