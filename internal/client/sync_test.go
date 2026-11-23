package client_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/digital-cube/shared-folder/internal/client"
)

func TestAddEditDelete(t *testing.T) {
	e := newEnv(t)
	ana, bob := e.joined("ana"), e.joined("bob")

	ana.write("a.txt", "one")
	ana.write("sub/deep/b.txt", "two")
	if r := ana.sync(); r.Sent != 4 { // mkdir sub, mkdir sub/deep, 2 puts
		t.Errorf("ana sent %d: %v", r.Sent, r.Items)
	}
	if got, _ := e.storage("sub/deep/b.txt"); got != "two" {
		t.Fatalf("server has %q", got)
	}
	r := bob.sync()
	bob.mustRead("a.txt", "one")
	bob.mustRead("sub/deep/b.txt", "two")
	if r.Received == 0 {
		t.Errorf("bob received nothing: %v", r.Items)
	}
	// the server's mtime came along
	fa, _ := os.Stat(ana.abs("a.txt"))
	fb, _ := os.Stat(bob.abs("a.txt"))
	if !fa.ModTime().Equal(fb.ModTime()) {
		t.Errorf("mtime: ana %v, bob %v", fa.ModTime(), fb.ModTime())
	}

	// edit, same size
	ana.write("a.txt", "uno")
	ana.sync()
	bob.sync()
	bob.mustRead("a.txt", "uno")

	// delete
	bob.remove("a.txt")
	bob.sync()
	if _, ok := e.storage("a.txt"); ok {
		t.Fatal("a.txt still on the server")
	}
	ana.sync()
	ana.mustNotExist("a.txt")
	sameTrees(t, ana, bob)

	// a second cycle has nothing to do
	if r := ana.sync(); r.Sent+r.Received+r.Conflicts != 0 {
		t.Errorf("idle cycle did work: %+v", r.Items)
	}
}

