package server

import (
	"bytes"
	"context"
	"errors"
	"image"
	"image/png"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func exists(p string) bool { _, err := os.Stat(p); return err == nil }

// With a separate db-root the databases go there and nothing else does.
func TestSeparateDBRoot(t *testing.T) {
	root, dbRoot := t.TempDir(), t.TempDir()
	srv, err := Open(root, Options{DBRoot: dbRoot})
	if err != nil {
		t.Fatal(err)
	}
	id, _, err := srv.CreateShare("tim", "ana")
	if err != nil {
		t.Fatal(err)
	}
	sh, _ := srv.Share(id)
	if _, err := sh.AddMember("bob", false); err != nil {
		t.Fatal(err)
	}
	if srv.DBRoot() != dbRoot || srv.Root() != root {
		t.Errorf("roots %s %s", srv.Root(), srv.DBRoot())
	}
	if got := sh.DBPath(); got != filepath.Join(dbRoot, id, DBFile) {
		t.Errorf("DBPath %s", got)
	}
	srv.Close()

	for _, p := range []string{
		filepath.Join(dbRoot, RegistryFile),
		filepath.Join(dbRoot, id, DBFile),
		filepath.Join(root, id, StorageDir),
		filepath.Join(root, id, TrashDir),
		filepath.Join(root, id, TmpDir),
		filepath.Join(root, id, logFile),
	} {
		if !exists(p) {
			t.Errorf("%s missing", p)
		}
	}
	for _, p := range []string{
		filepath.Join(root, RegistryFile),
		filepath.Join(root, id, DBFile),
		filepath.Join(dbRoot, id, StorageDir),
		filepath.Join(dbRoot, id, logFile),
	} {
		if exists(p) {
			t.Errorf("%s should not exist", p)
		}
	}

	// Reopened on the same roots, the folder and its members are there.
	srv, err = Open(root, Options{DBRoot: dbRoot})
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()
	sh, err = srv.Share(id)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := sh.MemberByName("bob"); err != nil {
		t.Error(err)
	}
}

// The default (no DBRoot, or DBRoot equal to the data-root) is the old
// layout: everything under the data-root.
func TestDefaultDBRootIsDataRoot(t *testing.T) {
	root := t.TempDir()
	for _, db := range []string{"", root} {
		srv, err := Open(root, Options{DBRoot: db})
		if err != nil {
			t.Fatal(err)
		}
		id, _, err := srv.CreateShare("tim"+db, "ana")
		if err != nil {
			t.Fatal(err)
		}
		if srv.DBRoot() != srv.Root() {
			t.Errorf("DBRoot %s, Root %s", srv.DBRoot(), srv.Root())
		}
		srv.Close()
		if !exists(filepath.Join(root, RegistryFile)) || !exists(filepath.Join(root, id, DBFile)) {
			t.Errorf("databases not in the data-root")
		}
	}
}

// A new, empty db-root next to a data-root that has its databases is
// refused: the server would come up without any folder.
func TestDBRootWithoutMoveRefused(t *testing.T) {
	root := t.TempDir()
	srv, err := Open(root, Options{})
	if err != nil {
		t.Fatal(err)
	}
	srv.Close()
	dbRoot := filepath.Join(t.TempDir(), "db")
	_, err = Open(root, Options{DBRoot: dbRoot})
	if err == nil || !strings.Contains(err.Error(), "shfd db move") {
		t.Fatalf("err = %v", err)
	}
	if exists(filepath.Join(dbRoot, RegistryFile)) {
		t.Error("an empty registry was created")
	}
}

func TestMoveDBs(t *testing.T) {
	root := t.TempDir()
	srv, err := Open(root, Options{})
	if err != nil {
		t.Fatal(err)
	}
	id, _, err := srv.CreateShare("tim", "ana")
	if err != nil {
		t.Fatal(err)
	}
	sh, _ := srv.Share(id)
	if _, err := sh.AddMember("bob", false); err != nil {
		t.Fatal(err)
	}
	// An account with a profile picture: the picture goes along.
	p := cheapPW
	srv.opts.PasswordParams = &p
	acct, _, err := srv.AddUser("bob", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := srv.SetAvatar(acct.ID, onePixelPNG(t)); err != nil {
		t.Fatal(err)
	}
	pic, _ := os.ReadFile(srv.avatarPath(acct.ID))
	// The server is still open, so the last commits sit in the WAL and
	// not in service.db: a copy of the file alone would miss them.
	if fi, err := os.Stat(sh.DBPath() + "-wal"); err != nil || fi.Size() == 0 {
		t.Fatalf("expected a non-empty WAL: %v %v", fi, err)
	}
	to := filepath.Join(t.TempDir(), "db")
	moved, err := MoveDBs(root, "", to)
	if err != nil {
		t.Fatal(err)
	}
	srv.Close()
	if len(moved) != 2 || moved[0].Name != "registry" || moved[1].Name != id {
		t.Fatalf("moved %+v", moved)
	}
	for _, m := range moved {
		if !exists(m.From) {
			t.Errorf("original %s was removed", m.From)
		}
	}
	if got, _ := MovedTo(root); got != to {
		t.Errorf("marker names %q, want %q", got, to)
	}

	// The copy has everything, the WAL's commits included.
	srv2, err := Open(root, Options{DBRoot: to})
	if err != nil {
		t.Fatal(err)
	}
	sh2, err := srv2.Share(id)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := sh2.MemberByName("bob"); err != nil {
		t.Errorf("bob not in the copy: %v", err)
	}
	if sh2.DBPath() != filepath.Join(to, id, DBFile) {
		t.Errorf("DBPath %s", sh2.DBPath())
	}
	if f, _, err := srv2.Avatar("bob"); err != nil {
		t.Errorf("picture not in the copy: %v", err)
	} else {
		got, _ := io.ReadAll(f)
		f.Close()
		if !bytes.Equal(got, pic) || len(pic) == 0 {
			t.Errorf("picture in the copy differs")
		}
	}
	srv2.Close()

	// Without the new db-root the data-root is refused, so nothing runs
	// on the old copies; so is another db-root.
	for _, db := range []string{"", t.TempDir()} {
		if _, err := Open(root, Options{DBRoot: db}); err == nil || !strings.Contains(err.Error(), "were moved to "+to) {
			t.Errorf("open with db-root %q: %v", db, err)
		}
	}

	// A target that already has databases is refused before any copy.
	if _, err := MoveDBs(root, to, to); err == nil {
		t.Error("move onto itself accepted")
	}
	to2 := t.TempDir()
	os.WriteFile(filepath.Join(to2, RegistryFile), []byte("x"), 0o600)
	if _, err := MoveDBs(root, to, to2); err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Errorf("move onto databases: %v", err)
	}
	// Moving from the wrong db-root is refused too.
	if _, err := MoveDBs(root, "", t.TempDir()); err == nil {
		t.Error("move from the stale copies accepted")
	}

	// Back into the data-root: the old copies are in the way.
	if _, err := MoveDBs(root, to, root); err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Errorf("move back over the old copies: %v", err)
	}
	os.Remove(filepath.Join(root, RegistryFile))
	os.Remove(filepath.Join(root, id, DBFile))
	os.Remove(filepath.Join(root, id, DBFile+"-wal"))
	os.Remove(filepath.Join(root, id, DBFile+"-shm"))
	if _, err := MoveDBs(root, to, root); err == nil || !strings.Contains(err.Error(), AvatarsDir+" already exists") {
		t.Errorf("move back over the old pictures: %v", err)
	}
	os.RemoveAll(filepath.Join(root, AvatarsDir))
	if _, err := MoveDBs(root, to, root); err != nil {
		t.Fatal(err)
	}
	if exists(filepath.Join(root, MovedMarker)) {
		t.Error("marker left after moving back")
	}
	srv3, err := Open(root, Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer srv3.Close()
	if _, err := srv3.Share(id); err != nil {
		t.Error(err)
	}
	if f, _, err := srv3.Avatar("bob"); err != nil {
		t.Errorf("picture after moving back: %v", err)
	} else {
		f.Close()
	}
}

func TestProbe(t *testing.T) {
	f := newFixture(t)
	sh, err := f.srv.Share(f.id)
	if err != nil {
		t.Fatal(err)
	}
	probe := func(d time.Duration) error {
		ctx, cancel := context.WithTimeout(context.Background(), d)
		defer cancel()
		return sh.Probe(ctx)
	}
	if err := probe(2 * time.Second); err != nil {
		t.Fatalf("idle folder: %v", err)
	}

	// A writer that does not let go of the lock is stuck.
	release := sh.HoldWriter()
	for i := 0; i < 3; i++ { // repeated checks wait on the same probe
		if err := probe(50 * time.Millisecond); !errors.Is(err, ErrStuck) {
			t.Fatalf("held lock: %v", err)
		}
	}
	// A verify holding it is not.
	end := sh.maintenance()
	if err := probe(50 * time.Millisecond); err != nil {
		t.Errorf("held by verify: %v", err)
	}
	end()
	release()
	if err := probe(2 * time.Second); err != nil {
		t.Errorf("after release: %v", err)
	}

	// A closed database is an error, not a hang.
	sh.db.Close()
	if err := probe(2 * time.Second); err == nil || errors.Is(err, ErrStuck) {
		t.Errorf("closed database: %v", err)
	}
}

func onePixelPNG(t *testing.T) []byte {
	t.Helper()
	var b bytes.Buffer
	if err := png.Encode(&b, image.NewGray(image.Rect(0, 0, 1, 1))); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}
