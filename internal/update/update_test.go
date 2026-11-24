//go:build unix

package update_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/digital-cube/shared-folder/internal/update"
	"github.com/digital-cube/shared-folder/internal/update/updatetest"
)

var ctx = context.Background()

func checker(current, goos, goarch string) *update.Checker {
	return &update.Checker{Current: current, GOOS: goos, GOARCH: goarch}
}

// TestCheckFollowsTheServer: the package is the one of the server's
// version for the client's platform, not the newest one.
func TestCheckFollowsTheServer(t *testing.T) {
	srv := updatetest.New(t, "abc1234")
	srv.AddPackage("fff9999", "linux", "amd64", "fff9999") // newer upload, other version
	want := srv.AddPackage("abc1234", "linux", "amd64", "abc1234")
	srv.AddPackage("abc1234", "darwin", "arm64", "abc1234")
	srv.AddPackage("0000aaa", "linux", "amd64", "0000aaa")

	c, err := checker("0000aaa", "linux", "amd64").Check(ctx, srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	if !c.Available() || c.Package.Name != want || c.ServerVersion != "abc1234" {
		t.Fatalf("check: %+v (package %+v)", c, c.Package)
	}
	if !strings.Contains(c.String(), "update available: shf 0000aaa → abc1234") {
		t.Errorf("String: %s", c.String())
	}

	c, err = checker("0000aaa", "darwin", "arm64").Check(ctx, srv.URL)
	if err != nil || c.Package == nil || c.Package.Name != "shared-folder-abc1234-darwin-arm64.tar.gz" {
		t.Fatalf("darwin/arm64: %+v %v", c, err)
	}

	c, err = checker("0000aaa", "darwin", "amd64").Check(ctx, srv.URL)
	if err != nil || c.Available() || !strings.Contains(c.String(), "no package of it for darwin/amd64") {
		t.Fatalf("darwin/amd64: %+v %v", c, err)
	}

	// already there: the list is not even asked for
	before := srv.Hits("/v1/downloads")
	c, err = checker("abc1234", "linux", "amd64").Check(ctx, srv.URL)
	if err != nil || !c.UpToDate() || c.Available() || !strings.HasPrefix(c.String(), "up to date") {
		t.Fatalf("up to date: %+v %v", c, err)
	}
	if srv.Hits("/v1/downloads") != before {
		t.Error("asked for the packages while up to date")
	}

	srv.SetDownloads(false)
	c, err = checker("0000aaa", "linux", "amd64").Check(ctx, srv.URL)
	if err != nil || c.Available() || !strings.Contains(c.String(), "publishes no packages") {
		t.Fatalf("no downloads: %+v %v", c, err)
	}

	if _, err := checker("x", "linux", "amd64").Check(ctx, "ftp://example.com"); err == nil {
		t.Error("ftp URL accepted")
	}
}

// fakeExe is an installed shf (a script) in a directory of its own.
func fakeExe(t *testing.T, v string) string {
	t.Helper()
	exe := filepath.Join(t.TempDir(), "shf")
	if err := os.WriteFile(exe, updatetest.Script(v), 0o755); err != nil {
		t.Fatal(err)
	}
	return exe
}

func dirNames(t *testing.T, dir string) []string {
	t.Helper()
	es, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, e := range es {
		out = append(out, e.Name())
	}
	return out
}

func TestInstall(t *testing.T) {
	srv := updatetest.New(t, "v2")
	srv.AddPackage("v2", "linux", "amd64", "v2")
	exe := fakeExe(t, "v1")
	c, err := checker("v1", "linux", "amd64").Check(ctx, srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	in := &update.Installer{Exe: exe}
	st, err := in.Prepare(ctx, c, "")
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Dir(st.Path) != filepath.Dir(exe) {
		t.Errorf("staged in %s, not next to %s", st.Path, exe)
	}
	if v, _ := update.BinaryVersion(ctx, exe); v != "v1" {
		t.Fatalf("Prepare changed the binary: %q", v)
	}
	if err := in.Install(st); err != nil {
		t.Fatal(err)
	}
	if v, err := update.BinaryVersion(ctx, exe); v != "v2" || err != nil {
		t.Fatalf("after install: %q %v", v, err)
	}
	if names := dirNames(t, filepath.Dir(exe)); len(names) != 1 {
		t.Errorf("left behind: %v", names)
	}
	fi, _ := os.Stat(exe)
	if fi.Mode().Perm() != 0o755 {
		t.Errorf("mode %v", fi.Mode())
	}
}

// refused runs Prepare against srv and expects an error containing want,
// with the binary and its directory untouched.
func refused(t *testing.T, srv *updatetest.Server, in *update.Installer, want string) {
	t.Helper()
	if in.Exe == "" {
		in.Exe = fakeExe(t, "v1")
	}
	c, err := checker("v1", "linux", "amd64").Check(ctx, srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	st, err := in.Prepare(ctx, c, "")
	if err == nil {
		in.Install(st)
		t.Fatalf("installed; want an error with %q", want)
	}
	if !strings.Contains(err.Error(), want) {
		t.Errorf("error %q, want %q", err, want)
	}
	if v, _ := update.BinaryVersion(ctx, in.Exe); v != "v1" {
		t.Errorf("binary changed to %q", v)
	}
	if names := dirNames(t, filepath.Dir(in.Exe)); len(names) != 1 {
		t.Errorf("left behind: %v", names)
	}
}

func TestWrongChecksumChangesNothing(t *testing.T) {
	srv := updatetest.New(t, "v2")
	name := srv.AddPackage("v2", "linux", "amd64", "v2")
	srv.SetSum(name, strings.Repeat("0", 64))
	refused(t, srv, &update.Installer{}, "does not match SHA256SUMS")
}

func TestNoChecksumChangesNothing(t *testing.T) {
	srv := updatetest.New(t, "v2")
	name := srv.AddPackage("v2", "linux", "amd64", "v2")
	srv.SetSum(name, "")
	refused(t, srv, &update.Installer{}, "lists no checksum")
}

func TestWrongVersionInsideChangesNothing(t *testing.T) {
	srv := updatetest.New(t, "v2")
	srv.AddPackage("v2", "linux", "amd64", "v9")
	refused(t, srv, &update.Installer{}, `reports version "v9"`)
}

func TestSizeLimits(t *testing.T) {
	srv := updatetest.New(t, "v2")
	srv.AddPackage("v2", "linux", "amd64", "v2")
	refused(t, srv, &update.Installer{MaxPackage: 100}, "limit of 100")
	refused(t, srv, &update.Installer{MaxBinary: 10}, "limit of 10")
}

func TestNotAPackage(t *testing.T) {
	srv := updatetest.New(t, "v2")
	srv.AddFile("shared-folder-v2-linux-amd64.tar.gz", "v2", "linux", "amd64", []byte("<html>not found</html>"))
	refused(t, srv, &update.Installer{}, "not a tar.gz")
}

func TestNotWritable(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root writes anywhere")
	}
	srv := updatetest.New(t, "v2")
	srv.AddPackage("v2", "linux", "amd64", "v2")
	exe := fakeExe(t, "v1")
	dir := filepath.Dir(exe)
	if err := os.Chmod(dir, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(dir, 0o755) })
	c, err := checker("v1", "linux", "amd64").Check(ctx, srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	in := &update.Installer{Exe: exe}
	_, err = in.Prepare(ctx, c, "")
	var nw *update.NotWritableError
	if !errors.As(err, &nw) || nw.Dir != dir || !strings.Contains(err.Error(), "is not writable") {
		t.Fatalf("err %v", err)
	}
	if srv.Hits("/downloads/shared-folder-v2-linux-amd64.tar.gz") != 0 {
		t.Error("downloaded although it could not be installed")
	}
	// staged elsewhere, as shf update then does
	st, err := in.Prepare(ctx, c, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if v, _ := update.BinaryVersion(ctx, st.Path); v != "v2" {
		t.Errorf("staged %q", v)
	}
	if v, _ := update.BinaryVersion(ctx, exe); v != "v1" {
		t.Errorf("binary changed to %q", v)
	}
}

func TestResolveSeveralServers(t *testing.T) {
	a := updatetest.New(t, "v2")
	b := updatetest.New(t, "v2")
	b.AddPackage("v2", "linux", "amd64", "v2") // only b publishes it
	ck := checker("v1", "linux", "amd64")

	c, err := ck.Resolve(ctx, update.Servers([]string{a.URL, b.URL + "/", a.URL}))
	if err != nil || !c.Available() || c.Server != b.URL {
		t.Fatalf("same version: %+v %v", c, err)
	}

	b.SetVersion("v3")
	_, err = ck.Resolve(ctx, update.Servers([]string{a.URL, b.URL}))
	var de *update.DisagreeError
	if !errors.As(err, &de) || de.Versions[a.URL] != "v2" || de.Versions[b.URL] != "v3" || !strings.Contains(err.Error(), "--server") {
		t.Fatalf("different versions: %v", err)
	}

	b.Close()
	_, err = ck.Resolve(ctx, update.Servers([]string{a.URL, b.URL}))
	if !errors.As(err, &de) || de.Errors[b.URL] == nil {
		t.Fatalf("one unreachable: %v", err)
	}

	if _, err := ck.Resolve(ctx, nil); !errors.Is(err, update.ErrNoServer) {
		t.Fatalf("no server: %v", err)
	}
}

func TestParseSums(t *testing.T) {
	h := strings.Repeat("a", 64)
	got := update.ParseSums([]byte(h + "  one.tar.gz\n" + strings.ToUpper(h) + " *two.tar.gz\nbad line\n"))
	if got["one.tar.gz"] != h || got["two.tar.gz"] != h || len(got) != 2 {
		t.Fatalf("%v", got)
	}
}
