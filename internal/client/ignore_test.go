package client_test

import (
	"os"
	"testing"
)

func TestIgnoredFileIsNotSent(t *testing.T) {
	e := newEnv(t)
	ana, bob := e.joined("ana"), e.joined("bob")
	ana.write(".shfignore", "*.log\nbuild/\n")
	ana.write("debug.log", "noise")
	ana.write("build/out.bin", "bin")
	ana.write("keep.txt", "k")
	ana.write(".DS_Store", "finder") // built-in list
	ana.sync()
	for _, p := range []string{"debug.log", "build/out.bin", "build", ".DS_Store"} {
		if _, ok := e.storage(p); ok {
			t.Errorf("%s reached the server", p)
		}
	}
	bob.sync()
	bob.mustRead("keep.txt", "k")
	bob.mustRead(".shfignore", "*.log\nbuild/\n")
	bob.mustNotExist("debug.log")
	bob.mustNotExist("build")

	// changes and deletions of ignored files are not reported either
	ana.write("debug.log", "more noise")
	ana.remove("build")
	if r := ana.sync(); r.Sent != 0 {
		t.Errorf("ignored changes sent: %v", r.Items)
	}
}

func TestIgnoringASyncedFileDoesNotDeleteIt(t *testing.T) {
	e := newEnv(t)
	ana, bob := e.joined("ana"), e.joined("bob")
	ana.write("notes.txt", "shared notes")
	ana.sync()
	bob.sync()

	// a local-only ignore (it lists itself, so it is not synchronized)
	ana.write(".shfignore", ".shfignore\nnotes.txt\n")
	ana.remove("notes.txt")
	if r := ana.sync(); r.Sent != 0 {
		t.Errorf("ana sent: %v", r.Items)
	}
	if got, ok := e.storage("notes.txt"); !ok || got != "shared notes" {
		t.Fatalf("notes.txt on the server: %q %v", got, ok)
	}
	if _, ok := e.storage(".shfignore"); ok {
		t.Error("a self-ignored .shfignore reached the server")
	}
	bob.sync()
	bob.mustRead("notes.txt", "shared notes")

	// the server's version moves on; ana does not follow while ignoring
	bob.write("notes.txt", "shared notes v2")
	bob.sync()
	ana.sync()
	ana.mustNotExist("notes.txt")

	// the ignore ends: the file is fetched, not taken for a local delete
	ana.remove(".shfignore")
	ana.sync()
	ana.mustRead("notes.txt", "shared notes v2")
	if got, _ := e.storage("notes.txt"); got != "shared notes v2" {
		t.Errorf("server: %q", got)
	}
}

func TestLocallyIgnoredServerFileIsNotFetched(t *testing.T) {
	e := newEnv(t)
	ana, bob := e.joined("ana"), e.joined("bob")
	ana.write(".shfignore", ".shfignore\nbig.bin\nmine.dat\n")
	ana.write("mine.dat", "ana's own")
	ana.sync()

	bob.write("big.bin", "large content")
	bob.write("mine.dat", "bob's mine.dat")
	bob.sync()
	r := ana.sync()
	ana.mustNotExist("big.bin")
	ana.mustRead("mine.dat", "ana's own") // a local file of the same name is not touched
	if r.Received != 0 || r.Sent != 0 {
		t.Errorf("ana: %v", r.Items)
	}
	if got, _ := e.storage("mine.dat"); got != "bob's mine.dat" {
		t.Errorf("server mine.dat: %q", got)
	}

	ana.write(".shfignore", ".shfignore\nmine.dat\n") // big.bin no longer ignored
	ana.sync()
	ana.mustRead("big.bin", "large content")
}

func TestArrivingShfignoreAppliesToItsBatch(t *testing.T) {
	e := newEnv(t)
	ana, bob := e.joined("ana"), e.joined("bob")
	// bob sends x.log, then a .shfignore that ignores it (ignoring is not
	// deleting: x.log stays on the server)
	bob.write("x.log", "log")
	bob.write("sub/y.tmp2", "t")
	bob.sync()
	bob.write(".shfignore", "*.log\n*.tmp2\n")
	bob.sync()
	if _, ok := e.storage("x.log"); !ok {
		t.Fatal("x.log vanished from the server")
	}

	// ana sees all of it in one batch: the .shfignore counts first
	r := ana.sync()
	ana.mustRead(".shfignore", "*.log\n*.tmp2\n")
	ana.mustNotExist("x.log")
	ana.mustNotExist("sub/y.tmp2")
	if _, err := os.Stat(ana.abs("sub")); err != nil {
		t.Errorf("sub/ (not ignored) missing: %v", err)
	}
	if hasItem(r, "↓ download", "x.log") {
		t.Errorf("x.log was fetched: %v", r.Items)
	}
}

func TestRemovedDirectoryWithIgnoredFilesIsKeptLocally(t *testing.T) {
	e := newEnv(t)
	ana, bob := e.joined("ana"), e.joined("bob")
	ana.write("photos/a.jpg", "jpeg")
	ana.sync()
	bob.sync()
	bob.write("photos/.DS_Store", "finder") // ignored by the built-in list

	ana.remove("photos")
	ana.sync()
	r := bob.sync()
	bob.mustNotExist("photos/a.jpg")
	bob.mustRead("photos/.DS_Store", "finder") // never touched
	if !hasItem(r, "keep", "photos") {
		t.Errorf("bob: %v", r.Items)
	}
	// the kept directory is not sent back as new
	if r := bob.sync(); r.Sent != 0 {
		t.Errorf("bob sent: %v", r.Items)
	}
	ana.sync()
	ana.mustNotExist("photos")
}
