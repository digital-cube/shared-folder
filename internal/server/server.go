// Package server is the core of shfd: the registry of folders under a
// data-root, and for each folder its storage, members, change log, trash and
// the rules that decide what every operation does
// (docs/specifikacija.md, sections 3, 4 and 6).
//
// Layout on disk:
//
//	<data-root>/
//	  <share-id>/
//	    storage/             current state, plain files
//	    trash/               deleted and replaced versions (flat)
//	    tmp/                 incoming uploads; same file system as storage/
//	    thumbnails/          made by shfd thumbs, keyed by content hash (internal/thumbs)
//	    service.log          human-readable event log
//	<db-root>/               (Options.DBRoot; the data-root by default)
//	  registry.db            folders: id (UUID), name, created_at; accounts
//	  avatars/<user-id>.jpg  profile pictures of accounts (avatars.go)
//	  <share-id>/
//	    service.db           SQLite (with -wal and -shm next to it)
//
// The databases may live apart from the data so that they stay on a local
// disk while storage/ is on NFS: SQLite's WAL and POSIX locks are not safe
// on a network file system (docs/server-ssh.md).
package server

import (
	"context"
	"crypto/rand"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/digital-cube/shared-folder/internal/authkeys"
	"github.com/digital-cube/shared-folder/internal/mail"
	"github.com/digital-cube/shared-folder/internal/password"
	"github.com/digital-cube/shared-folder/internal/pathx"
)

// Options tune a Server. The zero value is what production uses.
type Options struct {
	// Now returns the current time; tests pin it to get predictable
	// trash and conflict names.
	Now func() time.Time
	// SkipRecovery opens folders without finishing interrupted disk steps
	// or catching up service.log. The CLI's member commands use it: they
	// may run next to a live shfd serve, which owns that work.
	SkipRecovery bool
	// AuthorizedKeys, when set, is the authorized_keys file of the system
	// user sshd runs rsync-gate as. Registering a member's SSH key writes
	// its line there and revoking the member removes it. When nil, SSH
	// keys cannot be registered.
	AuthorizedKeys *authkeys.File
	// SSH is where clients reach sshd for rsync over SSH. It is handed to
	// a member when it registers its key, so "shf join" needs only the
	// API URL. Empty fields leave the client to its defaults.
	SSH SSHEndpoint
	// DBRoot is where registry.db and <share-id>/service.db live. Empty
	// means the data-root, the layout shfd always had.
	DBRoot string
	// PasswordParams are the Argon2id parameters of account passwords;
	// nil is password.Default. Tests make them cheap.
	PasswordParams *password.Params
	// Mail sends the mails of a forgotten password (reset.go); nil sends
	// none.
	Mail mail.Sender
	// ResetURL is the GUI's address that a reset link starts with
	// (ResetPageURL: "https://host/gui/"); "" turns the mails of a
	// forgotten password off, as a nil Mail does.
	ResetURL string
	// Log gets what happens in the background (a mail that failed);
	// slog.Default when nil.
	Log *slog.Logger
}

// SSHEndpoint is the address and system user of the server's sshd.
type SSHEndpoint struct {
	User string `json:"user,omitempty"`
	Host string `json:"host,omitempty"`
	Port int    `json:"port,omitempty"`
}

// ParseSSHEndpoint reads "user@host[:port]" ("[v6addr]:port" for IPv6);
// user and port are optional.
func ParseSSHEndpoint(s string) (SSHEndpoint, error) {
	var e SSHEndpoint
	if s == "" {
		return e, nil
	}
	if u, rest, ok := strings.Cut(s, "@"); ok {
		e.User, s = u, rest
	}
	host, port := s, ""
	if strings.HasPrefix(s, "[") {
		i := strings.Index(s, "]")
		if i < 0 {
			return e, fmt.Errorf("ssh endpoint %q: missing ]", s)
		}
		host, port = s[1:i], strings.TrimPrefix(s[i+1:], ":")
	} else if h, p, ok := strings.Cut(s, ":"); ok {
		host, port = h, p
	}
	if host == "" || strings.ContainsAny(host+e.User, " \t/@") {
		return e, fmt.Errorf("ssh endpoint %q: want user@host[:port]", s)
	}
	e.Host = host
	if port != "" {
		n, err := strconv.Atoi(port)
		if err != nil || n < 1 || n > 65535 {
			return e, fmt.Errorf("ssh endpoint: invalid port %q", port)
		}
		e.Port = n
	}
	return e, nil
}

// Server owns a data-root.
type Server struct {
	root   string
	dbRoot string
	opts   Options
	reg    *sql.DB

	mu     sync.Mutex
	shares map[string]*Share

	thumbSvc thumbService // when shfd thumbs last asked for jobs (thumbs.go)

	pwSem     chan struct{} // at most two password checks at a time (accounts.go)
	dummyOnce sync.Once
	dummy     string // the hash an unknown account's sign-in is checked against

	mailWG      sync.WaitGroup // mails being sent (reset.go)
	mailPending atomic.Int32
	mailSem     chan struct{} // at most two at a time

	avatarMu sync.Mutex // one change of a profile picture at a time (avatars.go)
}

