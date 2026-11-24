//go:build e2e

package e2e

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	_ "modernc.org/sqlite"

	"github.com/digital-cube/shared-folder/internal/sshdtest"
)

// The binaries under test, built once by TestMain.
var shfdBin, shfBin string

func TestMain(m *testing.M) {
	os.Exit(func() int {
		dir, err := os.MkdirTemp("", "shf-e2e-bin-")
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		defer os.RemoveAll(dir)
		cmd := exec.Command(goTool(), "build", "-trimpath", "-o", dir+string(os.PathSeparator), "./cmd/shfd", "./cmd/shf")
		cmd.Dir = ".."
		if out, err := cmd.CombinedOutput(); err != nil {
			fmt.Fprintf(os.Stderr, "e2e: building shfd and shf: %v\n%s", err, out)
			return 1
		}
		shfdBin, shfBin = filepath.Join(dir, "shfd"), filepath.Join(dir, "shf")
		return m.Run()
	}())
}

// goTool is the go command that runs these tests.
func goTool() string {
	if g := os.Getenv("GO"); g != "" {
		return g
	}
	if p := filepath.Join(runtime.GOROOT(), "bin", "go"); fileExists(p) {
		return p
	}
	return "go"
}

func fileExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

var rsyncVersionRE = regexp.MustCompile(`rsync\s+version\s+v?(\d+)\.(\d+)\.(\d+)`)

// requireTools skips the test when the machine lacks what the tests need.
func requireTools(t *testing.T) {
	t.Helper()
	if runtime.GOOS != "linux" {
		t.Skip("e2e: the end-to-end tests run on Linux")
	}
	for _, bin := range []string{"rsync", "ssh", "ssh-keygen"} {
		if _, err := exec.LookPath(bin); err != nil {
			t.Skipf("e2e: %s is not installed; the end-to-end tests need rsync 3.2.4+, the OpenSSH client and sshd (apt install rsync openssh-client openssh-server)", bin)
		}
	}
	if sshdtest.Find() == "" {
		t.Skip("e2e: sshd is not installed; the end-to-end tests need it (apt install openssh-server); it runs as you on a free port, no root needed")
	}
	out, _ := exec.Command("rsync", "--version").Output()
	m := rsyncVersionRE.FindSubmatch(out)
	if m == nil {
		t.Skip("e2e: rsync --version is not GNU rsync; the end-to-end tests need rsync 3.2.4+")
	}
	var v [3]int
	for i := range v {
		v[i], _ = strconv.Atoi(string(m[i+1]))
	}
	if v[0]*1_000_000+v[1]*1000+v[2] < 3_002_004 {
		t.Skipf("e2e: rsync %d.%d.%d is too old; the end-to-end tests need 3.2.4+", v[0], v[1], v[2])
	}
}

// ---- waiting ---------------------------------------------------------------

// eventually polls cond until it returns nil, and fails the test with its
// last error when that does not happen within timeout. Tests never sleep
// a fixed time: they wait for the state they expect.
func eventually(t *testing.T, timeout time.Duration, what string, cond func() error) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		err := cond()
		if err == nil {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("%s: not within %s: %v", what, timeout, err)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// Timeouts: how long a change may take to reach another client, and the
// long ones (many files, big files, restarts).
const (
	arrive = 20 * time.Second
	long   = 3 * time.Minute
)

// ---- processes ---------------------------------------------------------------

// proc is a background process whose output goes to a log file.
type proc struct {
	name string
	cmd  *exec.Cmd
	done chan struct{}
}

func startProc(t *testing.T, name, logPath string, env []string, bin string, args ...string) *proc {
	t.Helper()
	logf, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	fmt.Fprintf(logf, "==== %s: %s %s\n", time.Now().Format(time.RFC3339Nano), filepath.Base(bin), strings.Join(args, " "))
	cmd := exec.Command(bin, args...)
	cmd.Env = append(os.Environ(), env...)
	cmd.Stdout, cmd.Stderr = logf, logf
	// own process group (a signal to the test does not reach it); killed
	// with the test binary should that die without its cleanups
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true, Pdeathsig: syscall.SIGKILL}
	if err := cmd.Start(); err != nil {
		logf.Close()
		t.Fatalf("starting %s: %v", name, err)
	}
	p := &proc{name: name, cmd: cmd, done: make(chan struct{})}
	go func() {
		err := cmd.Wait()
		fmt.Fprintf(logf, "==== %s: exited: %v\n", time.Now().Format(time.RFC3339Nano), err)
		logf.Close()
		close(p.done)
	}()
	return p
}

