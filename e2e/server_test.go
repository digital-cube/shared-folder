//go:build e2e

package e2e

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"testing"
)

// Scenario 12: shfd trash restore brings a deleted file back, and it
// reaches both clients.
func TestTrashRestore(t *testing.T) {
	t.Parallel()
	c := newCluster(t)
	ana, bob := c.joined("ana"), c.joined("bob")
	ana.write("contracts/2026.pdf", "signed")
	bob.has("contracts/2026.pdf", "signed")
	ana.write("contracts/keep.pdf", "keeps the folder")
	bob.has("contracts/keep.pdf", "keeps the folder")

	bob.remove("contracts/2026.pdf")
	ana.gone("contracts/2026.pdf")
	e := c.trashFor("contracts/2026.pdf", "deleted", "bob", "signed")

	out := c.mustShfd("trash", "restore", c.share, fmt.Sprint(e.ID)) // through the running server's admin socket
	t.Logf("shfd trash restore:\n%s", out)
	ana.has("contracts/2026.pdf", "signed")
	bob.has("contracts/2026.pdf", "signed")
	restored := false
	for _, ev := range c.events() {
		if ev.Op == "restore" && ev.Path == "contracts/2026.pdf" {
			restored = true
		}
	}
	if !restored {
		t.Errorf("no restore event: %+v", c.events())
	}
	c.converged(arrive)
}

// Scenario 13: shfd is killed in the middle of an upload and started
// again. shfd verify finds nothing wrong, and the clients reconnect by
// themselves and finish synchronizing.
func TestServerCrash(t *testing.T) {
	t.Parallel()
	const size = 64 << 20
	c := newCluster(t)
	ana := c.newClient("ana")
	ana.set("bwlimit", 16<<10) // 16 MiB/s: about 4 s for the file
	ana.join()
	ana.start()
	bob := c.joined("bob")
	ana.write("small.txt", "before the crash")
	bob.has("small.txt", "before the crash")

	src, hash := bigFile(t, c.base, size, 13)
	if err := os.Rename(src, ana.abs("dump.bin")); err != nil {
		t.Fatal(err)
	}
	tmpDir := filepath.Join(c.shareDir(), "tmp")
	uploadRE := regexp.MustCompile(`^([0-9a-f]{32}|\.[0-9a-f]{32}\.\w+)$`)
	eventually(t, long, "the upload is under way", func() error {
		if n := largest(tmpDir, uploadRE); n < size/8 {
			return errorf("%d bytes in tmp/", n)
		}
		return nil
	})
	c.stopServer(true)
	if fileExists(c.storage("dump.bin")) {
		t.Fatal("the upload finished before the server was killed")
	}
	// work goes on at the clients while the server is down
	ana.write("while-down.txt", "written while the server was down")
	bob.write("bob-while-down.txt", "bob too")

	c.startServer()
	c.verifyClean()
	eventually(t, long, "the upload finished after the restart", func() error {
		h, err := hashFile(c.storage("dump.bin"))
		if err != nil {
			return err
		}
		if h != hash {
			return errorf("hash %s, want %s", h, hash)
		}
		return nil
	})
	bob.has("while-down.txt", "written while the server was down")
	ana.has("bob-while-down.txt", "bob too")
	eventually(t, long, "bob has dump.bin", func() error {
		h, err := hashFile(bob.abs("dump.bin"))
		if err != nil {
			return err
		}
		if h != hash {
			return errorf("hash %s", h)
		}
		return nil
	})
	c.converged(long)
}

// The databases are in the db-root, apart from the data (newCluster gives
// shfd its own temporary --db-root), and the whole stack works that way:
// the sync, rsync over SSH through rsync-gate (whose forced command carries
// --db-root), the admin socket and the health check.
func TestSeparateDBRoot(t *testing.T) {
	t.Parallel()
	c := newCluster(t)
	if c.dbRoot == c.root {
		t.Skip("E2E_DB_ROOT=same: the databases are in the data-root")
	}
	ana, bob := c.joined("ana"), c.joined("bob")
	ana.write("a/b.txt", "over ssh")
	bob.has("a/b.txt", "over ssh")

	for _, p := range []string{"registry.db", c.share + "/service.db", "serve.lock", "admin.sock"} {
		if _, err := os.Stat(filepath.Join(c.dbRoot, p)); err != nil {
			t.Errorf("db-root: %v", err)
		}
		if _, err := os.Stat(filepath.Join(c.root, p)); err == nil {
			t.Errorf("%s is in the data-root", p)
		}
	}
	for _, p := range []string{"storage/a/b.txt", "service.log"} {
		if _, err := os.Stat(filepath.Join(c.shareDir(), p)); err != nil {
			t.Errorf("data-root: %v", err)
		}
	}
	ak, _ := os.ReadFile(c.ak)
	if !regexp.MustCompile(`rsync-gate --data-root \S+ --db-root ` + regexp.QuoteMeta(c.dbRoot) + ` `).Match(ak) {
		t.Errorf("forced commands without --db-root:\n%s", ak)
	}
	if out := c.mustShfd("stats", c.share); !regexp.MustCompile(`(?m)^storage:\s+1 file`).MatchString(out) {
		t.Errorf("shfd stats:\n%s", out)
	}
	c.converged(arrive)
}
