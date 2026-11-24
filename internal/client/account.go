package client

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Accounts (docs/specifikacija.md, section 6): shf login signs this
// device in to a server with a username and password and keeps the
// device token in <state home>/accounts/<host>.json (0600). With it,
// shf join finds a folder by name and asks the server for credentials of
// this device's own (a folder token and an SSH key), and shf login moves
// folders joined with a member's token over to them.

// accountsDir is the directory of the accounts under the state home.
const accountsDir = "accounts"

// Account is this device signed in to a server.
type Account struct {
	URL        string    `json:"url"` // https://host[:port], as given to shf login
	Username   string    `json:"username"`
	DeviceID   int64     `json:"device_id"`
	DeviceName string    `json:"device_name"`
	Token      string    `json:"token"` // the device token ("shd_…")
	SignedIn   time.Time `json:"signed_in"`
}

// ErrNoAccount: this device is not signed in to the server.
var ErrNoAccount = errors.New("not signed in")

// ErrInvalidCredentials is a refused sign-in: a wrong username or
// password (or a disabled account, which the server does not tell apart).
var ErrInvalidCredentials = errors.New("wrong username or password")

// TooManyAttemptsError: the server pauses sign-ins of this username or
// address after several failures.
type TooManyAttemptsError struct {
	RetryAfter time.Duration // 0: the server did not say
}

func (e *TooManyAttemptsError) Error() string {
	if e.RetryAfter <= 0 {
		return "too many failed sign-ins; the server pauses further attempts for a while, try again later"
	}
	return "too many failed sign-ins; the server accepts the next attempt in " + roundWait(e.RetryAfter)
}

func roundWait(d time.Duration) string { return d.Round(time.Second).String() }

