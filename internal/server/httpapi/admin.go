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
//	GET  /v1/shares/{id}/admin/members
//	POST /v1/shares/{id}/admin/members                    AddMemberRequest (username, or user: an account)
//	POST /v1/shares/{id}/admin/members/{username}/revoke
//	POST /v1/shares/{id}/admin/members/{username}/role    SetRoleRequest
//	POST /v1/shares/{id}/admin/members/{username}/reset-token
//	DELETE /v1/shares/{id}/admin/members/{username}/devices/{device}   (revokes one device's credentials in the folder)
//	GET  /v1/shares/{id}/admin/log?after=&limit=
//	GET  /v1/shares/{id}/admin/files?dir=
//	GET  /v1/shares/{id}/admin/files/content?path=&download=  (file content, see content.go)
//
// The member routes change members with the same functions as shfd
// member add/revoke/set-role/reset-token. A token appears only in the
// answer to add and reset-token; it is never logged.
//
// The same routes are served without a token on shfd's local admin socket
// (NewLocal), which is how the shfd commands reach a running server. The
// socket also has the routes of shfd thumbs (thumbs.go), which the public
// API does not.

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

func registerAdmin(mux *http.ServeMux, wrap func(handler) http.Handler, raw func(rawHandler) http.Handler, a *api) {
	mux.Handle("GET /v1/shares/{id}/admin/trash", wrap(trashList))
	mux.Handle("POST /v1/shares/{id}/admin/trash/purge", wrap(trashPurge))
	mux.Handle("POST /v1/shares/{id}/admin/trash/{trash}/restore", wrap(trashRestore))
	mux.Handle("POST /v1/shares/{id}/admin/verify", wrap(a.verify))
	mux.Handle("GET /v1/shares/{id}/admin/stats", wrap(stats))
	mux.Handle("GET /v1/shares/{id}/admin/members", wrap(a.memberList))
	mux.Handle("POST /v1/shares/{id}/admin/members", wrap(a.memberAdd))
	mux.Handle("POST /v1/shares/{id}/admin/members/{username}/revoke", wrap(memberRevoke))
	mux.Handle("POST /v1/shares/{id}/admin/members/{username}/role", wrap(memberRole))
	mux.Handle("POST /v1/shares/{id}/admin/members/{username}/reset-token", wrap(memberResetToken))
	mux.Handle("DELETE /v1/shares/{id}/admin/members/{username}/devices/{device}", wrap(a.memberDeviceRevoke))
	mux.Handle("GET /v1/shares/{id}/admin/log", wrap(logRead))
	mux.Handle("GET /v1/shares/{id}/admin/files", wrap(filesList))
	mux.Handle("GET /v1/shares/{id}/admin/files/content", raw(a.fileContent))
}

// registerMemberFiles adds the read-only file routes for any active member
// of the folder (the same handlers as admin/files, admin/files/content and
// admin/thumbs): a member already has the whole folder through sync, so
// browsing and downloading it in the GUI gives nothing new. Changes go
// through POST /ops as from a device.
//
//	GET /v1/shares/{id}/files?dir=
//	GET /v1/shares/{id}/files/content?path=&download=
//	GET /v1/shares/{id}/thumbs/{hash}?size=
func registerMemberFiles(mux *http.ServeMux, a *api) {
	mux.Handle("GET /v1/shares/{id}/files", a.auth(filesList))
	mux.Handle("GET /v1/shares/{id}/files/content", a.memberRaw(a.fileContent))
	mux.HandleFunc("GET /v1/shares/{id}/thumbs/{hash}", a.memberThumb)
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

// verify checks the folder and, in registry.db, its rows of user_shares.
func (a *api) verify(w http.ResponseWriter, r *http.Request, sh *server.Share, m *server.Member) (any, error) {
	var req VerifyRequest
	if err := decodeOptional(r, &req); err != nil {
		return nil, err
	}
	res, err := sh.Verify(m, req.Repair)
	if err != nil {
		return nil, err
	}
	issues, err := a.srv.VerifyUserShares(sh, req.Repair)
	if err != nil {
		return nil, err
	}
	res.Issues = append(res.Issues, issues...)
	return res, nil
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
	registerAdmin(mux, a.local, a.localRaw, a)
	registerThumbsLocal(mux, a)
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		writeErr(w, http.StatusNotFound, server.CodeNotFound, "no such endpoint")
	})
	return mux
}

