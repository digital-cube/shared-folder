// Package rsyncssh is the client side of the rsync-over-SSH Transport
// (docs/specifikacija.md, section 5). It runs the system's rsync and ssh;
// on the server, sshd hands every connection made with the member's key to
// "shfd rsync-gate", which allows exactly these two transfers:
//
//	upload    rsync --partial … <src> host:tmp/<upload_id>
//	download  rsync --inplace … host:storage/<path> <dir>/.shf-tmp-part-<hash>
//
// An interrupted upload leaves its partial content as tmp/<upload_id> on
// the server (--partial); sending again resumes from it through rsync's
// delta algorithm instead of starting from zero (the server hands out the
// same upload_id when the same content is announced again). A download
// goes into a temporary file next to its destination, named after the
// content's hash and kept when the download is interrupted, so the next
// one resumes it the same way; its SHA-256 is checked, and only then is it
// renamed over the destination.
//
// ssh runs with the member's key only (no agent, no ~/.ssh/config),
// BatchMode, and its own known_hosts in the client's state directory: the
// server's host key is remembered on the first connection and must match
// on every later one. The user's ~/.ssh/known_hosts is never read or
// written.
//
// Transfers share one SSH connection (OpenSSH multiplexing: ControlMaster
// auto, ControlPersist): the first transfer opens a master connection
// that stays up for ControlPersist after the last one, and every later
// transfer is only a new session on it, without a new key exchange and
// authentication. sshd still applies the forced command to every session,
// so each transfer goes through rsync-gate exactly as before, and a
// revoked member is refused on the open connection too (rsync-gate checks
// the member on every call). Close asks the master to stop, and so does
// a transfer that finds the member refused or the connection broken. The master's
// socket lives in the state directory, or, when that path is too long for
// a unix socket, in a private directory under $XDG_RUNTIME_DIR or /tmp.
package rsyncssh

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/digital-cube/shared-folder/internal/hashx"
	"github.com/digital-cube/shared-folder/internal/ignore"
	"github.com/digital-cube/shared-folder/internal/pathx"
	"github.com/digital-cube/shared-folder/internal/transport"
)

// Errors a caller can branch on with errors.Is. transport.ErrChanged
// (fetch again) is returned as well.
var (
	// ErrNotInstalled: rsync or ssh is missing or too old on this machine.
	ErrNotInstalled = errors.New("rsync/ssh not available")
	// ErrHostKey: the server's host key differs from the one remembered
	// in known_hosts. Never retried automatically.
	ErrHostKey = errors.New("server host key does not match the remembered one")
	// ErrAuth: the server did not accept the member's key (not registered,
	// or the member was revoked: refused by sshd on a new connection, by
	// rsync-gate on a shared one).
	ErrAuth = errors.New("server refused the SSH key")
	// ErrDenied: rsync-gate refused the transfer.
	ErrDenied = errors.New("transfer refused by the server")
	// ErrUnreachable: the connection failed or timed out; try again later.
	ErrUnreachable = errors.New("server unreachable")
)

// MinRsyncVersion is the oldest rsync whose remote-argument escaping
// rsync-gate understands (3.2.4 started backslash-escaping file names).
const MinRsyncVersion = "3.2.4"

// Config of a Transport for one folder.
type Config struct {
	ShareID string
	Host    string // name or address of the server
	Port    int    // SSH port; 0 means 22
	User    string // the system user shfd runs as on the server
	KeyFile string // the member's private key
	// StateDir is the client's state directory for this folder; the
	// server's host key is remembered in StateDir/known_hosts.
	StateDir string

	// ConnectTimeout bounds the SSH handshake (default 20s); IOTimeout is
	// rsync's --timeout: the transfer fails after that long without data
	// (default 60s). The context passed to Send/Fetch bounds the whole.
	ConnectTimeout time.Duration
	IOTimeout      time.Duration
	// BwLimit is rsync's --bwlimit in KiB/s; 0 means unlimited.
	BwLimit int

	// Rsync and SSH are the binaries; "" takes $SHF_RSYNC and $SHF_SSH,
	// and when those are not set either, looks them up in PATH.
	Rsync, SSH string

	// ControlPersist is how long the shared SSH connection stays open
	// after its last transfer (default 60s). NoMultiplex gives every
	// transfer a connection of its own instead.
	ControlPersist time.Duration
	NoMultiplex    bool

	// OnTransfer, when set, receives rsync's statistics of every
	// successful transfer.
	OnTransfer func(Stats)
}

