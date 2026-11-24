package httpapi

import (
	"net"
	"net/http"
	"net/netip"
	"strconv"
	"strings"

	"github.com/digital-cube/shared-folder/internal/password"
	"github.com/digital-cube/shared-folder/internal/server"
)

// Accounts (docs/admin.md, "Nalozi"):
//
//	POST   /v1/auth/login          {username, password, device_name, kind} -> server.LoginResult (no token; attempt limit)
//	POST   /v1/auth/logout         (device token) revokes that device
//	GET    /v1/me                  (device token) MeResponse
//	POST   /v1/me/password         (device token) {old, new}
//	DELETE /v1/me/devices/{id}     (device token) revokes one of the account's devices
//	GET    /v1/users?q=            (device token of a folder admin, or an admin's member token) [{username, display_name}]
//	POST   /v1/shares/{id}/devices (device token) server.FolderDevice: the device's folder token
//
// A device token ("shd_…") is also accepted on the routes of a folder in
// which its account is an active member (auth): it acts as that member.
// A client device asks for a folder token of its own instead
// (POST /v1/shares/{id}/devices) and registers its own SSH key with it
// (PUT ssh-key): rsync over SSH needs a key per device.
// While the account has a temporary password (must_change), the token is
// good only for /v1/me, /v1/me/password and /v1/auth/logout; everything
// else answers 403 password_change_required.

// DefaultTrustedProxies is the proxy whose X-Forwarded-For shfd believes
// when none is configured: nginx on the same machine.
const DefaultTrustedProxies = "127.0.0.1"

// ParseTrustedProxies reads a comma-separated list of addresses and CIDR
// ranges ("127.0.0.1,10.0.0.0/8").
func ParseTrustedProxies(s string) ([]netip.Prefix, error) {
	var out []netip.Prefix
	for _, f := range strings.Split(s, ",") {
		f = strings.TrimSpace(f)
		if f == "" {
			continue
		}
		if strings.Contains(f, "/") {
			p, err := netip.ParsePrefix(f)
			if err != nil {
				return nil, err
			}
			out = append(out, p.Masked())
			continue
		}
		a, err := netip.ParseAddr(f)
		if err != nil {
			return nil, err
		}
		a = a.Unmap()
		out = append(out, netip.PrefixFrom(a, a.BitLen()))
	}
	return out, nil
}

// WithTrustedProxies sets the proxies whose X-Forwarded-For is believed
// (default DefaultTrustedProxies).
func WithTrustedProxies(p []netip.Prefix) Option { return func(a *api) { a.trusted = p } }

func (a *api) isTrusted(ip netip.Addr) bool {
	ip = ip.Unmap()
	for _, p := range a.trusted {
		if p.Contains(ip) {
			return true
		}
	}
	return false
}

// clientIP is the address a request came from, which the attempt limit
// counts. X-Forwarded-For is read only when the connection comes from a
// trusted proxy; from its right end, trusted hops are skipped and the
// first other one is the client. A hop that is not an address ends the
// walk at the last trusted one.
func (a *api) clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	peer, err := netip.ParseAddr(host)
	if err != nil {
		return host
	}
	peer = peer.Unmap()
	if !a.isTrusted(peer) {
		return peer.String()
	}
	var hops []string
	for _, v := range r.Header.Values("X-Forwarded-For") {
		for _, h := range strings.Split(v, ",") {
			if h = strings.TrimSpace(h); h != "" {
				hops = append(hops, h)
			}
		}
	}
	ip := peer
	for i := len(hops) - 1; i >= 0; i-- {
		h, err := netip.ParseAddr(hops[i])
		if err != nil {
			break
		}
		ip = h.Unmap()
		if !a.isTrusted(ip) {
			break
		}
	}
	return ip.String()
}

func registerAccounts(mux *http.ServeMux, a *api) {
	mux.HandleFunc("POST /v1/auth/login", a.login)
	mux.Handle("POST /v1/auth/logout", a.session(true, a.logout))
	mux.Handle("GET /v1/me", a.session(true, a.me))
	mux.Handle("POST /v1/me/password", a.session(true, a.changePassword))
	mux.Handle("DELETE /v1/me/devices/{device}", a.session(true, a.revokeDevice))
	mux.HandleFunc("GET /v1/users", a.users)
	mux.Handle("POST /v1/shares/{id}/devices", a.session(false, a.folderDevice))
}

// folderDevice issues the folder token of the session's device: a new
// one on every call, which revokes the device's earlier token in the
// folder. An unknown folder answers 401 like a token not valid for it, a
// folder the account is not an active member of 403.
func (a *api) folderDevice(w http.ResponseWriter, r *http.Request, sess *server.Session) (any, error) {
	sh, err := a.srv.Share(r.PathValue("id"))
	if server.CodeOf(err) == server.CodeUnknownShare {
		return nil, &server.Error{Code: server.CodeUnauthorized, Message: "token not valid for this folder"}
	}
	if err != nil {
		return nil, err
	}
	return a.srv.IssueFolderDevice(sh, sess)
}

// login answers a sign-in. Over the attempt limit it is 429 with
// Retry-After; a wrong password, an unknown and a disabled account all get
// the same 401 invalid_credentials.
func (a *api) login(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxBody)
	var req server.LoginRequest
	if err := decode(r, &req); err != nil {
		a.fail(w, r, err)
		return
	}
	req.IP = a.clientIP(r)
	res, err := a.srv.Login(r.Context(), req)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, res)
}

