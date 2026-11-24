package server

import (
	"bufio"
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"sync"
	"time"

	"github.com/digital-cube/shared-folder/internal/authkeys"
	"github.com/digital-cube/shared-folder/internal/pathx"
	"github.com/digital-cube/shared-folder/internal/thumbs"
)

// Directory and file names inside a share directory.
const (
	StorageDir = "storage"
	TrashDir   = "trash"
	TmpDir     = "tmp"
	DBFile     = "service.db"
	logFile    = "service.log"
)

// Share is one open folder. All operations that change it run one at a
// time under mu: the single writer of the folder.
type Share struct {
	ID    string
	dir   string // <data-root>/<share-id>
	dbDir string // <db-root>/<share-id>; dir unless the db-root is separate
	db    *sql.DB
	now   func() time.Time

	mu  sync.Mutex // the writer lock; also guards log
	log *os.File

	keys *authkeys.File // nil when SSH keys are not managed

	watch watchers // subscribers to the HEAD (the WebSocket)

	probes probes // liveness checks of the health check (probe.go)

	thumbs   thumbIndex    // ready and failed thumbnails (thumbs.go)
	thumbSvc *thumbService // the server's view of shfd thumbs; nil outside a Server

	// crashAfterCommit, when set, is called after the database commit and
	// before the disk steps run. Tests use it to simulate a crash there.
	crashAfterCommit func() error
}

func openShare(dir, dbDir, id string, opts Options) (*Share, error) {
	for _, d := range []string{dir, filepath.Join(dir, StorageDir), filepath.Join(dir, TrashDir), filepath.Join(dir, TmpDir), filepath.Join(dir, thumbs.Dir), dbDir} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			return nil, err
		}
	}
	db, err := openDB(filepath.Join(dbDir, DBFile), "service")
	if err != nil {
		return nil, err
	}
	lf, err := os.OpenFile(filepath.Join(dir, logFile), os.O_CREATE|os.O_RDWR|os.O_APPEND, 0o600)
	if err != nil {
		db.Close()
		return nil, err
	}
	s := &Share{ID: id, dir: dir, dbDir: dbDir, db: db, now: opts.Now, log: lf, keys: opts.AuthorizedKeys}
	if opts.SkipRecovery {
		return s, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.replayJournal(); err != nil {
		s.close()
		return nil, fmt.Errorf("folder %s: finishing interrupted disk steps: %w", id, err)
	}
	if err := s.catchUpLog(); err != nil {
		s.close()
		return nil, fmt.Errorf("folder %s: %s: %w", id, logFile, err)
	}
	return s, nil
}

func (s *Share) close() error {
	return errors.Join(s.db.Close(), s.log.Close())
}

// Dir is the share directory (<data-root>/<share-id>).
func (s *Share) Dir() string { return s.dir }

// DBPath is the folder's database (<db-root>/<share-id>/service.db).
func (s *Share) DBPath() string { return filepath.Join(s.dbDir, DBFile) }

// abs maps a share-relative slash path ("storage/a/b.txt") to the disk.
func (s *Share) abs(rel string) string { return filepath.Join(s.dir, filepath.FromSlash(rel)) }

// ---- members -------------------------------------------------------------

// Role of a member.
const (
	RoleAdmin  = "admin"
	RoleMember = "member"
)

// Member is a row of members, without the token hash.
type Member struct {
	ID           int64      `json:"id"`
	Username     string     `json:"username"`
	Role         string     `json:"role"`
	SSHPubkey    string     `json:"ssh_pubkey,omitempty"`
	CreatedAt    time.Time  `json:"created_at"`
	RevokedAt    *time.Time `json:"revoked_at,omitempty"`
	LastSeenAt   *time.Time `json:"last_seen_at,omitempty"`
	LastPushAt   *time.Time `json:"last_push_at,omitempty"`
	LastSeqAcked int64      `json:"last_seq_acked"`
}

// NewToken returns a fresh random token: "shf_" and 43 base64url characters
// (256 bits).
func NewToken() string {
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err)
	}
	return "shf_" + base64.RawURLEncoding.EncodeToString(b[:])
}

// HashToken is what members.token_hash stores. Tokens carry 256 random bits,
// so a plain SHA-256 is enough; there is nothing to brute-force.
func HashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// AddMember adds a member, or brings back a revoked one of that name (see
// AddOrReactivateMember), and returns the token. Only its hash is stored,
// so this is the one time the token is visible.
func (s *Share) AddMember(username string, admin bool) (string, error) {
	token, _, err := s.AddOrReactivateMember(username, admin)
	return token, err
}

