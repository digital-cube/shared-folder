package server

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/digital-cube/shared-folder/internal/password"
	"github.com/digital-cube/shared-folder/internal/pathx"
)

// Accounts (docs/admin.md, "Nalozi"): a user of registry.db signs in with
// a password and gets a device token; a member of a folder linked to the
// user (members.user_id) is reached with that token as with the member's
// own. Only the server's admin makes accounts (shfd user add).

// Kinds of a device.
const (
	DeviceClient = "client" // shf: does not expire
	DeviceGUI    = "gui"    // a browser session: GUIIdle without use, GUIMaxAge at most
)

// Lifetime of a GUI session.
const (
	GUIIdle   = 12 * time.Hour
	GUIMaxAge = 7 * 24 * time.Hour
)

// DeviceTokenPrefix starts every device token, so the API tells it from a
// member token ("shf_") without a lookup.
const DeviceTokenPrefix = "shd_"

// IsDeviceToken says whether token has the form of a device token.
func IsDeviceToken(token string) bool { return strings.HasPrefix(token, DeviceTokenPrefix) }

// newDeviceToken is "shd_" and 32 random bytes in base64url.
func newDeviceToken() string {
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err)
	}
	return DeviceTokenPrefix + base64.RawURLEncoding.EncodeToString(b[:])
}

// The attempt limit: after MaxFailures failed sign-ins of one username, or
// from one address, each further attempt waits 1 s, 2 s, 4 s, ... up to
// MaxLockout after the last failure. A success resets the count of that
// username or address; failures older than failureWindow are forgotten.
const (
	MaxFailures   = 5
	MaxLockout    = 15 * time.Minute
	failureWindow = 24 * time.Hour
)

// Outcomes of auth_log.
const (
	outcomeOK          = "ok"
	outcomeBadPassword = "bad_password"
	outcomeUnknownUser = "unknown_user"
	outcomeLocked      = "locked"
	outcomeDisabled    = "disabled"
)

// maxPasswordBytes bounds what a sign-in hashes: MaxLen characters of up
// to 4 bytes each.
const maxPasswordBytes = 4 * password.MaxLen

// touchEvery throttles the write of a device's last_seen_at (and the GUI
// session's sliding expiry) to once a minute.
const touchEvery = time.Minute

// logTime is the fixed-width form of auth_log.at, which must sort as text.
const logTime = "2006-01-02T15:04:05.000000000Z07:00"

// User is an account, without its password hash.
type User struct {
	ID          int64      `json:"id"`
	Username    string     `json:"username"`
	DisplayName string     `json:"display_name"`
	MustChange  bool       `json:"must_change"`
	CreatedAt   time.Time  `json:"created_at"`
	DisabledAt  *time.Time `json:"disabled_at,omitempty"`
	LastLoginAt *time.Time `json:"last_login_at,omitempty"`
}

// UserRef is what a search of accounts shows: nothing but the names.
type UserRef struct {
	Username    string `json:"username"`
	DisplayName string `json:"display_name"`
}

// Device is a signed-in device or GUI session of an account.
type Device struct {
	ID         int64      `json:"id"`
	Name       string     `json:"name"`
	Kind       string     `json:"kind"`
	CreatedAt  time.Time  `json:"created_at"`
	LastSeenAt *time.Time `json:"last_seen_at,omitempty"`
	ExpiresAt  *time.Time `json:"expires_at,omitempty"`
	RevokedAt  *time.Time `json:"revoked_at,omitempty"`
	// Current marks the device whose token made the request.
	Current bool `json:"current,omitempty"`
}

// Active says the device's token still works at now.
func (d *Device) Active(now time.Time) bool {
	return d.RevokedAt == nil && (d.ExpiresAt == nil || now.Before(*d.ExpiresAt))
}

// Session is a valid device token: the account and the device.
type Session struct {
	User     *User
	DeviceID int64
}

// UserFolder is a folder the account is an active member of.
type UserFolder struct {
	ShareID string `json:"share_id"`
	Name    string `json:"name"`
	Role    string `json:"role"`
	Member  string `json:"member"` // the member's username in that folder
	// LastChangeAt is the time of the folder's newest event; absent while
	// it has none.
	LastChangeAt *time.Time `json:"last_change_at,omitempty"`
}

const userCols = `id, username, display_name, must_change, created_at, disabled_at, last_login_at`

