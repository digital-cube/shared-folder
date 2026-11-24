package shfdcli

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"syscall"

	"github.com/digital-cube/shared-folder/internal/authkeys"
	"github.com/digital-cube/shared-folder/internal/rsyncgate"
	"github.com/digital-cube/shared-folder/internal/server"
)

// sshFlags are the flags of the commands that change members' SSH keys.
type sshFlags struct {
	authorizedKeys string
	binary         string
}

func (f *sshFlags) register(fs *flag.FlagSet, env Env) {
	fs.StringVar(&f.authorizedKeys, "authorized-keys", env.Getenv(EnvAuthorizedKeys), "authorized_keys file of the system user sshd runs shfd as")
	fs.StringVar(&f.binary, "shfd-binary", env.Getenv(EnvBinary), "path of shfd written into the forced commands (default: this executable)")
}

// options returns server options with the authorized_keys file, if one
// was given. Its forced command is "<binary> rsync-gate --data-root <abs
// root>", with "--db-root <abs db-root>" when the databases live apart:
// sshd does not pass $SHFD_DB_ROOT on.
func (f *sshFlags) options(root roots, env Env) (server.Options, error) {
	if f.authorizedKeys == "" {
		return server.Options{}, nil
	}
	akPath, err := filepath.Abs(f.authorizedKeys)
	if err != nil {
		return server.Options{}, err
	}
	absRoot, absDB, err := root.abs()
	if err != nil {
		return server.Options{}, err
	}
	bin := f.binary
	if bin == "" {
		if bin, err = os.Executable(); err != nil {
			return server.Options{}, fmt.Errorf("finding the shfd executable for authorized_keys (set --shfd-binary): %w", err)
		}
		if r, err := filepath.EvalSymlinks(bin); err == nil {
			bin = r
		}
	}
	if !filepath.IsAbs(bin) {
		return server.Options{}, usageError("--shfd-binary must be an absolute path: sshd runs the forced command with a minimal PATH")
	}
	cmd := []string{bin, "rsync-gate", "--data-root", absRoot}
	if absDB != absRoot {
		cmd = append(cmd, "--db-root", absDB)
	}
	return server.Options{AuthorizedKeys: &authkeys.File{Path: akPath, Command: cmd}}, nil
}

func keysCmd(args []string, env Env) error {
	if len(args) == 0 || args[0] != "sync" {
		return usageError("keys: want sync")
	}
	fs := flag.NewFlagSet("keys sync", flag.ContinueOnError)
	var sf sshFlags
	sf.register(fs, env)
	root, _, err := flags(fs, args[1:], 0, env)
	if err != nil {
		return err
	}
	if sf.authorizedKeys == "" {
		return usageError("keys sync: --authorized-keys or $%s is required", EnvAuthorizedKeys)
	}
	opts, err := sf.options(root, env)
	if err != nil {
		return err
	}
	opts.SkipRecovery = true
	srv, err := root.open(opts)
	if err != nil {
		return err
	}
	defer srv.Close()
	skipped, err := srv.SyncAuthorizedKeys()
	if err != nil {
		return err
	}
	for _, s := range skipped {
		fmt.Fprintf(env.Stderr, "skipped %s: its key clashes with another line\n", s)
	}
	es, err := opts.AuthorizedKeys.Entries()
	if err != nil {
		return err
	}
	devs := 0
	for _, e := range es {
		if e.DeviceID != 0 {
			devs++
		}
	}
	fmt.Fprintf(env.Stdout, "%s: %d member key(s), %d device key(s)\n", opts.AuthorizedKeys.Path, len(es)-devs, devs)
	return nil
}

// rsyncGate is the forced command of every member's key. Its exit code is
// rsync's, or non-zero when the command is refused. Nothing but rsync's
// protocol may go to stdout.
func rsyncGate(args []string, env Env) int {
	fs := flag.NewFlagSet("rsync-gate", flag.ContinueOnError)
	share := fs.String("share", "", "share id")
	member := fs.String("member", "", "member id")
	device := fs.Int64("device", 0, "device id of the account (the line of a device's key)")
	rsync := fs.String("rsync", "", "rsync binary on the server (default: rsync from PATH)")
	root, _, err := flags(fs, args, 0, env)
	if err != nil {
		fmt.Fprintf(env.Stderr, "shfd rsync-gate: %v\n", err)
		return 2
	}
	mid, err := strconv.ParseInt(*member, 10, 64)
	if err != nil || mid <= 0 || !server.ValidShareID(*share) {
		fmt.Fprintln(env.Stderr, "shfd rsync-gate: --share <share-id> and --member <member-id> are required")
		return 2
	}
	if *device < 0 {
		fmt.Fprintln(env.Stderr, "shfd rsync-gate: --device must be a device id")
		return 2
	}
	srv, err := root.open(server.Options{SkipRecovery: true})
	if err != nil {
		fmt.Fprintf(env.Stderr, "shfd rsync-gate: %v\n", err)
		return 1
	}
	defer srv.Close()
	sh, err := srv.Share(*share)
	if err != nil {
		fmt.Fprintf(env.Stderr, "shfd rsync-gate: %v\n", err)
		return 1
	}
	ctx := env.Ctx
	if ctx == nil {
		var stop context.CancelFunc
		ctx, stop = signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM, syscall.SIGHUP)
		defer stop()
	}
	stdin := env.Stdin
	if stdin == nil {
		stdin = os.Stdin
	}
	g := &rsyncgate.Gate{Share: sh, MemberID: mid, DeviceID: *device, Server: srv, Rsync: *rsync}
	return g.Run(ctx, env.Getenv("SSH_ORIGINAL_COMMAND"), stdin, env.Stdout, env.Stderr)
}
