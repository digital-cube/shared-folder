package httpapi_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"

	"github.com/digital-cube/shared-folder/internal/server"
	"github.com/digital-cube/shared-folder/internal/server/httpapi"
)

// wsServer serves the env's server again with short WebSocket intervals.
func (e *env) wsServer(opts ...httpapi.Option) (*httptest.Server, *httpapi.Handler) {
	e.t.Helper()
	h := httpapi.New(e.srv, nil, opts...)
	hs := httptest.NewServer(h)
	e.t.Cleanup(func() {
		h.Close()
		hs.Close()
	})
	return hs, h
}

type wsConn struct {
	t *testing.T
	c *websocket.Conn
}

func dialWS(t *testing.T, base, share, token string) (*wsConn, *http.Response, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	u := "ws" + strings.TrimPrefix(base, "http") + "/v1/shares/" + share + "/ws"
	c, resp, err := websocket.Dial(ctx, u, &websocket.DialOptions{HTTPHeader: http.Header{"Authorization": {"Bearer " + token}}})
	if err != nil {
		return nil, resp, err
	}
	t.Cleanup(func() { c.CloseNow() })
	return &wsConn{t: t, c: c}, resp, nil
}

func mustDial(t *testing.T, base, share, token string) *wsConn {
	t.Helper()
	w, _, err := dialWS(t, base, share, token)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	return w
}

// next reads one HEAD message, failing after d.
func (w *wsConn) next(d time.Duration) (int64, error) {
	ctx, cancel := context.WithTimeout(context.Background(), d)
	defer cancel()
	var m httpapi.HeadMessage
	err := wsjson.Read(ctx, w.c, &m)
	return m.Head, err
}

func (w *wsConn) mustHead(want int64) {
	w.t.Helper()
	got, err := w.next(5 * time.Second)
	if err != nil {
		w.t.Fatalf("waiting for head %d: %v", want, err)
	}
	if got != want {
		w.t.Fatalf("head = %d, want %d", got, want)
	}
}

func TestWSHeadOnConnect(t *testing.T) {
	e := newEnv(t)
	hs, _ := e.wsServer()
	mustDial(t, hs.URL, e.share, e.ana.token).mustHead(0)

	e.ana.mustPut("a.txt", "", []byte("a"))
	e.ana.mustOp(server.OpRequest{Op: "mkdir", Path: "dir"})
	mustDial(t, hs.URL, e.share, e.ana.token).mustHead(2)
}

func TestWSBroadcastToMembersOfTheFolderOnly(t *testing.T) {
	e := newEnv(t)
	hs, _ := e.wsServer()
	bob := e.member("bob")
	other, carlToken, err := e.srv.CreateShare("drugi", "carl")
	if err != nil {
		t.Fatal(err)
	}

	wa := mustDial(t, hs.URL, e.share, e.ana.token)
	wb := mustDial(t, hs.URL, e.share, bob.token)
	wc := mustDial(t, hs.URL, other, carlToken)
	for _, w := range []*wsConn{wa, wb, wc} {
		w.mustHead(0)
	}

	e.ana.mustPut("a.txt", "", []byte("a"))
	wa.mustHead(1) // the writer is told as well
	wb.mustHead(1)
	bob.mustPut("b.txt", "", []byte("b"))
	wa.mustHead(2)
	wb.mustHead(2)

	// a refused operation and an unchanged put add no event: no message
	bob.mustPut("b.txt", hashOf([]byte("b")), []byte("b"))
	if h, err := wa.next(300 * time.Millisecond); err == nil {
		t.Fatalf("message without a new event: head %d", h)
	}
	if h, err := wc.next(300 * time.Millisecond); err == nil {
		t.Fatalf("member of another folder got head %d", h)
	}
}

func TestWSRefusesBadTokens(t *testing.T) {
	e := newEnv(t)
	hs, _ := e.wsServer()
	_, carlToken, err := e.srv.CreateShare("drugi", "carl")
	if err != nil {
		t.Fatal(err)
	}
	for name, tok := range map[string]string{
		"missing":      "",
		"unknown":      server.NewToken(),
		"other folder": carlToken,
	} {
		_, resp, err := dialWS(t, hs.URL, e.share, tok)
		if err == nil {
			t.Fatalf("%s token: connected", name)
		}
		if resp == nil || resp.StatusCode != http.StatusUnauthorized {
			t.Fatalf("%s token: %v %v", name, resp, err)
		}
	}
}

