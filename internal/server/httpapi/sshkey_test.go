package httpapi_test

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/binary"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/digital-cube/shared-folder/internal/authkeys"
	"github.com/digital-cube/shared-folder/internal/server"
	"github.com/digital-cube/shared-folder/internal/server/httpapi"
)

func sshKey(t *testing.T, comment string) string {
	t.Helper()
	pub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	var blob []byte
	for _, part := range [][]byte{[]byte("ssh-ed25519"), pub} {
		blob = binary.BigEndian.AppendUint32(blob, uint32(len(part)))
		blob = append(blob, part...)
	}
	return "ssh-ed25519 " + base64.StdEncoding.EncodeToString(blob) + " " + comment
}

func TestSSHKey(t *testing.T) {
	akPath := filepath.Join(t.TempDir(), "authorized_keys")
	os.WriteFile(akPath, []byte("# kept\n"), 0o600)
	e := newEnv(t, func(o *server.Options) {
		o.AuthorizedKeys = &authkeys.File{Path: akPath, Command: []string{"/usr/bin/shfd", "rsync-gate", "--data-root", "/srv"}}
		o.SSH = server.SSHEndpoint{User: "shfd", Host: "files.example.org", Port: 2222}
	})
	bob := e.member("bob")
	key := sshKey(t, "bob@laptop")

	var res httpapi.SSHKeyResponse
	if err := bob.do("PUT", "/ssh-key", httpapi.SSHKeyRequest{PublicKey: key}, &res); err != nil {
		t.Fatal(err)
	}
	norm, _ := authkeys.ParsePublicKey(key)
	eq(t, "stored key", res.PublicKey, norm)
	eq(t, "fingerprint", res.Fingerprint, authkeys.Fingerprint(norm))
	eq(t, "advertised ssh endpoint", res.SSH, server.SSHEndpoint{User: "shfd", Host: "files.example.org", Port: 2222})

	sh, _ := e.srv.Share(e.share)
	ms, _ := sh.Members()
	var bobID int64
	for _, m := range ms {
		if m.Username == "bob" {
			bobID = m.ID
			eq(t, "members.ssh_pubkey", m.SSHPubkey, norm)
		}
	}
	data, _ := os.ReadFile(akPath)
	if !strings.HasPrefix(string(data), "# kept\n") || !strings.Contains(string(data), "--member 2\"") || !strings.Contains(string(data), norm+" shfd:bob@"+e.share) {
		t.Fatalf("authorized_keys:\n%s", data)
	}
	if bobID != 2 {
		t.Errorf("bob id %d", bobID)
	}

	// The same key for ana is refused; a broken key too.
	wantErr(t, e.ana.do("PUT", "/ssh-key", httpapi.SSHKeyRequest{PublicKey: key}, nil), http.StatusConflict, server.CodeKeyInUse)
	wantErr(t, e.ana.do("PUT", "/ssh-key", httpapi.SSHKeyRequest{PublicKey: "ssh-ed25519 AAAA"}, nil), http.StatusBadRequest, server.CodeInvalidKey)
	wantErr(t, e.ana.do("PUT", "/ssh-key", httpapi.SSHKeyRequest{PublicKey: `command="sh" ` + sshKey(t, "")}, nil), http.StatusBadRequest, server.CodeInvalidKey)
	// Without a token.
	anon := &client{e: e, share: e.share}
	wantErr(t, anon.do("PUT", "/ssh-key", httpapi.SSHKeyRequest{PublicKey: sshKey(t, "")}, nil), http.StatusUnauthorized, server.CodeUnauthorized)

	// Revoking bob removes his line and keeps the rest.
	if err := sh.RevokeMember("bob"); err != nil {
		t.Fatal(err)
	}
	data, _ = os.ReadFile(akPath)
	if strings.Contains(string(data), "shfd:bob@") || !strings.HasPrefix(string(data), "# kept\n") {
		t.Fatalf("after revoke:\n%s", data)
	}
	// A revoked token cannot register a key.
	wantErr(t, bob.do("PUT", "/ssh-key", httpapi.SSHKeyRequest{PublicKey: sshKey(t, "")}, nil), http.StatusUnauthorized, server.CodeUnauthorized)

	// keys sync puts back exactly the active members with keys.
	if err := e.ana.do("PUT", "/ssh-key", httpapi.SSHKeyRequest{PublicKey: sshKey(t, "ana")}, nil); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(akPath, []byte("# kept\n"), 0o600)
	if skipped, err := e.srv.SyncAuthorizedKeys(); err != nil || len(skipped) != 0 {
		t.Fatal(skipped, err)
	}
	es, _ := (&authkeys.File{Path: akPath}).Entries()
	if len(es) != 1 || es[0].Username != "ana" {
		t.Errorf("after sync: %+v", es)
	}
}

