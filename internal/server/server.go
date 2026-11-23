// Package server is the core of shfd: the registry of folders under a
// data-root, and for each folder its storage, members, change log, trash and
// the rules that decide what every operation does
// (docs/specifikacija.md, sections 3, 4 and 6).
//
// Layout on disk:
//
//	<data-root>/
//	  registry.db            folders: id (UUID), name, created_at
//	  <share-id>/
//	    storage/             current state, plain files
//	    trash/               deleted and replaced versions (flat)
//	    tmp/                 incoming uploads; same file system as storage/
//	    service.db           SQLite
//	    service.log          human-readable event log
package server

import (
	"crypto/rand"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/digital-cube/shared-folder/internal/authkeys"
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
}

// Server owns a data-root.
type Server struct {
	root string
	opts Options
	reg  *sql.DB

	mu     sync.Mutex
	shares map[string]*Share
}

// ShareInfo is one row of registry.db.
type ShareInfo struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	CreatedAt time.Time `json:"created_at"`
}

// Open opens the data-root, creating it and registry.db when missing.
func Open(root string, opts Options) (*Server, error) {
	if root == "" {
		return nil, errors.New("data-root is not set")
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(abs, 0o700); err != nil {
		return nil, err
	}
	reg, err := openDB(filepath.Join(abs, "registry.db"), "registry")
	if err != nil {
		return nil, err
	}
	return &Server{root: abs, opts: opts, reg: reg, shares: map[string]*Share{}}, nil
}

// Root is the absolute data-root.
func (s *Server) Root() string { return s.root }

// Close closes the registry and every open folder.
func (s *Server) Close() error {
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
	sh, err := openShare(filepath.Join(s.root, id), id, s.opts)
	if err != nil {
		return nil, err
	}
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
// databases: one line for every active member with an SSH key, in every
// folder. Lines whose key clashes with another line are left out and
// returned as "share-id/username".
func (s *Server) SyncAuthorizedKeys() (skipped []string, err error) {
	if s.opts.AuthorizedKeys == nil {
		return nil, errf(CodeSSHNotConfigured, "no authorized_keys file is configured")
	}
	shares, err := s.Shares()
	if err != nil {
		return nil, err
	}
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
		for _, m := range ms {
			if m.RevokedAt == nil && m.SSHPubkey != "" {
				entries = append(entries, authkeys.Entry{ShareID: si.ID, MemberID: m.ID, Username: m.Username, Key: m.SSHPubkey})
			}
		}
	}
	sk, err := s.opts.AuthorizedKeys.Replace(entries)
	for _, e := range sk {
		skipped = append(skipped, e.ShareID+"/"+e.Username)
	}
	return skipped, err
}