// Stats is what rsync --stats reported for one transfer.
type Stats struct {
	Direction string // "upload" or "download"
	Path      string // remote path: tmp/<upload_id> or storage/<path>
	Size      int64  // total file size
	Literal   int64  // bytes that had to be sent
	Matched   int64  // bytes the receiver already had (resumed or unchanged)
}

// Transport moves files with rsync over SSH.
type Transport struct {
	cfg        Config
	rsync, ssh string
	// ctl is the master connection's control socket; "" when transfers
	// do not share a connection.
	ctl string
}

var _ transport.Transport = (*Transport)(nil)

var (
	shareIDRE  = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)
	uploadIDRE = regexp.MustCompile(`^[0-9a-f]{32}$`)
	versionRE  = regexp.MustCompile(`rsync\s+version\s+v?(\d+)\.(\d+)\.(\d+)`)
	hashRE     = regexp.MustCompile(`^[0-9a-fA-F]{64}$`)
)

// PartialPrefix starts the name of a download in progress, kept when it is
// interrupted so the next attempt resumes it: PartialPrefix + the SHA-256
// of the content. It is one of the client's temporary files, which the
// scan never takes for the user's.
const PartialPrefix = ignore.ClientTempPrefix + "part-"

// New checks the configuration and that rsync (new enough) and ssh are
// installed.
func New(cfg Config) (*Transport, error) {
	switch {
	case !shareIDRE.MatchString(cfg.ShareID):
		return nil, fmt.Errorf("rsyncssh: invalid share id %q", cfg.ShareID)
	case cfg.Host == "" || strings.ContainsAny(cfg.Host, " \t\n/@") || strings.HasPrefix(cfg.Host, "-"):
		return nil, fmt.Errorf("rsyncssh: invalid host %q", cfg.Host)
	case cfg.User == "" || strings.ContainsAny(cfg.User, " \t\n@:/") || strings.HasPrefix(cfg.User, "-"):
		return nil, fmt.Errorf("rsyncssh: invalid user %q", cfg.User)
	case cfg.Port < 0 || cfg.Port > 65535:
		return nil, fmt.Errorf("rsyncssh: invalid port %d", cfg.Port)
	case cfg.KeyFile == "" || cfg.StateDir == "":
		return nil, errors.New("rsyncssh: KeyFile and StateDir are required")
	}
	if cfg.Port == 0 {
		cfg.Port = 22
	}
	if cfg.ConnectTimeout <= 0 {
		cfg.ConnectTimeout = 20 * time.Second
	}
	if cfg.IOTimeout <= 0 {
		cfg.IOTimeout = 60 * time.Second
	}
	if cfg.ControlPersist <= 0 {
		cfg.ControlPersist = 60 * time.Second
	}
	var err error
	for _, p := range []*string{&cfg.KeyFile, &cfg.StateDir} {
		if *p, err = filepath.Abs(*p); err != nil {
			return nil, err
		}
	}
	if strings.ContainsAny(cfg.StateDir, "\"\n") {
		return nil, fmt.Errorf("rsyncssh: state directory %q contains a double quote or newline", cfg.StateDir)
	}
	if _, err := os.Stat(cfg.KeyFile); err != nil {
		return nil, fmt.Errorf("rsyncssh: SSH key: %w", err)
	}
	t := &Transport{cfg: cfg}
	tools, err := FindTools(cfg.Rsync, cfg.SSH)
	if err != nil {
		return nil, err
	}
	t.rsync, t.ssh = tools.Rsync, tools.SSH
	if !cfg.NoMultiplex && multiplexSupported {
		t.ctl = controlPath(cfg)
	}
	return t, nil
}

// maxControlPath is the longest control socket path that works on every
// unix: sun_path holds 104 bytes on macOS and the BSDs (108 on Linux),
// including the terminating NUL, and ssh binds the master to the path
// plus "." and 16 random characters before renaming it into place.
const maxControlPath = 104 - 1 - 17