type sessionHandler func(w http.ResponseWriter, r *http.Request, sess *server.Session) (any, error)

// session resolves the device token of a request. Unless allowMustChange,
// an account with a temporary password is refused with
// password_change_required.
func (a *api) session(allowMustChange bool, h sessionHandler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token := bearerToken(r)
		if token == "" {
			writeErr(w, http.StatusUnauthorized, server.CodeUnauthorized, "missing bearer token")
			return
		}
		sess, err := a.srv.AuthenticateDevice(token)
		if err != nil {
			a.fail(w, r, err)
			return
		}
		if !allowMustChange && sess.User.MustChange {
			a.fail(w, r, errMustChange)
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, maxBody)
		out, err := h(w, r, sess)
		if err != nil {
			a.fail(w, r, err)
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		writeJSON(w, http.StatusOK, out)
	})
}

var errMustChange = &server.Error{Code: server.CodePasswordChangeRequired, Message: "the account has a temporary password; change it first (POST /v1/me/password)"}

func (a *api) logout(w http.ResponseWriter, r *http.Request, sess *server.Session) (any, error) {
	if err := a.srv.Logout(bearerToken(r)); err != nil {
		return nil, err
	}
	return map[string]string{"status": "ok"}, nil
}

// MeResponse is the body of GET /v1/me: the account, the folders it is an
// active member of and its devices that still work.
type MeResponse struct {
	User    *server.User        `json:"user"`
	Folders []server.UserFolder `json:"folders"`
	Devices []server.Device     `json:"devices"`
	// PasswordRules are the rules of a new password, for a form to show.
	PasswordRules PasswordRules `json:"password_rules"`
}

// PasswordRules: a new password has MinLen to MaxLen characters and is
// not the username (password.Validate).
type PasswordRules struct {
	MinLen        int  `json:"min_len"`
	MaxLen        int  `json:"max_len"`
	NotUsername   bool `json:"not_username"`
	DifferFromOld bool `json:"differ_from_old"`
}

var passwordRules = PasswordRules{MinLen: password.MinLen, MaxLen: password.MaxLen, NotUsername: true, DifferFromOld: true}

func (a *api) me(w http.ResponseWriter, r *http.Request, sess *server.Session) (any, error) {
	folders, err := a.srv.UserFolders(sess.User.ID)
	if err != nil {
		return nil, err
	}
	devs, err := a.srv.Devices(sess.User.ID, false)
	if err != nil {
		return nil, err
	}
	for i := range devs {
		devs[i].Current = devs[i].ID == sess.DeviceID
	}
	return MeResponse{User: sess.User, Folders: folders, Devices: devs, PasswordRules: passwordRules}, nil
}

// PasswordRequest is the body of POST /v1/me/password.
type PasswordRequest struct {
	Old string `json:"old"`
	New string `json:"new"`
}

func (a *api) changePassword(w http.ResponseWriter, r *http.Request, sess *server.Session) (any, error) {
	var req PasswordRequest
	if err := decode(r, &req); err != nil {
		return nil, err
	}
	if err := a.srv.ChangePassword(r.Context(), sess, req.Old, req.New, a.clientIP(r)); err != nil {
		return nil, err
	}
	return map[string]any{"status": "ok", "user": sess.User}, nil
}

func (a *api) revokeDevice(w http.ResponseWriter, r *http.Request, sess *server.Session) (any, error) {
	id, err := strconv.ParseInt(r.PathValue("device"), 10, 64)
	if err != nil || id <= 0 {
		return nil, &server.Error{Code: server.CodeUnknownDevice, Message: "no device " + strconv.Quote(r.PathValue("device"))}
	}
	if err := a.srv.RevokeDevice(sess.User.ID, id); err != nil {
		return nil, err
	}
	return map[string]string{"status": "ok"}, nil
}

// maxUserSearch bounds the answer of GET /v1/users.
const maxUserSearch = 20

// users searches accounts by username (prefix) and display name, for
// adding one to a folder. Only an admin of at least one folder may: the
// device token of an account that is one, or the member token of an
// active admin. It shows nothing but the names.
func (a *api) users(w http.ResponseWriter, r *http.Request) {
	token := bearerToken(r)
	if token == "" {
		writeErr(w, http.StatusUnauthorized, server.CodeUnauthorized, "missing bearer token")
		return
	}
	admin := false
	if server.IsDeviceToken(token) {
		sess, err := a.srv.AuthenticateDevice(token)
		if err != nil {
			a.fail(w, r, err)
			return
		}
		if sess.User.MustChange {
			a.fail(w, r, errMustChange)
			return
		}
		if admin, err = a.srv.IsFolderAdmin(sess.User.ID); err != nil {
			a.fail(w, r, err)
			return
		}
	} else {
		_, m, _, err := a.srv.Whoami(token)
		if err != nil {
			a.fail(w, r, err)
			return
		}
		admin = m.Role == server.RoleAdmin
	}
	if !admin {
		writeErr(w, http.StatusForbidden, server.CodeForbidden, "only an admin of a folder may search accounts")
		return
	}
	out, err := a.srv.SearchUsers(r.URL.Query().Get("q"), maxUserSearch)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}
