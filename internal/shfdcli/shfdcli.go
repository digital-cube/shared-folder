// Package shfdcli is the command line of shfd.
//
//	shfd serve --data-root DIR --listen ADDR [--tls-cert F --tls-key F]
//	shfd share create <name> [--admin USER]
//	shfd share list
//	shfd member add <share-id> <username> [--admin]
//	shfd member revoke <share-id> <username>
//	shfd member set-role <share-id> <username> admin|member
//	shfd member reset-token <share-id> <username>
//	shfd member list <share-id>
//	shfd keys sync
//	shfd trash list <share-id> [--path P] [--by USER] [--reason deleted|replaced] [--since DATE]
//	shfd trash restore <share-id> <trash-id> [--to PATH]
//	shfd trash purge <share-id> (--older-than AGE | --id N | --all) [--dry-run] [--yes]
//	shfd verify <share-id> [--repair] [--json]
//	shfd stats <share-id> [--json]
//	shfd db move --to DIR
//	shfd thumbs [--workers N] [--pdftoppm PATH] [--once] [--retry-failed]   (thumbs.go)
//	shfd rsync-gate --share <share-id> --member <member-id>   (sshd's forced command)
//
// --data-root defaults to $SHFD_DATA_ROOT and is accepted by every command,
// and so is --db-root (default $SHFD_DB_ROOT, else the data-root): where
// registry.db, every <share-id>/service.db, serve.lock and admin.sock live.
// --authorized-keys (default $SHFD_AUTHORIZED_KEYS) turns on the
// management of members' SSH keys for serve, member and keys.
//
// The trash, verify and stats commands (admin.go) talk to a running shfd
// serve over <data-root>/admin.sock, so they go through its single writer
// per folder; with no server running they do the same work in-process,
// holding the data-root lock. See docs/admin.md.
package shfdcli

import (
	"cmp"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"text/tabwriter"
	"time"

	"github.com/digital-cube/shared-folder/internal/authkeys"
	"github.com/digital-cube/shared-folder/internal/fsinfo"
	"github.com/digital-cube/shared-folder/internal/server"
	"github.com/digital-cube/shared-folder/internal/server/httpapi"
	"github.com/digital-cube/shared-folder/internal/version"
)

// Environment variables holding defaults of flags.
const (
	EnvDataRoot       = "SHFD_DATA_ROOT"       // --data-root
	EnvDBRoot         = "SHFD_DB_ROOT"         // --db-root
	EnvAuthorizedKeys = "SHFD_AUTHORIZED_KEYS" // --authorized-keys
	EnvBinary         = "SHFD_BINARY"          // --shfd-binary
	EnvSSHEndpoint    = "SHFD_SSH_ENDPOINT"    // serve --ssh-endpoint
	EnvDownloads      = "SHFD_DOWNLOADS"       // serve --downloads
)