// controlPath picks where the master connection's socket goes, or "" when
// no candidate is short enough. The name is a hash of everything that
// makes a connection: a changed server, user or key never reuses a master
// opened for the old one.
func controlPath(cfg Config) string {
	key, _ := os.ReadFile(cfg.KeyFile)
	h := sha256.New()
	for _, s := range []string{cfg.Host, strconv.Itoa(cfg.Port), cfg.User, cfg.KeyFile, string(key), cfg.StateDir} {
		h.Write([]byte(s))
		h.Write([]byte{0})
	}
	sum := hex.EncodeToString(h.Sum(nil))[:16]
	candidates := []string{filepath.Join(cfg.StateDir, "ssh-"+sum)}
	if d := os.Getenv("XDG_RUNTIME_DIR"); filepath.IsAbs(d) {
		candidates = append(candidates, filepath.Join(d, "shf", sum))
	}
	candidates = append(candidates, filepath.Join("/tmp", "shf-"+strconv.Itoa(os.Getuid()), sum))
	for _, c := range candidates {
		if len(c) <= maxControlPath && !strings.ContainsAny(c, "\"\n") {
			return c
		}
	}
	return ""
}

// prepareControlDir makes the directory of the control socket. Outside the
// state directory it must be a private directory of this user: a socket
// planted there by somebody else would see every transfer.
func (t *Transport) prepareControlDir() error {
	dir := filepath.Dir(t.ctl)
	if dir == t.cfg.StateDir {
		return nil // made by prepareState
	}
	if err := os.MkdirAll(filepath.Dir(dir), 0o700); err != nil {
		return err
	}
	if err := os.Mkdir(dir, 0o700); err != nil && !errors.Is(err, os.ErrExist) {
		return err
	}
	return checkPrivateDir(dir)
}

// Environment variables naming the rsync and ssh binaries, read when
// Config leaves them empty. "shf service install" writes the absolute
// paths it found into the service definition through them, because a
// service manager starts the daemon with a PATH of its own (launchd's
// lacks Homebrew, so it finds Apple's openrsync in /usr/bin).
const (
	EnvRsync = "SHF_RSYNC"
	EnvSSH   = "SHF_SSH"
)

// Tools are the absolute paths of the rsync and ssh a Transport runs.
type Tools struct{ Rsync, SSH string }

// FindTools finds rsync and ssh the way New does: the given name or path,
// else $SHF_RSYNC / $SHF_SSH, else "rsync" / "ssh" in PATH; and checks that
// rsync is MinRsyncVersion or newer. Errors wrap ErrNotInstalled.
func FindTools(rsync, ssh string) (Tools, error) {
	var t Tools
	var err error
	if rsync == "" {
		rsync = os.Getenv(EnvRsync)
	}
	if ssh == "" {
		ssh = os.Getenv(EnvSSH)
	}
	if t.Rsync, err = lookPath(rsync, "rsync"); err != nil {
		return Tools{}, err
	}
	if t.SSH, err = lookPath(ssh, "ssh"); err != nil {
		return Tools{}, err
	}
	if err := checkRsyncVersion(t.Rsync); err != nil {
		return Tools{}, err
	}
	return t, nil
}

func lookPath(given, name string) (string, error) {
	if given == "" {
		given = name
	}
	p, err := exec.LookPath(given)
	if err != nil {
		return "", fmt.Errorf("%w: %s not found (%v); install it with your package manager, e.g. \"apt install %s\" or \"brew install %s\"",
			ErrNotInstalled, name, err, name, name)
	}
	if abs, err := filepath.Abs(p); err == nil {
		p = abs
	}
	return p, nil
}

// goos is runtime.GOOS; tests replace it to see another platform's message.
var goos = runtime.GOOS

// macOSRsyncHint is what a Mac user does about Apple's openrsync.
const macOSRsyncHint = `install rsync with "brew install rsync" and put Homebrew ahead of /usr/bin in PATH ` +
	`(eval "$(/opt/homebrew/bin/brew shellenv)" in your shell profile; /usr/local/bin/brew on an Intel Mac), ` +
	`then run "shf service install" again if the background client is installed`

