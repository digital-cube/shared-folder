package shfdcli

import (
	"bytes"
	"context"
	"net/http"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/digital-cube/shared-folder/internal/server"
)

func run(t *testing.T, env map[string]string, args ...string) (code int, stdout, stderr string) {
	t.Helper()
	var o, e bytes.Buffer
	code = Run(args, Env{Stdout: &o, Stderr: &e, Getenv: func(k string) string { return env[k] }})
	return code, o.String(), e.String()
}

var (
	idRE    = regexp.MustCompile(`share-id: (\S+)`)
	tokenRE = regexp.MustCompile(`token:\s+(shf_\S+)`)
)

func TestShareAndMemberCommands(t *testing.T) {
	root := t.TempDir()
	env := map[string]string{EnvDataRoot: root}

	code, out, errOut := run(t, env, "share", "create", "tim")
	if code != 0 {
		t.Fatalf("share create: %d %s", code, errOut)
	}
	id := idRE.FindStringSubmatch(out)
	tok := tokenRE.FindStringSubmatch(out)
	if id == nil || tok == nil || !strings.Contains(out, "admin:    admin") {
		t.Fatalf("share create output:\n%s", out)
	}

	// --data-root flag wins over the environment, and flags may follow
	// positional arguments.
	code, out, errOut = run(t, map[string]string{EnvDataRoot: "/nonexistent"}, "member", "add", id[1], "bob", "--admin", "--data-root", root)
	if code != 0 || tokenRE.FindStringSubmatch(out) == nil {
		t.Fatalf("member add: %d\n%s%s", code, out, errOut)
	}
	if code, _, _ = run(t, env, "member", "add", id[1], "bob"); code != 1 {
		t.Errorf("duplicate member add exit = %d", code)
	}
	run(t, env, "member", "add", id[1], "cecilija")
	if code, _, errOut = run(t, env, "member", "revoke", id[1], "cecilija"); code != 0 {
		t.Fatalf("revoke: %s", errOut)
	}

	code, out, _ = run(t, env, "member", "list", id[1])
	if code != 0 {
		t.Fatal(out)
	}
	for _, want := range []*regexp.Regexp{
		regexp.MustCompile(`(?m)^admin\s+admin\s+active`),
		regexp.MustCompile(`(?m)^bob\s+admin\s+active`),
		regexp.MustCompile(`(?m)^cecilija\s+member\s+revoked`),
	} {
		if !want.MatchString(out) {
			t.Errorf("member list misses %s:\n%s", want, out)
		}
	}
	if strings.Contains(out, tok[1]) {
		t.Error("member list shows a token")
	}

	code, out, _ = run(t, env, "share", "list")
	if code != 0 || !strings.Contains(out, id[1]) || !strings.Contains(out, "tim") {
		t.Errorf("share list:\n%s", out)
	}

	// Usage errors exit 2.
	for _, args := range [][]string{
		{"nepoznato"}, {"member", "add", id[1]}, {"share"}, {"member", "list"},
	} {
		if code, _, _ := run(t, env, args...); code != 2 {
			t.Errorf("%v exit = %d, want 2", args, code)
		}
	}
	if code, _, _ := run(t, nil, "share", "list"); code != 2 {
		t.Errorf("missing data-root exit = %d, want 2", code)
	}
	if code, _, _ := run(t, env, "member", "list", "00000000-0000-4000-8000-000000000000"); code != 1 {
		t.Errorf("unknown share exit = %d, want 1", code)
	}
}