func scanUser(sc interface{ Scan(...any) error }, extra ...any) (*User, error) {
	var u User
	var created, disabled, login sql.NullString
	if err := sc.Scan(append([]any{&u.ID, &u.Username, &u.DisplayName, &u.MustChange, &created, &disabled, &login}, extra...)...); err != nil {
		return nil, err
	}
	if t := parseTime(created); t != nil {
		u.CreatedAt = *t
	}
	u.DisabledAt, u.LastLoginAt = parseTime(disabled), parseTime(login)
	return &u, nil
}

func (s *Server) pwParams() password.Params {
	if s.opts.PasswordParams != nil {
		return *s.opts.PasswordParams
	}
	return password.Default
}

// checkPassword verifies pw against a hash, at most two at a time over the
// whole server: each check takes 64 MiB, and sign-ins must not take the
// machine's memory.
func (s *Server) checkPassword(ctx context.Context, pw, phc string) (ok, rehash bool, err error) {
	select {
	case s.pwSem <- struct{}{}:
	case <-ctx.Done():
		return false, false, ctx.Err()
	}
	defer func() { <-s.pwSem }()
	return password.Verify(pw, phc, s.pwParams())
}

// hashPassword hashes pw with the server's parameters, under the same
// limit as checkPassword.
func (s *Server) hashPassword(ctx context.Context, pw string) (string, error) {
	select {
	case s.pwSem <- struct{}{}:
	case <-ctx.Done():
		return "", ctx.Err()
	}
	defer func() { <-s.pwSem }()
	return password.HashWith(pw, s.pwParams())
}

// dummyHash is checked for a sign-in of an unknown account, so that it
// takes as long as one of a known account.
func (s *Server) dummyHash() string {
	s.dummyOnce.Do(func() {
		h, err := password.HashWith(password.Generate(), s.pwParams())
		if err != nil {
			panic(err)
		}
		s.dummy = h
	})
	return s.dummy
}

// ---- accounts (shfd user ...) --------------------------------------------

// AddUser makes an account with a temporary password, which it returns:
// it is shown only this once, and the account must change it at its first
// sign-in (must_change).
func (s *Server) AddUser(username, displayName string) (*User, string, error) {
	if err := pathx.ValidateUsername(username); err != nil {
		return nil, "", errf(CodeInvalidRequest, "%v", err)
	}
	displayName = strings.TrimSpace(displayName)
	if utf8.RuneCountInString(displayName) > 128 {
		return nil, "", errf(CodeInvalidRequest, "the display name may have at most 128 characters")
	}
	temp := password.Generate()
	h, err := s.hashPassword(context.Background(), temp)
	if err != nil {
		return nil, "", err
	}
	_, err = s.reg.Exec(`INSERT INTO users (username, display_name, pw_hash, must_change, created_at) VALUES (?, ?, ?, 1, ?)`,
		username, displayName, h, fmtTime(s.opts.Now()))
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE") {
			return nil, "", errf(CodeUserExists, "account %q already exists", username)
		}
		return nil, "", err
	}
	u, err := s.UserByName(username)
	return u, temp, err
}

// UserByName returns an account, disabled or not.
func (s *Server) UserByName(username string) (*User, error) {
	u, err := scanUser(s.reg.QueryRow(`SELECT `+userCols+` FROM users WHERE username = ?`, username))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, errf(CodeUnknownUser, "no account %q", username)
	}
	return u, err
}