func checkRsyncVersion(bin string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, bin, "--version").Output()
	if err != nil {
		return fmt.Errorf("%w: running %s --version: %v", ErrNotInstalled, bin, err)
	}
	m := versionRE.FindSubmatch(out)
	var v [3]int
	if m != nil {
		for i := range v {
			v[i], _ = strconv.Atoi(string(m[i+1]))
		}
	}
	// openrsync (macOS 15+ ships it as /usr/bin/rsync) reports itself as
	// "openrsync: protocol version 29" and "rsync version 2.6.9 compatible";
	// older macOS had the real rsync 2.6.9, just as unusable.
	openrsync := bytes.Contains(out, []byte("openrsync"))
	if goos == "darwin" && (openrsync || v == [3]int{2, 6, 9}) {
		what := "Apple's openrsync"
		if !openrsync {
			what = "Apple's rsync 2.6.9"
		}
		return fmt.Errorf("%w: %s is %s; shf needs rsync %s or newer: %s",
			ErrNotInstalled, bin, what, MinRsyncVersion, macOSRsyncHint)
	}
	if openrsync || m == nil {
		first, _, _ := strings.Cut(string(out), "\n")
		return fmt.Errorf("%w: %s is not the rsync shf needs (%q); install rsync %s or newer (on macOS: brew install rsync)",
			ErrNotInstalled, bin, first, MinRsyncVersion)
	}
	if v[0]*1_000_000+v[1]*1000+v[2] < 3_002_004 {
		return fmt.Errorf("%w: rsync %d.%d.%d is too old (%s); shf needs %s or newer", ErrNotInstalled, v[0], v[1], v[2], bin, MinRsyncVersion)
	}
	return nil
}

// ControlPath is the shared connection's control socket, "" when every
// transfer connects on its own.
func (t *Transport) ControlPath() string { return t.ctl }

// KnownHosts is where the server's host key is remembered.
func (t *Transport) KnownHosts() string { return filepath.Join(t.cfg.StateDir, "known_hosts") }

// sshArgs is the ssh command line (without the destination host). With
// mux the connection is shared through the control socket.
func (t *Transport) sshArgs(mux bool) []string {
	c := t.cfg
	a := []string{
		t.ssh,
		"-F", os.DevNull, // ignore ~/.ssh/config and /etc/ssh/ssh_config
		"-i", c.KeyFile,
		"-p", strconv.Itoa(c.Port),
		"-l", c.User,
		"-o", "IdentitiesOnly=yes",
		"-o", "IdentityAgent=none",
		"-o", "BatchMode=yes",
		"-o", "PasswordAuthentication=no",
		"-o", "KbdInteractiveAuthentication=no",
		"-o", "StrictHostKeyChecking=accept-new",
		// ssh splits -o values on blanks (UserKnownHostsFile takes a
		// list); its config quoting keeps a path with spaces whole.
		"-o", `UserKnownHostsFile="` + t.KnownHosts() + `"`,
		"-o", "GlobalKnownHostsFile=" + os.DevNull,
		"-o", "UpdateHostKeys=no",
		"-o", "ConnectTimeout=" + strconv.Itoa(int(c.ConnectTimeout.Round(time.Second)/time.Second)),
		"-o", "ServerAliveInterval=15",
		"-o", "ServerAliveCountMax=3",
		"-o", "ForwardAgent=no",
		"-o", "ForwardX11=no",
		"-o", "ClearAllForwardings=yes",
		"-o", "RequestTTY=no",
		"-o", "LogLevel=ERROR",
	}
	if !mux || t.ctl == "" {
		return append(a, "-o", "ControlMaster=no", "-o", "ControlPath=none")
	}
	return append(a,
		"-o", "ControlMaster=auto",
		// ssh expands %-tokens in the path and splits on blanks.
		"-o", `ControlPath="`+strings.ReplaceAll(t.ctl, "%", "%%")+`"`,
		"-o", "ControlPersist="+strconv.Itoa(max(1, int(c.ControlPersist.Round(time.Second)/time.Second))),
	)
}

