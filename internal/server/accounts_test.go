package server

import (
	"context"
	"database/sql"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/digital-cube/shared-folder/internal/password"
)

// cheapPW keeps the account tests fast.
var cheapPW = password.Params{Memory: 64, Time: 1, Threads: 1, SaltLen: 16, KeyLen: 32}

type testClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *testClock) Now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *testClock) Advance(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

// acctFixture is a server with a moving clock, cheap password hashes and
// one folder "tim" whose admin is the member "ana" (with a token).
type acctFixture struct {
	t     *testing.T
	srv   *Server
	clock *testClock
	sh    *Share
}

func newAcctFixture(t *testing.T) *acctFixture {
	t.Helper()
	f := &acctFixture{t: t, clock: &testClock{t: t0}}
	p := cheapPW
	srv, err := Open(t.TempDir(), Options{Now: f.clock.Now, PasswordParams: &p})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { srv.Close() })
	f.srv = srv
	id, _, err := srv.CreateShare("tim", "ana")
	if err != nil {
		t.Fatal(err)
	}
	if f.sh, err = srv.Share(id); err != nil {
		t.Fatal(err)
	}
	return f
}

// user makes an account and gives it the password pw (must_change off).
func (f *acctFixture) user(name, pw string) *User {
	f.t.Helper()
	u, _, err := f.srv.AddUser(name, strings.ToUpper(name[:1])+name[1:])
	if err != nil {
		f.t.Fatal(err)
	}
	h, _ := password.HashWith(pw, cheapPW)
	if _, err := f.srv.reg.Exec(`UPDATE users SET pw_hash = ?, must_change = 0 WHERE id = ?`, h, u.ID); err != nil {
		f.t.Fatal(err)
	}
	u.MustChange = false
	return u
}

func (f *acctFixture) login(user, pw, kind, ip string) (*LoginResult, error) {
	return f.srv.Login(context.Background(), LoginRequest{Username: user, Password: pw, DeviceName: "test", Kind: kind, IP: ip})
}

func (f *acctFixture) outcomes() []string {
	f.t.Helper()
	rows, err := f.srv.reg.Query(`SELECT username || ' ' || ip || ' ' || outcome FROM auth_log ORDER BY rowid`)
	if err != nil {
		f.t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var s string
		rows.Scan(&s)
		out = append(out, s)
	}
	return out
}

const pwBob = "bobova-lozinka-1"

func TestAddUserAndTemporaryPassword(t *testing.T) {
	f := newAcctFixture(t)
	u, temp, err := f.srv.AddUser("bob", "  Bob Bobić ")
	if err != nil {
		t.Fatal(err)
	}
	if !u.MustChange || u.DisplayName != "Bob Bobić" || !u.CreatedAt.Equal(t0) {
		t.Errorf("user = %+v", u)
	}
	var stored string
	f.srv.reg.QueryRow(`SELECT pw_hash FROM users WHERE id = ?`, u.ID).Scan(&stored)
	if strings.Contains(stored, temp) || !strings.HasPrefix(stored, "$argon2id$") {
		t.Errorf("pw_hash = %q", stored)
	}
	if _, _, err := f.srv.AddUser("bob", ""); CodeOf(err) != CodeUserExists {
		t.Errorf("duplicate: %v", err)
	}
	if _, _, err := f.srv.AddUser("Bob!", ""); CodeOf(err) != CodeInvalidRequest {
		t.Errorf("bad username: %v", err)
	}
	res, err := f.login("bob", temp, "", "10.0.0.1")
	if err != nil || !res.MustChange || !strings.HasPrefix(res.Token, DeviceTokenPrefix) || len(res.Token) != 4+43 {
		t.Fatalf("login with the temporary password: %+v %v", res, err)
	}
	// The token is stored as its hash only.
	var n int
	f.srv.reg.QueryRow(`SELECT count(*) FROM devices WHERE token_hash = ?`, HashToken(res.Token)).Scan(&n)
	if n != 1 {
		t.Error("device token not stored as its sha256")
	}
	sess, err := f.srv.AuthenticateDevice(res.Token)
	if err != nil {
		t.Fatal(err)
	}
	// A new password: rules first, then the old one.
	ctx := context.Background()
	if err := f.srv.ChangePassword(ctx, sess, temp, "kratka", "10.0.0.1"); CodeOf(err) != CodeInvalidPassword {
		t.Errorf("short: %v", err)
	}
	if err := f.srv.ChangePassword(ctx, sess, temp, temp, "10.0.0.1"); CodeOf(err) != CodeInvalidPassword {
		t.Errorf("same as old: %v", err)
	}
	if err := f.srv.ChangePassword(ctx, sess, "pogrešna-stara", pwBob, "10.0.0.1"); CodeOf(err) != CodeWrongPassword {
		t.Errorf("wrong old: %v", err)
	}
	if err := f.srv.ChangePassword(ctx, sess, temp, pwBob, "10.0.0.1"); err != nil {
		t.Fatal(err)
	}
	if res, err := f.login("bob", pwBob, "", "10.0.0.1"); err != nil || res.MustChange {
		t.Fatalf("login with the new password: %+v %v", res, err)
	}
	if _, err := f.login("bob", temp, "", "10.0.0.1"); CodeOf(err) != CodeInvalidCredentials {
		t.Errorf("old temporary password: %v", err)
	}
	// passwd: a new temporary password, must_change again.
	temp2, err := f.srv.ResetPassword("bob")
	if err != nil {
		t.Fatal(err)
	}
	if res, err := f.login("bob", temp2, "", "10.0.0.1"); err != nil || !res.MustChange {
		t.Fatalf("after passwd: %+v %v", res, err)
	}
	if _, err := f.srv.ResetPassword("niko"); CodeOf(err) != CodeUnknownUser {
		t.Errorf("passwd of unknown: %v", err)
	}
}