func (a *api) local(h handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sh, m, err := a.localMember(r)
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

// localMember resolves the folder of a request on the admin socket and
// the admin it runs as: the X-Shfd-As header, or the oldest active admin.
func (a *api) localMember(r *http.Request) (*server.Share, *server.Member, error) {
	sh, err := a.srv.Share(r.PathValue("id"))
	if err != nil {
		return nil, nil, err
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
		return nil, nil, err
	}
	return sh, m, nil
}

// AddMemberRequest is the body of POST /admin/members: a new member with
// its own token (username), or an account (user), which becomes a member
// under its username and reaches the folder with its device tokens.
type AddMemberRequest struct {
	Username string `json:"username,omitempty"`
	User     string `json:"user,omitempty"`
	Admin    bool   `json:"admin"`
}

// TokenResponse answers add and reset-token: the member as it is now and
// its new token, which is shown only this once (only its hash is stored).
// An account added as a member gets no token.
type TokenResponse struct {
	Member      *server.MemberInfo `json:"member"`
	Token       string             `json:"token,omitempty"`
	Reactivated bool               `json:"reactivated,omitempty"`
}

// SetRoleRequest is the body of POST /admin/members/{username}/role.
type SetRoleRequest struct {
	Role string `json:"role"`
}

// MemberResponse answers revoke and role: the member as it is now.
type MemberResponse struct {
	Member  *server.MemberInfo `json:"member"`
	OldRole string             `json:"old_role,omitempty"`
}

func (a *api) memberList(w http.ResponseWriter, r *http.Request, sh *server.Share, m *server.Member) (any, error) {
	l, err := sh.MemberInfos()
	if err != nil {
		return nil, err
	}
	return l, a.srv.FillAccounts(l.Members)
}

func (a *api) memberAdd(w http.ResponseWriter, r *http.Request, sh *server.Share, m *server.Member) (any, error) {
	var req AddMemberRequest
	if err := decode(r, &req); err != nil {
		return nil, err
	}
	if req.User != "" {
		if req.Username != "" {
			return nil, &server.Error{Code: server.CodeInvalidRequest, Message: "give username or user, not both"}
		}
		nm, reactivated, err := a.srv.AddUserMember(sh, req.User, req.Admin)
		if err != nil {
			return nil, err
		}
		mi, err := sh.MemberInfoByName(nm.Username)
		if err != nil {
			return nil, err
		}
		one := []server.MemberInfo{*mi}
		if err := a.srv.FillAccounts(one); err != nil {
			return nil, err
		}
		return TokenResponse{Member: &one[0], Reactivated: reactivated}, nil
	}
	token, reactivated, err := sh.AddOrReactivateMember(strings.TrimSpace(req.Username), req.Admin)
	if err != nil {
		return nil, err
	}
	mi, err := sh.MemberInfoByName(strings.TrimSpace(req.Username))
	if err != nil {
		return nil, err
	}
	return TokenResponse{Member: mi, Token: token, Reactivated: reactivated}, nil
}

func memberRevoke(w http.ResponseWriter, r *http.Request, sh *server.Share, m *server.Member) (any, error) {
	name := r.PathValue("username")
	if err := sh.RevokeMember(name); err != nil {
		return nil, err
	}
	mi, err := sh.MemberInfoByName(name)
	if err != nil {
		return nil, err
	}
	return MemberResponse{Member: mi}, nil
}

// memberDeviceRevoke revokes the folder credentials of one device of a
// member and answers with the member as it is now, devices included.
func (a *api) memberDeviceRevoke(w http.ResponseWriter, r *http.Request, sh *server.Share, m *server.Member) (any, error) {
	name := r.PathValue("username")
	id, err := strconv.ParseInt(r.PathValue("device"), 10, 64)
	if err != nil || id <= 0 {
		return nil, &server.Error{Code: server.CodeUnknownDevice, Message: "no device " + strconv.Quote(r.PathValue("device"))}
	}
	if err := sh.RevokeMemberDevice(name, id); err != nil {
		return nil, err
	}
	mi, err := sh.MemberInfoByName(name)
	if err != nil {
		return nil, err
	}
	one := []server.MemberInfo{*mi}
	if err := a.srv.FillAccounts(one); err != nil {
		return nil, err
	}
	return MemberResponse{Member: &one[0]}, nil
}

func memberRole(w http.ResponseWriter, r *http.Request, sh *server.Share, m *server.Member) (any, error) {
	name := r.PathValue("username")
	var req SetRoleRequest
	if err := decode(r, &req); err != nil {
		return nil, err
	}
	old, err := sh.SetRole(name, req.Role)
	if err != nil {
		return nil, err
	}
	mi, err := sh.MemberInfoByName(name)
	if err != nil {
		return nil, err
	}
	return MemberResponse{Member: mi, OldRole: old}, nil
}

func memberResetToken(w http.ResponseWriter, r *http.Request, sh *server.Share, m *server.Member) (any, error) {
	name := r.PathValue("username")
	token, err := sh.ResetToken(name)
	if err != nil {
		return nil, err
	}
	mi, err := sh.MemberInfoByName(name)
	if err != nil {
		return nil, err
	}
	return TokenResponse{Member: mi, Token: token}, nil
}

// logRead reads service.log: from the byte offset after= (as returned in
// next_offset), or, without it, its last limit lines.
func logRead(w http.ResponseWriter, r *http.Request, sh *server.Share, m *server.Member) (any, error) {
	q := r.URL.Query()
	after := int64(-1)
	if v := q.Get("after"); v != "" {
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil || n < 0 {
			return nil, &server.Error{Code: server.CodeInvalidRequest, Message: "after must be a non-negative byte offset"}
		}
		after = n
	}
	limit, err := intParam(q.Get("limit"), server.DefaultLogLimit)
	if err != nil || limit < 1 {
		return nil, &server.Error{Code: server.CodeInvalidRequest, Message: "limit must be a positive integer"}
	}
	return sh.ReadLog(after, int(min(limit, server.MaxLogLimit)))
}

func filesList(w http.ResponseWriter, r *http.Request, sh *server.Share, m *server.Member) (any, error) {
	return sh.ListDir(r.URL.Query().Get("dir"))
}
