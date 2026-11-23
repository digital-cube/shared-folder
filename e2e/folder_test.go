//go:build e2e

package e2e

import (
	"fmt"
	"path"
	"strings"
	"testing"
	"time"
)

// Scenario 6: bob is off while ana makes 200 changes (adds, edits,
// deletions, moves). When bob comes back his folder is identical to ana's.
func TestReturnAfterLongAbsence(t *testing.T) {
	t.Parallel()
	c := newCluster(t)
	ana, bob := c.joined("ana"), c.joined("bob")
	ana.write("before.txt", "both have this")
	bob.has("before.txt", "both have this")
	bob.stop()

	file := func(i int) string { return fmt.Sprintf("d%d/f%03d.txt", i%10, i) }
	for i := 0; i < 100; i++ { // 100 adds
		ana.write(file(i), fmt.Sprintf("v1 of %d", i))
	}
	t0 := time.Now()
	eventually(t, long, "ana's 100 new files reached the server", ana.sameAsStorage)
	t.Logf("100 adds reached the server in %s", time.Since(t0))

	for i := 0; i < 40; i++ { // 40 edits
		ana.write(file(i), fmt.Sprintf("v2 of %d, edited while bob was away", i))
	}
	for i := 40; i < 70; i++ { // 30 deletions
		ana.remove(file(i))
	}
	for i := 70; i < 100; i++ { // 30 moves
		ana.rename(file(i), "moved/"+file(i))
	}
	t0 = time.Now()
	eventually(t, long, "ana's 100 further changes reached the server", ana.sameAsStorage)
	t.Logf("100 edits, deletions and moves reached the server in %s", time.Since(t0))
	moves := 0
	for _, e := range c.events() {
		if e.Op == "move" {
			moves++
		}
	}
	if moves != 30 {
		t.Errorf("want 30 move events, got %d", moves)
	}

	bob.start()
	t0 = time.Now()
	c.converged(long)
	t.Logf("bob caught up in %s", time.Since(t0))
	a, _ := readTree(ana.dir, ana.skip)
	b, _ := readTree(bob.dir, bob.skip)
	if d := diff("ana", a, "bob", b); d != "" {
		t.Fatalf("bob's folder differs from ana's:\n  %s", d)
	}
}

// Scenario 7: .shfignore. A shared one at the root and one in a
// subfolder; ignored files do not reach the server; ignoring a file that
// is already synchronized deletes it neither on the server nor at bob; a
// .shfignore that lists itself stays ana's; a server file ana ignores
// locally is not fetched by her.
func TestShfignore(t *testing.T) {
	t.Parallel()
	c := newCluster(t)
	ana, bob := c.joined("ana"), c.joined("bob")
	shared := func(p string) bool {
		return path.Ext(p) == ".log" || p == "build" || strings.HasPrefix(p, "build/") ||
			p == "sub/secret.txt" || p == "notes.txt"
	}
	bob.skip = shared
	ana.skip = func(p string) bool {
		return shared(p) || p == "priv/.shfignore" || (strings.HasPrefix(p, "priv/") && path.Ext(p) == ".bak")
	}

	// shared rules at two levels
	ana.write(".shfignore", "*.log\nbuild/\n")
	ana.write("sub/.shfignore", "secret.txt\n")
	ana.write("app.log", "noise")
	ana.write("build/out.bin", "binary")
	ana.write("sub/secret.txt", "only mine")
	ana.write("sub/trace.log", "noise below")
	ana.write("secret.txt", "the subfolder's rule does not reach up here")
	ana.write("sub/ok.txt", "shared")
	bob.has("sub/ok.txt", "shared")
	bob.has("secret.txt", "the subfolder's rule does not reach up here")
	bob.has(".shfignore", "*.log\nbuild/\n")
	bob.has("sub/.shfignore", "secret.txt\n")
	for _, p := range []string{"app.log", "build", "sub/secret.txt", "sub/trace.log"} {
		if fileExists(c.storage(p)) {
			t.Errorf("ignored %s reached the server", p)
		}
		if _, ok := bob.read(p); ok || fileExists(bob.abs(p)) {
			t.Errorf("ignored %s reached bob", p)
		}
	}

	// ignoring a synchronized file is not deleting it
	ana.write("notes.txt", "shared notes")
	bob.has("notes.txt", "shared notes")
	ana.write(".shfignore", "*.log\nbuild/\nnotes.txt\n")
	bob.has(".shfignore", "*.log\nbuild/\nnotes.txt\n")
	ana.remove("notes.txt")
	ana.write("marker-1.txt", "after the local removal")
	bob.has("marker-1.txt", "after the local removal")
	if got := readFile(c.storage("notes.txt")); got != "shared notes" {
		t.Errorf("notes.txt on the server: %q", got)
	}
	if got, _ := bob.read("notes.txt"); got != "shared notes" {
		t.Errorf("notes.txt at bob: %q", got)
	}

	// a .shfignore that lists itself is ana's alone
	ana.write("priv/.shfignore", ".shfignore\n*.bak\n")
	ana.write("priv/draft.bak", "backup")
	ana.write("priv/keep.txt", "shared")
	bob.has("priv/keep.txt", "shared")
	for _, p := range []string{"priv/.shfignore", "priv/draft.bak"} {
		if fileExists(c.storage(p)) {
			t.Errorf("%s reached the server", p)
		}
		if fileExists(bob.abs(p)) {
			t.Errorf("%s reached bob", p)
		}
	}

	// what the server has and ana ignores locally, ana does not fetch
	bob.write("priv/remote.bak", "bob's backup")
	bob.write("priv/after.txt", "same batch")
	ana.has("priv/after.txt", "same batch")
	c.stored("priv/remote.bak", "bob's backup")
	if fileExists(ana.abs("priv/remote.bak")) {
		t.Error("ana fetched priv/remote.bak, which she ignores")
	}

	c.converged(arrive)
}

