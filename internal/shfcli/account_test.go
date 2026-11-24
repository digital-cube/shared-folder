package shfcli_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/digital-cube/shared-folder/internal/client"
	"github.com/digital-cube/shared-folder/internal/password"
	"github.com/digital-cube/shared-folder/internal/server"
	"github.com/digital-cube/shared-folder/internal/server/httpapi"
	"github.com/digital-cube/shared-folder/internal/shfcli"
	"github.com/digital-cube/shared-folder/internal/transport"
)

// acctEnv is a real shfd in process with accounts (cheap password
// hashing) and a client state home; files move through the local
// transport.
type acctEnv struct {
	t    *testing.T
	root string
	srv  *server.Server
	url  string
	home string
}

func newAcctEnv(t *testing.T) *acctEnv {
	t.Helper()
	e := &acctEnv{t: t, root: t.TempDir(), home: t.TempDir()}
	p := password.Params{Memory: 64, Time: 1, Threads: 1, SaltLen: 16, KeyLen: 32}
	srv, err := server.Open(e.root, server.Options{PasswordParams: &p})
	if err != nil {
		t.Fatal(err)
	}
	api := httpapi.New(srv, nil)
	hs := httptest.NewServer(api)
	t.Cleanup(func() { api.Close(); hs.Close(); srv.Close() })
	e.srv, e.url = srv, hs.URL
	return e
}

// run runs shf with stdin.
func (e *acctEnv) run(stdin string, args ...string) (int, string) {
	e.t.Helper()
	var out, errb bytes.Buffer
	code := shfcli.Run(args, shfcli.Env{
		Stdout: &out, Stderr: &errb, Ctx: context.Background(), Stdin: strings.NewReader(stdin),
		Getenv:  func(k string) string { return map[string]string{client.EnvStateHome: e.home}[k] },
		Options: client.Options{Transport: transport.Local{DataRoot: e.root}},
	})
	return code, out.String() + errb.String()
}

func (e *acctEnv) must(stdin string, args ...string) string {
	e.t.Helper()
	code, out := e.run(stdin, args...)
	if code != 0 {
		e.t.Fatalf("shf %s: exit %d\n%s", strings.Join(args, " "), code, out)
	}
	return out
}

func (e *acctEnv) share(name string) (string, *server.Share) {
	e.t.Helper()
	id, _, err := e.srv.CreateShare(name, "admin")
	if err != nil {
		e.t.Fatal(err)
	}
	sh, err := e.srv.Share(id)
	if err != nil {
		e.t.Fatal(err)
	}
	return id, sh
}

// user makes an account and returns its temporary password.
func (e *acctEnv) user(name string) string {
	e.t.Helper()
	_, temp, err := e.srv.AddUser(name, "")
	if err != nil {
		e.t.Fatal(err)
	}
	return temp
}

func (e *acctEnv) member(sh *server.Share, user string) {
	e.t.Helper()
	if _, _, err := e.srv.AddUserMember(sh, user, false); err != nil {
		e.t.Fatal(err)
	}
}

func (e *acctEnv) config(folder string) *client.Config {
	e.t.Helper()
	c, err := client.Find(e.home, folder)
	if err != nil {
		e.t.Fatal(err)
	}
	return c
}

func (e *acctEnv) stored(share, p string) string {
	b, _ := os.ReadFile(filepath.Join(e.root, share, "storage", filepath.FromSlash(p)))
	return string(b)
}

const bobPW = "bobova-nova-lozinka"

