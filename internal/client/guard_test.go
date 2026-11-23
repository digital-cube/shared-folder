package client_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"testing"

	"github.com/digital-cube/shared-folder/internal/client"
)

// guarded is an env whose members run with the default mass-delete
// limits (more than 20 files or 10 % of the folder).
func guarded(t *testing.T) *env {
	e := newEnv(t)
	e.tune = func(o *client.Options) { o.MassDelete = nil }
	return e
}

func (e *env) head() int64 {
	e.t.Helper()
	sh, err := e.srv.Share(e.share)
	if err != nil {
		e.t.Fatal(err)
	}
	h, err := sh.Head()
	if err != nil {
		e.t.Fatal(err)
	}
	return h
}

func (m *member) fill(n int, dir string) {
	for i := range n {
		m.write(fmt.Sprintf("%sf%02d.txt", dir, i), fmt.Sprintf("file %d", i))
	}
}

func (m *member) status() *client.Status {
	m.t.Helper()
	s, err := m.c.Status(context.Background())
	if err != nil {
		m.t.Fatal(err)
	}
	return s
}

func TestMassDeleteLimits(t *testing.T) {
	for _, c := range []struct {
		g       client.MassDelete
		n, want int
		ok      bool
	}{
		{client.DefaultMassDelete, 1000, 20, true}, // 10 % = 100, 20 is less
		{client.DefaultMassDelete, 100, 10, true},
		{client.DefaultMassDelete, 30, 3, true},
		{client.DefaultMassDelete, 9, 0, true}, // a tiny folder blocks every deletion
		{client.MassDelete{Files: 5, Percent: -1}, 1000, 5, true},
		{client.MassDelete{Files: -1, Percent: 50}, 10, 5, true},
		{client.MassDelete{Files: -1, Percent: -1}, 10, -1, false},
	} {
		got, ok := c.g.Allowed(c.n)
		if got != c.want || ok != c.ok {
			t.Errorf("%+v.Allowed(%d) = %d, %v; want %d, %v", c.g, c.n, got, ok, c.want, c.ok)
		}
	}
}

func TestMassDeleteBlocksAndConfirmReleases(t *testing.T) {
	e := guarded(t)
	ana, bob := e.joined("ana"), e.joined("bob")
	ana.fill(30, "")
	ana.sync()
	bob.sync()
	head := e.head()

	// 25 of 30 deleted (limit: 3), plus an edit and a new file
	for i := range 25 {
		ana.remove(fmt.Sprintf("f%02d.txt", i))
	}
	ana.write("f29.txt", "edited")
	ana.write("new.txt", "new")
	r := ana.sync()
	if !r.Blocked {
		t.Fatalf("not blocked: %v", r.Items)
	}
	if !hasItem(r, "blocked", "") || !hasItem(r, "held", "f00.txt") {
		t.Errorf("block not in the log: %v", r.Items)
	}
	for i := range 25 {
		if _, ok := e.storage(fmt.Sprintf("f%02d.txt", i)); !ok {
			t.Fatalf("f%02d.txt was deleted on the server", i)
		}
	}
	if got, _ := e.storage("f29.txt"); got != "edited" {
		t.Errorf("edit not sent: %q", got)
	}
	if _, ok := e.storage("new.txt"); !ok {
		t.Error("new file not sent")
	}
	if got := e.head(); got != head+2 {
		t.Errorf("server events: %d new, want 2 (put, put)", got-head)
	}
	// the cycle log says so
	cs, err := ana.c.State().Cycles(1)
	if err != nil || len(cs) == 0 {
		t.Fatal(cs, err)
	}
	logged := false
	for _, it := range cs[0].Items {
		logged = logged || it.Op == "blocked"
	}
	if !logged {
		t.Errorf("block not in the local log: %v", cs[0].Items)
	}

	// status lists them; a further cycle keeps holding and does not loop
	s := ana.status()
	if !s.Blocked || len(s.Held) != 25 {
		t.Fatalf("status: blocked %v, %d held", s.Blocked, len(s.Held))
	}
	if s.Pending != 0 {
		t.Errorf("held deletions counted as pending: %d", s.Pending)
	}
	// while blocked, a single further deletion is held too
	ana.remove("f25.txt")
	if r := ana.sync(); !r.Blocked || r.Sent != 0 {
		t.Fatalf("second cycle: blocked %v, sent %d: %v", r.Blocked, r.Sent, r.Items)
	}
	if _, ok := e.storage("f25.txt"); !ok {
		t.Fatal("deletion sent while blocked")
	}
	bob.sync()
	bob.mustRead("f00.txt", "file 0")

	n, r, err := ana.c.Confirm(context.Background())
	if err != nil {
		t.Fatalf("confirm: %v (%v)", err, r.Items)
	}
	if n != 26 || r.Blocked {
		t.Fatalf("confirm released %d, blocked %v", n, r.Blocked)
	}
	for i := range 26 {
		if _, ok := e.storage(fmt.Sprintf("f%02d.txt", i)); ok {
			t.Fatalf("f%02d.txt still on the server after confirm", i)
		}
	}
	if s := ana.status(); s.Blocked || len(s.Held) != 0 {
		t.Fatalf("still blocked after confirm: %+v", s.Held)
	}
	bob.sync()
	sameTrees(t, ana, bob)
	if _, _, err := ana.c.Confirm(context.Background()); !errors.Is(err, client.ErrNothingHeld) {
		t.Errorf("confirm with nothing held: %v", err)
	}
}

