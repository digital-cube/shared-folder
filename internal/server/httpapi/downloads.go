package httpapi

// Packages for download: what `make release` leaves in dist/, copied by the
// admin into the directory of shfd serve --downloads (docs/install.md). No
// token, like the health check: the packages are the open-source client and
// server, and the Install page shows them before sign-in.
//
//	GET /v1/downloads          the packages, newest first (Downloads)
//	GET /downloads/{name}      one package, or SHA256SUMS

import (
	"bufio"
	"errors"
	"io/fs"
	"net/http"
	"os"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/digital-cube/shared-folder/internal/server"
)

// SumsFile is the checksum list of `make release`, served next to the
// packages.
const SumsFile = "SHA256SUMS"

// pkgName is the name `make release` gives a package:
// shared-folder-<version>-<os>-<arch>.tar.gz. The version may itself hold
// dashes (git describe: v0.1.0-3-gabc1234-dirty); os and arch may not.
var pkgName = regexp.MustCompile(`^shared-folder-([0-9A-Za-z][0-9A-Za-z._+-]*)-([a-z0-9]+)-([a-z0-9]+)\.tar\.gz$`)

// Package is one file of GET /v1/downloads.
type Package struct {
	Name     string    `json:"name"`
	Version  string    `json:"version"`
	OS       string    `json:"os"`   // GOOS: linux, darwin, …
	Arch     string    `json:"arch"` // GOARCH: amd64, arm64, …
	Size     int64     `json:"size"`
	Modified time.Time `json:"modified"`
	// SHA256 is the package's line in SHA256SUMS, "" when it has none. It
	// is what the file should hash to, not a hash computed by the server.
	SHA256 string `json:"sha256,omitempty"`
	URL    string `json:"url"` // path on this server: /downloads/<name>
}

// Downloads is the body of GET /v1/downloads.
type Downloads struct {
	// Enabled says shfd serve was given --downloads; without it Packages is
	// always empty.
	Enabled bool `json:"enabled"`
	// Packages are newest first (by modification time, then name).
	Packages []Package `json:"packages"`
	// SHA256SUMS is the path of the checksum list, "" when there is none.
	SHA256SUMS string `json:"sha256sums,omitempty"`
}

// WithDownloads serves the packages in dir (shfd serve --downloads). An
// empty dir turns the downloads off: the list is empty and every file 404.
func WithDownloads(dir string) Option { return func(a *api) { a.downloads = dir } }

func registerDownloads(mux *http.ServeMux, a *api) {
	mux.HandleFunc("GET /v1/downloads", a.listDownloads)
	mux.HandleFunc("GET /downloads/{name}", a.download)
}

// downloadable says whether name is a file of the downloads directory that
// may be served.
func downloadable(name string) bool { return name == SumsFile || pkgName.MatchString(name) }

func (a *api) listDownloads(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-cache")
	body := Downloads{Enabled: a.downloads != "", Packages: []Package{}}
	if a.downloads == "" {
		writeJSON(w, http.StatusOK, body)
		return
	}
	root, err := os.OpenRoot(a.downloads)
	if err != nil {
		a.log.Error("downloads directory unavailable", "dir", a.downloads, "err", err)
		writeErr(w, http.StatusInternalServerError, "internal", "downloads unavailable")
		return
	}
	defer root.Close()
	entries, err := fs.ReadDir(root.FS(), ".")
	if err != nil {
		a.log.Error("downloads directory unreadable", "dir", a.downloads, "err", err)
		writeErr(w, http.StatusInternalServerError, "internal", "downloads unavailable")
		return
	}
	sums := map[string]string{}
	for _, e := range entries {
		name := e.Name()
		if !downloadable(name) {
			continue
		}
		fi, err := root.Stat(name) // follows a symlink, but not out of the directory
		if err != nil || !fi.Mode().IsRegular() {
			continue
		}
		if name == SumsFile {
			body.SHA256SUMS = "/downloads/" + SumsFile
			sums = readSums(root, a)
			continue
		}
		m := pkgName.FindStringSubmatch(name)
		body.Packages = append(body.Packages, Package{
			Name: name, Version: m[1], OS: m[2], Arch: m[3],
			Size: fi.Size(), Modified: fi.ModTime().UTC(), URL: "/downloads/" + name,
		})
	}
	for i := range body.Packages {
		body.Packages[i].SHA256 = sums[body.Packages[i].Name]
	}
	sort.SliceStable(body.Packages, func(i, j int) bool {
		pi, pj := body.Packages[i], body.Packages[j]
		if !pi.Modified.Equal(pj.Modified) {
			return pi.Modified.After(pj.Modified)
		}
		return pi.Name < pj.Name
	})
	writeJSON(w, http.StatusOK, body)
}

// readSums parses SHA256SUMS ("<hex>  <name>", or "<hex> *<name>" in binary
// mode) into name → hex; lines it does not understand are skipped.
func readSums(root *os.Root, a *api) map[string]string {
	sums := map[string]string{}
	f, err := root.Open(SumsFile)
	if err != nil {
		a.log.Warn("downloads: SHA256SUMS unreadable", "err", err)
		return sums
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		hash, name, ok := strings.Cut(strings.TrimSpace(sc.Text()), " ")
		if !ok || len(hash) != 64 {
			continue
		}
		name = strings.TrimPrefix(strings.TrimLeft(name, " "), "*")
		sums[name] = strings.ToLower(hash)
	}
	return sums
}

func (a *api) download(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if a.downloads == "" || !downloadable(name) {
		writeErr(w, http.StatusNotFound, server.CodeNotFound, "no such download")
		return
	}
	f, err := os.OpenInRoot(a.downloads, name)
	if err != nil {
		if !errors.Is(err, fs.ErrNotExist) {
			a.log.Error("download unavailable", "name", name, "err", err)
		}
		writeErr(w, http.StatusNotFound, server.CodeNotFound, "no such download")
		return
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil || !fi.Mode().IsRegular() {
		writeErr(w, http.StatusNotFound, server.CodeNotFound, "no such download")
		return
	}
	h := w.Header()
	if name == SumsFile {
		h.Set("Content-Type", "text/plain; charset=utf-8")
	} else {
		h.Set("Content-Type", "application/gzip")
		h.Set("Content-Disposition", `attachment; filename="`+name+`"`)
	}
	h.Set("Cache-Control", "no-cache") // a new release may reuse the name (VERSION=…)
	h.Set("X-Content-Type-Options", "nosniff")
	http.ServeContent(w, r, name, fi.ModTime(), f)
}