func (p *proc) running() bool {
	select {
	case <-p.done:
		return false
	default:
		return true
	}
}

// stop sends SIGTERM and waits for the process (the daemon finishes the
// step in progress, up to 60 s).
func (p *proc) stop(t *testing.T) {
	t.Helper()
	if !p.running() {
		return
	}
	p.cmd.Process.Signal(syscall.SIGTERM)
	select {
	case <-p.done:
	case <-time.After(75 * time.Second):
		t.Errorf("%s did not stop within 75 s of SIGTERM; killing it", p.name)
		p.kill()
	}
}

// kill ends the process and everything it started (rsync and ssh run in
// process groups of their own) at once, like a power cut.
func (p *proc) kill() {
	if !p.running() {
		return
	}
	pids := append(descendants(p.cmd.Process.Pid), p.cmd.Process.Pid)
	for _, pid := range pids {
		syscall.Kill(pid, syscall.SIGSTOP) // nobody starts anything new meanwhile
	}
	for _, pid := range pids {
		syscall.Kill(pid, syscall.SIGKILL)
	}
	<-p.done
}

// descendants lists the processes below pid, from /proc.
func descendants(pid int) []int {
	children := map[int][]int{}
	des, _ := os.ReadDir("/proc")
	for _, de := range des {
		n, err := strconv.Atoi(de.Name())
		if err != nil {
			continue
		}
		b, err := os.ReadFile("/proc/" + de.Name() + "/stat")
		if err != nil {
			continue
		}
		// pid (comm) state ppid ...; comm may contain spaces and parens
		s := string(b)
		i := strings.LastIndexByte(s, ')')
		if i < 0 {
			continue
		}
		f := strings.Fields(s[i+1:])
		if len(f) < 2 {
			continue
		}
		pp, _ := strconv.Atoi(f[1])
		children[pp] = append(children[pp], n)
	}
	var out []int
	var walk func(int)
	walk = func(p int) {
		for _, c := range children[p] {
			out = append(out, c)
			walk(c)
		}
	}
	walk(pid)
	return out
}

func freePort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	return ln.Addr().(*net.TCPAddr).Port
}

// ---- the server --------------------------------------------------------------

// cluster is one test's world: a data-root, sshd, shfd serve, one folder
// and its clients.
type cluster struct {
	t        *testing.T
	base     string
	root     string // shfd data-root
	ak       string // authorized_keys of "the shfd user" (us)
	sshd     *sshdtest.SSHD
	port     int
	url      string
	share    string
	serveLog string
	serve    *proc
	clients  []*client
	dbh      *sql.DB
}

// newCluster starts sshd and shfd serve with one folder, "tim".
func newCluster(t *testing.T) *cluster {
	t.Helper()
	requireTools(t)
	c := &cluster{t: t, base: t.TempDir()}
	c.root = filepath.Join(c.base, "data")
	c.ak = filepath.Join(c.base, "ssh", "authorized_keys")
	c.serveLog = filepath.Join(c.base, "shfd-serve.log")
	if err := os.MkdirAll(filepath.Dir(c.ak), 0o700); err != nil {
		t.Fatal(err)
	}
	c.sshd = sshdtest.Start(t, c.ak)
	c.port = freePort(t)
	c.url = fmt.Sprintf("http://127.0.0.1:%d", c.port)
	t.Cleanup(c.dumpLogsOnFailure)
	t.Cleanup(c.stopAll)

	out := c.mustShfd("share", "create", "tim", "--admin", "admin")
	m := regexp.MustCompile(`share-id:\s+(\S+)`).FindStringSubmatch(out)
	if m == nil {
		t.Fatalf("share create printed no share-id:\n%s", out)
	}
	c.share = m[1]
	c.startServer()
	return c
}

