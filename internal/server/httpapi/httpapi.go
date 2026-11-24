// Package httpapi is the control API of shfd over HTTP(S) and JSON
// (docs/specifikacija.md, section 4). Every route except the health check
// is authorized with "Authorization: Bearer <member token>" of the folder
// in the URL.
//
//	GET  /healthz                      (and GET /; no token, see health)
//	GET  /v1/shares/{id}/changes?since=N&limit=M
//	POST /v1/shares/{id}/uploads
//	POST /v1/shares/{id}/uploads/{upload_id}/commit
//	POST /v1/shares/{id}/ops
//	POST /v1/shares/{id}/sync-report
//	PUT  /v1/shares/{id}/ssh-key
//	GET  /v1/shares/{id}/ws            (WebSocket, see ws.go)
//	     /v1/shares/{id}/admin/...     (admin role only, see admin.go)
//
// Errors are {"error": {"code": "...", "message": "..."}}; the code is one
// of the server.Code values.
package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/digital-cube/shared-folder/internal/authkeys"
	"github.com/digital-cube/shared-folder/internal/server"
	"github.com/digital-cube/shared-folder/internal/version"
)

const (
	// DefaultLimit and MaxLimit bound the page size of /changes.
	DefaultLimit = 500
	MaxLimit     = 5000
	maxBody      = 1 << 20
)

// ChangesResponse is the body of GET /changes.
type ChangesResponse struct {
	Events []server.Event `json:"events"`
	// Head is the newest seq of the folder when the page was read.
	Head int64 `json:"head"`
	// More says there are events after the last one on this page (up to
	// Head); ask again with since = Next.
	More bool  `json:"more"`
	Next int64 `json:"next"`
}

// ErrorBody is the body of every error response.
type ErrorBody struct {
	Error struct {
		Code    server.Code `json:"code"`
		Message string      `json:"message"`
	} `json:"error"`
}

// Option tunes the handler.
type Option func(*api)

// WithPingInterval sets how often the WebSocket pings a client; a client
// that does not answer within the same interval is disconnected
// (default 30 s).
func WithPingInterval(d time.Duration) Option { return func(a *api) { a.ping = d } }

// WithMemberCheck sets how often an open WebSocket re-checks its member's
// token from the database, which closes it after a revocation or a token
// reset made by another process (shfd member revoke, reset-token). One in
// this process closes it at once. Default 10 s.
func WithMemberCheck(d time.Duration) Option { return func(a *api) { a.memberCheck = d } }

// Handler is the API. Close disconnects the open WebSockets, which
// http.Server.Shutdown does not track.
type Handler struct {
	http.Handler
	a *api
}

// Close ends every open WebSocket with "going away".
func (h *Handler) Close() { h.a.cancel() }

// New returns the API handler.
func New(srv *server.Server, log *slog.Logger, opts ...Option) *Handler {
	if log == nil {
		log = slog.Default()
	}
	a := &api{srv: srv, log: log, ping: 30 * time.Second, memberCheck: 10 * time.Second}
	for _, o := range opts {
		o(a)
	}
	a.ctx, a.cancel = context.WithCancel(context.Background())
	mux := http.NewServeMux()
	mux.Handle("GET /v1/shares/{id}/changes", a.auth(a.changes))
	mux.Handle("POST /v1/shares/{id}/uploads", a.auth(a.announce))
	mux.Handle("POST /v1/shares/{id}/uploads/{upload}/commit", a.auth(a.commit))
	mux.Handle("POST /v1/shares/{id}/ops", a.auth(a.ops))
	mux.Handle("POST /v1/shares/{id}/sync-report", a.auth(a.syncReport))
	mux.Handle("PUT /v1/shares/{id}/ssh-key", a.auth(a.setSSHKey))
	mux.HandleFunc("GET /v1/shares/{id}/ws", a.ws)
	registerAdmin(mux, a.admin)
	// Without a method in the pattern: the catch-all "/" below matches
	// every method, so the mux would answer 404 instead of 405.
	mux.HandleFunc("/healthz", a.health)
	mux.HandleFunc("/{$}", a.health)
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		writeErr(w, http.StatusNotFound, server.CodeNotFound, "no such endpoint")
	})
	return &Handler{Handler: mux, a: a}
}

