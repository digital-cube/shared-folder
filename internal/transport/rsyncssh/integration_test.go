package rsyncssh

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"net"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/digital-cube/shared-folder/internal/authkeys"
	"github.com/digital-cube/shared-folder/internal/hashx"
	"github.com/digital-cube/shared-folder/internal/server"
	"github.com/digital-cube/shared-folder/internal/shfdcli"
	"github.com/digital-cube/shared-folder/internal/transport"
)

// The test binary doubles as shfd: sshd runs "<test binary> rsync-gate …"
// as the forced command of the member's key.
func TestMain(m *testing.M) {
	if len(os.Args) > 1 && os.Args[1] == "rsync-gate" {
		os.Exit(shfdcli.Main(os.Args[1:]))
	}
	os.Exit(m.Run())
}

func findSSHD() string {
	if p, err := exec.LookPath("sshd"); err == nil {
		return p
	}
	for _, p := range []string{"/usr/sbin/sshd", "/usr/local/sbin/sshd", "/opt/homebrew/sbin/sshd"} {
		if fi, err := os.Stat(p); err == nil && !fi.IsDir() {
			return p
		}
	}
	return ""
}

// world is a server data-root with one folder, an sshd in front of it and
// a client Transport for the folder's admin "ana".
type world struct {
	t        *testing.T
	root     string
	srv      *server.Server
	sh       *server.Share
	ana      *server.Member
	keys     *authkeys.File
	port     int
	stateDir string
	keyFile  string
	pubKey   string
	sshdLog  *syncBuffer
}

func newWorld(t *testing.T) *world {
	t.Helper()
	if testing.Short() {
		t.Skip("integration test; skipped with -short")
	}
	sshd := findSSHD()
	for _, bin := range []string{"rsync", "ssh", "ssh-keygen"} {
		if _, err := exec.LookPath(bin); err != nil {
			t.Skipf("%s not installed", bin)
		}
	}
	if sshd == "" {
		t.Skip("sshd not installed")
	}
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	u, err := user.Current()
	if err != nil {
		t.Skip("no current user:", err)
	}

	w := &world{t: t, root: t.TempDir(), sshdLog: &syncBuffer{}}
	sshDir := t.TempDir()
	w.keys = &authkeys.File{
		Path:    filepath.Join(sshDir, "authorized_keys"),
		Command: []string{self, "rsync-gate", "--data-root", w.root},
	}
	// A line of the admin's own that must survive.
	os.WriteFile(w.keys.Path, []byte("# not shfd's\n"), 0o600)

	w.srv, err = server.Open(w.root, server.Options{AuthorizedKeys: w.keys})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { w.srv.Close() })
	id, token, err := w.srv.CreateShare("tim", "ana")
	if err != nil {
		t.Fatal(err)
	}
	w.sh, _ = w.srv.Share(id)
	w.ana, _ = w.sh.Authenticate(token)

	// The client's state directory has a space in it, as on macOS.
	w.stateDir = filepath.Join(t.TempDir(), "state dir", id)
	w.keyFile = filepath.Join(w.stateDir, "id_ed25519")
	w.pubKey, err = GenerateKey(context.Background(), w.keyFile, "ana@test")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.sh.SetSSHKey(w.ana, w.pubKey); err != nil {
		t.Fatal(err)
	}

	hostKey := filepath.Join(sshDir, "host_ed25519")
	if out, err := exec.Command("ssh-keygen", "-q", "-t", "ed25519", "-N", "", "-f", hostKey).CombinedOutput(); err != nil {
		t.Fatalf("ssh-keygen: %v %s", err, out)
	}
	w.port = w.startSSHD(sshd, sshDir, hostKey, u.Username)
	return w
}

