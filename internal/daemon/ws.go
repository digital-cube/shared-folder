package daemon

import (
	"context"
	"errors"
	"math/rand/v2"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
)

// headMessage is what the server's WebSocket sends
// (internal/server/httpapi.HeadMessage).
type headMessage struct {
	Head *int64 `json:"head"`
}

// closeRevoked is the close status of a revoked token
// (httpapi.CloseRevoked).
const closeRevoked = websocket.StatusPolicyViolation

// wsURL is the WebSocket of a folder on the server at base (http[s]://).
func wsURL(base, shareID string) (string, error) {
	u, err := url.Parse(strings.TrimRight(base, "/"))
	if err != nil {
		return "", err
	}
	switch u.Scheme {
	case "https":
		u.Scheme = "wss"
	case "http":
		u.Scheme = "ws"
	default:
		return "", errors.New("server URL must be http or https")
	}
	u.Path += "/v1/shares/" + url.PathEscape(shareID) + "/ws"
	return u.String(), nil
}

// websocket keeps the folder's WebSocket open: every HEAD it brings is
// handed to the loop; after a break it reconnects with a growing pause
// (1 s doubling up to Settings.ReconnectMax) with random jitter.
func (f *folder) websocket(ctx context.Context) {
	u, err := wsURL(f.fc.URL, f.fc.ShareID)
	if err != nil {
		f.log.Error("no WebSocket; polling only", "err", err)
		return
	}
	limit := time.Duration(f.set.ReconnectMax)
	pause := time.Second
	logged := ""
	for ctx.Err() == nil {
		got, err := f.wsSession(ctx, u)
		f.setWSUp(false)
		if ctx.Err() != nil {
			return
		}
		if got {
			pause = time.Second // it worked for a while: start over
		}
		if websocket.CloseStatus(err) == closeRevoked {
			pause = limit
		}
		if msg := errText(err); msg != logged {
			f.log.Warn("WebSocket down; polling until it is back", "err", msg)
			logged = msg
		}
		// full jitter over the upper half: [pause/2, pause)
		d := pause/2 + rand.N(pause/2+1)
		select {
		case <-ctx.Done():
			return
		case <-time.After(d):
		}
		pause = min(2*pause, limit)
		if got {
			logged = ""
		}
	}
}

func errText(err error) string {
	if err == nil {
		return "closed"
	}
	return err.Error()
}

// wsSession runs one connection until it breaks; got says whether a
// message arrived.
func (f *folder) wsSession(ctx context.Context, u string) (got bool, err error) {
	dctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	c, resp, err := websocket.Dial(dctx, u, &websocket.DialOptions{
		HTTPClient: f.cfg.Options.HTTPClient,
		HTTPHeader: http.Header{"Authorization": {"Bearer " + f.fc.Token}},
	})
	cancel()
	if resp != nil && resp.Body != nil {
		resp.Body.Close()
	}
	if err != nil {
		return false, err
	}
	defer c.CloseNow()
	c.SetReadLimit(1 << 10)
	sctx, stop := context.WithCancel(ctx)
	defer stop()
	go f.wsPinger(sctx, c)
	for {
		var m headMessage
		if err := wsjson.Read(sctx, c, &m); err != nil {
			if ctx.Err() != nil {
				c.Close(websocket.StatusNormalClosure, "client stopping")
			}
			return got, err
		}
		if m.Head == nil {
			continue
		}
		if !got {
			got = true
			f.setWSUp(true)
			f.log.Info("WebSocket connected", "head", *m.Head)
		}
		f.heardHead(*m.Head)
	}
}

// wsPinger pings every Settings.Ping; no pong within the same time drops
// the connection, and the session reconnects.
func (f *folder) wsPinger(ctx context.Context, c *websocket.Conn) {
	d := time.Duration(f.set.Ping)
	t := time.NewTicker(d)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		pctx, cancel := context.WithTimeout(ctx, d)
		err := c.Ping(pctx)
		cancel()
		if err != nil {
			c.CloseNow()
			return
		}
	}
}
