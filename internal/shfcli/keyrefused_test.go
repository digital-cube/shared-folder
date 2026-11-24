package shfcli_test

import (
	"bytes"
	"context"
	"fmt"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/digital-cube/shared-folder/internal/client"
	"github.com/digital-cube/shared-folder/internal/server"
	"github.com/digital-cube/shared-folder/internal/server/httpapi"
	"github.com/digital-cube/shared-folder/internal/shfcli"
	"github.com/digital-cube/shared-folder/internal/transport"
	"github.com/digital-cube/shared-folder/internal/transport/rsyncssh"
)

// refusing is the local transport, or, while refuse is set, a server
// whose sshd refuses the key.
type refusing struct {
	transport.Transport
	refuse atomic.Bool
}

func (r *refusing) Send(ctx context.Context, u transport.Upload, src string) error {
	if r.refuse.Load() {
		return fmt.Errorf("rsync tmp/x: %w: ana@127.0.0.1: Permission denied (publickey).", rsyncssh.ErrAuth)
	}
	return r.Transport.Send(ctx, u, src)
}

// TestKeyRefusedCommands: shf sync says what to do when the server
// refuses the key, shf status shows the state until shf join is run
// again with the member's own token.
func TestKeyRefusedCommands(t *testing.T) {
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
	tr := &refusing{Transport: transport.Local{DataRoot: root}}
	run := func(args ...string) (int, string) {
		var out, errb bytes.Buffer
		code := shfcli.Run(args, shfcli.Env{
			Stdout: &out, Stderr: &errb, Ctx: context.Background(),
			Getenv:  func(k string) string { return map[string]string{client.EnvStateHome: home}[k] },
			Options: client.Options{Transport: tr},
		})
		return code, out.String() + errb.String()
	}
	if code, out := run("join", hs.URL, share, token, folder); code != 0 {
		t.Fatalf("join: %s", out)
	}
	if code, out := run("status", folder); code != 0 || !strings.Contains(out, "state:      ok") {
		t.Fatalf("status: %d\n%s", code, out)
	}

	tr.refuse.Store(true)
	os.WriteFile(filepath.Join(folder, "a.txt"), []byte("a"), 0o644)
	code, out := run("sync", folder)
	if code != 1 || !strings.Contains(out, client.KeyRefusedHint) {
		t.Errorf("sync with a refused key: %d\n%s", code, out)
	}
	code, out = run("status", folder)
	if code != 0 || !strings.Contains(out, "state:      key refused") || !strings.Contains(out, client.KeyRefusedHint) {
		t.Errorf("status with a refused key: %d\n%s", code, out)
	}

	// the remedy the message names
	tr.refuse.Store(false)
	if code, out := run("join", hs.URL, share, token, folder); code != 0 || !strings.Contains(out, "↑ put a.txt") {
		t.Fatalf("joining again: %d\n%s", code, out)
	}
	if code, out = run("status", folder); code != 0 || !strings.Contains(out, "state:      ok") {
		t.Errorf("status after joining again: %d\n%s", code, out)
	}
}