// Scenario 9: ana deletes 50 files at once. Nothing is deleted on the
// server, shf status shows the block; after shf confirm the deletions
// reach bob. Second case: shf restore-local brings the files back at ana.
func TestMassDelete(t *testing.T) {
	t.Parallel()
	for _, how := range []string{"confirm", "restore-local"} {
		t.Run(how, func(t *testing.T) {
			t.Parallel()
			c := newCluster(t)
			var ana, bob *client
			for _, name := range []string{"ana", "bob"} {
				cl := c.newClient(name)
				cl.set("mass_delete", map[string]any{"files": 20, "percent": 10}) // the specification's limits
				cl.join()
				cl.start()
				if name == "ana" {
					ana = cl
				} else {
					bob = cl
				}
			}
			photo := func(i int) string { return fmt.Sprintf("photos/p%02d.jpg", i) }
			for i := 0; i < 60; i++ {
				ana.write(photo(i), fmt.Sprintf("photo %d", i))
			}
			eventually(t, long, "bob has the 60 photos", func() error {
				if err := bob.sameAsStorage(); err != nil {
					return err
				}
				return ana.sameAsStorage()
			})

			for i := 0; i < 50; i++ {
				ana.remove(photo(i))
			}
			eventually(t, arrive, "shf status shows the block", func() error {
				out := ana.mustShf("status")
				if !strings.Contains(out, "state:      blocked: 50 deletions held back") {
					return errorf("status:\n%s", out)
				}
				return nil
			})
			for i := 0; i < 60; i++ {
				if !fileExists(c.storage(photo(i))) {
					t.Fatalf("%s was deleted on the server while blocked", photo(i))
				}
				if !fileExists(bob.abs(photo(i))) {
					t.Fatalf("%s was deleted at bob while blocked", photo(i))
				}
			}
			for _, e := range c.events() {
				if e.Op == "delete" {
					t.Fatalf("a delete reached the server while blocked: %+v", e)
				}
			}

			if how == "confirm" {
				out := ana.mustShf("confirm")
				if !strings.Contains(out, "released 50 held deletions") {
					t.Errorf("shf confirm:\n%s", out)
				}
				for i := 0; i < 50; i++ {
					bob.gone(photo(i))
				}
				bob.has(photo(59), "photo 59")
				deleted := 0
				for _, e := range c.trash() {
					if e.Reason == "deleted" && e.By == "ana" {
						deleted++
					}
				}
				if deleted != 50 {
					t.Errorf("want 50 deleted entries in the trash, got %d", deleted)
				}
			} else {
				out := ana.mustShf("restore-local")
				if !strings.Contains(out, "restoring 50 held paths") {
					t.Errorf("shf restore-local:\n%s", out)
				}
				for i := 0; i < 60; i++ {
					ana.has(photo(i), fmt.Sprintf("photo %d", i))
				}
				if len(c.trash()) != 0 {
					t.Errorf("trash: %+v", c.trash())
				}
			}
			if out := ana.mustShf("status"); !strings.Contains(out, "state:      ok") {
				t.Errorf("status after %s:\n%s", how, out)
			}
			c.converged(arrive)
		})
	}
}
