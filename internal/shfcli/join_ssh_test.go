package shfcli_test

import (
	"bytes"
	"context"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/digital-cube/shared-folder/internal/authkeys"
	"github.com/digital-cube/shared-folder/internal/client"
	"github.com/digital-cube/shared-folder/internal/server"
	"github.com/digital-cube/shared-folder/internal/server/httpapi"
	"github.com/digital-cube/shared-folder/internal/shfcli"
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

// TestJoinWarnsKeyReplaced: the same member joined from a second device
// replaces the first device's key; shf join says so, with the replaced
// key's fingerprint. Joining again from the same device does not warn.
func TestJoinWarnsKeyReplaced(t *testing.T) {
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
	share, _, err := srv.CreateShare("tim", "ana")
	if err != nil {
		t.Fatal(err)
	}
	sh, _ := srv.Share(share)
	token, _ := sh.AddMember("marko", false)

	join := func(home, folder string) (stdout, stderr string) {
		t.Helper()
		var out, errb bytes.Buffer
		code := shfcli.Run([]string{"join", hs.URL, share, token, folder}, shfcli.Env{
			Stdout: &out, Stderr: &errb, Ctx: context.Background(),
			Getenv: func(k string) string { return map[string]string{client.EnvStateHome: home}[k] },
		})
		if code != 0 {
			t.Fatalf("join %s: %d\n%s%s", folder, code, out.String(), errb.String())
		}
		return out.String(), errb.String()
	}
	macHome, macDir := t.TempDir(), filepath.Join(t.TempDir(), "mac")
	linuxHome, linuxDir := t.TempDir(), filepath.Join(t.TempDir(), "linux")

	if _, e := join(macHome, macDir); strings.Contains(e, "warning") {
		t.Errorf("first join warns:\n%s", e)
	}
	if _, e := join(macHome, macDir); strings.Contains(e, "warning") {
		t.Errorf("same device joining again warns:\n%s", e)
	}
	ms, _ := sh.Members()
	var macFP string
	for _, m := range ms {
		if m.Username == "marko" {
			macFP = authkeys.Fingerprint(m.SSHPubkey)
		}
	}
	_, e := join(linuxHome, linuxDir)
	want := "shf: warning: " + client.KeyReplacedWarning(macFP)
	if macFP == "" || !strings.Contains(e, want) {
		t.Errorf("second device: stderr\n%s\nwant %q", e, want)
	}
}
