package daemon_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/digital-cube/shared-folder/internal/client"
	"github.com/digital-cube/shared-folder/internal/daemon"
	"github.com/digital-cube/shared-folder/internal/server"
	"github.com/digital-cube/shared-folder/internal/server/httpapi"
	"github.com/digital-cube/shared-folder/internal/transport"
)

// world is a real shfd in process, reached over HTTP; content moves
// through the local transport.
type world struct {
	t     *testing.T
	root  string
	srv   *server.Server
	url   string
	share string
}

// newWorld starts a server; wrap, when set, sits in front of the API.
func newWorld(t *testing.T, wrap func(http.Handler) http.Handler) *world {
	t.Helper()
	w := &world{t: t, root: t.TempDir()}
	srv, err := server.Open(w.root, server.Options{})
	if err != nil {
		t.Fatal(err)
	}
	w.srv = srv
	api := httpapi.New(srv, nil)
	var h http.Handler = api
	if wrap != nil {
		h = wrap(h)
	}
	hs := httptest.NewServer(h)
	t.Cleanup(func() {
		api.Close()
		hs.Close()
		srv.Close()
	})
	w.url = hs.URL
	if w.share, _, err = srv.CreateShare("tim", "admin"); err != nil {
		t.Fatal(err)
	}
	return w
}

func (w *world) shareObj() *server.Share {
	sh, err := w.srv.Share(w.share)
	if err != nil {
		w.t.Fatal(err)
	}
	return sh
}

func (w *world) head() int64 {
	h, err := w.shareObj().Head()
	if err != nil {
		w.t.Fatal(err)
	}
	return h
}

func (w *world) storage(p string) (string, bool) {
	b, err := os.ReadFile(filepath.Join(w.root, w.share, "storage", filepath.FromSlash(p)))
	return string(b), err == nil
}

// puts counts the put events of a path in the server's log.
func (w *world) puts(p string) int {
	evs, _, _, err := w.shareObj().Changes(0, 100000)
	if err != nil {
		w.t.Fatal(err)
	}
	n := 0
	for _, e := range evs {
		if e.Path == p && e.Op == "put" {
			n++
		}
	}
	return n
}

// peer is one member with its own state home and folder.
type peer struct {
	t    *testing.T
	w    *world
	user string
	home string
	dir  string
	url  string // the server as this peer reaches it

	mu     sync.Mutex
	cycles []cycle
	log    bytes.Buffer
}

type cycle struct {
	reason string
	res    *client.Result
	err    error
}

// join joins an empty folder (one cycle, then the client is closed: the
// daemon opens its own).
func (w *world) join(user string) *peer { return w.joinVia(user, w.url) }

func (w *world) joinVia(user, url string) *peer {
	w.t.Helper()
	p := &peer{t: w.t, w: w, user: user, home: w.t.TempDir(), dir: filepath.Join(w.t.TempDir(), user), url: url}
	tok, err := w.shareObj().AddMember(user, false)
	if err != nil {
		w.t.Fatal(err)
	}
	c, _, err := client.Join(context.Background(), client.JoinRequest{Home: p.home, URL: url, ShareID: w.share, Token: tok, Folder: p.dir},
		client.Options{Transport: transport.Local{DataRoot: w.root}})
	if err != nil {
		w.t.Fatal(err)
	}
	c.Close()
	return p
}

// fast are short intervals, for tests that are about behaviour, not about
// the defaults.
func fast() client.Settings {
	s := client.DefaultSettings()
	s.Debounce = client.Duration(300 * time.Millisecond)
	s.Settle = client.Duration(time.Second)
	s.Poll = client.Duration(time.Hour)
	return s
}

// start runs the peer's daemon; the returned function stops it (as
// SIGTERM does) and waits for it to return.
func (p *peer) start(set client.Settings) func() {
	p.t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- daemon.Run(ctx, daemon.Config{
			Home: p.home, Settings: set,
			Options: client.Options{Transport: transport.Local{DataRoot: p.w.root}},
			Log:     slog.New(slog.NewTextHandler(syncWriter{&p.mu, &p.log}, &slog.HandlerOptions{Level: slog.LevelDebug})),
			OnCycle: func(_, reason string, res *client.Result, err error) {
				p.mu.Lock()
				p.cycles = append(p.cycles, cycle{reason, res, err})
				p.mu.Unlock()
			},
		})
	}()
	stopped := false
	stop := func() {
		if stopped {
			return
		}
		stopped = true
		cancel()
		select {
		case err := <-done:
			if err != nil {
				p.t.Errorf("%s: daemon: %v", p.user, err)
			}
		case <-time.After(10 * time.Second):
			p.t.Errorf("%s: daemon did not stop", p.user)
		}
	}
	p.t.Cleanup(func() {
		stop()
		if p.t.Failed() {
			p.mu.Lock()
			p.t.Logf("%s daemon log:\n%s", p.user, p.log.String())
			p.mu.Unlock()
		}
	})
	// the full comparison at start is done before the test goes on
	waitFor(p.t, 10*time.Second, p.user+": first cycle", func() bool { return len(p.cyclesOf(daemon.ReasonStart)) > 0 })
	return stop
}

