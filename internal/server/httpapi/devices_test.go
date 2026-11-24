package httpapi_test

import (
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/digital-cube/shared-folder/internal/authkeys"
	"github.com/digital-cube/shared-folder/internal/server"
	"github.com/digital-cube/shared-folder/internal/server/httpapi"
)

// newDevEnv is an env with accounts and an authorized_keys file.
func newDevEnv(t *testing.T) (*env, string) {
	akPath := filepath.Join(t.TempDir(), "authorized_keys")
	e := newEnv(t, func(o *server.Options) {
		p := cheapPW
		o.PasswordParams = &p
		o.AuthorizedKeys = &authkeys.File{Path: akPath, Command: []string{"/usr/bin/shfd", "rsync-gate", "--data-root", "/srv"}}
		o.SSH = server.SSHEndpoint{User: "shfd", Host: "files.example.org", Port: 2222}
	})
	return e, akPath
}

// signIn signs an account in as a client device of the given name and
// returns its device token and device id.
func (e *env) signIn(name, pw, device string) (string, int64) {
	e.t.Helper()
	st, _, b := e.call("POST", "/v1/auth/login", "", server.LoginRequest{Username: name, Password: pw, DeviceName: device, Kind: "client"})
	if st != 200 {
		e.t.Fatalf("login %s: %d %s", name, st, b)
	}
	var res server.LoginResult
	json.Unmarshal(b, &res)
	st, _, b = e.call("GET", "/v1/me", res.Token, nil)
	var me httpapi.MeResponse
	json.Unmarshal(b, &me)
	for _, d := range me.Devices {
		if d.Current {
			return res.Token, d.ID
		}
	}
	e.t.Fatalf("no current device in /v1/me: %d %s", st, b)
	return "", 0
}

// folderDevice asks for the folder token of a device and returns the
// answer; a failure is returned as status and body.
func (e *env) folderDevice(share, deviceToken string) (*server.FolderDevice, int, []byte) {
	e.t.Helper()
	st, _, b := e.call("POST", "/v1/shares/"+share+"/devices", deviceToken, nil)
	if st != 200 {
		return nil, st, b
	}
	var fd server.FolderDevice
	if err := json.Unmarshal(b, &fd); err != nil {
		e.t.Fatal(err)
	}
	return &fd, st, b
}

func (e *env) mustFolderDevice(share, deviceToken string) *server.FolderDevice {
	e.t.Helper()
	fd, st, b := e.folderDevice(share, deviceToken)
	if fd == nil {
		e.t.Fatalf("POST devices: %d %s", st, b)
	}
	return fd
}

func akLines(t *testing.T, p string) []string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	var out []string
	for _, ln := range strings.Split(string(b), "\n") {
		if strings.HasPrefix(ln, "command=") {
			out = append(out, ln)
		}
	}
	return out
}

// akHas says whether a line of authorized_keys runs rsync-gate for the
// given arguments (after --data-root) and carries the key and comment.
func akHas(lines []string, args, key, comment string) bool {
	norm, _ := authkeys.ParsePublicKey(key)
	for _, ln := range lines {
		if strings.Contains(ln, " "+args+`",`) && strings.Contains(ln, norm+" "+comment) {
			return true
		}
	}
	return false
}