// AddOrReactivateMember adds a member and returns the token. A username is
// unique in the folder, revoked members included, so a name that belongs
// to a revoked member brings that member back instead (reactivated is
// true): the same id, so the history (updated_by, trash "by") stays
// theirs, with a new token, the role given by admin, no SSH key (the old
// one is not restored; the client registers one again through shf join)
// and last_seq_acked back to 0 like a new member, since the client that
// joins again starts from the beginning of the log. last_seen_at and
// last_push_at are kept: they record what the member did, and the next
// call or push replaces them. A name of an active member fails with
// CodeMemberExists.
func (s *Share) AddOrReactivateMember(username string, admin bool) (token string, reactivated bool, err error) {
	if err := pathx.ValidateUsername(username); err != nil {
		return "", false, errf(CodeInvalidRequest, "%v", err)
	}
	role := RoleMember
	if admin {
		role = RoleAdmin
	}
	token = NewToken()
	tx, err := s.db.Begin()
	if err != nil {
		return "", false, err
	}
	defer tx.Rollback()
	var id int64
	var revoked sql.NullString
	err = tx.QueryRow(`SELECT id, revoked_at FROM members WHERE username = ?`, username).Scan(&id, &revoked)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		if _, err := tx.Exec(`INSERT INTO members (username, token_hash, role, created_at) VALUES (?, ?, ?, ?)`,
			username, HashToken(token), role, fmtTime(s.now())); err != nil {
			return "", false, err
		}
	case err != nil:
		return "", false, err
	case !revoked.Valid:
		return "", false, errf(CodeMemberExists, "member %q already exists", username)
	default:
		reactivated = true
		if _, err := tx.Exec(`UPDATE members SET token_hash = ?, role = ?, revoked_at = NULL, ssh_pubkey = '',
			last_seq_acked = 0 WHERE id = ?`, HashToken(token), role, id); err != nil {
			return "", false, err
		}
		// revoke removed the line, but its removal may have failed; the
		// old key must not let the member in again. Done while the row is
		// locked, like SetSSHKey.
		if s.keys != nil {
			if err := s.keys.Remove(s.ID, id); err != nil {
				return "", false, fmt.Errorf("removing the old SSH key of %s from %s: %w", username, s.keys.Path, err)
			}
		}
	}
	if err := tx.Commit(); err != nil {
		return "", false, err
	}
	return token, reactivated, nil
}

// SetRole changes the role of an active member and returns the role it
// had. It refuses a revoked or unknown member (CodeUnknownMember) and
// taking the admin role from the folder's last active admin
// (CodeLastAdmin): trash, verify and purge act in the name of the oldest
// active admin, so a folder must always have one.
func (s *Share) SetRole(username, role string) (old string, err error) {
	if role != RoleAdmin && role != RoleMember {
		return "", errf(CodeInvalidRequest, "role must be %s or %s, not %q", RoleAdmin, RoleMember, role)
	}
	tx, err := s.db.Begin()
	if err != nil {
		return "", err
	}
	defer tx.Rollback()
	var id int64
	err = tx.QueryRow(`SELECT id, role FROM members WHERE username = ? AND revoked_at IS NULL`, username).Scan(&id, &old)
	if errors.Is(err, sql.ErrNoRows) {
		return "", errf(CodeUnknownMember, "no active member %q", username)
	}
	if err != nil {
		return "", err
	}
	if old == role {
		return old, nil
	}
	if old == RoleAdmin {
		var others int
		if err := tx.QueryRow(`SELECT count(*) FROM members WHERE role = 'admin' AND revoked_at IS NULL AND id != ?`, id).Scan(&others); err != nil {
			return "", err
		}
		if others == 0 {
			return "", errf(CodeLastAdmin, "%s is the last active admin of the folder; make another member an admin first", username)
		}
	}
	if _, err := tx.Exec(`UPDATE members SET role = ? WHERE id = ?`, role, id); err != nil {
		return "", err
	}
	return old, tx.Commit()
}

