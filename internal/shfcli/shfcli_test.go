package shfcli_test

import (
	"bytes"
	"context"
	"fmt"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/digital-cube/shared-folder/internal/client"
	"github.com/digital-cube/shared-folder/internal/server"
	"github.com/digital-cube/shared-folder/internal/server/httpapi"
	"github.com/digital-cube/shared-folder/internal/shfcli"
	"github.com/digital-cube/shared-folder/internal/transport"
)

func TestCommands(t *testing.T) {
	root := t.TempDir()
	srv, err := server.Open(root, server.Options{})
	if err != nil {
		t.Fatal(err)
	}
	hs := httptest.NewServer(httpapi.New(srv, nil))
	defer func() { hs.Close(); srv.Close() }()
	share, token, err := srv.CreateShare("tim", "ana")
	if err != nil {
		t.Fatal(err)
	}
	home, folder := t.TempDir(), filepath.Join(t.TempDir(), "tim")
	if err := os.MkdirAll(folder, 0o755); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(folder, "hello.txt"), []byte("hi"), 0o644)
	os.WriteFile(filepath.Join(folder, "bad?.txt"), []byte("x"), 0o644)

	run := func(args ...string) (int, string) {
		var out, errb bytes.Buffer
		code := shfcli.Run(args, shfcli.Env{
			Stdout: &out, Stderr: &errb, Ctx: context.Background(),
			Getenv:  func(k string) string { return map[string]string{client.EnvStateHome: home}[k] },
			Options: client.Options{Transport: transport.Local{DataRoot: root}},
		})
		return code, out.String() + errb.String()
	}

	code, out := run("join", hs.URL, share, token, folder)
	if code != 0 || !strings.Contains(out, "joined "+folder) || !strings.Contains(out, "↑ put hello.txt") {
		t.Fatalf("join: %d\n%s", code, out)
	}
	if !strings.Contains(out, "bad?.txt") {
		t.Errorf("join does not report the rejected name:\n%s", out)
	}
	if b, err := os.ReadFile(filepath.Join(root, share, "storage", "hello.txt")); err != nil || string(b) != "hi" {
		t.Fatalf("server: %q %v", b, err)
	}

	os.WriteFile(filepath.Join(folder, "second.txt"), []byte("2"), 0o644)
	code, out = run("status", folder)
	if code != 0 || !strings.Contains(out, "pending:    1 changes") || !strings.Contains(out, "bad?.txt") {
		t.Errorf("status: %d\n%s", code, out)
	}
	code, out = run("sync", folder)
	if code != 0 || !strings.Contains(out, "sent 1") {
		t.Errorf("sync: %d\n%s", code, out)
	}
	code, out = run("status", folder)
	if code != 0 || !strings.Contains(out, "seq:        local 2, server HEAD 2") || !strings.Contains(out, "pending:    0 changes") {
		t.Errorf("status after sync: %d\n%s", code, out)
	}
	code, out = run("log", folder, "-n", "5")
	if code != 0 || !strings.Contains(out, "↑ put second.txt") || strings.Count(out, "seq ") != 2 {
		t.Errorf("log: %d\n%s", code, out)
	}

	if code, out = run("sync", t.TempDir()); code != 1 || !strings.Contains(out, "not inside a joined folder") {
		t.Errorf("sync outside a joined folder: %d %s", code, out)
	}
	if code, _ = run("join", hs.URL, share); code != 2 {
		t.Errorf("join with too few arguments: exit %d", code)
	}
	if code, _ = run("frobnicate"); code != 2 {
		t.Errorf("unknown command: exit %d", code)
	}

	// a failing cycle exits 1: the server is gone
	hs.Close()
	if code, out = run("sync", folder); code != 1 {
		t.Errorf("sync against a stopped server: %d %s", code, out)
	}
}

func TestMassDeleteCommands(t *testing.T) {
	root := t.TempDir()
	srv, err := server.Open(root, server.Options{})
	if err != nil {
		t.Fatal(err)
	}
	hs := httptest.NewServer(httpapi.New(srv, nil))
	defer func() { hs.Close(); srv.Close() }()
	share, token, err := srv.CreateShare("tim", "ana")
	if err != nil {
		t.Fatal(err)
	}
	home, folder := t.TempDir(), filepath.Join(t.TempDir(), "tim")
	os.MkdirAll(folder, 0o755)
	for i := range 100 {
		os.WriteFile(filepath.Join(folder, fmt.Sprintf("f%03d.txt", i)), []byte(fmt.Sprint(i)), 0o644)
	}
	// the limit comes from settings.json: 5 files (10 % would allow 10)
	os.WriteFile(filepath.Join(home, client.SettingsFile), []byte(`{"mass_delete": {"files": 5, "percent": 10}}`), 0o644)
	run := func(args ...string) (int, string) {
		var out, errb bytes.Buffer
		code := shfcli.Run(args, shfcli.Env{
			Stdout: &out, Stderr: &errb, Ctx: context.Background(),
			Getenv:  func(k string) string { return map[string]string{client.EnvStateHome: home}[k] },
			Options: client.Options{Transport: transport.Local{DataRoot: root}},
		})
		return code, out.String() + errb.String()
	}
	if code, out := run("join", hs.URL, share, token, folder); code != 0 {
		t.Fatalf("join: %s", out)
	}
	remove := func() {
		for i := range 6 {
			os.Remove(filepath.Join(folder, fmt.Sprintf("f%03d.txt", i)))
		}
	}
	onServer := func(i int) bool {
		_, err := os.Stat(filepath.Join(root, share, "storage", fmt.Sprintf("f%03d.txt", i)))
		return err == nil
	}

	remove()
	code, out := run("sync", folder)
	if code != 0 || !strings.Contains(out, "BLOCKED") || !onServer(0) {
		t.Fatalf("sync: %d\n%s", code, out)
	}
	code, out = run("status", folder)
	if code != 0 || !strings.Contains(out, "blocked: 6 deletions held back") || !strings.Contains(out, "delete f000.txt") {
		t.Fatalf("status: %d\n%s", code, out)
	}
	code, out = run("restore-local", folder)
	if code != 0 || !strings.Contains(out, "restoring 6") {
		t.Fatalf("restore-local: %d\n%s", code, out)
	}
	if _, err := os.Stat(filepath.Join(folder, "f000.txt")); err != nil {
		t.Fatalf("not restored: %v", err)
	}
	if code, out = run("status", folder); !strings.Contains(out, "state:      ok") {
		t.Fatalf("status after restore: %s", out)
	}

	remove()
	run("sync", folder)
	code, out = run("confirm", folder)
	if code != 0 || !strings.Contains(out, "released 6") || onServer(0) {
		t.Fatalf("confirm: %d\n%s", code, out)
	}
	if code, out = run("confirm", folder); code != 0 || !strings.Contains(out, "nothing to do") {
		t.Fatalf("second confirm: %d\n%s", code, out)
	}
}
