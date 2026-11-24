package httpapi_test

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"sync"
	"testing"

	"github.com/digital-cube/shared-folder/internal/mail"
	"github.com/digital-cube/shared-folder/internal/server"
	"github.com/digital-cube/shared-folder/internal/server/httpapi"
)

const brevoKey = "xkeysib-test-0011223344556677-NEVERLOG"

// brevoFake is Brevo's transactional endpoint: it keeps every mail and
// answers 201, or status when set.
type brevoFake struct {
	mu     sync.Mutex
	mails  []map[string]any
	keys   []string
	status int
}

func (b *brevoFake) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	var m map[string]any
	json.NewDecoder(r.Body).Decode(&m)
	b.mu.Lock()
	b.mails = append(b.mails, m)
	b.keys = append(b.keys, r.Header.Get("api-key"))
	st := b.status
	b.mu.Unlock()
	if st != 0 {
		w.WriteHeader(st)
		io.WriteString(w, `{"code":"unauthorized","message":"Key not found `+r.Header.Get("api-key")+`"}`)
		return
	}
	w.WriteHeader(http.StatusCreated)
	io.WriteString(w, `{"messageId":"<x@brevo>"}`)
}

func (b *brevoFake) sent() []map[string]any {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]map[string]any(nil), b.mails...)
}

// syncBuffer is a log destination for goroutines.
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

// newMailEnv is an env whose server mails through a fake Brevo (the real
// sender, over HTTP) and logs into a buffer, as does the API (slog's
// default for the test).
func newMailEnv(t *testing.T) (*env, *brevoFake, *syncBuffer) {
	t.Helper()
	fb := &brevoFake{}
	ts := httptest.NewServer(fb)
	t.Cleanup(ts.Close)
	logs := &syncBuffer{}
	log := slog.New(slog.NewTextHandler(logs, nil))
	old := slog.Default()
	slog.SetDefault(log)
	t.Cleanup(func() { slog.SetDefault(old) })
	from, _ := mail.ParseAddress(mail.DefaultFrom)
	e := newEnv(t, func(o *server.Options) {
		p := cheapPW
		o.PasswordParams = &p
		o.Mail = mail.NewBrevo(brevoKey, from, ts.URL+"/v3/smtp/email")
		o.ResetURL = "https://shared-storage.example.work/gui/"
		o.Log = log
	})
	return e, fb, logs
}

var tokenRE = regexp.MustCompile(`#reset\?token=([A-Za-z0-9_-]{43})`)

func TestForgotOverHTTP(t *testing.T) {
	e, fb, logs := newMailEnv(t)
	e.account("bob", "bobova-lozinka")
	if _, err := e.srv.SetEmail("bob", "bob@example.com"); err != nil {
		t.Fatal(err)
	}

	// The same status and body for an account and for nothing.
	var bodies []string
	for _, login := range []string{"bob", "nobody", "bob@example.com", "nobody@example.com"} {
		st, hdr, b := e.call("POST", "/v1/auth/forgot", "", map[string]string{"login": login, "lang": "en"}, "X-Forwarded-For", "203.0.113.5")
		if st != http.StatusAccepted || hdr.Get("Cache-Control") != "no-store" {
			t.Fatalf("%s: %d %s", login, st, b)
		}
		bodies = append(bodies, string(b))
	}
	for _, b := range bodies[1:] {
		if b != bodies[0] {
			t.Errorf("bodies differ: %q vs %q", b, bodies[0])
		}
	}
	e.srv.WaitMail()
	sent := fb.sent()
	if len(sent) != 2 {
		t.Fatalf("%d mails, want 2 (bob by username and by email)", len(sent))
	}
	for i, k := range fb.keys {
		if k != brevoKey {
			t.Errorf("mail %d: api-key %q", i, k)
		}
	}
	m := sent[1]
	to := m["to"].([]any)[0].(map[string]any)
	sender := m["sender"].(map[string]any)
	if to["email"] != "bob@example.com" || sender["email"] != "noreply@digitalcube.rs" || sender["name"] != "shared-storage" {
		t.Errorf("mail %v", m)
	}
	text, html := m["textContent"].(string), m["htmlContent"].(string)
	if !strings.Contains(text, "valid for 30 minutes") || !strings.Contains(html, "https://shared-storage.example.work/gui/#reset?token=") {
		t.Errorf("mail text %q", text)
	}
	tok := tokenRE.FindStringSubmatch(text)
	if tok == nil {
		t.Fatalf("no token in %q", text)
	}

	// A GUI session and a client device; the reset ends only the first.
	gui := e.loginOK("bob", "bobova-lozinka", "gui")
	cli := e.loginOK("bob", "bobova-lozinka", "client")
	st, _, b := e.call("POST", "/v1/auth/reset", "", map[string]string{"token": tok[1], "new_password": "x", "lang": "sr"})
	if st != 400 || errCode(b) != server.CodeInvalidPassword {
		t.Errorf("short password: %d %s", st, b)
	}
	st, _, b = e.call("POST", "/v1/auth/reset", "", map[string]string{"token": tok[1], "new_password": "nova-bobova-lozinka", "lang": "sr"})
	if st != 200 {
		t.Fatalf("reset: %d %s", st, b)
	}
	var res server.ResetResult
	json.Unmarshal(b, &res)
	if res.Username != "bob" || res.SignedOut != 1 {
		t.Errorf("result %+v", res)
	}
	if st, _, _ := e.call("GET", "/v1/me", gui, nil); st != 401 {
		t.Errorf("gui session after the reset: %d", st)
	}
	if st, _, _ := e.call("GET", "/v1/me", cli, nil); st != 200 {
		t.Errorf("client device after the reset: %d", st)
	}
	st, _, b = e.call("POST", "/v1/auth/reset", "", map[string]string{"token": tok[1], "new_password": "treca-bobova-lozinka"})
	if st != 400 || errCode(b) != server.CodeInvalidResetToken {
		t.Errorf("second use: %d %s", st, b)
	}
	e.loginOK("bob", "nova-bobova-lozinka", "client")

	e.srv.WaitMail()
	sent = fb.sent()
	if len(sent) != 3 || !strings.Contains(sent[2]["textContent"].(string), "lozinka naloga bob") {
		t.Errorf("notice: %d mails, last %v", len(sent), sent[len(sent)-1]["subject"])
	}

	// Neither the key nor a token in any log line.
	for _, secret := range []string{brevoKey, tok[1]} {
		if strings.Contains(logs.String(), secret) {
			t.Errorf("log shows a secret:\n%s", logs.String())
		}
	}
}

