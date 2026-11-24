package server

import (
	"context"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/digital-cube/shared-folder/internal/mail"
)

const resetPage = "https://shf.example.com/gui/"

// newResetFixture is newAcctFixture with a fake mail sender and a reset
// page; bob has an email address and the password pwBob.
func newResetFixture(t *testing.T) (*acctFixture, *mail.Fake, *User) {
	t.Helper()
	f := &acctFixture{t: t, clock: &testClock{t: t0}}
	p := cheapPW
	fake := &mail.Fake{}
	srv, err := Open(t.TempDir(), Options{Now: f.clock.Now, PasswordParams: &p, Mail: fake, ResetURL: resetPage, Log: quiet})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { srv.Close() })
	f.srv = srv
	bob := f.user("bob", pwBob)
	if _, err := srv.SetEmail("bob", "  Bob@Example.COM "); err != nil {
		t.Fatal(err)
	}
	bob.Email = "bob@example.com"
	return f, fake, bob
}

var quiet = slog.New(slog.NewTextHandler(io.Discard, nil))

func (f *acctFixture) forgot(login, ip string) error {
	f.t.Helper()
	err := f.srv.Forgot(ForgotRequest{Login: login, Lang: "sr", IP: ip})
	f.srv.WaitMail()
	return err
}

// tokenOf takes the token out of the link of a reset mail.
func tokenOf(t *testing.T, m mail.Message) string {
	t.Helper()
	i := strings.Index(m.Text, resetPage+ResetFragment)
	if i < 0 {
		t.Fatalf("no link in %q", m.Text)
	}
	tok := m.Text[i+len(resetPage+ResetFragment):]
	return tok[:strings.IndexAny(tok, " \n")]
}

func (f *acctFixture) reset(token, pw, ip string) (*ResetResult, error) {
	res, err := f.srv.ResetWithToken(context.Background(), ResetRequest{Token: token, NewPassword: pw, Lang: "en", IP: ip})
	f.srv.WaitMail()
	return res, err
}

func TestNormalizeEmail(t *testing.T) {
	for in, want := range map[string]string{"": "", " Ana@Example.COM ": "ana@example.com", "a.b+c@sub.example.rs": "a.b+c@sub.example.rs"} {
		if got, err := NormalizeEmail(in); err != nil || got != want {
			t.Errorf("%q: %q %v", in, got, err)
		}
	}
	for _, in := range []string{"ana", "ana@", "@example.com", "ana@localhost", "Ana <ana@example.com>", "a@b@c.com", "ana@example.com.", "a b@example.com", strings.Repeat("a", 250) + "@example.com"} {
		if _, err := NormalizeEmail(in); CodeOf(err) != CodeInvalidEmail {
			t.Errorf("%q: %v", in, err)
		}
	}
}

func TestEmailOfAccounts(t *testing.T) {
	f, _, bob := newResetFixture(t)
	if u, _ := f.srv.UserByName("bob"); u.Email != "bob@example.com" {
		t.Fatalf("email %q", u.Email)
	}
	// One address, one account; the check ignores case.
	if _, _, err := f.srv.AddUserWithEmail("ana2", "", "BOB@example.com"); CodeOf(err) != CodeEmailTaken {
		t.Errorf("add with a taken email: %v", err)
	}
	if _, err := f.srv.UserByName("ana2"); CodeOf(err) != CodeUnknownUser {
		t.Errorf("the account was made anyway: %v", err)
	}
	u, _, err := f.srv.AddUserWithEmail("cid", "", "cid@example.com")
	if err != nil || u.Email != "cid@example.com" {
		t.Fatalf("add with email: %+v %v", u, err)
	}
	if _, err := f.srv.SetEmail("cid", "bob@example.com"); CodeOf(err) != CodeEmailTaken {
		t.Errorf("set a taken email: %v", err)
	}
	if _, err := f.srv.SetEmail("cid", "not an address"); CodeOf(err) != CodeInvalidEmail {
		t.Errorf("set a bad email: %v", err)
	}
	if u, err := f.srv.SetEmail("cid", ""); err != nil || u.Email != "" {
		t.Errorf("remove: %+v %v", u, err)
	}
	// Two accounts without an address are fine (NULL is not unique).
	if _, _, err := f.srv.AddUserWithEmail("dan", "", ""); err != nil {
		t.Errorf("a second account without email: %v", err)
	}

	// The account's own change asks for its password, which counts like a sign-in.
	sess := &Session{User: bob}
	if err := f.srv.SetMyEmail(context.Background(), sess, "bob@new.example.com", "wrong-password-1", "10.0.0.1"); CodeOf(err) != CodeWrongPassword {
		t.Errorf("wrong password: %v", err)
	}
	if err := f.srv.SetMyEmail(context.Background(), sess, "bob@", pwBob, "10.0.0.1"); CodeOf(err) != CodeInvalidEmail {
		t.Errorf("bad address: %v", err)
	}
	if err := f.srv.SetMyEmail(context.Background(), sess, "Bob@New.example.com", pwBob, "10.0.0.1"); err != nil || bob.Email != "bob@new.example.com" {
		t.Errorf("own change: %v %q", err, bob.Email)
	}
	got := f.outcomes()
	if got[len(got)-1] != "bob 10.0.0.1 bad_password" {
		t.Errorf("auth_log %v", got)
	}
}