// TestLoginFoldersJoinLogout: shf login with a temporary password,
// shf folders, shf join by name and by id, shf logout, the folders signed
// out, and shf login again.
func TestLoginFoldersJoinLogout(t *testing.T) {
	e := newAcctEnv(t)
	tim, timSh := e.share("tim")
	docs, docsSh := e.share("docs")
	e.share("other") // not bob's
	temp := e.user("bob")
	e.member(timSh, "bob")
	e.member(docsSh, "bob")

	// no account yet: join by name says what to do
	if code, out := e.run("", "join", e.url, "tim", filepath.Join(t.TempDir(), "x")); code != 1 || !strings.Contains(out, "shf login "+e.url) {
		t.Errorf("join without an account: %d\n%s", code, out)
	}
	if code, out := e.run("", "folders"); code != 1 || !strings.Contains(out, "shf login") {
		t.Errorf("folders without an account: %d\n%s", code, out)
	}

	// a temporary password must be replaced right away; a short one is refused
	if code, out := e.run(temp+"\nshort\n", "login", e.url, "--user", "bob", "--password-stdin", "--device-name", "mac"); code != 1 ||
		!strings.Contains(out, "the new password was refused") {
		t.Fatalf("login with a short new password: %d\n%s", code, out)
	}
	if _, err := client.LoadAccount(e.home, e.url); err == nil {
		t.Error("a sign-in whose temporary password was not replaced was saved")
	}
	out := e.must(temp+"\n"+bobPW+"\n", "login", e.url, "--user", "bob", "--password-stdin", "--device-name", "mac")
	if !strings.Contains(out, "temporary password") || !strings.Contains(out, "password changed") ||
		!regexp.MustCompile(`signed in to \S+ as bob \(device mac, id \d+\)`).MatchString(out) {
		t.Errorf("login:\n%s", out)
	}
	host := strings.TrimPrefix(e.url, "http://")
	af := filepath.Join(e.home, "accounts", host+".json")
	fi, err := os.Stat(af)
	if err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("account file: %v %v", fi, err)
	}
	var saved map[string]any
	b, _ := os.ReadFile(af)
	json.Unmarshal(b, &saved)
	for _, k := range []string{"url", "username", "device_id", "token"} {
		if saved[k] == nil || saved[k] == "" || saved[k] == 0.0 {
			t.Errorf("account file has no %s: %s", k, b)
		}
	}
	if !strings.HasPrefix(saved["token"].(string), "shd_") {
		t.Errorf("account token %v", saved["token"])
	}

	// the password from the terminal (ReadPassword), the username asked on stdin
	var outb bytes.Buffer
	code := shfcli.Run([]string{"login", e.url, "--device-name", "mac"}, shfcli.Env{
		Stdout: &outb, Stderr: &outb, Ctx: context.Background(), Stdin: strings.NewReader("bob\n"),
		Getenv:       func(k string) string { return map[string]string{client.EnvStateHome: e.home}[k] },
		ReadPassword: func(prompt string) (string, error) { return bobPW, nil },
	})
	if code != 0 || !strings.Contains(outb.String(), "username: ") || !strings.Contains(outb.String(), "signed in to") {
		t.Fatalf("login on a terminal: %d\n%s", code, outb.String())
	}

	out = e.must("", "folders")
	for _, re := range []string{`(?m)^\s+tim\s+member\s+` + tim + `\s+no$`, `(?m)^\s+docs\s+member\s+` + docs + `\s+no$`} {
		if !regexp.MustCompile(re).MatchString(out) {
			t.Errorf("folders: no %s:\n%s", re, out)
		}
	}
	if strings.Contains(out, "other") {
		t.Errorf("folders lists a folder bob is not a member of:\n%s", out)
	}

	// join by name
	timDir := filepath.Join(t.TempDir(), "tim")
	os.MkdirAll(timDir, 0o755)
	os.WriteFile(filepath.Join(timDir, "a.txt"), []byte("from bob"), 0o644)
	out = e.must("", "join", e.url, "tim", timDir)
	if !strings.Contains(out, "joined "+timDir+" to share "+tim) || !strings.Contains(out, "account: bob (member bob, device mac)") {
		t.Errorf("join by name:\n%s", out)
	}
	if got := e.stored(tim, "a.txt"); got != "from bob" {
		t.Errorf("a.txt on the server: %q", got)
	}
	cfg := e.config(timDir)
	if cfg.Account != "bob" || cfg.DeviceID == 0 || cfg.Token == "" || strings.HasPrefix(cfg.Token, "shd_") {
		t.Errorf("config of a folder joined by account: %+v", cfg)
	}
	// the folder token is this device's credential in the folder
	if m, err := e.srv.AuthenticateFolder(timSh, cfg.Token); err != nil || m.Username != "bob" || m.DeviceID != cfg.DeviceID {
		t.Errorf("folder token: %+v %v", m, err)
	}

	// join by id
	docsDir := filepath.Join(t.TempDir(), "docs")
	out = e.must("", "join", e.url, docs, docsDir)
	if !strings.Contains(out, "joined "+docsDir+" to share "+docs) {
		t.Errorf("join by id:\n%s", out)
	}
	if code, out := e.run("", "join", e.url, "nope", filepath.Join(t.TempDir(), "nope")); code != 1 || !strings.Contains(out, `has no folder "nope"`) {
		t.Errorf("join of an unknown name: %d\n%s", code, out)
	}
	if out := e.must("", "folders", e.url); !strings.Contains(out, timDir) || !strings.Contains(out, docsDir) {
		t.Errorf("folders after the joins:\n%s", out)
	}
	if out := e.must("", "status", timDir); !regexp.MustCompile(`account:\s+bob, member bob, device mac \(id \d+\)`).MatchString(out) ||
		!strings.Contains(out, "state:      ok") {
		t.Errorf("status:\n%s", out)
	}

	// logout: the device is revoked, the folders stay but are signed out
	out = e.must("", "logout", e.url)
	if !strings.Contains(out, "signed out of "+e.url) || !strings.Contains(out, timDir) || !strings.Contains(out, docsDir) {
		t.Errorf("logout:\n%s", out)
	}
	if _, err := os.Stat(af); !os.IsNotExist(err) {
		t.Errorf("account file after logout: %v", err)
	}
	if _, err := e.srv.AuthenticateFolder(timSh, cfg.Token); err == nil {
		t.Error("the folder token works after logout")
	}
	if _, err := os.Stat(filepath.Join(timDir, "a.txt")); err != nil {
		t.Errorf("the folder's files after logout: %v", err)
	}
	if out := e.must("", "status", timDir); !strings.Contains(out, "state:      signed out") || !strings.Contains(out, "shf login "+e.url) {
		t.Errorf("status after logout:\n%s", out)
	}
	if code, out := e.run("", "sync", timDir); code != 1 || !strings.Contains(out, "shf login "+e.url) {
		t.Errorf("sync after logout: %d\n%s", code, out)
	}
	if code, out := e.run("", "logout", e.url); code != 1 || !strings.Contains(out, "not signed in") {
		t.Errorf("logout twice: %d\n%s", code, out)
	}

	// signing in again gives both folders the new device's credentials
	out = e.must(bobPW+"\n", "login", e.url, "--user", "bob", "--password-stdin", "--device-name", "mac")
	if strings.Count(out, "signed in again") != 2 {
		t.Errorf("login again:\n%s", out)
	}
	os.WriteFile(filepath.Join(timDir, "b.txt"), []byte("after"), 0o644)
	e.must("", "sync", timDir)
	if got := e.stored(tim, "b.txt"); got != "after" {
		t.Errorf("b.txt after signing in again: %q", got)
	}
	if out := e.must("", "status", timDir); !strings.Contains(out, "state:      ok") {
		t.Errorf("status after signing in again:\n%s", out)
	}
}

