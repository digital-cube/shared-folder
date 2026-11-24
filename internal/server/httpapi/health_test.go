package httpapi_test

import (
	"encoding/json"
	"io"
	"net/http"
	"testing"
	"time"

	"github.com/digital-cube/shared-folder/internal/server"
	"github.com/digital-cube/shared-folder/internal/server/httpapi"
	"github.com/digital-cube/shared-folder/internal/version"
)

// raw sends an unauthenticated request to the env's server.
func (e *env) raw(method, path string) (*http.Response, []byte) {
	e.t.Helper()
	req, err := http.NewRequest(method, e.http.URL+path, nil)
	if err != nil {
		e.t.Fatal(err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		e.t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp, b
}

func TestHealthOK(t *testing.T) {
	e := newEnv(t)
	for _, p := range []string{"/healthz", "/"} {
		resp, b := e.raw("GET", p)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("GET %s: %d %s", p, resp.StatusCode, b)
		}
		if ct := resp.Header.Get("Content-Type"); ct != "application/json" {
			t.Errorf("GET %s: Content-Type %q", p, ct)
		}
		if cc := resp.Header.Get("Cache-Control"); cc != "no-store" {
			t.Errorf("GET %s: Cache-Control %q", p, cc)
		}
		// Exactly these keys: nothing about folders, members or paths (mail
		// says only whether forgotten-password mails go out).
		var m map[string]any
		if err := json.Unmarshal(b, &m); err != nil {
			t.Fatal(err)
		}
		want := map[string]any{"service": "shfd", "version": version.Version, "ok": true, "mail": "disabled"}
		if len(m) != len(want) {
			t.Errorf("GET %s: body %s, want only %v", p, b, want)
		}
		for k, v := range want {
			if m[k] != v {
				t.Errorf("GET %s: %s = %v, want %v", p, k, m[k], v)
			}
		}

		resp, b = e.raw("HEAD", p)
		if resp.StatusCode != http.StatusOK || len(b) != 0 {
			t.Errorf("HEAD %s: %d, %d bytes", p, resp.StatusCode, len(b))
		}
	}
}

func TestHealthVersion(t *testing.T) {
	old := version.Version
	version.Version = "v9.8.7-test"
	t.Cleanup(func() { version.Version = old })
	e := newEnv(t)
	_, b := e.raw("GET", "/healthz")
	var h httpapi.Health
	if err := json.Unmarshal(b, &h); err != nil {
		t.Fatal(err)
	}
	if h.Version != "v9.8.7-test" {
		t.Errorf("version %q", h.Version)
	}
}

func TestHealthRegistryUnavailable(t *testing.T) {
	e := newEnv(t)
	e.srv.Close()
	for _, p := range []string{"/healthz", "/"} {
		resp, b := e.raw("GET", p)
		if resp.StatusCode != http.StatusServiceUnavailable {
			t.Fatalf("GET %s: %d %s", p, resp.StatusCode, b)
		}
		var h httpapi.Health
		if err := json.Unmarshal(b, &h); err != nil {
			t.Fatal(err)
		}
		want := httpapi.Health{Service: "shfd", Version: version.Version, OK: false, Error: "registry unavailable", Mail: "disabled"}
		if h != want {
			t.Errorf("GET %s: %+v, want %+v", p, h, want)
		}
		if cc := resp.Header.Get("Cache-Control"); cc != "no-store" {
			t.Errorf("GET %s: Cache-Control %q", p, cc)
		}
	}
}

func TestHealthMethodNotAllowed(t *testing.T) {
	e := newEnv(t)
	for _, m := range []string{"POST", "PUT", "DELETE"} {
		for _, p := range []string{"/healthz", "/"} {
			resp, b := e.raw(m, p)
			if resp.StatusCode != http.StatusMethodNotAllowed {
				t.Errorf("%s %s: %d %s", m, p, resp.StatusCode, b)
			}
			if a := resp.Header.Get("Allow"); a != "GET, HEAD" {
				t.Errorf("%s %s: Allow %q", m, p, a)
			}
		}
	}
}

func TestUnknownPathStillNotFound(t *testing.T) {
	e := newEnv(t)
	for _, p := range []string{"/nepostojece", "/healthz/x", "/v1"} {
		resp, b := e.raw("GET", p)
		if resp.StatusCode != http.StatusNotFound {
			t.Fatalf("GET %s: %d %s", p, resp.StatusCode, b)
		}
		var eb httpapi.ErrorBody
		if err := json.Unmarshal(b, &eb); err != nil {
			t.Fatal(err)
		}
		if eb.Error.Code != server.CodeNotFound {
			t.Errorf("GET %s: code %q", p, eb.Error.Code)
		}
	}
}

// A folder whose writer hangs (a lock held, as by a goroutine stuck in
// the kernel under SQLite) makes the health check fail, naming the folder
// and nothing else; once it lets go the check passes again.
func TestHealthShareWriterStuck(t *testing.T) {
	e := newEnv(t)
	sh, err := e.srv.Share(e.share)
	if err != nil {
		t.Fatal(err)
	}
	release := sh.HoldWriter()
	start := time.Now()
	resp, b := e.raw("GET", "/healthz")
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("GET /healthz: %d %s", resp.StatusCode, b)
	}
	if d := time.Since(start); d > httpapi.HealthTimeout+time.Second {
		t.Errorf("health check took %v", d)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	want := map[string]any{"service": "shfd", "version": version.Version, "ok": false, "error": "share " + e.share + " stuck", "mail": "disabled"}
	if len(m) != len(want) {
		t.Errorf("body %s, want only %v", b, want)
	}
	for k, v := range want {
		if m[k] != v {
			t.Errorf("%s = %v, want %v", k, m[k], v)
		}
	}
	release()
	if resp, b := e.raw("GET", "/healthz"); resp.StatusCode != http.StatusOK {
		t.Errorf("after release: %d %s", resp.StatusCode, b)
	}
}

// The health check works the same with the databases in their own db-root.
func TestHealthSeparateDBRoot(t *testing.T) {
	db := t.TempDir()
	e := newEnv(t, func(o *server.Options) { o.DBRoot = db })
	if resp, b := e.raw("GET", "/healthz"); resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /healthz: %d %s", resp.StatusCode, b)
	}
	if e.srv.DBRoot() != db {
		t.Errorf("DBRoot %s", e.srv.DBRoot())
	}
}
