package httpapi_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/digital-cube/shared-folder/internal/server"
	"github.com/digital-cube/shared-folder/internal/server/httpapi"
	"github.com/digital-cube/shared-folder/internal/transport"

	_ "modernc.org/sqlite"
)

// t0 is the pinned start of the test clock: 20261123T145846Z in names.
var t0 = time.Date(2026, 11, 23, 14, 58, 46, 0, time.UTC)

const ts0 = "20261123T145846Z"

type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) Now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *clock) Advance(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

// env is a server over a temporary data-root with one folder whose first
// admin is "ana", reached over real HTTP, with content moved by the local
// transport.
type env struct {
	t     *testing.T
	root  string
	srv   *server.Server
	http  *httptest.Server
	clock *clock
	share string
	tr    transport.Transport
	ana   *client
}

func newEnv(t *testing.T) *env {
	t.Helper()
	e := &env{t: t, root: t.TempDir(), clock: &clock{t: t0}}
	srv, err := server.Open(e.root, server.Options{Now: e.clock.Now})
	if err != nil {
		t.Fatal(err)
	}
	e.srv = srv
	e.http = httptest.NewServer(httpapi.New(srv, nil))
	t.Cleanup(func() {
		e.http.Close()
		srv.Close()
	})
	id, token, err := srv.CreateShare("tim", "ana")
	if err != nil {
		t.Fatal(err)
	}
	e.share = id
	e.tr = transport.Local{DataRoot: e.root}
	e.ana = &client{e: e, share: id, user: "ana", token: token}
	return e
}

// member adds a member to the env's folder and returns its client.
func (e *env) member(user string) *client {
	e.t.Helper()
	sh, err := e.srv.Share(e.share)
	if err != nil {
		e.t.Fatal(err)
	}
	tok, err := sh.AddMember(user, false)
	if err != nil {
		e.t.Fatal(err)
	}
	return &client{e: e, share: e.share, user: user, token: tok}
}

type client struct {
	e     *env
	share string
	user  string
	token string
}

// apiError is a non-2xx answer.
type apiError struct {
	Status int
	Code   server.Code
	Msg    string
}

func (a *apiError) Error() string { return fmt.Sprintf("%d %s: %s", a.Status, a.Code, a.Msg) }

func (c *client) do(method, path string, in, out any) *apiError {
	c.e.t.Helper()
	var body io.Reader
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			c.e.t.Fatal(err)
		}
		body = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, c.e.http.URL+"/v1/shares/"+c.share+path, body)
	if err != nil {
		c.e.t.Fatal(err)
	}
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		c.e.t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode/100 != 2 {
		var eb httpapi.ErrorBody
		json.Unmarshal(raw, &eb)
		return &apiError{Status: resp.StatusCode, Code: eb.Error.Code, Msg: eb.Error.Message}
	}
	if out != nil {
		if err := json.Unmarshal(raw, out); err != nil {
			c.e.t.Fatalf("decode %s: %v", raw, err)
		}
	}
	return nil
}