func (w *world) startSSHD(sshd, dir, hostKey, username string) int {
	t := w.t
	for attempt := 0; attempt < 3; attempt++ {
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Skip("no loopback:", err)
		}
		port := ln.Addr().(*net.TCPAddr).Port
		ln.Close()
		cfg := filepath.Join(dir, "sshd_config")
		os.WriteFile(cfg, []byte(fmt.Sprintf(`Port %d
ListenAddress 127.0.0.1
HostKey %s
AuthorizedKeysFile %s
PidFile %s
AllowUsers %s
StrictModes no
UsePAM no
PasswordAuthentication no
KbdInteractiveAuthentication no
PubkeyAuthentication yes
AuthenticationMethods publickey
PermitTTY no
AllowTcpForwarding no
X11Forwarding no
LogLevel VERBOSE
`, port, hostKey, w.keys.Path, filepath.Join(dir, "sshd.pid"), username)), 0o600)
		ctx, cancel := context.WithCancel(context.Background())
		cmd := exec.CommandContext(ctx, sshd, "-D", "-e", "-f", cfg)
		cmd.WaitDelay = 5 * time.Second
		cmd.Stdout, cmd.Stderr = w.sshdLog, w.sshdLog
		if err := cmd.Start(); err != nil {
			cancel()
			t.Skip("cannot start sshd:", err)
		}
		exited := make(chan struct{})
		go func() { cmd.Wait(); close(exited) }()
		t.Cleanup(func() {
			// The connections first: a client's shared connection
			// (ControlPersist) would keep its sshd child, and with it
			// the log pipe, open after the listener is gone.
			exec.Command("pkill", "-TERM", "-P", fmt.Sprint(cmd.Process.Pid)).Run()
			cancel()
			<-exited
			if t.Failed() {
				t.Logf("sshd log:\n%s", w.sshdLog.String())
			}
		})
		deadline := time.Now().Add(10 * time.Second)
		for time.Now().Before(deadline) {
			select {
			case <-exited:
				deadline = time.Time{}
				continue
			default:
			}
			c, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), time.Second)
			if err == nil {
				banner, _ := bufio.NewReader(c).ReadString('\n')
				c.Close()
				if strings.HasPrefix(banner, "SSH-") {
					return port
				}
			}
			time.Sleep(50 * time.Millisecond)
		}
		cancel()
		<-exited
		t.Logf("sshd attempt %d did not come up:\n%s", attempt+1, w.sshdLog.String())
	}
	t.Skip("sshd does not start in this environment")
	return 0
}

func (w *world) transport(tune func(*Config)) *Transport {
	w.t.Helper()
	u, _ := user.Current()
	cfg := Config{
		ShareID:  w.sh.ID,
		Host:     "127.0.0.1",
		Port:     w.port,
		User:     u.Username,
		KeyFile:  w.keyFile,
		StateDir: w.stateDir,
	}
	if tune != nil {
		tune(&cfg)
	}
	tr, err := New(cfg)
	if err != nil {
		w.t.Fatal(err)
	}
	w.t.Cleanup(func() { tr.Close() })
	return tr
}

// accepted counts the connections sshd authenticated.
func (w *world) accepted() int {
	return strings.Count(w.sshdLog.String(), "Accepted publickey for ")
}

// waitGone waits until the control socket at p is removed.
func waitGone(t *testing.T, p string) {
	t.Helper()
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
		if _, err := os.Lstat(p); errors.Is(err, os.ErrNotExist) {
			return
		}
	}
	t.Errorf("control socket %s is still there", p)
}

func (w *world) announce(p string, file string) *server.AnnounceResult {
	w.t.Helper()
	h, err := hashx.File(file)
	if err != nil {
		w.t.Fatal(err)
	}
	fi, _ := os.Stat(file)
	ar, err := w.sh.Announce(w.ana, server.AnnounceRequest{Path: p, NewHash: h, Size: fi.Size()})
	if err != nil || ar.Status != server.StatusUpload {
		w.t.Fatalf("announce %s: %+v %v", p, ar, err)
	}
	return ar
}

func upload(ar *server.AnnounceResult, share string) transport.Upload {
	return transport.Upload{ShareID: share, UploadID: ar.UploadID, Dest: ar.Dest}
}

func (w *world) logLines(sub string) []string {
	b, _ := os.ReadFile(filepath.Join(w.sh.Dir(), "service.log"))
	var out []string
	for _, l := range strings.Split(string(b), "\n") {
		if strings.Contains(l, " transfer ") && strings.Contains(l, sub) {
			out = append(out, l)
		}
	}
	return out
}

