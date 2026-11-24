//go:build e2e

package e2e

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// Scenario: shfd serve --downloads publishes a package of its own version
// (dev, as these binaries are built); a client built as another version
// follows it: shf update replaces the binary, and shf daemon does the same
// on its own, starts the new binary in its place and goes on
// synchronizing.
func TestUpdateFollowsTheServer(t *testing.T) {
	t.Parallel()
	c := newCluster(t)

	// the package of the server's version: today's shf, as make release
	// packs it
	dl := t.TempDir()
	name := fmt.Sprintf("shared-folder-dev-%s-%s.tar.gz", runtime.GOOS, runtime.GOARCH)
	pkg := packShf(t, strings.TrimSuffix(name, ".tar.gz"), shfBin)
	if err := os.WriteFile(filepath.Join(dl, name), pkg, 0o644); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(pkg)
	os.WriteFile(filepath.Join(dl, "SHA256SUMS"), []byte(hex.EncodeToString(sum[:])+"  "+name+"\n"), 0o644)
	c.stopServer(false)
	c.serveArgs = []string{"--downloads", dl}
	c.startServer()

	// a client of another version
	oldBin := buildShf(t, "old0001")
	ana := c.newClient("ana")
	home := t.TempDir() // $HOME: no service installed there
	run := func(bin string, args ...string) (string, int) {
		t.Helper()
		cmd := exec.Command(bin, args...)
		cmd.Env = append(os.Environ(), append(ana.env(), "HOME="+home, "XDG_CONFIG_HOME="+filepath.Join(home, ".config"))...)
		cmd.Dir = ana.dir
		out, err := cmd.CombinedOutput()
		code := 0
		if ee := (*exec.ExitError)(nil); err != nil {
			if !errors.As(err, &ee) {
				t.Fatalf("%s %v: %v", bin, args, err)
			}
			code = ee.ExitCode()
		}
		return string(out), code
	}
	if out, code := run(oldBin, "join", c.url, c.share, ana.token, ana.dir); code != 0 {
		t.Fatalf("join: %d\n%s", code, out)
	}
	if out, _ := run(oldBin, "update", "--check"); !strings.Contains(out, "update available: shf old0001 → dev") {
		t.Fatalf("update --check:\n%s", out)
	}
	if out, _ := run(oldBin, "status"); !strings.Contains(out, "version:    shf old0001; server dev, update available") {
		t.Errorf("status:\n%s", out)
	}
	out, code := run(oldBin, "update", "--yes")
	if code != 0 || !strings.Contains(out, "installed shf dev at "+oldBin) {
		t.Fatalf("update: %d\n%s", code, out)
	}
	if out, _ := run(oldBin, "version"); !strings.HasPrefix(out, "shf dev\n") || !strings.Contains(out, "server "+c.url+": dev, up to date") {
		t.Fatalf("version after update:\n%s", out)
	}

	// shf daemon, on its own
	daemonBin := buildShf(t, "old0002")
	ana.daemon = startProc(t, "ana shf daemon", ana.log,
		append(ana.env(), "HOME="+home, "XDG_CONFIG_HOME="+filepath.Join(home, ".config")), daemonBin, "daemon", "-v")
	pid := ana.daemon.cmd.Process.Pid
	eventually(t, long, "the daemon restarted as dev", func() error {
		log := readFile(ana.log)
		if !strings.Contains(log, "auto-update: installed shf dev (was old0002)") {
			return fmt.Errorf("not updated yet")
		}
		if !strings.Contains(log, "shf daemon starting\" version=dev") {
			return fmt.Errorf("not restarted yet")
		}
		return nil
	})
	if !ana.daemon.running() || ana.daemon.cmd.Process.Pid != pid {
		t.Fatal("the daemon did not go on in the same process")
	}
	if out, _ := run(daemonBin, "version", "--short"); out != "shf dev\n" {
		t.Fatalf("daemon binary: %q", out)
	}
	ana.write("after-update.txt", "synchronized by the new shf")
	c.stored("after-update.txt", "synchronized by the new shf")
	if log := readFile(ana.log); strings.Count(log, "auto-update: installed") != 1 {
		t.Errorf("updated more than once:\n%s", log)
	}
}

// buildShf builds shf reporting version into a directory of its own.
func buildShf(t *testing.T, version string) string {
	t.Helper()
	dir := t.TempDir()
	cmd := exec.Command(goTool(), "build", "-trimpath",
		"-ldflags", "-X github.com/digital-cube/shared-folder/internal/version.Version="+version,
		"-o", filepath.Join(dir, "shf"), "./cmd/shf")
	cmd.Dir = ".."
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("building shf %s: %v\n%s", version, err, out)
	}
	return filepath.Join(dir, "shf")
}

// packShf is a package as make release makes it, with bin as its shf.
func packShf(t *testing.T, dir, bin string) []byte {
	t.Helper()
	b, err := os.ReadFile(bin)
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	tw.WriteHeader(&tar.Header{Name: dir + "/", Typeflag: tar.TypeDir, Mode: 0o755, ModTime: time.Now()})
	tw.WriteHeader(&tar.Header{Name: dir + "/shf", Typeflag: tar.TypeReg, Mode: 0o755, Size: int64(len(b)), ModTime: time.Now()})
	tw.Write(b)
	tw.Close()
	gz.Close()
	return buf.Bytes()
}
