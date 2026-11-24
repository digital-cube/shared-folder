package mail

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

const testKey = "xkeysib-0123456789abcdef-SECRET"

// fakeBrevo is Brevo's /v3/smtp/email: it keeps each request, and answers
// with status (201 when 0) and body.
type fakeBrevo struct {
	mu     sync.Mutex
	reqs   []fakeReq
	status int
	body   string
}

type fakeReq struct {
	key  string
	path string
	body brevoRequest
}

func (f *fakeBrevo) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	var b brevoRequest
	json.NewDecoder(r.Body).Decode(&b)
	f.mu.Lock()
	f.reqs = append(f.reqs, fakeReq{key: r.Header.Get("api-key"), path: r.URL.Path, body: b})
	st, body := f.status, f.body
	f.mu.Unlock()
	if st == 0 {
		st, body = http.StatusCreated, `{"messageId":"<1@smtp-relay.mailin.fr>"}`
	}
	w.Header().Set("content-type", "application/json")
	w.WriteHeader(st)
	io.WriteString(w, body)
}

func TestBrevoSend(t *testing.T) {
	fb := &fakeBrevo{}
	ts := httptest.NewServer(fb)
	defer ts.Close()
	from, _ := ParseAddress(DefaultFrom)
	b := NewBrevo(testKey, from, ts.URL+"/v3/smtp/email")
	m := Message{To: Address{Name: "Ana", Email: "ana@example.com"}, Subject: "s", Text: "t", HTML: "<p>h</p>"}
	if err := b.Send(context.Background(), m); err != nil {
		t.Fatal(err)
	}
	if len(fb.reqs) != 1 {
		t.Fatalf("%d requests", len(fb.reqs))
	}
	r := fb.reqs[0]
	if r.key != testKey || r.path != "/v3/smtp/email" {
		t.Errorf("api-key %q, path %q", r.key, r.path)
	}
	want := brevoRequest{
		Sender:  brevoContact{Name: "shared-storage", Email: "noreply@digitalcube.rs"},
		To:      []brevoContact{{Name: "Ana", Email: "ana@example.com"}},
		Subject: "s", HTMLContent: "<p>h</p>", TextContent: "t",
	}
	if fmt.Sprint(r.body) != fmt.Sprint(want) {
		t.Errorf("body %+v, want %+v", r.body, want)
	}
	if err := b.Send(context.Background(), Message{}); err != ErrNoRecipient {
		t.Errorf("no recipient: %v", err)
	}
}

// The key appears in no error, no String and no log line, even when Brevo
// echoes it back.
func TestBrevoKeyNeverShown(t *testing.T) {
	fb := &fakeBrevo{status: http.StatusUnauthorized, body: `{"code":"unauthorized","message":"Key not found: ` + testKey + `"}`}
	ts := httptest.NewServer(fb)
	defer ts.Close()
	b := NewBrevo(testKey, Address{Email: "noreply@digitalcube.rs"}, ts.URL)
	err := b.Send(context.Background(), Message{To: Address{Email: "a@b.rs"}})
	if err == nil || !strings.Contains(err.Error(), "401") || !strings.Contains(err.Error(), "Key not found") {
		t.Fatalf("err %v", err)
	}
	var logged bytes.Buffer
	log := slog.New(slog.NewTextHandler(&logged, nil))
	log.Info("x", "sender", b, "err", err)
	log.Info("y", "sender", fmt.Sprintf("%v %+v %#v %s", b, b, b, b))
	for what, s := range map[string]string{"error": err.Error(), "log": logged.String(), "String": b.String()} {
		if strings.Contains(s, testKey) || strings.Contains(s, "SECRET") {
			t.Errorf("%s shows the key: %s", what, s)
		}
	}
	// A server that cannot be reached: the error names the URL, not the key.
	ts.Close()
	err = b.Send(context.Background(), Message{To: Address{Email: "a@b.rs"}})
	if err == nil || strings.Contains(err.Error(), testKey) {
		t.Errorf("unreachable: %v", err)
	}
}