const usage = `shfd — shared folder server

Usage:
  shfd serve --data-root DIR [--listen ADDR] [--tls-cert FILE --tls-key FILE]
             [--ssh-endpoint USER@HOST[:PORT]] [--downloads DIR]
  shfd share create <name> [--admin USERNAME]
  shfd share list
  shfd member add <share-id> <username> [--admin]
  shfd member revoke <share-id> <username>
  shfd member set-role <share-id> <username> admin|member
  shfd member reset-token <share-id> <username>
  shfd member list <share-id>
  shfd keys sync
  shfd trash list <share-id> [--path PREFIX] [--by USERNAME]
                  [--reason deleted|replaced] [--since DATE]
  shfd trash restore <share-id> <trash-id> [--to PATH]
  shfd trash purge <share-id> (--older-than AGE | --id ID | --all)
                   [--dry-run] [--yes]
  shfd verify <share-id> [--repair] [--json]
  shfd stats <share-id> [--json]
  shfd db move --to DIR
  shfd thumbs [--workers N] [--pdftoppm PATH] [--once] [--retry-failed]
              [--max-image-mib N] [--max-megapixels N] [--max-pdf-mib N]
              [--pdf-timeout D]
  shfd rsync-gate --share <share-id> --member <member-id>
  shfd version
  shfd help

Every command takes --data-root; it defaults to $SHFD_DATA_ROOT. Every
command also takes --db-root (default $SHFD_DB_ROOT, else the data-root):
the directory of registry.db, <share-id>/service.db, serve.lock and
admin.sock. Keep it on a local disk; storage/ may be on NFS, the SQLite
databases should not (serve warns when they are). db move copies the
databases of a stopped server to --to with SQLite's backup API, checks
each copy and prints what to change in the systemd unit (docs/admin.md).
serve speaks plain HTTP unless --tls-cert and --tls-key are given (TLS can
also come from a reverse proxy). serve --downloads DIR (default
$SHFD_DOWNLOADS) serves the packages of "make release" copied into DIR
(shared-folder-<version>-<os>-<arch>.tar.gz and SHA256SUMS) without a
token: GET /v1/downloads lists them, GET /downloads/<name> sends one; the
Install page of shfd-gui links to them (docs/install.md).

rsync over SSH (docs/server-ssh.md): serve, member and keys take
--authorized-keys FILE (default $SHFD_AUTHORIZED_KEYS), the authorized_keys
of the system user sshd runs shfd as. Members then register their SSH key
through the API, revoke removes it, and keys sync rewrites shfd's block from
the databases. Each line runs "<shfd-binary> rsync-gate"; --shfd-binary
(default $SHFD_BINARY, else this executable) sets the path written there.
serve --ssh-endpoint (default $SHFD_SSH_ENDPOINT) is the address clients
use for rsync over SSH; "shf join" learns it when it registers its key.
rsync-gate is that forced command: it reads $SSH_ORIGINAL_COMMAND and runs
only the rsync transfers the member may do.

Members (docs/admin.md): member add prints the member's token, which is
shown only then. A username stays taken by a revoked member; member add
with that name brings the member back (same history, new token, role from
--admin, no SSH key until shf join registers one). set-role refuses to take
the admin role from the folder's last active admin. reset-token prints a
new token for an active member; the old one stops working at once and the
SSH key stays.

Admin commands (docs/admin.md): trash, verify and stats reach a running
serve through <data-root>/admin.sock, so they are safe while it runs; with
no server they work directly on the data-root. They act in the name of the
folder's oldest active admin, or of --as USERNAME (an admin). AGE is 36h,
30d or 2w; DATE is YYYY-MM-DD or RFC 3339. trash purge asks before it
deletes unless --yes is given. verify exits with 1 when it finds (or, with
--repair, leaves) an issue.

Thumbnails (docs/admin.md): shfd thumbs is a long-running service of its
own (systemd unit shfd-thumbs.service) that makes the thumbnails of
images and PDFs into <data-root>/<share-id>/thumbnails/. It needs a running
shfd serve, from which it takes its jobs over admin.sock; it opens no
database. PDFs need pdftoppm (apt install poppler-utils; --pdftoppm or
$SHFD_PDFTOPPM). A file that cannot be done gets a .fail marker and is
not tried again until --retry-failed. --once does what waits and exits
(exit 1 when serve does not answer).
`

// Env is what a command runs against; tests replace it.
type Env struct {
	Stdin          io.Reader // rsync-gate and trash purge's question; os.Stdin when nil
	Stdout, Stderr io.Writer
	Getenv         func(string) string
	// Ctx ends serve (SIGINT/SIGTERM when nil).
	Ctx context.Context
	// Ready, when set, receives the address serve listens on.
	Ready func(addr string)
	// DetectFS tells serve which file system the db-root is on, to warn
	// about a network one; fsinfo.Detect when nil.
	DetectFS fsinfo.Detector
}

// Main runs with the process environment.
func Main(args []string) int {
	return Run(args, Env{Stdout: os.Stdout, Stderr: os.Stderr, Getenv: os.Getenv})
}

// Run executes one command and returns the exit code.
func Run(args []string, env Env) int {
	if env.Getenv == nil {
		env.Getenv = func(string) string { return "" }
	}
	if len(args) == 0 {
		fmt.Fprint(env.Stdout, usage)
		return 0
	}
	var err error
	switch args[0] {
	case "version", "-version", "--version", "-v":
		fmt.Fprintf(env.Stdout, "shfd %s\n", version.Version)
		return 0
	case "help", "-help", "--help", "-h":
		fmt.Fprint(env.Stdout, usage)
		return 0
	case "serve":
		err = serve(args[1:], env)
	case "share":
		err = shareCmd(args[1:], env)
	case "member":
		err = memberCmd(args[1:], env)
	case "keys":
		err = keysCmd(args[1:], env)
	case "trash":
		err = trashCmd(args[1:], env)
	case "verify":
		err = verifyCmd(args[1:], env)
	case "stats":
		err = statsCmd(args[1:], env)
	case "db":
		err = dbCmd(args[1:], env)
	case "thumbs":
		err = thumbsCmd(args[1:], env)
	case "rsync-gate":
		return rsyncGate(args[1:], env)
	default:
		err = usageError("unknown command %q", args[0])
	}
	var ue usageErr
	switch {
	case err == nil:
		return 0
	case errors.As(err, &ue):
		fmt.Fprintf(env.Stderr, "shfd: %v\n\n%s", err, usage)
		return 2
	default:
		fmt.Fprintf(env.Stderr, "shfd: %v\n", err)
		return 1
	}
}