// TestLoginRefused: a wrong password, and the pause after several.
func TestLoginRefused(t *testing.T) {
	e := newAcctEnv(t)
	e.user("eve")
	code, out := e.run("wrong-password-1\n", "login", e.url, "--user", "eve", "--password-stdin")
	if code != 1 || !strings.Contains(out, "wrong username or password") {
		t.Fatalf("wrong password: %d\n%s", code, out)
	}
	if code, out := e.run("x\n", "login", e.url, "--user", "nobody", "--password-stdin"); code != 1 || !strings.Contains(out, "wrong username or password") {
		t.Errorf("unknown account: %d\n%s", code, out)
	}
	paused := regexp.MustCompile(`too many failed sign-ins; the server accepts the next attempt in \d+`)
	for i := 0; i < 10 && !paused.MatchString(out); i++ {
		_, out = e.run("wrong-password-1\n", "login", e.url, "--user", "eve", "--password-stdin")
	}
	if !paused.MatchString(out) {
		t.Errorf("no pause after repeated failures:\n%s", out)
	}
	if code, out := e.run("", "login", e.url, "--password-stdin"); code != 2 || !strings.Contains(out, "--password-stdin needs --user") {
		t.Errorf("--password-stdin without --user: %d\n%s", code, out)
	}
}

