package httpapi_test

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/digital-cube/shared-folder/internal/server"
	"github.com/digital-cube/shared-folder/internal/server/httpapi"
)

// downloadsEnv is a server whose --downloads is dir ("" for none).
func downloadsEnv(t *testing.T, dir string) *httptest.Server {
	t.Helper()
	srv, err := server.Open(t.TempDir(), server.Options{})
	if err != nil {
		t.Fatal(err)
	}
	hs := httptest.NewServer(httpapi.New(srv, nil, httpapi.WithDownloads(dir)))
	t.Cleanup(func() {
		hs.Close()
		srv.Close()
	})
	return hs
}

func get(t *testing.T, url string) (*http.Response, []byte) {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp, b
}

// release writes what `make release` leaves in dist/: the packages, then
// SHA256SUMS. Each file is one second newer than the one before.
func release(t *testing.T, dir string, at time.Time, names ...string) map[string]string {
	t.Helper()
	sums := map[string]string{}
	var list string
	for i, n := range names {
		content := []byte("tar.gz of " + n)
		p := filepath.Join(dir, n)
		if err := os.WriteFile(p, content, 0o644); err != nil {
			t.Fatal(err)
		}
		mt := at.Add(time.Duration(i) * time.Second)
		if err := os.Chtimes(p, mt, mt); err != nil {
			t.Fatal(err)
		}
		h := sha256.Sum256(content)
		sums[n] = hex.EncodeToString(h[:])
		list += fmt.Sprintf("%s  %s\n", sums[n], n)
	}
	if err := os.WriteFile(filepath.Join(dir, httpapi.SumsFile), []byte(list), 0o644); err != nil {
		t.Fatal(err)
	}
	return sums
}

func TestDownloadsOff(t *testing.T) {
	hs := downloadsEnv(t, "")
	resp, b := get(t, hs.URL+"/v1/downloads")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("list: %d %s", resp.StatusCode, b)
	}
	var d httpapi.Downloads
	if err := json.Unmarshal(b, &d); err != nil {
		t.Fatal(err)
	}
	if d.Enabled || d.Packages == nil || len(d.Packages) != 0 || d.SHA256SUMS != "" {
		t.Errorf("list without --downloads: %s", b)
	}
	for _, p := range []string{"/downloads/SHA256SUMS", "/downloads/shared-folder-v1-linux-amd64.tar.gz"} {
		if resp, _ := get(t, hs.URL+p); resp.StatusCode != http.StatusNotFound {
			t.Errorf("GET %s without --downloads: %d", p, resp.StatusCode)
		}
	}
}

