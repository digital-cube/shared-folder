package server

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
	"net/mail"
	"net/url"
	"strings"
	"time"
	"unicode/utf8"

	shfmail "github.com/digital-cube/shared-folder/internal/mail"
	"github.com/digital-cube/shared-folder/internal/password"
)

// A forgotten password (docs/admin.md, "Zaboravljena lozinka"): an account
// with an email address asks for a link (POST /v1/auth/forgot), the link
// carries a one-time token, and the token sets a new password
// (POST /v1/auth/reset). Mails go through Options.Mail; without it, or
// without Options.ResetURL, nothing is sent and forgot answers as always.

// Limits of a forgotten password.
const (
	// ResetValidity is how long a reset token works.
	ResetValidity = 30 * time.Minute
	// MaxResetsPerAccount links are mailed to one account in resetWindow;
	// further requests answer as always but send nothing.
	MaxResetsPerAccount = 3
	// MaxResetsPerIP requests are let through from one address in
	// resetWindow; further ones get 429 too_many_attempts.
	MaxResetsPerIP = 10
	resetWindow    = time.Hour
	// maxEmailLen is the longest address SMTP carries.
	maxEmailLen = 254
	// maxPendingMails bounds the mails waiting to go out; more are dropped
	// (and logged).
	maxPendingMails = 64
	// mailTimeout bounds one mail, Brevo's answer included.
	mailTimeout = 30 * time.Second
)

// ResetFragment follows the GUI's URL in a reset link; the token comes
// after it.
const ResetFragment = "#reset?token="

var (
	errEmailTaken   = errf(CodeEmailTaken, "the email address belongs to another account")
	errInvalidToken = errf(CodeInvalidResetToken, "the link is not valid: it was used, it expired, or a newer one replaced it")
)

// ---- email addresses ------------------------------------------------------

// NormalizeEmail checks an address and returns it in lowercase; "" stays
// "" (no address). Only a bare address is taken ("ana@example.com", not
// "Ana <ana@example.com>").
func NormalizeEmail(s string) (string, error) {
	s = strings.ToLower(strings.TrimSpace(s))
	if s == "" {
		return "", nil
	}
	bad := errf(CodeInvalidEmail, "%q is not an email address", s)
	if len(s) > maxEmailLen || !utf8.ValidString(s) {
		return "", bad
	}
	a, err := mail.ParseAddress(s)
	if err != nil || a.Name != "" || a.Address != s {
		return "", bad
	}
	local, domain, ok := strings.Cut(s, "@")
	if !ok || local == "" || strings.Contains(domain, "@") || !strings.Contains(domain, ".") ||
		strings.HasPrefix(domain, ".") || strings.HasSuffix(domain, ".") {
		return "", bad
	}
	return s, nil
}

func nullIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// emailFree fails with CodeEmailTaken when another account than exceptID
// has the address.
func (s *Server) emailFree(email string, exceptID int64) error {
	var id int64
	err := s.reg.QueryRow(`SELECT id FROM users WHERE email = ? AND id != ?`, email, exceptID).Scan(&id)
	switch {
	case err == nil:
		return errEmailTaken
	case errors.Is(err, sql.ErrNoRows):
		return nil
	}
	return err
}

func (s *Server) setEmail(u *User, email string) error {
	email, err := NormalizeEmail(email)
	if err != nil {
		return err
	}
	if email != "" {
		if err := s.emailFree(email, u.ID); err != nil {
			return err
		}
	}
	if _, err := s.reg.Exec(`UPDATE users SET email = ? WHERE id = ?`, nullIfEmpty(email), u.ID); err != nil {
		if strings.Contains(err.Error(), "UNIQUE") {
			return errEmailTaken
		}
		return err
	}
	u.Email = email
	return nil
}

// SetEmail sets the email address of an account; "" removes it
// (shfd user set-email).
func (s *Server) SetEmail(username, email string) (*User, error) {
	u, err := s.UserByName(username)
	if err != nil {
		return nil, err
	}
	return u, s.setEmail(u, email)
}

// SetMyEmail sets the email address of a session's account after checking
// its password, which counts against the attempt limit like a sign-in
// (PUT /v1/me/email).
func (s *Server) SetMyEmail(ctx context.Context, sess *Session, email, currentPw, ip string) error {
	if _, err := NormalizeEmail(email); err != nil {
		return err
	}
	if err := s.checkCurrentPassword(ctx, sess.User, currentPw, ip, "the password is wrong"); err != nil {
		return err
	}
	return s.setEmail(sess.User, email)
}

