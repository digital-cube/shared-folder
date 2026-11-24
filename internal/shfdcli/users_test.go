package shfdcli

import (
	"context"
	"database/sql"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/digital-cube/shared-folder/internal/server"
)

var tempRE = regexp.MustCompile(`temporary password: (\S+)`)

func TestUserCommands(t *testing.T) {
	root := t.TempDir()
	env := map[string]string{EnvDataRoot: root}
	_, out, _ := run(t, env, "share", "create", "tim", "--admin", "igor")
	id := idRE.FindStringSubmatch(out)[1]
	run(t, env, "member", "add", id, "marko")

	code, out, errOut := run(t, env, "user", "add", "bob", "--display-name", "Bob Bobić")
	temp := tempRE.FindStringSubmatch(out)
	if code != 0 || temp == nil || !strings.Contains(out, "shown only now") {
		t.Fatalf("user add: %d\n%s%s", code, out, errOut)
	}
	if code, _, errOut = run(t, env, "user", "add", "bob"); code != 1 || !strings.Contains(errOut, "already exists") {
		t.Errorf("user add twice: %d %s", code, errOut)
	}
	run(t, env, "user", "add", "marko")
	run(t, env, "user", "add", "cid")

	// member add --user and member link.
	if code, out, errOut = run(t, env, "member", "add", id, "bob", "--user", "--admin"); code != 0 || strings.Contains(out, "shf_") {
		t.Fatalf("member add --user: %d\n%s%s", code, out, errOut)
	}
	if code, _, errOut = run(t, env, "member", "add", id, "marko", "--user"); code != 1 || !strings.Contains(errOut, "member link") {
		t.Errorf("member add --user of a token member: %d %s", code, errOut)
	}
	if code, out, errOut = run(t, env, "member", "link", id, "marko", "marko"); code != 0 || !strings.Contains(out, "keep working") {
		t.Fatalf("member link: %d\n%s%s", code, out, errOut)
	}
	if code, _, _ = run(t, env, "member", "link", id, "marko", "cid"); code != 1 {
		t.Errorf("link to a second account: %d", code)
	}
	_, out, _ = run(t, env, "member", "list", id)
	for _, re := range []string{`(?m)^bob\s+admin\s.*\sbob\s+-$`, `(?m)^marko\s+member\s.*\smarko\s+-$`, `(?m)^igor\s+admin\s.*\s-\s+-$`} {
		if !regexp.MustCompile(re).MatchString(out) {
			t.Errorf("member list does not match %s:\n%s", re, out)
		}
	}

	// list and show.
	code, out, _ = run(t, env, "user", "list")
	if code != 0 || !regexp.MustCompile(`(?m)^bob\s+Bob Bobić\s+active \(temporary password\)\s+-\s+0$`).MatchString(out) {
		t.Errorf("user list:\n%s", out)
	}
	srv, err := server.Open(root, server.Options{})
	if err != nil {
		t.Fatal(err)
	}
	res, err := srv.Login(context.Background(), server.LoginRequest{Username: "bob", Password: temp[1], DeviceName: "macbook"})
	srv.Close()
	if err != nil {
		t.Fatal(err)
	}
	code, out, _ = run(t, env, "user", "show", "bob")
	if code != 0 || !regexp.MustCompile(`(?m)^`+id+`\s+tim\s+bob\s+admin$`).MatchString(out) ||
		!regexp.MustCompile(`(?m)^1\s+macbook\s+client\s.*\sactive$`).MatchString(out) {
		t.Errorf("user show:\n%s", out)
	}

	// disable signs the device out; enable does not bring it back.
	if code, out, _ = run(t, env, "user", "disable", "bob"); code != 0 || !strings.Contains(out, "signed out 1 device") {
		t.Errorf("disable: %d %s", code, out)
	}
	srv, _ = server.Open(root, server.Options{})
	if _, err := srv.AuthenticateDevice(res.Token); server.CodeOf(err) != server.CodeUnauthorized {
		t.Errorf("device after disable: %v", err)
	}
	srv.Close()
	_, out, _ = run(t, env, "user", "show", "bob")
	if !strings.Contains(out, "disabled") || !strings.Contains(out, "signed out") {
		t.Errorf("user show after disable:\n%s", out)
	}
	if code, _, _ = run(t, env, "user", "enable", "bob"); code != 0 {
		t.Error("enable")
	}

	// passwd: a new temporary password.
	code, out, _ = run(t, env, "user", "passwd", "bob")
	temp2 := tempRE.FindStringSubmatch(out)
	if code != 0 || temp2 == nil || temp2[1] == temp[1] {
		t.Fatalf("passwd:\n%s", out)
	}
	if code, _, errOut = run(t, env, "user", "passwd", "niko"); code != 1 || !strings.Contains(errOut, "no account") {
		t.Errorf("passwd of unknown: %d %s", code, errOut)
	}
	if code, _, _ = run(t, env, "user", "frobnicate"); code != 2 {
		t.Errorf("unknown subcommand: %d", code)
	}

	// verify repairs a lost row of user_shares.
	srv, _ = server.Open(root, server.Options{})
	u, _ := srv.UserByName("marko")
	srv.Close()
	dropUserShares(t, root, u.ID)
	if code, out, _ = run(t, env, "verify", id); code != 1 || !strings.Contains(out, "user_shares") {
		t.Errorf("verify: %d\n%s", code, out)
	}
	if code, out, _ = run(t, env, "verify", id, "--repair"); code != 0 {
		t.Errorf("verify --repair: %d\n%s", code, out)
	}
	if code, out, _ = run(t, env, "verify", id); code != 0 {
		t.Errorf("verify after repair: %d\n%s", code, out)
	}
}

func dropUserShares(t *testing.T, root string, userID int64) {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+filepath.Join(root, server.RegistryFile)+"?_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`DELETE FROM user_shares WHERE user_id = ?`, userID); err != nil {
		t.Fatal(err)
	}
}

func TestServeTrustedProxyFlag(t *testing.T) {
	env := map[string]string{EnvDataRoot: t.TempDir(), EnvTrustedProxies: "nonsense"}
	if code, _, errOut := run(t, env, "serve", "--listen", "127.0.0.1:0"); code != 2 || !strings.Contains(errOut, "--trusted-proxy") {
		t.Errorf("bad $%s: %d %s", EnvTrustedProxies, code, errOut)
	}
	if code, _, errOut := run(t, env, "serve", "--listen", "127.0.0.1:0", "--trusted-proxy", "10.0.0.0/33"); code != 2 {
		t.Errorf("bad --trusted-proxy: %d %s", code, errOut)
	}
}