// ResetToken gives an active member a new token and returns it; the old
// one stops working at once (the API answers 401) and the member's open
// WebSockets in this process are closed as on a revocation (those of a
// shfd serve in another process within its member check). The SSH key
// stays. A revoked member is brought back with AddMember instead.
func (s *Share) ResetToken(username string) (string, error) {
	token := NewToken()
	var id int64
	err := s.db.QueryRow(`UPDATE members SET token_hash = ? WHERE username = ? AND revoked_at IS NULL RETURNING id`,
		HashToken(token), username).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		if m, err := s.MemberByName(username); err == nil && m.RevokedAt != nil {
			return "", errf(CodeUnknownMember, "member %q is revoked; bring the member back with shfd member add", username)
		}
		return "", errf(CodeUnknownMember, "no member %q", username)
	}
	if err != nil {
		return "", err
	}
	s.revokeWatchers(id)
	return token, nil
}

// RevokeMember revokes a member's token and removes the member's line from
// authorized_keys. The member stays in the list (and in the history);
// revoking twice is not an error. rsync-gate refuses a revoked member even
// while the line is still there, so a failure to rewrite authorized_keys
// (returned after the revocation is stored) does not keep access open.
// Revoking the folder's last active admin is refused (CodeLastAdmin), as
// in SetRole: a folder must always have one.
func (s *Share) RevokeMember(username string) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var id int64
	var role string
	var revoked sql.NullString
	err = tx.QueryRow(`SELECT id, role, revoked_at FROM members WHERE username = ?`, username).Scan(&id, &role, &revoked)
	if errors.Is(err, sql.ErrNoRows) {
		return errf(CodeUnknownMember, "no member %q", username)
	}
	if err != nil {
		return err
	}
	if !revoked.Valid && role == RoleAdmin {
		var others int
		if err := tx.QueryRow(`SELECT count(*) FROM members WHERE role = 'admin' AND revoked_at IS NULL AND id != ?`, id).Scan(&others); err != nil {
			return err
		}
		if others == 0 {
			return errf(CodeLastAdmin, "%s is the last active admin of the folder; make another member an admin first", username)
		}
	}
	if _, err := tx.Exec(`UPDATE members SET revoked_at = coalesce(revoked_at, ?) WHERE id = ?`, fmtTime(s.now()), id); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	s.revokeWatchers(id)
	if s.keys != nil {
		if err := s.keys.Remove(s.ID, id); err != nil {
			return fmt.Errorf("member %s is revoked, but removing the SSH key from %s failed: %w", username, s.keys.Path, err)
		}
	}
	return nil
}

// SetSSHKey registers the member's SSH public key: it is stored in
// members.ssh_pubkey and written to authorized_keys as the member's line,
// replacing an earlier key. It returns the key as stored ("type base64")
// and, when that replaced a different key, the replaced one: a member has
// one key, so the device that registered it can no longer sync. Such a
// replacement leaves a key-replaced line in service.log. Registering the
// same key again (a join repeated after an interruption) replaces nothing.
func (s *Share) SetSSHKey(m *Member, pubkey string) (key, replaced string, err error) {
	if s.keys == nil {
		return "", "", errf(CodeSSHNotConfigured, "this server does not manage SSH keys (no authorized_keys file configured)")
	}
	key, err = authkeys.ParsePublicKey(pubkey)
	if err != nil {
		return "", "", errf(CodeInvalidKey, "%v", err)
	}
	tx, err := s.db.Begin()
	if err != nil {
		return "", "", err
	}
	defer tx.Rollback()
	var revoked sql.NullString
	var old string
	if err := tx.QueryRow(`SELECT revoked_at, ssh_pubkey FROM members WHERE id = ?`, m.ID).Scan(&revoked, &old); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", "", errf(CodeUnknownMember, "no member %d", m.ID)
		}
		return "", "", err
	}
	if revoked.Valid {
		return "", "", errf(CodeUnauthorized, "member %s is revoked", m.Username)
	}
	if _, err := tx.Exec(`UPDATE members SET ssh_pubkey = ? WHERE id = ?`, key, m.ID); err != nil {
		return "", "", err
	}
	// The file is written while the row is locked, so a concurrent revoke
	// cannot slip in between and leave the line behind.
	if err := s.keys.Set(authkeys.Entry{ShareID: s.ID, MemberID: m.ID, Username: m.Username, Key: key}); err != nil {
		if errors.Is(err, authkeys.ErrKeyInUse) {
			return "", "", errf(CodeKeyInUse, "%v", err)
		}
		return "", "", err
	}
	if err := tx.Commit(); err != nil {
		return "", "", err
	}
	if old == "" || old == key {
		return key, "", nil
	}
	line := fmt.Sprintf("%s key-replaced member=%s old=%s new=%s\n",
		fmtTime(s.now()), m.Username, authkeys.Fingerprint(old), authkeys.Fingerprint(key))
	s.mu.Lock()
	defer s.mu.Unlock()
	// The key is registered; a log line that cannot be written does not
	// undo that.
	_ = s.writeLog([]byte(line))
	return key, old, nil
}