type usageErr struct{ msg string }

func (e usageErr) Error() string { return e.msg }

func usageError(format string, a ...any) error { return usageErr{fmt.Sprintf(format, a...)} }

// roots are the --data-root and --db-root of a command.
type roots struct {
	data string // folders' storage/, trash/, tmp/ and service.log
	db   string // registry.db, <share-id>/service.db, serve.lock, admin.sock; "" = data
}

// abs returns both roots made absolute, the db-root defaulting to the
// data-root.
func (r roots) abs() (data, db string, err error) { return server.Roots(r.data, r.db) }

// open opens the server on these roots.
func (r roots) open(opts server.Options) (*server.Server, error) {
	opts.DBRoot = r.db
	return server.Open(r.data, opts)
}

// flags parses args where flags and positional arguments may be mixed
// ("member add ID ana --admin") and checks the number of positionals.
// Every command gets --data-root and --db-root.
func flags(fs *flag.FlagSet, args []string, nPos int, env Env) (r roots, pos []string, err error) {
	fs.SetOutput(io.Discard)
	fs.StringVar(&r.data, "data-root", env.Getenv(EnvDataRoot), "data directory")
	fs.StringVar(&r.db, "db-root", env.Getenv(EnvDBRoot), "directory of the SQLite databases, on a local disk (default: the data-root)")
	for {
		if err := fs.Parse(args); err != nil {
			return roots{}, nil, usageError("%s: %v", fs.Name(), err)
		}
		args = fs.Args()
		if len(args) == 0 {
			break
		}
		pos = append(pos, args[0])
		args = args[1:]
	}
	if len(pos) != nPos {
		return roots{}, nil, usageError("%s: want %d argument(s), got %d", fs.Name(), nPos, len(pos))
	}
	if r.data == "" {
		return roots{}, nil, usageError("%s: --data-root or $%s is required", fs.Name(), EnvDataRoot)
	}
	return r, pos, nil
}

func shareCmd(args []string, env Env) error {
	if len(args) == 0 {
		return usageError("share: want create or list")
	}
	switch args[0] {
	case "create":
		fs := flag.NewFlagSet("share create", flag.ContinueOnError)
		admin := fs.String("admin", "admin", "username of the first admin")
		root, pos, err := flags(fs, args[1:], 1, env)
		if err != nil {
			return err
		}
		srv, err := root.open(server.Options{})
		if err != nil {
			return err
		}
		defer srv.Close()
		id, token, err := srv.CreateShare(pos[0], *admin)
		if err != nil {
			return err
		}
		fmt.Fprintf(env.Stdout, "share-id: %s\nadmin:    %s\ntoken:    %s\n\nThe token is shown only now; it is stored as a hash.\n", id, *admin, token)
		return nil
	case "list":
		fs := flag.NewFlagSet("share list", flag.ContinueOnError)
		root, _, err := flags(fs, args[1:], 0, env)
		if err != nil {
			return err
		}
		srv, err := root.open(server.Options{})
		if err != nil {
			return err
		}
		defer srv.Close()
		shares, err := srv.Shares()
		if err != nil {
			return err
		}
		tw := tabwriter.NewWriter(env.Stdout, 0, 4, 2, ' ', 0)
		fmt.Fprintln(tw, "ID\tNAME\tCREATED")
		for _, s := range shares {
			fmt.Fprintf(tw, "%s\t%s\t%s\n", s.ID, s.Name, s.CreatedAt.Format(time.RFC3339))
		}
		return tw.Flush()
	}
	return usageError("share: unknown subcommand %q", args[0])
}

