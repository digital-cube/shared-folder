package httpapi_test

import (
	"bytes"
	"encoding/json"
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
	"github.com/digital-cube/shared-folder/internal/thumbs"
)

// local serves the admin socket's routes of e's server.
func (e *env) local() *httptest.Server {
	e.t.Helper()
	ls := httptest.NewServer(httpapi.NewLocal(e.srv, nil))
	e.t.Cleanup(ls.Close)
	return ls
}

// call sends one request and decodes a JSON answer into out (when given);
// it returns the response with its body read into body.
func call(t *testing.T, method, url, token string, in, out any, hdr ...string) (*http.Response, []byte) {
	t.Helper()
	var body io.Reader
	if in != nil {
		b, _ := json.Marshal(in)
		body = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, url, body)
	if err != nil {
		t.Fatal(err)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	for i := 0; i+1 < len(hdr); i += 2 {
		req.Header.Set(hdr[i], hdr[i+1])
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if out != nil && resp.StatusCode == http.StatusOK {
		if err := json.Unmarshal(raw, out); err != nil {
			t.Fatalf("%s %s: %v: %s", method, url, err, raw)
		}
	}
	return resp, raw
}

func wantStatus(t *testing.T, what string, resp *http.Response, raw []byte, status int, code server.Code) {
	t.Helper()
	if resp.StatusCode != status {
		t.Errorf("%s: status %d, want %d: %s", what, resp.StatusCode, status, raw)
		return
	}
	if code != "" {
		var eb httpapi.ErrorBody
		json.Unmarshal(raw, &eb)
		if eb.Error.Code != code {
			t.Errorf("%s: code %q, want %q", what, eb.Error.Code, code)
		}
	}
}

func jobs(t *testing.T, ls *httptest.Server, q string) server.ThumbJobs {
	t.Helper()
	var j server.ThumbJobs
	resp, raw := call(t, "GET", ls.URL+"/v1/thumbs/jobs?"+q, "", nil, &j)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("jobs: %d %s", resp.StatusCode, raw)
	}
	return j
}

func jobPaths(j server.ThumbJobs) []string {
	out := []string{}
	for _, x := range j.Jobs {
		out = append(out, x.Path)
	}
	return out
}

func done(t *testing.T, ls *httptest.Server, share string, rs ...server.ThumbResult) {
	t.Helper()
	resp, raw := call(t, "POST", ls.URL+"/v1/thumbs/done", "", httpapi.ThumbsDoneRequest{ShareID: share, Results: rs}, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("done: %d %s", resp.StatusCode, raw)
	}
}

// fakeThumb writes every size of a thumbnail as shfd thumbs would.
func fakeThumb(t *testing.T, st thumbs.Store, hash string) {
	t.Helper()
	os.MkdirAll(filepath.Join(st.Dir, hash[:2]), 0o700)
	for _, s := range thumbs.Sizes {
		if err := os.WriteFile(st.Path(hash, s.Name), []byte("jpeg "+s.Name+" "+hash), 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

func (e *env) store() thumbs.Store { return thumbs.NewStore(e.dir()) }

func thumbOf(t *testing.T, l *server.DirListing, name string) string {
	t.Helper()
	for _, f := range l.Entries {
		if f.Name == name {
			return f.Thumb
		}
	}
	t.Fatalf("no %s in the listing", name)
	return ""
}

func TestThumbStatesInFiles(t *testing.T) {
	e := newEnv(t)
	ls := e.local()
	e.ana.mustPut("a.PNG", "", []byte("png a"))
	e.ana.mustPut("b.pdf", "", []byte("pdf b"))
	e.ana.mustPut("c.txt", "", []byte("text c"))
	e.ana.mustPut("empty.jpg", "", []byte{})
	e.ana.mustOp(server.OpRequest{Op: "mkdir", Path: "pics.jpg"})
	if _, err := os.Stat(filepath.Join(e.dir(), "thumbnails")); err != nil {
		t.Fatalf("thumbnails/ not made with the folder: %v", err)
	}

	states := func() map[string]string {
		l, err := e.ana.dir("")
		if err != nil {
			t.Fatal(err)
		}
		out := map[string]string{}
		for _, f := range l.Entries {
			out[f.Name] = f.Thumb
		}
		return out
	}
	// No service has asked yet: nothing is pending.
	eqMap(t, "no service", states(), map[string]string{"a.PNG": "", "b.pdf": "", "c.txt": "", "empty.jpg": "", "pics.jpg": ""})

	// A service for images only.
	j := jobs(t, ls, "kinds=image")
	eqList(t, "image jobs", jobPaths(j), []string{"a.PNG"})
	eqMap(t, "image service", states(), map[string]string{"a.PNG": "pending", "b.pdf": "", "c.txt": "", "empty.jpg": "", "pics.jpg": ""})

	// Both kinds; a.PNG done, b.pdf failed.
	j = jobs(t, ls, "kinds=image,pdf")
	eqList(t, "all jobs", jobPaths(j), []string{"b.pdf", "a.PNG"})
	eq(t, "pending", j.Pending, 2)
	a, b := hashOf([]byte("png a")), hashOf([]byte("pdf b"))
	fakeThumb(t, e.store(), a)
	done(t, ls, e.share, server.ThumbResult{Hash: a, OK: true}, server.ThumbResult{Hash: b, Error: "broken"})
	eqMap(t, "after done", states(), map[string]string{"a.PNG": "ready", "b.pdf": "failed", "c.txt": "", "empty.jpg": "", "pics.jpg": ""})

	// The service stops asking: pending turns into "", ready and failed stay.
	e.ana.mustPut("d.jpeg", "", []byte("jpeg d"))
	eq(t, "d pending", states()["d.jpeg"], "pending")
	e.clock.Advance(server.ThumbPollWindow + time.Second)
	eqMap(t, "service gone", states(), map[string]string{"a.PNG": "ready", "b.pdf": "failed", "c.txt": "", "d.jpeg": "", "empty.jpg": "", "pics.jpg": ""})
}

func eqMap(t *testing.T, what string, got, want map[string]string) {
	t.Helper()
	if len(got) != len(want) {
		t.Errorf("%s: %v, want %v", what, got, want)
		return
	}
	for k, v := range want {
		if g, ok := got[k]; !ok || g != v {
			t.Errorf("%s: %s = %q, want %q", what, k, g, v)
		}
	}
}

func TestThumbJobs(t *testing.T) {
	e := newEnv(t)
	ls := e.local()
	e.ana.mustPut("old.jpg", "", []byte("one"))
	e.ana.mustPut("copy.jpg", "", []byte("one")) // same content: one job
	e.ana.mustPut("new.png", "", []byte("two"))
	e.ana.mustPut("doc.pdf", "", []byte("three"))
	e.ana.mustPut("notes.md", "", []byte("four"))

	j := jobs(t, ls, "")
	eqList(t, "newest first, one per hash", jobPaths(j), []string{"doc.pdf", "new.png", "copy.jpg"})
	eq(t, "pending", j.Pending, 3)
	if x := j.Jobs[1]; x.ShareID != e.share || x.Hash != hashOf([]byte("two")) || x.Size != 3 || x.Kind != thumbs.KindImage {
		t.Errorf("job %+v", x)
	}
	j = jobs(t, ls, "limit=1&kinds=image")
	eqList(t, "limit", jobPaths(j), []string{"new.png"})
	eq(t, "pending of images", j.Pending, 2)
	resp, raw := call(t, "GET", ls.URL+"/v1/thumbs/jobs?kinds=video", "", nil, nil)
	wantStatus(t, "unknown kind", resp, raw, http.StatusBadRequest, server.CodeInvalidRequest)
	resp, raw = call(t, "GET", ls.URL+"/v1/thumbs/jobs?limit=0", "", nil, nil)
	wantStatus(t, "limit 0", resp, raw, http.StatusBadRequest, server.CodeInvalidRequest)

	// Done: none left. A rename or a copy of a done file makes no job.
	one, two, three := hashOf([]byte("one")), hashOf([]byte("two")), hashOf([]byte("three"))
	done(t, ls, e.share, server.ThumbResult{Hash: one, OK: true}, server.ThumbResult{Hash: two, OK: true},
		server.ThumbResult{Hash: three, Error: "pdftoppm: broken"})
	eqList(t, "all done", jobPaths(jobs(t, ls, "")), []string{})
	e.ana.mustOp(server.OpRequest{Op: "move", Path: "new.png", NewPath: "renamed.png", BaseHash: two})
	e.ana.mustPut("again.jpg", "", []byte("one"))
	eqList(t, "rename and copy", jobPaths(jobs(t, ls, "")), []string{})

	// New content is a new job.
	e.ana.mustPut("renamed.png", two, []byte("two, edited"))
	eqList(t, "replaced", jobPaths(jobs(t, ls, "")), []string{"renamed.png"})

	// A skipped job is not handed out again for a while.
	done(t, ls, e.share, server.ThumbResult{Hash: hashOf([]byte("two, edited")), Skipped: true})
	eqList(t, "skipped", jobPaths(jobs(t, ls, "")), []string{})
	e.clock.Advance(server.ThumbSkipDelay + time.Second)
	eqList(t, "skip over", jobPaths(jobs(t, ls, "")), []string{"renamed.png"})

	// rescan reads thumbnails/ again: a .fail the service wrote on its own
	// (a crash) counts, and so does a .fail it removed (--retry-failed).
	edited := hashOf([]byte("two, edited"))
	os.MkdirAll(filepath.Join(e.store().Dir, edited[:2]), 0o700)
	os.WriteFile(filepath.Join(e.store().Dir, edited[:2], edited+".fail"), []byte(thumbs.CrashReason), 0o600)
	fakeThumb(t, e.store(), one)
	fakeThumb(t, e.store(), two)
	eqList(t, "before rescan", jobPaths(jobs(t, ls, "")), []string{"renamed.png"})
	eqList(t, "rescan", jobPaths(jobs(t, ls, "rescan=1")), []string{"doc.pdf"}) // three had no .fail on disk

	// Bad reports.
	resp, raw = call(t, "POST", ls.URL+"/v1/thumbs/done", "", httpapi.ThumbsDoneRequest{ShareID: e.share, Results: []server.ThumbResult{{Hash: "../x", OK: true}}}, nil)
	wantStatus(t, "bad hash", resp, raw, http.StatusBadRequest, server.CodeInvalidRequest)
	resp, raw = call(t, "POST", ls.URL+"/v1/thumbs/done", "", httpapi.ThumbsDoneRequest{ShareID: "00000000-0000-4000-8000-000000000000"}, nil)
	wantStatus(t, "unknown share", resp, raw, http.StatusNotFound, server.CodeUnknownShare)
}

// Jobs take turns between folders.
func TestThumbJobsAcrossFolders(t *testing.T) {
	e := newEnv(t)
	ls := e.local()
	id2, tok2, err := e.srv.CreateShare("drugi", "zed")
	if err != nil {
		t.Fatal(err)
	}
	zed := &client{e: e, share: id2, user: "zed", token: tok2}
	for _, n := range []string{"1", "2", "3"} {
		e.ana.mustPut("a"+n+".jpg", "", []byte("a"+n))
	}
	zed.mustPut("z1.jpg", "", []byte("z1"))
	j := jobs(t, ls, "limit=3")
	eq(t, "pending", j.Pending, 4)
	shares := map[string]int{}
	for _, x := range j.Jobs {
		shares[x.ShareID]++
	}
	if shares[id2] != 1 || shares[e.share] != 2 {
		t.Errorf("jobs %+v", j.Jobs)
	}
}

func TestThumbsUnreferenced(t *testing.T) {
	e := newEnv(t)
	ls := e.local()
	kept, trashed, purged, never := []byte("kept"), []byte("trashed"), []byte("purged"), hashOf([]byte("never"))
	e.ana.mustPut("kept.jpg", "", kept)
	e.ana.mustPut("trashed.jpg", "", trashed)
	e.ana.mustPut("purged.jpg", "", purged)
	e.ana.del("trashed.jpg", hashOf(trashed))
	e.ana.del("purged.jpg", hashOf(purged))
	l := e.ana.mustTrashList(nil)
	for _, te := range l.Entries {
		if te.OrigPath == "purged.jpg" {
			e.ana.mustPurge(server.PurgeRequest{IDs: []int64{te.ID}})
		}
	}
	for _, h := range []string{hashOf(kept), hashOf(trashed), hashOf(purged), never} {
		fakeThumb(t, e.store(), h)
	}
	done(t, ls, e.share, server.ThumbResult{Hash: hashOf(purged), OK: true})

	ask := func(hs ...string) (*http.Response, []byte, httpapi.HashesBody) {
		var out httpapi.HashesBody
		resp, raw := call(t, "POST", ls.URL+"/v1/shares/"+e.share+"/admin/thumbs/unreferenced", "", httpapi.HashesBody{Hashes: hs}, &out)
		return resp, raw, out
	}
	resp, raw, out := ask(hashOf(kept), hashOf(trashed), hashOf(purged), never)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("%d %s", resp.StatusCode, raw)
	}
	eqList(t, "unreferenced", out.Hashes, []string{hashOf(purged), never})

	// The answer drops them from the index: content uploaded again is a
	// job again (the service deletes the old files first).
	e.ana.mustPut("back.jpg", "", purged)
	eqList(t, "uploaded again", jobPaths(jobs(t, ls, "")), []string{"back.jpg"})

	resp, raw, _ = ask("ABC")
	wantStatus(t, "bad hash", resp, raw, http.StatusBadRequest, server.CodeInvalidRequest)
	many := make([]string, server.MaxThumbHashes+1)
	for i := range many {
		many[i] = never
	}
	resp, raw, _ = ask(many...)
	wantStatus(t, "too many", resp, raw, http.StatusBadRequest, server.CodeInvalidRequest)
	resp, raw, out = ask()
	if resp.StatusCode != http.StatusOK || len(out.Hashes) != 0 {
		t.Errorf("empty: %d %s", resp.StatusCode, raw)
	}
}

// The service's routes are on the admin socket only.
func TestThumbServiceRoutesNotPublic(t *testing.T) {
	e := newEnv(t)
	for _, r := range []struct{ method, path string }{
		{"GET", "/v1/thumbs/jobs"},
		{"GET", "/v1/thumbs/jobs?kinds=image"},
		{"POST", "/v1/thumbs/done"},
		{"POST", "/v1/shares/" + e.share + "/admin/thumbs/unreferenced"},
	} {
		resp, raw := call(t, r.method, e.http.URL+r.path, e.ana.token, map[string]any{"share_id": e.share, "hashes": []string{}}, nil)
		wantStatus(t, r.method+" "+r.path, resp, raw, http.StatusNotFound, server.CodeNotFound)
	}
}

func TestThumbImage(t *testing.T) {
	e := newEnv(t)
	bob := e.member("bob")
	content := []byte("a photo")
	e.ana.mustPut("photo.jpg", "", content)
	h := hashOf(content)
	fakeThumb(t, e.store(), h)
	base := e.http.URL + "/v1/shares/" + e.share + "/admin/thumbs/"

	resp, raw := call(t, "GET", base+h, e.ana.token, nil, nil)
	if resp.StatusCode != http.StatusOK || string(raw) != "jpeg m "+h {
		t.Fatalf("default size: %d %q", resp.StatusCode, raw)
	}
	for k, v := range map[string]string{
		"Content-Type":           "image/jpeg",
		"Cache-Control":          "private, max-age=31536000, immutable",
		"X-Content-Type-Options": "nosniff",
		"ETag":                   `"` + h + `.m"`,
	} {
		if got := resp.Header.Get(k); got != v {
			t.Errorf("%s = %q, want %q", k, got, v)
		}
	}
	for _, s := range []string{"s", "m", "l"} {
		resp, raw := call(t, "GET", base+h+"?size="+s, e.ana.token, nil, nil)
		if resp.StatusCode != http.StatusOK || string(raw) != "jpeg "+s+" "+h || resp.Header.Get("ETag") != `"`+h+"."+s+`"` {
			t.Errorf("size %s: %d %q %s", s, resp.StatusCode, raw, resp.Header.Get("ETag"))
		}
	}
	resp, raw = call(t, "GET", base+h+"?size=l", e.ana.token, nil, nil, "If-None-Match", `"`+h+`.l"`)
	if resp.StatusCode != http.StatusNotModified || len(raw) != 0 {
		t.Errorf("If-None-Match: %d %q", resp.StatusCode, raw)
	}
	resp, _ = call(t, "GET", base+h+"?size=l", e.ana.token, nil, nil, "If-None-Match", `"`+h+`.m"`)
	eq(t, "other ETag", resp.StatusCode, http.StatusOK)

	other := hashOf([]byte("no thumbnail"))
	resp, raw = call(t, "GET", base+other, e.ana.token, nil, nil)
	wantStatus(t, "missing", resp, raw, http.StatusNotFound, server.CodeNotFound)

	for name, p := range map[string]string{
		"dot-dot":       "..%2F" + h[:61],
		"dot-dot long":  "..%2F..%2Fservice.db",
		"short":         h[:63],
		"long":          h + "0",
		"upper case":    strings.ToUpper(h),
		"unknown size":  h + "?size=xl",
		"size path":     h + "?size=..%2Fx",
		"marker suffix": h + ".fail",
	} {
		resp, raw := call(t, "GET", base+p, e.ana.token, nil, nil)
		wantStatus(t, name, resp, raw, http.StatusBadRequest, server.CodeInvalidRequest)
	}

	resp, raw = call(t, "GET", base+h, "", nil, nil)
	wantStatus(t, "no token", resp, raw, http.StatusUnauthorized, server.CodeUnauthorized)
	resp, raw = call(t, "GET", base+h, "shf_wrong", nil, nil)
	wantStatus(t, "bad token", resp, raw, http.StatusUnauthorized, server.CodeUnauthorized)
	resp, raw = call(t, "GET", base+h, bob.token, nil, nil)
	wantStatus(t, "member", resp, raw, http.StatusForbidden, server.CodeForbidden)

	// The admin of another folder gets neither this folder's thumbnail nor,
	// through their own folder, a hash only this one has.
	id2, tok2, err := e.srv.CreateShare("drugi", "zed")
	if err != nil {
		t.Fatal(err)
	}
	resp, raw = call(t, "GET", base+h, tok2, nil, nil)
	wantStatus(t, "other admin, this folder", resp, raw, http.StatusUnauthorized, server.CodeUnauthorized)
	resp, raw = call(t, "GET", e.http.URL+"/v1/shares/"+id2+"/admin/thumbs/"+h, tok2, nil, nil)
	wantStatus(t, "other admin, own folder", resp, raw, http.StatusNotFound, server.CodeNotFound)
}

// verify compares storage/ and trash/ with the database; thumbnails/ is
// neither and is not reported, nor touched by a repair.
func TestVerifyIgnoresThumbnails(t *testing.T) {
	e := newEnv(t)
	e.ana.mustPut("photo.jpg", "", []byte("photo"))
	h := hashOf([]byte("photo"))
	fakeThumb(t, e.store(), h)
	orphan := hashOf([]byte("gone long ago"))
	fakeThumb(t, e.store(), orphan)
	os.WriteFile(filepath.Join(e.store().Dir, orphan[:2], orphan+".fail"), []byte("x"), 0o600)
	for _, repair := range []bool{false, true} {
		res := e.ana.verify(repair)
		if len(res.Issues) != 0 {
			t.Errorf("repair=%v: issues %+v", repair, res.Issues)
		}
	}
	if !e.store().Ready(h) || !e.store().Ready(orphan) || !e.store().Failed(orphan) {
		t.Error("verify --repair changed thumbnails/")
	}
}
