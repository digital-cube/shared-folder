package client

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/digital-cube/shared-folder/internal/ignore"
	"github.com/digital-cube/shared-folder/internal/transport/rsyncssh"
)

var shareIDRE = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

// DefaultSSHUser is the server's system user when neither the server nor
// the command line names one (docs/server-ssh.md).
const DefaultSSHUser = "shfd"

// JoinRequest are the arguments of shf join.
type JoinRequest struct {
	Home string // the state home (StateHome)
	URL  string // the server's API, https://host[:port]
	// ShareID is the folder's id; with Account (and no Token) it may also
	// be the folder's name among the account's folders.
	ShareID string
	// Token is the member's own token (the old way). Empty with Account.
	Token string
	// Account, when set and Token is empty, is this device's sign-in to
	// the server (shf login): the folder is found among the account's
	// and the device gets credentials of its own in it
	// (POST /v1/shares/{id}/devices) and a new SSH key.
	Account *Account
	Folder  string
	// SSH overrides the endpoint for rsync over SSH. Unset fields come from
	// the server (shfd serve --ssh-endpoint), then from defaults: the host
	// of URL, port 22, user "shfd".
	SSH SSH
	// Warn, when set, is told what the user should know but does not stop
	// the join: KeyReplacedWarning when registering this device's key
	// replaced another device's key of the same member.
	Warn func(msg string)
}

// KeyReplacedWarning is the warning shf join prints when the member was
// already joined from another device, whose key (fingerprint) this join
// replaced: the server keeps one key per member.
func KeyReplacedWarning(fingerprint string) string {
	return "this member was already joined from another device (key " + fingerprint + "); " +
		"that device can no longer sync. Every device or person needs its own member and its own token " +
		"(shfd member add on the server)"
}

// Join connects a local folder to a shared folder: it checks the token
// with the server, creates the member's SSH key and registers it,
// remembers the server's host key, writes the local state and runs the
// first cycle. A folder that already has files is merged, never cleaned:
// files only here are sent, files only on the server are fetched, equal
// ones are recorded and different ones become conflict copies.
//
// When the first cycle fails the folder stays joined; Join returns the
// open client together with the error, and shf sync continues.
func Join(ctx context.Context, req JoinRequest, opts Options) (*Client, *Result, error) {
	u, err := url.Parse(req.URL)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
		return nil, nil, fmt.Errorf("invalid server URL %q: want https://host[:port]", req.URL)
	}
	byAccount := strings.TrimSpace(req.Token) == "" && req.Account != nil
	var acctFolder *AccountFolder
	switch {
	case byAccount:
		if !sameServer(req.Account.URL, req.URL) {
			return nil, nil, fmt.Errorf("the account is of %s, not of %s", req.Account.URL, req.URL)
		}
		me, err := req.Account.Me(ctx, opts.HTTPClient)
		if err != nil {
			return nil, nil, fmt.Errorf("asking %s for your folders: %w", u.Redacted(), err)
		}
		if acctFolder, err = me.FindFolder(req.ShareID); err != nil {
			return nil, nil, err
		}
		req.ShareID = acctFolder.ShareID
	case strings.TrimSpace(req.Token) == "":
		return nil, nil, errors.New("empty token")
	}
	if !shareIDRE.MatchString(req.ShareID) {
		return nil, nil, fmt.Errorf("invalid share id %q: want a UUID as printed by shfd share create", req.ShareID)
	}
	if err := os.MkdirAll(req.Folder, 0o755); err != nil {
		return nil, nil, err
	}
	folder, err := absClean(req.Folder)
	if err != nil {
		return nil, nil, err
	}
	if fi, err := os.Stat(folder); err != nil || !fi.IsDir() {
		return nil, nil, fmt.Errorf("%s is not a directory", folder)
	}
	joined, err := Joined(req.Home)
	if err != nil {
		return nil, nil, err
	}
	for _, c := range joined {
		switch {
		case c.ShareID == req.ShareID && c.Folder == folder:
			// Joining again: the token is checked anew and this device's
			// key registered again (another device of the same member
			// replaced it); the folder's state is kept.
		case c.ShareID == req.ShareID:
			return nil, nil, fmt.Errorf("share %s is already joined at %s", req.ShareID, c.Folder)
		case within(folder, c.Folder) || within(c.Folder, folder):
			return nil, nil, fmt.Errorf("%s overlaps %s, which is joined to share %s; one folder on disk holds one shared folder", folder, c.Folder, c.ShareID)
		}
	}
	if within(req.Home, folder) {
		return nil, nil, fmt.Errorf("the client's state directory %s must not be inside the shared folder", req.Home)
	}

	var fd *folderDevice
	if byAccount {
		if fd, err = req.Account.issueFolderDevice(ctx, opts.HTTPClient, req.ShareID); err != nil {
			return nil, nil, err
		}
		req.Token = fd.Token
	}
	a := newAPI(u.String(), req.ShareID, req.Token, opts.HTTPClient)
	if _, err := a.head(ctx); err != nil {
		if codeOf(err) == "unauthorized" {
			return nil, nil, errors.New("the server refused the token for this folder (wrong, revoked, or another folder's)")
		}
		return nil, nil, fmt.Errorf("contacting %s: %w", u.Redacted(), err)
	}

	cfg := &Config{URL: strings.TrimRight(u.String(), "/"), ShareID: req.ShareID, Folder: folder, Token: req.Token,
		Dir: filepath.Join(req.Home, req.ShareID)}
	if fd != nil {
		cfg.setDevice(req.Account, fd)
	}
	if err := os.MkdirAll(cfg.Dir, 0o700); err != nil {
		return nil, nil, err
	}
	if opts.Transport == nil {
		// a device of an account gets a key of its own, never one a
		// member's token registered before (the server would refuse it
		// as another line's)
		if cfg.SSH, err = setupSSH(ctx, a, cfg, u, req.SSH, req.Warn, fd != nil); err != nil {
			return nil, nil, err
		}
	}
	cfg.Joined = time.Now().UTC()
	if err := cfg.save(); err != nil {
		return nil, nil, err
	}
	c, err := Open(cfg, opts)
	if err != nil {
		return nil, nil, err
	}
	// the key was just registered (and, over SSH, accepted)
	if err := clearKeyRefused(c.st.db); err != nil {
		c.Close()
		return nil, nil, err
	}
	res, err := c.Sync(ctx)
	return c, res, err
}

