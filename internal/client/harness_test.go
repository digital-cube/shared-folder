package client_test

import (
	"context"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/digital-cube/shared-folder/internal/client"
	"github.com/digital-cube/shared-folder/internal/server"
	"github.com/digital-cube/shared-folder/internal/server/httpapi"
	"github.com/digital-cube/shared-folder/internal/transport"
)

// env is a real shfd (in process, over HTTP) with one folder; content
// moves through the local transport.
type env struct {
	t     *testing.T
	root  string
	srv   *server.Server
	url   string
	share string
	n     int
}

func newEnv(t *testing.T) *env {
	t.Helper()
	e := &env{t: t, root: t.TempDir()}
	srv, err := server.Open(e.root, server.Options{})
	if err != nil {
		t.Fatal(err)
	}
	e.srv = srv
	hs := httptest.NewServer(httpapi.New(srv, nil))
	t.Cleanup(func() {
		hs.Close()
		srv.Close()
	})
	e.url = hs.URL
	id, _, err := srv.CreateShare("tim", "admin")
	if err != nil {
		t.Fatal(err)
	}
	e.share = id
	return e
}

func (e *env) options() client.Options {
	return client.Options{Transport: transport.Local{DataRoot: e.root}}
}

// token adds a member and returns its token.
func (e *env) token(user string) string {
	e.t.Helper()
	sh, err := e.srv.Share(e.share)
	if err != nil {
		e.t.Fatal(err)
	}
	tok, err := sh.AddMember(user, false)
	if err != nil {
		e.t.Fatal(err)
	}
	return tok
}

// storage is the server's copy of p ("" when absent).
func (e *env) storage(p string) (string, bool) {
	b, err := os.ReadFile(filepath.Join(e.root, e.share, "storage", filepath.FromSlash(p)))
	return string(b), err == nil
}

// member is one client: its own state home and folder.
type member struct {
	t    *testing.T
	e    *env
	user string
	home string
	dir  string
	c    *client.Client
}

// newMember prepares a member whose folder may be filled before join.
func (e *env) newMember(user string) *member {
	return &member{t: e.t, e: e, user: user, home: e.t.TempDir(), dir: filepath.Join(e.t.TempDir(), user)}
}

// join joins the member's folder and checks the first cycle succeeded.
func (m *member) join() *client.Result {
	m.t.Helper()
	c, res, err := client.Join(context.Background(), client.JoinRequest{
		Home: m.home, URL: m.e.url, ShareID: m.e.share, Token: m.e.token(m.user), Folder: m.dir,
	}, m.e.options())
	if err != nil {
		m.t.Fatalf("%s: join: %v", m.user, err)
	}
	m.c = c
	m.t.Cleanup(func() { c.Close() })
	return res
}

// joined returns a member that has joined an empty folder.
func (e *env) joined(user string) *member {
	m := e.newMember(user)
	m.join()
	return m
}

func (m *member) sync() *client.Result {
	m.t.Helper()
	res, err := m.c.Sync(context.Background())
	if err != nil {
		m.t.Fatalf("%s: sync: %v\nitems: %v", m.user, err, res.Items)
	}
	return res
}

func (m *member) abs(p string) string { return filepath.Join(m.dir, filepath.FromSlash(p)) }

// write creates p with content; every write gets a distinct mtime, so a
// rewrite of the same size is seen even on coarse clocks.
func (m *member) write(p, content string) {
	m.t.Helper()
	abs := m.abs(p)
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		m.t.Fatal(err)
	}
	if err := os.WriteFile(abs, []byte(content), 0o644); err != nil {
		m.t.Fatal(err)
	}
	tick++
	ts := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC).Add(time.Duration(tick) * time.Second)
	if err := os.Chtimes(abs, ts, ts); err != nil {
		m.t.Fatal(err)
	}
}

var tick int

func (m *member) read(p string) (string, bool) {
	b, err := os.ReadFile(m.abs(p))
	return string(b), err == nil
}

func (m *member) mustRead(p, want string) {
	m.t.Helper()
	got, ok := m.read(p)
	if !ok {
		m.t.Fatalf("%s: %s is missing", m.user, p)
	}
	if got != want {
		m.t.Fatalf("%s: %s = %q, want %q", m.user, p, got, want)
	}
}

func (m *member) mustNotExist(p string) {
	m.t.Helper()
	if _, err := os.Lstat(m.abs(p)); err == nil {
		m.t.Fatalf("%s: %s exists", m.user, p)
	}
}

func (m *member) remove(p string) {
	m.t.Helper()
	if err := os.RemoveAll(m.abs(p)); err != nil {
		m.t.Fatal(err)
	}
}

// tree lists the folder: "path" for files with their content, "path/"
// for directories.
func (m *member) tree() []string {
	m.t.Helper()
	var out []string
	err := filepath.WalkDir(m.dir, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(m.dir, p)
		if rel == "." {
			return nil
		}
		rel = filepath.ToSlash(rel)
		if d.IsDir() {
			out = append(out, rel+"/")
			return nil
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		out = append(out, rel+"="+string(b))
		return nil
	})
	if err != nil {
		m.t.Fatal(err)
	}
	sort.Strings(out)
	return out
}

func sameTrees(t *testing.T, a, b *member) {
	t.Helper()
	ta, tb := strings.Join(a.tree(), "\n"), strings.Join(b.tree(), "\n")
	if ta != tb {
		t.Fatalf("folders differ:\n%s:\n%s\n\n%s:\n%s", a.user, ta, b.user, tb)
	}
}

func hasItem(r *client.Result, op, path string) bool {
	for _, it := range r.Items {
		if it.Op == op && it.Path == path {
			return true
		}
	}
	return false
}