// UserByID returns an account, disabled or not.
func (s *Server) UserByID(id int64) (*User, error) {
	u, err := scanUser(s.reg.QueryRow(`SELECT `+userCols+` FROM users WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, errf(CodeUnknownUser, "no account %d", id)
	}
	return u, err
}

// Users lists every account by username.
func (s *Server) Users() ([]User, error) {
	rows, err := s.reg.Query(`SELECT ` + userCols + ` FROM users ORDER BY username`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []User
	for rows.Next() {
		u, err := scanUser(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *u)
	}
	return out, rows.Err()
}

// SearchUsers finds enabled accounts whose username starts with q or whose
// display name contains it, ignoring letter case; at most limit of them.
func (s *Server) SearchUsers(q string, limit int) ([]UserRef, error) {
	q = strings.ToLower(strings.TrimSpace(q))
	esc := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(q)
	rows, err := s.reg.Query(`SELECT username, display_name FROM users
		WHERE disabled_at IS NULL AND (username LIKE ? ESCAPE '\' OR lower(display_name) LIKE ? ESCAPE '\')
		ORDER BY username LIMIT ?`, esc+"%", "%"+esc+"%", limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []UserRef{}
	for rows.Next() {
		var r UserRef
		if err := rows.Scan(&r.Username, &r.DisplayName); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// ResetPassword gives an account a new temporary password and returns it
// (must_change again). Devices stay signed in, but until the password is
// changed their tokens get only password_change_required.
func (s *Server) ResetPassword(username string) (string, error) {
	u, err := s.UserByName(username)
	if err != nil {
		return "", err
	}
	temp := password.Generate()
	h, err := s.hashPassword(context.Background(), temp)
	if err != nil {
		return "", err
	}
	if _, err := s.reg.Exec(`UPDATE users SET pw_hash = ?, must_change = 1 WHERE id = ?`, h, u.ID); err != nil {
		return "", err
	}
	return temp, nil
}

// DisableUser disables an account and revokes all its devices and GUI
// sessions at once, and with them their folder credentials in every
// folder (member_devices, and their lines in authorized_keys); it returns
// how many devices were active. The account's members stay, and so do
// their legacy member tokens. A failure to rewrite authorized_keys is
// returned after the revocation is stored: rsync-gate --device refuses a
// disabled account's device anyway.
func (s *Server) DisableUser(username string) (int, error) {
	u, err := s.UserByName(username)
	if err != nil {
		return 0, err
	}
	now := fmtTime(s.opts.Now())
	tx, err := s.reg.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`UPDATE users SET disabled_at = coalesce(disabled_at, ?) WHERE id = ?`, now, u.ID); err != nil {
		return 0, err
	}
	res, err := tx.Exec(`UPDATE devices SET revoked_at = ? WHERE user_id = ? AND revoked_at IS NULL`, now, u.ID)
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	ids, err := s.userDeviceIDs(u.ID)
	if err != nil {
		return int(n), err
	}
	_, err = s.revokeDeviceCredentials(ids)
	return int(n), err
}

// userDeviceIDs lists every device of an account, revoked ones included:
// a credential of one may still be active after an earlier revocation
// that did not get through every folder.
func (s *Server) userDeviceIDs(userID int64) ([]int64, error) {
	rows, err := s.reg.Query(`SELECT id FROM devices WHERE user_id = ?`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// EnableUser enables a disabled account. Its devices stay revoked: they
// sign in again.
func (s *Server) EnableUser(username string) error {
	u, err := s.UserByName(username)
	if err != nil {
		return err
	}
	_, err = s.reg.Exec(`UPDATE users SET disabled_at = NULL WHERE id = ?`, u.ID)
	return err
}

// ---- sign-in -------------------------------------------------------------

// LoginRequest is a sign-in: the body of POST /v1/auth/login and the
// address it came from.
type LoginRequest struct {
	Username   string `json:"username"`
	Password   string `json:"password"`
	DeviceName string `json:"device_name"`
	Kind       string `json:"kind"` // client or gui
	IP         string `json:"-"`
}

// LoginResult is a successful sign-in: the new device token (shown only
// now; only its hash is stored) and the account.
type LoginResult struct {
	Token      string `json:"token"`
	User       *User  `json:"user"`
	MustChange bool   `json:"must_change"`
}

// Login checks a username and password and makes a device for them. A
// wrong password, an unknown and a disabled account all fail alike with
// CodeInvalidCredentials, after a password check of the same cost; a
// username or address over the attempt limit fails with
// CodeTooManyAttempts before any check. Every attempt goes to auth_log.
func (s *Server) Login(ctx context.Context, req LoginRequest) (*LoginResult, error) {
	if req.Kind == "" {
		req.Kind = DeviceClient
	}
	if req.Kind != DeviceClient && req.Kind != DeviceGUI {
		return nil, errf(CodeInvalidRequest, "kind must be %s or %s", DeviceClient, DeviceGUI)
	}
	name := strings.TrimSpace(req.DeviceName)
	if name == "" {
		name = req.Kind
	}
	if utf8.RuneCountInString(name) > 64 || strings.ContainsFunc(name, func(r rune) bool { return r < 0x20 || r == 0x7f }) {
		return nil, errf(CodeInvalidRequest, "device_name: at most 64 characters, no control characters")
	}
	username := strings.ToLower(strings.TrimSpace(req.Username))
	if len(username) > 64 {
		username = username[:64] // what auth_log keeps of a nonsense name
	}
	now := s.opts.Now()
	if wait, err := s.lockedFor(username, req.IP, now); err != nil {
		return nil, err
	} else if wait > 0 {
		if err := s.logAttempt(now, username, req.IP, outcomeLocked); err != nil {
			return nil, err
		}
		e := errf(CodeTooManyAttempts, "too many failed sign-ins; try again in %s", wait.Round(time.Second))
		e.RetryAfter = wait
		return nil, e
	}

	// Made before the lookup, so that the first sign-in of an unknown
	// account does not take longer by the making of the dummy hash.
	s.dummyHash()
	var u *User
	var phc string
	if pathx.ValidateUsername(username) == nil {
		found, err := scanUser(s.reg.QueryRow(`SELECT `+userCols+`, pw_hash FROM users WHERE username = ?`, username), &phc)
		switch {
		case err == nil:
			u = found
		case !errors.Is(err, sql.ErrNoRows):
			return nil, err
		}
	}
	return s.finishLogin(ctx, req, username, u, phc, name, now)
}

// finishLogin checks the password (against the dummy hash when u is nil),
// logs the outcome and, on success, makes the device.
func (s *Server) finishLogin(ctx context.Context, req LoginRequest, username string, u *User, phc, deviceName string, now time.Time) (*LoginResult, error) {
	if u == nil {
		phc = s.dummyHash()
	}
	ok, rehash := false, false
	if len(req.Password) <= maxPasswordBytes {
		var err error
		ok, rehash, err = s.checkPassword(ctx, req.Password, phc)
		if err != nil && !errors.Is(err, password.ErrMalformed) {
			return nil, err
		}
		if err != nil {
			ok, rehash = false, false
		}
	}
	outcome := outcomeOK
	switch {
	case u == nil:
		outcome = outcomeUnknownUser
	case !ok:
		outcome = outcomeBadPassword
	case u.DisabledAt != nil:
		outcome = outcomeDisabled
	}
	if err := s.logAttempt(now, username, req.IP, outcome); err != nil {
		return nil, err
	}
	if outcome != outcomeOK {
		return nil, errf(CodeInvalidCredentials, "wrong username or password")
	}
	if rehash {
		if h, err := s.hashPassword(ctx, req.Password); err == nil {
			// A failed rehash leaves the old hash, which still works.
			s.reg.Exec(`UPDATE users SET pw_hash = ? WHERE id = ?`, h, u.ID)
		}
	}
	token := newDeviceToken()
	var expires any
	if req.Kind == DeviceGUI {
		expires = fmtTime(guiExpiry(now, now))
	}
	tx, err := s.reg.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`INSERT INTO devices (user_id, name, kind, token_hash, created_at, last_seen_at, expires_at) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		u.ID, deviceName, req.Kind, HashToken(token), fmtTime(now), fmtTime(now), expires); err != nil {
		return nil, err
	}
	if _, err := tx.Exec(`UPDATE users SET last_login_at = ? WHERE id = ?`, fmtTime(now), u.ID); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	t := now.UTC()
	u.LastLoginAt = &t
	return &LoginResult{Token: token, User: u, MustChange: u.MustChange}, nil
}

// guiExpiry is when a GUI session made at created and last used at used
// ends: GUIIdle after the use, but not after GUIMaxAge from its start.
func guiExpiry(created, used time.Time) time.Time {
	e := used.Add(GUIIdle)
	if m := created.Add(GUIMaxAge); m.Before(e) {
		e = m
	}
	return e.UTC()
}

func (s *Server) logAttempt(now time.Time, username, ip, outcome string) error {
	_, err := s.reg.Exec(`INSERT INTO auth_log (at, username, ip, outcome) VALUES (?, ?, ?, ?)`,
		now.UTC().Format(logTime), username, ip, outcome)
	return err
}

// lockedFor is how long the next attempt of username from ip must still
// wait; 0 when it may go ahead. The username and the address are counted
// apart, and the longer wait wins.
func (s *Server) lockedFor(username, ip string, now time.Time) (time.Duration, error) {
	var wait time.Duration
	for _, k := range []struct{ col, val string }{{"username", username}, {"ip", ip}} {
		if k.val == "" {
			continue
		}
		w, err := s.lockedKey(k.col, k.val, now)
		if err != nil {
			return 0, err
		}
		wait = max(wait, w)
	}
	return wait, nil
}

func (s *Server) lockedKey(col, val string, now time.Time) (time.Duration, error) {
	rows, err := s.reg.Query(`SELECT at, outcome FROM auth_log WHERE `+col+` = ? AND at > ? ORDER BY at DESC LIMIT 200`,
		val, now.Add(-failureWindow).UTC().Format(logTime))
	if err != nil {
		return 0, err
	}
	defer rows.Close()
	n := 0
	var last time.Time
	for rows.Next() {
		var at, outcome string
		if err := rows.Scan(&at, &outcome); err != nil {
			return 0, err
		}
		if outcome == outcomeOK {
			break
		}
		if outcome == outcomeLocked {
			continue // a refused attempt checked nothing and does not lengthen the wait
		}
		if n == 0 {
			last, _ = time.Parse(time.RFC3339Nano, at)
		}
		n++
	}
	if err := rows.Err(); err != nil {
		return 0, err
	}
	if n < MaxFailures {
		return 0, nil
	}
	return max(last.Add(lockout(n)).Sub(now), 0), nil
}

// lockout is the wait after n failures (n >= MaxFailures): 1 s, doubling
// with each further failure, at most MaxLockout.
func lockout(n int) time.Duration {
	k := n - MaxFailures
	if k >= 20 {
		return MaxLockout
	}
	return min(time.Second<<k, MaxLockout)
}

// ---- device tokens -------------------------------------------------------

// AuthenticateDevice returns the session of a device token, or
// CodeUnauthorized when the token is unknown, revoked or expired, or its
// account is disabled. A use refreshes last_seen_at and slides a GUI
// session's expiry (at most once a minute). It does not look at
// must_change; the API does.
func (s *Server) AuthenticateDevice(token string) (*Session, error) {
	if !IsDeviceToken(token) {
		return nil, errf(CodeUnauthorized, "not a device token")
	}
	var devID int64
	var kind string
	var created, seen, expires, revoked sql.NullString
	u, err := scanUser(s.reg.QueryRow(`SELECT u.id, u.username, u.display_name, u.must_change, u.created_at, u.disabled_at, u.last_login_at,
		d.id, d.kind, d.created_at, d.last_seen_at, d.expires_at, d.revoked_at
		FROM devices d JOIN users u ON u.id = d.user_id WHERE d.token_hash = ?`, HashToken(token)),
		&devID, &kind, &created, &seen, &expires, &revoked)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, errf(CodeUnauthorized, "unknown token")
	}
	if err != nil {
		return nil, err
	}
	now := s.opts.Now()
	exp := parseTime(expires)
	switch {
	case revoked.Valid:
		return nil, errf(CodeUnauthorized, "device signed out")
	case exp != nil && !now.Before(*exp):
		return nil, errf(CodeUnauthorized, "session expired; sign in again")
	case u.DisabledAt != nil:
		return nil, errf(CodeUnauthorized, "account disabled")
	}
	if last := parseTime(seen); last == nil || now.Sub(*last) >= touchEvery {
		var newExp any
		if kind == DeviceGUI {
			start := now
			if c := parseTime(created); c != nil {
				start = *c
			}
			newExp = fmtTime(guiExpiry(start, now))
		}
		if _, err := s.reg.Exec(`UPDATE devices SET last_seen_at = ?, expires_at = coalesce(?, expires_at) WHERE id = ?`,
			fmtTime(now), newExp, devID); err != nil {
			return nil, err
		}
	}
	return &Session{User: u, DeviceID: devID}, nil
}

// Logout revokes the device of a token, and its folder credentials in
// every folder. A token already revoked or expired is not an error.
func (s *Server) Logout(token string) error {
	if !IsDeviceToken(token) {
		return errf(CodeUnauthorized, "not a device token")
	}
	var id int64
	err := s.reg.QueryRow(`UPDATE devices SET revoked_at = coalesce(revoked_at, ?) WHERE token_hash = ? RETURNING id`,
		fmtTime(s.opts.Now()), HashToken(token)).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return errf(CodeUnauthorized, "unknown token")
	}
	if err != nil {
		return err
	}
	_, err = s.revokeDeviceCredentials([]int64{id})
	return err
}