func hashOf(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

func (c *client) announce(p, base string, content []byte) (*server.AnnounceResult, *apiError) {
	c.e.t.Helper()
	var ar server.AnnounceResult
	err := c.do("POST", "/uploads", server.AnnounceRequest{
		Path: p, BaseHash: base, NewHash: hashOf(content), Size: int64(len(content)), Mtime: t0.UnixNano(),
	}, &ar)
	return &ar, err
}

// send writes content to a file and delivers it with the local transport.
func (c *client) send(ar *server.AnnounceResult, content []byte) {
	c.e.t.Helper()
	src := filepath.Join(c.e.t.TempDir(), "src")
	if err := os.WriteFile(src, content, 0o600); err != nil {
		c.e.t.Fatal(err)
	}
	if err := c.e.tr.Send(context.Background(), transport.Upload{ShareID: c.share, UploadID: ar.UploadID, Dest: ar.Dest}, src); err != nil {
		c.e.t.Fatal(err)
	}
}

func (c *client) commit(uploadID string) (*server.Result, *apiError) {
	c.e.t.Helper()
	var res server.Result
	err := c.do("POST", "/uploads/"+uploadID+"/commit", nil, &res)
	return &res, err
}

// put runs announce, send and commit. When the announce says there is
// nothing to send it returns a Result with status "unchanged".
func (c *client) put(p, base string, content []byte) (*server.Result, *apiError) {
	c.e.t.Helper()
	ar, err := c.announce(p, base, content)
	if err != nil {
		return nil, err
	}
	if ar.Status == server.StatusUnchanged {
		return &server.Result{Status: server.StatusUnchanged, Path: ar.Path, Head: ar.Head}, nil
	}
	c.send(ar, content)
	return c.commit(ar.UploadID)
}

func (c *client) mustPut(p, base string, content []byte) *server.Result {
	c.e.t.Helper()
	res, err := c.put(p, base, content)
	if err != nil {
		c.e.t.Fatalf("put %s: %v", p, err)
	}
	return res
}

func (c *client) op(req server.OpRequest) (*server.Result, *apiError) {
	c.e.t.Helper()
	var res server.Result
	err := c.do("POST", "/ops", req, &res)
	return &res, err
}

func (c *client) mustOp(req server.OpRequest) *server.Result {
	c.e.t.Helper()
	res, err := c.op(req)
	if err != nil {
		c.e.t.Fatalf("op %+v: %v", req, err)
	}
	return res
}

func (c *client) changes(since int64, limit int) (*httpapi.ChangesResponse, *apiError) {
	c.e.t.Helper()
	var cr httpapi.ChangesResponse
	q := fmt.Sprintf("/changes?since=%d", since)
	if limit > 0 {
		q += fmt.Sprintf("&limit=%d", limit)
	}
	err := c.do("GET", q, nil, &cr)
	return &cr, err
}

// ---- inspecting the folder ----------------------------------------------

func (e *env) dir() string { return filepath.Join(e.root, e.share) }

func (e *env) db() *sql.DB {
	e.t.Helper()
	db, err := sql.Open("sqlite", "file:"+filepath.Join(e.dir(), "service.db")+"?mode=ro&_pragma=busy_timeout(5000)")
	if err != nil {
		e.t.Fatal(err)
	}
	e.t.Cleanup(func() { db.Close() })
	return db
}

type fileRow struct {
	Path, Kind, Hash string
	Size, Seq        int64
	UpdatedBy        string
}

// files returns the files table keyed by path.
func (e *env) files() map[string]fileRow {
	e.t.Helper()
	rows, err := e.db().Query(`SELECT path, kind, hash, size, seq, updated_by FROM files`)
	if err != nil {
		e.t.Fatal(err)
	}
	defer rows.Close()
	out := map[string]fileRow{}
	for rows.Next() {
		var r fileRow
		if err := rows.Scan(&r.Path, &r.Kind, &r.Hash, &r.Size, &r.Seq, &r.UpdatedBy); err != nil {
			e.t.Fatal(err)
		}
		out[r.Path] = r
	}
	return out
}

func (e *env) events() []server.Event {
	e.t.Helper()
	rows, err := e.db().Query(`SELECT seq, member, op, path, old_path, hash, prev_hash, trash_id FROM events ORDER BY seq`)
	if err != nil {
		e.t.Fatal(err)
	}
	defer rows.Close()
	var out []server.Event
	for rows.Next() {
		var ev server.Event
		var tid sql.NullInt64
		if err := rows.Scan(&ev.Seq, &ev.Member, &ev.Op, &ev.Path, &ev.OldPath, &ev.Hash, &ev.PrevHash, &tid); err != nil {
			e.t.Fatal(err)
		}
		if tid.Valid {
			ev.TrashID = &tid.Int64
		}
		out = append(out, ev)
	}
	return out
}

type trashRow struct {
	ID                   int64
	OrigPath, Name, Hash string
	Reason, ByMember     string
	Size, EventSeq       int64
}

func (e *env) trash() []trashRow {
	e.t.Helper()
	rows, err := e.db().Query(`SELECT id, orig_path, trash_name, hash, reason, by_member, size, event_seq FROM trash ORDER BY id`)
	if err != nil {
		e.t.Fatal(err)
	}
	defer rows.Close()
	var out []trashRow
	for rows.Next() {
		var r trashRow
		if err := rows.Scan(&r.ID, &r.OrigPath, &r.Name, &r.Hash, &r.Reason, &r.ByMember, &r.Size, &r.EventSeq); err != nil {
			e.t.Fatal(err)
		}
		out = append(out, r)
	}
	return out
}

// tree lists a directory of the share recursively: "a/", "a/b.txt".
func (e *env) tree(sub string) []string {
	e.t.Helper()
	base := filepath.Join(e.dir(), sub)
	var out []string
	filepath.WalkDir(base, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			e.t.Fatal(err)
		}
		if p == base {
			return nil
		}
		rel, _ := filepath.Rel(base, p)
		rel = filepath.ToSlash(rel)
		if d.IsDir() {
			rel += "/"
		}
		out = append(out, rel)
		return nil
	})
	sort.Strings(out)
	return out
}