func (c *cluster) shareDir() string   { return filepath.Join(c.root, c.share) }
func (c *cluster) storageDir() string { return filepath.Join(c.shareDir(), "storage") }
func (c *cluster) serviceLog() string { return filepath.Join(c.shareDir(), "service.log") }
func (c *cluster) serviceDB() string  { return filepath.Join(c.shareDir(), "service.db") }
func (c *cluster) storage(p string) string {
	return filepath.Join(c.storageDir(), filepath.FromSlash(p))
}

// shfd runs an admin command of shfd and returns its output and exit code.
func (c *cluster) shfd(args ...string) (string, int) {
	c.t.Helper()
	cmd := exec.Command(shfdBin, args...)
	cmd.Env = append(os.Environ(),
		"SHFD_DATA_ROOT="+c.root,
		"SHFD_AUTHORIZED_KEYS="+c.ak,
		"SHFD_BINARY="+shfdBin)
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	err := cmd.Run()
	code := 0
	var ee *exec.ExitError
	switch {
	case errors.As(err, &ee):
		code = ee.ExitCode()
	case err != nil:
		c.t.Fatalf("shfd %s: %v", strings.Join(args, " "), err)
	}
	return out.String(), code
}

func (c *cluster) mustShfd(args ...string) string {
	c.t.Helper()
	out, code := c.shfd(args...)
	if code != 0 {
		c.t.Fatalf("shfd %s: exit %d\n%s", strings.Join(args, " "), code, out)
	}
	return out
}

// startServer runs shfd serve and waits until it answers.
func (c *cluster) startServer() {
	c.t.Helper()
	c.serve = startProc(c.t, "shfd serve", c.serveLog,
		[]string{"SHFD_DATA_ROOT=" + c.root, "SHFD_AUTHORIZED_KEYS=" + c.ak, "SHFD_BINARY=" + shfdBin},
		shfdBin, "serve", "--listen", fmt.Sprintf("127.0.0.1:%d", c.port),
		"--ssh-endpoint", fmt.Sprintf("%s@127.0.0.1:%d", c.sshd.User, c.sshd.Port))
	hc := &http.Client{Timeout: time.Second}
	eventually(c.t, 20*time.Second, "shfd serve answers", func() error {
		if !c.serve.running() {
			c.t.Fatalf("shfd serve exited:\n%s", readFile(c.serveLog))
		}
		resp, err := hc.Get(c.url + "/v1/shares/" + c.share + "/changes?since=0")
		if err != nil {
			return err
		}
		resp.Body.Close()
		return nil
	})
}

// stopServer stops shfd serve: gracefully, or with SIGKILL (a crash).
func (c *cluster) stopServer(crash bool) {
	c.t.Helper()
	if crash {
		c.serve.kill()
	} else {
		c.serve.stop(c.t)
	}
}

func (c *cluster) stopAll() {
	for _, cl := range c.clients {
		if cl.daemon != nil {
			cl.daemon.kill()
		}
	}
	if c.serve != nil {
		c.serve.kill()
	}
}

func readFile(p string) string {
	b, err := os.ReadFile(p)
	if err != nil {
		return fmt.Sprintf("(%v)", err)
	}
	return string(b)
}

// dumpLogsOnFailure prints service.log, shfd serve's output and every
// client's log when the test failed, or always with E2E_LOGS=1.
func (c *cluster) dumpLogsOnFailure() {
	if !c.t.Failed() && os.Getenv("E2E_LOGS") == "" {
		return
	}
	var b strings.Builder
	fmt.Fprintf(&b, "\n======== service.log (%s)\n%s", c.serviceLog(), readFile(c.serviceLog()))
	fmt.Fprintf(&b, "\n======== shfd serve\n%s", readFile(c.serveLog))
	for _, cl := range c.clients {
		fmt.Fprintf(&b, "\n======== client %s (shf daemon)\n%s", cl.name, readFile(cl.log))
	}
	if out, err := exec.Command(shfdBin, "verify", c.share, "--data-root", c.root).CombinedOutput(); err != nil || len(out) > 0 {
		fmt.Fprintf(&b, "\n======== shfd verify\n%s", out)
	}
	c.t.Log(b.String())
}

