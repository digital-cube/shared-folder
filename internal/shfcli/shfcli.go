// Package shfcli holds the commands of the shf client (docs/specifikacija.md,
// section 8.5):
//
//	shf join <url> <share-id> <token> <folder> [--ssh USER@HOST[:PORT]]
//	shf sync [<folder>]
//	shf status [<folder>]
//	shf log [<folder>] [-n N]
package shfcli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/digital-cube/shared-folder/internal/client"
	"github.com/digital-cube/shared-folder/internal/version"
)

const usage = `shf — shared folder client

Usage:
  shf join <url> <share-id> <token> <folder> [--ssh USER@HOST[:PORT]]
  shf sync [<folder>]
  shf status [<folder>]
  shf log [<folder>] [-n N]
  shf version
  shf help

join connects a local folder (created if missing; existing files are
merged, nothing is deleted) to a shared folder and runs the first sync.
The SSH endpoint for file transfer comes from the server; --ssh overrides
it. sync runs one full two-way cycle and exits non-zero if anything was
not synchronized. status and log read the local state; <folder> defaults
to the current directory (or any folder inside a joined one).

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
	return client.Open(cfg, env.Options)
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
