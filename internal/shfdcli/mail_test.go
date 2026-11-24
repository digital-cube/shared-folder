package shfdcli

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/digital-cube/shared-folder/internal/server"
)

const cliKey = "xkeysib-cli-test-key-DONOTPRINT"

type cliBrevo struct {
	mu    sync.Mutex
	mails []map[string]any
	keys  []string
}

func (b *cliBrevo) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	var m map[string]any
	json.NewDecoder(r.Body).Decode(&m)
	b.mu.Lock()
	b.mails = append(b.mails, m)
	b.keys = append(b.keys, r.Header.Get("api-key"))
	b.mu.Unlock()
	w.WriteHeader(http.StatusCreated)
	io.WriteString(w, `{"messageId":"x"}`)
}

func keyFile(t *testing.T) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "brevo.key")
	if err := os.WriteFile(p, []byte(cliKey+"\n"), 0o640); err != nil {
		t.Fatal(err)
	}
	return p
}

var linkRE = regexp.MustCompile(`link:\s+(\S+)#reset\?token=(\S+)`)

func TestUserEmailAndResetLink(t *testing.T) {
	root := t.TempDir()
	env := map[string]string{EnvDataRoot: root}
	if code, _, errOut := run(t, env, "user", "add", "bob", "--email", "Bob@Example.com"); code != 0 {
		t.Fatalf("user add: %s", errOut)
	}
	if code, _, errOut := run(t, env, "user", "add", "cid", "--email", "bob@example.com"); code != 1 || !strings.Contains(errOut, "email_taken") {
		t.Errorf("taken email: %d %s", code, errOut)
	}
	if code, _, errOut := run(t, env, "user", "add", "dan", "--email", "dan"); code != 1 || !strings.Contains(errOut, "invalid_email") {
		t.Errorf("bad email: %d %s", code, errOut)
	}
	_, out, _ := run(t, env, "user", "show", "bob")
	if !regexp.MustCompile(`(?m)^email:\s+bob@example.com$`).MatchString(out) {
		t.Errorf("show:\n%s", out)
	}
	_, out, _ = run(t, env, "user", "list")
	if !regexp.MustCompile(`(?m)^bob\s+-\s+bob@example.com\s+active`).MatchString(out) {
		t.Errorf("list:\n%s", out)
	}
	if code, out, _ := run(t, env, "user", "set-email", "bob", "novi@example.com"); code != 0 || !strings.Contains(out, "email of bob: novi@example.com") {
		t.Errorf("set-email: %d %s", code, out)
	}
	if code, out, _ := run(t, env, "user", "set-email", "bob", "-"); code != 0 || !strings.Contains(out, "no email address") {
		t.Errorf("set-email -: %d %s", code, out)
	}
	if code, _, _ := run(t, env, "user", "set-email", "bob"); code != 2 {
		t.Errorf("set-email without an address: %d", code)
	}

	// Without a public URL: the path and the token, and how to get the whole link.
	code, out, errOut := run(t, env, "user", "reset-link", "bob")
	if code != 0 || !strings.Contains(out, "<public URL>/gui/#reset?token=") || !strings.Contains(out, "No mail was sent") {
		t.Fatalf("reset-link: %d %s %s", code, out, errOut)
	}
	env[EnvPublicURL] = "https://shf.example.com"
	env[EnvGUIBase] = "/"
	code, out, _ = run(t, env, "user", "reset-link", "bob")
	m := linkRE.FindStringSubmatch(out)
	if code != 0 || m == nil || m[1] != "https://shf.example.com/" {
		t.Fatalf("reset-link: %d\n%s", code, out)
	}
	code, out, _ = run(t, env, "user", "reset-link", "bob", "--gui-base", "/gui")
	m2 := linkRE.FindStringSubmatch(out)
	if code != 0 || m2 == nil || m2[1] != "https://shf.example.com/gui/" {
		t.Fatalf("reset-link --gui-base: %d\n%s", code, out)
	}
	if code, _, errOut := run(t, env, "user", "reset-link", "bob", "--public-url", "shf.example.com"); code != 2 {
		t.Errorf("bad public URL: %d %s", code, errOut)
	}

	srv, err := server.Open(root, server.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()
	// The newer link voided the older one.
	if _, err := srv.ResetWithToken(context.Background(), server.ResetRequest{Token: m[2], NewPassword: "nova-bobova-lozinka"}); server.CodeOf(err) != server.CodeInvalidResetToken {
		t.Errorf("older link: %v", err)
	}
	if _, err := srv.ResetWithToken(context.Background(), server.ResetRequest{Token: m2[2], NewPassword: "nova-bobova-lozinka"}); err != nil {
		t.Errorf("newest link: %v", err)
	}
}

func TestMailTest(t *testing.T) {
	fb := &cliBrevo{}
	ts := httptest.NewServer(fb)
	defer ts.Close()
	env := map[string]string{EnvBrevoURL: ts.URL}
	if code, _, errOut := run(t, env, "mail", "test", "igor@example.com"); code != 2 || !strings.Contains(errOut, EnvBrevoKeyFile) {
		t.Errorf("without a key: %d %s", code, errOut)
	}
	env[EnvBrevoKeyFile] = keyFile(t)
	code, out, errOut := run(t, env, "mail", "test", "igor@example.com")
	if code != 0 || !strings.Contains(out, "sent a test mail to igor@example.com from \"shared-storage\" <noreply@digitalcube.rs>") {
		t.Fatalf("mail test: %d %s %s", code, out, errOut)
	}
	if len(fb.mails) != 1 || fb.keys[0] != cliKey {
		t.Fatalf("brevo got %d mails, key %v", len(fb.mails), fb.keys)
	}
	if strings.Contains(out+errOut, cliKey) {
		t.Errorf("output shows the key")
	}
	code, _, _ = run(t, env, "mail", "test", "igor@example.com", "--mail-from", "Tim <tim@example.com>")
	if code != 0 || fb.mails[1]["sender"].(map[string]any)["email"] != "tim@example.com" {
		t.Errorf("--mail-from: %d %v", code, fb.mails[1]["sender"])
	}
	if code, _, _ := run(t, env, "mail", "test", "not-an-address"); code != 2 {
		t.Errorf("bad address: %d", code)
	}
	if code, _, _ := run(t, env, "mail"); code != 2 {
		t.Errorf("mail alone: %d", code)
	}
}

// serve says whether mail is on, in its log and in /healthz, and never
// shows the key.
func TestServeMailStatus(t *testing.T) {
	kf := keyFile(t)
	for _, c := range []struct {
		name string
		env  map[string]string
		log  string
		mail string
	}{
		{"no key", map[string]string{}, "mail: disabled (no --brevo-key-file", "disabled"},
		{"no public URL", map[string]string{EnvBrevoKeyFile: kf}, "mail: disabled (no --public-url", "disabled"},
		{"on", map[string]string{EnvBrevoKeyFile: kf, EnvPublicURL: "https://shf.example.com"}, "mail: enabled", "enabled"},
	} {
		t.Run(c.name, func(t *testing.T) {
			c.env[EnvDataRoot] = t.TempDir()
			ctx, cancel := context.WithCancel(context.Background())
			ready := make(chan string, 1)
			done := make(chan int, 1)
			var stderr bytes.Buffer
			go func() {
				done <- Run([]string{"serve", "--listen", "127.0.0.1:0"}, Env{
					Stdout: &bytes.Buffer{}, Stderr: &stderr, Ctx: ctx,
					Getenv: func(k string) string { return c.env[k] },
					Ready:  func(addr string) { ready <- addr },
				})
			}()
			var addr string
			select {
			case addr = <-ready:
			case code := <-done:
				t.Fatalf("serve exited %d: %s", code, stderr.String())
			case <-time.After(10 * time.Second):
				t.Fatal("serve did not start")
			}
			resp, err := http.Get("http://" + addr + "/healthz")
			if err != nil {
				t.Fatal(err)
			}
			b, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			cancel()
			<-done
			if !strings.Contains(string(b), `"mail":"`+c.mail+`"`) {
				t.Errorf("healthz: %s", b)
			}
			log := stderr.String()
			if !strings.Contains(log, c.log) || !strings.Contains(log, "mail="+c.mail) {
				t.Errorf("log has no %q:\n%s", c.log, log)
			}
			if strings.Contains(log, cliKey) {
				t.Errorf("log shows the key:\n%s", log)
			}
		})
	}
	// A key file that cannot be read stops serve, without its content.
	env := map[string]string{EnvDataRoot: t.TempDir(), EnvBrevoKeyFile: filepath.Join(t.TempDir(), "missing")}
	if code, _, errOut := run(t, env, "serve", "--listen", "127.0.0.1:0"); code != 1 || !strings.Contains(errOut, "brevo key file") {
		t.Errorf("missing key file: %d %s", code, errOut)
	}
}