func memberCmd(args []string, env Env) error {
	if len(args) == 0 {
		return usageError("member: want add, revoke, set-role, reset-token or list")
	}
	var sf sshFlags
	open := func(root roots, id string) (*server.Server, *server.Share, error) {
		opts, err := sf.options(root, env)
		if err != nil {
			return nil, nil, err
		}
		opts.SkipRecovery = true
		srv, err := root.open(opts)
		if err != nil {
			return nil, nil, err
		}
		sh, err := srv.Share(id)
		if err != nil {
			srv.Close()
			return nil, nil, err
		}
		return srv, sh, nil
	}
	switch args[0] {
	case "add":
		fs := flag.NewFlagSet("member add", flag.ContinueOnError)
		sf.register(fs, env)
		admin := fs.Bool("admin", false, "make the member an admin")
		root, pos, err := flags(fs, args[1:], 2, env)
		if err != nil {
			return err
		}
		srv, sh, err := open(root, pos[0])
		if err != nil {
			return err
		}
		defer srv.Close()
		token, reactivated, err := sh.AddOrReactivateMember(pos[1], *admin)
		if err != nil {
			return err
		}
		name := pos[1]
		if reactivated {
			name += " (reactivated)"
		}
		printToken(env, name, token)
		if reactivated {
			fmt.Fprintf(env.Stdout, "\n%s was revoked and is back, with the same history. The old token and\nSSH key stay invalid: run shf join with this token on the member's\nmachine, which registers its SSH key again.\n", pos[1])
		}
		return nil
	case "revoke":
		fs := flag.NewFlagSet("member revoke", flag.ContinueOnError)
		sf.register(fs, env)
		root, pos, err := flags(fs, args[1:], 2, env)
		if err != nil {
			return err
		}
		srv, sh, err := open(root, pos[0])
		if err != nil {
			return err
		}
		defer srv.Close()
		if err := sh.RevokeMember(pos[1]); err != nil {
			return err
		}
		fmt.Fprintf(env.Stdout, "revoked %s\n", pos[1])
		return nil
	case "set-role":
		fs := flag.NewFlagSet("member set-role", flag.ContinueOnError)
		sf.register(fs, env)
		root, pos, err := flags(fs, args[1:], 3, env)
		if err != nil {
			return err
		}
		if pos[2] != server.RoleAdmin && pos[2] != server.RoleMember {
			return usageError("member set-role: role must be admin or member, not %q", pos[2])
		}
		srv, sh, err := open(root, pos[0])
		if err != nil {
			return err
		}
		defer srv.Close()
		old, err := sh.SetRole(pos[1], pos[2])
		if err != nil {
			return err
		}
		if old == pos[2] {
			fmt.Fprintf(env.Stdout, "%s: role %s (unchanged)\n", pos[1], old)
		} else {
			fmt.Fprintf(env.Stdout, "%s: role %s -> %s\n", pos[1], old, pos[2])
		}
		return nil
	case "reset-token":
		fs := flag.NewFlagSet("member reset-token", flag.ContinueOnError)
		sf.register(fs, env)
		root, pos, err := flags(fs, args[1:], 2, env)
		if err != nil {
			return err
		}
		srv, sh, err := open(root, pos[0])
		if err != nil {
			return err
		}
		defer srv.Close()
		token, err := sh.ResetToken(pos[1])
		if err != nil {
			return err
		}
		printToken(env, pos[1], token)
		fmt.Fprintf(env.Stdout, "\nThe old token no longer works. The SSH key stays; give the member this\ntoken for their shf client.\n")
		return nil
	case "list":
		fs := flag.NewFlagSet("member list", flag.ContinueOnError)
		sf.register(fs, env)
		root, pos, err := flags(fs, args[1:], 1, env)
		if err != nil {
			return err
		}
		srv, sh, err := open(root, pos[0])
		if err != nil {
			return err
		}
		defer srv.Close()
		ms, err := sh.Members()
		if err != nil {
			return err
		}
		tw := tabwriter.NewWriter(env.Stdout, 0, 4, 2, ' ', 0)
		// SSH KEY is the fingerprint of the member's one registered key: the
		// device that joined last (shf join replaces an earlier device's key).
		fmt.Fprintln(tw, "USERNAME\tROLE\tSTATUS\tLAST SEEN\tLAST PUSH\tACKED SEQ\tSSH KEY")
		for _, m := range ms {
			st := "active"
			if m.RevokedAt != nil {
				st = "revoked " + m.RevokedAt.Format(time.RFC3339)
			}
			fp := "-"
			if m.SSHPubkey != "" {
				fp = authkeys.Fingerprint(m.SSHPubkey)
			}
			fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%d\t%s\n", m.Username, m.Role, st, when(m.LastSeenAt), when(m.LastPushAt), m.LastSeqAcked, fp)
		}
		return tw.Flush()
	}
	return usageError("member: unknown subcommand %q", args[0])
}

// printToken prints a member's new token the way member add always has.
func printToken(env Env, name, token string) {
	fmt.Fprintf(env.Stdout, "member: %s\ntoken:  %s\n\nThe token is shown only now; it is stored as a hash.\n", name, token)
}

func when(t *time.Time) string {
	if t == nil {
		return "-"
	}
	return t.Format(time.RFC3339)
}