// rshQuote quotes a word for rsync's own splitting of the -e string:
// inside '…' a doubled quote stands for one.
func rshQuote(w string) string {
	if w != "" && !strings.ContainsAny(w, " \t\n'\"\\") {
		return w
	}
	return "'" + strings.ReplaceAll(w, "'", "''") + "'"
}

func (t *Transport) rsh() string {
	args := t.sshArgs(true)
	for i, a := range args {
		args[i] = rshQuote(a)
	}
	return strings.Join(args, " ")
}

func (t *Transport) remote(p string) string {
	h := t.cfg.Host
	if strings.Contains(h, ":") {
		h = "[" + h + "]" // IPv6 address
	}
	return h + ":" + p
}

func (t *Transport) baseArgs() []string {
	a := []string{
		"--timeout=" + strconv.Itoa(int(t.cfg.IOTimeout.Round(time.Second)/time.Second)),
		"--stats",
		"-e", t.rsh(),
	}
	if t.cfg.BwLimit > 0 {
		a = append(a, "--bwlimit="+strconv.Itoa(t.cfg.BwLimit))
	}
	return a
}

// Send delivers src as the content of upload u. An interrupted Send leaves
// partial content on the server; calling Send again for the same upload
// continues from it.
func (t *Transport) Send(ctx context.Context, u transport.Upload, src string) error {
	if u.ShareID != t.cfg.ShareID {
		return fmt.Errorf("rsyncssh: upload is for folder %s, this transport is for %s", u.ShareID, t.cfg.ShareID)
	}
	if !uploadIDRE.MatchString(u.UploadID) || u.Dest != "tmp/"+u.UploadID {
		return fmt.Errorf("rsyncssh: invalid upload target %q / %q", u.UploadID, u.Dest)
	}
	abs, err := filepath.Abs(src)
	if err != nil {
		return err
	}
	fi, err := os.Stat(abs)
	if err != nil {
		return err
	}
	if !fi.Mode().IsRegular() {
		return fmt.Errorf("rsyncssh: %s is not a regular file", src)
	}
	if err := t.prepareState(); err != nil {
		return err
	}
	args := append(t.baseArgs(), "--partial", "--", abs, t.remote(u.Dest))
	st, err := t.run(ctx, args, u.Dest)
	if err != nil {
		return err
	}
	st.Direction = "upload"
	t.report(st)
	return nil
}

// Fetch downloads storage/<p> into a temporary file in the directory of
// dst (PartialPrefix + hash), checks that its SHA-256 is hash and renames
// it to dst. An interrupted Fetch leaves the temporary file, and fetching
// the same content again resumes it.
// When the server's file is gone or has other content (replaced between
// the listing and the download) it returns transport.ErrChanged and
// leaves dst untouched; the caller fetches the newer version.
func (t *Transport) Fetch(ctx context.Context, shareID, p, hash, dst string) error {
	if shareID != t.cfg.ShareID {
		return fmt.Errorf("rsyncssh: fetch from folder %s, this transport is for %s", shareID, t.cfg.ShareID)
	}
	norm, err := pathx.Normalize(p)
	if err != nil || norm != p {
		return fmt.Errorf("rsyncssh: invalid path %q", p)
	}
	if err := t.prepareState(); err != nil {
		return err
	}
	// The temporary file is named after the content. An interrupted
	// download (a lost connection, the client stopped or killed) leaves it
	// in place, and the next Fetch of the same content continues from it:
	// rsync takes what is there as the basis and sends only the rest.
	var tmpName string
	if hashRE.MatchString(hash) {
		tmpName = filepath.Join(filepath.Dir(dst), PartialPrefix+strings.ToLower(hash))
		f, err := os.OpenFile(tmpName, os.O_CREATE|os.O_WRONLY, 0o600)
		if err != nil {
			return err
		}
		f.Close()
	} else {
		tmp, err := os.CreateTemp(filepath.Dir(dst), ignore.ClientTempPrefix+"*")
		if err != nil {
			return err
		}
		tmpName = tmp.Name()
		tmp.Close()
		defer os.Remove(tmpName) // a no-op after the rename
	}

	remote := "storage/" + p
	// --inplace writes straight into our temporary file (no second
	// temporary name of rsync's own); -I transfers even when size and
	// time happen to match the placeholder.
	args := append(t.baseArgs(), "--inplace", "-I", "--", t.remote(remote), tmpName)
	st, err := t.run(ctx, args, remote)
	if err != nil {
		if errors.Is(err, transport.ErrChanged) {
			os.Remove(tmpName) // that content is gone from the server
		}
		return err
	}
	got, err := hashx.File(tmpName)
	if err != nil {
		return err
	}
	if !strings.EqualFold(got, hash) {
		os.Remove(tmpName)
		return fmt.Errorf("%s: downloaded content has hash %s, wanted %s: %w", p, got, hash, transport.ErrChanged)
	}
	if err := os.Rename(tmpName, dst); err != nil {
		return err
	}
	st.Direction = "download"
	t.report(st)
	return nil
}