func TestLoginSameAnswer(t *testing.T) {
	f := newAcctFixture(t)
	f.user("bob", pwBob)
	f.user("cid", "cidova-lozinka-1")
	if _, err := f.srv.DisableUser("cid"); err != nil {
		t.Fatal(err)
	}
	var msgs []string
	for _, c := range [][2]string{
		{"bob", "pogrešna-lozinka"},        // wrong password
		{"niko", pwBob},                    // unknown account
		{"cid", "cidova-lozinka-1"},        // disabled, right password
		{"Ne Valja!", pwBob},               // not even a username
		{"bob", strings.Repeat("x", 5000)}, // too long to hash
	} {
		_, err := f.login(c[0], c[1], "client", "10.0.0.9")
		if CodeOf(err) != CodeInvalidCredentials {
			t.Fatalf("%v: %v", c, err)
		}
		msgs = append(msgs, err.Error())
	}
	for _, m := range msgs[1:] {
		if m != msgs[0] {
			t.Errorf("answers differ: %q vs %q", m, msgs[0])
		}
	}
	if f.srv.dummy == "" {
		t.Error("an unknown account was not checked against the dummy hash")
	}
	want := []string{"bob 10.0.0.9 bad_password", "niko 10.0.0.9 unknown_user", "cid 10.0.0.9 disabled",
		"ne valja! 10.0.0.9 unknown_user", "bob 10.0.0.9 bad_password"}
	if got := f.outcomes(); strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("auth_log = %q", got)
	}
	// Five failures from 10.0.0.9: the next attempt waits a second.
	f.clock.Advance(time.Second)
	if _, err := f.login("BOB ", pwBob, "client", "10.0.0.9"); err != nil {
		t.Errorf("username in another case: %v", err)
	}
	if got := f.outcomes(); got[len(got)-1] != "bob 10.0.0.9 ok" {
		t.Errorf("last auth_log = %q", got[len(got)-1])
	}
	if _, err := f.login("bob", pwBob, "phone", "10.0.0.9"); CodeOf(err) != CodeInvalidRequest {
		t.Errorf("bad kind: %v", err)
	}
	u, _ := f.srv.UserByName("bob")
	if u.LastLoginAt == nil || !u.LastLoginAt.Equal(t0.Add(time.Second)) {
		t.Errorf("last_login_at = %v", u.LastLoginAt)
	}
}