// db opens the folder's service.db for reading.
func (c *cluster) db() *sql.DB {
	c.t.Helper()
	if c.dbh != nil {
		return c.dbh
	}
	db, err := sql.Open("sqlite", "file:"+c.serviceDB()+"?mode=ro&_pragma=busy_timeout(10000)")
	if err != nil {
		c.t.Fatal(err)
	}
	c.t.Cleanup(func() { db.Close() })
	c.dbh = db
	return db
}

type event struct {
	Seq                   int64
	Member, Op, Path, Old string
	Hash                  string
}

func (c *cluster) events() []event {
	c.t.Helper()
	rows, err := c.db().Query(`SELECT seq, member, op, path, old_path, hash FROM events ORDER BY seq`)
	if err != nil {
		c.t.Fatal(err)
	}
	defer rows.Close()
	var out []event
	for rows.Next() {
		var e event
		if err := rows.Scan(&e.Seq, &e.Member, &e.Op, &e.Path, &e.Old, &e.Hash); err != nil {
			c.t.Fatal(err)
		}
		out = append(out, e)
	}
	if err := rows.Err(); err != nil {
		c.t.Fatal(err)
	}
	return out
}

func (c *cluster) eventsOn(p string) []event {
	var out []event
	for _, e := range c.events() {
		if e.Path == p || e.Old == p {
			out = append(out, e)
		}
	}
	return out
}

type trashEntry struct {
	ID                               int64
	OrigPath, Name, Hash, Reason, By string
}

func (c *cluster) trash() []trashEntry {
	c.t.Helper()
	rows, err := c.db().Query(`SELECT id, orig_path, trash_name, hash, reason, by_member FROM trash ORDER BY id`)
	if err != nil {
		c.t.Fatal(err)
	}
	defer rows.Close()
	var out []trashEntry
	for rows.Next() {
		var e trashEntry
		if err := rows.Scan(&e.ID, &e.OrigPath, &e.Name, &e.Hash, &e.Reason, &e.By); err != nil {
			c.t.Fatal(err)
		}
		out = append(out, e)
	}
	return out
}

// trashFor is the newest trash entry of p with the reason; it checks the
// name and that trash/ holds the old content.
func (c *cluster) trashFor(p, reason, by, content string) trashEntry {
	c.t.Helper()
	var found *trashEntry
	for _, e := range c.trash() {
		if e.OrigPath == p && e.Reason == reason {
			found = &e
		}
	}
	if found == nil {
		c.t.Fatalf("no %s trash entry for %s; trash: %+v", reason, p, c.trash())
	}
	re := regexp.MustCompile(`^` + regexp.QuoteMeta(filepath.Base(p)) + `\.` + reason + `-by-` + by + `-\d{8}T\d{6}Z(-\d+)?$`)
	if !re.MatchString(found.Name) {
		c.t.Errorf("trash name %q of %s does not match %s", found.Name, p, re)
	}
	got := readFile(filepath.Join(c.shareDir(), "trash", found.Name))
	if got != content {
		c.t.Errorf("trash/%s = %q, want %q", found.Name, got, content)
	}
	return *found
}

// ---- clients -----------------------------------------------------------------

// client is one member with its own state home, folder and shf daemon.
type client struct {
	c        *cluster
	name     string
	home     string // SHF_STATE_HOME
	dir      string // the shared folder on disk
	token    string
	log      string
	daemon   *proc
	settings map[string]any
	// skip tells which paths this client does not compare with storage/
	// (what it ignores).
	skip func(p string) bool
	// revoked: the member was revoked; its folder is not compared.
	revoked bool
}