// RevokeDevice revokes one device of an account, and its folder
// credentials in every folder: their tokens stop working, their lines
// leave authorized_keys and their WebSockets (in this process) close.
func (s *Server) RevokeDevice(userID, deviceID int64) error {
	res, err := s.reg.Exec(`UPDATE devices SET revoked_at = coalesce(revoked_at, ?) WHERE id = ? AND user_id = ?`,
		fmtTime(s.opts.Now()), deviceID, userID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return errf(CodeUnknownDevice, "no device %d", deviceID)
	}
	_, err = s.revokeDeviceCredentials([]int64{deviceID})
	return err
}

// Devices lists the devices of an account that still work, newest first;
// all of them, revoked and expired included, with all.
func (s *Server) Devices(userID int64, all bool) ([]Device, error) {
	rows, err := s.reg.Query(`SELECT id, name, kind, created_at, last_seen_at, expires_at, revoked_at
		FROM devices WHERE user_id = ? ORDER BY id DESC`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	now := s.opts.Now()
	out := []Device{}
	for rows.Next() {
		var d Device
		var created, seen, expires, revoked sql.NullString
		if err := rows.Scan(&d.ID, &d.Name, &d.Kind, &created, &seen, &expires, &revoked); err != nil {
			return nil, err
		}
		if t := parseTime(created); t != nil {
			d.CreatedAt = *t
		}
		d.LastSeenAt, d.ExpiresAt, d.RevokedAt = parseTime(seen), parseTime(expires), parseTime(revoked)
		if all || d.Active(now) {
			out = append(out, d)
		}
	}
	return out, rows.Err()
}

// ChangePassword sets a new password for the account of a session after
// checking the old one, and clears must_change. A wrong old password
// fails with CodeWrongPassword and counts against the attempt limit like a
// failed sign-in.
func (s *Server) ChangePassword(ctx context.Context, sess *Session, oldPw, newPw, ip string) error {
	u := sess.User
	if err := password.Validate(u.Username, newPw); err != nil {
		return errf(CodeInvalidPassword, "%v", err)
	}
	if newPw == oldPw {
		return errf(CodeInvalidPassword, "the new password must differ from the old one")
	}
	now := s.opts.Now()
	if wait, err := s.lockedFor(u.Username, ip, now); err != nil {
		return err
	} else if wait > 0 {
		if err := s.logAttempt(now, u.Username, ip, outcomeLocked); err != nil {
			return err
		}
		e := errf(CodeTooManyAttempts, "too many failed attempts; try again in %s", wait.Round(time.Second))
		e.RetryAfter = wait
		return e
	}
	var phc string
	if err := s.reg.QueryRow(`SELECT pw_hash FROM users WHERE id = ?`, u.ID).Scan(&phc); err != nil {
		return err
	}
	ok := false
	if len(oldPw) <= maxPasswordBytes {
		var err error
		ok, _, err = s.checkPassword(ctx, oldPw, phc)
		if err != nil && !errors.Is(err, password.ErrMalformed) {
			return err
		}
	}
	if !ok {
		if err := s.logAttempt(now, u.Username, ip, outcomeBadPassword); err != nil {
			return err
		}
		return errf(CodeWrongPassword, "the old password is wrong")
	}
	h, err := s.hashPassword(ctx, newPw)
	if err != nil {
		return err
	}
	_, err = s.reg.Exec(`UPDATE users SET pw_hash = ?, must_change = 0 WHERE id = ?`, h, u.ID)
	if err == nil {
		u.MustChange = false
	}
	return err
}

// ---- accounts as members -------------------------------------------------

// MemberForUser returns the active member of sh linked to the account, or
// CodeUnauthorized when there is none (the account is not a member, or
// its member is revoked).
func (s *Server) MemberForUser(sh *Share, userID int64) (*Member, error) {
	m, err := scanMember(sh.db.QueryRow(`SELECT `+memberCols+` FROM members WHERE user_id = ?`, userID))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, errf(CodeUnauthorized, "token not valid for this folder")
	}
	if err != nil {
		return nil, err
	}
	if m.RevokedAt != nil {
		return nil, errf(CodeUnauthorized, "member revoked")
	}
	return m, nil
}

