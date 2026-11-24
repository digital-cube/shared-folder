package httpapi_test

import (
	"bytes"
	"encoding/json"
	"hash/crc32"
	"image"
	"image/color"
	"image/png"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/digital-cube/shared-folder/internal/server"
	"github.com/digital-cube/shared-folder/internal/server/httpapi"
	"github.com/digital-cube/shared-folder/internal/thumbs"
)

func testPNG(t *testing.T, w, h int) []byte {
	t.Helper()
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Set(x, y, color.NRGBA{uint8(x), uint8(y), 200, 255})
		}
	}
	var b bytes.Buffer
	if err := png.Encode(&b, img); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

// putRaw sends body as the raw body of a request.
func (e *env) putRaw(method, path, token, contentType string, body []byte) (int, http.Header, []byte) {
	e.t.Helper()
	req, err := http.NewRequest(method, e.http.URL+path, bytes.NewReader(body))
	if err != nil {
		e.t.Fatal(err)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		e.t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, resp.Header, b
}

func TestAvatarAPI(t *testing.T) {
	dbRoot := t.TempDir()
	e := newEnv(t, func(o *server.Options) { p := cheapPW; o.PasswordParams = &p; o.DBRoot = dbRoot })
	e.account("bob", "bobova-lozinka")
	bob := e.loginOK("bob", "bobova-lozinka", "gui")
	u, err := e.srv.UserByName("bob")
	if err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(dbRoot, server.AvatarsDir, strconv.FormatInt(u.ID, 10)+".jpg")

	// No picture yet: null in /v1/me, 404 for the picture.
	var me httpapi.MeResponse
	_, _, b := e.call("GET", "/v1/me", bob, nil)
	if json.Unmarshal(b, &me); me.User.AvatarURL != nil || !strings.Contains(string(b), `"avatar_url":null`) {
		t.Errorf("me without a picture: %s", b)
	}
	if st, _, _ := e.call("GET", "/v1/users/bob/avatar", bob, nil); st != 404 {
		t.Errorf("no picture: %d", st)
	}

	// A raw PNG body.
	st, _, b := e.putRaw("PUT", "/v1/me/avatar", bob, "image/png", testPNG(t, 300, 200))
	var ar httpapi.AvatarResponse
	if json.Unmarshal(b, &ar); st != 200 || !strings.HasPrefix(ar.AvatarURL, "/v1/users/bob/avatar?v=") {
		t.Fatalf("put: %d %s", st, b)
	}
	if ar.User == nil || ar.User.AvatarURL == nil || *ar.User.AvatarURL != ar.AvatarURL {
		t.Errorf("put user: %s", b)
	}
	stored, err := os.ReadFile(file)
	if err != nil {
		t.Fatalf("picture not in the db-root: %v", err)
	}
	if _, err := os.Stat(filepath.Join(e.root, server.AvatarsDir)); err == nil {
		t.Error("pictures written to the data-root")
	}
	if left, _ := filepath.Glob(filepath.Join(dbRoot, server.AvatarsDir, ".*")); len(left) != 0 {
		t.Errorf("temporary files left: %v", left)
	}
	_, _, b = e.call("GET", "/v1/me", bob, nil)
	if json.Unmarshal(b, &me); me.User.AvatarURL == nil || *me.User.AvatarURL != ar.AvatarURL {
		t.Errorf("me after put: %s", b)
	}

	// Fetching it: the current version is cached for good, any other
	// version is revalidated with the ETag.
	st, h, body := e.call("GET", ar.AvatarURL, bob, nil)
	if st != 200 || h.Get("Cache-Control") != httpapi.AvatarCacheForever || h.Get("Content-Type") != "image/jpeg" || !bytes.Equal(body, stored) {
		t.Fatalf("get: %d %v", st, h)
	}
	if h.Get("X-Content-Type-Options") != "nosniff" {
		t.Errorf("no nosniff: %v", h)
	}
	if cfg, format, err := image.DecodeConfig(bytes.NewReader(body)); err != nil || format != "jpeg" || cfg.Width != thumbs.AvatarPx || cfg.Height != thumbs.AvatarPx {
		t.Errorf("picture is %s %dx%d %v", format, cfg.Width, cfg.Height, err)
	}
	for _, p := range []string{"/v1/users/bob/avatar", "/v1/users/bob/avatar?v=1"} {
		st, h, _ := e.call("GET", p, bob, nil)
		if st != 200 || h.Get("Cache-Control") != "private, no-cache" || h.Get("ETag") == "" {
			t.Errorf("%s: %d %v", p, st, h)
		}
	}
	if st, _, _ := e.call("GET", ar.AvatarURL, bob, nil, "If-None-Match", h.Get("ETag")); st != 304 {
		t.Errorf("If-None-Match: %d", st)
	}

	// Who may see it: any account, any member token; nobody without one.
	if st, h, b := e.call("GET", ar.AvatarURL, "", nil); st != 401 || errCode(b) != server.CodeUnauthorized || h.Get("Cache-Control") != "no-store" {
		t.Errorf("without a token: %d %v %s", st, h, b)
	}
	if st, _, _ := e.call("GET", ar.AvatarURL, "shf_nonsense", nil); st != 401 {
		t.Errorf("bad member token: %d", st)
	}
	if st, _, _ := e.call("GET", ar.AvatarURL, "shd_nonsense", nil); st != 401 {
		t.Errorf("bad device token: %d", st)
	}
	if st, _, _ := e.call("GET", ar.AvatarURL, e.ana.token, nil); st != 200 {
		t.Errorf("member token: %d", st)
	}
	if st, _, _ := e.call("GET", "/v1/users/nobody/avatar", bob, nil); st != 404 {
		t.Errorf("unknown account: %d", st)
	}
	if st, _, _ := e.putRaw("PUT", "/v1/me/avatar", "", "image/png", testPNG(t, 10, 10)); st != 401 {
		t.Errorf("put without a token: %d", st)
	}
	if st, _, _ := e.putRaw("PUT", "/v1/me/avatar", e.ana.token, "image/png", testPNG(t, 10, 10)); st != 401 {
		t.Errorf("put with a member token: %d", st)
	}

	// A multipart upload replaces it under a new version.
	var mp bytes.Buffer
	mw := multipart.NewWriter(&mp)
	mw.WriteField("note", "ignored")
	fw, _ := mw.CreateFormFile("avatar", "me.png")
	fw.Write(testPNG(t, 120, 160))
	mw.Close()
	st, _, b = e.putRaw("PUT", "/v1/me/avatar", bob, mw.FormDataContentType(), mp.Bytes())
	var ar2 httpapi.AvatarResponse
	if json.Unmarshal(b, &ar2); st != 200 || ar2.AvatarURL == ar.AvatarURL {
		t.Fatalf("multipart: %d %s", st, b)
	}
	if st, h, _ := e.call("GET", ar.AvatarURL, bob, nil); st != 200 || h.Get("Cache-Control") != "private, no-cache" {
		t.Errorf("the old version: %d %v", st, h)
	}

	// Refused: too large, not an image (whatever it claims), a bomb. None
	// of them changes the picture.
	big := append(testPNG(t, 10, 10), make([]byte, server.MaxAvatarBytes)...)
	if st, _, b := e.putRaw("PUT", "/v1/me/avatar", bob, "image/png", big); st != 413 || errCode(b) != server.CodeImageTooLarge {
		t.Errorf("too large: %d %s", st, b)
	}
	if st, _, b := e.putRaw("PUT", "/v1/me/avatar", bob, "image/png", []byte("<html><body>me.png</body></html>")); st != 415 || errCode(b) != server.CodeUnsupportedImage {
		t.Errorf("html as png: %d %s", st, b)
	}
	if st, _, b := e.putRaw("PUT", "/v1/me/avatar", bob, "image/png", bombPNG(t, 50_000, 50_000)); st != 422 || errCode(b) != server.CodeImageTooManyPixels {
		t.Errorf("bomb: %d %s", st, b)
	}
	if st, _, b := e.putRaw("PUT", "/v1/me/avatar", bob, "image/png", nil); st != 415 {
		t.Errorf("empty: %d %s", st, b)
	}
	if u, _ := e.srv.UserByName("bob"); u.AvatarURL == nil || *u.AvatarURL != ar2.AvatarURL {
		t.Errorf("a refused upload changed the picture: %v", u.AvatarURL)
	}

	// The folder's member list shows the picture of a member's account.
	if _, _, err := e.srv.AddUserMember(mustShare(t, e), "bob", false); err != nil {
		t.Fatal(err)
	}
	var ml server.MemberList
	if err := e.ana.do("GET", "/admin/members", nil, &ml); err != nil {
		t.Fatal(err)
	}
	for _, m := range ml.Members {
		switch m.Username {
		case "bob":
			if m.AvatarURL == nil || *m.AvatarURL != ar2.AvatarURL {
				t.Errorf("member bob: %v", m.AvatarURL)
			}
		default:
			if m.AvatarURL != nil {
				t.Errorf("member %s: %v", m.Username, *m.AvatarURL)
			}
		}
	}

	// Removing it.
	st, _, b = e.call("DELETE", "/v1/me/avatar", bob, nil)
	if st != 200 || !strings.Contains(string(b), `"removed":true`) {
		t.Fatalf("delete: %d %s", st, b)
	}
	if _, err := os.Stat(file); !os.IsNotExist(err) {
		t.Errorf("file after delete: %v", err)
	}
	if st, h, _ := e.call("GET", ar2.AvatarURL, bob, nil); st != 404 || h.Get("Cache-Control") != "no-store" {
		t.Errorf("get after delete: %d %v", st, h)
	}
	_, _, b = e.call("GET", "/v1/me", bob, nil)
	if !strings.Contains(string(b), `"avatar_url":null`) {
		t.Errorf("me after delete: %s", b)
	}
	if st, _, b := e.call("DELETE", "/v1/me/avatar", bob, nil); st != 200 || !strings.Contains(string(b), `"removed":false`) {
		t.Errorf("delete twice: %d %s", st, b)
	}

	// A new picture after a removal gets a version no browser has cached.
	e.clock.Advance(time.Second)
	st, _, b = e.putRaw("PUT", "/v1/me/avatar", bob, "image/png", testPNG(t, 64, 64))
	var ar3 httpapi.AvatarResponse
	if json.Unmarshal(b, &ar3); st != 200 || ar3.AvatarURL == ar.AvatarURL || ar3.AvatarURL == ar2.AvatarURL {
		t.Errorf("after removal: %d %s (before %s, %s)", st, b, ar.AvatarURL, ar2.AvatarURL)
	}
}

func mustShare(t *testing.T, e *env) *server.Share {
	t.Helper()
	sh, err := e.srv.Share(e.share)
	if err != nil {
		t.Fatal(err)
	}
	return sh
}

// bombPNG is a PNG header that claims w×h pixels, with no pixels behind
// it: a decompression bomb as its header shows it.
func bombPNG(t *testing.T, w, h uint32) []byte {
	data := testPNG(t, 1, 1)
	be := func(b []byte, v uint32) { b[0], b[1], b[2], b[3] = byte(v>>24), byte(v>>16), byte(v>>8), byte(v) }
	be(data[16:], w)
	be(data[20:], h)
	be(data[29:], crc32.ChecksumIEEE(data[12:29]))
	return data
}