func TestSSHKeyNotConfigured(t *testing.T) {
	e := newEnv(t)
	wantErr(t, e.ana.do("PUT", "/ssh-key", httpapi.SSHKeyRequest{PublicKey: sshKey(t, "")}, nil), http.StatusNotImplemented, server.CodeSSHNotConfigured)
}

// TestSSHKeyReplaced: a member has one key. Registering a different one
// (another device joined as the same member) answers with the replaced
// key and leaves a key-replaced line in service.log; the first key and
// the same key again do neither.
func TestSSHKeyReplaced(t *testing.T) {
	akPath := filepath.Join(t.TempDir(), "authorized_keys")
	e := newEnv(t, func(o *server.Options) {
		o.AuthorizedKeys = &authkeys.File{Path: akPath, Command: []string{"/usr/bin/shfd", "rsync-gate", "--data-root", "/srv"}}
	})
	marko := e.member("marko")
	mac, linux := sshKey(t, "shf@mac"), sshKey(t, "shf@linux")
	put := func(key string) httpapi.SSHKeyResponse {
		t.Helper()
		var res httpapi.SSHKeyResponse
		if err := marko.do("PUT", "/ssh-key", httpapi.SSHKeyRequest{PublicKey: key}, &res); err != nil {
			t.Fatal(err)
		}
		return res
	}
	replacedLines := func() []string {
		var out []string
		for _, l := range e.logLines() {
			if strings.Contains(l, " key-replaced ") {
				out = append(out, l)
			}
		}
		return out
	}

	if res := put(mac); res.Replaced != nil {
		t.Errorf("first registration: replaced %+v", res.Replaced)
	}
	if res := put(mac); res.Replaced != nil {
		t.Errorf("same key again: replaced %+v", res.Replaced)
	}
	if l := replacedLines(); len(l) != 0 {
		t.Errorf("service.log before a replacement: %q", l)
	}

	macFP, _ := authkeys.ParsePublicKey(mac)
	macFP = authkeys.Fingerprint(macFP)
	linuxKey, _ := authkeys.ParsePublicKey(linux)
	res := put(linux)
	if res.Replaced == nil || res.Replaced.Fingerprint != macFP {
		t.Fatalf("other key: replaced %+v, want %s", res.Replaced, macFP)
	}
	eq(t, "fingerprint", res.Fingerprint, authkeys.Fingerprint(linuxKey))
	l := replacedLines()
	if len(l) != 1 || !strings.HasSuffix(l[0], " key-replaced member=marko old="+macFP+" new="+res.Fingerprint) {
		t.Errorf("service.log: %q", l)
	}

	// The answer of the first registration carries no "replaced" at all.
	var raw map[string]any
	other := e.member("ivan")
	if err := other.do("PUT", "/ssh-key", httpapi.SSHKeyRequest{PublicKey: sshKey(t, "")}, &raw); err != nil {
		t.Fatal(err)
	}
	if _, ok := raw["replaced"]; ok {
		t.Errorf("first registration: %v", raw)
	}
}
