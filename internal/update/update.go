// Package update replaces the shf binary with the package its server
// publishes (shf update, and shf daemon on its own; docs/install.md).
//
// The rule: a client follows its server. Versions are short git hashes
// (git describe), which cannot be ordered, so there is no "newest": the
// client looks for the package whose version is the version the server
// reports on GET /healthz, built for its own GOOS/GOARCH, in the list of
// GET /v1/downloads (shfd serve --downloads). When the client already is
// at that version, or the server publishes no such package, there is
// nothing to do. A client joined to folders on several servers that do
// not run the same version updates only when told which server to follow.
//
// Installing a package: it is downloaded without a token, within a time
// and size limit, hashed against the SHA256SUMS of the same server while
// the binary shf is taken out of it into a temporary file in the
// directory of the running binary, the new binary has to report the
// expected version (shf version --short), and only then is it renamed over
// the old one (os.Rename: atomic, and a running shf keeps its own copy).
// A file shf downloads itself carries no com.apple.quarantine, so macOS
// shows no Gatekeeper dialog for it.
package update

import (
	"archive/tar"
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// Limits of the network calls.
const (
	// CheckTimeout bounds GET /healthz and GET /v1/downloads.
	CheckTimeout = 30 * time.Second
	// DownloadTimeout bounds the download of a package.
	DownloadTimeout = 10 * time.Minute
	// MaxPackage is the largest package accepted (a package is about
	// 15 MB).
	MaxPackage = 256 << 20
	// MaxBinary is the largest shf taken out of a package.
	MaxBinary = 256 << 20
	// VersionTimeout bounds "<new shf> version --short".
	VersionTimeout = 30 * time.Second
)

// SumsFile is the checksum list next to the packages.
const SumsFile = "SHA256SUMS"

// tempPrefix starts the name of every file shf update leaves next to the
// binary while it works.
const tempPrefix = ".shf-update-"

// Package is one entry of GET /v1/downloads (internal/server/httpapi).
type Package struct {
	Name    string `json:"name"`
	Version string `json:"version"`
	OS      string `json:"os"`
	Arch    string `json:"arch"`
	Size    int64  `json:"size"`
	SHA256  string `json:"sha256,omitempty"`
	URL     string `json:"url"`
}

type downloads struct {
	Enabled    bool      `json:"enabled"`
	Packages   []Package `json:"packages"`
	SHA256SUMS string    `json:"sha256sums,omitempty"`
}

// Check is what a server offers this client.
type Check struct {
	Server        string // the server's base URL
	ServerVersion string // its /healthz version
	Current       string // this shf's version
	GOOS, GOARCH  string
	// Downloads says the server publishes packages (shfd serve
	// --downloads); without them Package is nil.
	Downloads bool
	// Package is the server's package of ServerVersion for GOOS/GOARCH,
	// nil when it has none.
	Package *Package
	// Sums is the path of the server's SHA256SUMS ("" when it has none).
	Sums string
}

// UpToDate says the client already runs the server's version.
func (c *Check) UpToDate() bool { return c.Current == c.ServerVersion }

// Available says there is a package to install.
func (c *Check) Available() bool { return !c.UpToDate() && c.Package != nil }

// String says in one line what the check found.
func (c *Check) String() string {
	switch {
	case c.UpToDate():
		return fmt.Sprintf("up to date: shf %s, server %s runs %s", c.Current, c.Server, c.ServerVersion)
	case !c.Downloads:
		return fmt.Sprintf("server %s runs %s (this shf: %s) but publishes no packages (shfd serve --downloads); nothing to install",
			c.Server, c.ServerVersion, c.Current)
	case c.Package == nil:
		return fmt.Sprintf("server %s runs %s (this shf: %s) but has no package of it for %s/%s; nothing to install",
			c.Server, c.ServerVersion, c.Current, c.GOOS, c.GOARCH)
	}
	return fmt.Sprintf("update available: shf %s → %s (%s, from %s)", c.Current, c.ServerVersion, c.Package.Name, c.Server)
}

// Checker asks servers what they offer.
type Checker struct {
	// HTTP talks to the servers; nil is a client without a timeout of its
	// own (every call has a deadline anyway).
	HTTP *http.Client
	// Current is this shf's version (internal/version.Version).
	Current      string
	GOOS, GOARCH string
}

func (ck *Checker) client() *http.Client {
	if ck.HTTP != nil {
		return ck.HTTP
	}
	return http.DefaultClient
}

// get fetches base+p into at most limit bytes. Any status other than
// 200 is an error, except that okStatus lets /healthz answer 503 (an
// unhealthy server still reports its version).
func (ck *Checker) get(ctx context.Context, base, p string, limit int64, okStatus ...int) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(base, "/")+p, nil)
	if err != nil {
		return nil, err
	}
	resp, err := ck.client().Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	ok := resp.StatusCode == http.StatusOK
	for _, s := range okStatus {
		ok = ok || resp.StatusCode == s
	}
	if !ok {
		return nil, fmt.Errorf("GET %s: %s", p, resp.Status)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return nil, fmt.Errorf("GET %s: %w", p, err)
	}
	if int64(len(b)) > limit {
		return nil, fmt.Errorf("GET %s: answer larger than %d bytes", p, limit)
	}
	return b, nil
}