func TestFolderDeviceIssueAndReissue(t *testing.T) {
	e, _ := newDevEnv(t)
	sh, _ := e.srv.Share(e.share)
	e.account("bob", "bobova-lozinka")
	e.account("dan", "danova-lozinka")
	if _, _, err := e.srv.AddUserMember(sh, "bob", false); err != nil {
		t.Fatal(err)
	}
	mac, macID := e.signIn("bob", "bobova-lozinka", "macbook")
	dan, _ := e.signIn("dan", "danova-lozinka", "laptop")

	fd := e.mustFolderDevice(e.share, mac)
	if !strings.HasPrefix(fd.Token, "shf_") || fd.Member != "bob" || fd.Role != server.RoleMember || fd.DeviceID != macID {
		t.Errorf("answer: %+v", fd)
	}
	eq(t, "ssh endpoint", fd.SSH, server.SSHEndpoint{User: "shfd", Host: "files.example.org", Port: 2222})

	// The folder token acts as the member on the folder's routes.
	c := &client{e: e, share: e.share, user: "bob", token: fd.Token}
	if _, err := c.put("mac.txt", "", []byte("m")); err != nil {
		t.Fatal(err)
	}
	if fs, _ := sh.ListDir(""); len(fs.Entries) != 1 || fs.Entries[0].UpdatedBy != "bob" {
		t.Errorf("files = %+v", fs.Entries)
	}
	if _, err := c.changes(0, 10); err != nil {
		t.Errorf("changes: %v", err)
	}
	// ... and on /v1/whoami, as the member.
	if st, _, b := e.call("GET", "/v1/whoami", fd.Token, nil); st != 200 || !strings.Contains(string(b), `"username":"bob"`) {
		t.Errorf("whoami: %d %s", st, b)
	}
	// The device was seen.
	if devs, _ := e.srv.Devices(*mustMember(t, sh, "bob").UserID, false); len(devs) != 1 || devs[0].LastSeenAt == nil {
		t.Errorf("devices: %+v", devs)
	}

	// Asking again: a new token, and the old one stops working.
	fd2 := e.mustFolderDevice(e.share, mac)
	if fd2.Token == fd.Token {
		t.Fatal("the same token twice")
	}
	if _, err := c.changes(0, 10); err == nil || err.Status != 401 {
		t.Errorf("old folder token: %v", err)
	}
	c.token = fd2.Token
	if _, err := c.changes(0, 10); err != nil {
		t.Errorf("new folder token: %v", err)
	}
	var n int
	e.db().QueryRow(`SELECT count(*) FROM member_devices`).Scan(&n)
	eq(t, "member_devices rows", n, 1)

	// Not a member: 403. An unknown folder: 401. A member token: 401 (it
	// is no account's device).
	if _, st, b := e.folderDevice(e.share, dan); st != 403 || errCode(b) != server.CodeForbidden {
		t.Errorf("non-member: %d %s", st, b)
	}
	if _, st, b := e.folderDevice("00000000-0000-4000-8000-000000000000", mac); st != 401 || errCode(b) != server.CodeUnauthorized {
		t.Errorf("unknown folder: %d %s", st, b)
	}
	if _, st, _ := e.folderDevice(e.share, e.ana.token); st != 401 {
		t.Errorf("member token: %d", st)
	}
	if _, st, _ := e.folderDevice(e.share, fd2.Token); st != 401 {
		t.Errorf("folder token: %d", st)
	}
	// A revoked member: 403.
	if err := sh.RevokeMember("bob"); err != nil {
		t.Fatal(err)
	}
	if _, st, b := e.folderDevice(e.share, mac); st != 403 {
		t.Errorf("revoked member: %d %s", st, b)
	}
	if _, err := c.changes(0, 10); err == nil || err.Status != 401 {
		t.Errorf("folder token of a revoked member: %v", err)
	}
}

