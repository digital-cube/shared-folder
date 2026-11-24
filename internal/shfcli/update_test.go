//go:build unix

package shfcli_test

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/digital-cube/shared-folder/internal/client"
	"github.com/digital-cube/shared-folder/internal/shfcli"
	"github.com/digital-cube/shared-folder/internal/update"
	"github.com/digital-cube/shared-folder/internal/update/updatetest"
	"github.com/digital-cube/shared-folder/internal/version"
)

// updEnv is a client whose binary is a script reporting "old", in a
// directory of its own, with a state home where folders can be joined
// (written directly: no server behind them is needed).
type updEnv struct {
	t    *testing.T
	home string
	exe  string
	hdir string // $HOME, where no service is installed
}

func newUpdEnv(t *testing.T) *updEnv {
	e := &updEnv{t: t, home: t.TempDir(), exe: filepath.Join(t.TempDir(), "shf"), hdir: t.TempDir()}
	if err := os.WriteFile(e.exe, updatetest.Script(version.Version), 0o755); err != nil {
		t.Fatal(err)
	}
	return e
}

// joinedTo records a folder joined to server.
func (e *updEnv) joinedTo(server, share string) {
	e.t.Helper()
	dir := filepath.Join(e.home, share)
	folder := filepath.Join(e.t.TempDir(), share)
	os.MkdirAll(dir, 0o700)
	os.MkdirAll(folder, 0o755)
	b, _ := json.Marshal(map[string]string{"url": server, "share_id": share, "folder": folder})
	os.WriteFile(filepath.Join(dir, "config.json"), b, 0o600)
	os.WriteFile(filepath.Join(dir, "token"), []byte("tok"), 0o600)
}

func (e *updEnv) env(stdin string, out *bytes.Buffer) shfcli.Env {
	vars := map[string]string{client.EnvStateHome: e.home, "HOME": e.hdir, "XDG_CONFIG_HOME": filepath.Join(e.hdir, ".config")}
	return shfcli.Env{
		Stdout: out, Stderr: out, Ctx: context.Background(),
		Getenv: func(k string) string { return vars[k] },
		Stdin:  strings.NewReader(stdin), Exe: e.exe,
	}
}

func (e *updEnv) run(stdin string, args ...string) (int, string) {
	var out bytes.Buffer
	code := shfcli.Run(args, e.env(stdin, &out))
	return code, out.String()
}

func (e *updEnv) version() string {
	v, err := update.BinaryVersion(context.Background(), e.exe)
	if err != nil {
		e.t.Fatal(err)
	}
	return v
}

func TestUpdateCheckAndInstall(t *testing.T) {
	srv := updatetest.New(t, "v2")
	srv.AddPackage("v2", runtime.GOOS, runtime.GOARCH, "v2")
	e := newUpdEnv(t)
	e.joinedTo(srv.URL, "11111111-1111-1111-1111-111111111111")

	code, out := e.run("", "update", "--check")
	if code != 0 || !strings.Contains(out, "update available: shf "+version.Version+" → v2") {
		t.Fatalf("--check: %d\n%s", code, out)
	}
	if e.version() != version.Version || srv.Hits("/downloads/SHA256SUMS") != 0 {
		t.Fatal("--check installed something")
	}

	code, out = e.run("", "version")
	if code != 0 || !strings.Contains(out, "server "+srv.URL+": v2, update available (shf update)") {
		t.Errorf("version: %d\n%s", code, out)
	}
	if code, out = e.run("", "version", "--short"); out != "shf "+version.Version+"\n" {
		t.Errorf("version --short: %d %q", code, out)
	}

	// asked, and the answer is no
	code, out = e.run("n\n", "update")
	if code != 0 || !strings.Contains(out, "not updated") || e.version() != version.Version {
		t.Fatalf("answer no: %d\n%s", code, out)
	}

	code, out = e.run("y\n", "update")
	if code != 0 || !strings.Contains(out, "installed shf v2 at "+e.exe) || !strings.Contains(out, "service is not installed") {
		t.Fatalf("update: %d\n%s", code, out)
	}
	if e.version() != "v2" {
		t.Fatalf("binary reports %q", e.version())
	}
}

func TestUpdateNothingToDo(t *testing.T) {
	srv := updatetest.New(t, version.Version)
	e := newUpdEnv(t)
	e.joinedTo(srv.URL, "11111111-1111-1111-1111-111111111111")
	if code, out := e.run("", "update", "--yes"); code != 0 || !strings.Contains(out, "up to date") {
		t.Fatalf("up to date: %d\n%s", code, out)
	}
	srv.SetVersion("v2")
	srv.SetDownloads(false)
	if code, out := e.run("", "update", "--yes"); code != 0 || !strings.Contains(out, "publishes no packages") {
		t.Fatalf("no downloads: %d\n%s", code, out)
	}
	srv.SetDownloads(true)
	srv.AddPackage("v2", "plan9", "mips", "v2")
	if code, out := e.run("", "update", "--yes"); code != 0 || !strings.Contains(out, "no package of it for "+runtime.GOOS) {
		t.Fatalf("no package: %d\n%s", code, out)
	}
	if e.version() != version.Version {
		t.Fatal("binary changed")
	}
	// joined nowhere and no --server
	e2 := newUpdEnv(t)
	if code, out := e2.run("", "update"); code != 1 || !strings.Contains(out, "--server") {
		t.Fatalf("no server: %d\n%s", code, out)
	}
}