// Probe connects once with the member's key and returns when sshd
// accepted it. On the first connection that remembers the server's host
// key in StateDir/known_hosts (shf join does this, so a later transfer
// never meets an unknown host). The forced command refuses the probe's
// command, which is expected: only a failure of ssh itself (exit status
// 255) is an error. The probe never uses a shared connection: it checks
// that the key is accepted now.
func (t *Transport) Probe(ctx context.Context) error {
	if err := t.prepareState(); err != nil {
		return err
	}
	var stderr bytes.Buffer
	args := append(t.sshArgs(false)[1:], "--", t.cfg.Host, "shf-probe")
	cmd := exec.CommandContext(ctx, t.ssh, args...)
	cmd.Stdin = nil
	cmd.Stdout = io.Discard
	cmd.Stderr = &limitedWriter{w: &stderr, n: 64 << 10}
	cmd.Env = cleanEnv(os.Environ())
	setProcessGroup(cmd)
	cmd.WaitDelay = 5 * time.Second
	err := cmd.Run()
	var ee *exec.ExitError
	if err == nil || (errors.As(err, &ee) && ee.ExitCode() != 255) {
		return nil
	}
	if ctx.Err() != nil {
		return fmt.Errorf("ssh probe: %w", ctx.Err())
	}
	return classify("(probe)", err, stderr.String())
}

func (t *Transport) report(st Stats) {
	if t.cfg.OnTransfer != nil {
		t.cfg.OnTransfer(st)
	}
}

func (t *Transport) prepareState() error {
	if err := os.MkdirAll(t.cfg.StateDir, 0o700); err != nil {
		return err
	}
	if t.ctl != "" {
		if err := t.prepareControlDir(); err != nil {
			// Not fatal: transfers go without a shared connection.
			t.ctl = ""
		}
	}
	return nil
}

// Close stops the shared SSH connection, if one is open: it accepts no new
// transfers, and exits as soon as those still running (of another process
// of the same folder, say) are done. The Transport stays usable; the next
// transfer opens a new connection.
func (t *Transport) Close() error {
	t.stopMaster()
	return nil
}

func (t *Transport) stopMaster() {
	if t.ctl == "" {
		return
	}
	if _, err := os.Lstat(t.ctl); err != nil {
		return // no master
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	args := append(t.sshArgs(true)[1:], "-O", "stop", "--", t.cfg.Host)
	cmd := exec.CommandContext(ctx, t.ssh, args...)
	cmd.Stdout, cmd.Stderr = io.Discard, io.Discard
	cmd.Env = cleanEnv(os.Environ())
	cmd.WaitDelay = 2 * time.Second
	cmd.Run() // best effort: a stale socket or a master already gone is fine
}

// run executes rsync and turns its failures into the package's errors.
func (t *Transport) run(ctx context.Context, args []string, remotePath string) (Stats, error) {
	var stdout, stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, t.rsync, args...)
	cmd.Stdout = &stdout
	cmd.Stderr = &limitedWriter{w: &stderr, n: 64 << 10}
	cmd.Env = cleanEnv(os.Environ())
	setProcessGroup(cmd)
	cmd.WaitDelay = 5 * time.Second
	err := cmd.Run()
	if err != nil {
		if ctx.Err() != nil {
			return Stats{}, fmt.Errorf("rsync %s: %w", remotePath, ctx.Err())
		}
		err = classify(remotePath, err, stderr.String())
		if errors.Is(err, ErrAuth) || errors.Is(err, ErrUnreachable) {
			// Revoked: nothing more goes over this connection. Or the
			// connection itself failed (a dead link after a network
			// change hangs until --timeout): the next try connects anew.
			t.stopMaster()
		}
		return Stats{}, err
	}
	st := parseStats(stdout.String())
	st.Path = remotePath
	return st, nil
}