func TestMove(t *testing.T) {
	e := newEnv(t)
	ana, bob := e.joined("ana"), e.joined("bob")
	ana.write("docs/report.pdf", strings.Repeat("x", 4096))
	ana.sync()
	bob.sync()

	if err := os.MkdirAll(ana.abs("archive"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(ana.abs("docs/report.pdf"), ana.abs("archive/report-2026.pdf")); err != nil {
		t.Fatal(err)
	}
	r := ana.sync()
	if !hasItem(r, "↑ move", "archive/report-2026.pdf") {
		t.Fatalf("no move sent: %v", r.Items)
	}
	if hasItem(r, "↑ put", "archive/report-2026.pdf") || hasItem(r, "↑ delete", "docs/report.pdf") {
		t.Fatalf("move sent as put + delete: %v", r.Items)
	}
	r = bob.sync()
	if !hasItem(r, "↓ move", "archive/report-2026.pdf") {
		t.Errorf("bob did not rename locally: %v", r.Items)
	}
	bob.mustRead("archive/report-2026.pdf", strings.Repeat("x", 4096))
	bob.mustNotExist("docs/report.pdf")
	sameTrees(t, ana, bob)
}

func TestEmptyDirectory(t *testing.T) {
	e := newEnv(t)
	ana, bob := e.joined("ana"), e.joined("bob")
	if err := os.MkdirAll(ana.abs("empty/inner"), 0o755); err != nil {
		t.Fatal(err)
	}
	ana.sync()
	bob.sync()
	if fi, err := os.Stat(bob.abs("empty/inner")); err != nil || !fi.IsDir() {
		t.Fatalf("bob has no empty/inner: %v", err)
	}
	bob.remove("empty")
	bob.sync()
	ana.sync()
	ana.mustNotExist("empty")
	sameTrees(t, ana, bob)
}

var conflictRE = regexp.MustCompile(`^x\.conflict-bob-\d{8}T\d{6}Z\.txt$`)

func TestConflict(t *testing.T) {
	e := newEnv(t)
	ana, bob := e.joined("ana"), e.joined("bob")
	ana.write("x.txt", "base")
	ana.sync()
	bob.sync()

	ana.write("x.txt", "ana's version")
	bob.write("x.txt", "bob's version, longer")
	ana.sync()
	r := bob.sync()
	if r.Conflicts != 1 {
		t.Fatalf("bob: %d conflicts: %v", r.Conflicts, r.Items)
	}
	ana.sync()

	for _, m := range []*member{ana, bob} {
		m.mustRead("x.txt", "ana's version")
		entries, _ := os.ReadDir(m.dir)
		var copies []string
		for _, en := range entries {
			if conflictRE.MatchString(en.Name()) {
				copies = append(copies, en.Name())
			}
		}
		if len(copies) != 1 {
			t.Fatalf("%s: conflict copies %v", m.user, copies)
		}
		m.mustRead(copies[0], "bob's version, longer")
	}
	sameTrees(t, ana, bob)
}

func TestEditAgainstDelete(t *testing.T) {
	t.Run("delete first, then edit", func(t *testing.T) {
		e := newEnv(t)
		ana, bob := e.joined("ana"), e.joined("bob")
		ana.write("y.txt", "v1")
		ana.sync()
		bob.sync()

		ana.remove("y.txt")
		bob.write("y.txt", "v2 by bob")
		ana.sync() // the delete reaches the server first
		bob.sync() // the edit wins: the server restores the file
		if got, _ := e.storage("y.txt"); got != "v2 by bob" {
			t.Fatalf("server has %q", got)
		}
		ana.sync()
		ana.mustRead("y.txt", "v2 by bob")
		sameTrees(t, ana, bob)
	})
	t.Run("edit first, then delete", func(t *testing.T) {
		e := newEnv(t)
		ana, bob := e.joined("ana"), e.joined("bob")
		ana.write("y.txt", "v1")
		ana.sync()
		bob.sync()

		bob.write("y.txt", "v2 by bob")
		ana.remove("y.txt")
		bob.sync() // the edit reaches the server first
		ana.sync() // ana's delete would be stale: she gets the new version
		ana.mustRead("y.txt", "v2 by bob")
		if got, _ := e.storage("y.txt"); got != "v2 by bob" {
			t.Fatalf("server has %q", got)
		}
		sameTrees(t, ana, bob)
	})
}

func TestJoinNonEmptyFolderMerges(t *testing.T) {
	e := newEnv(t)
	ana := e.joined("ana")
	ana.write("both-same.txt", "same")
	ana.write("only-server.txt", "s")
	ana.write("differs.txt", "server side")
	ana.write("dir/kept.txt", "k")
	ana.sync()

	bob := e.newMember("bob")
	bob.write("both-same.txt", "same")
	bob.write("only-local.txt", "l")
	bob.write("differs.txt", "local side!")
	if err := os.MkdirAll(bob.abs("local-empty"), 0o755); err != nil {
		t.Fatal(err)
	}
	r := bob.join()
	if r.Conflicts != 1 {
		t.Errorf("conflicts = %d: %v", r.Conflicts, r.Items)
	}
	bob.mustRead("both-same.txt", "same")
	bob.mustRead("only-server.txt", "s")
	bob.mustRead("only-local.txt", "l")
	bob.mustRead("differs.txt", "server side")
	bob.mustRead("dir/kept.txt", "k")
	if got, _ := e.storage("only-local.txt"); got != "l" {
		t.Errorf("only-local.txt not sent: %q", got)
	}
	ana.sync()
	sameTrees(t, ana, bob)
	if _, err := os.Stat(ana.abs("local-empty")); err != nil {
		t.Errorf("empty local directory not sent: %v", err)
	}
}

func TestInterruptedCycleResumes(t *testing.T) {
	e := newEnv(t)
	ana := e.joined("ana")
	for i := range 6 {
		ana.write("f"+string(rune('0'+i))+".txt", strings.Repeat("z", i+1))
	}
	stop := errors.New("power cut")
	n := 0
	cut, err := client.Open(ana.c.Config(), client.Options{
		Transport: e.options().Transport,
		AfterApply: func(client.Action) error {
			if n++; n == 2 {
				return stop
			}
			return nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = cut.Sync(context.Background())
	cut.Close()
	if !errors.Is(err, stop) {
		t.Fatalf("interrupted sync: %v", err)
	}
	r := ana.sync()
	if r.Sent != 4 {
		t.Fatalf("resumed cycle sent %d, want the 4 that were left: %v", r.Sent, r.Items)
	}
	bob := e.joined("bob")
	sameTrees(t, ana, bob)
}

func TestRejectedNamesStayLocal(t *testing.T) {
	e := newEnv(t)
	ana, bob := e.joined("ana"), e.joined("bob")
	bob.write("README.md", "upper")
	bob.sync()
	ana.sync()
	ana.write("bad:name.txt", "x")
	ana.write("readme.md", "lower") // only the letter case differs from README.md
	ana.write("fine.txt", "ok")
	r := ana.sync()
	if len(r.Problems) != 2 {
		t.Fatalf("problems: %+v", r.Problems)
	}
	if _, ok := e.storage("bad:name.txt"); ok {
		t.Error("bad:name.txt reached the server")
	}
	if got, _ := e.storage("README.md"); got != "upper" {
		t.Errorf("README.md on the server = %q", got)
	}
	ana.mustRead("bad:name.txt", "x")
	ana.mustRead("readme.md", "lower")
	st, err := ana.c.Status(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(st.Problems) != 2 || st.Problems[0].Path != "bad:name.txt" || st.Problems[1].Path != "readme.md" {
		t.Errorf("status problems: %+v", st.Problems)
	}
}

func TestSymlinksAreSkipped(t *testing.T) {
	e := newEnv(t)
	ana, bob := e.joined("ana"), e.joined("bob")
	ana.write("real.txt", "r")
	if err := os.Symlink("real.txt", ana.abs("link.txt")); err != nil {
		t.Skip("no symlinks:", err)
	}
	if err := os.Symlink(os.TempDir(), ana.abs("linkdir")); err != nil {
		t.Fatal(err)
	}
	ana.sync()
	bob.sync()
	bob.mustRead("real.txt", "r")
	bob.mustNotExist("link.txt")
	bob.mustNotExist("linkdir")
}

func TestStatusAndLock(t *testing.T) {
	e := newEnv(t)
	ana, bob := e.joined("ana"), e.joined("bob")
	ana.write("a", "1")
	ana.write("b", "22")
	ana.sync()
	bob.write("mine", "3")
	st, err := bob.c.Status(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if st.Pending != 3 || st.Head == st.Seq || st.Last == nil || st.Busy {
		t.Errorf("status: pending %d, seq %d, head %d, last %v, busy %v", st.Pending, st.Seq, st.Head, st.Last, st.Busy)
	}
	bob.sync()
	st, _ = bob.c.Status(context.Background())
	if st.Pending != 0 || st.Head != st.Seq {
		t.Errorf("after sync: pending %d, seq %d, head %d", st.Pending, st.Seq, st.Head)
	}

	// a second client over the same folder waits for the first
	other, err := client.Open(bob.c.Config(), e.options())
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	inner := errors.New("checked")
	held, err := client.Open(bob.c.Config(), client.Options{Transport: e.options().Transport, AfterApply: func(client.Action) error {
		if _, err := other.Sync(context.Background()); !errors.Is(err, client.ErrBusy) {
			t.Errorf("concurrent sync: %v, want ErrBusy", err)
		}
		return inner
	}})
	if err != nil {
		t.Fatal(err)
	}
	defer held.Close()
	bob.write("trigger", "t")
	if _, err := held.Sync(context.Background()); !errors.Is(err, inner) {
		t.Fatalf("sync: %v", err)
	}
}

func TestRacyMtime(t *testing.T) {
	// two writes of the same size within one clock tick: the second is
	// still seen, because a fresh mtime is not trusted for the hash cache
	e := newEnv(t)
	ana, bob := e.joined("ana"), e.joined("bob")
	if err := os.WriteFile(ana.abs("r.txt"), []byte("aaaa"), 0o644); err != nil {
		t.Fatal(err)
	}
	ana.sync()
	fi, _ := os.Stat(ana.abs("r.txt"))
	if err := os.WriteFile(ana.abs("r.txt"), []byte("bbbb"), 0o644); err != nil {
		t.Fatal(err)
	}
	os.Chtimes(ana.abs("r.txt"), fi.ModTime(), fi.ModTime())
	ana.sync()
	bob.sync()
	bob.mustRead("r.txt", "bbbb")
}

func TestJoinChecks(t *testing.T) {
	e := newEnv(t)
	ana := e.joined("ana")
	ctx := context.Background()
	// token and key files are private
	for _, f := range []string{"token", "config.json", "state.db"} {
		fi, err := os.Stat(filepath.Join(ana.c.Config().Dir, f))
		if err != nil {
			t.Fatal(err)
		}
		if fi.Mode().Perm() != 0o600 {
			t.Errorf("%s has mode %v", f, fi.Mode().Perm())
		}
	}
	// the same share twice, a nested folder, a bad token
	_, _, err := client.Join(ctx, client.JoinRequest{Home: ana.home, URL: e.url, ShareID: e.share, Token: "x", Folder: t.TempDir()}, e.options())
	if err == nil || !strings.Contains(err.Error(), "already joined") {
		t.Errorf("second join of the share: %v", err)
	}
	_, _, err = client.Join(ctx, client.JoinRequest{Home: t.TempDir(), URL: e.url, ShareID: e.share, Token: "wrong", Folder: t.TempDir()}, e.options())
	if err == nil || !strings.Contains(err.Error(), "refused the token") {
		t.Errorf("bad token: %v", err)
	}
	id2, tok2, err := e.srv.CreateShare("other", "ana")
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = client.Join(ctx, client.JoinRequest{Home: ana.home, URL: e.url, ShareID: id2, Token: tok2, Folder: ana.abs("inner")}, e.options())
	if err == nil || !strings.Contains(err.Error(), "overlaps") {
		t.Errorf("nested folder: %v", err)
	}
	// Find works from inside the folder
	ana.write("d/x", "1")
	cfg, err := client.Find(ana.home, ana.abs("d"))
	if err != nil || cfg.ShareID != e.share {
		t.Errorf("Find: %v %v", cfg, err)
	}
}

// A cycle whose every round finds new work only because another member
// keeps changing the server is not a failure: everything it planned was
// done, and the newer changes are the next cycle's.
func TestBusyServerIsNoFailure(t *testing.T) {
	e := newEnv(t)
	ana, bob := e.joined("ana"), e.joined("bob")
	ana.write("f0.txt", "0")
	ana.sync()
	n := 0
	busy, err := client.Open(bob.c.Config(), client.Options{
		Transport:  e.options().Transport,
		MassDelete: &client.MassDelete{Files: -1, Percent: -1},
		AfterApply: func(client.Action) error {
			// ana adds a file after each of bob's steps
			n++
			ana.write(fmt.Sprintf("f%d.txt", n), fmt.Sprint(n))
			ana.sync()
			return nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer busy.Close()
	res, err := busy.Sync(context.Background())
	if err != nil {
		t.Fatalf("sync against a busy server: %v (%+v)", err, res)
	}
	head, err := ana.c.Head(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if res.Received < 5 || res.ToSeq >= head {
		t.Fatalf("the cycle was not cut off by the moving log: received %d, seq %d, head %d", res.Received, res.ToSeq, head)
	}
	if res.Pending != 0 {
		t.Errorf("pending %d", res.Pending)
	}
}