func TestUpdateServersDisagree(t *testing.T) {
	a := updatetest.New(t, "v2")
	b := updatetest.New(t, "v3")
	a.AddPackage("v2", runtime.GOOS, runtime.GOARCH, "v2")
	b.AddPackage("v3", runtime.GOOS, runtime.GOARCH, "v3")
	e := newUpdEnv(t)
	e.joinedTo(a.URL, "11111111-1111-1111-1111-111111111111")
	e.joinedTo(b.URL, "22222222-2222-2222-2222-222222222222")

	code, out := e.run("", "update", "--yes")
	if code != 1 || !strings.Contains(out, a.URL+" runs v2") || !strings.Contains(out, b.URL+" runs v3") || !strings.Contains(out, "--server URL") {
		t.Fatalf("disagree: %d\n%s", code, out)
	}
	if e.version() != version.Version {
		t.Fatal("binary changed")
	}
	code, out = e.run("", "update", "--yes", "--server", b.URL)
	if code != 0 || e.version() != "v3" {
		t.Fatalf("--server: %d %s\n%s", code, e.version(), out)
	}
}

func TestUpdateNotWritable(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root writes anywhere")
	}
	srv := updatetest.New(t, "v2")
	srv.AddPackage("v2", runtime.GOOS, runtime.GOARCH, "v2")
	e := newUpdEnv(t)
	dir := filepath.Dir(e.exe)
	os.Chmod(dir, 0o555)
	t.Cleanup(func() { os.Chmod(dir, 0o755) })

	code, out := e.run("", "update", "--yes", "--server", srv.URL)
	if code != 1 || !strings.Contains(out, dir+" is not writable") || !strings.Contains(out, "sudo install -m 0755 ") ||
		!strings.Contains(out, " "+e.exe+"\n") {
		t.Fatalf("not writable: %d\n%s", code, out)
	}
	if e.version() != version.Version {
		t.Fatal("binary changed")
	}
	// the command it prints installs a verified v2
	i := strings.Index(out, "sudo install -m 0755 ")
	staged := strings.Fields(out[i:])[4]
	if v, err := update.BinaryVersion(context.Background(), staged); v != "v2" || err != nil {
		t.Errorf("staged %s: %q %v", staged, v, err)
	}
	os.RemoveAll(filepath.Dir(staged))
}

// TestDaemonUpdatesAndRestarts: shf daemon installs the server's version
// at start and then starts the new binary in its place.
func TestDaemonUpdatesAndRestarts(t *testing.T) {
	srv := updatetest.New(t, "v2")
	srv.AddPackage("v2", runtime.GOOS, runtime.GOARCH, "v2")
	e := newUpdEnv(t)
	e.joinedTo(srv.URL, "11111111-1111-1111-1111-111111111111")
	os.WriteFile(filepath.Join(e.home, "settings.json"), []byte(`{"poll": "1h", "retry_max": "1h"}`), 0o600)

	var out bytes.Buffer
	env := e.env("", &out)
	var mu sync.Mutex
	env.Stdout, env.Stderr = lockedWriter{&mu, &out}, lockedWriter{&mu, &out}
	var exec []string
	env.Exec = func(exe string, argv []string) error {
		exec = append([]string{exe}, argv...)
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	env.Ctx = ctx
	code := shfcli.Run([]string{"daemon", "-v"}, env)
	mu.Lock()
	log := out.String()
	mu.Unlock()
	if code != 0 || ctx.Err() != nil {
		t.Fatalf("daemon: %d %v\n%s", code, ctx.Err(), log)
	}
	if strings.Join(exec, " ") != e.exe+" "+e.exe+" daemon -v" {
		t.Errorf("exec %q", exec)
	}
	if e.version() != "v2" {
		t.Errorf("binary reports %q", e.version())
	}
	if strings.Count(log, "auto-update") != 1 || !strings.Contains(log, "auto-update: installed shf v2") {
		t.Errorf("log:\n%s", log)
	}
}

// TestDaemonAutoUpdateOff: auto_update false in settings.json, and the
// daemon does not even ask the server.
func TestDaemonAutoUpdateOff(t *testing.T) {
	srv := updatetest.New(t, "v2")
	srv.AddPackage("v2", runtime.GOOS, runtime.GOARCH, "v2")
	e := newUpdEnv(t)
	e.joinedTo(srv.URL, "11111111-1111-1111-1111-111111111111")
	os.WriteFile(filepath.Join(e.home, "settings.json"), []byte(`{"auto_update": false, "poll": "1h", "retry_max": "1h"}`), 0o600)

	var out bytes.Buffer
	var mu sync.Mutex
	env := e.env("", &out)
	env.Stdout, env.Stderr = lockedWriter{&mu, &out}, lockedWriter{&mu, &out}
	env.Exec = func(string, []string) error { t.Error("restarted"); return nil }
	ctx, cancel := context.WithTimeout(context.Background(), 1500*time.Millisecond)
	defer cancel()
	env.Ctx = ctx
	code := shfcli.Run([]string{"daemon"}, env)
	mu.Lock()
	log := out.String()
	mu.Unlock()
	if code != 0 || !strings.Contains(log, "auto-update off") {
		t.Errorf("daemon: %d\n%s", code, log)
	}
	if srv.Hits("/v1/downloads") != 0 || srv.Hits("/downloads/SHA256SUMS") != 0 || e.version() != version.Version {
		t.Error("updated although auto_update is false")
	}
}

type lockedWriter struct {
	mu *sync.Mutex
	b  *bytes.Buffer
}

func (w lockedWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.b.Write(p)
}