// setupSSH creates the member's key (or reuses the one an interrupted
// join left), registers it, settles the endpoint and connects once, which
// remembers the server's host key and proves the key is accepted.
//
// With fresh a new key is made beside the folder's key and takes its
// place only once the server accepted it: the device credentials of an
// account (shf join by account, shf login moving a folder over).
func setupSSH(ctx context.Context, a *api, cfg *Config, u *url.URL, over SSH, warn func(string), fresh bool) (*SSH, error) {
	keyFile := cfg.KeyFile()
	var pub string
	if fresh {
		keyFile += ".new"
		os.Remove(keyFile)
		os.Remove(keyFile + ".pub")
	}
	if b, err := os.ReadFile(keyFile + ".pub"); err == nil && !fresh {
		pub = strings.TrimSpace(string(b))
	} else {
		host, _ := os.Hostname()
		comment := "shf@" + host + " " + cfg.ShareID
		if cfg.Account != "" {
			comment = "shf@" + host + " " + cfg.Account + "/" + cfg.Device + " " + cfg.ShareID
		}
		if pub, err = rsyncssh.GenerateKey(ctx, keyFile, comment); err != nil {
			return nil, fmt.Errorf("creating the SSH key: %w", err)
		}
	}
	os.Chmod(keyFile, 0o600)
	res, err := a.setSSHKey(ctx, pub)
	if err != nil {
		return nil, fmt.Errorf("registering the SSH key: %w", err)
	}
	if res.Replaced != nil && warn != nil {
		warn(KeyReplacedWarning(res.Replaced.Fingerprint))
	}
	ep := &SSH{User: over.User, Host: over.Host, Port: over.Port}
	if ep.User == "" {
		ep.User = res.SSH.User
	}
	if ep.Host == "" {
		ep.Host = res.SSH.Host
	}
	if ep.Port == 0 {
		ep.Port = res.SSH.Port
	}
	if ep.User == "" {
		ep.User = DefaultSSHUser
	}
	if ep.Host == "" {
		ep.Host = u.Hostname()
	}
	if ep.Port == 0 {
		ep.Port = 22
	}
	tr, err := rsyncssh.New(rsyncssh.Config{ShareID: cfg.ShareID, Host: ep.Host, Port: ep.Port, User: ep.User,
		KeyFile: keyFile, StateDir: cfg.Dir})
	if err != nil {
		return nil, err
	}
	err = tr.Probe(ctx)
	if fresh {
		tr.Close() // its control socket is named after the temporary key file
	}
	if err != nil {
		return nil, fmt.Errorf("SSH to %s@%s: %w", ep.User, net.JoinHostPort(ep.Host, fmt.Sprint(ep.Port)), err)
	}
	if fresh {
		// the old key goes: the public half first, so that a crash in
		// between leaves no pair that does not belong together
		if err := os.Rename(keyFile+".pub", cfg.KeyFile()+".pub"); err != nil {
			return nil, err
		}
		if err := os.Rename(keyFile, cfg.KeyFile()); err != nil {
			return nil, err
		}
	}
	return ep, nil
}