func (e *env) read(sub, p string) string {
	e.t.Helper()
	b, err := os.ReadFile(filepath.Join(e.dir(), sub, filepath.FromSlash(p)))
	if err != nil {
		e.t.Fatal(err)
	}
	return string(b)
}

func (e *env) logLines() []string {
	e.t.Helper()
	b, err := os.ReadFile(filepath.Join(e.dir(), "service.log"))
	if err != nil {
		e.t.Fatal(err)
	}
	s := strings.TrimRight(string(b), "\n")
	if s == "" {
		return nil
	}
	return strings.Split(s, "\n")
}

// ---- assertions ----------------------------------------------------------

func eq[T comparable](t *testing.T, what string, got, want T) {
	t.Helper()
	if got != want {
		t.Errorf("%s = %v, want %v", what, got, want)
	}
}

func eqList(t *testing.T, what string, got, want []string) {
	t.Helper()
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("%s:\n got  %q\n want %q", what, got, want)
	}
}

func wantErr(t *testing.T, err *apiError, status int, code server.Code) {
	t.Helper()
	if err == nil {
		t.Fatalf("want %d %s, got success", status, code)
	}
	if err.Status != status || err.Code != code {
		t.Fatalf("got %v, want %d %s", err, status, code)
	}
}

// eventOps renders events as "seq op path[<-old]" for compact comparison.
func eventOps(evs []server.Event) []string {
	var out []string
	for _, ev := range evs {
		s := fmt.Sprintf("%d %s %s", ev.Seq, ev.Op, ev.Path)
		if ev.OldPath != "" {
			s += " <- " + ev.OldPath
		}
		out = append(out, s)
	}
	return out
}

// logOps renders service.log lines the same way.
func logOps(lines []string) []string {
	var out []string
	for _, l := range lines {
		var seq int64
		var member, op, p, old string
		f := strings.Fields(l)
		for _, kv := range f[1:] {
			k, v, _ := strings.Cut(kv, "=")
			switch k {
			case "seq":
				fmt.Sscan(v, &seq)
			case "member":
				member = v
			case "op":
				op = v
			case "path":
				p = strings.Trim(v, `"`)
			case "old_path":
				old = strings.Trim(v, `"`)
			}
		}
		if op == "" {
			continue // a sync line
		}
		s := fmt.Sprintf("%d %s %s", seq, op, p)
		if old != "" {
			s += " <- " + old
		}
		_ = member
		out = append(out, s)
	}
	return out
}

// checkLogMatchesEvents asserts that service.log has exactly one line per
// event, in order.
func (e *env) checkLogMatchesEvents() {
	e.t.Helper()
	eqList(e.t, "service.log vs events", logOps(e.logLines()), eventOps(e.events()))
}
