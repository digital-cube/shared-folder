package client_test

import (
	"context"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/digital-cube/shared-folder/internal/authkeys"
	"github.com/digital-cube/shared-folder/internal/client"
	"github.com/digital-cube/shared-folder/internal/server"
	"github.com/digital-cube/shared-folder/internal/server/httpapi"
	"github.com/digital-cube/shared-folder/internal/shfdcli"
	"github.com/digital-cube/shared-folder/internal/sshdtest"
)

// The test binary doubles as shfd: sshd runs "<test binary> rsync-gate …"
// as the forced command of each member's key.
func TestMain(m *testing.M) {
	if len(os.Args) > 1 && os.Args[1] == "rsync-gate" {
		os.Exit(shfdcli.Main(os.Args[1:]))
	}
	os.Exit(m.Run())
}

// TestJoinOverSSH joins two members with real rsync over SSH: the key is
// created and registered, the endpoint comes from the server and the host
// key is remembered by join.
func TestJoinOverSSH(t *testing.T) {
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	ak := filepath.Join(t.TempDir(), "authorized_keys")
	sshd := sshdtest.Start(t, ak)
	srv, err := server.Open(root, server.Options{
		AuthorizedKeys: &authkeys.File{Path: ak, Command: []string{self, "rsync-gate", "--data-root", root}},
		SSH:            server.SSHEndpoint{User: sshd.User, Host: "127.0.0.1", Port: sshd.Port},
	})
	if err != nil {
		t.Fatal(err)
	}
	hs := httptest.NewServer(httpapi.New(srv, nil))
	defer func() { hs.Close(); srv.Close() }()
	share, anaTok, err := srv.CreateShare("tim", "ana")
	if err != nil {
		t.Fatal(err)
	}
	sh, _ := srv.Share(share)
	bobTok, _ := sh.AddMember("bob", false)

	ctx := context.Background()
	join := func(user, tok string, files map[string]string) (*client.Client, string) {
		t.Helper()
		home, dir := filepath.Join(t.TempDir(), "state home"), filepath.Join(t.TempDir(), user)
		for p, c := range files {
			os.MkdirAll(filepath.Dir(filepath.Join(dir, p)), 0o755)
			os.WriteFile(filepath.Join(dir, p), []byte(c), 0o644)
		}
		c, res, err := client.Join(ctx, client.JoinRequest{Home: home, URL: hs.URL, ShareID: share, Token: tok, Folder: dir}, client.Options{})
		if err != nil {
			t.Fatalf("%s: join: %v (%+v)", user, err, res)
		}
		t.Cleanup(func() { c.Close() })
		cfg := c.Config()
		if cfg.SSH == nil || cfg.SSH.Port != sshd.Port || cfg.SSH.User != sshd.User {
			t.Errorf("%s: endpoint %+v", user, cfg.SSH)
		}
		for _, f := range []string{"id_ed25519", "token"} {
			if fi, err := os.Stat(filepath.Join(cfg.Dir, f)); err != nil || fi.Mode().Perm() != 0o600 {
				t.Errorf("%s: %s: %v %v", user, f, fi.Mode(), err)
			}
		}
		if fi, err := os.Stat(filepath.Join(cfg.Dir, "known_hosts")); err != nil || fi.Size() == 0 {
			t.Errorf("%s: host key not remembered: %v", user, err)
		}
		return c, dir
	}

	ana, anaDir := join("ana", anaTok, map[string]string{"plan.txt": "v1", "img/logo.svg": "<svg/>"})
	bob, bobDir := join("bob", bobTok, nil)
	if b, _ := os.ReadFile(filepath.Join(bobDir, "img/logo.svg")); string(b) != "<svg/>" {
		t.Fatalf("bob: logo %q", b)
	}
	os.WriteFile(filepath.Join(bobDir, "plan.txt"), []byte("v2 from bob"), 0o644)
	if _, err := bob.Sync(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := ana.Sync(ctx); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(filepath.Join(anaDir, "plan.txt")); string(b) != "v2 from bob" {
		t.Fatalf("ana: plan %q", b)
	}
}
