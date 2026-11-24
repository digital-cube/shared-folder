package shfdcli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/digital-cube/shared-folder/internal/authkeys"
	"github.com/digital-cube/shared-folder/internal/server"
)

const testKey = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIOMqqnkVzrm0SdG6UOoqKLsabgH5C9okWi0dh2l9GKJl bob@laptop"

func TestKeysRevokeAndGate(t *testing.T) {
	root := t.TempDir()
	ak := filepath.Join(t.TempDir(), "authorized_keys")
	os.WriteFile(ak, []byte("# own line\n"), 0o600)
	env := map[string]string{EnvDataRoot: root, EnvAuthorizedKeys: ak, EnvBinary: "/opt/shfd/bin/shfd"}

	_, out, _ := run(t, env, "share", "create", "tim")
	id := idRE.FindStringSubmatch(out)[1]
	run(t, env, "member", "add", id, "bob")

	// Register bob's key the way the API does.
	var sf sshFlags
	sf.authorizedKeys, sf.binary = ak, "/opt/shfd/bin/shfd"
	opts, err := sf.options(root, Env{})
	if err != nil {
		t.Fatal(err)
	}
	srv, _ := server.Open(root, opts)
	sh, _ := srv.Share(id)
	bob, _ := sh.MemberByID(2)
	if _, err := sh.SetSSHKey(bob, testKey); err != nil {
		t.Fatal(err)
	}
	srv.Close()
	data, _ := os.ReadFile(ak)
	absRoot, _ := filepath.Abs(root)
	if !strings.Contains(string(data), `command="/opt/shfd/bin/shfd rsync-gate --data-root `+absRoot+` --share `+id+` --member 2",`) {
		t.Fatalf("authorized_keys:\n%s", data)
	}

	// keys sync after the block was lost.
	os.WriteFile(ak, []byte("# own line\n"), 0o600)
	if code, out, errOut := run(t, env, "keys", "sync"); code != 0 || !strings.Contains(out, "1 member key(s)") {
		t.Fatalf("keys sync: %d %s %s", code, out, errOut)
	}

	// The gate refuses a shell.
	var o, e bytes.Buffer
	genv := map[string]string{EnvDataRoot: root, "SSH_ORIGINAL_COMMAND": "id"}
	code := Run([]string{"rsync-gate", "--share", id, "--member", "2"}, Env{Stdout: &o, Stderr: &e, Stdin: strings.NewReader(""), Getenv: func(k string) string { return genv[k] }})
	if code == 0 || o.Len() != 0 || !strings.Contains(e.String(), "denied (not_rsync)") {
		t.Errorf("gate: %d %q %q", code, o.String(), e.String())
	}
	if code, _, _ := run(t, genv, "rsync-gate", "--share", id); code != 2 {
		t.Errorf("gate without --member: %d", code)
	}

	// Revoke removes the line.
	if code, _, errOut := run(t, env, "member", "revoke", id, "bob"); code != 0 {
		t.Fatal(errOut)
	}
	es, _ := (&authkeys.File{Path: ak}).Entries()
	data, _ = os.ReadFile(ak)
	if len(es) != 0 || !strings.HasPrefix(string(data), "# own line\n") {
		t.Errorf("after revoke:\n%s", data)
	}
	// Bringing bob back does not bring his key back, not even through
	// keys sync, until he registers one again.
	if code, out, errOut := run(t, env, "member", "add", id, "bob"); code != 0 || !strings.Contains(out, "(reactivated)") {
		t.Fatalf("reactivate: %d %q %q", code, out, errOut)
	}
	run(t, env, "keys", "sync")
	if es, _ := (&authkeys.File{Path: ak}).Entries(); len(es) != 0 {
		t.Errorf("after reactivation: %+v", es)
	}
	if code, _, _ := run(t, map[string]string{EnvDataRoot: root}, "keys", "sync"); code != 2 {
		t.Errorf("keys sync without --authorized-keys: %d", code)
	}
}