// UserFolders lists the folders the account is an active member of, by
// name. It reads user_shares and checks each folder's own members table,
// so a stale row of the index shows nothing.
func (s *Server) UserFolders(userID int64) ([]UserFolder, error) {
	rows, err := s.reg.Query(`SELECT us.share_id, s.name FROM user_shares us JOIN shares s ON s.id = us.share_id
		WHERE us.user_id = ? ORDER BY s.name`, userID)
	if err != nil {
		return nil, err
	}
	type row struct{ id, name string }
	var rs []row
	for rows.Next() {
		var r row
		if err := rows.Scan(&r.id, &r.name); err != nil {
			rows.Close()
			return nil, err
		}
		rs = append(rs, r)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	out := []UserFolder{}
	for _, r := range rs {
		sh, err := s.Share(r.id)
		if err != nil {
			return nil, err
		}
		m, err := s.MemberForUser(sh, userID)
		if CodeOf(err) == CodeUnauthorized {
			continue
		}
		if err != nil {
			return nil, err
		}
		last, err := sh.LastChangeAt()
		if err != nil {
			return nil, err
		}
		out = append(out, UserFolder{ShareID: r.id, Name: r.name, Role: m.Role, Member: m.Username, LastChangeAt: last})
	}
	return out, nil
}

// IsFolderAdmin says whether the account is an active admin of at least
// one folder.
func (s *Server) IsFolderAdmin(userID int64) (bool, error) {
	fs, err := s.UserFolders(userID)
	if err != nil {
		return false, err
	}
	for _, f := range fs {
		if f.Role == RoleAdmin {
			return true, nil
		}
	}
	return false, nil
}

// AddUserMember makes an account a member of sh, under the account's
// username, or brings back the account's revoked member (see
// AddOrReactivateMember). The member's own token is made and thrown away:
// the account reaches the folder with its device tokens. A disabled
// account is refused.
func (s *Server) AddUserMember(sh *Share, username string, admin bool) (m *Member, reactivated bool, err error) {
	u, err := s.UserByName(strings.ToLower(strings.TrimSpace(username)))
	if err != nil {
		return nil, false, err
	}
	if u.DisabledAt != nil {
		return nil, false, errf(CodeUserDisabled, "account %q is disabled", u.Username)
	}
	if _, reactivated, err = sh.addOrReactivate(u.Username, admin, u.ID); err != nil {
		return nil, false, err
	}
	m, err = s.MemberForUser(sh, u.ID)
	if err != nil {
		return nil, false, err
	}
	return m, reactivated, s.indexUserShare(u.ID, sh.ID, m.ID)
}

// LinkMember links an existing member of sh to an account; the member's
// token and SSH key keep working. Linking a member to the account it
// already has is not an error.
func (s *Server) LinkMember(sh *Share, member, username string) (*Member, error) {
	u, err := s.UserByName(strings.ToLower(strings.TrimSpace(username)))
	if err != nil {
		return nil, err
	}
	tx, err := sh.db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	var id int64
	var linked sql.NullInt64
	err = tx.QueryRow(`SELECT id, user_id FROM members WHERE username = ?`, member).Scan(&id, &linked)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, errf(CodeUnknownMember, "no member %q", member)
	}
	if err != nil {
		return nil, err
	}
	if linked.Valid && linked.Int64 != u.ID {
		return nil, errf(CodeMemberExists, "member %q is linked to another account", member)
	}
	var other string
	err = tx.QueryRow(`SELECT username FROM members WHERE user_id = ? AND id != ?`, u.ID, id).Scan(&other)
	if err == nil {
		return nil, errf(CodeMemberExists, "account %q is already member %q of the folder", u.Username, other)
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	if _, err := tx.Exec(`UPDATE members SET user_id = ? WHERE id = ?`, u.ID, id); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	if err := s.indexUserShare(u.ID, sh.ID, id); err != nil {
		return nil, err
	}
	return sh.MemberByID(id)
}

func (s *Server) indexUserShare(userID int64, shareID string, memberID int64) error {
	_, err := s.reg.Exec(`INSERT INTO user_shares (user_id, share_id, member_id) VALUES (?, ?, ?)
		ON CONFLICT (user_id, share_id) DO UPDATE SET member_id = excluded.member_id`, userID, shareID, memberID)
	if err != nil {
		return fmt.Errorf("the member is linked, but user_shares was not updated (shfd verify --repair fixes it): %w", err)
	}
	return nil
}

// AreaUserShares is the area of verify findings about user_shares.
const AreaUserShares = "user_shares"

// VerifyUserShares compares the user_shares rows of a folder with the
// links of its members (members.user_id), which are the truth: a link
// without a row is missing, a row without a link (or of an account that
// no longer exists) is untracked, a row naming another member is a
// mismatch. With repair the rows are brought in line.
func (s *Server) VerifyUserShares(sh *Share, repair bool) ([]VerifyIssue, error) {
	want := map[int64]Member{} // user_id -> member
	ms, err := sh.Members()
	if err != nil {
		return nil, err
	}
	for _, m := range ms {
		if m.UserID != nil {
			want[*m.UserID] = m
		}
	}
	names, err := s.UsernamesByID()
	if err != nil {
		return nil, err
	}
	have := map[int64]int64{} // user_id -> member_id
	rows, err := s.reg.Query(`SELECT user_id, member_id FROM user_shares WHERE share_id = ?`, sh.ID)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var u, m int64
		if err := rows.Scan(&u, &m); err != nil {
			rows.Close()
			return nil, err
		}
		have[u] = m
	}
	rows.Close()

	var issues []VerifyIssue
	add := func(is VerifyIssue, fix func() error) error {
		if repair && fix != nil {
			if err := fix(); err != nil {
				return err
			}
			is.Repaired = true
		}
		issues = append(issues, is)
		return nil
	}
	label := func(id int64) string {
		if n, ok := names[id]; ok {
			return n
		}
		return fmt.Sprintf("#%d", id)
	}
	for uid, m := range want {
		var err error
		switch got, ok := have[uid]; {
		case names[uid] == "":
			err = add(VerifyIssue{Kind: IssueMissing, Area: AreaUserShares, Path: m.Username,
				Detail: fmt.Sprintf("member is linked to account #%d, which does not exist; link it again with shfd member link", uid)}, nil)
		case !ok:
			err = add(VerifyIssue{Kind: IssueMissing, Area: AreaUserShares, Path: m.Username,
				Detail: "linked to account " + label(uid) + ", not in user_shares"},
				func() error { return s.indexUserShare(uid, sh.ID, m.ID) })
		case got != m.ID:
			err = add(VerifyIssue{Kind: IssueMismatch, Area: AreaUserShares, Path: m.Username,
				Detail: fmt.Sprintf("user_shares of account %s names member #%d", label(uid), got)},
				func() error { return s.indexUserShare(uid, sh.ID, m.ID) })
		}
		if err != nil {
			return nil, err
		}
	}
	var stale []int64
	for uid := range have {
		if _, ok := want[uid]; !ok {
			stale = append(stale, uid)
		}
	}
	sort.Slice(stale, func(i, j int) bool { return stale[i] < stale[j] })
	for _, uid := range stale {
		if err := add(VerifyIssue{Kind: IssueUntracked, Area: AreaUserShares, Path: label(uid),
			Detail: "in user_shares, but no member of the folder is linked to the account"},
			func() error {
				_, err := s.reg.Exec(`DELETE FROM user_shares WHERE user_id = ? AND share_id = ?`, uid, sh.ID)
				return err
			}); err != nil {
			return nil, err
		}
	}
	sort.SliceStable(issues, func(i, j int) bool { return issues[i].Path < issues[j].Path })
	return issues, nil
}