func mustMember(t *testing.T, sh *server.Share, name string) *server.Member {
	t.Helper()
	m, err := sh.MemberByName(name)
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func TestFolderDeviceSSHKeys(t *testing.T) {
	e, ak := newDevEnv(t)
	sh, _ := e.srv.Share(e.share)
	e.account("bob", "bobova-lozinka")
	// marko is an old member with a token of its own, linked to the
	// account bob (shfd member link).
	tok, _ := sh.AddMember("marko", false)
	if _, err := e.srv.LinkMember(sh, "marko", "bob"); err != nil {
		t.Fatal(err)
	}
	old := &client{e: e, share: e.share, user: "marko", token: tok}
	mac, macID := e.signIn("bob", "bobova-lozinka", "Mac Book")
	lin, linID := e.signIn("bob", "bobova-lozinka", "linux")
	macC := &client{e: e, share: e.share, user: "marko", token: e.mustFolderDevice(e.share, mac).Token}
	linC := &client{e: e, share: e.share, user: "marko", token: e.mustFolderDevice(e.share, lin).Token}

	kOld, kMac, kLin := sshKey(t, "old"), sshKey(t, "mac"), sshKey(t, "lin")
	if err := old.do("PUT", "/ssh-key", httpapi.SSHKeyRequest{PublicKey: kOld}, nil); err != nil {
		t.Fatal(err)
	}
	for _, x := range []struct {
		c *client
		k string
	}{{macC, kMac}, {linC, kLin}} {
		var res httpapi.SSHKeyResponse
		if err := x.c.do("PUT", "/ssh-key", httpapi.SSHKeyRequest{PublicKey: x.k}, &res); err != nil {
			t.Fatal(err)
		}
		if res.Replaced != nil || res.Fingerprint != authkeys.Fingerprint(x.k) || res.SSH.Host != "files.example.org" {
			t.Errorf("answer: %+v", res)
		}
	}
	// A device registering a new key replaces its own, without a warning.
	kMac2 := sshKey(t, "mac2")
	var res httpapi.SSHKeyResponse
	if err := macC.do("PUT", "/ssh-key", httpapi.SSHKeyRequest{PublicKey: kMac2}, &res); err != nil || res.Replaced != nil {
		t.Fatalf("second key of the mac: %v %+v", err, res)
	}
	// The same key on two devices: refused.
	if err := linC.do("PUT", "/ssh-key", httpapi.SSHKeyRequest{PublicKey: kMac2}, nil); err == nil || err.Code != server.CodeKeyInUse {
		t.Errorf("a key in use: %v", err)
	}

	m := mustMember(t, sh, "marko")
	norm := func(k string) string { n, _ := authkeys.ParsePublicKey(k); return n }
	eq(t, "members.ssh_pubkey (legacy) stays", m.SSHPubkey, norm(kOld))
	var macKey string
	e.db().QueryRow(`SELECT ssh_pubkey FROM member_devices WHERE device_id = ?`, macID).Scan(&macKey)
	eq(t, "member_devices.ssh_pubkey", macKey, norm(kMac2))

	lines := akLines(t, ak)
	mid := "--member " + itoa(m.ID)
	share := "--share " + e.share + " "
	if len(lines) != 3 ||
		!akHas(lines, share+mid, kOld, "shfd:marko@"+e.share) ||
		!akHas(lines, share+mid+" --device "+itoa(macID), kMac2, "shfd:marko/Mac-Book@"+e.share) ||
		!akHas(lines, share+mid+" --device "+itoa(linID), kLin, "shfd:marko/linux@"+e.share) {
		t.Fatalf("authorized_keys:\n%s", strings.Join(lines, "\n"))
	}
	// The file reads back as it was written.
	if es, err := (&authkeys.File{Path: ak}).Entries(); err != nil || len(es) != 3 || es[1].DeviceID != macID || es[1].Device != "Mac-Book" || es[1].Username != "marko" {
		t.Errorf("entries: %+v %v", es, err)
	}
	// Legacy registration with the member's token still warns about
	// replacing the member's own key, and touches no device line.
	var lres httpapi.SSHKeyResponse
	kOld2 := sshKey(t, "old2")
	if err := old.do("PUT", "/ssh-key", httpapi.SSHKeyRequest{PublicKey: kOld2}, &lres); err != nil || lres.Replaced == nil {
		t.Errorf("legacy replacement: %v %+v", err, lres)
	}
	if n := len(akLines(t, ak)); n != 3 {
		t.Errorf("%d lines after the legacy replacement", n)
	}

	// keys sync writes the same lines.
	os.WriteFile(ak, nil, 0o600)
	if skipped, err := e.srv.SyncAuthorizedKeys(); err != nil || len(skipped) != 0 {
		t.Fatalf("sync: %v %v", skipped, err)
	}
	lines = akLines(t, ak)
	if len(lines) != 3 || !akHas(lines, share+mid+" --device "+itoa(linID), kLin, "shfd:marko/linux@"+e.share) ||
		!akHas(lines, share+mid, kOld2, "shfd:marko@"+e.share) {
		t.Errorf("after keys sync:\n%s", strings.Join(lines, "\n"))
	}

	// Re-issuing the mac's token drops its key: the client registers a
	// new one with the new token.
	e.mustFolderDevice(e.share, mac)
	lines = akLines(t, ak)
	if len(lines) != 2 || akHas(lines, share+mid+" --device "+itoa(macID), kMac2, "shfd:marko/Mac-Book@"+e.share) {
		t.Errorf("after re-issue:\n%s", strings.Join(lines, "\n"))
	}
	// A device token cannot register a key.
	if st, _, b := e.call("PUT", "/v1/shares/"+e.share+"/ssh-key", lin, httpapi.SSHKeyRequest{PublicKey: sshKey(t, "x")}); st != 403 {
		t.Errorf("ssh-key with a device token: %d %s", st, b)
	}
}

func itoa(n int64) string { return strconv.FormatInt(n, 10) }

func TestFolderDeviceRevocation(t *testing.T) {
	e, ak := newDevEnv(t)
	sh, _ := e.srv.Share(e.share)
	other, _, err := e.srv.CreateShare("drugi", "carl")
	if err != nil {
		t.Fatal(err)
	}
	sh2, _ := e.srv.Share(other)
	e.account("bob", "bobova-lozinka")
	e.srv.AddUserMember(sh, "bob", false)
	e.srv.AddUserMember(sh2, "bob", false)
	mac, macID := e.signIn("bob", "bobova-lozinka", "macbook")
	lin, linID := e.signIn("bob", "bobova-lozinka", "linux")
	hs, _ := e.wsServer(httpapi.WithMemberCheck(time.Hour))

	type dev struct {
		c  *client
		ws *wsConn
	}
	open := func(share, token, key string) dev {
		t.Helper()
		c := &client{e: e, share: share, user: "bob", token: e.mustFolderDevice(share, token).Token}
		if err := c.do("PUT", "/ssh-key", httpapi.SSHKeyRequest{PublicKey: sshKey(t, key)}, nil); err != nil {
			t.Fatal(err)
		}
		w := mustDial(t, hs.URL, share, c.token)
		w.mustHead(0)
		return dev{c, w}
	}
	macA, linA := open(e.share, mac, "macA"), open(e.share, lin, "linA")
	macB, linB := open(other, mac, "macB"), open(other, lin, "linB")
	if n := len(akLines(t, ak)); n != 4 {
		t.Fatalf("%d lines", n)
	}
	closed := func(w *wsConn) bool {
		t.Helper()
		_, err := w.next(2 * time.Second)
		return err != nil && strings.Contains(err.Error(), "token revoked")
	}
	alive := func(d dev, what string) {
		t.Helper()
		if _, err := d.c.changes(0, 1); err != nil {
			t.Errorf("%s: %v", what, err)
		}
	}
	gone := func(d dev, what string) {
		t.Helper()
		if _, err := d.c.changes(0, 1); err == nil || err.Status != 401 {
			t.Errorf("%s still works: %v", what, err)
		}
	}

	// The folder admin revokes the mac in folder A: its token, key and
	// WebSocket; the linux in A and the mac in B stay.
	st, _, b := e.call("DELETE", "/v1/shares/"+e.share+"/admin/members/bob/devices/"+itoa(macID), e.ana.token, nil)
	if st != 200 {
		t.Fatalf("admin revoke: %d %s", st, b)
	}
	var mr httpapi.MemberResponse
	json.Unmarshal(b, &mr)
	if len(mr.Member.Devices) != 2 || !mr.Member.Devices[0].Revoked || mr.Member.Devices[1].Revoked || mr.Member.Devices[0].Name != "macbook" {
		t.Errorf("member after revoke: %+v", mr.Member.Devices)
	}
	if !closed(macA.ws) {
		t.Error("the mac's WebSocket in A stayed open")
	}
	gone(macA, "mac in A")
	alive(linA, "linux in A")
	alive(macB, "mac in B")
	if n := len(akLines(t, ak)); n != 3 {
		t.Errorf("%d lines after the admin revoke", n)
	}
	// Asking again does not bring it back: the device signs in anew.
	if _, st, b := e.folderDevice(e.share, mac); st != 403 || errCode(b) != server.CodeDeviceRevoked {
		t.Errorf("re-issue after a revoke: %d %s", st, b)
	}
	// Errors of the admin route.
	if st, _, _ := e.call("DELETE", "/v1/shares/"+e.share+"/admin/members/bob/devices/999", e.ana.token, nil); st != 404 {
		t.Errorf("unknown device: %d", st)
	}
	if st, _, _ := e.call("DELETE", "/v1/shares/"+e.share+"/admin/members/bob/devices/"+itoa(linID), linA.c.token, nil); st != 403 {
		t.Errorf("by a non-admin: %d", st)
	}
	// Left open: the linux in A and both devices in B.
	if n := sh.Watchers(); n != 1 {
		t.Errorf("%d WebSockets open in A, want the linux's", n)
	}

	// The account revokes its mac: its credentials in every folder.
	if st, _, b := e.call("DELETE", "/v1/me/devices/"+itoa(macID), lin, nil); st != 200 {
		t.Fatalf("me revoke: %d %s", st, b)
	}
	if !closed(macB.ws) {
		t.Error("the mac's WebSocket in B stayed open")
	}
	gone(macB, "mac in B")
	alive(linB, "linux in B")
	if n := len(akLines(t, ak)); n != 2 {
		t.Errorf("%d lines after revoking the device", n)
	}

	// Disabling the account: all its lines and WebSockets.
	if _, err := e.srv.DisableUser("bob"); err != nil {
		t.Fatal(err)
	}
	if !closed(linA.ws) || !closed(linB.ws) {
		t.Error("a WebSocket stayed open after the account was disabled")
	}
	gone(linA, "linux in A")
	gone(linB, "linux in B")
	if lines := akLines(t, ak); len(lines) != 0 {
		t.Errorf("lines after disable:\n%s", strings.Join(lines, "\n"))
	}
	// keys sync writes nothing back.
	if _, err := e.srv.SyncAuthorizedKeys(); err != nil {
		t.Fatal(err)
	}
	if lines := akLines(t, ak); len(lines) != 0 {
		t.Errorf("lines after keys sync:\n%s", strings.Join(lines, "\n"))
	}
}

func TestFolderDeviceRevokedByMemberRevokeAndCheck(t *testing.T) {
	e, ak := newDevEnv(t)
	sh, _ := e.srv.Share(e.share)
	e.account("bob", "bobova-lozinka")
	e.srv.AddUserMember(sh, "bob", false)
	mac, _ := e.signIn("bob", "bobova-lozinka", "macbook")
	c := &client{e: e, share: e.share, user: "bob", token: e.mustFolderDevice(e.share, mac).Token}
	if err := c.do("PUT", "/ssh-key", httpapi.SSHKeyRequest{PublicKey: sshKey(t, "m")}, nil); err != nil {
		t.Fatal(err)
	}
	// A WebSocket that learns of a revocation only from its re-check (as
	// when another process revoked it).
	hs, _ := e.wsServer(httpapi.WithMemberCheck(50 * time.Millisecond))
	w := mustDial(t, hs.URL, e.share, c.token)
	w.mustHead(0)
	rw := e.dbRW()
	if _, err := rw.Exec(`UPDATE member_devices SET revoked_at = '2026-01-01T00:00:00Z'`); err != nil {
		t.Fatal(err)
	}
	if _, err := w.next(2 * time.Second); err == nil || !strings.Contains(err.Error(), "token revoked") {
		t.Errorf("re-check: %v", err)
	}
	rw.Exec(`UPDATE member_devices SET revoked_at = NULL`)

	// Revoking the member removes the device lines too.
	if err := sh.RevokeMember("bob"); err != nil {
		t.Fatal(err)
	}
	if lines := akLines(t, ak); len(lines) != 0 {
		t.Errorf("lines after revoking the member:\n%s", strings.Join(lines, "\n"))
	}
	var revoked int
	e.db().QueryRow(`SELECT count(*) FROM member_devices WHERE revoked_at IS NOT NULL`).Scan(&revoked)
	eq(t, "device credentials revoked with the member", revoked, 1)
}

// dbRW opens the folder's database for writing, as another process would.
func (e *env) dbRW() *sql.DB {
	e.t.Helper()
	db, err := sql.Open("sqlite", "file:"+filepath.Join(e.dir(), "service.db")+"?_pragma=busy_timeout(5000)")
	if err != nil {
		e.t.Fatal(err)
	}
	e.t.Cleanup(func() { db.Close() })
	return db
}

func TestAdminMembersListsDevices(t *testing.T) {
	e, _ := newDevEnv(t)
	sh, _ := e.srv.Share(e.share)
	e.account("bob", "bobova-lozinka")
	e.srv.AddUserMember(sh, "bob", false)
	mac, macID := e.signIn("bob", "bobova-lozinka", "macbook")
	_, _ = e.signIn("bob", "bobova-lozinka", "unused") // signed in, but no folder token: not listed
	c := &client{e: e, share: e.share, user: "bob", token: e.mustFolderDevice(e.share, mac).Token}
	key := sshKey(t, "m")
	if err := c.do("PUT", "/ssh-key", httpapi.SSHKeyRequest{PublicKey: key}, nil); err != nil {
		t.Fatal(err)
	}
	st, _, b := e.call("GET", "/v1/shares/"+e.share+"/admin/members", e.ana.token, nil)
	if st != 200 {
		t.Fatalf("%d %s", st, b)
	}
	var l server.MemberList
	json.Unmarshal(b, &l)
	var bob *server.MemberInfo
	for i := range l.Members {
		if l.Members[i].Username == "bob" {
			bob = &l.Members[i]
		} else if l.Members[i].Devices == nil {
			t.Errorf("%s: devices is null", l.Members[i].Username)
		}
	}
	if bob == nil || len(bob.Devices) != 1 {
		t.Fatalf("bob: %+v", bob)
	}
	d := bob.Devices[0]
	if d.ID != macID || d.Name != "macbook" || d.Kind != "client" || d.SSHFingerprint != authkeys.Fingerprint(key) || d.Revoked || d.LastSeenAt == nil {
		t.Errorf("device: %+v", d)
	}
	// Its key makes the member active, without a key of its own.
	eq(t, "status", bob.Status, server.StatusActive)
	if !regexp.MustCompile(`"devices":\[\{"id":`).Match(b) {
		t.Errorf("json: %s", b)
	}
}