// ---- configuration -------------------------------------------------------

// ResetPageURL is the GUI's address that a reset link starts with: the
// public URL of the server and the GUI's base path ("/gui", or "" at the
// root), "https://host/gui/". A link is never made from a request's Host.
func ResetPageURL(publicURL, guiBase string) (string, error) {
	u, err := url.Parse(strings.TrimSpace(publicURL))
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" || u.RawQuery != "" || u.Fragment != "" || u.User != nil {
		return "", fmt.Errorf("public URL %q: want https://host[/path]", publicURL)
	}
	base := strings.Trim(strings.TrimSpace(guiBase), "/")
	if strings.ContainsAny(base, "?#") {
		return "", fmt.Errorf("GUI base %q: want a path such as /gui", guiBase)
	}
	out := strings.TrimRight(u.String(), "/") + "/"
	if base != "" {
		out += base + "/"
	}
	return out, nil
}

// MailEnabled says forgotten-password mails go out: there is a sender and
// a page for the link.
func (s *Server) MailEnabled() bool { return s.opts.Mail != nil && s.opts.ResetURL != "" }

// site names the server in a mail: the host of the reset page.
func (s *Server) site() string {
	if u, err := url.Parse(s.opts.ResetURL); err == nil && u.Host != "" {
		return u.Host
	}
	return "shfd"
}

func (s *Server) log() *slog.Logger {
	if s.opts.Log != nil {
		return s.opts.Log
	}
	return slog.Default()
}

// ---- POST /v1/auth/forgot ------------------------------------------------

// ForgotRequest asks for a reset link: the body of POST /v1/auth/forgot
// and the address it came from.
type ForgotRequest struct {
	// Login is a username or an email address.
	Login string `json:"login"`
	// Lang is the language of the mail: "sr" (the default) or "en".
	Lang string `json:"lang"`
	IP   string `json:"-"`
}

// Forgot handles a forgotten password. It answers the same whether the
// account exists or not, and does the same work before it answers: one
// lookup, one count and one line of auth_log. The link itself is made and
// mailed after the answer, in the background, and only for an enabled
// account with an email address, at most MaxResetsPerAccount times in an
// hour (a further request is logged as locked). Over MaxResetsPerIP from
// one address it fails with CodeTooManyAttempts, which says nothing about
// the account either.
func (s *Server) Forgot(req ForgotRequest) error {
	login := strings.ToLower(strings.TrimSpace(req.Login))
	if login == "" {
		return errf(CodeInvalidRequest, "login is empty: a username or an email address")
	}
	if len(login) > maxEmailLen {
		login = strings.ToValidUTF8(login[:maxEmailLen], "") // what auth_log keeps of a nonsense name
	}
	now := s.opts.Now()
	since := now.Add(-resetWindow)
	if req.IP != "" {
		n, oldest, err := s.countLog("ip", req.IP, outcomeResetRequested, since)
		if err != nil {
			return err
		}
		if n >= MaxResetsPerIP {
			if err := s.logAttempt(now, login, req.IP, outcomeLocked); err != nil {
				return err
			}
			wait := max(oldest.Add(resetWindow).Sub(now), time.Second)
			e := errf(CodeTooManyAttempts, "too many requests from this address; try again in %s", wait.Round(time.Second))
			e.RetryAfter = wait
			return e
		}
	}
	// Usernames have no '@' and addresses always one, so this names at
	// most one account, with one query either way.
	u, err := scanUser(s.reg.QueryRow(`SELECT `+userCols+` FROM users WHERE username = ?1 OR email = ?1`, login))
	if errors.Is(err, sql.ErrNoRows) {
		u, err = nil, nil
	}
	if err != nil {
		return err
	}
	name := login
	if u != nil {
		name = u.Username
	}
	n, _, err := s.countLog("username", name, outcomeResetRequested, since)
	if err != nil {
		return err
	}
	outcome := outcomeResetRequested
	if n >= MaxResetsPerAccount {
		outcome = outcomeLocked
	}
	if err := s.logAttempt(now, name, req.IP, outcome); err != nil {
		return err
	}
	if outcome == outcomeResetRequested && u != nil && u.DisabledAt == nil && u.Email != "" && s.MailEnabled() {
		lang, ip := shfmail.NormLang(req.Lang), req.IP
		s.queueMail("reset link", u.Username, func(ctx context.Context) error {
			token, _, err := s.issueReset(u.ID, ip)
			if err != nil {
				return err
			}
			r := shfmail.Recipient{Username: u.Username, DisplayName: u.DisplayName, Email: u.Email}
			return s.opts.Mail.Send(ctx, shfmail.ResetMail(lang, r, s.opts.ResetURL+ResetFragment+token, ResetValidity, s.site()))
		})
	}
	return nil
}

