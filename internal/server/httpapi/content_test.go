package httpapi_test

import (
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/digital-cube/shared-folder/internal/server"
	"github.com/digital-cube/shared-folder/internal/server/httpapi"
)

func (e *env) contentURL(p string, extra ...string) string {
	u := e.http.URL + "/v1/shares/" + e.share + "/admin/files/content?path=" + url.QueryEscape(p)
	for _, x := range extra {
		u += "&" + x
	}
	return u
}

func TestContentWhitelist(t *testing.T) {
	e := newEnv(t)
	files := map[string]string{
		"a.png":          "png bytes",
		"Slike/b.JPEG":   "jpeg bytes",
		"c.webp":         "webp",
		"d.pdf":          "%PDF-1.4",
		"e.txt":          "hello",
		"f.MD":           "# title",
		"g.csv":          "a,b",
		"h.log":          "line",
		"i.json":         "{}",
		"v.mp4":          "video",
		"w.WEBM":         "video",
		"x.mp3":          "audio",
		"y.flac":         "audio",
		"noi.html":       "<script>alert(1)</script>",
		"j.htm":          "<b>x</b>",
		"k.svg":          "<svg onload=alert(1)/>",
		"l.xml":          "<x/>",
		"m.js":           "alert(1)",
		"n.docx":         "PK",
		"bez-ekstenzije": "data",
	}
	e.ana.mustOp(server.OpRequest{Op: "mkdir", Path: "Slike"})
	for p, c := range files {
		e.ana.mustPut(p, "", []byte(c))
	}
	inline := map[string]string{
		"a.png":        "image/png",
		"Slike/b.JPEG": "image/jpeg",
		"c.webp":       "image/webp",
		"d.pdf":        "application/pdf",
		"e.txt":        "text/plain; charset=utf-8",
		"f.MD":         "text/plain; charset=utf-8",
		"g.csv":        "text/plain; charset=utf-8",
		"h.log":        "text/plain; charset=utf-8",
		"i.json":       "application/json",
		"v.mp4":        "video/mp4",
		"w.WEBM":       "video/webm",
		"x.mp3":        "audio/mpeg",
		"y.flac":       "audio/flac",
	}
	for p, c := range files {
		resp, raw := call(t, "GET", e.contentURL(p), e.ana.token, nil, nil)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("%s: %d %s", p, resp.StatusCode, raw)
		}
		eq(t, p+" body", string(raw), c)
		h := resp.Header
		eq(t, p+" nosniff", h.Get("X-Content-Type-Options"), "nosniff")
		eq(t, p+" csp", h.Get("Content-Security-Policy"), "sandbox")
		eq(t, p+" etag", h.Get("ETag"), `"`+hashOf([]byte(c))+`"`)
		eq(t, p+" accept-ranges", h.Get("Accept-Ranges"), "bytes")
		name := p[strings.LastIndex(p, "/")+1:]
		if want, ok := inline[p]; ok {
			eq(t, p+" type", h.Get("Content-Type"), want)
			eq(t, p+" disposition", h.Get("Content-Disposition"), `inline; filename="`+name+`"`)
		} else {
			eq(t, p+" type", h.Get("Content-Type"), "application/octet-stream")
			eq(t, p+" disposition", h.Get("Content-Disposition"), `attachment; filename="`+name+`"`)
		}
		// download=1 is always an attachment; the type stays.
		resp, _ = call(t, "GET", e.contentURL(p, "download=1"), e.ana.token, nil, nil)
		eq(t, p+" download", resp.Header.Get("Content-Disposition"), `attachment; filename="`+name+`"`)
	}
}

func TestContentDisposition(t *testing.T) {
	for _, c := range []struct{ disp, name, want string }{
		{"inline", "a.txt", `inline; filename="a.txt"`},
		{"attachment", `čaj "x".pdf`, `attachment; filename="_aj _x_.pdf"; filename*=UTF-8''%C4%8Daj%20%22x%22.pdf`},
		{"attachment", "a\\b;c.txt", `attachment; filename="a_b;c.txt"; filename*=UTF-8''a%5Cb%3Bc.txt`},
		{"attachment", "Ђурђевдан 100%.jpg", `attachment; filename="_________ 100_.jpg"; filename*=UTF-8''%D0%82%D1%83%D1%80%D1%92%D0%B5%D0%B2%D0%B4%D0%B0%D0%BD%20100%25.jpg`},
	} {
		eq(t, c.name, httpapi.ContentDisposition(c.disp, c.name), c.want)
	}
	if _, ok := httpapi.InlineType("x.SVG"); ok {
		t.Error("svg must not be inline")
	}
}