// ServerVersion is the version base reports on GET /healthz.
func (ck *Checker) ServerVersion(ctx context.Context, base string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, CheckTimeout)
	defer cancel()
	b, err := ck.get(ctx, base, "/healthz", 64<<10, http.StatusServiceUnavailable)
	if err != nil {
		return "", fmt.Errorf("%s: %w", base, err)
	}
	var h struct {
		Service string `json:"service"`
		Version string `json:"version"`
	}
	if err := json.Unmarshal(b, &h); err != nil || h.Version == "" {
		return "", fmt.Errorf("%s: /healthz reports no version", base)
	}
	return h.Version, nil
}

// Check asks base for its version and the package of it for this
// platform.
func (ck *Checker) Check(ctx context.Context, base string) (*Check, error) {
	base = strings.TrimRight(base, "/")
	if err := checkURL(base); err != nil {
		return nil, err
	}
	sv, err := ck.ServerVersion(ctx, base)
	if err != nil {
		return nil, err
	}
	return ck.checkAt(ctx, base, sv)
}

func (ck *Checker) checkAt(ctx context.Context, base, sv string) (*Check, error) {
	c := &Check{Server: base, ServerVersion: sv, Current: ck.Current, GOOS: ck.GOOS, GOARCH: ck.GOARCH}
	if c.UpToDate() {
		return c, nil
	}
	ctx, cancel := context.WithTimeout(ctx, CheckTimeout)
	defer cancel()
	b, err := ck.get(ctx, base, "/v1/downloads", 4<<20, http.StatusNotFound)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", base, err)
	}
	var d downloads
	if json.Unmarshal(b, &d) != nil {
		// an older server without /v1/downloads answers 404 with an error
		// body: no packages
		return c, nil
	}
	c.Downloads, c.Sums = d.Enabled, d.SHA256SUMS
	for _, p := range d.Packages {
		if p.Version == sv && p.OS == ck.GOOS && p.Arch == ck.GOARCH {
			p := p
			c.Package = &p
			break
		}
	}
	return c, nil
}

func checkURL(base string) error {
	u, err := url.Parse(base)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
		return fmt.Errorf("invalid server URL %q: want https://host[:port]", base)
	}
	return nil
}

// ErrNoServer: the client is joined to no folder, so it has no server to
// follow.
var ErrNoServer = errors.New("no joined folder, so no server to follow; name one with --server URL")

// ServerVersions is the version of each server, for DisagreeError.
type ServerVersions map[string]string

// DisagreeError: the servers of the joined folders run different
// versions (or not all of them answered), so the client does not know
// which to follow.
type DisagreeError struct {
	Versions ServerVersions
	Errors   map[string]error // servers that did not answer
}

func (e *DisagreeError) Error() string {
	var parts []string
	for _, s := range sortedKeys(e.Versions) {
		parts = append(parts, s+" runs "+e.Versions[s])
	}
	for _, s := range sortedKeys(e.Errors) {
		parts = append(parts, s+" did not answer ("+e.Errors[s].Error()+")")
	}
	return "the servers of the joined folders do not agree on a version (" + strings.Join(parts, "; ") +
		"); name the one to follow with --server URL"
}

func sortedKeys[V any](m map[string]V) []string {
	ks := make([]string, 0, len(m))
	for k := range m {
		ks = append(ks, k)
	}
	sort.Strings(ks)
	return ks
}