// Brevo refuses the key and says it back: the failure is logged without it.
func TestForgotBrevoRefusesKeyNotLogged(t *testing.T) {
	e, fb, logs := newMailEnv(t)
	fb.status = http.StatusUnauthorized
	e.account("bob", "bobova-lozinka")
	e.srv.SetEmail("bob", "bob@example.com")
	if st, _, b := e.call("POST", "/v1/auth/forgot", "", map[string]string{"login": "bob"}); st != 202 {
		t.Fatalf("%d %s", st, b)
	}
	e.srv.WaitMail()
	out := logs.String()
	if !strings.Contains(out, "mail: not sent") || !strings.Contains(out, "401") {
		t.Errorf("no failure logged:\n%s", out)
	}
	if strings.Contains(out, brevoKey) || strings.Contains(out, "NEVERLOG") {
		t.Errorf("log shows the key:\n%s", out)
	}
}

func TestForgotLimitOverHTTP(t *testing.T) {
	e, _, _ := newMailEnv(t)
	for i := range server.MaxResetsPerIP {
		if st, _, b := e.call("POST", "/v1/auth/forgot", "", map[string]string{"login": "u" + string(rune('a'+i))}, "X-Forwarded-For", "203.0.113.9"); st != 202 {
			t.Fatalf("%d: %d %s", i, st, b)
		}
	}
	st, hdr, b := e.call("POST", "/v1/auth/forgot", "", map[string]string{"login": "bob"}, "X-Forwarded-For", "203.0.113.9")
	if st != 429 || errCode(b) != server.CodeTooManyAttempts || hdr.Get("Retry-After") != "3600" {
		t.Errorf("over the limit: %d %v %s", st, hdr.Get("Retry-After"), b)
	}
	if st, _, _ := e.call("POST", "/v1/auth/forgot", "", map[string]string{"login": "bob"}, "X-Forwarded-For", "203.0.113.10"); st != 202 {
		t.Errorf("another client behind the proxy: %d", st)
	}
	if st, _, b := e.call("POST", "/v1/auth/forgot", "", map[string]string{"login": " "}); st != 400 {
		t.Errorf("empty login: %d %s", st, b)
	}
	if st, _, b := e.call("POST", "/v1/auth/reset", "", map[string]string{"token": "nope", "new_password": "nova-bobova-lozinka"}); st != 400 || errCode(b) != server.CodeInvalidResetToken {
		t.Errorf("unknown token: %d %s", st, b)
	}
}

func TestMyEmailAndHealth(t *testing.T) {
	e, _, _ := newMailEnv(t)
	e.account("bob", "bobova-lozinka")
	tok := e.loginOK("bob", "bobova-lozinka", "gui")
	var me httpapi.MeResponse
	_, _, b := e.call("GET", "/v1/me", tok, nil)
	json.Unmarshal(b, &me)
	if me.User.Email != "" || !strings.Contains(string(b), `"email":""`) {
		t.Errorf("me: %s", b)
	}
	if st, _, b := e.call("PUT", "/v1/me/email", tok, httpapi.EmailRequest{Email: "bob@example.com", CurrentPassword: "pogresna-lozinka"}); st != 403 || errCode(b) != server.CodeWrongPassword {
		t.Errorf("wrong password: %d %s", st, b)
	}
	if st, _, b := e.call("PUT", "/v1/me/email", tok, httpapi.EmailRequest{Email: "bob", CurrentPassword: "bobova-lozinka"}); st != 400 || errCode(b) != server.CodeInvalidEmail {
		t.Errorf("bad address: %d %s", st, b)
	}
	e.account("cid", "cidova-lozinka")
	e.srv.SetEmail("cid", "cid@example.com")
	if st, _, b := e.call("PUT", "/v1/me/email", tok, httpapi.EmailRequest{Email: "CID@example.com", CurrentPassword: "bobova-lozinka"}); st != 409 || errCode(b) != server.CodeEmailTaken {
		t.Errorf("taken: %d %s", st, b)
	}
	st, _, b := e.call("PUT", "/v1/me/email", tok, httpapi.EmailRequest{Email: " Bob@Example.com ", CurrentPassword: "bobova-lozinka"})
	if st != 200 || !strings.Contains(string(b), `"email":"bob@example.com"`) {
		t.Errorf("set: %d %s", st, b)
	}
	_, _, b = e.call("GET", "/v1/me", tok, nil)
	if !strings.Contains(string(b), `"email":"bob@example.com"`) {
		t.Errorf("me after: %s", b)
	}

	_, _, b = e.call("GET", "/healthz", "", nil)
	if !strings.Contains(string(b), `"mail":"enabled"`) {
		t.Errorf("healthz: %s", b)
	}
}
