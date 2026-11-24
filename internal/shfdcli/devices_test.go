package shfdcli

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/digital-cube/shared-folder/internal/authkeys"
	"github.com/digital-cube/shared-folder/internal/server"
)

func TestMemberDevicesCommands(t *testing.T) {
	root := t.TempDir()
	ak := filepath.Join(t.TempDir(), "authorized_keys")
	env := map[string]string{EnvDataRoot: root, EnvAuthorizedKeys: ak, EnvBinary: "/opt/shfd/bin/shfd"}
	_, out, _ := run(t, env, "share", "create", "tim", "--admin", "igor")
	id := idRE.FindStringSubmatch(out)[1]
	_, out, _ = run(t, env, "user", "add", "bob")
	temp := tempRE.FindStringSubmatch(out)[1]
	if code, out, errOut := run(t, env, "member", "add", id, "bob", "--user"); code != 0 {
		t.Fatalf("member add --user: %d\n%s%s", code, out, errOut)
	}

	// The device signs in, gets its folder token and registers its key,
	// the way the API does.
	var sf sshFlags
	sf.authorizedKeys, sf.binary = ak, "/opt/shfd/bin/shfd"
	opts, err := sf.options(roots{data: root}, Env{})
	if err != nil {
		t.Fatal(err)
	}
	srv, _ := server.Open(root, opts)
	sh, _ := srv.Share(id)
	res, err := srv.Login(context.Background(), server.LoginRequest{Username: "bob", Password: temp, DeviceName: "macbook"})
	if err != nil {
		t.Fatal(err)
	}
	sess, _ := srv.AuthenticateDevice(res.Token)
	fd, err := srv.IssueFolderDevice(sh, sess)
	if err != nil {
		t.Fatal(err)
	}
	m, err := srv.AuthenticateFolder(sh, fd.Token)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := sh.SetSSHKey(m, testKey); err != nil {
		t.Fatal(err)
	}
	srv.Close()
	dev := strconv.FormatInt(fd.DeviceID, 10)

	data, _ := os.ReadFile(ak)
	if !strings.Contains(string(data), "--member "+strconv.FormatInt(m.ID, 10)+" --device "+dev+`",`) ||
		!strings.Contains(string(data), "shfd:bob/macbook@"+id) {
		t.Fatalf("authorized_keys:\n%s", data)
	}

	fp := authkeys.Fingerprint(testKey)
	_, out, _ = run(t, env, "member", "list", id)
	if !regexp.MustCompile(`(?m)^bob\s+member\s+active\s.*\s1\s+bob\s+-$`).MatchString(out) ||
		!regexp.MustCompile(`(?m)^bob\s+`+dev+`\s+macbook\s+client\s+\S+\s+active\s+`+regexp.QuoteMeta(fp)+`$`).MatchString(out) {
		t.Errorf("member list:\n%s", out)
	}
	_, out, _ = run(t, env, "member", "list", id, "--json")
	var l server.MemberList
	if err := json.Unmarshal([]byte(out), &l); err != nil || len(l.Members) != 2 {
		t.Fatalf("member list --json: %v\n%s", err, out)
	}
	if d := l.Members[1].Devices; l.Members[1].Account != "bob" || len(d) != 1 || d[0].Name != "macbook" || d[0].SSHFingerprint != fp || d[0].Revoked {
		t.Errorf("json member: %+v", l.Members[1])
	}

	os.WriteFile(ak, nil, 0o600)
	if code, out, errOut := run(t, env, "keys", "sync"); code != 0 || !strings.Contains(out, "0 member key(s), 1 device key(s)") {
		t.Errorf("keys sync: %d %s%s", code, out, errOut)
	}

	if code, out, errOut := run(t, env, "user", "disable", "bob"); code != 0 || !strings.Contains(out, "signed out 1 device") {
		t.Fatalf("user disable: %d %s%s", code, out, errOut)
	}
	if data, _ := os.ReadFile(ak); strings.Contains(string(data), "--device") {
		t.Errorf("a device line after user disable:\n%s", data)
	}
	_, out, _ = run(t, env, "member", "list", id)
	if !regexp.MustCompile(`(?m)^bob\s+member\s+active\s.*\s0 \(\+1 revoked\)\s+bob\s+-$`).MatchString(out) ||
		!regexp.MustCompile(`(?m)^bob\s+`+dev+`\s+macbook\s.*\srevoked `).MatchString(out) {
		t.Errorf("member list after disable:\n%s", out)
	}
}