func writeFile(t *testing.T, dir, name string, b []byte) string {
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, b, 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestIntegrationUploadDownload(t *testing.T) {
	w := newWorld(t)
	tr := w.transport(nil)
	ctx := context.Background()
	local := t.TempDir()

	// Upload, then commit through the server's rules.
	content := []byte("izveštaj za tim\n")
	src := writeFile(t, local, "src.txt", content)
	ar := w.announce("dokumenti/izveštaj Q4 [final] $x.txt", src)
	if err := tr.Send(ctx, upload(ar, w.sh.ID), src); err != nil {
		t.Fatal(err)
	}
	res, err := w.sh.Commit(w.ana, ar.UploadID)
	if err != nil || res.Status != server.StatusApplied {
		t.Fatalf("commit: %+v %v", res, err)
	}
	if _, err := os.Stat(tr.KnownHosts()); err != nil {
		t.Errorf("host key not remembered in the state directory: %v", err)
	}

	// Download it back.
	h, _ := hashx.File(src)
	dst := filepath.Join(local, "back.txt")
	var stats []Stats
	tr.cfg.OnTransfer = func(s Stats) { stats = append(stats, s) }
	if err := tr.Fetch(ctx, w.sh.ID, ar.Path, h, dst); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(dst); !bytes.Equal(b, content) {
		t.Errorf("downloaded %q", b)
	}
	if len(stats) != 1 || stats[0].Direction != "download" || stats[0].Size != int64(len(content)) {
		t.Errorf("stats %+v", stats)
	}

	// A stale hash: retryable ErrChanged, nothing left behind, dst untouched.
	if err := tr.Fetch(ctx, w.sh.ID, ar.Path, strings.Repeat("0", 64), dst); !errors.Is(err, transport.ErrChanged) {
		t.Errorf("stale hash: %v", err)
	}
	// A file that is gone: also ErrChanged.
	if err := tr.Fetch(ctx, w.sh.ID, "nema.txt", h, filepath.Join(local, "nema.txt")); !errors.Is(err, transport.ErrChanged) {
		t.Errorf("missing file: %v", err)
	}
	entries, _ := os.ReadDir(local)
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".shf-tmp-") {
			t.Errorf("temporary file left: %s", e.Name())
		}
	}
	if b, _ := os.ReadFile(dst); !bytes.Equal(b, content) {
		t.Errorf("dst changed by a failed fetch: %q", b)
	}

	// The gate refuses what the client package would never send: a shell,
	// a committed upload id, a path outside storage/.
	sshCmd := func(remote string) (string, error) {
		args := append(tr.sshArgs(true)[1:], "127.0.0.1", remote)
		out, err := exec.Command(tr.ssh, args...).CombinedOutput()
		return string(out), err
	}
	for _, c := range []struct{ cmd, want string }{
		{"id", "denied (not_rsync)"},
		{"rsync --server -e.LsfxCIvu --partial . tmp/" + ar.UploadID, "denied (upload)"},
		{"rsync --server --sender -e.LsfxCIvu . storage/../service.db", "denied (path)"},
		{"rsync --server --sender -e.LsfxCIvu --remove-source-files . storage/x", "denied (option)"},
	} {
		out, err := sshCmd(c.cmd)
		if err == nil || !strings.Contains(out, c.want) {
			t.Errorf("ssh %q: %v %q", c.cmd, err, out)
		}
	}
	if err := tr.Send(ctx, upload(ar, w.sh.ID), src); !errors.Is(err, ErrDenied) {
		t.Errorf("send to a committed upload: %v", err)
	}

	// service.log has a line per call.
	if l := w.logLines(`dir=upload path="tmp/` + ar.UploadID + `" outcome=ok`); len(l) != 1 {
		t.Errorf("upload log lines: %q", l)
	}
	if l := w.logLines(`dir=download path="storage/` + ar.Path + `" outcome=ok`); len(l) != 2 {
		t.Errorf("download log lines: %q", l)
	}
	if l := w.logLines(`outcome=denied detail="not_rsync`); len(l) != 1 {
		t.Errorf("denied log lines: %q", l)
	}
}

