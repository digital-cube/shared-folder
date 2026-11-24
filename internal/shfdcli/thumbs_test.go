package shfdcli

import (
	"bytes"
	"context"
	"encoding/json"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/digital-cube/shared-folder/internal/server"
	"github.com/digital-cube/shared-folder/internal/thumbs"
)

// apiClient is a member of a folder over shfd serve's HTTP API.
type apiClient struct {
	t                  *testing.T
	addr, share, token string
	root               string
}

func (c *apiClient) call(method, path string, in, out any) {
	c.t.Helper()
	var body io.Reader
	if in != nil {
		b, _ := json.Marshal(in)
		body = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, "http://"+c.addr+"/v1/shares/"+c.share+path, body)
	req.Header.Set("Authorization", "Bearer "+c.token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		c.t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		c.t.Fatalf("%s %s: %d %s", method, path, resp.StatusCode, raw)
	}
	if out != nil {
		if err := json.Unmarshal(raw, out); err != nil {
			c.t.Fatal(err)
		}
	}
}

// put uploads content to p the way a client does: announce, write the
// content where the transport would, commit.
func (c *apiClient) put(p, base string, content []byte) {
	c.t.Helper()
	var ar server.AnnounceResult
	c.call("POST", "/uploads", server.AnnounceRequest{Path: p, BaseHash: base, NewHash: sha(content), Size: int64(len(content))}, &ar)
	if err := os.WriteFile(filepath.Join(c.root, c.share, ar.Dest), content, 0o600); err != nil {
		c.t.Fatal(err)
	}
	c.call("POST", "/uploads/"+ar.UploadID+"/commit", nil, nil)
}

func (c *apiClient) thumbStates() map[string]string {
	c.t.Helper()
	var l server.DirListing
	c.call("GET", "/admin/files?dir=", nil, &l)
	out := map[string]string{}
	for _, e := range l.Entries {
		out[e.Name] = e.Thumb
	}
	return out
}

func pngOf(t *testing.T, w, h int, c color.Color) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Set(x, y, c)
		}
	}
	var b bytes.Buffer
	if err := png.Encode(&b, img); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

// pendingJobs asks the running server, over admin.sock, how many hashes
// wait for a thumbnail of kinds.
func pendingJobs(t *testing.T, root string, kinds ...thumbs.Kind) int {
	t.Helper()
	c := newThumbsClient(root)
	defer c.close()
	j, err := c.jobs(context.Background(), kinds, false)
	if err != nil {
		t.Fatal(err)
	}
	return j.Pending
}

// shfd thumbs --once against a real shfd serve, through admin.sock.
func TestThumbsWithServe(t *testing.T) {
	root := t.TempDir()
	env := map[string]string{EnvDataRoot: root}
	_, out, _ := run(t, env, "share", "create", "tim", "--admin", "ana")
	id, token := idRE.FindStringSubmatch(out)[1], tokenRE.FindStringSubmatch(out)[1]
	addr, _ := startServeAt(t, root)
	ana := &apiClient{t: t, addr: addr, share: id, token: token, root: root}
	_, pdfErr := exec.LookPath("pdftoppm")
	hasPDF := pdfErr == nil

	red, blue := pngOf(t, 600, 300, color.RGBA{255, 0, 0, 255}), pngOf(t, 300, 600, color.RGBA{0, 0, 255, 255})
	pdf, err := os.ReadFile("../thumbs/testdata/halfred-200x100.pdf")
	if err != nil {
		t.Fatal(err)
	}
	crash := []byte("not a jpeg, and a decoder once died on it")
	ana.put("photo.png", "", red)
	ana.put("doc.pdf", "", pdf)
	ana.put("notes.txt", "", []byte("text"))
	ana.put("crash.jpg", "", crash)
	st := thumbs.NewStore(filepath.Join(root, id))
	// An earlier run died while it made crash.jpg's thumbnail.
	os.MkdirAll(filepath.Join(st.Dir, sha(crash)[:2]), 0o700)
	os.WriteFile(filepath.Join(st.Dir, sha(crash)[:2], sha(crash)+".work"), nil, 0o600)

	thumbsOnce := func(args ...string) string {
		t.Helper()
		code, _, errOut := run(t, env, append([]string{"thumbs", "--once"}, args...)...)
		if code != 0 {
			t.Fatalf("thumbs --once: %d\n%s", code, errOut)
		}
		return errOut
	}
	log := thumbsOnce()
	mustMatch(t, "first run", log, `connected to shfd serve`, `msg=thumbnail .*hash=`+sha(red)+` kind=image .*outcome=ready`,
		`a thumbnail was being made when shfd thumbs stopped.*hash=`+sha(crash))
	if strings.Contains(log, "photo.png") {
		t.Errorf("a path in the log above Debug:\n%s", log)
	}
	if !st.Ready(sha(red)) {
		t.Fatal("no thumbnail of photo.png")
	}
	if hasPDF != st.Ready(sha(pdf)) {
		t.Errorf("PDF thumbnail ready=%v with pdftoppm=%v", st.Ready(sha(pdf)), hasPDF)
	}
	if r, _ := st.FailReason(sha(crash)); r != thumbs.CrashReason {
		t.Errorf("crash.jpg: .fail %q", r)
	}
	states := ana.thumbStates()
	want := map[string]string{"photo.png": "ready", "notes.txt": "", "crash.jpg": "failed", "doc.pdf": ""}
	if hasPDF {
		want["doc.pdf"] = "ready"
	}
	for k, v := range want {
		if states[k] != v {
			t.Errorf("%s: thumb %q, want %q", k, states[k], v)
		}
	}
	if n := pendingJobs(t, root, thumbs.KindImage); n != 0 {
		t.Errorf("pending after the run: %d", n)
	}

	// A rename needs no new thumbnail.
	ana.call("POST", "/ops", server.OpRequest{Op: "move", Path: "photo.png", NewPath: "renamed.png", BaseHash: sha(red)}, nil)
	if n := pendingJobs(t, root, thumbs.KindImage); n != 0 {
		t.Errorf("pending after a rename: %d", n)
	}
	// New content does.
	ana.put("renamed.png", sha(red), blue)
	if n := pendingJobs(t, root, thumbs.KindImage); n != 1 {
		t.Errorf("pending after new content: %d", n)
	}
	thumbsOnce()
	if !st.Ready(sha(blue)) || !st.Ready(sha(red)) { // red is in the trash: kept
		t.Fatalf("ready: blue %v, red %v", st.Ready(sha(blue)), st.Ready(sha(red)))
	}
	f, _ := os.Open(st.Path(sha(blue), "l"))
	cfg, err := jpeg.DecodeConfig(f)
	f.Close()
	if err != nil || cfg.Width != 256 || cfg.Height != 512 {
		t.Errorf("blue l: %+v %v", cfg, err)
	}

	// Deleted and purged: the cleanup removes both thumbnails.
	ana.call("POST", "/ops", server.OpRequest{Op: "delete", Path: "renamed.png", BaseHash: sha(blue)}, nil)
	thumbsOnce()
	if !st.Ready(sha(blue)) {
		t.Fatal("thumbnail of a file in the trash removed")
	}
	if code, _, errOut := run(t, env, "trash", "purge", id, "--all", "--yes"); code != 0 {
		t.Fatalf("purge: %s", errOut)
	}
	log = thumbsOnce()
	mustMatch(t, "cleanup", log, `msg="thumbnail cleanup" removed=2`)
	if st.Ready(sha(blue)) || st.Ready(sha(red)) {
		t.Error("thumbnails of purged files kept")
	}
	if _, err := os.Stat(st.Path(sha(red), "l")); err == nil {
		t.Error("l of a purged file kept")
	}

	// --retry-failed tries crash.jpg again; now it fails for real.
	log = thumbsOnce("--retry-failed")
	mustMatch(t, "retry", log, `failed thumbnails cleared for another try" .*count=1`, `hash=`+sha(crash)+` kind=image .*outcome=failed reason="not a readable image`)
	if r, _ := st.FailReason(sha(crash)); !strings.HasPrefix(r, "not a readable image") {
		t.Errorf("crash.jpg after retry: %q", r)
	}

	// A second service on the same data-root is refused.
	unlock, err := lockFile(filepath.Join(root, ThumbsLock), "held by the test")
	if err != nil {
		t.Fatal(err)
	}
	code, _, errOut := run(t, env, "thumbs", "--once")
	unlock()
	if code != 1 || !strings.Contains(errOut, "another shfd thumbs runs") {
		t.Errorf("second service: %d %s", code, errOut)
	}
}