func TestContentRangeAndConditional(t *testing.T) {
	e := newEnv(t)
	body := "0123456789abcdefghij"
	e.ana.mustPut("r.txt", "", []byte(body))
	resp, raw := call(t, "GET", e.contentURL("r.txt"), e.ana.token, nil, nil, "Range", "bytes=5-9")
	eq(t, "range status", resp.StatusCode, http.StatusPartialContent)
	eq(t, "range body", string(raw), "56789")
	eq(t, "content-range", resp.Header.Get("Content-Range"), "bytes 5-9/20")
	eq(t, "range csp", resp.Header.Get("Content-Security-Policy"), "sandbox")

	etag := `"` + hashOf([]byte(body)) + `"`
	resp, raw = call(t, "GET", e.contentURL("r.txt"), e.ana.token, nil, nil, "If-None-Match", etag)
	eq(t, "304", resp.StatusCode, http.StatusNotModified)
	eq(t, "304 body", len(raw), 0)
	resp, _ = call(t, "GET", e.contentURL("r.txt"), e.ana.token, nil, nil, "If-None-Match", `"other"`)
	eq(t, "other etag", resp.StatusCode, http.StatusOK)
	// If-Range with a stale ETag sends the whole file.
	resp, raw = call(t, "GET", e.contentURL("r.txt"), e.ana.token, nil, nil, "Range", "bytes=0-1", "If-Range", `"old"`)
	eq(t, "if-range stale", resp.StatusCode, http.StatusOK)
	eq(t, "if-range body", string(raw), body)

	resp, raw = call(t, "HEAD", e.contentURL("r.txt"), e.ana.token, nil, nil)
	eq(t, "head", resp.StatusCode, http.StatusOK)
	eq(t, "head length", resp.Header.Get("Content-Length"), "20")
	eq(t, "head body", len(raw), 0)
}

