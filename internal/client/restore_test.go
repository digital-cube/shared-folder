package client_test

import (
	"os"
	"testing"

	"github.com/digital-cube/shared-folder/internal/server"
)

// restoreTrash restores the newest trash entry of origPath in the name of
// the folder's admin, as shfd trash restore does.
func (e *env) restoreTrash(origPath, to string) *server.RestoreResult {
	e.t.Helper()
	sh, err := e.srv.Share(e.share)
	if err != nil {
		e.t.Fatal(err)
	}
	admin, err := sh.DefaultAdmin()
	if err != nil {
		e.t.Fatal(err)
	}
	l, err := sh.ListTrash(admin, server.TrashFilter{PathPrefix: origPath})
	if err != nil {
		e.t.Fatal(err)
	}
	if l.Count == 0 {
		e.t.Fatalf("no trash entry for %s", origPath)
	}
	res, err := sh.RestoreTrash(admin, l.Entries[l.Count-1].ID, to)
	if err != nil {
		e.t.Fatal(err)
	}
	return res
}

// A file restored from the trash reaches every client through an ordinary
// sync cycle, with its old mtime; a restore next to a file that came back
// meanwhile arrives as the ".restored-" copy and leaves that file alone.
func TestRestoredFileReachesClients(t *testing.T) {
	e := newEnv(t)
	ana, bob := e.joined("ana"), e.joined("bob")

	ana.write("izvestaji/q3.txt", "treci kvartal")
	ana.sync()
	bob.sync()
	before, err := os.Stat(bob.abs("izvestaji/q3.txt"))
	if err != nil {
		t.Fatal(err)
	}
	bob.remove("izvestaji")
	bob.sync()
	ana.sync()
	ana.mustNotExist("izvestaji")

	res := e.restoreTrash("izvestaji/q3.txt", "")
	if res.Path != "izvestaji/q3.txt" || res.Renamed {
		t.Fatalf("restore: %+v", res)
	}
	for _, m := range []*member{ana, bob} {
		r := m.sync()
		m.mustRead("izvestaji/q3.txt", "treci kvartal")
		if r.Received == 0 {
			t.Errorf("%s received nothing: %v", m.user, r.Items)
		}
		fi, _ := os.Stat(m.abs("izvestaji/q3.txt"))
		if !fi.ModTime().Equal(before.ModTime()) {
			t.Errorf("%s: mtime %v, want the original %v", m.user, fi.ModTime(), before.ModTime())
		}
	}
	sameTrees(t, ana, bob)

	// bob writes a new version over it; restoring the deleted one now
	// gives a restored copy beside bob's file.
	bob.write("izvestaji/q3.txt", "bobova verzija")
	bob.sync()
	res = e.restoreTrash("izvestaji/q3.txt", "")
	if !res.Renamed {
		t.Fatalf("restore onto a changed file: %+v", res)
	}
	for _, m := range []*member{ana, bob} {
		m.sync()
		m.mustRead("izvestaji/q3.txt", "bobova verzija")
		m.mustRead(res.Path, "treci kvartal")
	}
	sameTrees(t, ana, bob)
	if r := ana.sync(); r.Sent+r.Received+r.Conflicts != 0 {
		t.Errorf("idle cycle did work: %+v", r.Items)
	}
}