// Resolve checks the servers of the joined folders: all of them must
// answer and run the same version. The first that publishes a package of
// it for this platform is the one to install from; when none does, the
// check of the first is returned (nothing to install).
func (ck *Checker) Resolve(ctx context.Context, servers []string) (*Check, error) {
	if len(servers) == 0 {
		return nil, ErrNoServer
	}
	versions := ServerVersions{}
	errs := map[string]error{}
	var mu sync.Mutex
	var wg sync.WaitGroup
	for _, s := range servers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			v, err := ck.ServerVersion(ctx, s)
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				errs[s] = err
			} else {
				versions[s] = v
			}
		}()
	}
	wg.Wait()
	if len(servers) == 1 && len(errs) == 1 {
		return nil, errs[servers[0]]
	}
	distinct := map[string]bool{}
	for _, v := range versions {
		distinct[v] = true
	}
	if len(errs) > 0 || len(distinct) > 1 {
		return nil, &DisagreeError{Versions: versions, Errors: errs}
	}
	var first *Check
	for _, s := range servers {
		c, err := ck.checkAt(ctx, s, versions[s])
		if err != nil {
			if first == nil && len(servers) == 1 {
				return nil, err
			}
			continue
		}
		if c.UpToDate() || c.Available() {
			return c, nil
		}
		if first == nil {
			first = c
		}
	}
	if first == nil {
		return nil, fmt.Errorf("no server answered GET /v1/downloads")
	}
	return first, nil
}

// ---- installing --------------------------------------------------------------

// NotWritableError: shf cannot create files next to the binary, so it
// cannot replace it.
type NotWritableError struct {
	Dir string
	Err error
}

func (e *NotWritableError) Error() string {
	return fmt.Sprintf("cannot replace shf: %s is not writable for you (%v)", e.Dir, e.Err)
}

func (e *NotWritableError) Unwrap() error { return e.Err }

// Installer downloads, verifies and installs packages.
type Installer struct {
	// HTTP downloads; nil is http.DefaultClient (every call has a
	// deadline: DownloadTimeout).
	HTTP *http.Client
	// Exe is the absolute path of the binary to replace.
	Exe string
	// MaxPackage and MaxBinary override the limits (0: the defaults).
	MaxPackage, MaxBinary int64
	// Version runs a binary and returns the version it reports; nil runs
	// "<bin> version --short".
	Version func(ctx context.Context, bin string) (string, error)
}

// Staged is a verified new binary waiting to be installed.
type Staged struct {
	Path    string // the new binary (a temporary file)
	Version string
	From    *Check
}

// Discard removes the staged binary.
func (s *Staged) Discard() {
	if s != nil && s.Path != "" {
		os.Remove(s.Path)
	}
}

// Writable checks that files can be created in dir (the directory of the
// binary), which replacing the binary needs; its error is a
// *NotWritableError.
func Writable(dir string) error {
	f, err := os.CreateTemp(dir, tempPrefix+"probe-*")
	if err != nil {
		var pe *fs.PathError
		if errors.As(err, &pe) {
			err = pe.Err
		}
		return &NotWritableError{Dir: dir, Err: err}
	}
	f.Close()
	os.Remove(f.Name())
	return nil
}