func serve(args []string, env Env) error {
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	listen := fs.String("listen", ":8443", "address to listen on")
	cert := fs.String("tls-cert", "", "TLS certificate (PEM)")
	key := fs.String("tls-key", "", "TLS private key (PEM)")
	sshEP := fs.String("ssh-endpoint", env.Getenv(EnvSSHEndpoint), "user@host[:port] of sshd, told to clients that register a key")
	downloads := fs.String("downloads", env.Getenv(EnvDownloads), "directory of the release packages (make release) served at /downloads/")
	var sf sshFlags
	sf.register(fs, env)
	root, _, err := flags(fs, args, 0, env)
	if err != nil {
		return err
	}
	if (*cert == "") != (*key == "") {
		return usageError("serve: --tls-cert and --tls-key go together")
	}
	opts, err := sf.options(root, env)
	if err != nil {
		return err
	}
	if opts.SSH, err = server.ParseSSHEndpoint(*sshEP); err != nil {
		return usageError("serve: %v", err)
	}
	if *downloads != "" {
		if *downloads, err = filepath.Abs(*downloads); err != nil {
			return err
		}
		if fi, err := os.Stat(*downloads); err != nil || !fi.IsDir() {
			return usageError("serve: --downloads %s is not a directory", *downloads)
		}
	}
	srv, err := root.open(opts)
	if err != nil {
		return err
	}
	defer srv.Close()
	unlock, err := lockDataRoot(srv.DBRoot())
	if err != nil {
		return err
	}
	defer unlock()
	log := slog.New(slog.NewTextHandler(env.Stderr, nil))
	warnNetworkDB(srv, env, log)
	if err := srv.OpenAll(); err != nil {
		return err
	}

	ln, err := net.Listen("tcp", *listen)
	if err != nil {
		return err
	}
	if stopAdmin, err := serveAdminSocket(srv, log); err != nil {
		log.Warn("admin socket not available; shfd trash, verify and stats cannot run while this server does", "err", err)
	} else {
		defer stopAdmin()
	}
	api := httpapi.New(srv, log, httpapi.WithDownloads(*downloads))
	hs := &http.Server{
		Handler:           api,
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       2 * time.Minute,
	}
	hs.RegisterOnShutdown(api.Close) // WebSockets are not tracked by Shutdown
	ctx := env.Ctx
	if ctx == nil {
		var stop context.CancelFunc
		ctx, stop = signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer stop()
	}
	errc := make(chan error, 1)
	go func() {
		if *cert != "" {
			errc <- hs.ServeTLS(ln, *cert, *key)
		} else {
			errc <- hs.Serve(ln)
		}
	}()
	scheme := "http"
	if *cert != "" {
		scheme = "https"
	}
	akeys := "-"
	if opts.AuthorizedKeys != nil {
		akeys = opts.AuthorizedKeys.Path
	}
	log.Info("shfd listening", "addr", ln.Addr().String(), "scheme", scheme, "data_root", srv.Root(), "db_root", srv.DBRoot(), "authorized_keys", akeys, "downloads", cmp.Or(*downloads, "-"), "version", version.Version)
	if env.Ready != nil {
		env.Ready(ln.Addr().String())
	}
	select {
	case err := <-errc:
		return err
	case <-ctx.Done():
	}
	sctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := hs.Shutdown(sctx); err != nil {
		return err
	}
	log.Info("shfd stopped")
	return nil
}

// warnNetworkDB logs a warning when the databases are on a network file
// system (NFS, CIFS/SMB, sshfs, ...). SQLite does not recommend WAL or
// POSIX locks there, and an NFS client that stops finishing I/O leaves the
// folder's writer hanging in the kernel, holding every request of that
// folder. serve keeps running; the fix is --db-root on a local disk.
func warnNetworkDB(srv *server.Server, env Env, log *slog.Logger) {
	detect := env.DetectFS
	if detect == nil {
		detect = fsinfo.Detect
	}
	fi, err := detect(srv.DBRoot())
	if err != nil {
		log.Debug("file system of the db-root not detected", "db_root", srv.DBRoot(), "err", err)
		return
	}
	if !fi.Network {
		return
	}
	hint := "put the databases on a local disk: stop shfd, run shfd db move --to /var/lib/shfd/db, then start it with --db-root /var/lib/shfd/db (or SHFD_DB_ROOT); storage/ may stay where it is"
	log.Warn("the SQLite databases are on a network file system; SQLite's WAL and locks are not safe there and a stalled mount hangs the folder's writer",
		"db_root", srv.DBRoot(), "fs", fi.Type, "hint", hint)
}
