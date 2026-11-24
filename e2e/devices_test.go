//go:build e2e

package e2e

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// apiJSON calls a path of the API with a bearer token and a JSON body,
// and decodes the answer into out (when not nil). It returns the status.
func (c *cluster) apiJSON(token, method, path string, in, out any) int {
	c.t.Helper()
	var body io.Reader
	if in != nil {
		b, _ := json.Marshal(in)
		body = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, c.url+path, body)
	if err != nil {
		c.t.Fatal(err)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		c.t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	if out != nil && resp.StatusCode == http.StatusOK {
		if err := json.Unmarshal(b, out); err != nil {
			c.t.Fatalf("%s %s: %v\n%s", method, path, err, b)
		}
	}
	if resp.StatusCode != http.StatusOK {
		c.t.Logf("%s %s: HTTP %d %s", method, path, resp.StatusCode, b)
	}
	return resp.StatusCode
}

// signIn signs an account in as a client device and returns its device
// token and device id.
func (c *cluster) signIn(user, pw, device string) (string, int64) {
	c.t.Helper()
	var res struct {
		Token string `json:"token"`
	}
	if st := c.apiJSON("", "POST", "/v1/auth/login", map[string]string{"username": user, "password": pw, "device_name": device, "kind": "client"}, &res); st != 200 {
		c.t.Fatalf("login %s: HTTP %d", user, st)
	}
	var me struct {
		Devices []struct {
			ID      int64 `json:"id"`
			Current bool  `json:"current"`
		} `json:"devices"`
	}
	c.apiJSON(res.Token, "GET", "/v1/me", nil, &me)
	for _, d := range me.Devices {
		if d.Current {
			return res.Token, d.ID
		}
	}
	c.t.Fatalf("no current device for %s", device)
	return "", 0
}

// deviceClient is a client of an account's device: it gets the device's
// folder token (POST /v1/shares/{id}/devices) and joins with it, which
// registers an SSH key of the device's own.
func (c *cluster) deviceClient(member, machine, deviceToken string) *client {
	c.t.Helper()
	var fd struct {
		Token  string `json:"token"`
		Member string `json:"member"`
	}
	if st := c.apiJSON(deviceToken, "POST", "/v1/shares/"+c.share+"/devices", nil, &fd); st != 200 || fd.Member != member {
		c.t.Fatalf("folder token of %s: HTTP %d %+v", machine, st, fd)
	}
	cl := c.clientWithToken(member, machine, fd.Token)
	cl.join()
	cl.start()
	return cl
}

// Two devices of one account in one folder sync at the same time, each
// with its own folder token and SSH key, next to a client with a member
// token; revoking one device leaves the other and the old client alone.
func TestDevicesOfOneAccount(t *testing.T) {
	t.Parallel()
	c := newCluster(t)
	ana := c.joined("ana") // the old way: a member token

	out := c.mustShfd("user", "add", "bob")
	temp := regexp.MustCompile(`temporary password: (\S+)`).FindStringSubmatch(out)[1]
	c.mustShfd("member", "add", c.share, "bob", "--user")
	mac, macID := c.signIn("bob", temp, "macbook")
	const pw = "bobova-nova-lozinka"
	if st := c.apiJSON(mac, "POST", "/v1/me/password", map[string]string{"old": temp, "new": pw}, nil); st != 200 {
		t.Fatalf("password change: HTTP %d", st)
	}
	lin, linID := c.signIn("bob", pw, "linux")

	bobMac := c.deviceClient("bob", "bob-mac", mac)
	bobLin := c.deviceClient("bob", "bob-linux", lin)

	// Each device has its line, and the member token client its own.
	ak := readFile(c.ak)
	for _, want := range []string{"shfd:ana@", "shfd:bob/macbook@", "shfd:bob/linux@",
		" --device " + strconv.FormatInt(macID, 10) + `",`, " --device " + strconv.FormatInt(linID, 10) + `",`} {
		if !strings.Contains(ak, want) {
			t.Errorf("authorized_keys has no %q:\n%s", want, ak)
		}
	}
	if strings.Contains(readFile(bobLin.log), "replaced") {
		t.Errorf("the linux join warned about a replaced key:\n%s", readFile(bobLin.log))
	}

	// All three write at once; nobody's key was taken.
	bobMac.write("mac.txt", "from the mac")
	bobLin.write("linux.txt", "from linux")
	ana.write("ana.txt", "from ana")
	for _, cl := range []*client{ana, bobMac, bobLin} {
		cl.has("mac.txt", "from the mac")
		cl.has("linux.txt", "from linux")
		cl.has("ana.txt", "from ana")
	}
	c.converged(arrive)
	for _, cl := range []*client{bobMac, bobLin} {
		if out := cl.mustShf("status"); !strings.Contains(out, "state:      ok") {
			t.Errorf("%s status:\n%s", cl.name, out)
		}
	}
	// The history names the member, not the device.
	for _, e := range c.eventsOn("mac.txt") {
		if e.Member != "bob" {
			t.Errorf("mac.txt by %q", e.Member)
		}
	}

	// bob revokes the mac from the linux: only the mac stops.
	if st := c.apiJSON(lin, "DELETE", "/v1/me/devices/"+strconv.FormatInt(macID, 10), nil, nil); st != 200 {
		t.Fatalf("revoking the mac: HTTP %d", st)
	}
	bobMac.revoked = true
	if ak := readFile(c.ak); strings.Contains(ak, "shfd:bob/macbook@") || !strings.Contains(ak, "shfd:bob/linux@") || !strings.Contains(ak, "shfd:ana@") {
		t.Errorf("authorized_keys after revoking the mac:\n%s", ak)
	}
	if code := c.apiStatus(bobMac.token, "GET", "/changes?since=0"); code != http.StatusUnauthorized {
		t.Errorf("the mac's folder token after the revocation: HTTP %d", code)
	}
	// Its key is gone from authorized_keys; over an SSH connection it
	// still had open, rsync-gate refuses it as device_revoked.
	key := filepath.Join(bobMac.home, c.share, "id_ed25519")
	if out, err := c.rsyncAs(key, "storage/mac.txt", filepath.Join(c.base, "probe.txt")); err == nil || !strings.Contains(out, "Permission denied") {
		t.Errorf("the revoked mac's key still works: %v\n%s", err, out)
	}

	bobMac.write("mac-after.txt", "should not arrive")
	bobLin.write("linux-after.txt", "still syncs")
	ana.write("ana-after.txt", "still syncs")
	bobLin.has("ana-after.txt", "still syncs")
	ana.has("linux-after.txt", "still syncs")
	eventually(t, arrive, "the mac's daemon is refused", func() error {
		if !strings.Contains(readFile(bobMac.log), "unauthorized") {
			return errorf("no refusal in the mac's log")
		}
		return nil
	})
	if fileExists(c.storage("mac-after.txt")) {
		t.Error("the revoked mac's file reached the server")
	}
	c.converged(arrive)
	c.verifyClean()

	// The admin's view lists both devices, the mac revoked.
	out = c.mustShfd("member", "list", c.share)
	if !regexp.MustCompile(`(?m)^bob\s+`+strconv.FormatInt(macID, 10)+`\s+macbook\s.*\srevoked `).MatchString(out) ||
		!regexp.MustCompile(`(?m)^bob\s+`+strconv.FormatInt(linID, 10)+`\s+linux\s.*\sactive\s`).MatchString(out) {
		t.Errorf("member list:\n%s", out)
	}
}

// rsync-gate refuses a device's key over a connection opened before the
// device was revoked, as device_revoked, and says so in service.log.
func TestRevokedDeviceOnOpenConnection(t *testing.T) {
	t.Parallel()
	c := newCluster(t)
	ana := c.joined("ana")
	ana.write("a.txt", "a")
	c.stored("a.txt", "a")

	out := c.mustShfd("user", "add", "bob")
	temp := regexp.MustCompile(`temporary password: (\S+)`).FindStringSubmatch(out)[1]
	c.mustShfd("member", "add", c.share, "bob", "--user")
	dev, devID := c.signIn("bob", temp, "macbook")
	if st := c.apiJSON(dev, "POST", "/v1/me/password", map[string]string{"old": temp, "new": "bobova-nova-lozinka"}, nil); st != 200 {
		t.Fatalf("password change: HTTP %d", st)
	}
	bob := c.deviceClient("bob", "bob-mac", dev)
	bob.has("a.txt", "a")
	key := filepath.Join(bob.home, c.share, "id_ed25519")
	sock := c.sshMaster(key)
	if out, err := c.rsyncVia(sock, "storage/a.txt", filepath.Join(c.base, "probe.txt")); err != nil {
		t.Fatalf("download before the revocation: %v\n%s", err, out)
	}

	// The folder's admin revokes the device in the folder.
	out = c.mustShfd("member", "list", c.share, "--json")
	if !strings.Contains(out, `"name": "macbook"`) {
		t.Fatalf("member list --json:\n%s", out)
	}
	if st := c.apiJSON(ana.token, "DELETE", "/v1/shares/"+c.share+"/admin/members/bob/devices/"+strconv.FormatInt(devID, 10), nil, nil); st != 403 {
		t.Errorf("a member that is not an admin revoked a device: HTTP %d", st)
	}
	admin := regexp.MustCompile(`token:\s+(\S+)`).FindStringSubmatch(c.mustShfd("member", "reset-token", c.share, "admin"))[1]
	if st := c.apiJSON(admin, "DELETE", "/v1/shares/"+c.share+"/admin/members/bob/devices/"+strconv.FormatInt(devID, 10), nil, nil); st != 200 {
		t.Fatalf("admin revoke: HTTP %d", st)
	}
	bob.revoked = true
	out, err := c.rsyncVia(sock, "storage/a.txt", filepath.Join(c.base, "probe-2.txt"))
	if err == nil || !strings.Contains(out, "denied (device_revoked)") {
		t.Errorf("the revoked device downloaded over its open connection: %v\n%s", err, out)
	}
	if !strings.Contains(readFile(c.serviceLog()), `transfer member=bob device="macbook#`+strconv.FormatInt(devID, 10)+`" dir=- path="" outcome=denied detail="device_revoked: `) {
		t.Errorf("service.log:\n%s", readFile(c.serviceLog()))
	}
}