// testSettings shorten the client's intervals. The mass-delete guard is
// off unless a test turns it on: with the specification's 10 % a folder
// of fewer than ten files could not lose one file without shf confirm.
func testSettings() map[string]any {
	return map[string]any{
		"debounce":      "200ms",
		"settle":        "300ms",
		"poll":          "1s",
		"reconnect_max": "1s",
		"ping":          "5s",
		"retry_max":     "1s",
		"mass_delete":   map[string]any{"files": -1, "percent": -1},
	}
}

// newClient adds a member and prepares its directories; the folder may be
// filled before join.
func (c *cluster) newClient(name string) *client {
	c.t.Helper()
	out := c.mustShfd("member", "add", c.share, name)
	m := regexp.MustCompile(`token:\s+(\S+)`).FindStringSubmatch(out)
	if m == nil {
		c.t.Fatalf("member add printed no token:\n%s", out)
	}
	return c.clientWithToken(name, name, m[1])
}

// clientWithToken prepares the directories of a client of member name
// under clients/<machine>, with a token the test already has.
func (c *cluster) clientWithToken(name, machine, token string) *client {
	c.t.Helper()
	cl := &client{c: c, name: name, token: token,
		home: filepath.Join(c.base, "clients", machine, "state"),
		dir:  filepath.Join(c.base, "clients", machine, "folder"),
		log:  filepath.Join(c.base, "clients", machine, "shf.log"),
		skip: func(string) bool { return false }, settings: testSettings()}
	if err := os.MkdirAll(cl.dir, 0o755); err != nil {
		c.t.Fatal(err)
	}
	cl.writeSettings()
	c.clients = append(c.clients, cl)
	return cl
}

// joined is a member that has joined and runs shf daemon.
func (c *cluster) joined(name string) *client {
	c.t.Helper()
	cl := c.newClient(name)
	cl.join()
	cl.start()
	return cl
}

func (cl *client) writeSettings() {
	cl.c.t.Helper()
	b, _ := json.MarshalIndent(cl.settings, "", "  ")
	if err := os.MkdirAll(cl.home, 0o700); err != nil {
		cl.c.t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cl.home, "settings.json"), b, 0o600); err != nil {
		cl.c.t.Fatal(err)
	}
}

// set changes one setting; it takes effect when the daemon starts.
func (cl *client) set(key string, v any) {
	cl.settings[key] = v
	cl.writeSettings()
}

func (cl *client) env() []string { return []string{"SHF_STATE_HOME=" + cl.home} }

// shf runs a command of the client and returns its output and exit code.
func (cl *client) shf(args ...string) (string, int) {
	cl.c.t.Helper()
	cmd := exec.Command(shfBin, args...)
	cmd.Env = append(os.Environ(), cl.env()...)
	cmd.Dir = cl.dir
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	err := cmd.Run()
	var ee *exec.ExitError
	switch {
	case errors.As(err, &ee):
		return out.String(), ee.ExitCode()
	case err != nil:
		cl.c.t.Fatalf("%s: shf %s: %v", cl.name, strings.Join(args, " "), err)
	}
	return out.String(), 0
}

func (cl *client) mustShf(args ...string) string {
	cl.c.t.Helper()
	out, code := cl.shf(args...)
	if code != 0 {
		cl.c.t.Fatalf("%s: shf %s: exit %d\n%s", cl.name, strings.Join(args, " "), code, out)
	}
	return out
}

// join runs shf join with real rsync over SSH (key, endpoint and host key
// set up by join itself).
func (cl *client) join() string {
	cl.c.t.Helper()
	out := cl.mustShf("join", cl.c.url, cl.c.share, cl.token, cl.dir)
	fmt.Fprintf(mustAppend(cl.c.t, cl.log), "==== shf join\n%s", out)
	return out
}

func mustAppend(t *testing.T, p string) io.Writer {
	f, err := os.OpenFile(p, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { f.Close() })
	return f
}

