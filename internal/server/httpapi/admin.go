package httpapi

import (
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/digital-cube/shared-folder/internal/server"
)

// Admin routes (docs/admin.md). Over the public API they need the token of
// a member with the admin role; anyone else gets 403 forbidden.
//
//	GET  /v1/shares/{id}/admin/trash?path=&by=&reason=&since=
//	POST /v1/shares/{id}/admin/trash/{trash_id}/restore   {"to": "..."}
//	POST /v1/shares/{id}/admin/trash/purge                server.PurgeRequest
//	POST /v1/shares/{id}/admin/verify                     {"repair": bool}
//	GET  /v1/shares/{id}/admin/stats
//
// The same routes are served without a token on shfd's local admin socket
// (NewLocal), which is how the shfd commands reach a running server.

// HeaderAs names, on the local admin socket, the admin in whose name the
// operation runs; without it the folder's oldest active admin is used.
const HeaderAs = "X-Shfd-As"

// RestoreRequest is the body of POST /admin/trash/{trash_id}/restore.
type RestoreRequest struct {
	To string `json:"to,omitempty"` // empty: the original path
}

// VerifyRequest is the body of POST /admin/verify.
type VerifyRequest struct {
	Repair bool `json:"repair"`
}

func registerAdmin(mux *http.ServeMux, wrap func(handler) http.Handler) {
	mux.Handle("GET /v1/shares/{id}/admin/trash", wrap(trashList))
	mux.Handle("POST /v1/shares/{id}/admin/trash/purge", wrap(trashPurge))
	mux.Handle("POST /v1/shares/{id}/admin/trash/{trash}/restore", wrap(trashRestore))
	mux.Handle("POST /v1/shares/{id}/admin/verify", wrap(verify))
	mux.Handle("GET /v1/shares/{id}/admin/stats", wrap(stats))
}

// admin is auth plus the admin role.
func (a *api) admin(h handler) http.Handler {
	return a.auth(func(w http.ResponseWriter, r *http.Request, sh *server.Share, m *server.Member) (any, error) {
		if err := server.RequireAdmin(m); err != nil {
			return nil, err
		}
		return h(w, r, sh, m)
	})
}

func trashList(w http.ResponseWriter, r *http.Request, sh *server.Share, m *server.Member) (any, error) {
	q := r.URL.Query()
	f := server.TrashFilter{PathPrefix: q.Get("path"), By: q.Get("by"), Reason: q.Get("reason")}
	if v := q.Get("since"); v != "" {
		t, err := ParseSince(v)
		if err != nil {
			return nil, &server.Error{Code: server.CodeInvalidRequest, Message: err.Error()}
		}
		f.Since = t
	}
	return sh.ListTrash(m, f)
}

// ParseSince reads a point in time: RFC 3339, or a date (YYYY-MM-DD,
// midnight UTC).
func ParseSince(v string) (time.Time, error) {
	if t, err := time.Parse(time.RFC3339Nano, v); err == nil {
		return t, nil
	}
	if t, err := time.Parse("2006-01-02", v); err == nil {
		return t, nil
	}
	return time.Time{}, &server.Error{Code: server.CodeInvalidRequest, Message: "since " + strconv.Quote(v) + ": want YYYY-MM-DD or RFC 3339"}
}

func trashRestore(w http.ResponseWriter, r *http.Request, sh *server.Share, m *server.Member) (any, error) {
	id, err := strconv.ParseInt(r.PathValue("trash"), 10, 64)
	if err != nil || id <= 0 {
		return nil, &server.Error{Code: server.CodeUnknownTrash, Message: "no trash entry " + strconv.Quote(r.PathValue("trash"))}
	}
	var req RestoreRequest
	if err := decodeOptional(r, &req); err != nil {
		return nil, err
	}
	return sh.RestoreTrash(m, id, req.To)
}

func trashPurge(w http.ResponseWriter, r *http.Request, sh *server.Share, m *server.Member) (any, error) {
	var req server.PurgeRequest
	if err := decode(r, &req); err != nil {
		return nil, err
	}
	return sh.PurgeTrash(m, req)
}

func verify(w http.ResponseWriter, r *http.Request, sh *server.Share, m *server.Member) (any, error) {
	var req VerifyRequest
	if err := decodeOptional(r, &req); err != nil {
		return nil, err
	}
	return sh.Verify(m, req.Repair)
}

func stats(w http.ResponseWriter, r *http.Request, sh *server.Share, m *server.Member) (any, error) {
	return sh.Stats(m)
}

// decodeOptional is decode for a body that may be empty.
func decodeOptional(r *http.Request, v any) error {
	if r.ContentLength == 0 {
		return nil
	}
	return decode(r, v)
}

// NewLocal returns the admin routes for shfd's local admin socket. There
// is no token: whoever can open the socket (a file of mode 0600 inside the
// data-root) is the server's operator. The operation runs in the name of
// the admin named by the X-Shfd-As header, or of the folder's oldest
// active admin.
func NewLocal(srv *server.Server, log *slog.Logger) http.Handler {
	if log == nil {
		log = slog.Default()
	}
	a := &api{srv: srv, log: log}
	mux := http.NewServeMux()
	registerAdmin(mux, a.local)
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		writeErr(w, http.StatusNotFound, server.CodeNotFound, "no such endpoint")
	})
	return mux
}

func (a *api) local(h handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sh, err := a.srv.Share(r.PathValue("id"))
		if err != nil {
			a.fail(w, r, err)
			return
		}
		var m *server.Member
		if as := strings.TrimSpace(r.Header.Get(HeaderAs)); as != "" {
			m, err = sh.MemberByName(as)
		} else {
			m, err = sh.DefaultAdmin()
		}
		if err == nil {
			err = server.RequireAdmin(m)
		}
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
		writeJSON(w, http.StatusOK, out)
	})
}
