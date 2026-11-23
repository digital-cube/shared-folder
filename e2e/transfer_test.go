//go:build e2e

package e2e

import (
	"math/rand/v2"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// bigFile writes size pseudo-random bytes outside every folder and
// returns its path and SHA-256.
func bigFile(t *testing.T, dir string, size int, seed byte) (string, string) {
	t.Helper()
	data := make([]byte, size)
	rand.NewChaCha8([32]byte{seed}).Read(data)
	p := filepath.Join(dir, "big-"+strconv.Itoa(int(seed)))
	if err := os.WriteFile(p, data, 0o644); err != nil {
		t.Fatal(err)
	}
	h, err := hashFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return p, h
}

// largest is the size of the biggest file in dir whose name matches re.
func largest(dir string, re *regexp.Regexp) int64 {
	var n int64
	des, _ := os.ReadDir(dir)
	for _, de := range des {
		if !re.MatchString(de.Name()) {
			continue
		}
		if fi, err := de.Info(); err == nil && fi.Size() > n {
			n = fi.Size()
		}
	}
	return n
}

var transferRE = regexp.MustCompile(`msg=transfer .*direction=(upload|download) .*size=(\d+) sent=(\d+) matched=(\d+)`)

// lastTransfer is the newest transfer of the direction in the client's
// log: how many bytes were sent and how many the other side already had.
func (cl *client) lastTransfer(direction string) (size, sent, matched int64, ok bool) {
	for _, l := range strings.Split(readFile(cl.log), "\n") {
		m := transferRE.FindStringSubmatch(l)
		if m == nil || m[1] != direction {
			continue
		}
		size, _ = strconv.ParseInt(m[2], 10, 64)
		sent, _ = strconv.ParseInt(m[3], 10, 64)
		matched, _ = strconv.ParseInt(m[4], 10, 64)
		ok = true
	}
	return
}

// Scenario 8: the upload of a 200 MB file is cut off halfway (the client
// is killed), continues when the client starts again without sending
// everything again, and the server's copy has the right hash. The same
// for the download at the other client.
func TestInterruptedTransfer(t *testing.T) {
	t.Parallel()
	const size = 200 << 20
	c := newCluster(t)
	ana, bob := c.newClient("ana"), c.newClient("bob")
	ana.join()
	bob.join() // bob's daemon starts only for the download
	src, hash := bigFile(t, c.base, size, 8)

	// upload, slowed down so it can be cut off in the middle
	ana.set("bwlimit", 25<<10) // 25 MiB/s: about 8 s for the file
	ana.start()
	if err := os.MkdirAll(ana.abs("video"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(src, ana.abs("video/talk.mkv")); err != nil {
		t.Fatal(err)
	}
	tmpDir := filepath.Join(c.shareDir(), "tmp")
	// rsync receives into .<id>.XXXXXX and keeps the partial file as <id>
	uploadRE := regexp.MustCompile(`^([0-9a-f]{32}|\.[0-9a-f]{32}\.\w+)$`)
	eventually(t, long, "a third of the upload arrived", func() error {
		if n := largest(tmpDir, uploadRE); n < size/3 {
			return errorf("%d bytes in tmp/", n)
		}
		return nil
	})
	ana.kill()
	partial := largest(tmpDir, uploadRE)
	if partial >= size || fileExists(c.storage("video/talk.mkv")) {
		t.Fatalf("the upload was not interrupted (%d of %d bytes)", partial, size)
	}
	t.Logf("upload cut off after %d of %d bytes", partial, size)

	ana.set("bwlimit", 0)
	ana.start()
	eventually(t, long, "the upload finished with the right content", func() error {
		h, err := hashFile(c.storage("video/talk.mkv"))
		if err != nil {
			return err
		}
		if h != hash {
			return errorf("hash %s, want %s", h, hash)
		}
		return nil
	})
	sz, sent, matched, ok := ana.lastTransfer("upload")
	t.Logf("upload resumed: sent %d, already there %d, of %d", sent, matched, sz)
	if !ok || sz != size || matched < partial/2 || sent > size-partial/2 {
		t.Errorf("the upload started over: sent %d, matched %d of %d (ok %v); %d bytes had arrived before", sent, matched, sz, ok, partial)
	}

	// download at bob, cut off the same way
	bob.set("bwlimit", 25<<10)
	bob.start()
	partRE := regexp.MustCompile(`^\.shf-tmp-part-` + hash + `$`)
	eventually(t, long, "a third of the download arrived", func() error {
		if n := largest(bob.abs("video"), partRE); n < size/3 {
			return errorf("%d bytes downloaded", n)
		}
		return nil
	})
	bob.kill()
	partial = largest(bob.abs("video"), partRE)
	if partial >= size || fileExists(bob.abs("video/talk.mkv")) {
		t.Fatalf("the download was not interrupted (%d of %d bytes)", partial, size)
	}
	t.Logf("download cut off after %d of %d bytes", partial, size)

	bob.set("bwlimit", 0)
	bob.start()
	eventually(t, long, "the download finished with the right content", func() error {
		h, err := hashFile(bob.abs("video/talk.mkv"))
		if err != nil {
			return err
		}
		if h != hash {
			return errorf("hash %s, want %s", h, hash)
		}
		return nil
	})
	sz, sent, matched, ok = bob.lastTransfer("download")
	t.Logf("download resumed: sent %d, already there %d, of %d", sent, matched, sz)
	if !ok || sz != size || matched < partial/2 || sent > size-partial/2 {
		t.Errorf("the download started over: sent %d, matched %d of %d (ok %v); %d bytes had arrived before", sent, matched, sz, ok, partial)
	}
	if n := countMatches(bob.dir, regexp.MustCompile(`\.shf-tmp-`)); n != 0 {
		t.Errorf("%d temporary files left at bob", n)
	}
	c.converged(long)
}
