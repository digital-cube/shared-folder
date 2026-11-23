// Package rsyncssh is the client side of the rsync-over-SSH Transport
// (docs/specifikacija.md, section 5). It runs the system's rsync and ssh;
// on the server, sshd hands every connection made with the member's key to
// "shfd rsync-gate", which allows exactly these two transfers:
//
//	upload    rsync --partial … <src> host:tmp/<upload_id>
//	download  rsync --inplace … host:storage/<path> <dir>/.shf-tmp-*
//
// An interrupted upload leaves its partial content as tmp/<upload_id> on
// the server (--partial); sending again resumes from it through rsync's
// delta algorithm instead of starting from zero. A download goes into a
// temporary file next to its destination, its SHA-256 is checked, and only
// then is it renamed over the destination.
//
// ssh runs with the member's key only (no agent, no ~/.ssh/config),
// BatchMode, and its own known_hosts in the client's state directory: the
// server's host key is remembered on the first connection and must match
// on every later one. The user's ~/.ssh/known_hosts is never read or
// written.
package rsyncssh

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
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
	// or the member was revoked).
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

	// Rsync and SSH are the binaries; "" looks them up in PATH.
	Rsync, SSH string

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
}

var _ transport.Transport = (*Transport)(nil)

var (
	shareIDRE  = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)
	uploadIDRE = regexp.MustCompile(`^[0-9a-f]{32}$`)
	versionRE  = regexp.MustCompile(`rsync\s+version\s+v?(\d+)\.(\d+)\.(\d+)`)
)

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
	if t.rsync, err = lookPath(cfg.Rsync, "rsync"); err != nil {
		return nil, err
	}
	if t.ssh, err = lookPath(cfg.SSH, "ssh"); err != nil {
		return nil, err
	}
	if err := checkRsyncVersion(t.rsync); err != nil {
		return nil, err
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
	return p, nil
}

func checkRsyncVersion(bin string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, bin, "--version").Output()
	if err != nil {
		return fmt.Errorf("%w: running %s --version: %v", ErrNotInstalled, bin, err)
	}
	m := versionRE.FindSubmatch(out)
	if m == nil {
		first, _, _ := strings.Cut(string(out), "\n")
		return fmt.Errorf("%w: %s is not the rsync shf needs (%q); install rsync %s or newer (on macOS: brew install rsync)",
			ErrNotInstalled, bin, first, MinRsyncVersion)
	}
	var v [3]int
	for i := range v {
		v[i], _ = strconv.Atoi(string(m[i+1]))
	}
	if v[0]*1_000_000+v[1]*1000+v[2] < 3_002_004 {
		return fmt.Errorf("%w: rsync %d.%d.%d is too old; shf needs %s or newer", ErrNotInstalled, v[0], v[1], v[2], MinRsyncVersion)
	}
	return nil
}

// KnownHosts is where the server's host key is remembered.
func (t *Transport) KnownHosts() string { return filepath.Join(t.cfg.StateDir, "known_hosts") }

// sshArgs is the ssh command line (without the destination host).
func (t *Transport) sshArgs() []string {
	c := t.cfg
	return []string{
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
	args := t.sshArgs()
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

// Fetch downloads storage/<p> into a temporary .shf-tmp-* file in the
// directory of dst, checks that its SHA-256 is hash and renames it to dst.
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
	tmp, err := os.CreateTemp(filepath.Dir(dst), ignore.ClientTempPrefix+"*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	tmp.Close()
	defer os.Remove(tmpName) // a no-op after the rename

	remote := "storage/" + p
	// --inplace writes straight into our temporary file (no second
	// temporary name of rsync's own); -I transfers even when size and
	// time happen to match the empty placeholder.
	args := append(t.baseArgs(), "--inplace", "-I", "--", t.remote(remote), tmpName)
	st, err := t.run(ctx, args, remote)
	if err != nil {
		return err
	}
	got, err := hashx.File(tmpName)
	if err != nil {
		return err
	}
	if !strings.EqualFold(got, hash) {
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
// 255) is an error.
func (t *Transport) Probe(ctx context.Context) error {
	if err := t.prepareState(); err != nil {
		return err
	}
	var stderr bytes.Buffer
	args := append(t.sshArgs()[1:], "--", t.cfg.Host, "shf-probe")
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
	return os.MkdirAll(t.cfg.StateDir, 0o700)
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
		return Stats{}, classify(remotePath, err, stderr.String())
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
		if m[1] == "not_found" {
			return wrap(transport.ErrChanged, m[2])
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
