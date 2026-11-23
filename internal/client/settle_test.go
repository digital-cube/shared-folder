package client_test

import (
	"os"
	"testing"
	"time"

	"github.com/digital-cube/shared-folder/internal/client"
)

// A file that vanished from a folder that is still changing is not
// reported deleted at once: the user may be in the middle of a move whose
// other end the scan has not seen yet. When it shows up, the two are a
// move; a real deletion goes once the folder has been still for Settle.
func TestDeletionWaitsForTheFolderToSettle(t *testing.T) {
	e := newEnv(t)
	now := time.Now()
	clock := func() time.Time { return now }
	e.tune = func(o *client.Options) { o.Settle = time.Minute; o.Now = clock }
	ana := e.joined("ana")
	ana.write("docs/a.txt", "moved in two scans")
	ana.write("docs/b.txt", "really deleted")
	ana.sync()

	// the first scan sees a.txt gone, but not yet where it went
	fi, _ := os.Stat(ana.abs("docs/a.txt"))
	ana.remove("docs/a.txt")
	ana.remove("docs/b.txt")
	now = time.Now()
	r := ana.sync()
	if r.Sent != 0 || r.Unsettled != 2 || r.RetryAt.IsZero() {
		t.Fatalf("deletions from a changing folder were sent: %+v", r)
	}
	if _, ok := e.storage("docs/a.txt"); !ok {
		t.Fatal("a.txt deleted on the server")
	}

	// the other end appears (same content, same mtime: a rename)
	ana.write("archive/a.txt", "moved in two scans")
	os.Chtimes(ana.abs("archive/a.txt"), fi.ModTime(), fi.ModTime())
	now = now.Add(time.Minute)
	r = ana.sync()
	if r.Unsettled != 0 {
		t.Fatalf("still unsettled: %+v", r)
	}
	var moved, deleted bool
	for _, it := range r.Items {
		moved = moved || (it.Op == "↑ move" && it.Path == "archive/a.txt")
		deleted = deleted || (it.Op == "↑ delete" && it.Path == "docs/b.txt")
	}
	if !moved || !deleted {
		t.Fatalf("want a move of a.txt and the deletion of b.txt: %v", r.Items)
	}
	if got, ok := e.storage("archive/a.txt"); !ok || got != "moved in two scans" {
		t.Errorf("archive/a.txt on the server: %q %v", got, ok)
	}
	if _, ok := e.storage("docs/b.txt"); ok {
		t.Error("b.txt is still on the server")
	}
}