// ServerKey checks a server URL and returns it without a trailing slash
// and its host[:port], which names the account file.
func ServerKey(raw string) (base, host string, err error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
		return "", "", fmt.Errorf("invalid server URL %q: want https://host[:port]", raw)
	}
	if strings.ContainsAny(u.Host, `/\`) || strings.HasPrefix(u.Host, ".") {
		return "", "", fmt.Errorf("invalid server URL %q", raw)
	}
	u.Path, u.RawQuery, u.Fragment = strings.TrimRight(u.Path, "/"), "", ""
	return strings.TrimRight(u.String(), "/"), strings.ToLower(u.Host), nil
}

// sameServer reports whether two URLs name the same server.
func sameServer(a, b string) bool {
	_, ha, err1 := ServerKey(a)
	_, hb, err2 := ServerKey(b)
	return err1 == nil && err2 == nil && ha == hb
}

func accountPath(home, host string) string {
	return filepath.Join(home, accountsDir, host+".json")
}

// LoadAccount returns the account of the server at serverURL, or an
// error wrapping ErrNoAccount.
func LoadAccount(home, serverURL string) (*Account, error) {
	_, host, err := ServerKey(serverURL)
	if err != nil {
		return nil, err
	}
	b, err := os.ReadFile(accountPath(home, host))
	if errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("%w to %s", ErrNoAccount, host)
	}
	if err != nil {
		return nil, err
	}
	var a Account
	if err := json.Unmarshal(b, &a); err != nil {
		return nil, fmt.Errorf("%s: %w", accountPath(home, host), err)
	}
	if a.Token == "" {
		return nil, fmt.Errorf("%w to %s (%s has no token)", ErrNoAccount, host, accountPath(home, host))
	}
	return &a, nil
}

// Accounts lists the accounts this device is signed in with, by server.
func Accounts(home string) ([]*Account, error) {
	entries, err := os.ReadDir(filepath.Join(home, accountsDir))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []*Account
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		b, err := os.ReadFile(filepath.Join(home, accountsDir, e.Name()))
		if err != nil {
			return nil, err
		}
		var a Account
		if err := json.Unmarshal(b, &a); err != nil || a.Token == "" {
			continue
		}
		out = append(out, &a)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].URL < out[j].URL })
	return out, nil
}

// SaveAccount writes the account file (0600) of a.URL's server.
func SaveAccount(home string, a *Account) error {
	_, host, err := ServerKey(a.URL)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Join(home, accountsDir), 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(a, "", "  ")
	if err != nil {
		return err
	}
	return writePrivate(accountPath(home, host), append(b, '\n'))
}

// RemoveAccount deletes the account file of the server; a missing one is
// no error.
func RemoveAccount(home, serverURL string) error {
	_, host, err := ServerKey(serverURL)
	if err != nil {
		return err
	}
	if err := os.Remove(accountPath(home, host)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

// ---- the account API (internal/server/httpapi/accounts.go) ---------------

// Me is GET /v1/me: the account, its folders and its devices.
type Me struct {
	User struct {
		Username    string `json:"username"`
		DisplayName string `json:"display_name"`
		MustChange  bool   `json:"must_change"`
	} `json:"user"`
	Folders []AccountFolder `json:"folders"`
	Devices []AccountDevice `json:"devices"`
}

// AccountFolder is a folder the account is an active member of.
type AccountFolder struct {
	ShareID string `json:"share_id"`
	Name    string `json:"name"`
	Role    string `json:"role"`
	Member  string `json:"member"` // the member's username in that folder
}

// AccountDevice is a signed-in device of the account.
type AccountDevice struct {
	ID      int64  `json:"id"`
	Name    string `json:"name"`
	Kind    string `json:"kind"`
	Current bool   `json:"current"`
}

// folderDevice is POST /v1/shares/{id}/devices.
type folderDevice struct {
	Token    string `json:"token"`
	Member   string `json:"member"`
	Role     string `json:"role"`
	DeviceID int64  `json:"device_id"`
}

func httpClient(hc *http.Client) *http.Client {
	if hc == nil {
		return &http.Client{Timeout: 60 * time.Second}
	}
	return hc
}

func (a *Account) call(ctx context.Context, hc *http.Client, method, path string, in, out any) error {
	base, _, err := ServerKey(a.URL)
	if err != nil {
		return err
	}
	return doJSON(ctx, httpClient(hc), method, base+path, a.Token, in, out)
}

// SignInRequest are the arguments of SignIn.
type SignInRequest struct {
	URL        string
	Username   string
	Password   string
	DeviceName string
	HTTPClient *http.Client
}

// SignIn signs this device in as a client device. With mustChange the
// account has a temporary password: until ChangePassword succeeds the
// token is good for nothing else, and DeviceID is not known yet. The
// account is not saved (SaveAccount).
func SignIn(ctx context.Context, req SignInRequest) (a *Account, mustChange bool, err error) {
	base, _, err := ServerKey(req.URL)
	if err != nil {
		return nil, false, err
	}
	var res struct {
		Token      string `json:"token"`
		MustChange bool   `json:"must_change"`
		User       struct {
			Username string `json:"username"`
		} `json:"user"`
	}
	err = doJSON(ctx, httpClient(req.HTTPClient), http.MethodPost, base+"/v1/auth/login", "", map[string]string{
		"username": req.Username, "password": req.Password, "device_name": req.DeviceName, "kind": "client",
	}, &res)
	var ae *APIError
	switch {
	case errors.As(err, &ae) && ae.Code == "invalid_credentials":
		return nil, false, ErrInvalidCredentials
	case errors.As(err, &ae) && ae.Code == "too_many_attempts":
		return nil, false, &TooManyAttemptsError{RetryAfter: ae.RetryAfter}
	case err != nil:
		return nil, false, fmt.Errorf("signing in to %s: %w", base, err)
	case res.Token == "":
		return nil, false, fmt.Errorf("signing in to %s: the server sent no token", base)
	}
	a = &Account{URL: base, Username: res.User.Username, DeviceName: req.DeviceName, Token: res.Token, SignedIn: time.Now().UTC()}
	if a.Username == "" {
		a.Username = req.Username
	}
	if res.MustChange {
		return a, true, nil
	}
	return a, false, a.learnDevice(ctx, req.HTTPClient)
}

// learnDevice sets DeviceID (and the name the server keeps) from /v1/me.
func (a *Account) learnDevice(ctx context.Context, hc *http.Client) error {
	me, err := a.Me(ctx, hc)
	if err != nil {
		return err
	}
	for _, d := range me.Devices {
		if d.Current {
			a.DeviceID, a.DeviceName = d.ID, d.Name
		}
	}
	return nil
}

// ChangePassword replaces the account's password (the temporary one after
// an admin's reset). The server's refusal (invalid_password: the rules of
// a password; wrong_password) comes back as an *APIError.
func (a *Account) ChangePassword(ctx context.Context, hc *http.Client, old, new string) error {
	if err := a.call(ctx, hc, http.MethodPost, "/v1/me/password", map[string]string{"old": old, "new": new}, nil); err != nil {
		return err
	}
	if a.DeviceID == 0 {
		return a.learnDevice(ctx, hc)
	}
	return nil
}

// Me asks the server for the account, its folders and devices.
func (a *Account) Me(ctx context.Context, hc *http.Client) (*Me, error) {
	var me Me
	if err := a.call(ctx, hc, http.MethodGet, "/v1/me", nil, &me); err != nil {
		return nil, accountErr(a, err)
	}
	return &me, nil
}

// SignOut revokes the device on the server (POST /v1/auth/logout). A
// device the server already revoked is no error.
func (a *Account) SignOut(ctx context.Context, hc *http.Client) error {
	err := a.call(ctx, hc, http.MethodPost, "/v1/auth/logout", nil, nil)
	if c := codeOf(err); c == "unauthorized" || c == "device_revoked" {
		return nil
	}
	return err
}

// accountErr explains a refused device token.
func accountErr(a *Account, err error) error {
	if c := codeOf(err); c == "unauthorized" || c == "device_revoked" {
		return fmt.Errorf("%w: the server no longer accepts this device's sign-in (signed out or revoked); run shf login %s", ErrSignedOut, a.URL)
	}
	return err
}

// FindFolder returns the account's folder named by nameOrID: its share id,
// or its name (exactly, else ignoring case) when that names one folder.
func (me *Me) FindFolder(nameOrID string) (*AccountFolder, error) {
	for i := range me.Folders {
		if me.Folders[i].ShareID == nameOrID {
			return &me.Folders[i], nil
		}
	}
	for _, fold := range []func(string, string) bool{func(a, b string) bool { return a == b }, strings.EqualFold} {
		var hits []*AccountFolder
		for i := range me.Folders {
			if fold(me.Folders[i].Name, nameOrID) {
				hits = append(hits, &me.Folders[i])
			}
		}
		switch len(hits) {
		case 0:
			continue
		case 1:
			return hits[0], nil
		default:
			ids := make([]string, len(hits))
			for i, h := range hits {
				ids[i] = h.ShareID
			}
			return nil, fmt.Errorf("%d of your folders are called %q; name one by its id: %s", len(hits), nameOrID, strings.Join(ids, ", "))
		}
	}
	names := make([]string, len(me.Folders))
	for i, f := range me.Folders {
		names[i] = f.Name
	}
	if len(names) == 0 {
		return nil, fmt.Errorf("the account %s is not a member of any folder yet; ask a folder's admin to add it", me.User.Username)
	}
	return nil, fmt.Errorf("the account %s has no folder %q (its folders: %s; shf folders lists them)", me.User.Username, nameOrID, strings.Join(names, ", "))
}

// issueFolderDevice asks for the folder token of this device in a folder.
func (a *Account) issueFolderDevice(ctx context.Context, hc *http.Client, shareID string) (*folderDevice, error) {
	var fd folderDevice
	err := a.call(ctx, hc, http.MethodPost, "/v1/shares/"+url.PathEscape(shareID)+"/devices", nil, &fd)
	switch codeOf(err) {
	case "":
	case "forbidden":
		return nil, fmt.Errorf("the account %s is not a member of folder %s (any more)", a.Username, shareID)
	default:
		return nil, accountErr(a, err)
	}
	if fd.Token == "" {
		return nil, errors.New("the server sent no folder token")
	}
	return &fd, nil
}

// ---- signed out ------------------------------------------------------------

// ErrSignedOut is in the chain of a cycle's error when the folder's
// credentials belong to an account device that is signed out: shf logout
// here, or the device or account revoked on the server. It does not pass
// by itself; shf login gives the folder new credentials.
var ErrSignedOut = errors.New("signed out")

// SignedOutHint is what the user is told about a signed-out folder.
func SignedOutHint(cfg *Config) string {
	return "this device is signed out of " + cfg.URL + ": run shf login " + cfg.URL
}

// home is the state home a folder's state directory is in.
func (c *Config) home() string { return filepath.Dir(c.Dir) }

// SignedOutLocally reports whether the folder's credentials belong to an
// account this device is no longer signed in with (shf logout), or to an
// earlier sign-in than the current one.
func (c *Config) SignedOutLocally() bool {
	if c.Account == "" {
		return false
	}
	a, err := LoadAccount(c.home(), c.URL)
	if err != nil {
		return errors.Is(err, ErrNoAccount)
	}
	return a.DeviceID != 0 && c.DeviceID != 0 && a.DeviceID != c.DeviceID
}

// tokenID identifies a token without keeping it.
func tokenID(token string) string {
	h := sha256.Sum256([]byte(token))
	return hex.EncodeToString(h[:8])
}

// signedOutErr wraps a refusal of an account folder's token.
func (c *Client) signedOutErr(err error) error {
	if c.cfg.Account == "" || errors.Is(err, ErrSignedOut) {
		return err
	}
	if code := codeOf(err); code != "unauthorized" && code != "device_revoked" {
		return err
	}
	return fmt.Errorf("%w: %s (%w)", ErrSignedOut, SignedOutHint(c.cfg), err)
}