func TestIntegrationResumeUpload(t *testing.T) {
	w := newWorld(t)
	ctx := context.Background()
	const size = 48 << 20
	data := make([]byte, size)
	rng := rand.NewChaCha8([32]byte{1, 2, 3})
	rng.Read(data)
	src := writeFile(t, t.TempDir(), "big.bin", data)
	ar := w.announce("veliki.bin", src)
	tmpDir := filepath.Join(w.sh.Dir(), "tmp")

	// Slow first attempt, cut off once a few MiB have arrived.
	slow := w.transport(func(c *Config) { c.BwLimit = 8 << 10 }) // 8 MiB/s
	cctx, cancel := context.WithCancel(ctx)
	errc := make(chan error, 1)
	go func() { errc <- slow.Send(cctx, upload(ar, w.sh.ID), src) }()
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		if received(tmpDir, ar.UploadID) >= 6<<20 {
			break
		}
		select {
		case err := <-errc:
			t.Fatalf("upload finished before it could be interrupted: %v", err)
		case <-time.After(20 * time.Millisecond):
		}
	}
	cancel()
	if err := <-errc; err == nil {
		t.Fatal("interrupted Send returned nil")
	}
	// The server's rsync keeps what it got as tmp/<upload_id> (--partial).
	var partial int64
	for deadline := time.Now().Add(15 * time.Second); time.Now().Before(deadline); time.Sleep(50 * time.Millisecond) {
		if fi, err := os.Stat(filepath.Join(tmpDir, ar.UploadID)); err == nil && fi.Size() > 0 {
			partial = fi.Size()
			break
		}
	}
	if partial == 0 || partial >= size {
		t.Fatalf("partial upload is %d bytes (of %d)", partial, size)
	}
	t.Logf("interrupted after %d of %d bytes", partial, size)

	// Second attempt at full speed continues from the partial file.
	var st Stats
	fast := w.transport(func(c *Config) { c.OnTransfer = func(s Stats) { st = s } })
	if err := fast.Send(ctx, upload(ar, w.sh.ID), src); err != nil {
		t.Fatal(err)
	}
	t.Logf("resumed: matched %d, literal %d of %d", st.Matched, st.Literal, st.Size)
	if st.Size != size || st.Matched < partial/2 || st.Literal > size-partial/2 {
		t.Errorf("resume started from zero: %+v (partial was %d)", st, partial)
	}
	res, err := w.sh.Commit(w.ana, ar.UploadID)
	if err != nil || res.Status != server.StatusApplied {
		t.Fatalf("commit: %+v %v", res, err)
	}
	got, _ := os.ReadFile(filepath.Join(w.sh.Dir(), "storage", "veliki.bin"))
	if !bytes.Equal(got, data) {
		t.Error("stored content differs from the source")
	}
	if l := w.logLines(`dir=upload path="tmp/` + ar.UploadID + `" outcome=failed`); len(l) != 1 {
		t.Errorf("interrupted upload log lines: %q", l)
	}
}

// An interrupted download keeps its partial content next to the
// destination (named after the hash); the next Fetch of the same content
// resumes it instead of starting from zero.
func TestIntegrationResumeDownload(t *testing.T) {
	w := newWorld(t)
	ctx := context.Background()
	const size = 48 << 20
	data := make([]byte, size)
	rng := rand.NewChaCha8([32]byte{4, 5, 6})
	rng.Read(data)
	src := writeFile(t, t.TempDir(), "big.bin", data)
	ar := w.announce("veliki.bin", src)
	if err := w.transport(nil).Send(ctx, upload(ar, w.sh.ID), src); err != nil {
		t.Fatal(err)
	}
	if res, err := w.sh.Commit(w.ana, ar.UploadID); err != nil || res.Status != server.StatusApplied {
		t.Fatalf("commit: %+v %v", res, err)
	}
	hash, _ := hashx.File(src)
	local := t.TempDir()
	dst := filepath.Join(local, "veliki.bin")
	part := filepath.Join(local, PartialPrefix+hash)

	slow := w.transport(func(c *Config) { c.BwLimit = 8 << 10 }) // 8 MiB/s
	cctx, cancel := context.WithCancel(ctx)
	errc := make(chan error, 1)
	go func() { errc <- slow.Fetch(cctx, w.sh.ID, "veliki.bin", hash, dst) }()
	for deadline := time.Now().Add(30 * time.Second); time.Now().Before(deadline); {
		if fi, err := os.Stat(part); err == nil && fi.Size() >= 6<<20 {
			break
		}
		select {
		case err := <-errc:
			t.Fatalf("download finished before it could be interrupted: %v", err)
		case <-time.After(20 * time.Millisecond):
		}
	}
	cancel()
	if err := <-errc; err == nil {
		t.Fatal("interrupted Fetch returned nil")
	}
	fi, err := os.Stat(part)
	if err != nil || fi.Size() == 0 || fi.Size() >= size {
		t.Fatalf("partial download: %v %v", fi, err)
	}
	partial := fi.Size()
	if _, err := os.Stat(dst); err == nil {
		t.Fatal("the destination appeared before the download was complete")
	}

	var st Stats
	fast := w.transport(func(c *Config) { c.OnTransfer = func(s Stats) { st = s } })
	if err := fast.Fetch(ctx, w.sh.ID, "veliki.bin", hash, dst); err != nil {
		t.Fatal(err)
	}
	t.Logf("interrupted after %d bytes; resumed: matched %d, literal %d of %d", partial, st.Matched, st.Literal, st.Size)
	if st.Matched < partial/2 || st.Literal > size-partial/2 {
		t.Errorf("resume started from zero: %+v (partial was %d)", st, partial)
	}
	if got, _ := os.ReadFile(dst); !bytes.Equal(got, data) {
		t.Error("downloaded content differs")
	}
	if _, err := os.Stat(part); err == nil {
		t.Error("the partial file is still there after the rename")
	}
}

