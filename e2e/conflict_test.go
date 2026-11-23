//go:build e2e

package e2e

import (
	"regexp"
	"sync"
	"testing"
)

// Scenario 3: ana and bob are both away from the server and edit the same
// file differently. When they come back (ana first), both end up with
// ana's version under the original name and bob's as the same conflict
// copy name.conflict-bob-<ts>.ext; no content is lost.
func TestConflictWhileOffline(t *testing.T) {
	t.Parallel()
	c := newCluster(t)
	ana, bob := c.joined("ana"), c.joined("bob")
	ana.write("notes/meeting.txt", "agenda")
	bob.has("notes/meeting.txt", "agenda")

	ana.stop()
	bob.stop()
	ana.write("notes/meeting.txt", "agenda + ana's minutes")
	bob.write("notes/meeting.txt", "agenda + bob's minutes, different")

	ana.start()
	c.stored("notes/meeting.txt", "agenda + ana's minutes")
	bob.start()

	conflictRE := regexp.MustCompile(`^notes/meeting\.conflict-bob-\d{8}T\d{6}Z(-\d+)?\.txt$`)
	var copyName string
	for _, cl := range []*client{ana, bob} {
		cl.has("notes/meeting.txt", "agenda + ana's minutes")
		eventuallyOneMatch(t, cl.dir, conflictRE)
	}
	copyName = onlyMatch(t, c.storageDir(), conflictRE)
	for _, cl := range []*client{ana, bob} {
		cl.has(copyName, "agenda + bob's minutes, different")
	}
	var conflicts int
	for _, e := range c.events() {
		if e.Op == "conflict" {
			conflicts++
		}
	}
	if conflicts != 1 {
		t.Errorf("want one conflict event, got %d: %+v", conflicts, c.events())
	}
	c.converged(arrive)
}

// eventuallyOneMatch waits until dir holds exactly one file matching re.
func eventuallyOneMatch(t *testing.T, dir string, re *regexp.Regexp) {
	t.Helper()
	eventually(t, arrive, "one file matching "+re.String()+" in "+dir, func() error {
		n := countMatches(dir, re)
		if n != 1 {
			return errorf("%d files match", n)
		}
		return nil
	})
}

// Scenario 4: ana and bob add a file of the same name and the same
// content at the same time: no conflict copy, one event on the server.
func TestSameContentAtOnce(t *testing.T) {
	t.Parallel()
	c := newCluster(t)
	ana, bob := c.joined("ana"), c.joined("bob")
	// both start from a synchronized folder
	ana.write("ready.txt", "x")
	bob.has("ready.txt", "x")

	var wg sync.WaitGroup
	for _, cl := range []*client{ana, bob} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			cl.write("report/final.pdf", "the same bytes at both")
		}()
	}
	wg.Wait()
	c.stored("report/final.pdf", "the same bytes at both")
	c.converged(arrive)

	if n := countMatches(c.storageDir(), regexp.MustCompile(`conflict`)); n != 0 {
		t.Errorf("%d conflict copies on the server", n)
	}
	var puts int
	for _, e := range c.eventsOn("report/final.pdf") {
		if e.Op != "put" {
			t.Errorf("unexpected event %+v", e)
		}
		puts++
	}
	if puts != 1 {
		t.Errorf("want one event for report/final.pdf, got %d: %+v", puts, c.eventsOn("report/final.pdf"))
	}
}

// Scenario 5: while both are away, ana deletes a file and bob edits it.
// The edit wins: the file stays with bob's content at both, whichever of
// them reaches the server first.
func TestEditAgainstDelete(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name        string
		deleteFirst bool
	}{{"delete reaches the server first", true}, {"edit reaches the server first", false}} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			c := newCluster(t)
			ana, bob := c.joined("ana"), c.joined("bob")
			ana.write("draft.txt", "original")
			bob.has("draft.txt", "original")

			ana.stop()
			bob.stop()
			ana.remove("draft.txt")
			bob.write("draft.txt", "bob's edit")

			first, second := ana, bob
			if !tc.deleteFirst {
				first, second = bob, ana
			}
			first.start()
			if tc.deleteFirst {
				eventually(t, arrive, "the delete reached the server", func() error {
					if fileExists(c.storage("draft.txt")) {
						return errorf("still in storage")
					}
					return nil
				})
			} else {
				c.stored("draft.txt", "bob's edit")
			}
			second.start()

			ana.has("draft.txt", "bob's edit")
			bob.has("draft.txt", "bob's edit")
			c.converged(arrive)
			if n := countMatches(c.storageDir(), regexp.MustCompile(`conflict`)); n != 0 {
				t.Errorf("%d conflict copies on the server", n)
			}
			// the original version is kept in the trash either way
			found := false
			for _, e := range c.trash() {
				if e.OrigPath == "draft.txt" && readFile(c.shareDir()+"/trash/"+e.Name) == "original" {
					found = true
				}
			}
			if !found {
				t.Errorf("the original version is not in the trash: %+v", c.trash())
			}
		})
	}
}