// MemberByID returns a member, revoked or not.
func (s *Share) MemberByID(id int64) (*Member, error) {
	m, err := scanMember(s.db.QueryRow(`SELECT `+memberCols+` FROM members WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, errf(CodeUnknownMember, "no member %d", id)
	}
	return m, err
}

// CheckUpload fails with CodeUnknownUpload unless uploadID is an open
// (announced, not yet committed) upload of the member.
func (s *Share) CheckUpload(memberID int64, uploadID string) error {
	if !ValidUploadID(uploadID) {
		return errf(CodeUnknownUpload, "no upload %q", uploadID)
	}
	_, err := loadUpload(s.db, uploadID, memberID)
	return err
}

const memberCols = `id, username, role, ssh_pubkey, created_at, revoked_at, last_seen_at, last_push_at, last_seq_acked`

func scanMember(sc interface{ Scan(...any) error }) (*Member, error) {
	var m Member
	var created, revoked, seen, push sql.NullString
	if err := sc.Scan(&m.ID, &m.Username, &m.Role, &m.SSHPubkey, &created, &revoked, &seen, &push, &m.LastSeqAcked); err != nil {
		return nil, err
	}
	if t := parseTime(created); t != nil {
		m.CreatedAt = *t
	}
	m.RevokedAt, m.LastSeenAt, m.LastPushAt = parseTime(revoked), parseTime(seen), parseTime(push)
	return &m, nil
}

// Members lists all members, revoked ones included.
func (s *Share) Members() ([]Member, error) {
	rows, err := s.db.Query(`SELECT ` + memberCols + ` FROM members ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Member
	for rows.Next() {
		m, err := scanMember(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *m)
	}
	return out, rows.Err()
}

// Authenticate returns the active member whose token this is, or an
// *Error with CodeUnauthorized for an empty, unknown or revoked token.
func (s *Share) Authenticate(token string) (*Member, error) {
	if token == "" {
		return nil, errf(CodeUnauthorized, "missing token")
	}
	m, err := scanMember(s.db.QueryRow(`SELECT `+memberCols+` FROM members WHERE token_hash = ?`, HashToken(token)))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, errf(CodeUnauthorized, "unknown token")
	}
	if err != nil {
		return nil, err
	}
	if m.RevokedAt != nil {
		return nil, errf(CodeUnauthorized, "token revoked")
	}
	return m, nil
}

// Touch records that the member made a successful call.
func (s *Share) Touch(memberID int64) error {
	_, err := s.db.Exec(`UPDATE members SET last_seen_at = ? WHERE id = ?`, fmtTime(s.now()), memberID)
	return err
}

// ---- service.log ---------------------------------------------------------

// logEvent appends one line per event: time, seq, member, operation, path.
// Caller holds s.mu.
func (s *Share) logEvents(evs []Event) error {
	var b bytes.Buffer
	for _, e := range evs {
		fmt.Fprintf(&b, "%s seq=%d member=%s op=%s path=%s", e.TS.UTC().Format(timeLayout), e.Seq, e.Member, e.Op, strconv.Quote(e.Path))
		if e.OldPath != "" {
			fmt.Fprintf(&b, " old_path=%s", strconv.Quote(e.OldPath))
		}
		b.WriteByte('\n')
	}
	return s.writeLog(b.Bytes())
}

// Transfer is one call of rsync-gate, for service.log.
type Transfer struct {
	Member    string // username, or "#<id>" when the id is unknown
	Direction string // "upload", "download" or "" when the command was refused before that was clear
	Path      string // share-relative: tmp/<upload_id> or storage/<path>
	Outcome   string // "ok", "denied" or "failed"
	Detail    string // reason of a denial, or the exit status of rsync
}

// LogTransfer appends one line for a transfer to service.log. rsync-gate
// runs in its own process; the file is opened O_APPEND and every line is a
// single write, so its lines and those of shfd serve do not interleave.
func (s *Share) LogTransfer(t Transfer) error {
	line := fmt.Sprintf("%s transfer member=%s dir=%s path=%s outcome=%s",
		fmtTime(s.now()), t.Member, orDash(t.Direction), strconv.Quote(t.Path), t.Outcome)
	if t.Detail != "" {
		line += " detail=" + strconv.Quote(t.Detail)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.writeLog([]byte(line + "\n"))
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

func (s *Share) writeLog(p []byte) error {
	if len(p) == 0 {
		return nil
	}
	if _, err := s.log.Write(p); err != nil {
		return err
	}
	return s.log.Sync()
}

var logSeqRE = regexp.MustCompile(` seq=(\d+) `)

// catchUpLog appends the lines of events that were committed but did not
// reach service.log (a crash right after a commit). events is the source
// of truth; the log is its readable copy.
func (s *Share) catchUpLog() error {
	last, err := lastLoggedSeq(s.log)
	if err != nil {
		return err
	}
	var head int64
	if err := s.db.QueryRow(`SELECT coalesce(max(seq), 0) FROM events`).Scan(&head); err != nil {
		return err
	}
	for last < head {
		evs, _, _, err := s.changes(last, 1000)
		if err != nil {
			return err
		}
		if len(evs) == 0 {
			return nil
		}
		if err := s.logEvents(evs); err != nil {
			return err
		}
		last = evs[len(evs)-1].Seq
	}
	return nil
}

// lastLoggedSeq finds the largest seq in the tail of the log.
func lastLoggedSeq(f *os.File) (int64, error) {
	st, err := f.Stat()
	if err != nil {
		return 0, err
	}
	const tail = 256 << 10
	off := max(st.Size()-tail, 0)
	buf := make([]byte, st.Size()-off)
	if _, err := f.ReadAt(buf, off); err != nil && err != io.EOF {
		return 0, err
	}
	var last int64
	sc := bufio.NewScanner(bytes.NewReader(buf))
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	for sc.Scan() {
		if m := logSeqRE.FindSubmatch(sc.Bytes()); m != nil {
			if n, err := strconv.ParseInt(string(m[1]), 10, 64); err == nil && n > last {
				last = n
			}
		}
	}
	return last, nil
}

// ---- disk journal --------------------------------------------------------

// fsStep is one change on disk. Paths are relative to the share directory.
// Every step is idempotent, so replaying a journal is always safe:
//
//	rename  if dst exists the step is done; otherwise src is renamed to dst.
//	        A plan never renames onto an existing dst, which is what makes
//	        "dst exists" mean "already done".
//	mkdir   create dst (and parents) if missing.
//	rmdir   remove the empty directory dst if present.
//	remove  remove the file dst if present.
type fsStep struct {
	Op, Src, Dst string
}

func (s *Share) runStep(st fsStep) error {
	dst := s.abs(st.Dst)
	switch st.Op {
	case "rename":
		if _, err := os.Lstat(dst); err == nil {
			return nil
		}
		src := s.abs(st.Src)
		if _, err := os.Lstat(src); err != nil {
			return fmt.Errorf("rename %s -> %s: source and target both missing: %w", st.Src, st.Dst, err)
		}
		if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
			return err
		}
		if err := os.Rename(src, dst); err != nil {
			return err
		}
		syncDir(filepath.Dir(src))
		syncDir(filepath.Dir(dst))
		return nil
	case "mkdir":
		if err := os.MkdirAll(dst, 0o700); err != nil {
			return err
		}
		syncDir(filepath.Dir(dst))
		return nil
	case "rmdir", "remove":
		if err := os.Remove(dst); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		syncDir(filepath.Dir(dst))
		return nil
	}
	return fmt.Errorf("unknown disk step %q", st.Op)
}

// replayJournal finishes the disk steps of operations whose database part
// committed. Caller holds s.mu.
func (s *Share) replayJournal() error {
	rows, err := s.db.Query(`SELECT op, src, dst FROM fs_journal ORDER BY id`)
	if err != nil {
		return err
	}
	var steps []fsStep
	for rows.Next() {
		var st fsStep
		if err := rows.Scan(&st.Op, &st.Src, &st.Dst); err != nil {
			rows.Close()
			return err
		}
		steps = append(steps, st)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	if len(steps) == 0 {
		return nil
	}
	return s.runSteps(steps)
}

func (s *Share) runSteps(steps []fsStep) error {
	for _, st := range steps {
		if err := s.runStep(st); err != nil {
			return err
		}
	}
	_, err := s.db.Exec(`DELETE FROM fs_journal`)
	return err
}

// syncDir makes a rename inside dir durable. Best effort: not every
// platform can fsync a directory.
func syncDir(dir string) {
	if d, err := os.Open(dir); err == nil {
		d.Sync()
		d.Close()
	}
}