func TestServe(t *testing.T) {
	root := t.TempDir()
	env := map[string]string{EnvDataRoot: root}
	_, out, _ := run(t, env, "share", "create", "tim", "--admin", "ana")
	id := idRE.FindStringSubmatch(out)[1]
	tok := tokenRE.FindStringSubmatch(out)[1]

	ctx, cancel := context.WithCancel(context.Background())
	ready := make(chan string, 1)
	done := make(chan int, 1)
	var stderr bytes.Buffer
	go func() {
		done <- Run([]string{"serve", "--listen", "127.0.0.1:0"}, Env{
			Stdout: &bytes.Buffer{}, Stderr: &stderr, Ctx: ctx,
			Getenv: func(k string) string { return env[k] },
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

	// A second server on the same data-root is refused.
	if code, _, errOut := run(t, env, "serve", "--listen", "127.0.0.1:0"); code != 1 || !strings.Contains(errOut, "already served") {
		t.Errorf("second serve: %d %s", code, errOut)
	}

	req, _ := http.NewRequest("GET", "http://"+addr+"/v1/shares/"+id+"/changes?since=0", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Errorf("changes status %d", resp.StatusCode)
	}

	// Members can be added while the server runs, and work at once.
	_, out, _ = run(t, env, "member", "add", id, "bob")
	req.Header.Set("Authorization", "Bearer "+tokenRE.FindStringSubmatch(out)[1])
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Errorf("new member status %d", resp.StatusCode)
	}

	cancel()
	select {
	case code := <-done:
		if code != 0 {
			t.Errorf("serve exit %d: %s", code, stderr.String())
		}
	case <-time.After(10 * time.Second):
		t.Fatal("serve did not stop")
	}

	if code, _, _ := run(t, env, "serve", "--tls-cert", "x"); code != 2 {
		t.Errorf("tls-cert without key exit = %d", code)
	}
}

func TestMemberSetRoleResetTokenReactivate(t *testing.T) {
	root := t.TempDir()
	env := map[string]string{EnvDataRoot: root}
	_, out, _ := run(t, env, "share", "create", "tim")
	id := idRE.FindStringSubmatch(out)[1]
	_, out, _ = run(t, env, "member", "add", id, "marko")
	oldTok := tokenRE.FindStringSubmatch(out)[1]

	// set-role up and down; the last admin keeps the role.
	code, out, errOut := run(t, env, "member", "set-role", id, "marko", "admin")
	if code != 0 || !strings.Contains(out, "marko: role member -> admin") {
		t.Fatalf("set-role up: %d %q %q", code, out, errOut)
	}
	if code, out, _ = run(t, env, "member", "set-role", id, "admin", "member"); code != 0 || !strings.Contains(out, "admin: role admin -> member") {
		t.Fatalf("set-role down: %d %q", code, out)
	}
	if code, _, errOut = run(t, env, "member", "set-role", id, "marko", "member"); code != 1 || !strings.Contains(errOut, "last_admin") {
		t.Errorf("set-role of the last admin: %d %q", code, errOut)
	}
	if code, _, _ = run(t, env, "member", "set-role", id, "marko", "owner"); code != 2 {
		t.Errorf("set-role with a bad role: %d", code)
	}
	if code, _, errOut = run(t, env, "member", "set-role", id, "niko", "admin"); code != 1 || !strings.Contains(errOut, "unknown_member") {
		t.Errorf("set-role of an unknown member: %d %q", code, errOut)
	}

	// reset-token prints a new token in the format of member add.
	code, out, errOut = run(t, env, "member", "reset-token", id, "marko")
	tok := tokenRE.FindStringSubmatch(out)
	if code != 0 || tok == nil || tok[1] == oldTok || !strings.Contains(out, "member: marko\n") {
		t.Fatalf("reset-token: %d %q %q", code, out, errOut)
	}
	srv, err := server.Open(root, server.Options{})
	if err != nil {
		t.Fatal(err)
	}
	sh, _ := srv.Share(id)
	if _, err := sh.Authenticate(oldTok); server.CodeOf(err) != server.CodeUnauthorized {
		t.Errorf("old token after reset: %v", err)
	}
	m, err := sh.Authenticate(tok[1])
	if err != nil {
		t.Fatalf("new token: %v", err)
	}
	srv.Close()

	// add over an active name, revoke, reset-token of a revoked member.
	if code, _, errOut = run(t, env, "member", "add", id, "marko"); code != 1 || !strings.Contains(errOut, "member_exists") {
		t.Errorf("add over an active member: %d %q", code, errOut)
	}
	run(t, env, "member", "set-role", id, "admin", "admin")
	run(t, env, "member", "revoke", id, "marko")
	if code, _, errOut = run(t, env, "member", "reset-token", id, "marko"); code != 1 || !strings.Contains(errOut, "member add") {
		t.Errorf("reset-token of a revoked member: %d %q", code, errOut)
	}

	// add brings the revoked member back.
	code, out, errOut = run(t, env, "member", "add", id, "marko")
	newTok := tokenRE.FindStringSubmatch(out)
	if code != 0 || newTok == nil || !strings.Contains(out, "member: marko (reactivated)") {
		t.Fatalf("reactivate: %d %q %q", code, out, errOut)
	}
	srv, _ = server.Open(root, server.Options{})
	defer srv.Close()
	sh, _ = srv.Share(id)
	back, err := sh.Authenticate(newTok[1])
	if err != nil || back.ID != m.ID || back.Role != server.RoleMember {
		t.Errorf("reactivated: %+v %v (was id %d)", back, err, m.ID)
	}
	_, out, _ = run(t, env, "member", "list", id)
	if !regexp.MustCompile(`(?m)^marko\s+member\s+active`).MatchString(out) {
		t.Errorf("member list:\n%s", out)
	}
}
