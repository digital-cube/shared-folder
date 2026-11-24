package shfdcli

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/digital-cube/shared-folder/internal/fsinfo"
	"github.com/digital-cube/shared-folder/internal/server"
)

// syncBuf is a bytes.Buffer serve may write to while the test reads it.
type syncBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuf) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuf) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

// serveWith runs shfd serve with env and the given file system detector
// until stop is called (or the test ends).
func serveWith(t *testing.T, env map[string]string, detect fsinfo.Detector) (stop func(), stderr *syncBuf) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	ready := make(chan string, 1)
	done := make(chan int, 1)
	stderr = &syncBuf{}
	go func() {
		done <- Run([]string{"serve", "--listen", "127.0.0.1:0"}, Env{
			Stdout: &bytes.Buffer{}, Stderr: stderr, Ctx: ctx,
			Getenv:   func(k string) string { return env[k] },
			Ready:    func(addr string) { ready <- addr },
			DetectFS: detect,
		})
	}()
	select {
	case <-ready:
	case code := <-done:
		t.Fatalf("serve exited %d: %s", code, stderr.String())
	case <-time.After(10 * time.Second):
		t.Fatal("serve did not start")
	}
	stopped := false
	stop = func() {
		if stopped {
			return
		}
		stopped = true
		cancel()
		select {
		case code := <-done:
			if code != 0 {
				t.Errorf("serve exit %d: %s", code, stderr.String())
			}
		case <-time.After(10 * time.Second):
			t.Fatal("serve did not stop")
		}
	}
	t.Cleanup(stop)
	return stop, stderr
}

func local(string) (fsinfo.Info, error) { return fsinfo.Info{Type: "ext4"}, nil }

// Every command works with the databases in their own db-root; they, the
// lock and the admin socket are there, the folders' data is not.
func TestSeparateDBRootCommands(t *testing.T) {
	root, db := t.TempDir(), t.TempDir()
	env := map[string]string{EnvDataRoot: root, EnvDBRoot: db}
	code, out, errOut := run(t, env, "share", "create", "tim", "--admin", "ana")
	if code != 0 {
		t.Fatalf("share create: %s", errOut)
	}
	id := idRE.FindStringSubmatch(out)[1]
	if code, _, errOut := run(t, env, "member", "add", id, "bob"); code != 0 {
		t.Fatalf("member add: %s", errOut)
	}
	// --db-root as a flag works the same as the variable.
	if code, out, _ := run(t, map[string]string{EnvDataRoot: root}, "member", "list", id, "--db-root", db); code != 0 || !strings.Contains(out, "bob") {
		t.Errorf("member list --db-root: %d %s", code, out)
	}

	stop, stderr := serveWith(t, env, local)
	for _, p := range []string{registryPath(db), filepath.Join(db, id, server.DBFile), filepath.Join(db, "serve.lock"), filepath.Join(db, AdminSocket)} {
		if _, err := os.Stat(p); err != nil {
			t.Errorf("%s: %v", p, err)
		}
	}
	for _, p := range []string{registryPath(root), filepath.Join(root, id, server.DBFile), filepath.Join(root, "serve.lock"), filepath.Join(root, AdminSocket)} {
		if _, err := os.Stat(p); err == nil {
			t.Errorf("%s exists", p)
		}
	}
	if _, err := os.Stat(filepath.Join(root, id, server.StorageDir)); err != nil {
		t.Errorf("storage: %v", err)
	}
	if strings.Contains(stderr.String(), "network file system") {
		t.Errorf("warning on a local disk: %s", stderr.String())
	}
	if !strings.Contains(stderr.String(), "db_root="+db) {
		t.Errorf("serve does not log its db-root: %s", stderr.String())
	}
	// A second server on the same databases is refused.
	if code, _, errOut := run(t, env, "serve", "--listen", "127.0.0.1:0"); code != 1 || !strings.Contains(errOut, "already served") {
		t.Errorf("second serve: %d %s", code, errOut)
	}
	// The admin commands reach the server through the socket in the db-root.
	for _, args := range [][]string{{"stats", id}, {"trash", "list", id}, {"verify", id}} {
		if code, _, errOut := run(t, env, args...); code != 0 {
			t.Errorf("%v: %d %s", args, code, errOut)
		}
	}
	stop()
	// And in-process once it stopped.
	if code, out, errOut := run(t, env, "stats", id); code != 0 || !strings.Contains(out, "bob") {
		t.Errorf("stats without serve: %d %s %s", code, out, errOut)
	}
	if code, out, _ := run(t, env, "share", "list"); code != 0 || !strings.Contains(out, id) {
		t.Errorf("share list: %d %s", code, out)
	}
}

// registryPath is <dir>/registry.db.
func registryPath(dir string) string { return filepath.Join(dir, server.RegistryFile) }

// serve warns, and keeps running, when the db-root is on a network file
// system.
func TestServeWarnsNetworkDB(t *testing.T) {
	root := t.TempDir()
	env := map[string]string{EnvDataRoot: root}
	var asked []string
	var mu sync.Mutex
	nfs := func(p string) (fsinfo.Info, error) {
		mu.Lock()
		asked = append(asked, p)
		mu.Unlock()
		return fsinfo.Info{Type: "nfs", Network: true}, nil
	}
	stop, stderr := serveWith(t, env, nfs)
	out := stderr.String()
	if !strings.Contains(out, "level=WARN") || !strings.Contains(out, "network file system") ||
		!strings.Contains(out, "fs=nfs") || !strings.Contains(out, "--db-root") {
		t.Errorf("no warning:\n%s", out)
	}
	if !strings.Contains(out, "shfd listening") {
		t.Errorf("serve did not go on: %s", out)
	}
	stop()
	mu.Lock()
	defer mu.Unlock()
	if abs, _ := filepath.Abs(root); len(asked) != 1 || asked[0] != abs {
		t.Errorf("detector asked about %v", asked)
	}
}