type api struct {
	srv *server.Server
	log *slog.Logger

	ping, memberCheck time.Duration
	ctx               context.Context // canceled by Handler.Close
	cancel            context.CancelFunc
}

// HealthTimeout bounds the registry check of GET /healthz.
const HealthTimeout = 2 * time.Second

// Health is the body of GET /healthz (and GET /). It says on purpose
// nothing beyond the version: no folders, members, data-root or SSH
// endpoint, and no detail of a failure.
type Health struct {
	Service string `json:"service"` // always "shfd"
	Version string `json:"version"` // internal/version.Version
	OK      bool   `json:"ok"`
	Error   string `json:"error,omitempty"`
}

// health answers without a token: 200 when registry.db responds within
// HealthTimeout, 503 otherwise. Only GET and HEAD are allowed.
func (a *api) health(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		writeErr(w, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed")
		return
	}
	body := Health{Service: "shfd", Version: version.Version, OK: true}
	ctx, cancel := context.WithTimeout(r.Context(), HealthTimeout)
	defer cancel()
	if err := a.srv.Ping(ctx); err != nil {
		a.log.Warn("health check: registry unavailable", "err", err)
		body.OK, body.Error = false, "registry unavailable"
		writeJSON(w, http.StatusServiceUnavailable, body)
		return
	}
	writeJSON(w, http.StatusOK, body)
}

type handler func(w http.ResponseWriter, r *http.Request, sh *server.Share, m *server.Member) (any, error)

// auth resolves the folder and the member. An unknown folder answers like
// a bad token (401), so the API does not reveal which folders exist. A
// successful call (2xx) refreshes the member's last_seen_at.
func (a *api) auth(h handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sh, m, ok := a.authenticate(w, r)
		if !ok {
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, maxBody)
		out, err := h(w, r, sh, m)
		if err != nil {
			a.fail(w, r, err)
			return
		}
		if err := sh.Touch(m.ID); err != nil {
			a.log.Warn("last_seen_at not updated", "share", sh.ID, "member", m.Username, "err", err)
		}
		writeJSON(w, http.StatusOK, out)
	})
}

// bearerToken is the token of the Authorization header, "" when there is
// none.
func bearerToken(r *http.Request) string {
	token, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	if !ok {
		return ""
	}
	return strings.TrimSpace(token)
}

// authenticate resolves the folder and member of a request, answering it
// with an error when that fails.
func (a *api) authenticate(w http.ResponseWriter, r *http.Request) (*server.Share, *server.Member, bool) {
	token := bearerToken(r)
	if token == "" {
		writeErr(w, http.StatusUnauthorized, server.CodeUnauthorized, "missing bearer token")
		return nil, nil, false
	}
	sh, err := a.srv.Share(r.PathValue("id"))
	if server.CodeOf(err) == server.CodeUnknownShare {
		writeErr(w, http.StatusUnauthorized, server.CodeUnauthorized, "token not valid for this folder")
		return nil, nil, false
	}
	if err != nil {
		a.fail(w, r, err)
		return nil, nil, false
	}
	m, err := sh.Authenticate(token)
	if err != nil {
		a.fail(w, r, err)
		return nil, nil, false
	}
	return sh, m, true
}

func (a *api) changes(w http.ResponseWriter, r *http.Request, sh *server.Share, m *server.Member) (any, error) {
	q := r.URL.Query()
	since, err := intParam(q.Get("since"), 0)
	if err != nil || since < 0 {
		return nil, &server.Error{Code: server.CodeInvalidRequest, Message: "since must be a non-negative integer"}
	}
	limit, err := intParam(q.Get("limit"), DefaultLimit)
	if err != nil || limit < 1 {
		return nil, &server.Error{Code: server.CodeInvalidRequest, Message: "limit must be a positive integer"}
	}
	limit = min(limit, MaxLimit)
	evs, head, more, err := sh.Changes(since, int(limit))
	if err != nil {
		return nil, err
	}
	next := since
	if len(evs) > 0 {
		next = evs[len(evs)-1].Seq
	}
	return ChangesResponse{Events: evs, Head: head, More: more, Next: next}, nil
}

func (a *api) announce(w http.ResponseWriter, r *http.Request, sh *server.Share, m *server.Member) (any, error) {
	var req server.AnnounceRequest
	if err := decode(r, &req); err != nil {
		return nil, err
	}
	return sh.Announce(m, req)
}