// Forgot does the same for an account that exists and one that does not;
// only the first gets a mail, with a link to the GUI and a token of which
// the database keeps only the hash.
func TestForgotSameAnswerMailOnlyForAccount(t *testing.T) {
	f, fake, _ := newResetFixture(t)
	f.user("nomail", "nomail-lozinka-1")
	f.user("off", "off-lozinka-1")
	f.srv.SetEmail("off", "off@example.com")
	f.srv.DisableUser("off")

	for _, login := range []string{"nobody", "nobody@example.com", "nomail", "off", "off@example.com"} {
		if err := f.forgot(login, "10.0.0.1"); err != nil {
			t.Errorf("%s: %v", login, err)
		}
	}
	if n := len(fake.Sent()); n != 0 {
		t.Fatalf("%d mails for accounts that get none", n)
	}
	if err := f.forgot("  BOB@example.com ", "10.0.0.1"); err != nil {
		t.Fatal(err)
	}
	if err := f.forgot("bob", "10.0.0.2"); err != nil {
		t.Fatal(err)
	}
	sent := fake.Sent()
	if len(sent) != 2 {
		t.Fatalf("%d mails", len(sent))
	}
	m := sent[0]
	if m.To.Email != "bob@example.com" || !strings.Contains(m.Text, "Bob (bob)") || !strings.Contains(m.Text, "30 minuta") {
		t.Errorf("mail %+v", m)
	}
	tok := tokenOf(t, m)
	if len(tok) != 43 {
		t.Errorf("token %q", tok)
	}
	var plain int
	f.srv.reg.QueryRow(`SELECT count(*) FROM password_resets WHERE token_hash = ? OR token_hash = ?`, tok, HashToken(tok)).Scan(&plain)
	var hashed int
	f.srv.reg.QueryRow(`SELECT count(*) FROM password_resets WHERE token_hash = ?`, HashToken(tok)).Scan(&hashed)
	if plain != 1 || hashed != 1 {
		t.Errorf("token stored as %d/%d", plain, hashed)
	}
	want := []string{
		"nobody 10.0.0.1 reset_requested", "nobody@example.com 10.0.0.1 reset_requested", "nomail 10.0.0.1 reset_requested",
		"off 10.0.0.1 reset_requested", "off 10.0.0.1 reset_requested", "bob 10.0.0.1 reset_requested", "bob 10.0.0.2 reset_requested",
	}
	if got := f.outcomes(); strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("auth_log\n%v\nwant\n%v", got, want)
	}

	// The newer request voided the first link.
	if _, err := f.reset(tok, "nova-lozinka-123", "10.0.0.3"); CodeOf(err) != CodeInvalidResetToken {
		t.Errorf("voided token: %v", err)
	}
	if _, err := f.reset(tokenOf(t, sent[1]), "nova-lozinka-123", "10.0.0.3"); err != nil {
		t.Errorf("newest token: %v", err)
	}
}

