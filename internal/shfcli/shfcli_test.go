package shfcli_test

import (
	"bytes"
	"context"
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
