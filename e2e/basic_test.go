//go:build e2e

package e2e

import (
	"regexp"
	"testing"
)

// Scenario 1: what one client does reaches the other with no command: a
// new file, an edit, a move, an empty folder and a deletion. The deleted
// file waits in the server's trash under .deleted-by-<user>-<ts>, with its
// original path in the database.
func TestBasicFlow(t *testing.T) {
	t.Parallel()
	c := newCluster(t)
	ana, bob := c.joined("ana"), c.joined("bob")

	ana.write("docs/plan.txt", "v1")
	bob.has("docs/plan.txt", "v1")

	ana.write("docs/plan.txt", "v2, longer")
	bob.has("docs/plan.txt", "v2, longer")

	ana.rename("docs/plan.txt", "archive/plan-2026.txt")
	bob.has("archive/plan-2026.txt", "v2, longer")
	bob.gone("docs/plan.txt")
	moved := false
	for _, e := range c.events() {
		if e.Op == "move" && e.Old == "docs/plan.txt" && e.Path == "archive/plan-2026.txt" {
			moved = true
		}
	}
	if !moved {
		t.Errorf("no move event docs/plan.txt -> archive/plan-2026.txt: %+v", c.events())
	}

	ana.mkdir("empty/inner")
	bob.isDir("empty/inner")
	ana.remove("empty/inner")
	bob.gone("empty/inner")

	ana.remove("archive/plan-2026.txt")
	bob.gone("archive/plan-2026.txt")
	c.trashFor("archive/plan-2026.txt", "deleted", "ana", "v2, longer")

	// the other way round, too
	bob.write("from-bob.md", "hello")
	ana.has("from-bob.md", "hello")

	c.converged(arrive)
}

// Scenario 2: an edit sends the version it replaces to the trash as
// .replaced-by-<user>-<ts>.
func TestReplacedVersionGoesToTrash(t *testing.T) {
	t.Parallel()
	c := newCluster(t)
	ana, bob := c.joined("ana"), c.joined("bob")

	ana.write("budget.ods", "first version")
	bob.has("budget.ods", "first version")
	ana.write("budget.ods", "second version")
	bob.has("budget.ods", "second version")
	c.trashFor("budget.ods", "replaced", "ana", "first version")

	// bob's edit replaces ana's version in the same way
	bob.write("budget.ods", "third version")
	ana.has("budget.ods", "third version")
	e := c.trashFor("budget.ods", "replaced", "bob", "second version")
	if !regexp.MustCompile(`replaced-by-bob-`).MatchString(e.Name) {
		t.Errorf("trash name %q", e.Name)
	}
	c.converged(arrive)
}