func TestIntegrationHostKeyAndRevoke(t *testing.T) {
	w := newWorld(t)
	ctx := context.Background()
	tr := w.transport(nil)
	local := t.TempDir()
	src := writeFile(t, local, "a.txt", []byte("a"))
	ar := w.announce("a.txt", src)
	if err := tr.Send(ctx, upload(ar, w.sh.ID), src); err != nil {
		t.Fatal(err)
	}
	if _, err := w.sh.Commit(w.ana, ar.UploadID); err != nil {
		t.Fatal(err)
	}
	h, _ := hashx.File(src)

	// Another host key under the same name: refused, not overwritten.
	// (Checked when a connection is made, so the shared one goes first.)
	tr.Close()
	waitGone(t, tr.ctl)
	kh, _ := os.ReadFile(tr.KnownHosts())
	other := filepath.Join(t.TempDir(), "other")
	exec.Command("ssh-keygen", "-q", "-t", "ed25519", "-N", "", "-f", other).Run()
	otherPub, _ := os.ReadFile(other + ".pub")
	f := strings.Fields(string(otherPub))
	forged := fmt.Sprintf("[127.0.0.1]:%d %s %s\n", w.port, f[0], f[1])
	os.WriteFile(tr.KnownHosts(), []byte(forged), 0o600)
	if err := tr.Fetch(ctx, w.sh.ID, "a.txt", h, filepath.Join(local, "b.txt")); !errors.Is(err, ErrHostKey) {
		t.Errorf("changed host key: %v", err)
	}
	if b, _ := os.ReadFile(tr.KnownHosts()); string(b) != forged {
		t.Error("known_hosts was rewritten")
	}
	os.WriteFile(tr.KnownHosts(), kh, 0o600)

	// Revoking ana removes her line: the key is refused by sshd.
	if err := w.sh.RevokeMember("ana"); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(w.keys.Path); !strings.HasPrefix(string(b), "# not shfd's\n") || strings.Contains(string(b), "shfd:ana@") {
		t.Errorf("authorized_keys after revoke:\n%s", b)
	}
	if err := tr.Fetch(ctx, w.sh.ID, "a.txt", h, filepath.Join(local, "b.txt")); !errors.Is(err, ErrAuth) {
		t.Errorf("revoked member, line removed: %v", err)
	}
	// Even if the line comes back (a stale copy of the file), the gate
	// refuses a revoked member.
	if err := w.keys.Set(authkeys.Entry{ShareID: w.sh.ID, MemberID: w.ana.ID, Username: "ana", Key: w.pubKey}); err != nil {
		t.Fatal(err)
	}
	err := tr.Fetch(ctx, w.sh.ID, "a.txt", h, filepath.Join(local, "b.txt"))
	if !errors.Is(err, ErrAuth) || !strings.Contains(err.Error(), "revoked") {
		t.Errorf("revoked member, line present: %v", err)
	}
	if l := w.logLines(`member=ana dir=- path="" outcome=denied detail="member: member ana is revoked"`); len(l) != 1 {
		t.Errorf("log lines %q", l)
	}
}

// received is how much of an upload has arrived: rsync's temporary file
// (tmp/.<id>.XXXXXX) or the partial file itself.
func received(dir, id string) int64 {
	var n int64
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), "."+id+".") || e.Name() == id {
			if fi, err := e.Info(); err == nil && fi.Size() > n {
				n = fi.Size()
			}
		}
	}
	return n
}

type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

