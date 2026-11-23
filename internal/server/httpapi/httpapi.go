// Package httpapi is the control API of shfd over HTTP(S) and JSON
// (docs/specifikacija.md, section 4). Every route is authorized with
// "Authorization: Bearer <member token>" of the folder in the URL.
//
//	GET  /v1/shares/{id}/changes?since=N&limit=M
//	POST /v1/shares/{id}/uploads
//	POST /v1/shares/{id}/uploads/{upload_id}/commit
//	POST /v1/shares/{id}/ops
//	POST /v1/shares/{id}/sync-report
//	PUT  /v1/shares/{id}/ssh-key
//
// Errors are {"error": {"code": "...", "message": "..."}}; the code is one
// of the server.Code values.
package httpapi

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"strings"

	"github.com/digital-cube/shared-folder/internal/authkeys"
	"github.com/digital-cube/shared-folder/internal/server"
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

// New returns the API handler.
func New(srv *server.Server, log *slog.Logger) http.Handler {
	if log == nil {
		log = slog.Default()
	}
	a := &api{srv: srv, log: log}
	mux := http.NewServeMux()
	mux.Handle("GET /v1/shares/{id}/changes", a.auth(a.changes))
	mux.Handle("POST /v1/shares/{id}/uploads", a.auth(a.announce))
	mux.Handle("POST /v1/shares/{id}/uploads/{upload}/commit", a.auth(a.commit))
	mux.Handle("POST /v1/shares/{id}/ops", a.auth(a.ops))
	mux.Handle("POST /v1/shares/{id}/sync-report", a.auth(a.syncReport))
	mux.Handle("PUT /v1/shares/{id}/ssh-key", a.auth(a.setSSHKey))
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		writeErr(w, http.StatusNotFound, server.CodeNotFound, "no such endpoint")
	})
	return mux
}

type api struct {
	srv *server.Server
	log *slog.Logger
}

type handler func(w http.ResponseWriter, r *http.Request, sh *server.Share, m *server.Member) (any, error)

// auth resolves the folder and the member. An unknown folder answers like
// a bad token (401), so the API does not reveal which folders exist. A
// successful call (2xx) refreshes the member's last_seen_at.
func (a *api) auth(h handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		if !ok || strings.TrimSpace(token) == "" {
			writeErr(w, http.StatusUnauthorized, server.CodeUnauthorized, "missing bearer token")
			return
		}
		sh, err := a.srv.Share(r.PathValue("id"))
		if server.CodeOf(err) == server.CodeUnknownShare {
			writeErr(w, http.StatusUnauthorized, server.CodeUnauthorized, "token not valid for this folder")
			return
		}
		if err != nil {
			a.fail(w, r, err)
			return
		}
		m, err := sh.Authenticate(strings.TrimSpace(token))
		if err != nil {
			a.fail(w, r, err)
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
	case server.CodeNotFound, server.CodeUnknownUpload, server.CodeUnknownShare, server.CodeUnknownMember:
		return http.StatusNotFound
	case server.CodeStaleBase, server.CodeCaseConflict, server.CodeDestExists, server.CodeKindMismatch,
		server.CodeParentNotDir, server.CodeIsDir, server.CodeDirNotEmpty, server.CodeUploadMissing,
		server.CodeMemberExists, server.CodeKeyInUse:
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