// TestLoginMigratesLegacyFolder: a folder joined with a member's token
// moves to the account at shf login: same folder, same state, nothing
// fetched again; the server keeps the legacy token.
func TestLoginMigratesLegacyFolder(t *testing.T) {
	e := newAcctEnv(t)
	tim, timSh := e.share("tim")
	legacy, err := timSh.AddMember("ana", false)
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(t.TempDir(), "tim")
	os.MkdirAll(dir, 0o755)
	for _, f := range []string{"a.txt", "b.txt"} {
		os.WriteFile(filepath.Join(dir, f), []byte(f), 0o644)
	}
	e.must("", "join", e.url, tim, legacy, dir)
	if out := e.must("", "status", dir); !strings.Contains(out, "account:    none: joined with a member's token") {
		t.Errorf("status of a legacy folder:\n%s", out)
	}
	before := e.config(dir)
	st, _ := os.Stat(filepath.Join(dir, "a.txt"))

	temp := e.user("ana")
	if _, err := e.srv.LinkMember(timSh, "ana", "ana"); err != nil {
		t.Fatal(err)
	}
	const pw = "anina-nova-lozinka"
	// asked, and declined
	out := e.must(temp+"\n"+pw+"\nn\n", "login", e.url, "--user", "ana", "--password-stdin")
	if !strings.Contains(out, "was joined with member ana's own token") || !strings.Contains(out, "left on their member's token: "+dir) {
		t.Errorf("login, declined:\n%s", out)
	}
	if c := e.config(dir); c.Account != "" || c.Token != legacy {
		t.Errorf("declined, yet the folder changed: %+v", c)
	}
	// asked, and accepted
	out = e.must(pw+"\ny\n", "login", e.url, "--user", "ana", "--password-stdin")
	if !strings.Contains(out, dir+": moved to the account") {
		t.Errorf("login, accepted:\n%s", out)
	}
	after := e.config(dir)
	if after.Account != "ana" || after.Member != "ana" || after.DeviceID == 0 || after.Token == legacy || after.Dir != before.Dir ||
		!after.Joined.Equal(before.Joined) {
		t.Errorf("config after the move: %+v (before %+v)", after, before)
	}
	if b, _ := os.ReadFile(filepath.Join(after.Dir, "token")); strings.TrimSpace(string(b)) == legacy {
		t.Error("the legacy token is still on disk")
	}
	// the server keeps the legacy token until an admin revokes it
	if m, err := e.srv.AuthenticateFolder(timSh, legacy); err != nil || m.Username != "ana" {
		t.Errorf("legacy token after the move: %v %v", m, err)
	}
	// the same folder goes on: nothing is fetched again
	out = e.must("", "sync", dir)
	if !strings.Contains(out, "sync: sent 0, received 0, conflicts 0") {
		t.Errorf("first sync after the move:\n%s", out)
	}
	if st2, _ := os.Stat(filepath.Join(dir, "a.txt")); !os.SameFile(st, st2) || !st.ModTime().Equal(st2.ModTime()) {
		t.Error("a.txt was replaced by the move")
	}
	os.WriteFile(filepath.Join(dir, "c.txt"), []byte("after the move"), 0o644)
	e.must("", "sync", dir)
	if got := e.stored(tim, "c.txt"); got != "after the move" {
		t.Errorf("c.txt: %q", got)
	}
	if out := e.must("", "status", dir); !strings.Contains(out, "account:    ana, member ana") {
		t.Errorf("status after the move:\n%s", out)
	}
	// nothing left to offer
	if out := e.must(pw+"\n", "login", e.url, "--user", "ana", "--password-stdin"); strings.Contains(out, "Move it") {
		t.Errorf("login offers a moved folder again:\n%s", out)
	}
	if out := e.must(pw+"\n", "login", e.url, "--user", "ana", "--password-stdin"); !strings.Contains(out, "signed in again") {
		t.Errorf("a new sign-in does not take the folder along:\n%s", out)
	}
}

// TestRevokedDeviceSignsOut: a device revoked on the server is signed out
// for shf sync and shf status, until shf login.
func TestRevokedDeviceSignsOut(t *testing.T) {
	e := newAcctEnv(t)
	_, timSh := e.share("tim")
	temp := e.user("bob")
	e.member(timSh, "bob")
	e.must(temp+"\n"+bobPW+"\n", "login", e.url, "--user", "bob", "--password-stdin", "--device-name", "mac")
	dir := filepath.Join(t.TempDir(), "tim")
	e.must("", "join", e.url, "tim", dir)
	cfg := e.config(dir)
	u, err := e.srv.UserByName("bob")
	if err != nil {
		t.Fatal(err)
	}
	if err := e.srv.RevokeDevice(u.ID, cfg.DeviceID); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(dir, "a.txt"), []byte("a"), 0o644)
	code, out := e.run("", "sync", dir)
	if code != 1 || !strings.Contains(out, "signed out") || !strings.Contains(out, "shf login "+e.url) {
		t.Errorf("sync with a revoked device: %d\n%s", code, out)
	}
	if out := e.must("", "status", dir); !regexp.MustCompile(`state:\s+signed out \(since [^)]+\): nothing is synchronized`).MatchString(out) {
		t.Errorf("status with a revoked device:\n%s", out)
	}
	e.must(bobPW+"\n", "login", e.url, "--user", "bob", "--password-stdin", "--device-name", "mac")
	e.must("", "sync", dir)
	if out := e.must("", "status", dir); !strings.Contains(out, "state:      ok") {
		t.Errorf("status after signing in again:\n%s", out)
	}
}
