package httpapi_test

import (
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/digital-cube/shared-folder/internal/authkeys"
	"github.com/digital-cube/shared-folder/internal/server"
	"github.com/digital-cube/shared-folder/internal/server/httpapi"
)

func withKeys(t *testing.T) (string, func(*server.Options)) {
	akPath := filepath.Join(t.TempDir(), "authorized_keys")
	os.WriteFile(akPath, []byte("# kept\n"), 0o600)
	return akPath, func(o *server.Options) {
		o.AuthorizedKeys = &authkeys.File{Path: akPath, Command: []string{"/usr/bin/shfd", "rsync-gate", "--data-root", "/srv"}}
	}
}

func keyEntries(t *testing.T, akPath string) []authkeys.Entry {
	t.Helper()
	es, err := (&authkeys.File{Path: akPath}).Entries()
	if err != nil {
		t.Fatal(err)
	}
	return es
}

func TestResetTokenAPI(t *testing.T) {
	akPath, keys := withKeys(t)
	e := newEnv(t, keys)
	hs, _ := e.wsServer(httpapi.WithMemberCheck(50 * time.Millisecond))
	bob := e.member("bob")
	if err := bob.do("PUT", "/ssh-key", httpapi.SSHKeyRequest{PublicKey: sshKey(t, "bob")}, nil); err != nil {
		t.Fatal(err)
	}
	bob.mustPut("b.txt", "", []byte("b"))
	wb := mustDial(t, hs.URL, e.share, bob.token)
	wb.mustHead(1)

	sh, _ := e.srv.Share(e.share)
	tok, err := sh.ResetToken("bob")
	if err != nil {
		t.Fatal(err)
	}
	// The old token: 401 at once, and the WebSocket is closed.
	_, aerr := bob.changes(0, 10)
	wantErr(t, aerr, http.StatusUnauthorized, server.CodeUnauthorized)
	if st := closeStatus(t, wb, 2*time.Second); st != httpapi.CloseRevoked {
		t.Fatalf("close status %v, want %v", st, httpapi.CloseRevoked)
	}
	// The new one works; the SSH key stays.
	bob.token = tok
	if _, aerr := bob.changes(0, 10); aerr != nil {
		t.Fatal(aerr)
	}
	bob.mustPut("c.txt", "", []byte("c"))
	if es := keyEntries(t, akPath); len(es) != 1 || es[0].Username != "bob" {
		t.Errorf("authorized_keys after reset: %+v", es)
	}

	// A reset by another process (shfd member reset-token) is seen by the
	// periodic check.
	wb = mustDial(t, hs.URL, e.share, bob.token)
	wb.mustHead(2)
	cli, err := server.Open(e.root, server.Options{SkipRecovery: true})
	if err != nil {
		t.Fatal(err)
	}
	defer cli.Close()
	csh, err := cli.Share(e.share)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := csh.ResetToken("bob"); err != nil {
		t.Fatal(err)
	}
	if st := closeStatus(t, wb, 2*time.Second); st != httpapi.CloseRevoked {
		t.Fatalf("close status after a reset in another process %v, want %v", st, httpapi.CloseRevoked)
	}
	_, aerr = bob.changes(0, 10)
	wantErr(t, aerr, http.StatusUnauthorized, server.CodeUnauthorized)
}

func TestReactivatedMemberAPI(t *testing.T) {
	akPath, keys := withKeys(t)
	e := newEnv(t, keys)
	marko := e.member("marko")
	key := sshKey(t, "marko")
	if err := marko.do("PUT", "/ssh-key", httpapi.SSHKeyRequest{PublicKey: key}, nil); err != nil {
		t.Fatal(err)
	}
	marko.mustPut("m.txt", "", []byte("m"))
	sh, _ := e.srv.Share(e.share)
	before, _ := sh.MemberByName("marko")
	if err := sh.RevokeMember("marko"); err != nil {
		t.Fatal(err)
	}
	// A line left behind by a failed removal must not survive either.
	norm, _ := authkeys.ParsePublicKey(key)
	if err := (&authkeys.File{Path: akPath, Command: []string{"/usr/bin/shfd"}}).Set(authkeys.Entry{ShareID: e.share, MemberID: before.ID, Username: "marko", Key: norm}); err != nil {
		t.Fatal(err)
	}

	tok, reactivated, err := sh.AddOrReactivateMember("marko", true)
	if err != nil || !reactivated {
		t.Fatalf("reactivate: %v %v", reactivated, err)
	}
	after, _ := sh.MemberByName("marko")
	eq(t, "id", after.ID, before.ID)
	eq(t, "role", after.Role, server.RoleAdmin)
	eq(t, "ssh_pubkey", after.SSHPubkey, "")
	if es := keyEntries(t, akPath); len(es) != 0 {
		t.Errorf("authorized_keys after reactivation: %+v", es)
	}

	// The old token stays dead, the new one works, and the history is
	// still the member's.
	_, aerr := marko.changes(0, 10)
	wantErr(t, aerr, http.StatusUnauthorized, server.CodeUnauthorized)
	old := marko.token
	marko.token = tok
	ch, aerr := marko.changes(0, 10)
	if aerr != nil {
		t.Fatal(aerr)
	}
	if len(ch.Events) != 1 || ch.Events[0].Member != "marko" {
		t.Errorf("history: %+v", ch.Events)
	}
	// The same key is registered again.
	if err := marko.do("PUT", "/ssh-key", httpapi.SSHKeyRequest{PublicKey: key}, nil); err != nil {
		t.Fatal(err)
	}
	if es := keyEntries(t, akPath); len(es) != 1 || es[0].MemberID != before.ID || es[0].Key != norm {
		t.Errorf("authorized_keys after PUT ssh-key: %+v", es)
	}
	// An admin again: the admin routes answer.
	if _, aerr := marko.trashList(nil); aerr != nil {
		t.Errorf("trash list as reactivated admin: %v", aerr)
	}

	// The name of an active member stays taken.
	if _, _, err := sh.AddOrReactivateMember("marko", false); server.CodeOf(err) != server.CodeMemberExists {
		t.Errorf("add over active: %v", err)
	}
	if tok == old {
		t.Error("token reused")
	}
}