func TestForgotMailOff(t *testing.T) {
	for _, o := range []Options{{ResetURL: resetPage}, {Mail: &mail.Fake{}}} {
		f := &acctFixture{t: t, clock: &testClock{t: t0}}
		p := cheapPW
		o.Now, o.PasswordParams, o.Log = f.clock.Now, &p, quiet
		srv, err := Open(t.TempDir(), o)
		if err != nil {
			t.Fatal(err)
		}
		f.srv = srv
		f.user("bob", pwBob)
		srv.SetEmail("bob", "bob@example.com")
		if srv.MailEnabled() {
			t.Errorf("%+v: mail enabled", o)
		}
		if err := f.forgot("bob", "10.0.0.1"); err != nil {
			t.Errorf("forgot: %v", err)
		}
		if fk, ok := o.Mail.(*mail.Fake); ok && len(fk.Sent()) != 0 {
			t.Errorf("sent without a reset page")
		}
		var n int
		srv.reg.QueryRow(`SELECT count(*) FROM password_resets`).Scan(&n)
		if n != 0 {
			t.Errorf("%d tokens made with mail off", n)
		}
		srv.Close()
	}
	if err := (&Server{}).Forgot(ForgotRequest{Login: " "}); CodeOf(err) != CodeInvalidRequest {
		t.Errorf("empty login: %v", err)
	}
}

