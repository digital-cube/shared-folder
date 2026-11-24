package server

import (
	"strings"
	"testing"
)

func TestSetRole(t *testing.T) {
	f := newFixture(t) // ana is the folder's only admin
	sh, _ := f.share()
	for _, u := range []string{"bob", "cid"} {
		if _, err := sh.AddMember(u, false); err != nil {
			t.Fatal(err)
		}
	}
	role := func(u string) string {
		t.Helper()
		m, err := sh.MemberByName(u)
		if err != nil {
			t.Fatal(err)
		}
		return m.Role
	}

	// Up.
	if old, err := sh.SetRole("bob", RoleAdmin); err != nil || old != RoleMember {
		t.Fatalf("bob -> admin: %q %v", old, err)
	}
	if role("bob") != RoleAdmin {
		t.Error("bob is not an admin")
	}
	// The same role again changes nothing.
	if old, err := sh.SetRole("bob", RoleAdmin); err != nil || old != RoleAdmin {
		t.Fatalf("bob -> admin again: %q %v", old, err)
	}
	// Down, while another admin remains.
	if old, err := sh.SetRole("ana", RoleMember); err != nil || old != RoleAdmin {
		t.Fatalf("ana -> member: %q %v", old, err)
	}
	// bob is the last active admin now; a revoked admin does not count.
	if _, err := sh.AddMember("dan", true); err != nil {
		t.Fatal(err)
	}
	if err := sh.RevokeMember("dan"); err != nil {
		t.Fatal(err)
	}
	if _, err := sh.SetRole("bob", RoleMember); CodeOf(err) != CodeLastAdmin {
		t.Fatalf("last admin -> member: %v", err)
	}
	if role("bob") != RoleAdmin {
		t.Error("the last admin lost the role")
	}
	if m, err := sh.DefaultAdmin(); err != nil || m.Username != "bob" {
		t.Errorf("default admin: %v %v", m, err)
	}
	// With a second admin it works.
	if _, err := sh.SetRole("cid", RoleAdmin); err != nil {
		t.Fatal(err)
	}
	if _, err := sh.SetRole("bob", RoleMember); err != nil {
		t.Fatalf("bob -> member with cid admin: %v", err)
	}

	// Revoked, unknown, bad role.
	if _, err := sh.SetRole("dan", RoleAdmin); CodeOf(err) != CodeUnknownMember {
		t.Errorf("revoked: %v", err)
	}
	if _, err := sh.SetRole("niko", RoleAdmin); CodeOf(err) != CodeUnknownMember {
		t.Errorf("unknown: %v", err)
	}
	if _, err := sh.SetRole("bob", "owner"); CodeOf(err) != CodeInvalidRequest {
		t.Errorf("bad role: %v", err)
	}
}

func TestResetToken(t *testing.T) {
	f := newFixture(t)
	sh, _ := f.share()
	old, err := sh.AddMember("bob", false)
	if err != nil {
		t.Fatal(err)
	}
	bob, _ := sh.Authenticate(old)
	sh.db.Exec(`UPDATE members SET ssh_pubkey = 'ssh-ed25519 AAAA' WHERE id = ?`, bob.ID)
	w := sh.Watch(bob.ID)
	defer w.Close()
	wa := sh.Watch(1)
	defer wa.Close()

	tok, err := sh.ResetToken("bob")
	if err != nil {
		t.Fatal(err)
	}
	if tok == old || !strings.HasPrefix(tok, "shf_") {
		t.Fatalf("new token %q", tok)
	}
	if _, err := sh.Authenticate(old); CodeOf(err) != CodeUnauthorized {
		t.Errorf("old token: %v", err)
	}
	m, err := sh.Authenticate(tok)
	if err != nil || m.ID != bob.ID {
		t.Fatalf("new token: %v %v", m, err)
	}
	if m.SSHPubkey != "ssh-ed25519 AAAA" {
		t.Errorf("ssh key after reset: %q", m.SSHPubkey)
	}
	select {
	case <-w.Revoked:
	default:
		t.Error("bob's watcher was not closed")
	}
	select {
	case <-wa.Revoked:
		t.Error("ana's watcher was closed")
	default:
	}

	if err := sh.RevokeMember("bob"); err != nil {
		t.Fatal(err)
	}
	_, err = sh.ResetToken("bob")
	if CodeOf(err) != CodeUnknownMember || !strings.Contains(err.Error(), "member add") {
		t.Errorf("revoked: %v", err)
	}
	if _, err := sh.ResetToken("niko"); CodeOf(err) != CodeUnknownMember {
		t.Errorf("unknown: %v", err)
	}
}

func TestReactivateMember(t *testing.T) {
	f := newFixture(t)
	sh, _ := f.share()
	old, err := sh.AddMember("marko", false)
	if err != nil {
		t.Fatal(err)
	}
	marko, _ := sh.Authenticate(old)
	sh.db.Exec(`UPDATE members SET ssh_pubkey = 'ssh-ed25519 AAAA', last_seq_acked = 7 WHERE id = ?`, marko.ID)

	// An active name stays taken.
	if _, _, err := sh.AddOrReactivateMember("marko", true); CodeOf(err) != CodeMemberExists {
		t.Fatalf("add over an active member: %v", err)
	}
	if err := sh.RevokeMember("marko"); err != nil {
		t.Fatal(err)
	}

	tok, reactivated, err := sh.AddOrReactivateMember("marko", true)
	if err != nil || !reactivated {
		t.Fatalf("reactivate: %v %v", reactivated, err)
	}
	if _, err := sh.Authenticate(old); CodeOf(err) != CodeUnauthorized {
		t.Errorf("old token: %v", err)
	}
	m, err := sh.Authenticate(tok)
	if err != nil {
		t.Fatalf("new token: %v", err)
	}
	if m.ID != marko.ID || m.Role != RoleAdmin || m.RevokedAt != nil || m.SSHPubkey != "" || m.LastSeqAcked != 0 || !m.CreatedAt.Equal(marko.CreatedAt) {
		t.Errorf("reactivated member: %+v (was %+v)", m, marko)
	}
	ms, _ := sh.Members()
	if len(ms) != 2 {
		t.Errorf("members: %+v", ms)
	}

	// A new name is not a reactivation.
	if _, reactivated, err := sh.AddOrReactivateMember("nova", false); err != nil || reactivated {
		t.Errorf("new member: %v %v", reactivated, err)
	}
}