func TestMassDeleteUnderTheLimitPasses(t *testing.T) {
	e := guarded(t)
	ana := e.joined("ana")
	ana.fill(30, "")
	ana.sync()
	for i := range 3 { // 10 % of 30
		ana.remove(fmt.Sprintf("f%02d.txt", i))
	}
	if r := ana.sync(); r.Blocked || r.Sent != 3 {
		t.Fatalf("blocked %v, sent %d: %v", r.Blocked, r.Sent, r.Items)
	}
}

func TestRestoreLocalBringsFilesBack(t *testing.T) {
	e := guarded(t)
	ana := e.joined("ana")
	ana.fill(25, "photos/")
	ana.write("keep.txt", "k")
	ana.sync()
	head := e.head()

	ana.remove("photos") // the whole directory
	r := ana.sync()
	if !r.Blocked {
		t.Fatalf("not blocked: %v", r.Items)
	}
	held, _ := ana.c.State().HeldDeletes()
	if len(held) != 26 { // 25 files and the directory
		t.Fatalf("%d held: %+v", len(held), held)
	}

	n, r, err := ana.c.RestoreLocal(context.Background())
	if err != nil {
		t.Fatalf("restore-local: %v (%v)", err, r.Items)
	}
	if n != 26 || r.Blocked {
		t.Fatalf("restored %d, blocked %v", n, r.Blocked)
	}
	for i := range 25 {
		ana.mustRead(fmt.Sprintf("photos/f%02d.txt", i), fmt.Sprintf("file %d", i))
	}
	if got := e.head(); got != head {
		t.Errorf("server log moved by %d events", got-head)
	}
	if s := ana.status(); s.Blocked || len(s.Held) != 0 || s.Pending != 0 {
		t.Fatalf("after restore: blocked %v, held %d, pending %d", s.Blocked, len(s.Held), s.Pending)
	}
}

func TestBlockEndsWhenFilesComeBack(t *testing.T) {
	e := guarded(t)
	ana := e.joined("ana")
	ana.fill(30, "")
	ana.sync()
	for i := range 10 {
		ana.remove(fmt.Sprintf("f%02d.txt", i))
	}
	if r := ana.sync(); !r.Blocked {
		t.Fatal("not blocked")
	}
	ana.fill(10, "") // the user puts them back
	if r := ana.sync(); r.Blocked {
		t.Fatalf("still blocked: %v", r.Items)
	}
}

func TestUnavailableRootSendsNoDeletes(t *testing.T) {
	e := guarded(t)
	ana := e.joined("ana")
	ana.fill(5, "")
	ana.sync()
	head := e.head()

	// the disk is gone
	moved := ana.dir + ".away"
	if err := os.Rename(ana.dir, moved); err != nil {
		t.Fatal(err)
	}
	_, err := ana.c.Sync(context.Background())
	if !errors.Is(err, client.ErrFolderUnavailable) {
		t.Fatalf("sync without the folder: %v", err)
	}
	// a file in its place
	if err := os.WriteFile(ana.dir, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := ana.c.Sync(context.Background()); !errors.Is(err, client.ErrFolderUnavailable) {
		t.Fatalf("sync with a file for the folder: %v", err)
	}
	os.Remove(ana.dir)
	// a folder that cannot be read
	if os.Geteuid() != 0 {
		if err := os.Mkdir(ana.dir, 0o000); err != nil {
			t.Fatal(err)
		}
		if _, err := ana.c.Sync(context.Background()); !errors.Is(err, client.ErrFolderUnavailable) {
			t.Fatalf("sync with an unreadable folder: %v", err)
		}
		os.Chmod(ana.dir, 0o755)
		os.Remove(ana.dir)
	}
	if got := e.head(); got != head {
		t.Fatalf("server log moved by %d events", got-head)
	}
	for i := range 5 {
		if _, ok := e.storage(fmt.Sprintf("f%02d.txt", i)); !ok {
			t.Fatalf("f%02d.txt deleted on the server", i)
		}
	}
	// back again: nothing to do
	if err := os.Rename(moved, ana.dir); err != nil {
		t.Fatal(err)
	}
	if r := ana.sync(); r.Sent != 0 || r.Received != 0 {
		t.Fatalf("cycle after the disk came back did work: %v", r.Items)
	}
}
