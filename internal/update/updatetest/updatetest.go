// Package updatetest is a fake shfd for tests of shf update: /healthz with
// a version, /v1/downloads and /downloads/{name} with packages whose shf
// is a shell script that prints the version it was given.
package updatetest

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"sync"
	"testing"
)

// Package is an entry of GET /v1/downloads.
type Package struct {
	Name    string `json:"name"`
	Version string `json:"version"`
	OS      string `json:"os"`
	Arch    string `json:"arch"`
	Size    int64  `json:"size"`
	SHA256  string `json:"sha256,omitempty"`
	URL     string `json:"url"`
}

// Server is the fake shfd.
type Server struct {
	*httptest.Server

	mu        sync.Mutex
	version   string
	downloads bool
	pkgs      []Package
	files     map[string][]byte
	sums      map[string]string
	hits      map[string]int
}

// New starts a server reporting version, with downloads on.
func New(t testing.TB, version string) *Server {
	s := &Server{version: version, downloads: true, files: map[string][]byte{}, sums: map[string]string{}, hits: map[string]int{}}
	s.Server = httptest.NewServer(http.HandlerFunc(s.serve))
	t.Cleanup(s.Close)
	return s
}

// SetVersion changes what /healthz reports.
func (s *Server) SetVersion(v string) { s.mu.Lock(); s.version = v; s.mu.Unlock() }

// SetDownloads turns the packages on or off (shfd serve --downloads).
func (s *Server) SetDownloads(on bool) { s.mu.Lock(); s.downloads = on; s.mu.Unlock() }

// Hits says how often path was asked for.
func (s *Server) Hits(path string) int { s.mu.Lock(); defer s.mu.Unlock(); return s.hits[path] }

// Script is a stand-in shf: "shf version" prints "shf <v>".
func Script(v string) []byte {
	return []byte("#!/bin/sh\necho \"shf " + v + "\"\n")
}

// Tarball is a package as make release builds it, with bin as its shf.
func Tarball(name string, bin []byte) []byte {
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	dir := strings.TrimSuffix(name, ".tar.gz")
	tw.WriteHeader(&tar.Header{Name: dir + "/", Typeflag: tar.TypeDir, Mode: 0o755})
	for _, f := range []struct {
		name string
		body []byte
	}{{"README.md", []byte("readme\n")}, {"shfd", []byte("#!/bin/sh\necho shfd\n")}, {"shf", bin}} {
		tw.WriteHeader(&tar.Header{Name: dir + "/" + f.name, Typeflag: tar.TypeReg, Mode: 0o755, Size: int64(len(f.body))})
		tw.Write(f.body)
	}
	tw.Close()
	gz.Close()
	return buf.Bytes()
}

// AddPackage publishes the package of version for goos/goarch whose shf
// reports reports; it returns the package's name.
func (s *Server) AddPackage(version, goos, goarch, reports string) string {
	name := fmt.Sprintf("shared-folder-%s-%s-%s.tar.gz", version, goos, goarch)
	return s.AddFile(name, version, goos, goarch, Tarball(name, Script(reports)))
}

// AddFile publishes body as a package.
func (s *Server) AddFile(name, version, goos, goarch string, body []byte) string {
	sum := sha256.Sum256(body)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.files[name] = body
	s.sums[name] = hex.EncodeToString(sum[:])
	s.pkgs = append(s.pkgs, Package{Name: name, Version: version, OS: goos, Arch: goarch, Size: int64(len(body)),
		SHA256: s.sums[name], URL: "/downloads/" + name})
	return name
}

// SetSum changes the line of name in SHA256SUMS (and the list), as when
// the package was replaced or tampered with.
func (s *Server) SetSum(name, hexSum string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sums[name] = hexSum
	for i := range s.pkgs {
		if s.pkgs[i].Name == name {
			s.pkgs[i].SHA256 = hexSum
		}
	}
}

func (s *Server) serve(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.hits[r.URL.Path]++
	if r.Header.Get("Authorization") != "" {
		http.Error(w, "a token was sent", http.StatusBadRequest)
		return
	}
	switch p := r.URL.Path; {
	case p == "/healthz":
		json.NewEncoder(w).Encode(map[string]any{"service": "shfd", "version": s.version, "ok": true})
	case p == "/v1/downloads":
		body := map[string]any{"enabled": s.downloads, "packages": []Package{}}
		if s.downloads {
			body["packages"] = s.pkgs
			body["sha256sums"] = "/downloads/SHA256SUMS"
		}
		json.NewEncoder(w).Encode(body)
	case p == "/downloads/SHA256SUMS" && s.downloads:
		names := make([]string, 0, len(s.sums))
		for n := range s.sums {
			names = append(names, n)
		}
		sort.Strings(names)
		for _, n := range names {
			fmt.Fprintf(w, "%s  %s\n", s.sums[n], n)
		}
	case strings.HasPrefix(p, "/downloads/") && s.downloads && s.files[strings.TrimPrefix(p, "/downloads/")] != nil:
		w.Write(s.files[strings.TrimPrefix(p, "/downloads/")])
	default:
		w.WriteHeader(http.StatusNotFound)
		w.Write([]byte(`{"error":{"code":"not_found","message":"no such endpoint"}}`))
	}
}