// UsernamesByID maps account ids to usernames, for listings.
func (s *Server) UsernamesByID() (map[int64]string, error) {
	rows, err := s.reg.Query(`SELECT id, username FROM users`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int64]string{}
	for rows.Next() {
		var id int64
		var n string
		if err := rows.Scan(&id, &n); err != nil {
			return nil, err
		}
		out[id] = n
	}
	return out, rows.Err()
}

// FillAccounts sets MemberInfo.Account from MemberInfo.UserID, and the
// name, kind, last use and state of every device in MemberInfo.Devices
// from the registry. A member with an account but neither its own key nor
// a device with a working key is pending.
func (s *Server) FillAccounts(ms []MemberInfo) error {
	names, err := s.UsernamesByID()
	if err != nil {
		return err
	}
	devs, err := s.deviceStates()
	if err != nil {
		return err
	}
	now := s.opts.Now()
	for i := range ms {
		mi := &ms[i]
		if mi.UserID != nil {
			mi.Account = names[*mi.UserID]
		}
		keyed := false
		for j := range mi.Devices {
			di := &mi.Devices[j]
			d, ok := devs[di.ID]
			if !ok {
				di.Revoked = true
				continue
			}
			di.Name, di.Kind, di.LastSeenAt = d.Name, d.Kind, d.LastSeenAt
			if d.why(&Member{UserID: mi.UserID}, now) != "" {
				di.Revoked = true
			}
			if di.Revoked {
				di.Connected = 0
			} else if di.SSHFingerprint != "" {
				keyed = true
			}
		}
		if keyed && mi.Status == StatusPending {
			mi.Status = StatusActive
		}
	}
	return nil
}