// start runs shf daemon -v.
func (cl *client) start() {
	cl.c.t.Helper()
	if cl.daemon != nil && cl.daemon.running() {
		cl.c.t.Fatalf("%s: daemon already running", cl.name)
	}
	cl.daemon = startProc(cl.c.t, cl.name+" shf daemon", cl.log, cl.env(), shfBin, "daemon", "-v")
}

// stop ends the daemon with SIGTERM (it finishes the step in progress).
func (cl *client) stop() { cl.c.t.Helper(); cl.daemon.stop(cl.c.t) }

// kill ends the daemon and its rsync/ssh at once.
func (cl *client) kill() { cl.daemon.kill() }

func (cl *client) abs(p string) string { return filepath.Join(cl.dir, filepath.FromSlash(p)) }

// write creates or replaces p.
func (cl *client) write(p, content string) {
	cl.c.t.Helper()
	abs := cl.abs(p)
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		cl.c.t.Fatal(err)
	}
	if err := os.WriteFile(abs, []byte(content), 0o644); err != nil {
		cl.c.t.Fatal(err)
	}
}

func (cl *client) mkdir(p string) {
	cl.c.t.Helper()
	if err := os.MkdirAll(cl.abs(p), 0o755); err != nil {
		cl.c.t.Fatal(err)
	}
}

func (cl *client) remove(p string) {
	cl.c.t.Helper()
	if err := os.RemoveAll(cl.abs(p)); err != nil {
		cl.c.t.Fatal(err)
	}
}

func (cl *client) rename(from, to string) {
	cl.c.t.Helper()
	if err := os.MkdirAll(filepath.Dir(cl.abs(to)), 0o755); err != nil {
		cl.c.t.Fatal(err)
	}
	if err := os.Rename(cl.abs(from), cl.abs(to)); err != nil {
		cl.c.t.Fatal(err)
	}
}

func (cl *client) read(p string) (string, bool) {
	b, err := os.ReadFile(cl.abs(p))
	return string(b), err == nil
}

// has waits until p has content at this client.
func (cl *client) has(p, content string) {
	cl.c.t.Helper()
	eventually(cl.c.t, arrive, fmt.Sprintf("%s has %s", cl.name, p), func() error {
		got, ok := cl.read(p)
		if !ok {
			return errors.New("missing")
		}
		if got != content {
			return fmt.Errorf("content %q, want %q", got, content)
		}
		return nil
	})
}

// gone waits until p does not exist at this client.
func (cl *client) gone(p string) {
	cl.c.t.Helper()
	eventually(cl.c.t, arrive, fmt.Sprintf("%s lost %s", cl.name, p), func() error {
		if _, err := os.Lstat(cl.abs(p)); err == nil {
			return errors.New("still there")
		}
		return nil
	})
}

// isDir waits until p is a directory at this client.
func (cl *client) isDir(p string) {
	cl.c.t.Helper()
	eventually(cl.c.t, arrive, fmt.Sprintf("%s has directory %s", cl.name, p), func() error {
		fi, err := os.Stat(cl.abs(p))
		if err != nil {
			return err
		}
		if !fi.IsDir() {
			return errors.New("not a directory")
		}
		return nil
	})
}

// stored waits until the server's storage/ holds p with content.
func (c *cluster) stored(p, content string) {
	c.t.Helper()
	eventually(c.t, arrive, "storage has "+p, func() error {
		got := readFile(c.storage(p))
		if got != content {
			return fmt.Errorf("content %q, want %q", got, content)
		}
		return nil
	})
}

// ---- trees -------------------------------------------------------------------

// tree is a folder: path -> "dir/" or the SHA-256 of the file.
type tree map[string]string

func isClientTemp(p string) bool { return strings.HasPrefix(filepath.Base(p), ".shf-tmp-") }