// countLog counts the lines of auth_log with col = val and outcome after
// since, and gives the time of the oldest of them.
func (s *Server) countLog(col, val, outcome string, since time.Time) (int, time.Time, error) {
	var n int
	var oldest sql.NullString
	err := s.reg.QueryRow(`SELECT count(*), min(at) FROM auth_log WHERE `+col+` = ? AND outcome = ? AND at > ?`,
		val, outcome, since.UTC().Format(logTime)).Scan(&n, &oldest)
	if err != nil {
		return 0, time.Time{}, err
	}
	var t time.Time
	if oldest.Valid {
		t, _ = time.Parse(time.RFC3339Nano, oldest.String)
	}
	return n, t, nil
}

// newResetToken is 32 random bytes in base64url.
func newResetToken() string {
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err)
	}
	return base64.RawURLEncoding.EncodeToString(b[:])
}

// issueReset makes a reset token for an account, valid ResetValidity, and
// voids the account's earlier tokens that are still unused.
func (s *Server) issueReset(userID int64, ip string) (string, time.Time, error) {
	token := newResetToken()
	now := s.opts.Now()
	exp := now.Add(ResetValidity)
	tx, err := s.reg.Begin()
	if err != nil {
		return "", time.Time{}, err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`UPDATE password_resets SET used_at = ? WHERE user_id = ? AND used_at IS NULL`, fmtTime(now), userID); err != nil {
		return "", time.Time{}, err
	}
	if _, err := tx.Exec(`INSERT INTO password_resets (user_id, token_hash, created_at, expires_at, ip) VALUES (?, ?, ?, ?, ?)`,
		userID, HashToken(token), fmtTime(now), fmtTime(exp), ip); err != nil {
		return "", time.Time{}, err
	}
	return token, exp.UTC(), tx.Commit()
}

// IssueResetToken makes a reset token for an account without mailing it
// (shfd user reset-link, when mail does not work): the link is the GUI's
// URL, ResetFragment and the token. Earlier unused tokens stop working.
func (s *Server) IssueResetToken(username string) (token string, expires time.Time, err error) {
	u, err := s.UserByName(username)
	if err != nil {
		return "", time.Time{}, err
	}
	if u.DisabledAt != nil {
		return "", time.Time{}, errf(CodeUserDisabled, "account %q is disabled", u.Username)
	}
	return s.issueReset(u.ID, "cli")
}

// ---- POST /v1/auth/reset -------------------------------------------------

// ResetRequest sets a new password with a reset token: the body of
// POST /v1/auth/reset and the address it came from.
type ResetRequest struct {
	Token       string `json:"token"`
	NewPassword string `json:"new_password"`
	// Lang is the language of the "password changed" mail.
	Lang string `json:"lang"`
	IP   string `json:"-"`
}

// ResetResult is a password set with a reset token.
type ResetResult struct {
	Username string `json:"username"`
	// SignedOut is how many GUI sessions of the account were signed out.
	SignedOut int `json:"signed_out"`
}