func TestResetPassword(t *testing.T) {
	f, fake, bob := newResetFixture(t)
	f.srv.reg.Exec(`UPDATE users SET must_change = 1 WHERE id = ?`, bob.ID)
	gui1, _ := f.login("bob", pwBob, DeviceGUI, "10.0.0.1")
	gui2, _ := f.login("bob", pwBob, DeviceGUI, "10.0.0.1")
	cli, err := f.login("bob", pwBob, DeviceClient, "10.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	f.forgot("bob", "10.0.0.1")
	tok := tokenOf(t, fake.Sent()[0])
	fake.Reset()

	// The rules of every password; a refused one leaves the token usable.
	if _, err := f.reset(tok, "short", "10.0.0.9"); CodeOf(err) != CodeInvalidPassword {
		t.Errorf("short: %v", err)
	}
	if _, err := f.reset(tok, "BOB", "10.0.0.9"); CodeOf(err) != CodeInvalidPassword {
		t.Errorf("username: %v", err)
	}
	res, err := f.reset(tok, "nova-lozinka-123", "10.0.0.9")
	if err != nil {
		t.Fatal(err)
	}
	if res.Username != "bob" || res.SignedOut != 2 {
		t.Errorf("result %+v", res)
	}
	// The new password works, must_change is gone, the old one fails.
	if _, err := f.login("bob", pwBob, DeviceClient, "10.0.0.1"); CodeOf(err) != CodeInvalidCredentials {
		t.Errorf("old password: %v", err)
	}
	if r, err := f.login("bob", "nova-lozinka-123", DeviceClient, "10.0.0.1"); err != nil || r.MustChange {
		t.Errorf("new password: %+v %v", r, err)
	}
	// GUI sessions signed out, the client device not.
	for _, tk := range []string{gui1.Token, gui2.Token} {
		if _, err := f.srv.AuthenticateDevice(tk); CodeOf(err) != CodeUnauthorized {
			t.Errorf("gui session still works: %v", err)
		}
	}
	if _, err := f.srv.AuthenticateDevice(cli.Token); err != nil {
		t.Errorf("client device: %v", err)
	}
	// Once only.
	if _, err := f.reset(tok, "druga-lozinka-123", "10.0.0.9"); CodeOf(err) != CodeInvalidResetToken {
		t.Errorf("token used twice: %v", err)
	}
	// The notice, in the language of the request.
	sent := fake.Sent()
	if len(sent) != 1 || sent[0].To.Email != "bob@example.com" || !strings.Contains(sent[0].Text, "was changed at 2026-11-23 14:58 UTC from the address 10.0.0.9") {
		t.Errorf("notice %+v", sent)
	}
	got := strings.Join(f.outcomes(), "|")
	if !strings.Contains(got, "bob 10.0.0.9 reset_done") || !strings.HasSuffix(got, "bob 10.0.0.9 bad_token") {
		t.Errorf("auth_log %v", got)
	}
}

func TestResetTokenExpires(t *testing.T) {
	f, fake, _ := newResetFixture(t)
	f.forgot("bob", "10.0.0.1")
	tok := tokenOf(t, fake.Sent()[0])
	f.clock.Advance(ResetValidity)
	if _, err := f.reset(tok, "nova-lozinka-123", "10.0.0.1"); CodeOf(err) != CodeInvalidResetToken {
		t.Errorf("expired: %v", err)
	}
	// A disabled account's token does not work either.
	f.forgot("bob", "10.0.0.1")
	tok = tokenOf(t, fake.Sent()[1])
	f.srv.DisableUser("bob")
	if _, err := f.reset(tok, "nova-lozinka-123", "10.0.0.1"); CodeOf(err) != CodeInvalidResetToken {
		t.Errorf("disabled: %v", err)
	}
	if _, err := f.reset("", "nova-lozinka-123", "10.0.0.1"); CodeOf(err) != CodeInvalidResetToken {
		t.Errorf("empty: %v", err)
	}
}

// At most MaxResetsPerAccount links an hour per account, whether asked by
// username or by email and from any address; the answer stays the same.
func TestForgotLimitPerAccount(t *testing.T) {
	f, fake, _ := newResetFixture(t)
	logins := []string{"bob", "bob@example.com", "bob", "BOB", "bob@example.com"}
	for i, l := range logins {
		if err := f.forgot(l, "10.0.0."+string(rune('1'+i))); err != nil {
			t.Fatalf("%d: %v", i, err)
		}
	}
	if n := len(fake.Sent()); n != MaxResetsPerAccount {
		t.Errorf("%d mails, want %d", n, MaxResetsPerAccount)
	}
	got := f.outcomes()
	if got[3] != "bob 10.0.0.4 locked" || got[4] != "bob 10.0.0.5 locked" {
		t.Errorf("auth_log %v", got)
	}
	f.clock.Advance(resetWindow)
	f.forgot("bob", "10.0.0.1")
	if n := len(fake.Sent()); n != MaxResetsPerAccount+1 {
		t.Errorf("after an hour: %d mails", n)
	}
}

// One address may ask MaxResetsPerIP times an hour, for any login; then it
// gets too_many_attempts with the wait.
func TestForgotLimitPerIP(t *testing.T) {
	f, _, _ := newResetFixture(t)
	for i := range MaxResetsPerIP {
		if err := f.forgot("user"+string(rune('a'+i)), "198.51.100.7"); err != nil {
			t.Fatalf("%d: %v", i, err)
		}
		f.clock.Advance(time.Minute)
	}
	err := f.forgot("bob", "198.51.100.7")
	var se *Error
	if CodeOf(err) != CodeTooManyAttempts || !asError(err, &se) || se.RetryAfter != resetWindow-MaxResetsPerIP*time.Minute {
		t.Fatalf("over the limit: %v", err)
	}
	if err := f.forgot("bob", "198.51.100.8"); err != nil {
		t.Errorf("another address: %v", err)
	}
	f.clock.Advance(se.RetryAfter - time.Second)
	if err := f.forgot("bob", "198.51.100.7"); CodeOf(err) != CodeTooManyAttempts {
		t.Errorf("a second before the oldest leaves the window: %v", err)
	}
	f.clock.Advance(time.Second)
	if err := f.forgot("bob", "198.51.100.7"); err != nil {
		t.Errorf("after the oldest left the window: %v", err)
	}
}

// Asking for links is no failed sign-in; a wrong reset token is one for
// the address, and a reset clears the account's failures.
func TestResetAndTheSignInLimit(t *testing.T) {
	f, fake, _ := newResetFixture(t)
	for range MaxResetsPerAccount {
		f.forgot("bob", "10.0.0.1")
	}
	if _, err := f.login("bob", pwBob, DeviceClient, "10.0.0.1"); err != nil {
		t.Fatalf("sign-in after forgot: %v", err)
	}
	for range MaxFailures {
		if _, err := f.reset("guessed-token", "nova-lozinka-123", "10.0.0.66"); CodeOf(err) != CodeInvalidResetToken {
			t.Fatal(err)
		}
	}
	if _, err := f.reset("guessed-token", "nova-lozinka-123", "10.0.0.66"); CodeOf(err) != CodeTooManyAttempts {
		t.Errorf("after %d bad tokens: %v", MaxFailures, err)
	}
	// bob fails his sign-in until he and his address are locked, then
	// resets the password from that address.
	for range MaxFailures {
		f.login("bob", "wrong-password-1", DeviceClient, "10.0.0.2")
	}
	if _, err := f.login("bob", pwBob, DeviceClient, "10.0.0.3"); CodeOf(err) != CodeTooManyAttempts {
		t.Fatalf("not locked: %v", err)
	}
	tok := tokenOf(t, fake.Sent()[len(fake.Sent())-1])
	if _, err := f.reset(tok, "nova-lozinka-123", "10.0.0.2"); err != nil {
		t.Fatal(err)
	}
	if _, err := f.login("bob", "nova-lozinka-123", DeviceClient, "10.0.0.3"); err != nil {
		t.Errorf("sign-in after the reset: %v", err)
	}
}

func TestIssueResetToken(t *testing.T) {
	f, fake, _ := newResetFixture(t)
	f.forgot("bob", "10.0.0.1")
	mailed := tokenOf(t, fake.Sent()[0])
	tok, exp, err := f.srv.IssueResetToken("bob")
	if err != nil || !exp.Equal(t0.Add(ResetValidity)) {
		t.Fatalf("%v %v", exp, err)
	}
	if _, err := f.reset(mailed, "nova-lozinka-123", "10.0.0.1"); CodeOf(err) != CodeInvalidResetToken {
		t.Errorf("the mailed token still works: %v", err)
	}
	if _, err := f.reset(tok, "nova-lozinka-123", "10.0.0.1"); err != nil {
		t.Errorf("the admin's token: %v", err)
	}
	if _, _, err := f.srv.IssueResetToken("nobody"); CodeOf(err) != CodeUnknownUser {
		t.Errorf("unknown: %v", err)
	}
	f.srv.DisableUser("bob")
	if _, _, err := f.srv.IssueResetToken("bob"); CodeOf(err) != CodeUserDisabled {
		t.Errorf("disabled: %v", err)
	}
}

func TestResetPageURL(t *testing.T) {
	for _, c := range [][3]string{
		{"https://shared-storage.igor.digitalcube.work", "/gui", "https://shared-storage.igor.digitalcube.work/gui/"},
		{"https://h.example.com/", "gui/", "https://h.example.com/gui/"},
		{"https://h.example.com", "/", "https://h.example.com/"},
		{"https://h.example.com", "", "https://h.example.com/"},
		{"http://127.0.0.1:8444/sub/", "/gui", "http://127.0.0.1:8444/sub/gui/"},
	} {
		if got, err := ResetPageURL(c[0], c[1]); err != nil || got != c[2] {
			t.Errorf("%q %q: %q %v", c[0], c[1], got, err)
		}
	}
	for _, bad := range []string{"", "shf.example.com", "ftp://h", "https://h/?a=1", "https://u:p@h"} {
		if _, err := ResetPageURL(bad, "/gui"); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}

// The mail goes out after the answer: a slow sender does not hold Forgot.
func TestForgotDoesNotWaitForTheMail(t *testing.T) {
	f, _, _ := newResetFixture(t)
	slow := &slowSender{release: make(chan struct{})}
	f.srv.opts.Mail = slow
	done := make(chan error, 1)
	go func() { done <- f.srv.Forgot(ForgotRequest{Login: "bob", IP: "10.0.0.1"}) }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Forgot waited for the mail")
	}
	close(slow.release)
	f.srv.WaitMail()
}

type slowSender struct{ release chan struct{} }

func (s *slowSender) Send(ctx context.Context, _ mail.Message) error {
	<-s.release
	return nil
}
