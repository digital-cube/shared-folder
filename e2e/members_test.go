//go:build e2e

package e2e

import (
	"bytes"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// Scenario 10: a third client joins with a folder that already has some
// of the same files, some different ones and some of its own. Nothing is
// deleted; the different ones become conflict copies.
func TestJoinNonEmptyFolder(t *testing.T) {
	t.Parallel()
	c := newCluster(t)
	ana, bob := c.joined("ana"), c.joined("bob")
	ana.write("same.txt", "identical everywhere")
	ana.write("diff.txt", "the server's version")
	ana.write("docs/shared-only.md", "only on the server")
	bob.has("docs/shared-only.md", "only on the server")
	bob.has("diff.txt", "the server's version")
	bob.has("same.txt", "identical everywhere")

	carl := c.newClient("carl")
	carl.write("same.txt", "identical everywhere")
	carl.write("diff.txt", "carl's own version")
	carl.write("carl-only.txt", "carl brought this")
	carl.write("docs/carl.md", "and this")
	carl.mkdir("empty-at-carl")
	carl.join()
	carl.start()

	conflictRE := regexp.MustCompile(`^diff\.conflict-carl-\d{8}T\d{6}Z(-\d+)?\.txt$`)
	eventually(t, arrive, "the conflict copy is on the server", func() error {
		if n := countMatches(c.storageDir(), conflictRE); n != 1 {
			return errorf("%d conflict copies", n)
		}
		return nil
	})
	copyName := onlyMatch(t, c.storageDir(), conflictRE)
	for _, cl := range []*client{ana, bob, carl} {
		cl.has("same.txt", "identical everywhere")
		cl.has("diff.txt", "the server's version")
		cl.has(copyName, "carl's own version")
		cl.has("docs/shared-only.md", "only on the server")
		cl.has("carl-only.txt", "carl brought this")
		cl.has("docs/carl.md", "and this")
		cl.isDir("empty-at-carl")
	}
	c.converged(arrive)
	for _, e := range c.events() {
		if e.Op == "delete" || e.Op == "rmdir" {
			t.Errorf("something was deleted: %+v", e)
		}
	}
	for _, e := range c.eventsOn("same.txt") {
		if e.Member != "ana" {
			t.Errorf("same.txt changed by the join: %+v", e)
		}
	}
}

// Scenario 11: after shfd member revoke the member's client can neither
// read nor send, through the API or over SSH.
func TestRevokedMember(t *testing.T) {
	t.Parallel()
	c := newCluster(t)
	ana, bob := c.joined("ana"), c.joined("bob")
	ana.write("before.txt", "both have this")
	bob.has("before.txt", "both have this")

	key := filepath.Join(bob.home, c.share, "id_ed25519")
	if !fileExists(key) {
		t.Fatalf("bob's key is not at %s", key)
	}
	// the same direct transfers succeed before the revocation, so their
	// failure afterwards is the revocation's doing
	if out, err := c.rsyncAs(key, "storage/before.txt", filepath.Join(c.base, "probe.txt")); err != nil {
		t.Fatalf("bob cannot download before the revocation: %v\n%s", err, out)
	}
	if code := c.apiStatus(bob.token, "GET", "/changes?since=0"); code != http.StatusOK {
		t.Fatalf("bob's token before the revocation: HTTP %d", code)
	}

	c.mustShfd("member", "revoke", c.share, "bob")
	bob.revoked = true

	if strings.Contains(readFile(c.ak), "shfd:bob@") {
		t.Errorf("bob's key is still in authorized_keys:\n%s", readFile(c.ak))
	}
	for _, call := range [][2]string{{"GET", "/changes?since=0"}, {"POST", "/uploads"}, {"POST", "/ops"}, {"GET", "/ws"}} {
		if code := c.apiStatus(bob.token, call[0], call[1]); code != http.StatusUnauthorized {
			t.Errorf("revoked token, %s %s: HTTP %d, want 401", call[0], call[1], code)
		}
	}
	out, err := c.rsyncAs(key, "storage/before.txt", filepath.Join(c.base, "probe-2.txt"))
	if err == nil || !strings.Contains(out, "Permission denied") {
		t.Errorf("revoked key can still download: %v\n%s", err, out)
	}
	src := filepath.Join(c.base, "upload.txt")
	os.WriteFile(src, []byte("x"), 0o644)
	out, err = c.rsyncAs(key, src, "tmp/0123456789abcdef0123456789abcdef", true)
	if err == nil || !strings.Contains(out, "Permission denied") {
		t.Errorf("revoked key can still upload: %v\n%s", err, out)
	}

	// and the running client gets nothing through either
	bob.write("bob-after.txt", "should not arrive")
	ana.write("ana-after.txt", "bob should not get this")
	c.stored("ana-after.txt", "bob should not get this")
	eventually(t, arrive, "bob's daemon is refused", func() error {
		if !strings.Contains(readFile(bob.log), "unauthorized") {
			return errorf("no refusal in bob's log")
		}
		return nil
	})
	ana.write("ana-after-2.txt", "nor this")
	c.stored("ana-after-2.txt", "nor this")
	if fileExists(c.storage("bob-after.txt")) {
		t.Error("bob's new file reached the server after the revocation")
	}
	for _, p := range []string{"ana-after.txt", "ana-after-2.txt"} {
		if fileExists(bob.abs(p)) {
			t.Errorf("bob received %s after the revocation", p)
		}
	}
	c.converged(arrive)
}

// apiStatus calls the folder's API with token and returns the HTTP status.
func (c *cluster) apiStatus(token, method, path string) int {
	c.t.Helper()
	body := bytes.NewReader([]byte(`{}`))
	req, err := http.NewRequest(method, c.url+"/v1/shares/"+c.share+path, body)
	if err != nil {
		c.t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		c.t.Fatal(err)
	}
	resp.Body.Close()
	return resp.StatusCode
}

// rsyncAs runs rsync over SSH with the given key, from remote to local
// (or local to remote when upload is set), as a client would.
func (c *cluster) rsyncAs(key, from, to string, upload ...bool) (string, error) {
	rsh := fmt.Sprintf("ssh -F /dev/null -i %s -p %d -l %s -o IdentitiesOnly=yes -o IdentityAgent=none -o BatchMode=yes "+
		"-o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null -o LogLevel=ERROR", key, c.sshd.Port, c.sshd.User)
	args := []string{"--timeout=20", "-e", rsh}
	if len(upload) > 0 && upload[0] {
		args = append(args, "--partial", "--", from, "127.0.0.1:"+to)
	} else {
		args = append(args, "--inplace", "--", "127.0.0.1:"+from, to)
	}
	cmd := exec.Command("rsync", args...)
	cmd.Env = append(os.Environ(), "LC_ALL=C")
	out, err := cmd.CombinedOutput()
	return string(out), err
}