func closeStatus(t *testing.T, w *wsConn, within time.Duration) websocket.StatusCode {
	t.Helper()
	deadline := time.Now().Add(within)
	for {
		_, err := w.next(time.Until(deadline))
		if err == nil {
			continue
		}
		if errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("connection still open after %v", within)
		}
		return websocket.CloseStatus(err)
	}
}

func TestWSRevokeClosesConnection(t *testing.T) {
	e := newEnv(t)
	hs, _ := e.wsServer(httpapi.WithMemberCheck(50 * time.Millisecond))
	bob, cid := e.member("bob"), e.member("cid")
	wa := mustDial(t, hs.URL, e.share, e.ana.token)
	wb := mustDial(t, hs.URL, e.share, bob.token)
	wb2 := mustDial(t, hs.URL, e.share, bob.token)
	for _, w := range []*wsConn{wa, wb, wb2} {
		w.mustHead(0)
	}

	sh, _ := e.srv.Share(e.share)
	if err := sh.RevokeMember("bob"); err != nil {
		t.Fatal(err)
	}
	for _, w := range []*wsConn{wb, wb2} {
		if st := closeStatus(t, w, 2*time.Second); st != httpapi.CloseRevoked {
			t.Fatalf("close status %v, want %v", st, httpapi.CloseRevoked)
		}
	}
	if _, _, err := dialWS(t, hs.URL, e.share, bob.token); err == nil {
		t.Fatal("revoked member connected again")
	}
	// ana stays connected and is still told
	e.ana.mustPut("a.txt", "", []byte("a"))
	wa.mustHead(1)

	// a revocation by another process (shfd member revoke) is seen by the
	// periodic member check
	wc := mustDial(t, hs.URL, e.share, cid.token)
	wc.mustHead(1)
	cli, err := server.Open(e.root, server.Options{SkipRecovery: true})
	if err != nil {
		t.Fatal(err)
	}
	defer cli.Close()
	csh, err := cli.Share(e.share)
	if err != nil {
		t.Fatal(err)
	}
	if err := csh.RevokeMember("cid"); err != nil {
		t.Fatal(err)
	}
	if st := closeStatus(t, wc, 2*time.Second); st != httpapi.CloseRevoked {
		t.Fatalf("close status %v, want %v", st, httpapi.CloseRevoked)
	}
}

func TestWSDropsClientThatDoesNotAnswerPings(t *testing.T) {
	e := newEnv(t)
	hs, _ := e.wsServer(httpapi.WithPingInterval(50 * time.Millisecond))
	sh, _ := e.srv.Share(e.share)

	// a client that reads answers pings and stays
	live := mustDial(t, hs.URL, e.share, e.ana.token)
	live.mustHead(0)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	liveCtx := live.c.CloseRead(ctx)

	// a client that never reads never sends a pong
	mustDial(t, hs.URL, e.share, e.ana.token)
	waitFor(t, 3*time.Second, "the silent client is dropped", func() bool { return sh.Watchers() == 1 })
	time.Sleep(200 * time.Millisecond)
	if liveCtx.Err() != nil || sh.Watchers() != 1 {
		t.Fatalf("the answering client was dropped (watchers %d)", sh.Watchers())
	}
}

func TestWSShutdownSaysGoingAway(t *testing.T) {
	e := newEnv(t)
	hs, h := e.wsServer()
	w := mustDial(t, hs.URL, e.share, e.ana.token)
	w.mustHead(0)
	h.Close()
	if st := closeStatus(t, w, 2*time.Second); st != httpapi.CloseGoingAway {
		t.Fatalf("close status %v, want %v", st, httpapi.CloseGoingAway)
	}
}

func waitFor(t *testing.T, d time.Duration, what string, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(d)
	for !ok() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting until %s", what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}