// ResetWithToken sets a new password with a reset token: once, within
// ResetValidity, and only the newest token of the account. The password
// follows the rules of every password (password.Validate); must_change is
// cleared, every GUI session of the account is signed out (client devices
// stay) and the account gets a mail that its password changed. An
// unknown, used or expired token fails with CodeInvalidResetToken and
// counts against the attempt limit of the address like a wrong password;
// over the limit such a token gets CodeTooManyAttempts instead.
func (s *Server) ResetWithToken(ctx context.Context, req ResetRequest) (*ResetResult, error) {
	now := s.opts.Now()
	token := strings.TrimSpace(req.Token)
	var resetID, userID int64
	var expires string
	var used sql.NullString
	err := sql.ErrNoRows
	if token != "" && len(token) <= 128 {
		err = s.reg.QueryRow(`SELECT id, user_id, expires_at, used_at FROM password_resets WHERE token_hash = ?`, HashToken(token)).
			Scan(&resetID, &userID, &expires, &used)
	}
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	var u *User
	if err == nil {
		if u, err = s.UserByID(userID); err != nil {
			return nil, err
		}
	}
	exp := parseTime(sql.NullString{String: expires, Valid: true})
	if u == nil || used.Valid || exp == nil || !now.Before(*exp) || u.DisabledAt != nil {
		name := ""
		if u != nil {
			name = u.Username
		}
		// An address over the attempt limit learns nothing more about the
		// tokens it tries; a valid one works even then, so that a user who
		// locked the address out of signing in can still reset.
		if wait, err := s.lockedFor("", req.IP, now); err != nil {
			return nil, err
		} else if wait > 0 {
			if err := s.logAttempt(now, name, req.IP, outcomeLocked); err != nil {
				return nil, err
			}
			e := errf(CodeTooManyAttempts, "too many failed attempts; try again in %s", wait.Round(time.Second))
			e.RetryAfter = wait
			return nil, e
		}
		if err := s.logAttempt(now, name, req.IP, outcomeBadToken); err != nil {
			return nil, err
		}
		return nil, errInvalidToken
	}
	if err := password.Validate(u.Username, req.NewPassword); err != nil {
		return nil, errf(CodeInvalidPassword, "%v", err)
	}
	h, err := s.hashPassword(ctx, req.NewPassword)
	if err != nil {
		return nil, err
	}
	ids, err := s.applyReset(resetID, u.ID, h, now)
	if err != nil {
		return nil, err
	}
	if _, err := s.revokeDeviceCredentials(ids); err != nil {
		s.log().Warn("password reset: folder credentials of the signed-out GUI sessions not all revoked", "user", u.Username, "err", err)
	}
	if err := s.logAttempt(now, u.Username, req.IP, outcomeResetDone); err != nil {
		return nil, err
	}
	if u.Email != "" && s.opts.Mail != nil {
		lang, ip := shfmail.NormLang(req.Lang), req.IP
		s.queueMail("password changed notice", u.Username, func(ctx context.Context) error {
			r := shfmail.Recipient{Username: u.Username, DisplayName: u.DisplayName, Email: u.Email}
			return s.opts.Mail.Send(ctx, shfmail.PasswordChangedMail(lang, r, now, ip, s.site()))
		})
	}
	return &ResetResult{Username: u.Username, SignedOut: len(ids)}, nil
}

// applyReset uses the token, voids the account's other unused tokens, sets
// the password hash, clears must_change and signs out every GUI session
// of the account, all at once; it returns the sessions it signed out. A
// token used in the meantime fails with errInvalidToken.
func (s *Server) applyReset(resetID, userID int64, hash string, now time.Time) ([]int64, error) {
	at := fmtTime(now)
	tx, err := s.reg.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	res, err := tx.Exec(`UPDATE password_resets SET used_at = ? WHERE id = ? AND used_at IS NULL`, at, resetID)
	if err != nil {
		return nil, err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return nil, errInvalidToken
	}
	if _, err := tx.Exec(`UPDATE password_resets SET used_at = ? WHERE user_id = ? AND used_at IS NULL`, at, userID); err != nil {
		return nil, err
	}
	if _, err := tx.Exec(`UPDATE users SET pw_hash = ?, must_change = 0 WHERE id = ?`, hash, userID); err != nil {
		return nil, err
	}
	rows, err := tx.Query(`UPDATE devices SET revoked_at = ? WHERE user_id = ? AND kind = ? AND revoked_at IS NULL RETURNING id`, at, userID, DeviceGUI)
	if err != nil {
		return nil, err
	}
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		ids = append(ids, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return ids, tx.Commit()
}

// ---- sending -------------------------------------------------------------

// queueMail runs send in the background, at most two at a time, so that
// the request that caused it does not wait for (or show by its time) the
// mail. A failure is logged with what and the account, never with the
// link or a credential.
func (s *Server) queueMail(what, username string, send func(ctx context.Context) error) {
	if s.mailPending.Add(1) > maxPendingMails {
		s.mailPending.Add(-1)
		s.log().Warn("mail: too many mails waiting; dropped", "mail", what, "user", username)
		return
	}
	s.mailWG.Add(1)
	go func() {
		defer s.mailWG.Done()
		defer s.mailPending.Add(-1)
		s.mailSem <- struct{}{}
		defer func() { <-s.mailSem }()
		ctx, cancel := context.WithTimeout(context.Background(), mailTimeout)
		defer cancel()
		if err := send(ctx); err != nil {
			s.log().Warn("mail: not sent", "mail", what, "user", username, "err", err)
			return
		}
		s.log().Info("mail: sent", "mail", what, "user", username)
	}()
}

// WaitMail waits until every mail queued so far is sent or has failed.
func (s *Server) WaitMail() { s.mailWG.Wait() }