func TestThumbsOnceWithoutServe(t *testing.T) {
	root := t.TempDir()
	env := map[string]string{EnvDataRoot: root}
	run(t, env, "share", "create", "tim", "--admin", "ana")
	code, _, errOut := run(t, env, "thumbs", "--once")
	if code != 1 || !strings.Contains(errOut, "shfd serve does not answer") {
		t.Errorf("exit %d: %s", code, errOut)
	}
	if code, _, _ := run(t, env, "thumbs", "--workers", "0"); code != 2 {
		t.Errorf("--workers 0: exit %d", code)
	}
}

// The service waits for shfd serve, saying so once, and then works.
func TestThumbsServiceWaitsForServe(t *testing.T) {
	old := thumbsIdle
	thumbsIdle = 20 * time.Millisecond
	t.Cleanup(func() { thumbsIdle = old })
	root := t.TempDir()
	env := map[string]string{EnvDataRoot: root}
	_, out, _ := run(t, env, "share", "create", "tim", "--admin", "ana")
	id, token := idRE.FindStringSubmatch(out)[1], tokenRE.FindStringSubmatch(out)[1]

	ctx, cancel := context.WithCancel(context.Background())
	var logBuf syncBuf
	exited := make(chan int, 1)
	go func() {
		exited <- Run([]string{"thumbs", "--data-root", root, "--workers", "2"}, Env{Stdout: io.Discard, Stderr: &logBuf, Ctx: ctx, Getenv: func(string) string { return "" }})
	}()
	time.Sleep(200 * time.Millisecond) // about ten polls without a server
	addr, _ := startServeAt(t, root)
	ana := &apiClient{t: t, addr: addr, share: id, token: token, root: root}
	img := pngOf(t, 40, 30, color.RGBA{0, 255, 0, 255})
	ana.put("green.png", "", img)
	st := thumbs.NewStore(filepath.Join(root, id))
	deadline := time.Now().Add(10 * time.Second)
	for !st.Ready(sha(img)) && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if !st.Ready(sha(img)) {
		t.Fatalf("no thumbnail:\n%s", logBuf.String())
	}
	if s := ana.thumbStates()["green.png"]; s != "ready" {
		t.Errorf("thumb %q", s)
	}
	cancel()
	select {
	case code := <-exited:
		if code != 0 {
			t.Errorf("exit %d", code)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("thumbs did not stop")
	}
	log := logBuf.String()
	if n := strings.Count(log, "does not answer"); n != 1 {
		t.Errorf("%d lines about the missing server, want 1:\n%s", n, log)
	}
	mustMatch(t, "service", log, `connected to shfd serve`, `outcome=ready`, `shfd thumbs stopped`)
}
