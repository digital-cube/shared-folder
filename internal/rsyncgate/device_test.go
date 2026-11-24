package rsyncgate

import (
	"bytes"
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/digital-cube/shared-folder/internal/password"
	"github.com/digital-cube/shared-folder/internal/server"
)

// deviceFixture is a folder with member bob, linked to account bob, and
// two signed-in devices of the account with folder credentials.
type deviceFixture struct {
	srv        *server.Server
	sh         *server.Share
	bob        int64
	mac, linux int64 // device ids
	dir        string
}

func setupDevices(t *testing.T) *deviceFixture {
	t.Helper()
	p := password.Params{Memory: 64, Time: 1, Threads: 1, SaltLen: 16, KeyLen: 32}
	srv, err := server.Open(t.TempDir(), server.Options{PasswordParams: &p})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { srv.Close() })
	id, _, err := srv.CreateShare("tim", "ana")
	if err != nil {
		t.Fatal(err)
	}
	sh, _ := srv.Share(id)
	_, temp, err := srv.AddUser("bob", "")
	if err != nil {
		t.Fatal(err)
	}
	m, _, err := srv.AddUserMember(sh, "bob", false)
	if err != nil {
		t.Fatal(err)
	}
	f := &deviceFixture{srv: srv, sh: sh, bob: m.ID, dir: sh.Dir()}
	for _, name := range []string{"macbook", "linux"} {
		res, err := srv.Login(context.Background(), server.LoginRequest{Username: "bob", Password: temp, DeviceName: name})
		if err != nil {
			t.Fatal(err)
		}
		sess, err := srv.AuthenticateDevice(res.Token)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := srv.IssueFolderDevice(sh, sess); err != nil {
			t.Fatal(err)
		}
		if name == "macbook" {
			f.mac = sess.DeviceID
		} else {
			f.linux = sess.DeviceID
		}
	}
	os.WriteFile(filepath.Join(f.dir, "storage", "a.txt"), []byte("a"), 0o600)
	return f
}

func (f *deviceFixture) run(t *testing.T, device int64) (int, string, string) {
	t.Helper()
	bin, _ := fakeRsync(t, 0)
	g := &Gate{Share: f.sh, MemberID: f.bob, DeviceID: device, Server: f.srv, Rsync: bin}
	var out, errb bytes.Buffer
	code := g.Run(context.Background(), "rsync --server --sender . storage/a.txt", strings.NewReader(""), &out, &errb)
	b, _ := os.ReadFile(filepath.Join(f.dir, "service.log"))
	lines := strings.Split(strings.TrimSpace(string(b)), "\n")
	return code, errb.String(), lines[len(lines)-1]
}

func TestDeviceGate(t *testing.T) {
	f := setupDevices(t)
	mac, linux := f.mac, f.linux
	ok := func(what string, device int64, name string) {
		t.Helper()
		code, errb, l := f.run(t, device)
		if code != 0 || errb != "" {
			t.Errorf("%s: exit %d %q", what, code, errb)
		}
		if !strings.Contains(l, `transfer member=bob device="`+name+`#`) || !strings.Contains(l, "outcome=ok") {
			t.Errorf("%s: log line %q", what, l)
		}
	}
	denied := func(what string, device int64, detail string) {
		t.Helper()
		code, errb, l := f.run(t, device)
		if code == 0 || !strings.HasPrefix(errb, "shfd rsync-gate: denied (device_revoked): ") {
			t.Errorf("%s: exit %d %q", what, code, errb)
		}
		if !strings.Contains(l, `outcome=denied detail="device_revoked: `+detail) {
			t.Errorf("%s: log line %q", what, l)
		}
	}
	ok("mac", mac, "macbook")
	ok("linux", linux, "linux")
	denied("unknown device", 999, "device 999 has no credentials in this folder")

	// The device revoked in the folder: only that one.
	if err := f.sh.RevokeMemberDevice("bob", mac); err != nil {
		t.Fatal(err)
	}
	denied("revoked device", mac, "device")
	ok("the other device", linux, "linux")

	// The account's device signed out.
	u, _ := f.srv.UserByName("bob")
	if err := f.srv.RevokeDevice(u.ID, linux); err != nil {
		t.Fatal(err)
	}
	denied("signed-out device", linux, "device")
}

func TestDeviceGateAccountAndMember(t *testing.T) {
	f := setupDevices(t)
	// The registry alone: a device whose folder row is active but whose
	// account is disabled (as if the folder were not reached by disable).
	if _, err := f.srv.DisableUser("bob"); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", "file:"+f.sh.DBPath()+"?_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`UPDATE member_devices SET revoked_at = NULL`); err != nil {
		t.Fatal(err)
	}
	if code, errb, _ := f.run(t, f.mac); code == 0 || !strings.Contains(errb, "account disabled") {
		t.Errorf("disabled account (registry): %d %q", code, errb)
	}
	if code, errb, l := f.run(t, f.mac); code == 0 || !strings.Contains(errb, "denied (device_revoked)") || !strings.Contains(l, "outcome=denied") {
		t.Errorf("disabled account: %d %q %q", code, errb, l)
	}
	if err := f.srv.EnableUser("bob"); err != nil {
		t.Fatal(err)
	}

	g := setupDevices(t)
	if err := g.sh.RevokeMember("bob"); err != nil {
		t.Fatal(err)
	}
	if code, errb, l := g.run(t, g.linux); code == 0 || !strings.Contains(errb, "denied (device_revoked): member bob is revoked") ||
		!strings.Contains(l, `detail="device_revoked: member bob is revoked"`) {
		t.Errorf("revoked member: %d %q %q", code, errb, l)
	}
	// A line without --device of the same member is refused as before.
	if code, errb, _ := g.run(t, 0); code == 0 || !strings.Contains(errb, "denied (member)") {
		t.Errorf("member's own key: %d %q", code, errb)
	}
}

func TestDeviceGateNeedsServer(t *testing.T) {
	f := setupDevices(t)
	g := &Gate{Share: f.sh, MemberID: f.bob, DeviceID: f.mac}
	if _, _, err := g.Authorize("rsync --server --sender . storage/a.txt"); err == nil {
		t.Error("a device without the server's accounts was let through")
	}
}