// Transfers share one SSH connection; revoking the member cuts it off at
// once, on that open connection too; Close ends it.
func TestIntegrationSharedConnection(t *testing.T) {
	w := newWorld(t)
	ctx := context.Background()
	tr := w.transport(nil)
	if tr.ctl == "" {
		t.Fatal("no control socket")
	}
	local := t.TempDir()
	before := w.accepted()
	var hashes []string
	for i := 0; i < 5; i++ {
		name := fmt.Sprintf("f%d.txt", i)
		src := writeFile(t, local, name, []byte(name))
		ar := w.announce(name, src)
		if err := tr.Send(ctx, upload(ar, w.sh.ID), src); err != nil {
			t.Fatal(err)
		}
		if _, err := w.sh.Commit(w.ana, ar.UploadID); err != nil {
			t.Fatal(err)
		}
		h, _ := hashx.File(src)
		hashes = append(hashes, h)
	}
	for i, h := range hashes {
		if err := tr.Fetch(ctx, w.sh.ID, fmt.Sprintf("f%d.txt", i), h, filepath.Join(local, fmt.Sprintf("g%d.txt", i))); err != nil {
			t.Fatal(err)
		}
	}
	if n := w.accepted() - before; n != 1 {
		t.Errorf("10 transfers made %d SSH connections, want 1", n)
	}
	if fi, err := os.Lstat(tr.ctl); err != nil || fi.Mode()&os.ModeSocket == 0 {
		t.Fatalf("control socket %s: %v %v", tr.ctl, fi, err)
	}
	if len(w.logLines("outcome=ok")) != 10 {
		t.Errorf("service.log: %q", w.logLines("outcome=ok"))
	}

	// A second Transport of the same folder (shf status next to the
	// daemon) uses the same connection.
	other := w.transport(nil)
	if other.ctl != tr.ctl {
		t.Errorf("control paths differ: %s, %s", other.ctl, tr.ctl)
	}
	if err := other.Fetch(ctx, w.sh.ID, "f0.txt", hashes[0], filepath.Join(local, "h0.txt")); err != nil {
		t.Fatal(err)
	}
	if n := w.accepted() - before; n != 1 {
		t.Errorf("%d SSH connections after the second transport, want 1", n)
	}

	// Revoked while the connection is open: the next transfer over it is
	// refused (rsync-gate checks the member on every session), and the
	// client stops the connection.
	if err := w.sh.RevokeMember("ana"); err != nil {
		t.Fatal(err)
	}
	err := tr.Fetch(ctx, w.sh.ID, "f1.txt", hashes[1], filepath.Join(local, "r1.txt"))
	if !errors.Is(err, ErrAuth) || !strings.Contains(err.Error(), "revoked") {
		t.Errorf("revoked member over the open connection: %v", err)
	}
	src := writeFile(t, local, "late.txt", []byte("late"))
	if err := tr.Send(ctx, transport.Upload{ShareID: w.sh.ID, UploadID: strings.Repeat("ab", 16), Dest: "tmp/" + strings.Repeat("ab", 16)}, src); !errors.Is(err, ErrAuth) {
		t.Errorf("revoked member, upload: %v", err)
	}
	if n := w.accepted() - before; n != 1 {
		t.Errorf("sshd accepted a new connection of a revoked member (%d connections)", n)
	}
	if fileExists(filepath.Join(local, "r1.txt")) {
		t.Error("a revoked member downloaded a file")
	}
	if l := w.logLines(`member=ana dir=- path="" outcome=denied detail="member: member ana is revoked"`); len(l) < 1 {
		t.Errorf("no refusal in service.log")
	}
	waitGone(t, tr.ctl)
}

func TestIntegrationCloseEndsConnection(t *testing.T) {
	w := newWorld(t)
	ctx := context.Background()
	tr := w.transport(func(c *Config) { c.ControlPersist = time.Hour })
	local := t.TempDir()
	src := writeFile(t, local, "a.txt", []byte("a"))
	ar := w.announce("a.txt", src)
	if err := tr.Send(ctx, upload(ar, w.sh.ID), src); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(tr.ctl); err != nil {
		t.Fatalf("no master after a transfer: %v", err)
	}
	if err := tr.Close(); err != nil {
		t.Fatal(err)
	}
	waitGone(t, tr.ctl)
	if err := tr.Close(); err != nil { // nothing to stop
		t.Fatal(err)
	}
	// still usable: the next transfer opens a new connection
	before := w.accepted()
	if err := tr.Send(ctx, upload(ar, w.sh.ID), src); err != nil {
		t.Fatal(err)
	}
	if n := w.accepted() - before; n != 1 {
		t.Errorf("%d new connections after Close, want 1", n)
	}

	// NoMultiplex: a connection per transfer, no socket.
	plain := w.transport(func(c *Config) { c.NoMultiplex = true })
	if plain.ctl != "" {
		t.Errorf("NoMultiplex has a control path %s", plain.ctl)
	}
}

func fileExists(p string) bool {
	_, err := os.Lstat(p)
	return err == nil
}