func TestAttemptLimit(t *testing.T) {
	f := newAcctFixture(t)
	f.user("bob", pwBob)
	wrong := func(user, ip string) error { _, err := f.login(user, "pogrešna-lozinka", "client", ip); return err }
	for i := 0; i < MaxFailures; i++ {
		if err := wrong("bob", "10.0.0.1"); CodeOf(err) != CodeInvalidCredentials {
			t.Fatalf("attempt %d: %v", i+1, err)
		}
		f.clock.Advance(time.Millisecond)
	}
	// Five failures: the next attempt waits 1 s from the last one, even
	// with the right password and from another address.
	_, err := f.login("bob", pwBob, "client", "10.0.0.2")
	var e *Error
	if CodeOf(err) != CodeTooManyAttempts || !asError(err, &e) || e.RetryAfter <= 0 || e.RetryAfter > time.Second {
		t.Fatalf("locked: %v", err)
	}
	f.clock.Advance(time.Second)
	if err := wrong("bob", "10.0.0.2"); CodeOf(err) != CodeInvalidCredentials {
		t.Fatalf("after 1 s: %v", err)
	}
	// Sixth failure: 2 s.
	f.clock.Advance(1500 * time.Millisecond)
	if err := wrong("bob", "10.0.0.2"); CodeOf(err) != CodeTooManyAttempts {
		t.Fatalf("1.5 s after the sixth: %v", err)
	}
	f.clock.Advance(500 * time.Millisecond)
	if _, err := f.login("bob", pwBob, "client", "10.0.0.2"); err != nil {
		t.Fatalf("after 2 s, right password: %v", err)
	}
	// The success reset bob's count.
	if err := wrong("bob", "10.0.0.3"); CodeOf(err) != CodeInvalidCredentials {
		t.Fatalf("after the reset: %v", err)
	}

	// The address is counted on its own: five unknown names from one IP
	// lock that IP for everyone, but not the names from elsewhere.
	for i := 0; i < MaxFailures; i++ {
		wrong("niko"+string(rune('a'+i)), "10.6.6.6")
	}
	if err := wrong("nikoz", "10.6.6.6"); CodeOf(err) != CodeTooManyAttempts {
		t.Fatalf("locked address: %v", err)
	}
	if _, err := f.login("bob", pwBob, "client", "10.6.6.6"); CodeOf(err) != CodeTooManyAttempts {
		t.Fatalf("bob from the locked address: %v", err)
	}
	if _, err := f.login("bob", pwBob, "client", "10.0.0.4"); err != nil {
		t.Fatalf("bob from elsewhere: %v", err)
	}
	if got := f.outcomes(); !contains(got, "nikoz 10.6.6.6 locked") {
		t.Errorf("locked attempt not logged: %q", got)
	}
}

func TestLockoutGrowsToFifteenMinutes(t *testing.T) {
	for n, want := range map[int]time.Duration{5: time.Second, 6: 2 * time.Second, 10: 32 * time.Second,
		14: 512 * time.Second, 15: MaxLockout, 40: MaxLockout, 1000: MaxLockout} {
		if got := lockout(n); got != want {
			t.Errorf("lockout(%d) = %v, want %v", n, got, want)
		}
	}
	// And through Login: many failures, each after its wait.
	f := newAcctFixture(t)
	f.user("bob", pwBob)
	for i := 0; i < 20; i++ {
		f.login("bob", "pogrešna-lozinka", "client", "10.0.0.1")
		f.clock.Advance(lockout(max(i+1, MaxFailures)))
	}
	f.clock.Advance(-time.Second)
	_, err := f.login("bob", pwBob, "client", "10.0.0.1")
	var e *Error
	if !asError(err, &e) || e.Code != CodeTooManyAttempts || e.RetryAfter != time.Second {
		t.Fatalf("1 s before the end of a 15 min wait: %v", err)
	}
	f.clock.Advance(time.Second)
	if _, err := f.login("bob", pwBob, "client", "10.0.0.1"); err != nil {
		t.Fatalf("after 15 min: %v", err)
	}
	// Failures older than a day are forgotten.
	for i := 0; i < MaxFailures; i++ {
		f.login("bob", "pogrešna-lozinka", "client", "10.0.0.7")
	}
	f.clock.Advance(failureWindow + time.Second)
	if _, err := f.login("bob", pwBob, "client", "10.0.0.7"); err != nil {
		t.Fatalf("a day later: %v", err)
	}
}

