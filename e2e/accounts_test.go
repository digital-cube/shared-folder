//go:build e2e

package e2e

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"testing"
)

// shfIn runs a command of the client with stdin.
func (cl *client) shfIn(stdin string, args ...string) string {
	cl.c.t.Helper()
	cmd := exec.Command(shfBin, args...)
	cmd.Env = append(os.Environ(), cl.env()...)
	cmd.Dir = cl.dir
	cmd.Stdin = strings.NewReader(stdin)
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	err := cmd.Run()
	fmt.Fprintf(mustAppend(cl.c.t, cl.log), "==== shf %s\n%s", strings.Join(args, " "), out.String())
	var ee *exec.ExitError
	if errors.As(err, &ee) || err != nil {
		cl.c.t.Fatalf("%s: shf %s: %v\n%s", cl.name, strings.Join(args, " "), err, out.String())
	}
	return out.String()
}

// account makes an account and returns its temporary password.
func (c *cluster) account(name string) string {
	c.t.Helper()
	out := c.mustShfd("user", "add", name)
	m := regexp.MustCompile(`temporary password: (\S+)`).FindStringSubmatch(out)
	if m == nil {
		c.t.Fatalf("user add printed no password:\n%s", out)
	}
	return m[1]
}

// accountClient prepares a machine of an account (no token: it signs in).
func (c *cluster) accountClient(name, machine string) *client {
	c.t.Helper()
	return c.clientWithToken(name, machine, "")
}

func inode(t *testing.T, p string) uint64 {
	t.Helper()
	fi, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}
	return fi.Sys().(*syscall.Stat_t).Ino
}

// One account signs in on two machines with shf login, joins the folder
// by name on one and by id on the other: both synchronize, each with its
// own SSH key. shf logout on one stops only that one, which waits signed
// out and goes on after shf login.
func TestAccountLoginOnTwoDevices(t *testing.T) {
	t.Parallel()
	c := newCluster(t)
	ana := c.joined("ana") // a member's token, the old way

	temp := c.account("bob")
	c.mustShfd("member", "add", c.share, "bob", "--user")
	const pw = "bobova-nova-lozinka"
	mac := c.accountClient("bob", "bob-mac")
	lin := c.accountClient("bob", "bob-linux")
	out := mac.shfIn(temp+"\n"+pw+"\n", "login", c.url, "--user", "bob", "--password-stdin", "--device-name", "macbook")
	if !strings.Contains(out, "password changed") || !strings.Contains(out, "signed in to "+c.url+" as bob (device macbook") {
		t.Fatalf("login on the mac:\n%s", out)
	}
	lin.shfIn(pw+"\n", "login", c.url, "--user", "bob", "--password-stdin", "--device-name", "linux")
	if out := mac.mustShf("folders"); !regexp.MustCompile(`(?m)^\s+tim\s+member\s+` + c.share + `\s+no$`).MatchString(out) {
		t.Errorf("folders:\n%s", out)
	}

	out = mac.mustShf("join", c.url, "tim", mac.dir)
	fmt.Fprintf(mustAppend(t, mac.log), "==== shf join\n%s", out)
	if !strings.Contains(out, "account: bob (member bob, device macbook)") {
		t.Errorf("join by name:\n%s", out)
	}
	out = lin.mustShf("join", c.url, c.share, lin.dir)
	fmt.Fprintf(mustAppend(t, lin.log), "==== shf join\n%s", out)
	mac.start()
	lin.start()
	ak := readFile(c.ak)
	for _, want := range []string{"shfd:bob/macbook@", "shfd:bob/linux@", "shfd:ana@"} {
		if !strings.Contains(ak, want) {
			t.Errorf("authorized_keys has no %q:\n%s", want, ak)
		}
	}

	mac.write("mac.txt", "from the mac")
	lin.write("linux.txt", "from linux")
	ana.write("ana.txt", "from ana")
	for _, cl := range []*client{ana, mac, lin} {
		cl.has("mac.txt", "from the mac")
		cl.has("linux.txt", "from linux")
		cl.has("ana.txt", "from ana")
	}
	c.converged(arrive)
	for _, cl := range []*client{mac, lin} {
		if out := cl.mustShf("status"); !strings.Contains(out, "state:      ok") || !strings.Contains(out, "account:    bob, member bob") {
			t.Errorf("%s status:\n%s", cl.name, out)
		}
	}

	// logout on the linux: it stops, the mac and ana go on
	out = lin.mustShf("logout", c.url)
	if !strings.Contains(out, "signed out of "+c.url) || !strings.Contains(out, lin.dir) {
		t.Errorf("logout:\n%s", out)
	}
	lin.revoked = true
	if ak := readFile(c.ak); strings.Contains(ak, "shfd:bob/linux@") || !strings.Contains(ak, "shfd:bob/macbook@") {
		t.Errorf("authorized_keys after logout:\n%s", ak)
	}
	eventually(t, arrive, "the linux daemon signed out", func() error {
		if !strings.Contains(readFile(lin.log), "signed out: this device is signed out of "+c.url) {
			return errorf("not in its log yet")
		}
		return nil
	})
	if out := lin.mustShf("status"); !strings.Contains(out, "state:      signed out") || !strings.Contains(out, "shf login "+c.url) {
		t.Errorf("status after logout:\n%s", out)
	}
	lin.write("linux-offline.txt", "waits for login")
	mac.write("mac-after.txt", "still syncs")
	ana.has("mac-after.txt", "still syncs")
	c.converged(arrive)
	if fileExists(c.storage("linux-offline.txt")) {
		t.Error("a signed-out device's file reached the server")
	}
	if n := strings.Count(readFile(lin.log), "level=ERROR msg=\"signed out:"); n != 1 {
		t.Errorf("the signed-out folder was reported %d times (a loop?)", n)
	}

	// shf login again: the running daemon takes the folder up again
	out = lin.shfIn(pw+"\n", "login", c.url, "--user", "bob", "--password-stdin", "--device-name", "linux")
	if !strings.Contains(out, lin.dir+": signed in again") {
		t.Errorf("login again:\n%s", out)
	}
	lin.revoked = false
	c.stored("linux-offline.txt", "waits for login")
	lin.has("mac-after.txt", "still syncs")
	c.converged(arrive)
	c.verifyClean()
}