func TestLoadKey(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "brevo.key")
	os.WriteFile(p, []byte("  "+testKey+"  \n# comment\n"), 0o640)
	key, world, err := LoadKey(p)
	if err != nil || key != testKey || world {
		t.Fatalf("LoadKey: %q %v %v", key, world, err)
	}
	os.Chmod(p, 0o644)
	if _, world, _ = LoadKey(p); !world {
		t.Error("0644 not reported as world-readable")
	}
	os.WriteFile(p, []byte("\n"), 0o640)
	if _, _, err := LoadKey(p); err != ErrEmptyKey {
		t.Errorf("empty: %v", err)
	}
	os.WriteFile(p, []byte("two "+testKey), 0o640)
	if _, _, err := LoadKey(p); err == nil || strings.Contains(err.Error(), testKey) {
		t.Errorf("a key with a space: %v", err)
	}
	if _, _, err := LoadKey(filepath.Join(dir, "missing")); err == nil {
		t.Error("missing file: no error")
	}
	if _, _, err := LoadKey(dir); err == nil {
		t.Error("a directory: no error")
	}
}

func TestResetMail(t *testing.T) {
	r := Recipient{Username: "marko", DisplayName: "Marko <M>", Email: "marko@example.com"}
	link := "https://shf.example.com/gui/#reset?token=abc&x=1"
	for _, c := range []struct {
		lang string
		want []string
	}{
		{"", []string{"Zdravo Marko <M> (marko)", "nalog marko", "Link važi 30 minuta", "Ako niste vi tražili", "Nova lozinka za shf.example.com"}},
		{"sr", []string{"Link važi 30 minuta"}},
		{"en", []string{"Hello Marko <M> (marko)", "the account marko", "valid for 30 minutes", "If you did not ask for this", "Reset your password on shf.example.com"}},
		{"en-US", []string{"valid for 30 minutes"}},
	} {
		m := ResetMail(c.lang, r, link, 30*time.Minute, "shf.example.com")
		if m.To.Email != "marko@example.com" || m.To.Name != "Marko <M>" {
			t.Errorf("%s: to %+v", c.lang, m.To)
		}
		for _, w := range c.want {
			if !strings.Contains(m.Subject+"\n"+m.Text, w) {
				t.Errorf("%s: text has no %q:\n%s", c.lang, w, m.Text)
			}
		}
		if !strings.Contains(m.Text, link) {
			t.Errorf("%s: text has no link", c.lang)
		}
		// HTML: the link once as the button, once as text, escaped; the name escaped.
		if !strings.Contains(m.HTML, `href="https://shf.example.com/gui/#reset?token=abc&amp;x=1"`) ||
			strings.Contains(m.HTML, "Marko <M>") || !strings.Contains(m.HTML, "Marko &lt;M&gt;") {
			t.Errorf("%s: html:\n%s", c.lang, m.HTML)
		}
	}
}

func TestPasswordChangedMail(t *testing.T) {
	r := Recipient{Username: "marko", Email: "marko@example.com"}
	at := time.Date(2026, 11, 25, 9, 5, 0, 0, time.UTC)
	sr := PasswordChangedMail("sr", r, at, "203.0.113.9", "shf.example.com")
	en := PasswordChangedMail("en", r, at, "", "shf.example.com")
	for _, w := range []string{"Zdravo marko", "promenjena 2026-11-25 09:05 UTC sa adrese 203.0.113.9", "odjavljene", "Ako to niste bili vi"} {
		if !strings.Contains(sr.Text, w) || !strings.Contains(sr.HTML, w) {
			t.Errorf("sr: no %q:\n%s", w, sr.Text)
		}
	}
	for _, w := range []string{"changed at 2026-11-25 09:05 UTC,", "signed out", "If this was not you"} {
		if !strings.Contains(en.Text, w) {
			t.Errorf("en: no %q:\n%s", w, en.Text)
		}
	}
}

func TestFake(t *testing.T) {
	var f Fake
	f.Send(context.Background(), Message{Subject: "a"})
	if s := f.Sent(); len(s) != 1 || s[0].Subject != "a" {
		t.Fatalf("%+v", s)
	}
	f.Reset()
	f.Err = io.EOF
	if err := f.Send(context.Background(), Message{}); err != io.EOF || len(f.Sent()) != 0 {
		t.Errorf("%v %d", err, len(f.Sent()))
	}
}