// Prepare downloads the package of c and stages its shf in dir ("" is
// the directory of Exe, which must then be writable): checked against the
// server's SHA256SUMS and reporting the server's version. Nothing else is
// changed.
func (in *Installer) Prepare(ctx context.Context, c *Check, dir string) (*Staged, error) {
	if !c.Available() {
		return nil, errors.New(c.String())
	}
	if dir == "" {
		dir = filepath.Dir(in.Exe)
		if err := Writable(dir); err != nil {
			return nil, err
		}
		cleanStale(dir)
	}
	ck := &Checker{HTTP: in.HTTP}
	ctx, cancel := context.WithTimeout(ctx, DownloadTimeout)
	defer cancel()

	sumsPath := c.Sums
	if sumsPath == "" {
		sumsPath = "/downloads/" + SumsFile
	}
	sb, err := ck.get(ctx, c.Server, sumsPath, 1<<20)
	if err != nil {
		return nil, fmt.Errorf("%s: no checksums to verify the package against: %w", c.Server, err)
	}
	want := ParseSums(sb)[c.Package.Name]
	if want == "" {
		return nil, fmt.Errorf("%s: %s lists no checksum of %s; not installing it", c.Server, SumsFile, c.Package.Name)
	}
	if c.Package.SHA256 != "" && !strings.EqualFold(c.Package.SHA256, want) {
		return nil, fmt.Errorf("%s: the checksum of %s in the package list differs from %s; not installing it", c.Server, c.Package.Name, SumsFile)
	}

	p := c.Package.URL
	if p == "" {
		p = "/downloads/" + c.Package.Name
	}
	if !strings.HasPrefix(p, "/") {
		return nil, fmt.Errorf("%s: package URL %q is not a path on the server", c.Server, p)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.Server+p, nil)
	if err != nil {
		return nil, err
	}
	resp, err := ck.client().Do(req)
	if err != nil {
		return nil, fmt.Errorf("downloading %s: %w", c.Package.Name, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("downloading %s: %s", c.Package.Name, resp.Status)
	}
	maxPkg := in.MaxPackage
	if maxPkg <= 0 {
		maxPkg = MaxPackage
	}
	if resp.ContentLength > maxPkg {
		return nil, fmt.Errorf("downloading %s: %d bytes, more than the limit of %d", c.Package.Name, resp.ContentLength, maxPkg)
	}

	tmp, err := os.CreateTemp(dir, tempPrefix+"*")
	if err != nil {
		return nil, err
	}
	st := &Staged{Path: tmp.Name(), Version: c.ServerVersion, From: c}
	ok := false
	defer func() {
		if !ok {
			tmp.Close()
			st.Discard()
		}
	}()

	// One pass: what is read from the network is hashed and unpacked at
	// once; the binary counts only once the whole package hashed right.
	h := sha256.New()
	body := &limited{r: resp.Body, n: maxPkg}
	tee := io.TeeReader(body, h)
	if err := extract(tee, tmp, in.maxBinary()); err != nil {
		if body.over {
			return nil, fmt.Errorf("downloading %s: larger than the limit of %d bytes", c.Package.Name, maxPkg)
		}
		return nil, fmt.Errorf("%s: %w", c.Package.Name, err)
	}
	if _, err := io.Copy(io.Discard, tee); err != nil {
		return nil, fmt.Errorf("downloading %s: %w", c.Package.Name, err)
	}
	if body.over {
		return nil, fmt.Errorf("downloading %s: larger than the limit of %d bytes", c.Package.Name, maxPkg)
	}
	if got := hex.EncodeToString(h.Sum(nil)); got != strings.ToLower(want) {
		return nil, fmt.Errorf("%s: SHA256 %s does not match %s (%s); not installing it", c.Package.Name, got, SumsFile, want)
	}
	if err := tmp.Chmod(0o755); err != nil {
		return nil, err
	}
	if err := tmp.Sync(); err != nil {
		return nil, err
	}
	if err := tmp.Close(); err != nil {
		return nil, err
	}

	vctx, vcancel := context.WithTimeout(ctx, VersionTimeout)
	defer vcancel()
	run := in.Version
	if run == nil {
		run = BinaryVersion
	}
	got, err := run(vctx, st.Path)
	if err != nil {
		return nil, fmt.Errorf("the new shf does not run: %w", err)
	}
	if got != c.ServerVersion {
		return nil, fmt.Errorf("the new shf reports version %q, not the server's %q; not installing it", got, c.ServerVersion)
	}
	ok = true
	return st, nil
}

func (in *Installer) maxBinary() int64 {
	if in.MaxBinary > 0 {
		return in.MaxBinary
	}
	return MaxBinary
}

// Install puts a staged binary in place of Exe (one rename).
func (in *Installer) Install(st *Staged) error {
	if err := os.Rename(st.Path, in.Exe); err != nil {
		st.Discard()
		return fmt.Errorf("replacing %s: %w", in.Exe, err)
	}
	return nil
}

// limited reads at most n bytes of r and notes when r had more.
type limited struct {
	r    io.Reader
	n    int64
	over bool
}

func (l *limited) Read(p []byte) (int, error) {
	if l.n <= 0 {
		// one more byte tells a body of exactly n from a longer one
		var one [1]byte
		if k, _ := l.r.Read(one[:]); k > 0 {
			l.over = true
			return 0, errors.New("too large")
		}
		return 0, io.EOF
	}
	if int64(len(p)) > l.n {
		p = p[:l.n]
	}
	k, err := l.r.Read(p)
	l.n -= int64(k)
	return k, err
}

// extract copies the file shf of a package (shared-folder-<v>-<os>-<arch>/shf)
// into w.
func extract(r io.Reader, w io.Writer, maxBinary int64) error {
	gz, err := gzip.NewReader(r)
	if err != nil {
		return fmt.Errorf("not a tar.gz: %w", err)
	}
	tr := tar.NewReader(gz)
	for {
		hd, err := tr.Next()
		if err == io.EOF {
			return errors.New("the package holds no shf")
		}
		if err != nil {
			return fmt.Errorf("reading the package: %w", err)
		}
		name := path.Clean(strings.TrimPrefix(hd.Name, "./"))
		if hd.Typeflag != tar.TypeReg || path.Base(name) != "shf" || strings.Count(name, "/") > 1 {
			continue
		}
		if hd.Size > maxBinary {
			return fmt.Errorf("its shf is %d bytes, more than the limit of %d", hd.Size, maxBinary)
		}
		if _, err := io.Copy(w, io.LimitReader(tr, maxBinary)); err != nil {
			return fmt.Errorf("unpacking shf: %w", err)
		}
		// the rest of the archive is read for the checksum by the caller
		io.Copy(io.Discard, gz)
		return nil
	}
}

// ParseSums parses a SHA256SUMS ("<hex>  <name>" or "<hex> *<name>") into
// name → lower-case hex.
func ParseSums(b []byte) map[string]string {
	sums := map[string]string{}
	sc := bufio.NewScanner(bytes.NewReader(b))
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

// BinaryVersion runs "<bin> version --short" and returns the version of
// its first line, "shf <version>".
func BinaryVersion(ctx context.Context, bin string) (string, error) {
	cmd := exec.CommandContext(ctx, bin, "version", "--short")
	// a state home of its own: whatever the binary does, it finds no
	// joined folder and contacts no server
	cmd.Env = append(os.Environ(), "SHF_STATE_HOME="+filepath.Join(os.TempDir(), tempPrefix+"no-state"))
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	line, _, _ := strings.Cut(string(out), "\n")
	v, ok := strings.CutPrefix(strings.TrimSpace(line), "shf ")
	if !ok || v == "" {
		return "", fmt.Errorf("unexpected output of %s version: %q", filepath.Base(bin), line)
	}
	return v, nil
}

// cleanStale removes what an interrupted update left in dir.
func cleanStale(dir string) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		if !strings.HasPrefix(e.Name(), tempPrefix) {
			continue
		}
		if fi, err := e.Info(); err == nil && fi.Mode().IsRegular() && time.Since(fi.ModTime()) > time.Hour {
			os.Remove(filepath.Join(dir, e.Name()))
		}
	}
}