func TestContentPathsAndRoles(t *testing.T) {
	e := newEnv(t)
	e.ana.mustOp(server.OpRequest{Op: "mkdir", Path: "docs"})
	e.ana.mustPut("docs/a.txt", "", []byte("a"))
	storage := filepath.Join(e.dir(), server.StorageDir)
	// On disk, not in the database.
	os.WriteFile(filepath.Join(storage, "stray.txt"), []byte("stray"), 0o600)
	// A secret outside storage/ and a symlink to it that the database
	// records as a file.
	secret := filepath.Join(e.root, "secret.txt")
	os.WriteFile(secret, []byte("secret"), 0o600)

	for _, c := range []struct {
		path   string
		status int
		code   server.Code
	}{
		{"", http.StatusBadRequest, server.CodeInvalidRequest},
		{"../secret.txt", http.StatusBadRequest, server.CodeInvalidPath},
		{"docs/../../secret.txt", http.StatusBadRequest, server.CodeInvalidPath},
		{"/etc/passwd", http.StatusBadRequest, server.CodeInvalidPath},
		{"stray.txt", http.StatusNotFound, server.CodeNotFound},
		{"nema.txt", http.StatusNotFound, server.CodeNotFound},
		{"docs", http.StatusConflict, server.CodeKindMismatch},
		{"docs/", http.StatusConflict, server.CodeKindMismatch},
	} {
		resp, raw := call(t, "GET", e.contentURL(c.path), e.ana.token, nil, nil)
		wantStatus(t, "path "+c.path, resp, raw, c.status, c.code)
	}
	// Normalized: a doubled slash finds the file.
	resp, raw := call(t, "GET", e.contentURL("docs//a.txt"), e.ana.token, nil, nil)
	eq(t, "docs//a.txt", string(raw), "a")
	eq(t, "docs//a.txt status", resp.StatusCode, http.StatusOK)

	// A symlink out of storage/ in place of a recorded file is not followed.
	e.ana.mustPut("link.txt", "", []byte("secret"))
	os.Remove(filepath.Join(storage, "link.txt"))
	if err := os.Symlink(secret, filepath.Join(storage, "link.txt")); err != nil {
		t.Fatal(err)
	}
	resp, raw = call(t, "GET", e.contentURL("link.txt"), e.ana.token, nil, nil)
	wantStatus(t, "symlink out", resp, raw, http.StatusConflict, server.CodeFileChanged)
	if strings.Contains(string(raw), "secret\n") || string(raw) == "secret" {
		t.Errorf("symlink served the secret: %s", raw)
	}
	// Same through a directory symlink.
	os.Mkdir(filepath.Join(e.root, "outside"), 0o700)
	os.WriteFile(filepath.Join(e.root, "outside", "x.txt"), []byte("x"), 0o600)
	e.ana.mustOp(server.OpRequest{Op: "mkdir", Path: "ln"})
	e.ana.mustPut("ln/x.txt", "", []byte("x"))
	os.RemoveAll(filepath.Join(storage, "ln"))
	os.Symlink(filepath.Join(e.root, "outside"), filepath.Join(storage, "ln"))
	resp, raw = call(t, "GET", e.contentURL("ln/x.txt"), e.ana.token, nil, nil)
	wantStatus(t, "dir symlink out", resp, raw, http.StatusConflict, server.CodeFileChanged)

	// Roles: no token, a member, the admin socket.
	resp, raw = call(t, "GET", e.contentURL("docs/a.txt"), "", nil, nil)
	wantStatus(t, "no token", resp, raw, http.StatusUnauthorized, server.CodeUnauthorized)
	bob := e.member("bob")
	resp, raw = call(t, "GET", e.contentURL("docs/a.txt"), bob.token, nil, nil)
	wantStatus(t, "member", resp, raw, http.StatusForbidden, server.CodeForbidden)
	ls := e.local()
	resp, raw = call(t, "GET", ls.URL+"/v1/shares/"+e.share+"/admin/files/content?path=docs/a.txt", "", nil, nil)
	eq(t, "socket status", resp.StatusCode, http.StatusOK)
	eq(t, "socket body", string(raw), "a")
	eq(t, "socket csp", resp.Header.Get("Content-Security-Policy"), "sandbox")
	resp, raw = call(t, "GET", ls.URL+"/v1/shares/"+e.share+"/admin/files/content?path=docs/a.txt", "", nil, nil, httpapi.HeaderAs, "bob")
	wantStatus(t, "socket as member", resp, raw, http.StatusForbidden, server.CodeForbidden)
}

// Content on disk that is not what the database records is not served.
func TestContentChangedOnDisk(t *testing.T) {
	e := newEnv(t)
	e.ana.mustPut("a.txt", "", []byte("original"))
	p := filepath.Join(e.dir(), server.StorageDir, "a.txt")
	resp, _ := call(t, "GET", e.contentURL("a.txt"), e.ana.token, nil, nil)
	eq(t, "before", resp.StatusCode, http.StatusOK)

	// Same size, other bytes: a new file (as a rename would leave it), so
	// the verified-file cache does not apply and the hash is checked.
	os.Remove(p)
	os.WriteFile(p, []byte("ORIGINAL"), 0o600)
	resp, raw := call(t, "GET", e.contentURL("a.txt"), e.ana.token, nil, nil)
	wantStatus(t, "other content", resp, raw, http.StatusConflict, server.CodeFileChanged)

	os.WriteFile(p, []byte("longer content"), 0o600)
	resp, raw = call(t, "GET", e.contentURL("a.txt"), e.ana.token, nil, nil)
	wantStatus(t, "other size", resp, raw, http.StatusConflict, server.CodeFileChanged)

	os.Remove(p)
	resp, raw = call(t, "GET", e.contentURL("a.txt"), e.ana.token, nil, nil)
	wantStatus(t, "missing", resp, raw, http.StatusConflict, server.CodeFileChanged)

	os.WriteFile(p, []byte("original"), 0o600)
	resp, raw = call(t, "GET", e.contentURL("a.txt"), e.ana.token, nil, nil)
	eq(t, "restored", resp.StatusCode, http.StatusOK)
	eq(t, "restored body", string(raw), "original")
}