func readTree(root string, skip func(string) bool) (tree, error) {
	t := tree{}
	err := filepath.WalkDir(root, func(abs string, d fs.DirEntry, err error) error {
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) && abs != root {
				return nil // removed while we walk
			}
			return err
		}
		rel, _ := filepath.Rel(root, abs)
		if rel == "." {
			return nil
		}
		rel = filepath.ToSlash(rel)
		if isClientTemp(rel) || skip(rel) {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if d.IsDir() {
			t[rel] = "dir/"
			return nil
		}
		if !d.Type().IsRegular() {
			return nil
		}
		h, err := hashFile(abs)
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				return nil
			}
			return err
		}
		t[rel] = h
		return nil
	})
	return t, err
}

func hashFile(p string) (string, error) {
	f, err := os.Open(p)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// diff describes how b differs from a ("" when equal).
func diff(aName string, a tree, bName string, b tree) string {
	var lines []string
	for p, h := range a {
		switch hb, ok := b[p]; {
		case !ok:
			lines = append(lines, fmt.Sprintf("only in %s: %s", aName, p))
		case hb != h:
			lines = append(lines, fmt.Sprintf("differs: %s", p))
		}
	}
	for p := range b {
		if _, ok := a[p]; !ok {
			lines = append(lines, fmt.Sprintf("only in %s: %s", bName, p))
		}
	}
	sort.Strings(lines)
	if len(lines) > 20 {
		lines = append(lines[:20], fmt.Sprintf("… and %d more", len(lines)-20))
	}
	return strings.Join(lines, "\n  ")
}

// sameAsStorage is nil when the client's folder equals storage/ (both
// without the paths the client skips).
func (cl *client) sameAsStorage() error {
	st, err := readTree(cl.c.storageDir(), cl.skip)
	if err != nil {
		return err
	}
	mine, err := readTree(cl.dir, cl.skip)
	if err != nil {
		return err
	}
	if d := diff("storage", st, cl.name, mine); d != "" {
		return fmt.Errorf("\n  %s", d)
	}
	return nil
}

// converged waits until every client's folder equals storage/, then
// checks that shfd verify is clean and the change log has no gaps: the
// common check at the end of every scenario.
func (c *cluster) converged(timeout time.Duration) {
	c.t.Helper()
	for _, cl := range c.clients {
		if cl.daemon == nil || cl.revoked {
			continue // never started, or revoked on purpose
		}
		eventually(c.t, timeout, cl.name+" equals storage/", cl.sameAsStorage)
	}
	c.verifyClean()
	c.noGaps()
}

// verifyClean runs shfd verify (through the running server's admin socket).
func (c *cluster) verifyClean() {
	c.t.Helper()
	out, code := c.shfd("verify", c.share)
	if code != 0 {
		c.t.Fatalf("shfd verify found issues (exit %d):\n%s", code, out)
	}
}

// noGaps checks that the change log is numbered 1..HEAD.
func (c *cluster) noGaps() {
	c.t.Helper()
	for i, e := range c.events() {
		if e.Seq != int64(i+1) {
			c.t.Fatalf("change log has a gap: event %d has seq %d", i+1, e.Seq)
		}
	}
}

// wantOnly checks that, of the files matching re in dir, there is exactly
// one, and returns its slash path.
func onlyMatch(t *testing.T, dir string, re *regexp.Regexp) string {
	t.Helper()
	var found []string
	filepath.WalkDir(dir, func(abs string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			rel, _ := filepath.Rel(dir, abs)
			if re.MatchString(filepath.ToSlash(rel)) {
				found = append(found, filepath.ToSlash(rel))
			}
		}
		return nil
	})
	if len(found) != 1 {
		t.Fatalf("%s: want exactly one file matching %s, found %v", dir, re, found)
	}
	return found[0]
}

// ctxT is a context that ends with the test.
func ctxT(t *testing.T) context.Context {
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	return ctx
}

// countMatches counts the files below dir whose slash path matches re.
func countMatches(dir string, re *regexp.Regexp) int {
	n := 0
	filepath.WalkDir(dir, func(abs string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			rel, _ := filepath.Rel(dir, abs)
			if re.MatchString(filepath.ToSlash(rel)) {
				n++
			}
		}
		return nil
	})
	return n
}

func errorf(format string, a ...any) error { return fmt.Errorf(format, a...) }