type syncWriter struct {
	mu *sync.Mutex
	b  *bytes.Buffer
}

func (w syncWriter) Write(b []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.b.Write(b)
}

func (p *peer) cyclesOf(reason string) []cycle {
	p.mu.Lock()
	defer p.mu.Unlock()
	var out []cycle
	for _, c := range p.cycles {
		if reason == "" || c.reason == reason {
			out = append(out, c)
		}
	}
	return out
}

func (p *peer) abs(rel string) string { return filepath.Join(p.dir, filepath.FromSlash(rel)) }

func (p *peer) write(rel, content string) {
	p.t.Helper()
	if err := os.MkdirAll(filepath.Dir(p.abs(rel)), 0o755); err != nil {
		p.t.Fatal(err)
	}
	if err := os.WriteFile(p.abs(rel), []byte(content), 0o644); err != nil {
		p.t.Fatal(err)
	}
}

func (p *peer) read(rel string) (string, bool) {
	b, err := os.ReadFile(p.abs(rel))
	return string(b), err == nil
}

func waitFor(t *testing.T, d time.Duration, what string, ok func() bool) time.Duration {
	t.Helper()
	start := time.Now()
	for !ok() {
		if time.Since(start) > d {
			t.Fatalf("timed out after %v waiting for %s", d, what)
		}
		time.Sleep(20 * time.Millisecond)
	}
	return time.Since(start)
}

func (p *peer) waitContent(rel, want string, d time.Duration) time.Duration {
	p.t.Helper()
	return waitFor(p.t, d, p.user+": "+rel+" = "+want, func() bool {
		got, ok := p.read(rel)
		return ok && got == want
	})
}

// TestFileArrivesWithoutACommand: two background clients with the
// default intervals; a file added at one is at the other within 10 s.
func TestFileArrivesWithoutACommand(t *testing.T) {
	t.Parallel()
	w := newWorld(t, nil)
	ana, bob := w.join("ana"), w.join("bob")
	ana.start(client.DefaultSettings())
	bob.start(client.DefaultSettings())

	ana.write("docs/plan.txt", "plan v1")
	took := bob.waitContent("docs/plan.txt", "plan v1", 10*time.Second)
	t.Logf("arrived after %v", took.Round(time.Millisecond))
	// the file is on disk a moment before the cycle reports back
	waitFor(t, 5*time.Second, "bob woken by the server", func() bool { return len(bob.cyclesOf(daemon.ReasonRemote)) > 0 })

	// and back, an edit
	bob.write("docs/plan.txt", "plan v2")
	ana.waitContent("docs/plan.txt", "plan v2", 10*time.Second)
}

// TestPollWhenWebSocketIsDown: the server's WebSocket refuses; changes
// still arrive by polling.
func TestPollWhenWebSocketIsDown(t *testing.T) {
	t.Parallel()
	noWS := func(h http.Handler) http.Handler {
		return http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
			if strings.HasSuffix(r.URL.Path, "/ws") {
				http.Error(rw, "no websocket here", http.StatusServiceUnavailable)
				return
			}
			h.ServeHTTP(rw, r)
		})
	}
	w := newWorld(t, noWS)
	ana, bob := w.join("ana"), w.join("bob")
	set := fast()
	set.Poll = client.Duration(time.Second)
	ana.start(set)
	bob.start(set)

	ana.write("a.txt", "via poll")
	bob.waitContent("a.txt", "via poll", 10*time.Second)
	waitFor(t, 5*time.Second, "a cycle started by the poll", func() bool { return len(bob.cyclesOf(daemon.ReasonRemote)) > 0 })
}

// TestWebSocketReconnects: the connections break; the daemons connect
// again and changes keep arriving without polling.
func TestWebSocketReconnects(t *testing.T) {
	t.Parallel()
	w := newWorld(t, nil)
	px := newProxy(t, strings.TrimPrefix(w.url, "http://"))
	ana, bob := w.joinVia("ana", px.url()), w.joinVia("bob", px.url())
	set := fast() // Poll is an hour: only the WebSocket can bring changes
	ana.start(set)
	bob.start(set)
	ana.write("one.txt", "1")
	bob.waitContent("one.txt", "1", 10*time.Second)

	px.breakAll()
	time.Sleep(200 * time.Millisecond)
	ana.write("two.txt", "2")
	bob.waitContent("two.txt", "2", 15*time.Second)
}