func TestGUISessionExpiry(t *testing.T) {
	f := newAcctFixture(t)
	f.user("bob", pwBob)
	gui, err := f.login("bob", pwBob, DeviceGUI, "10.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	cli, err := f.login("bob", pwBob, DeviceClient, "10.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	// 12 h without use ends it.
	f.clock.Advance(GUIIdle - time.Second)
	if _, err := f.srv.AuthenticateDevice(gui.Token); err != nil {
		t.Fatalf("just under 12 h: %v", err)
	}
	f.clock.Advance(GUIIdle - time.Second) // the use above slid the expiry
	if _, err := f.srv.AuthenticateDevice(gui.Token); err != nil {
		t.Fatalf("used again within 12 h: %v", err)
	}
	f.clock.Advance(GUIIdle)
	if _, err := f.srv.AuthenticateDevice(gui.Token); CodeOf(err) != CodeUnauthorized {
		t.Fatalf("12 h idle: %v", err)
	}
	// Used every 11 h, it still ends 7 days after the sign-in.
	gui, _ = f.login("bob", pwBob, DeviceGUI, "10.0.0.1")
	start := f.clock.Now()
	for f.clock.Now().Add(11 * time.Hour).Before(start.Add(GUIMaxAge)) {
		f.clock.Advance(11 * time.Hour)
		if _, err := f.srv.AuthenticateDevice(gui.Token); err != nil {
			t.Fatalf("at %v: %v", f.clock.Now().Sub(start), err)
		}
	}
	f.clock.Advance(start.Add(GUIMaxAge).Sub(f.clock.Now()))
	if _, err := f.srv.AuthenticateDevice(gui.Token); CodeOf(err) != CodeUnauthorized {
		t.Fatalf("after 7 days: %v", err)
	}
	// A client device does not expire.
	if _, err := f.srv.AuthenticateDevice(cli.Token); err != nil {
		t.Fatalf("client after %v: %v", f.clock.Now().Sub(t0), err)
	}
	u, _ := f.srv.UserByName("bob")
	devs, _ := f.srv.Devices(u.ID, false)
	if len(devs) != 1 || devs[0].Kind != DeviceClient {
		t.Errorf("active devices = %+v", devs)
	}
}

func TestLogoutDisableAndRevoke(t *testing.T) {
	f := newAcctFixture(t)
	f.user("bob", pwBob)
	a, _ := f.login("bob", pwBob, DeviceClient, "10.0.0.1")
	b, _ := f.login("bob", pwBob, DeviceGUI, "10.0.0.1")
	c, _ := f.login("bob", pwBob, DeviceClient, "10.0.0.1")
	if err := f.srv.Logout(a.Token); err != nil {
		t.Fatal(err)
	}
	if _, err := f.srv.AuthenticateDevice(a.Token); CodeOf(err) != CodeUnauthorized {
		t.Errorf("after logout: %v", err)
	}
	if _, err := f.srv.AuthenticateDevice(b.Token); err != nil {
		t.Errorf("another device after logout: %v", err)
	}
	sb, _ := f.srv.AuthenticateDevice(b.Token)
	if err := f.srv.RevokeDevice(sb.User.ID+1, sb.DeviceID); CodeOf(err) != CodeUnknownDevice {
		t.Errorf("revoke a device of another account: %v", err)
	}
	if n, err := f.srv.DisableUser("bob"); err != nil || n != 2 {
		t.Fatalf("disable = %d %v", n, err)
	}
	for _, tok := range []string{b.Token, c.Token} {
		if _, err := f.srv.AuthenticateDevice(tok); CodeOf(err) != CodeUnauthorized {
			t.Errorf("after disable: %v", err)
		}
	}
	if _, err := f.login("bob", pwBob, DeviceClient, "10.0.0.1"); CodeOf(err) != CodeInvalidCredentials {
		t.Errorf("sign-in of a disabled account: %v", err)
	}
	if err := f.srv.EnableUser("bob"); err != nil {
		t.Fatal(err)
	}
	if _, err := f.srv.AuthenticateDevice(c.Token); CodeOf(err) != CodeUnauthorized {
		t.Errorf("enable brought a device back: %v", err)
	}
	if _, err := f.login("bob", pwBob, DeviceClient, "10.0.0.1"); err != nil {
		t.Errorf("sign-in after enable: %v", err)
	}
}

func TestRehashOnLogin(t *testing.T) {
	f := newAcctFixture(t)
	u := f.user("bob", pwBob)
	weak, _ := password.HashWith(pwBob, password.Params{Memory: 32, Time: 1, Threads: 1, SaltLen: 8, KeyLen: 16})
	f.srv.reg.Exec(`UPDATE users SET pw_hash = ? WHERE id = ?`, weak, u.ID)
	if _, err := f.login("bob", pwBob, DeviceClient, "10.0.0.1"); err != nil {
		t.Fatal(err)
	}
	var h string
	f.srv.reg.QueryRow(`SELECT pw_hash FROM users WHERE id = ?`, u.ID).Scan(&h)
	if p, err := password.ParamsOf(h); err != nil || p != cheapPW {
		t.Errorf("after the sign-in: %+v %v (%s)", p, err, h)
	}
	// A wrong password does not rehash.
	f.srv.reg.Exec(`UPDATE users SET pw_hash = ? WHERE id = ?`, weak, u.ID)
	f.login("bob", "pogrešna-lozinka", DeviceClient, "10.0.0.1")
	f.srv.reg.QueryRow(`SELECT pw_hash FROM users WHERE id = ?`, u.ID).Scan(&h)
	if h != weak {
		t.Error("rehashed after a wrong password")
	}
}

func TestPasswordChecksAtMostTwo(t *testing.T) {
	f := newAcctFixture(t)
	f.srv.pwSem <- struct{}{}
	f.srv.pwSem <- struct{}{}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if _, _, err := f.srv.checkPassword(ctx, "x", f.srv.dummyHash()); err != context.DeadlineExceeded {
		t.Fatalf("a third check ran: %v", err)
	}
	<-f.srv.pwSem
	if _, _, err := f.srv.checkPassword(context.Background(), "x", f.srv.dummyHash()); err != nil {
		t.Fatal(err)
	}
}

func TestAccountMembers(t *testing.T) {
	f := newAcctFixture(t)
	bob := f.user("bob", pwBob)
	cid := f.user("cid", "cidova-lozinka-1")
	if _, err := f.sh.AddMember("marko", false); err != nil {
		t.Fatal(err)
	}
	legacy, _ := f.sh.AddMember("dragana", false)

	// member add --user
	m, re, err := f.srv.AddUserMember(f.sh, "bob", true)
	if err != nil || re || m.Username != "bob" || m.Role != RoleAdmin || m.UserID == nil || *m.UserID != bob.ID {
		t.Fatalf("add bob = %+v %v %v", m, re, err)
	}
	if _, _, err := f.srv.AddUserMember(f.sh, "bob", false); CodeOf(err) != CodeMemberExists {
		t.Errorf("bob twice: %v", err)
	}
	if _, _, err := f.srv.AddUserMember(f.sh, "niko", false); CodeOf(err) != CodeUnknownUser {
		t.Errorf("unknown account: %v", err)
	}
	// An account whose name is taken by a token member: link instead.
	marko := f.user("marko", "markova-lozinka")
	if _, _, err := f.srv.AddUserMember(f.sh, "marko", false); CodeOf(err) != CodeMemberExists || !strings.Contains(err.Error(), "member link") {
		t.Errorf("marko by account: %v", err)
	}
	// member link: dragana's member gets cid's account; her token stays.
	if m, err := f.srv.LinkMember(f.sh, "dragana", "cid"); err != nil || *m.UserID != cid.ID {
		t.Fatalf("link = %+v %v", m, err)
	}
	if m, err := f.sh.Authenticate(legacy); err != nil || m.Username != "dragana" {
		t.Errorf("legacy token after link: %v %v", m, err)
	}
	if _, err := f.srv.LinkMember(f.sh, "dragana", "cid"); err != nil {
		t.Errorf("same link again: %v", err)
	}
	if _, err := f.srv.LinkMember(f.sh, "marko", "cid"); CodeOf(err) != CodeMemberExists {
		t.Errorf("cid linked twice: %v", err)
	}
	if _, err := f.srv.LinkMember(f.sh, "dragana", "marko"); CodeOf(err) != CodeMemberExists {
		t.Errorf("dragana relinked: %v", err)
	}
	if _, err := f.srv.LinkMember(f.sh, "niko", "marko"); CodeOf(err) != CodeUnknownMember {
		t.Errorf("unknown member: %v", err)
	}
	if _, err := f.srv.LinkMember(f.sh, "marko", "marko"); err != nil {
		t.Fatal(err)
	}

	// My folders, through user_shares.
	for _, c := range []struct {
		u            *User
		member, role string
	}{{bob, "bob", RoleAdmin}, {cid, "dragana", RoleMember}, {marko, "marko", RoleMember}} {
		fs, err := f.srv.UserFolders(c.u.ID)
		if err != nil || len(fs) != 1 || fs[0].ShareID != f.sh.ID || fs[0].Name != "tim" || fs[0].Member != c.member || fs[0].Role != c.role {
			t.Errorf("folders of %s = %+v %v", c.u.Username, fs, err)
		}
	}
	if ok, _ := f.srv.IsFolderAdmin(bob.ID); !ok {
		t.Error("bob is not a folder admin")
	}
	if ok, _ := f.srv.IsFolderAdmin(cid.ID); ok {
		t.Error("cid is a folder admin")
	}
	// A revoked member is not "my folder"; bringing it back keeps the link.
	if err := f.sh.RevokeMember("dragana"); err != nil {
		t.Fatal(err)
	}
	if fs, _ := f.srv.UserFolders(cid.ID); len(fs) != 0 {
		t.Errorf("folders after revoke = %+v", fs)
	}
	if _, err := f.srv.MemberForUser(f.sh, cid.ID); CodeOf(err) != CodeUnauthorized {
		t.Errorf("member of a revoked link: %v", err)
	}
	if m, re, err := f.srv.AddUserMember(f.sh, "cid", false); err != nil || !re || m.Username != "dragana" {
		t.Fatalf("cid back = %+v %v %v", m, re, err)
	}
	// A disabled account is not added.
	f.user("dan", "danova-lozinka-1")
	f.srv.DisableUser("dan")
	if _, _, err := f.srv.AddUserMember(f.sh, "dan", false); CodeOf(err) != CodeUserDisabled {
		t.Errorf("disabled account: %v", err)
	}
	// Search shows names only, enabled accounts only.
	refs, err := f.srv.SearchUsers("Ma", 20)
	if err != nil || len(refs) != 1 || refs[0].Username != "marko" {
		t.Errorf("search = %+v %v", refs, err)
	}
	if refs, _ := f.srv.SearchUsers("", 20); len(refs) != 3 {
		t.Errorf("all = %+v", refs)
	}
	if refs, _ := f.srv.SearchUsers("%", 20); len(refs) != 0 {
		t.Errorf("%% matched %+v", refs)
	}
}

func TestVerifyUserShares(t *testing.T) {
	f := newAcctFixture(t)
	bob := f.user("bob", pwBob)
	cid := f.user("cid", "cidova-lozinka-1")
	f.srv.AddUserMember(f.sh, "bob", false)
	f.srv.AddUserMember(f.sh, "cid", false)
	if is, err := f.srv.VerifyUserShares(f.sh, false); err != nil || len(is) != 0 {
		t.Fatalf("clean = %+v %v", is, err)
	}
	// Lose bob's row, point cid's at another member, add a stale row.
	f.srv.reg.Exec(`DELETE FROM user_shares WHERE user_id = ?`, bob.ID)
	f.srv.reg.Exec(`UPDATE user_shares SET member_id = 99 WHERE user_id = ?`, cid.ID)
	dan := f.user("dan", "danova-lozinka-1")
	f.srv.reg.Exec(`INSERT INTO user_shares VALUES (?, ?, 1)`, dan.ID, f.sh.ID)
	is, err := f.srv.VerifyUserShares(f.sh, false)
	if err != nil || len(is) != 3 {
		t.Fatalf("issues = %+v %v", is, err)
	}
	kinds := map[string]string{}
	for _, i := range is {
		kinds[i.Path] = i.Kind
		if i.Area != AreaUserShares || i.Repaired {
			t.Errorf("issue %+v", i)
		}
	}
	if kinds["bob"] != IssueMissing || kinds["cid"] != IssueMismatch || kinds["dan"] != IssueUntracked {
		t.Errorf("kinds = %v", kinds)
	}
	if is, err := f.srv.VerifyUserShares(f.sh, true); err != nil || len(is) != 3 || !is[0].Repaired {
		t.Fatalf("repair = %+v %v", is, err)
	}
	if is, err := f.srv.VerifyUserShares(f.sh, false); err != nil || len(is) != 0 {
		t.Fatalf("after repair = %+v %v", is, err)
	}
	if fs, _ := f.srv.UserFolders(bob.ID); len(fs) != 1 {
		t.Errorf("bob's folders after repair: %+v", fs)
	}
}

// TestMigrateTestServerSchema opens databases with the schema the test
// server has before this change (registry 0001, service 0001-0004) and
// rows in them: the new migrations apply, nothing is lost, the old token
// works and its member can be linked to an account.
func TestMigrateTestServerSchema(t *testing.T) {
	root := t.TempDir()
	id := "0b0e2b6c-9d1e-4c4e-8d6b-3f2a1c0d9e8f"
	oldDB := func(file, set string, upTo int, rows ...string) {
		t.Helper()
		os.MkdirAll(filepath.Dir(file), 0o700)
		q := url.Values{}
		q.Add("_pragma", "foreign_keys(1)")
		db, err := sql.Open("sqlite", "file:"+file+"?"+q.Encode())
		if err != nil {
			t.Fatal(err)
		}
		defer db.Close()
		ms, err := migrations(set)
		if err != nil {
			t.Fatal(err)
		}
		for _, m := range ms[:upTo] {
			if _, err := db.Exec(m.sql); err != nil {
				t.Fatalf("%s: %v", m.name, err)
			}
		}
		if _, err := db.Exec(`PRAGMA user_version = ` + string(rune('0'+upTo))); err != nil {
			t.Fatal(err)
		}
		for _, r := range rows {
			if _, err := db.Exec(r); err != nil {
				t.Fatalf("%s: %v", r, err)
			}
		}
	}
	token := NewToken()
	oldDB(filepath.Join(root, RegistryFile), "registry", 1,
		`INSERT INTO shares VALUES ('`+id+`', 'stp-assets', '2026-11-23T10:00:00Z')`)
	oldDB(filepath.Join(root, id, DBFile), "service", 4,
		`INSERT INTO members (username, token_hash, role, created_at) VALUES ('igor', '`+HashToken(token)+`', 'admin', '2026-11-23T10:00:00Z')`,
		`INSERT INTO members (username, token_hash, role, created_at, revoked_at) VALUES ('igor4', 'x', 'member', '2026-11-23T10:00:00Z', '2026-11-24T10:00:00Z')`,
		`INSERT INTO events (seq, ts, member, op, path, hash, size, mtime) VALUES (1, '2026-11-23T10:01:00Z', 'igor', 'put', 'a.txt', 'h', 3, 5)`,
		`INSERT INTO files VALUES ('a.txt', 'a.txt', 'file', 'h', 3, 5, 1, 'igor')`)

	clock := &testClock{t: t0}
	p := cheapPW
	srv, err := Open(root, Options{Now: clock.Now, PasswordParams: &p, SkipRecovery: true})
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()
	sh, err := srv.Share(id)
	if err != nil {
		t.Fatal(err)
	}
	var rv, sv int
	srv.reg.QueryRow(`PRAGMA user_version`).Scan(&rv)
	sh.db.QueryRow(`PRAGMA user_version`).Scan(&sv)
	if rm, _ := migrations("registry"); rv != len(rm) || rv != 2 {
		t.Errorf("registry version %d", rv)
	}
	if sm, _ := migrations("service"); sv != len(sm) || sv != 5 {
		t.Errorf("service version %d", sv)
	}
	m, err := sh.Authenticate(token)
	if err != nil || m.Username != "igor" || m.UserID != nil {
		t.Fatalf("old token: %+v %v", m, err)
	}
	if head, _ := sh.Head(); head != 1 {
		t.Errorf("head = %d", head)
	}
	u, _, err := srv.AddUser("igor", "Igor")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := srv.LinkMember(sh, "igor", "igor"); err != nil {
		t.Fatal(err)
	}
	if fs, err := srv.UserFolders(u.ID); err != nil || len(fs) != 1 || fs[0].Name != "stp-assets" || fs[0].Role != RoleAdmin {
		t.Errorf("folders = %+v %v", fs, err)
	}
	if _, err := sh.Authenticate(token); err != nil {
		t.Errorf("old token after link: %v", err)
	}
	// The unique index: one member per account.
	if _, err := sh.db.Exec(`UPDATE members SET user_id = ? WHERE username = 'igor4'`, u.ID); err == nil || !strings.Contains(err.Error(), "UNIQUE") {
		t.Errorf("second member of one account: %v", err)
	}
}

func asError(err error, e **Error) bool {
	x, ok := err.(*Error)
	if ok {
		*e = x
	}
	return ok
}

func contains(ss []string, s string) bool {
	for _, x := range ss {
		if x == s {
			return true
		}
	}
	return false
}