// Servers are the distinct server URLs of the joined folders, sorted.
func Servers(urls []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, u := range urls {
		u = strings.TrimRight(u, "/")
		if u != "" && !seen[u] {
			seen[u] = true
			out = append(out, u)
		}
	}
	sort.Strings(out)
	return out
}

// Auto is the update shf daemon runs on its own: the servers of the
// joined folders decide, the binary is staged next to Exe.
type Auto struct {
	Checker   *Checker
	Installer *Installer
	// Servers returns the server URLs of the joined folders.
	Servers func() ([]string, error)
	// AfterInstall, when set, runs after the binary was replaced (the
	// service definition is written anew); its error is reported, the
	// update stays.
	AfterInstall func(ctx context.Context) error
}

// Prepare checks the servers and, when there is a new version, stages it.
// With nothing to install it returns a nil *Staged and says why.
func (a *Auto) Prepare(ctx context.Context) (*Staged, string, error) {
	servers, err := a.Servers()
	if err != nil {
		return nil, "", err
	}
	c, err := a.Checker.Resolve(ctx, Servers(servers))
	if err != nil {
		return nil, "", err
	}
	if !c.Available() {
		return nil, c.String(), nil
	}
	st, err := a.Installer.Prepare(ctx, c, "")
	if err != nil {
		return nil, "", fmt.Errorf("%s → %s: %w", c.Current, c.ServerVersion, err)
	}
	return st, c.String(), nil
}

// Install puts st in place, then runs AfterInstall.
func (a *Auto) Install(ctx context.Context, st *Staged) error {
	if err := a.Installer.Install(st); err != nil {
		return err
	}
	if a.AfterInstall != nil {
		if err := a.AfterInstall(ctx); err != nil {
			return &AfterInstallError{Err: err}
		}
	}
	return nil
}

// AfterInstallError: the binary was replaced, but what follows (the
// service definition) failed.
type AfterInstallError struct{ Err error }

func (e *AfterInstallError) Error() string {
	return "shf was replaced, but writing the service definition failed: " + e.Err.Error()
}

func (e *AfterInstallError) Unwrap() error { return e.Err }