// TestSlowlyWrittenFileIsSentOnceWhenDone: a file written in pieces, with
// pauses longer than the debounce but shorter than the settle time, is
// not sent while it grows and is sent exactly once at the end.
func TestSlowlyWrittenFileIsSentOnceWhenDone(t *testing.T) {
	t.Parallel()
	w := newWorld(t, nil)
	ana := w.join("ana")
	set := fast() // debounce 300 ms, settle 1 s
	ana.start(set)

	f, err := os.Create(ana.abs("video.bin"))
	if err != nil {
		t.Fatal(err)
	}
	var all strings.Builder
	for i := range 6 {
		chunk := strings.Repeat(fmt.Sprint(i), 1000)
		if _, err := f.WriteString(chunk); err != nil {
			t.Fatal(err)
		}
		all.WriteString(chunk)
		time.Sleep(600 * time.Millisecond)
		if _, ok := w.storage("video.bin"); ok {
			t.Fatalf("sent while being written (after %d pieces)", i+1)
		}
	}
	f.Close()
	held := 0
	for _, c := range ana.cyclesOf("") {
		if c.res != nil && c.res.Unsettled > 0 {
			held++
		}
	}
	if held == 0 {
		t.Fatal("no cycle saw the file being written; the test proves nothing")
	}
	waitFor(t, 10*time.Second, "the file on the server", func() bool {
		got, ok := w.storage("video.bin")
		return ok && got == all.String()
	})
	time.Sleep(2 * time.Second)
	if n := w.puts("video.bin"); n != 1 {
		t.Fatalf("%d puts of video.bin, want 1", n)
	}
}

// TestDownloadsAreNotSentBack: what a daemon writes on disk while
// downloading starts no cycle and sends nothing: the server's log does
// not move.
func TestDownloadsAreNotSentBack(t *testing.T) {
	t.Parallel()
	w := newWorld(t, nil)
	ana, bob := w.join("ana"), w.join("bob")
	set := fast()
	ana.start(set)
	bob.start(set)

	for i := range 5 {
		ana.write(fmt.Sprintf("dir%d/f.txt", i), fmt.Sprint("content ", i))
	}
	for i := range 5 {
		bob.waitContent(fmt.Sprintf("dir%d/f.txt", i), fmt.Sprint("content ", i), 10*time.Second)
	}
	head := w.head()
	time.Sleep(3 * time.Second) // well past debounce and settle
	if got := w.head(); got != head {
		t.Fatalf("server log moved from %d to %d after bob's downloads", head, got)
	}
	for _, c := range bob.cyclesOf("") {
		if c.res != nil && c.res.Sent > 0 {
			t.Fatalf("bob sent %d in a %s cycle: %v", c.res.Sent, c.reason, c.res.Items)
		}
	}
	if n := len(bob.cyclesOf(daemon.ReasonLocal)); n != 0 {
		t.Fatalf("bob's own downloads started %d local cycles", n)
	}
	// a real local change at bob still goes out
	bob.write("dir0/f.txt", "bob's edit")
	waitFor(t, 10*time.Second, "bob's edit on the server", func() bool {
		got, _ := w.storage("dir0/f.txt")
		return got == "bob's edit"
	})
}

// TestIgnoredChangesStartNoCycle: a change .shfignore excludes is not a
// reason for a cycle.
func TestIgnoredChangesStartNoCycle(t *testing.T) {
	t.Parallel()
	w := newWorld(t, nil)
	ana := w.join("ana")
	ana.write(".shfignore", "*.log\nbuild/\n")
	ana.start(fast())
	waitFor(t, 5*time.Second, ".shfignore on the server", func() bool { _, ok := w.storage(".shfignore"); return ok })
	before := len(ana.cyclesOf(daemon.ReasonLocal))
	ana.write("app.log", "noise")
	ana.write("build/out.o", "noise")
	time.Sleep(1500 * time.Millisecond)
	if n := len(ana.cyclesOf(daemon.ReasonLocal)) - before; n != 0 {
		t.Fatalf("%d cycles for ignored changes", n)
	}
}

// TestStopAndSingleInstance: a second daemon on the same state home is
// refused; stopping returns promptly.
func TestStopAndSingleInstance(t *testing.T) {
	t.Parallel()
	w := newWorld(t, nil)
	ana := w.join("ana")
	stop := ana.start(fast())
	err := daemon.Run(context.Background(), daemon.Config{Home: ana.home, Settings: fast()})
	if !errors.Is(err, daemon.ErrRunning) {
		t.Fatalf("second daemon: %v", err)
	}
	start := time.Now()
	stop()
	if d := time.Since(start); d > 3*time.Second {
		t.Fatalf("stopping took %v", d)
	}
}

// proxy forwards TCP to the server and can break every connection.
type proxy struct {
	t     *testing.T
	ln    net.Listener
	mu    sync.Mutex
	conns []net.Conn
}

func newProxy(t *testing.T, backend string) *proxy {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	px := &proxy{t: t, ln: ln}
	t.Cleanup(func() { ln.Close(); px.breakAll() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			b, err := net.Dial("tcp", backend)
			if err != nil {
				c.Close()
				continue
			}
			px.mu.Lock()
			px.conns = append(px.conns, c, b)
			px.mu.Unlock()
			go func() { io.Copy(b, c); b.Close() }()
			go func() { io.Copy(c, b); c.Close() }()
		}
	}()
	return px
}

func (px *proxy) url() string { return "http://" + px.ln.Addr().String() }

func (px *proxy) breakAll() {
	px.mu.Lock()
	defer px.mu.Unlock()
	for _, c := range px.conns {
		c.Close()
	}
	px.conns = nil
}