// ShareInfo is one row of registry.db.
type ShareInfo struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	CreatedAt time.Time `json:"created_at"`
}

// Open opens the data-root, creating it and registry.db when missing.
func Open(root string, opts Options) (*Server, error) {
	if opts.Now == nil {
		opts.Now = time.Now
	}
	abs, dbAbs, err := Roots(root, opts.DBRoot)
	if err != nil {
		return nil, err
	}
	if err := checkDBRoot(abs, dbAbs); err != nil {
		return nil, err
	}
	for _, d := range []string{abs, dbAbs} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			return nil, err
		}
	}
	reg, err := openDB(filepath.Join(dbAbs, RegistryFile), "registry")
	if err != nil {
		return nil, err
	}
	return &Server{root: abs, dbRoot: dbAbs, opts: opts, reg: reg, shares: map[string]*Share{}, pwSem: make(chan struct{}, 2), mailSem: make(chan struct{}, 2)}, nil
}

// Roots returns the absolute data-root and db-root; an empty dbRoot is
// the data-root.
func Roots(root, dbRoot string) (string, string, error) {
	if root == "" {
		return "", "", errors.New("data-root is not set")
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		return "", "", err
	}
	if dbRoot == "" {
		return abs, abs, nil
	}
	dbAbs, err := filepath.Abs(dbRoot)
	if err != nil {
		return "", "", err
	}
	return abs, dbAbs, nil
}

// RegistryFile is the name of the registry database in the db-root.
const RegistryFile = "registry.db"

// MovedMarker is the file, in the data-root, that shfd db move leaves
// behind: its first line is the db-root the databases were copied to.
// Opening the data-root with any other db-root is refused, so a server
// cannot come up on the stale copies the move left in place.
const MovedMarker = "db-moved"

// MovedTo reads the MovedMarker of a data-root; "" when there is none.
func MovedTo(absRoot string) (string, error) {
	b, err := os.ReadFile(filepath.Join(absRoot, MovedMarker))
	if errors.Is(err, os.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	line, _, _ := strings.Cut(string(b), "\n")
	return strings.TrimSpace(line), nil
}

// checkDBRoot refuses a db-root that would silently start from the wrong
// databases: one other than where shfd db move put them, or a new, empty
// db-root next to a data-root that still holds registry.db (the move was
// not done).
func checkDBRoot(abs, dbAbs string) error {
	moved, err := MovedTo(abs)
	if err != nil {
		return err
	}
	if moved != "" && moved != dbAbs {
		return fmt.Errorf("the databases of data-root %s were moved to %s (shfd db move); run with --db-root %s (or $SHFD_DB_ROOT)", abs, moved, moved)
	}
	if dbAbs == abs || moved == dbAbs {
		return nil
	}
	if _, err := os.Stat(filepath.Join(dbAbs, RegistryFile)); err == nil {
		return nil
	}
	if _, err := os.Stat(filepath.Join(abs, RegistryFile)); err == nil {
		return fmt.Errorf("db-root %s has no %s but data-root %s has one: move the databases first with shfd db move --to %s (with the server stopped)", dbAbs, RegistryFile, abs, dbAbs)
	}
	return nil
}

// Root is the absolute data-root.
func (s *Server) Root() string { return s.root }

// DBRoot is the absolute directory of the databases (the data-root unless
// Options.DBRoot is set). serve.lock and admin.sock live here too.
func (s *Server) DBRoot() string { return s.dbRoot }

// SSH is the sshd endpoint advertised to clients (Options.SSH).
func (s *Server) SSH() SSHEndpoint { return s.opts.SSH }

// Ping checks that registry.db answers a trivial query; the health check
// of the HTTP API calls it with a short deadline.
func (s *Server) Ping(ctx context.Context) error {
	var one int
	return s.reg.QueryRowContext(ctx, "SELECT 1").Scan(&one)
}

// Close closes the registry and every open folder.
func (s *Server) Close() error {
	s.WaitMail() // a mail being sent still writes registry.db
	s.mu.Lock()
	defer s.mu.Unlock()
	var errs []error
	for id, sh := range s.shares {
		errs = append(errs, sh.close())
		delete(s.shares, id)
	}
	errs = append(errs, s.reg.Close())
	return errors.Join(errs...)
}

// CreateShare registers a new folder, lays out its directories and database
// and adds its first member, an admin. It returns the folder's id and that
// admin's token; the token is not stored and cannot be shown again.
func (s *Server) CreateShare(name, adminUser string) (id, token string, err error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return "", "", errf(CodeInvalidRequest, "folder name is empty")
	}
	if err := pathx.ValidateUsername(adminUser); err != nil {
		return "", "", errf(CodeInvalidRequest, "%v", err)
	}
	id = newUUID()
	if _, err := s.reg.Exec(`INSERT INTO shares (id, name, created_at) VALUES (?, ?, ?)`,
		id, name, fmtTime(s.opts.Now())); err != nil {
		if strings.Contains(err.Error(), "UNIQUE") {
			return "", "", errf(CodeInvalidRequest, "a folder named %q already exists", name)
		}
		return "", "", err
	}
	sh, err := s.Share(id)
	if err == nil {
		token, err = sh.AddMember(adminUser, true)
	}
	if err != nil {
		s.reg.Exec(`DELETE FROM shares WHERE id = ?`, id)
		s.mu.Lock()
		if sh := s.shares[id]; sh != nil {
			sh.close()
			delete(s.shares, id)
		}
		s.mu.Unlock()
		os.RemoveAll(filepath.Join(s.root, id))
		if s.dbRoot != s.root {
			os.RemoveAll(filepath.Join(s.dbRoot, id))
		}
		return "", "", err
	}
	return id, token, nil
}