// setDevice records that the folder's credentials are those of the
// account's device.
func (c *Config) setDevice(a *Account, fd *folderDevice) {
	c.Token, c.Account, c.DeviceID, c.Device, c.Member = fd.Token, a.Username, a.DeviceID, a.DeviceName, fd.Member
	if fd.DeviceID != 0 {
		c.DeviceID = fd.DeviceID
	}
}

// Status is what shf status shows.
type Status struct {
	Config    *Config
	Last      *Cycle // nil before the first cycle
	Seq       int64  // last applied seq
	RemoteSeq int64
	Head      int64 // the server's HEAD (as last seen when HeadErr is set)
	HeadErr   error
	// Pending counts the steps a cycle would take now; -1 when unknown
	// (PendingErr says why).
	Pending    int
	PendingErr error
	Problems   []Problem
	Busy       bool // a cycle is running
	// Held are the deletions the mass-delete guard holds back; Blocked
	// says some of them wait for shf confirm or shf restore-local.
	Held    []HeldDelete
	Blocked bool
	// KeyRefusedSince is set while the server refuses this device's SSH
	// key (the last cycles failed with it; KeyRefusedHint says what to do).
	KeyRefusedSince time.Time
	// SignedOut: the folder's credentials belong to an account device
	// that is signed out (SignedOutHint says what to do). SignedOutSince
	// is when the server started refusing them; zero after shf logout
	// here, which the server was not asked about.
	SignedOut      bool
	SignedOutSince time.Time
}

// Status reports the folder's state without changing anything: it asks
// the server for new changes, scans the folder and plans, but applies and
// stores nothing.
func (c *Client) Status(ctx context.Context) (*Status, error) {
	s := &Status{Config: c.cfg, Pending: -1}
	var err error
	if s.Seq, err = c.st.Seq(); err != nil {
		return nil, err
	}
	if s.RemoteSeq, err = c.st.metaInt(metaRemoteSeq); err != nil {
		return nil, err
	}
	if s.Head, err = c.st.metaInt(metaHead); err != nil {
		return nil, err
	}
	cs, err := c.st.Cycles(1)
	if err != nil {
		return nil, err
	}
	if len(cs) > 0 {
		s.Last = &cs[0]
	}
	if s.Problems, err = c.st.Problems(); err != nil {
		return nil, err
	}
	if s.Held, err = c.st.HeldDeletes(); err != nil {
		return nil, err
	}
	if s.KeyRefusedSince, err = c.st.KeyRefusedSince(); err != nil {
		return nil, err
	}
	if s.SignedOutSince, err = c.st.SignedOutSince(c.cfg.Token); err != nil {
		return nil, err
	}
	s.SignedOut = !s.SignedOutSince.IsZero() || c.cfg.SignedOutLocally()
	for _, h := range s.Held {
		s.Blocked = s.Blocked || !h.Confirmed
	}
	if unlock, err := lock(c.cfg.Dir); errors.Is(err, ErrBusy) {
		s.Busy = true
	} else if err == nil {
		unlock()
	}

	remote, err := c.st.Remote()
	if err != nil {
		return nil, err
	}
	if c.cfg.SignedOutLocally() {
		s.HeadErr = fmt.Errorf("%w: %s", ErrSignedOut, SignedOutHint(c.cfg))
		s.PendingErr = s.HeadErr
		return s, nil
	}
	evs, head, err := c.api.changes(ctx, s.RemoteSeq)
	if err != nil {
		if err = c.signedOutErr(err); errors.Is(err, ErrSignedOut) {
			s.SignedOut = true
		}
		s.HeadErr = err
		s.PendingErr = fmt.Errorf("server not reachable: %w", err)
		return s, nil
	}
	s.Head = head
	applyEvents(remote, evs)
	n, err := c.pending(remote, s.Held)
	if err != nil {
		s.PendingErr = err
		return s, nil
	}
	s.Pending = n
	return s, nil
}

func (c *Client) pending(remote map[string]Entry, held []HeldDelete) (int, error) {
	isHeld := map[string]bool{}
	for _, h := range held {
		isHeld[h.Path] = true
	}
	manifest, err := c.st.Manifest()
	if err != nil {
		return 0, err
	}
	m, err := ignore.FromDir(c.cfg.Folder)
	if err != nil {
		return 0, err
	}
	sc, err := scan(c.cfg.Folder, m, manifest)
	if err != nil {
		return 0, err
	}
	acts := Plan(PlanInput{Manifest: manifest, Local: sc.Local, Remote: remote,
		Ignored: func(p string, isDir bool) bool { return sc.untracked(p) || m.Match(p, isDir) }})
	n := 0
	for _, a := range acts {
		if !a.IsBookkeeping() && !(isDeletion(a) && isHeld[a.Path]) {
			n++
		}
	}
	return n, nil
}