// A folder joined with a member's token moves to the member's account at
// shf login --migrate while its daemon runs: the same folder, nothing
// fetched again, a key of the device's own; it goes on after the admin
// revokes the old token.
func TestLegacyFolderMovesToAccount(t *testing.T) {
	t.Parallel()
	c := newCluster(t)
	ana := c.joined("ana")
	bob := c.joined("bob")
	ana.write("a.txt", "a")
	bob.has("a.txt", "a")
	c.converged(arrive)
	before := inode(t, ana.abs("a.txt"))
	legacyKey := readFile(filepath.Join(ana.home, c.share, "id_ed25519.pub"))

	temp := c.account("ana")
	c.mustShfd("member", "link", c.share, "ana", "ana")
	out := ana.shfIn(temp+"\nanina-nova-lozinka\n", "login", c.url, "--user", "ana", "--password-stdin", "--device-name", "laptop", "--migrate")
	if !strings.Contains(out, ana.dir+": moved to the account") {
		t.Fatalf("login --migrate:\n%s", out)
	}
	if k := readFile(filepath.Join(ana.home, c.share, "id_ed25519.pub")); k == legacyKey {
		t.Error("the folder kept the member's key")
	}
	ak := readFile(c.ak)
	if !strings.Contains(ak, "shfd:ana/laptop@") || !strings.Contains(ak, "shfd:ana@") {
		t.Errorf("authorized_keys after the move (the legacy line stays until revoked):\n%s", ak)
	}
	eventually(t, arrive, "the daemon restarts the folder", func() error {
		if !strings.Contains(readFile(ana.log), "the folder's credentials changed; starting it again") {
			return errorf("not in its log yet")
		}
		return nil
	})

	// the admin revokes the old token; the folder goes on with the new one
	c.mustShfd("member", "reset-token", c.share, "ana")
	ana.write("after.txt", "after the move")
	bob.has("after.txt", "after the move")
	bob.write("from-bob.txt", "b")
	ana.has("from-bob.txt", "b")
	c.converged(arrive)
	if got := inode(t, ana.abs("a.txt")); got != before {
		t.Error("a.txt was fetched again after the move")
	}
	if out := ana.mustShf("status"); !strings.Contains(out, "account:    ana, member ana, device laptop") || !strings.Contains(out, "state:      ok") {
		t.Errorf("status after the move:\n%s", out)
	}
	for _, e := range c.eventsOn("after.txt") {
		if e.Member != "ana" {
			t.Errorf("after.txt by %q", e.Member)
		}
	}
	c.verifyClean()
}
