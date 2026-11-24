package httpapi_test

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"io"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/digital-cube/shared-folder/internal/password"
	"github.com/digital-cube/shared-folder/internal/server"
	"github.com/digital-cube/shared-folder/internal/server/httpapi"
)

var cheapPW = password.Params{Memory: 64, Time: 1, Threads: 1, SaltLen: 16, KeyLen: 32}

func newAcctEnv(t *testing.T) *env {
	return newEnv(t, func(o *server.Options) { p := cheapPW; o.PasswordParams = &p })
}

// raw sends a request to a path of the API (not of the folder) with an
// optional bearer token and extra headers; it returns the status, the
// headers and the body.
func (e *env) call(method, path, token string, in any, hdr ...string) (int, http.Header, []byte) {
	e.t.Helper()
	var body io.Reader
	if in != nil {
		b, _ := json.Marshal(in)
		body = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, e.http.URL+path, body)
	if err != nil {
		e.t.Fatal(err)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	for i := 0; i+1 < len(hdr); i += 2 {
		req.Header.Add(hdr[i], hdr[i+1])
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		e.t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, resp.Header, b
}

func errCode(b []byte) server.Code {
	var eb httpapi.ErrorBody
	json.Unmarshal(b, &eb)
	return eb.Error.Code
}

// account makes an account with a password it has already changed.
func (e *env) account(name, pw string) {
	e.t.Helper()
	_, temp, err := e.srv.AddUser(name, "")
	if err != nil {
		e.t.Fatal(err)
	}
	tok := e.loginOK(name, temp, "gui")
	if st, _, b := e.call("POST", "/v1/me/password", tok, httpapi.PasswordRequest{Old: temp, New: pw}); st != 200 {
		e.t.Fatalf("password change: %d %s", st, b)
	}
	e.call("POST", "/v1/auth/logout", tok, nil)
}

func (e *env) loginOK(name, pw, kind string) string {
	e.t.Helper()
	st, _, b := e.call("POST", "/v1/auth/login", "", server.LoginRequest{Username: name, Password: pw, DeviceName: "test-" + kind, Kind: kind})
	if st != 200 {
		e.t.Fatalf("login %s: %d %s", name, st, b)
	}
	var res server.LoginResult
	json.Unmarshal(b, &res)
	return res.Token
}

func TestLoginMeAndMustChange(t *testing.T) {
	e := newAcctEnv(t)
	_, temp, err := e.srv.AddUser("bob", "Bob B.")
	if err != nil {
		t.Fatal(err)
	}
	sh, _ := e.srv.Share(e.share)
	if _, _, err := e.srv.AddUserMember(sh, "bob", true); err != nil {
		t.Fatal(err)
	}

	// Wrong password, unknown account: the same 401.
	for _, c := range [][2]string{{"bob", "pogrešna-lozinka"}, {"niko", temp}} {
		st, _, b := e.call("POST", "/v1/auth/login", "", server.LoginRequest{Username: c[0], Password: c[1]})
		if st != 401 || errCode(b) != server.CodeInvalidCredentials {
			t.Fatalf("login %v: %d %s", c, st, b)
		}
	}
	st, hdr, b := e.call("POST", "/v1/auth/login", "", server.LoginRequest{Username: "bob", Password: temp, DeviceName: "laptop", Kind: "gui"})
	var res server.LoginResult
	if json.Unmarshal(b, &res); st != 200 || !res.MustChange || res.User.Username != "bob" || res.User.DisplayName != "Bob B." || hdr.Get("Cache-Control") != "no-store" {
		t.Fatalf("login: %d %s", st, b)
	}
	tok := res.Token

	// With a temporary password only me, me/password and logout answer.
	if st, _, b := e.call("GET", "/v1/me", tok, nil); st != 200 || !strings.Contains(string(b), `"must_change":true`) {
		t.Errorf("me: %d %s", st, b)
	}
	for _, p := range []string{"/v1/shares/" + e.share + "/admin/files", "/v1/shares/" + e.share + "/changes", "/v1/users?q=a"} {
		if st, _, b := e.call("GET", p, tok, nil); st != 403 || errCode(b) != server.CodePasswordChangeRequired {
			t.Errorf("%s while must_change: %d %s", p, st, b)
		}
	}
	if st, _, b := e.call("POST", "/v1/me/password", tok, httpapi.PasswordRequest{Old: temp, New: "bob"}); st != 400 || errCode(b) != server.CodeInvalidPassword {
		t.Errorf("weak new password: %d %s", st, b)
	}
	if st, _, b := e.call("POST", "/v1/me/password", tok, httpapi.PasswordRequest{Old: "pogrešna-stara", New: "nova-lozinka-bob"}); st != 403 || errCode(b) != server.CodeWrongPassword {
		t.Errorf("wrong old password: %d %s", st, b)
	}
	if st, _, b := e.call("POST", "/v1/me/password", tok, httpapi.PasswordRequest{Old: temp, New: "nova-lozinka-bob"}); st != 200 {
		t.Fatalf("password change: %d %s", st, b)
	}

	// Now the token works on the folder, as member bob.
	if st, _, b := e.call("GET", "/v1/shares/"+e.share+"/admin/files", tok, nil); st != 200 {
		t.Errorf("admin/files: %d %s", st, b)
	}
	cli := e.loginOK("bob", "nova-lozinka-bob", "client")
	var me httpapi.MeResponse
	_, _, b = e.call("GET", "/v1/me", tok, nil)
	if err := json.Unmarshal(b, &me); err != nil || me.User.MustChange || len(me.Folders) != 1 || len(me.Devices) != 2 {
		t.Fatalf("me: %s", b)
	}
	if f := me.Folders[0]; f.ShareID != e.share || f.Name != "tim" || f.Role != "admin" || f.Member != "bob" {
		t.Errorf("folder = %+v", f)
	}
	var current, other server.Device
	for _, d := range me.Devices {
		if d.Current {
			current = d
		} else {
			other = d
		}
	}
	if current.Name != "laptop" || current.Kind != "gui" || current.ExpiresAt == nil || other.Kind != "client" || other.ExpiresAt != nil {
		t.Errorf("devices = %+v", me.Devices)
	}
	if strings.Contains(string(b), "token") || strings.Contains(string(b), "pw_hash") {
		t.Errorf("me shows secrets: %s", b)
	}

	// DELETE /v1/me/devices/{id} signs the client out, not the GUI.
	if st, _, b := e.call("DELETE", "/v1/me/devices/"+strconv.FormatInt(other.ID, 10), tok, nil); st != 200 {
		t.Fatalf("revoke device: %d %s", st, b)
	}
	if st, _, _ := e.call("GET", "/v1/me", cli, nil); st != 401 {
		t.Errorf("revoked device: %d", st)
	}
	if st, _, b := e.call("DELETE", "/v1/me/devices/999", tok, nil); st != 404 || errCode(b) != server.CodeUnknownDevice {
		t.Errorf("unknown device: %d %s", st, b)
	}
	// Logout.
	if st, _, b := e.call("POST", "/v1/auth/logout", tok, nil); st != 200 {
		t.Fatalf("logout: %d %s", st, b)
	}
	for _, p := range []string{"/v1/me", "/v1/shares/" + e.share + "/admin/files"} {
		if st, _, _ := e.call("GET", p, tok, nil); st != 401 {
			t.Errorf("%s after logout: %d", p, st)
		}
	}
}

func TestDeviceTokenOnFolderRoutes(t *testing.T) {
	e := newAcctEnv(t)
	sh, _ := e.srv.Share(e.share)
	e.account("bob", "bobova-lozinka")
	e.account("cid", "cidova-lozinka")
	e.account("dan", "danova-lozinka")
	e.srv.AddUserMember(sh, "bob", true)
	e.srv.AddUserMember(sh, "cid", false)
	bob := e.loginOK("bob", "bobova-lozinka", "gui")
	cid := e.loginOK("cid", "cidova-lozinka", "client")
	dan := e.loginOK("dan", "danova-lozinka", "client")
	e.ana.mustPut("a.txt", "", []byte("ana"))

	files := "/v1/shares/" + e.share + "/admin/files"
	if st, _, b := e.call("GET", files, bob, nil); st != 200 || !strings.Contains(string(b), "a.txt") {
		t.Errorf("admin bob: %d %s", st, b)
	}
	if st, _, b := e.call("GET", "/v1/shares/"+e.share+"/admin/files/content?path=a.txt", bob, nil); st != 200 || string(b) != "ana" {
		t.Errorf("content: %d %s", st, b)
	}
	if st, _, b := e.call("GET", files, cid, nil); st != 403 || errCode(b) != server.CodeForbidden {
		t.Errorf("member cid on admin: %d %s", st, b)
	}
	if st, _, b := e.call("GET", "/v1/shares/"+e.share+"/changes", cid, nil); st != 200 {
		t.Errorf("member cid on changes: %d %s", st, b)
	}
	if st, _, b := e.call("GET", files, dan, nil); st != 401 || errCode(b) != server.CodeUnauthorized {
		t.Errorf("non-member dan: %d %s", st, b)
	}
	if st, _, _ := e.call("GET", "/v1/shares/00000000-0000-4000-8000-000000000000/admin/files", bob, nil); st != 401 {
		t.Errorf("unknown folder: %d", st)
	}
	// It acts as the member: an upload by cid is cid's.
	c := &client{e: e, share: e.share, user: "cid", token: cid}
	if _, err := c.put("b.txt", "", []byte("cid")); err != nil {
		t.Fatal(err)
	}
	if fs, _ := sh.ListDir(""); len(fs.Entries) != 2 || fs.Entries[1].UpdatedBy != "cid" {
		t.Errorf("files = %+v", fs.Entries)
	}
	// The WebSocket takes it too; an SSH key needs the device's folder
	// token (POST /v1/shares/{id}/devices).
	hs, _ := e.wsServer()
	if _, _, err := dialWS(t, hs.URL, e.share, cid); err != nil {
		t.Errorf("ws with a device token: %v", err)
	}
	if err := c.do("PUT", "/ssh-key", httpapi.SSHKeyRequest{PublicKey: "ssh-ed25519 AAAA"}, nil); err == nil || err.Status != 403 {
		t.Errorf("ssh-key with a device token: %v", err)
	}
	// A revoked member, a disabled account: 401.
	if err := sh.RevokeMember("cid"); err != nil {
		t.Fatal(err)
	}
	if st, _, _ := e.call("GET", "/v1/shares/"+e.share+"/changes", cid, nil); st != 401 {
		t.Errorf("revoked member: %d", st)
	}
	if _, err := e.srv.DisableUser("bob"); err != nil {
		t.Fatal(err)
	}
	if st, _, _ := e.call("GET", files, bob, nil); st != 401 {
		t.Errorf("disabled account: %d", st)
	}
}

func TestAddAccountMemberAndSearch(t *testing.T) {
	e := newAcctEnv(t)
	e.account("bob", "bobova-lozinka")
	e.account("cid", "cidova-lozinka")
	e.srv.AddUser("marija", "Marija M.")
	cid := e.loginOK("cid", "cidova-lozinka", "gui")

	// Only an admin of a folder may search: cid is in no folder yet.
	if st, _, b := e.call("GET", "/v1/users?q=b", cid, nil); st != 403 || errCode(b) != server.CodeForbidden {
		t.Errorf("search by non-admin: %d %s", st, b)
	}
	// ana's member token (an admin) may.
	st, _, b := e.call("GET", "/v1/users?q=ma", e.ana.token, nil)
	var refs []server.UserRef
	if json.Unmarshal(b, &refs); st != 200 || len(refs) != 1 || refs[0].Username != "marija" || refs[0].DisplayName != "Marija M." {
		t.Errorf("search: %d %s", st, b)
	}
	if strings.Contains(string(b), "created_at") || strings.Contains(string(b), `"id"`) {
		t.Errorf("search shows more than names: %s", b)
	}
	// ana adds cid's account as an admin.
	var tr httpapi.TokenResponse
	if err := e.ana.do("POST", "/admin/members", httpapi.AddMemberRequest{User: "cid", Admin: true}, &tr); err != nil {
		t.Fatal(err)
	}
	if tr.Token != "" || tr.Member.Username != "cid" || tr.Member.Role != "admin" || tr.Member.Account != "cid" {
		t.Errorf("add = %+v", tr)
	}
	if err := e.ana.do("POST", "/admin/members", httpapi.AddMemberRequest{User: "cid"}, nil); err == nil || err.Code != server.CodeMemberExists {
		t.Errorf("twice: %v", err)
	}
	if err := e.ana.do("POST", "/admin/members", httpapi.AddMemberRequest{User: "niko"}, nil); err == nil || err.Status != 404 {
		t.Errorf("unknown account: %v", err)
	}
	if err := e.ana.do("POST", "/admin/members", httpapi.AddMemberRequest{User: "bob", Username: "bob"}, nil); err == nil || err.Code != server.CodeInvalidRequest {
		t.Errorf("both: %v", err)
	}
	// Now cid is an admin: search works with the device token.
	if st, _, b := e.call("GET", "/v1/users?q=", cid, nil); st != 200 || strings.Count(string(b), "username") != 3 {
		t.Errorf("search by cid: %d %s", st, b)
	}
	var ml server.MemberList
	e.ana.do("GET", "/admin/members", nil, &ml)
	for _, m := range ml.Members {
		if (m.Username == "cid") != (m.Account == "cid") {
			t.Errorf("member %+v", m)
		}
	}
	// A non-admin member token may not search.
	bob := e.member("bobm")
	if st, _, _ := e.call("GET", "/v1/users", bob.token, nil); st != 403 {
		t.Errorf("search by member token: %d", st)
	}
	if st, _, _ := e.call("GET", "/v1/users", "", nil); st != 401 {
		t.Errorf("search without a token: %d", st)
	}
}

func TestLoginLimitBehindProxy(t *testing.T) {
	e := newAcctEnv(t) // httptest connects from 127.0.0.1: a trusted proxy
	e.account("bob", "bobova-lozinka")
	login := func(pw, xff string) (int, http.Header, []byte) {
		return e.call("POST", "/v1/auth/login", "", server.LoginRequest{Username: "x" + xff[:1], Password: pw}, "X-Forwarded-For", xff)
	}
	// Five failures from client 203.0.113.7 lock that address, not
	// 203.0.113.8 behind the same proxy.
	for i := 0; i < server.MaxFailures; i++ {
		login("pogrešna-lozinka", "203.0.113.7")
	}
	st, hdr, b := login("pogrešna-lozinka", "203.0.113.7")
	if st != 429 || errCode(b) != server.CodeTooManyAttempts || hdr.Get("Retry-After") != "1" {
		t.Fatalf("locked: %d %v %s", st, hdr, b)
	}
	if st, _, b := e.call("POST", "/v1/auth/login", "", server.LoginRequest{Username: "bob", Password: "bobova-lozinka"}, "X-Forwarded-For", "203.0.113.8"); st != 200 {
		t.Errorf("other client: %d %s", st, b)
	}
	e.clock.Advance(time.Second)
	if st, _, _ := login("pogrešna-lozinka", "203.0.113.7"); st != 401 {
		t.Errorf("after the wait: %d", st)
	}
	var ip string
	db := openRegistry(t, e)
	db.QueryRow(`SELECT ip FROM auth_log ORDER BY rowid DESC LIMIT 1`).Scan(&ip)
	if ip != "203.0.113.7" {
		t.Errorf("auth_log ip = %q", ip)
	}
}

func openRegistry(t *testing.T, e *env) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+filepath.Join(e.root, server.RegistryFile)+"?mode=ro&_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}