// Shares lists the registry.
func (s *Server) Shares() ([]ShareInfo, error) {
	rows, err := s.reg.Query(`SELECT id, name, created_at FROM shares ORDER BY created_at, name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ShareInfo
	for rows.Next() {
		var si ShareInfo
		var at sql.NullString
		if err := rows.Scan(&si.ID, &si.Name, &at); err != nil {
			return nil, err
		}
		if t := parseTime(at); t != nil {
			si.CreatedAt = *t
		}
		out = append(out, si)
	}
	return out, rows.Err()
}

var uuidRE = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

// ValidShareID reports whether id has the form of a share id (a lowercase UUID).
func ValidShareID(id string) bool { return uuidRE.MatchString(id) }

// Share returns the open folder with the given id, opening it on first use
// (which also finishes any disk steps a crash left behind). It fails with
// CodeUnknownShare when the registry has no such folder.
func (s *Server) Share(id string) (*Share, error) {
	if !ValidShareID(id) {
		return nil, errf(CodeUnknownShare, "no folder %q", id)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if sh := s.shares[id]; sh != nil {
		return sh, nil
	}
	var n int
	if err := s.reg.QueryRow(`SELECT count(*) FROM shares WHERE id = ?`, id).Scan(&n); err != nil {
		return nil, err
	}
	if n == 0 {
		return nil, errf(CodeUnknownShare, "no folder %q", id)
	}
	sh, err := openShare(filepath.Join(s.root, id), filepath.Join(s.dbRoot, id), id, s.opts)
	if err != nil {
		return nil, err
	}
	sh.thumbSvc = &s.thumbSvc
	s.shares[id] = sh
	return sh, nil
}

// OpenAll opens every registered folder, which finishes the disk steps a
// crash left behind. shfd serve calls it before accepting requests.
func (s *Server) OpenAll() error {
	shares, err := s.Shares()
	if err != nil {
		return err
	}
	var errs []error
	for _, si := range shares {
		if _, err := s.Share(si.ID); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func newUUID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err)
	}
	b[6] = b[6]&0x0f | 0x40 // version 4
	b[8] = b[8]&0x3f | 0x80 // RFC 4122 variant
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

// SyncAuthorizedKeys rewrites the managed block of authorized_keys from the
// databases: in every folder, one line for every active member with an SSH
// key of its own (the legacy credential) and one for every device with an
// SSH key whose folder credential still works (member, member_devices row,
// the account's device and the account all active). Lines whose key
// clashes with another line are left out and returned as
// "share-id/username" or "share-id/username/device".
func (s *Server) SyncAuthorizedKeys() (skipped []string, err error) {
	if s.opts.AuthorizedKeys == nil {
		return nil, errf(CodeSSHNotConfigured, "no authorized_keys file is configured")
	}
	shares, err := s.Shares()
	if err != nil {
		return nil, err
	}
	devs, err := s.deviceStates()
	if err != nil {
		return nil, err
	}
	now := s.opts.Now()
	var entries []authkeys.Entry
	for _, si := range shares {
		sh, err := s.Share(si.ID)
		if err != nil {
			return nil, err
		}
		ms, err := sh.Members()
		if err != nil {
			return nil, err
		}
		byID := map[int64]*Member{}
		for i, m := range ms {
			byID[m.ID] = &ms[i]
			if m.RevokedAt == nil && m.SSHPubkey != "" {
				entries = append(entries, authkeys.Entry{ShareID: si.ID, MemberID: m.ID, Username: m.Username, Key: m.SSHPubkey})
			}
		}
		mds, err := sh.MemberDevices()
		if err != nil {
			return nil, err
		}
		for _, md := range mds {
			m, d := byID[md.MemberID], devs[md.DeviceID]
			if md.RevokedAt != nil || md.SSHPubkey == "" || m == nil || m.RevokedAt != nil || d.ID == 0 || d.why(m, now) != "" {
				continue
			}
			entries = append(entries, authkeys.Entry{ShareID: si.ID, MemberID: m.ID, DeviceID: d.ID,
				Username: m.Username, Device: d.Name, Key: md.SSHPubkey})
		}
	}
	sk, err := s.opts.AuthorizedKeys.Replace(entries)
	for _, e := range sk {
		name := e.ShareID + "/" + e.Username
		if e.DeviceID > 0 {
			name += "/" + authkeys.DeviceLabel(e.Device, e.DeviceID)
		}
		skipped = append(skipped, name)
	}
	return skipped, err
}