func (a *api) commit(w http.ResponseWriter, r *http.Request, sh *server.Share, m *server.Member) (any, error) {
	return sh.Commit(m, r.PathValue("upload"))
}

func (a *api) ops(w http.ResponseWriter, r *http.Request, sh *server.Share, m *server.Member) (any, error) {
	var req server.OpRequest
	if err := decode(r, &req); err != nil {
		return nil, err
	}
	return sh.Op(m, req)
}

func (a *api) syncReport(w http.ResponseWriter, r *http.Request, sh *server.Share, m *server.Member) (any, error) {
	var req server.SyncReport
	if err := decode(r, &req); err != nil {
		return nil, err
	}
	if err := sh.ReportSync(m, req); err != nil {
		return nil, err
	}
	return map[string]string{"status": "ok"}, nil
}

// SSHKeyRequest is the body of PUT /ssh-key: the member's OpenSSH public
// key, as in id_ed25519.pub.
type SSHKeyRequest struct {
	PublicKey string `json:"public_key"`
}

// SSHKeyResponse confirms the key that rsync over SSH now accepts for the
// member; it replaces any earlier one.
type SSHKeyResponse struct {
	PublicKey   string `json:"public_key"` // "type base64", without the comment
	Fingerprint string `json:"fingerprint"`
	// SSH is where rsync over SSH connects (shfd serve --ssh-endpoint);
	// empty fields are left to the client's defaults.
	SSH server.SSHEndpoint `json:"ssh"`
}

func (a *api) setSSHKey(w http.ResponseWriter, r *http.Request, sh *server.Share, m *server.Member) (any, error) {
	var req SSHKeyRequest
	if err := decode(r, &req); err != nil {
		return nil, err
	}
	key, err := sh.SetSSHKey(m, req.PublicKey)
	if err != nil {
		return nil, err
	}
	return SSHKeyResponse{PublicKey: key, Fingerprint: authkeys.Fingerprint(key), SSH: a.srv.SSH()}, nil
}

// status maps an error code to its HTTP status.
func status(c server.Code) int {
	switch c {
	case server.CodeUnauthorized:
		return http.StatusUnauthorized
	case server.CodeForbidden:
		return http.StatusForbidden
	case server.CodeNotFound, server.CodeUnknownUpload, server.CodeUnknownShare, server.CodeUnknownMember, server.CodeUnknownTrash:
		return http.StatusNotFound
	case server.CodeStaleBase, server.CodeCaseConflict, server.CodeDestExists, server.CodeKindMismatch,
		server.CodeParentNotDir, server.CodeIsDir, server.CodeDirNotEmpty, server.CodeUploadMissing,
		server.CodeMemberExists, server.CodeKeyInUse, server.CodeTrashDamaged:
		return http.StatusConflict
	case server.CodeSSHNotConfigured:
		return http.StatusNotImplemented
	case server.CodeHashMismatch, server.CodeSizeMismatch:
		return http.StatusUnprocessableEntity
	default:
		return http.StatusBadRequest
	}
}

func (a *api) fail(w http.ResponseWriter, r *http.Request, err error) {
	var se *server.Error
	if errors.As(err, &se) {
		writeErr(w, status(se.Code), se.Code, se.Message)
		return
	}
	var mbe *http.MaxBytesError
	if errors.As(err, &mbe) {
		writeErr(w, http.StatusRequestEntityTooLarge, server.CodeInvalidRequest, "request body too large")
		return
	}
	a.log.Error("request failed", "method", r.Method, "path", r.URL.Path, "err", err)
	writeErr(w, http.StatusInternalServerError, "internal", "internal error")
}

func decode(r *http.Request, v any) error {
	if err := json.NewDecoder(r.Body).Decode(v); err != nil {
		var mbe *http.MaxBytesError
		if errors.As(err, &mbe) {
			return err
		}
		return &server.Error{Code: server.CodeInvalidRequest, Message: "invalid JSON body: " + err.Error()}
	}
	return nil
}

func intParam(s string, def int64) (int64, error) {
	if s == "" {
		return def, nil
	}
	return strconv.ParseInt(s, 10, 64)
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, code int, c server.Code, msg string) {
	var b ErrorBody
	b.Error.Code, b.Error.Message = c, msg
	writeJSON(w, code, b)
}