func TestDownloads(t *testing.T) {
	dir := t.TempDir()
	t0 := time.Date(2026, 11, 1, 10, 0, 0, 0, time.UTC)
	// An older release whose SHA256SUMS was overwritten by the newer one.
	release(t, dir, t0, "shared-folder-v0.1.0-linux-amd64.tar.gz")
	sums := release(t, dir, t0.Add(time.Hour),
		"shared-folder-v0.2.0-3-gabc1234-dirty-linux-amd64.tar.gz",
		"shared-folder-v0.2.0-3-gabc1234-dirty-darwin-amd64.tar.gz",
		"shared-folder-v0.2.0-3-gabc1234-dirty-darwin-arm64.tar.gz",
	)
	// Not served: other files, a directory with a package name, and a
	// symlink out of the directory.
	outside := filepath.Join(t.TempDir(), "secret")
	os.WriteFile(outside, []byte("secret"), 0o600)
	os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("x"), 0o644)
	os.Mkdir(filepath.Join(dir, "shared-folder-v9-linux-amd64.tar.gz"), 0o755)
	if err := os.Symlink(outside, filepath.Join(dir, "shared-folder-v8-linux-amd64.tar.gz")); err != nil {
		t.Fatal(err)
	}

	hs := downloadsEnv(t, dir)
	resp, b := get(t, hs.URL+"/v1/downloads")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("list: %d %s", resp.StatusCode, b)
	}
	if cc := resp.Header.Get("Cache-Control"); cc != "no-cache" {
		t.Errorf("list: Cache-Control %q", cc)
	}
	var d httpapi.Downloads
	if err := json.Unmarshal(b, &d); err != nil {
		t.Fatal(err)
	}
	if !d.Enabled || d.SHA256SUMS != "/downloads/SHA256SUMS" {
		t.Errorf("list: %s", b)
	}
	type row struct{ name, version, os, arch, sha string }
	want := []row{
		{"shared-folder-v0.2.0-3-gabc1234-dirty-darwin-arm64.tar.gz", "v0.2.0-3-gabc1234-dirty", "darwin", "arm64", sums["shared-folder-v0.2.0-3-gabc1234-dirty-darwin-arm64.tar.gz"]},
		{"shared-folder-v0.2.0-3-gabc1234-dirty-darwin-amd64.tar.gz", "v0.2.0-3-gabc1234-dirty", "darwin", "amd64", sums["shared-folder-v0.2.0-3-gabc1234-dirty-darwin-amd64.tar.gz"]},
		{"shared-folder-v0.2.0-3-gabc1234-dirty-linux-amd64.tar.gz", "v0.2.0-3-gabc1234-dirty", "linux", "amd64", sums["shared-folder-v0.2.0-3-gabc1234-dirty-linux-amd64.tar.gz"]},
		{"shared-folder-v0.1.0-linux-amd64.tar.gz", "v0.1.0", "linux", "amd64", ""},
	}
	if len(d.Packages) != len(want) {
		t.Fatalf("list: %d packages, want %d: %s", len(d.Packages), len(want), b)
	}
	for i, w := range want {
		p := d.Packages[i]
		got := row{p.Name, p.Version, p.OS, p.Arch, p.SHA256}
		if got != w {
			t.Errorf("package %d = %+v, want %+v", i, got, w)
		}
		if p.URL != "/downloads/"+w.name || p.Size != int64(len("tar.gz of "+w.name)) {
			t.Errorf("package %d: url %q size %d", i, p.URL, p.Size)
		}
	}

	// A package: the bytes, as an attachment, hashing to its SHA256SUMS line.
	name := want[0].name
	resp, b = get(t, hs.URL+"/downloads/"+name)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET package: %d %s", resp.StatusCode, b)
	}
	h := sha256.Sum256(b)
	if hex.EncodeToString(h[:]) != want[0].sha {
		t.Errorf("GET package: content %q does not match SHA256SUMS", b)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "application/gzip" {
		t.Errorf("GET package: Content-Type %q", ct)
	}
	if cd := resp.Header.Get("Content-Disposition"); cd != `attachment; filename="`+name+`"` {
		t.Errorf("GET package: Content-Disposition %q", cd)
	}

	resp, b = get(t, hs.URL+"/downloads/SHA256SUMS")
	if resp.StatusCode != http.StatusOK || resp.Header.Get("Content-Type") != "text/plain; charset=utf-8" {
		t.Errorf("GET SHA256SUMS: %d %q", resp.StatusCode, resp.Header.Get("Content-Type"))
	}
	if want := want[2].sha + "  " + want[2].name + "\n"; !strings.Contains(string(b), want) {
		t.Errorf("GET SHA256SUMS: %q lacks %q", b, want)
	}

	// HEAD and a range work (http.ServeContent).
	req, _ := http.NewRequest("HEAD", hs.URL+"/downloads/"+name, nil)
	if r, err := http.DefaultClient.Do(req); err != nil || r.StatusCode != http.StatusOK || r.ContentLength != int64(len("tar.gz of "+name)) {
		t.Errorf("HEAD package: %v %+v", err, r)
	}

	for _, p := range []string{
		"/downloads/notes.txt",
		"/downloads/shared-folder-v9-linux-amd64.tar.gz", // a directory
		"/downloads/shared-folder-v8-linux-amd64.tar.gz", // symlink out of dir
		"/downloads/shared-folder-v7-linux-amd64.tar.gz", // missing
		"/downloads/..%2Fsecret",
		"/downloads/",
	} {
		if resp, b := get(t, hs.URL+p); resp.StatusCode != http.StatusNotFound {
			t.Errorf("GET %s: %d %s", p, resp.StatusCode, b)
		}
	}
	req, _ = http.NewRequest("POST", hs.URL+"/downloads/"+name, nil)
	if r, err := http.DefaultClient.Do(req); err != nil || r.StatusCode == http.StatusOK {
		t.Errorf("POST package: %v %+v", err, r)
	}
}