// cleanEnv drops variables that would change what rsync sends: -s
// (secluded args) hides the arguments from rsync-gate, --old-args changes
// the escaping it expects. LC_ALL=C keeps --stats parseable.
func cleanEnv(env []string) []string {
	out := env[:0:0]
	for _, kv := range env {
		k, _, _ := strings.Cut(kv, "=")
		switch k {
		case "RSYNC_PROTECT_ARGS", "RSYNC_OLD_ARGS", "RSYNC_RSH", "LC_ALL":
			continue
		}
		out = append(out, kv)
	}
	return append(out, "LC_ALL=C")
}

var gateRE = regexp.MustCompile(`shfd rsync-gate: denied \(([a-z_]+)\): ([^\n]*)`)

func classify(remotePath string, err error, stderr string) error {
	msg := strings.TrimSpace(stderr)
	if len(msg) > 2000 {
		msg = msg[:2000] + "…"
	}
	wrap := func(kind error, detail string) error {
		return fmt.Errorf("rsync %s: %w: %s", remotePath, kind, detail)
	}
	if m := gateRE.FindStringSubmatch(stderr); m != nil {
		switch m[1] {
		case "not_found":
			return wrap(transport.ErrChanged, m[2])
		case "member":
			// Unknown or revoked: what sshd says on a new connection
			// once the key is gone, said here on a shared one.
			return wrap(ErrAuth, m[1]+": "+m[2])
		}
		return wrap(ErrDenied, m[1]+": "+m[2])
	}
	switch {
	case strings.Contains(stderr, "REMOTE HOST IDENTIFICATION HAS CHANGED"),
		strings.Contains(stderr, "Host key verification failed"):
		return wrap(ErrHostKey, msg)
	case strings.Contains(stderr, "Permission denied (publickey"),
		strings.Contains(stderr, "Too many authentication failures"):
		return wrap(ErrAuth, msg)
	case strings.Contains(stderr, "Connection refused"),
		strings.Contains(stderr, "Connection timed out"),
		strings.Contains(stderr, "Operation timed out"),
		strings.Contains(stderr, "No route to host"),
		strings.Contains(stderr, "Network is unreachable"),
		strings.Contains(stderr, "Could not resolve hostname"),
		strings.Contains(stderr, "Connection reset"),
		strings.Contains(stderr, "Connection closed by"),
		strings.Contains(stderr, "timeout in data send/receive"),
		strings.Contains(stderr, "io timeout"),
		strings.Contains(stderr, "Broken pipe"),
		strings.Contains(stderr, "connection unexpectedly closed"):
		return wrap(ErrUnreachable, msg)
	}
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return fmt.Errorf("rsync %s: %s: %s", remotePath, ee, msg)
	}
	return fmt.Errorf("rsync %s: %w", remotePath, err)
}

var statRE = regexp.MustCompile(`(?m)^(Total file size|Literal data|Matched data): ([0-9,.]+) bytes`)

func parseStats(out string) Stats {
	var st Stats
	for _, m := range statRE.FindAllStringSubmatch(out, -1) {
		n, _ := strconv.ParseInt(strings.NewReplacer(",", "", ".", "").Replace(m[2]), 10, 64)
		switch m[1] {
		case "Total file size":
			st.Size = n
		case "Literal data":
			st.Literal = n
		case "Matched data":
			st.Matched = n
		}
	}
	return st
}

// limitedWriter keeps the first n bytes and drops the rest.
type limitedWriter struct {
	w io.Writer
	n int
}

func (l *limitedWriter) Write(p []byte) (int, error) {
	if l.n > 0 {
		k := min(len(p), l.n)
		l.w.Write(p[:k])
		l.n -= k
	}
	return len(p), nil
}
