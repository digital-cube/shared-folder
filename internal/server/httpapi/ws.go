package httpapi

import (
	"context"
	"net/http"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"

	"github.com/digital-cube/shared-folder/internal/server"
)

// HeadMessage is the only message of GET /v1/shares/{id}/ws: the newest
// seq of the folder's log. The events themselves are read from
// /changes?since=N.
type HeadMessage struct {
	Head int64 `json:"head"`
}

// Close codes a client sees when the server ends the WebSocket.
const (
	// CloseRevoked: the member's token was revoked; do not reconnect.
	CloseRevoked = websocket.StatusPolicyViolation
	// CloseGoingAway: the server is shutting down; reconnect later.
	CloseGoingAway = websocket.StatusGoingAway
)

// ws is GET /v1/shares/{id}/ws, authorized with the member's bearer token
// like every other route. Right after the upgrade the server sends the
// current HEAD, then one message after every write that added events to
// the folder's log. A slow client may skip intermediate values; the last
// one always arrives. The server pings every ping interval and drops a
// client that does not answer within the next one; revoking the member or
// resetting its token closes its connections. Clients send nothing (a data message closes the
// connection).
func (a *api) ws(w http.ResponseWriter, r *http.Request) {
	// A device token gets the WebSocket with its device credentials for
	// the folder (a later step); its re-check below knows member tokens.
	if server.IsDeviceToken(bearerToken(r)) {
		writeErr(w, http.StatusUnauthorized, server.CodeUnauthorized, "the WebSocket takes a member token of the folder")
		return
	}
	sh, m, ok := a.authenticate(w, r)
	if !ok {
		return
	}
	token := bearerToken(r)
	c, err := websocket.Accept(w, r, nil)
	if err != nil {
		return // Accept has answered the request
	}
	defer c.CloseNow()
	c.SetReadLimit(1 << 10)

	// Subscribe before reading the HEAD, so a write in between is sent.
	sub := sh.Watch(m.ID)
	defer sub.Close()
	if err := sh.Touch(m.ID); err != nil {
		a.log.Warn("last_seen_at not updated", "share", sh.ID, "member", m.Username, "err", err)
	}

	// The reader answers pings and notices the close; connCtx ends with
	// the connection.
	connCtx := c.CloseRead(context.Background())
	go a.pinger(connCtx, c)

	send := func(head int64) bool {
		ctx, cancel := context.WithTimeout(connCtx, a.ping)
		defer cancel()
		return wsjson.Write(ctx, c, HeadMessage{Head: head}) == nil
	}
	head, err := sh.Head()
	if err != nil {
		a.log.Error("websocket: reading HEAD", "share", sh.ID, "err", err)
		c.Close(websocket.StatusInternalError, "internal error")
		return
	}
	if !send(head) {
		return
	}
	check := time.NewTicker(a.memberCheck)
	defer check.Stop()
	for {
		select {
		case h := <-sub.Heads:
			if h != head {
				head = h
				if !send(head) {
					return
				}
			}
		case <-sub.Revoked:
			c.Close(CloseRevoked, "token revoked")
			return
		case <-check.C:
			// The token itself is checked again: shfd member revoke and
			// reset-token in another process both end it.
			if _, err := sh.Authenticate(token); server.CodeOf(err) == server.CodeUnauthorized {
				c.Close(CloseRevoked, "token revoked")
				return
			}
		case <-a.ctx.Done():
			c.Close(CloseGoingAway, "server shutting down")
			return
		case <-connCtx.Done():
			return
		}
	}
}

// pinger pings every interval and drops the connection when a pong does
// not come back within the next one.
func (a *api) pinger(ctx context.Context, c *websocket.Conn) {
	t := time.NewTicker(a.ping)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			pctx, cancel := context.WithTimeout(ctx, a.ping)
			err := c.Ping(pctx)
			cancel()
			if err != nil {
				c.CloseNow()
				return
			}
		}
	}
}