func TestDBMoveCommand(t *testing.T) {
	root := t.TempDir()
	env := map[string]string{EnvDataRoot: root}
	_, out, _ := run(t, env, "share", "create", "tim", "--admin", "ana")
	id := idRE.FindStringSubmatch(out)[1]
	run(t, env, "member", "add", id, "bob")
	to := filepath.Join(t.TempDir(), "db")

	if code, _, _ := run(t, env, "db", "move"); code != 2 {
		t.Errorf("db move without --to: %d", code)
	}

	// Not while the server runs.
	stop, _ := serveWith(t, env, local)
	code, _, errOut := run(t, env, "db", "move", "--to", to)
	if code != 1 || !strings.Contains(errOut, "stop shfd serve") {
		t.Errorf("db move while serving: %d %s", code, errOut)
	}
	if _, err := os.Stat(registryPath(to)); err == nil {
		t.Error("copied while serving")
	}
	stop()

	code, out, errOut = run(t, env, "db", "move", "--to", to)
	if code != 0 {
		t.Fatalf("db move: %d %s", code, errOut)
	}
	for _, want := range []string{"copied registry", "copied " + id, "integrity_check ok", "Environment=SHFD_DB_ROOT=" + to, "keys sync"} {
		if !strings.Contains(out, want) {
			t.Errorf("output misses %q:\n%s", want, out)
		}
	}
	for _, p := range []string{registryPath(root), filepath.Join(root, id, server.DBFile)} {
		if _, err := os.Stat(p); err != nil {
			t.Errorf("original removed: %v", err)
		}
	}

	// Without the new db-root the commands are refused; with it they work.
	if code, _, errOut := run(t, env, "share", "list"); code != 1 || !strings.Contains(errOut, "--db-root "+to) {
		t.Errorf("share list on the old copies: %d %s", code, errOut)
	}
	env[EnvDBRoot] = to
	if code, out, _ := run(t, env, "member", "list", id); code != 0 || !strings.Contains(out, "bob") {
		t.Errorf("member list on the copies: %d %s", code, out)
	}
	stop, _ = serveWith(t, env, local)
	stop()
	// A second move onto databases is refused.
	if code, _, errOut := run(t, env, "db", "move", "--to", to); code != 1 {
		t.Errorf("move onto itself: %d %s", code, errOut)
	}
}

// The forced command of authorized_keys carries --db-root when the
// databases live apart, since sshd passes no $SHFD_DB_ROOT to rsync-gate.
func TestForcedCommandDBRoot(t *testing.T) {
	root, db := t.TempDir(), t.TempDir()
	ak := filepath.Join(t.TempDir(), "authorized_keys")
	env := map[string]string{EnvDataRoot: root, EnvDBRoot: db, EnvAuthorizedKeys: ak, EnvBinary: "/opt/shfd/bin/shfd"}
	_, out, _ := run(t, env, "share", "create", "tim")
	id := idRE.FindStringSubmatch(out)[1]
	run(t, env, "member", "add", id, "bob")

	var sf sshFlags
	sf.authorizedKeys, sf.binary = ak, "/opt/shfd/bin/shfd"
	opts, err := sf.options(roots{data: root, db: db}, Env{})
	if err != nil {
		t.Fatal(err)
	}
	opts.DBRoot = db
	srv, err := server.Open(root, opts)
	if err != nil {
		t.Fatal(err)
	}
	sh, _ := srv.Share(id)
	bob, _ := sh.MemberByName("bob")
	if _, _, err := sh.SetSSHKey(bob, testKey); err != nil {
		t.Fatal(err)
	}
	srv.Close()
	data, _ := os.ReadFile(ak)
	if !strings.Contains(string(data), `command="/opt/shfd/bin/shfd rsync-gate --data-root `+root+` --db-root `+db+` --share `+id+` --member 2",`) {
		t.Fatalf("authorized_keys:\n%s", data)
	}
	os.WriteFile(ak, nil, 0o600)
	if code, _, errOut := run(t, env, "keys", "sync"); code != 0 {
		t.Fatal(errOut)
	}
	if data, _ := os.ReadFile(ak); !strings.Contains(string(data), "--db-root "+db+" ") {
		t.Errorf("keys sync:\n%s", data)
	}
	// rsync-gate finds the folder through --db-root (and refuses an
	// unknown command, which is all it gets here).
	var o, e bytes.Buffer
	code := Run([]string{"rsync-gate", "--data-root", root, "--db-root", db, "--share", id, "--member", "2"}, Env{
		Stdout: &o, Stderr: &e, Stdin: strings.NewReader(""),
		Getenv: func(k string) string {
			if k == "SSH_ORIGINAL_COMMAND" {
				return "ls"
			}
			return ""
		},
	})
	if code == 0 || strings.Contains(e.String(), "no folder") {
		t.Errorf("rsync-gate: %d %s", code, e.String())
	}
}
