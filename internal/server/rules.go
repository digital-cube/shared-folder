package server

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/digital-cube/shared-folder/internal/hashx"
	"github.com/digital-cube/shared-folder/internal/pathx"
)

// Event is one row of the change log.
type Event struct {
	Seq      int64     `json:"seq"`
	TS       time.Time `json:"ts"`
	Member   string    `json:"member"`
	Op       string    `json:"op"`
	Path     string    `json:"path"`
	OldPath  string    `json:"old_path,omitempty"`
	Hash     string    `json:"hash,omitempty"`
	PrevHash string    `json:"prev_hash,omitempty"`
	TrashID  *int64    `json:"trash_id,omitempty"`
	// Size and Mtime (Unix nanoseconds) of the content a put, conflict or
	// move leaves at Path.
	Size  int64 `json:"size,omitempty"`
	Mtime int64 `json:"mtime,omitempty"`
}

// Event operations.
const (
	OpPut      = "put"
	OpDelete   = "delete"
	OpMove     = "move"
	OpMkdir    = "mkdir"
	OpRmdir    = "rmdir"
	OpConflict = "conflict"
	OpRestore  = "restore"
	OpPurge    = "purge"
)

// Result statuses.
const (
	StatusApplied   = "applied"   // the operation changed the folder
	StatusUnchanged = "unchanged" // nothing to do; no event
	StatusConflict  = "conflict"  // the upload was kept as a conflict copy
	StatusUpload    = "upload"    // announce: send the content, then commit
)

// Result is what a commit or an op did.
type Result struct {
	Status string `json:"status"`
	Path   string `json:"path"`
	// ConflictPath is where the content went when Status is "conflict".
	ConflictPath string  `json:"conflict_path,omitempty"`
	Events       []Event `json:"events"`
	Head         int64   `json:"head"`
}

var (
	hashRE     = regexp.MustCompile(`^[0-9a-f]{64}$`)
	uploadIDRE = regexp.MustCompile(`^[0-9a-f]{32}$`)
)

// ValidUploadID reports whether id has the form the server hands out.
func ValidUploadID(id string) bool { return uploadIDRE.MatchString(id) }

func normPath(p string) (string, error) {
	n, err := pathx.Normalize(p)
	if err != nil {
		return "", errf(CodeInvalidPath, "%v", err)
	}
	return n, nil
}

func checkHash(field, h string, optional bool) error {
	if h == "" && optional {
		return nil
	}
	if !hashRE.MatchString(h) {
		return errf(CodeInvalidRequest, "%s must be 64 lowercase hex characters", field)
	}
	return nil
}

// ---- reading -------------------------------------------------------------

type querier interface {
	QueryRow(query string, args ...any) *sql.Row
}

type fileRow struct {
	Path, Kind, Hash string
	Size, Mtime, Seq int64
	UpdatedBy        string
}

func rowByFold(q querier, key string) (*fileRow, error) {
	var r fileRow
	err := q.QueryRow(`SELECT path, kind, hash, size, mtime, seq, updated_by FROM files WHERE fold_key = ?`, key).
		Scan(&r.Path, &r.Kind, &r.Hash, &r.Size, &r.Mtime, &r.Seq, &r.UpdatedBy)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &r, nil
}

// resolve looks p and each of its parents up by fold key. It returns the
// current row of p (nil when absent) and the parents that do not exist yet,
// outermost first. A name that matches an existing one only up to letter
// case is a case_conflict, unless the existing one is self (a move that
// only changes case); a parent that is a file is parent_not_dir.
func resolve(q querier, p, self string) (cur *fileRow, missing []string, err error) {
	segs := strings.Split(p, "/")
	for i := 1; i < len(segs); i++ {
		dir := strings.Join(segs[:i], "/")
		r, err := rowByFold(q, pathx.FoldKey(dir))
		if err != nil {
			return nil, nil, err
		}
		switch {
		case r == nil:
			missing = append(missing, dir)
		case r.Path != dir:
			return nil, nil, errf(CodeCaseConflict, "%q differs from existing %q only in letter case", dir, r.Path)
		case r.Kind != "dir":
			return nil, nil, errf(CodeParentNotDir, "%q is a file", dir)
		}
	}
	r, err := rowByFold(q, pathx.FoldKey(p))
	if err != nil || r == nil {
		return nil, missing, err
	}
	if r.Path == p {
		return r, missing, nil
	}
	if r.Path == self {
		return nil, missing, nil
	}
	return nil, nil, errf(CodeCaseConflict, "%q differs from existing %q only in letter case", p, r.Path)
}

// Head is the seq of the newest event (0 for an empty log).
func (s *Share) Head() (int64, error) {
	var head int64
	err := s.db.QueryRow(`SELECT coalesce(max(seq), 0) FROM events`).Scan(&head)
	return head, err
}

// Changes returns up to limit events after since, oldest first, the HEAD
// at the time of the call and whether events after the returned ones exist
// up to that HEAD.
func (s *Share) Changes(since int64, limit int) (evs []Event, head int64, more bool, err error) {
	return s.changes(since, limit)
}

func (s *Share) changes(since int64, limit int) ([]Event, int64, bool, error) {
	head, err := s.Head()
	if err != nil {
		return nil, 0, false, err
	}
	rows, err := s.db.Query(`SELECT seq, ts, member, op, path, old_path, hash, prev_hash, trash_id, size, mtime
		FROM events WHERE seq > ? AND seq <= ? ORDER BY seq LIMIT ?`, since, head, limit+1)
	if err != nil {
		return nil, 0, false, err
	}
	defer rows.Close()
	evs := []Event{}
	for rows.Next() {
		var e Event
		var ts string
		var tid sql.NullInt64
		if err := rows.Scan(&e.Seq, &ts, &e.Member, &e.Op, &e.Path, &e.OldPath, &e.Hash, &e.PrevHash, &tid, &e.Size, &e.Mtime); err != nil {
			return nil, 0, false, err
		}
		e.TS, _ = time.Parse(timeLayout, ts)
		if tid.Valid {
			e.TrashID = &tid.Int64
		}
		evs = append(evs, e)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, false, err
	}
	more := len(evs) > limit
	if more {
		evs = evs[:limit]
	}
	return evs, head, more, nil
}

// ---- the writer ----------------------------------------------------------

// op is one operation in progress: a database transaction plus the disk
// steps that go with it.
type op struct {
	s      *Share
	tx     *sql.Tx
	m      *Member
	now    time.Time
	head   int64
	events []Event
	steps  []fsStep
	// admin marks an admin operation (restore, purge, verify --repair):
	// it is not a push of the member, so last_push_at stays.
	admin bool
}

// write runs fn as one unit. fn inserts rows through o and queues disk
// steps; they are committed together with the steps in fs_journal, then
// the steps run and the journal is cleared. A crash before the commit
// leaves nothing changed; a crash after it is finished on the next open.
// Caller holds s.mu.
func (s *Share) write(m *Member, fn func(o *op) error) ([]Event, int64, error) {
	return s.writeAs(m, false, fn)
}

// writeAdmin is write for admin operations (see op.admin).
func (s *Share) writeAdmin(m *Member, fn func(o *op) error) ([]Event, int64, error) {
	return s.writeAs(m, true, fn)
}

func (s *Share) writeAs(m *Member, admin bool, fn func(o *op) error) ([]Event, int64, error) {
	if err := s.replayJournal(); err != nil {
		return nil, 0, fmt.Errorf("finishing earlier disk steps: %w", err)
	}
	tx, err := s.db.Begin()
	if err != nil {
		return nil, 0, err
	}
	o := &op{s: s, tx: tx, m: m, now: s.now().UTC(), admin: admin}
	if err := tx.QueryRow(`SELECT coalesce(max(seq), 0) FROM events`).Scan(&o.head); err != nil {
		tx.Rollback()
		return nil, 0, err
	}
	err = fn(o)
	if err == nil {
		err = s.checkSteps(o.steps)
	}
	if err == nil {
		err = o.finish()
	}
	if err != nil {
		tx.Rollback()
		return nil, 0, err
	}
	if err := tx.Commit(); err != nil {
		return nil, 0, err
	}
	if len(o.events) > 0 {
		// After the disk steps (or their failure, which the next write
		// retries): a member told about the new HEAD fetches the files.
		defer s.notify(o.events[len(o.events)-1].Seq)
	}
	if s.crashAfterCommit != nil {
		if err := s.crashAfterCommit(); err != nil {
			return nil, 0, err
		}
	}
	if len(o.steps) > 0 {
		if err := s.runSteps(o.steps); err != nil {
			return nil, 0, fmt.Errorf("disk steps (will be retried): %w", err)
		}
	}
	if err := s.logEvents(o.events); err != nil {
		return nil, 0, fmt.Errorf("%s: %w", logFile, err)
	}
	return o.events, o.head, nil
}

// finish writes the journal and the member's last push.
func (o *op) finish() error {
	for _, st := range o.steps {
		if _, err := o.tx.Exec(`INSERT INTO fs_journal (op, src, dst) VALUES (?, ?, ?)`, st.Op, st.Src, st.Dst); err != nil {
			return err
		}
	}
	if len(o.events) > 0 && !o.admin {
		if _, err := o.tx.Exec(`UPDATE members SET last_push_at = ? WHERE id = ?`, fmtTime(o.now), o.m.ID); err != nil {
			return err
		}
	}
	return nil
}

// checkSteps refuses a plan whose rename would land on a path that already
// exists on disk (and is not vacated by an earlier step of the same plan).
// That only happens when storage/ drifted from the database; going ahead
// would break the "target exists means done" rule of replay.
func (s *Share) checkSteps(steps []fsStep) error {
	vacated := map[string]bool{}
	for _, st := range steps {
		if st.Op != "rename" {
			continue
		}
		if _, err := os.Lstat(s.abs(st.Dst)); err == nil && !vacated[st.Dst] {
			return fmt.Errorf("%s exists on disk but not in the database; storage needs verify", st.Dst)
		}
		vacated[st.Src] = true
		delete(vacated, st.Dst)
	}
	return nil
}

func (o *op) step(opName, src, dst string) {
	o.steps = append(o.steps, fsStep{Op: opName, Src: src, Dst: dst})
}

func storagePath(p string) string { return StorageDir + "/" + p }

// event appends an event with the next seq.
func (o *op) event(e Event) (Event, error) {
	o.head++
	e.Seq, e.TS, e.Member = o.head, o.now, o.m.Username
	var tid any
	if e.TrashID != nil {
		tid = *e.TrashID
	}
	_, err := o.tx.Exec(`INSERT INTO events (seq, ts, member, op, path, old_path, hash, prev_hash, trash_id, size, mtime)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		e.Seq, fmtTime(e.TS), e.Member, e.Op, e.Path, e.OldPath, e.Hash, e.PrevHash, tid, e.Size, e.Mtime)
	if err != nil {
		return e, err
	}
	o.events = append(o.events, e)
	return e, nil
}

// mkdir records a new directory: files row, mkdir event, disk step.
func (o *op) mkdir(dir string) error {
	e, err := o.event(Event{Op: OpMkdir, Path: dir})
	if err != nil {
		return err
	}
	if _, err := o.tx.Exec(`INSERT INTO files (path, fold_key, kind, seq, updated_by) VALUES (?, ?, 'dir', ?, ?)`,
		dir, pathx.FoldKey(dir), e.Seq, o.m.Username); err != nil {
		return err
	}
	o.step("mkdir", "", storagePath(dir))
	return nil
}

// toTrash moves the current version of a file to trash/ under a unique
// name, recording it for the event that will get seq o.head+1.
func (o *op) toTrash(r *fileRow, reason pathx.TrashReason) (int64, error) {
	base, err := pathx.TrashName(r.Path, reason, o.m.Username, o.now)
	if err != nil {
		return 0, err
	}
	name := base
	for n := 2; ; n++ {
		var taken int
		if err := o.tx.QueryRow(`SELECT count(*) FROM trash WHERE fold_key = ?`, pathx.FoldKey(name)).Scan(&taken); err != nil {
			return 0, err
		}
		if _, statErr := os.Lstat(o.s.abs(TrashDir + "/" + name)); taken == 0 && statErr != nil {
			break
		}
		name = fmt.Sprintf("%s-%d", base, n)
	}
	res, err := o.tx.Exec(`INSERT INTO trash (orig_path, trash_name, fold_key, hash, size, reason, by_member, at, event_seq)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		r.Path, name, pathx.FoldKey(name), r.Hash, r.Size, string(reason), o.m.Username, fmtTime(o.now), o.head+1)
	if err != nil {
		return 0, err
	}
	o.step("rename", storagePath(r.Path), TrashDir+"/"+name)
	return res.LastInsertId()
}

// conflictPath returns a free name for the conflict copy of p.
func (o *op) conflictPath(p string) (string, error) {
	cp, err := pathx.ConflictPath(p, o.m.Username, o.now)
	if err != nil {
		return "", err
	}
	return o.freeDerived(cp)
}

// freeDerived returns cp, a derived name carrying o.now's timestamp, or
// the first of cp with "-2", "-3", … after the timestamp that no file or
// directory uses.
func (o *op) freeDerived(cp string) (string, error) {
	ts := pathx.Timestamp(o.now)
	i := strings.LastIndex(cp, ts) + len(ts)
	cand := cp
	for n := 2; ; n++ {
		r, err := rowByFold(o.tx, pathx.FoldKey(cand))
		if err != nil {
			return "", err
		}
		if r == nil {
			return cand, nil
		}
		cand = fmt.Sprintf("%s-%d%s", cp[:i], n, cp[i:])
	}
}

// ---- uploads -------------------------------------------------------------

// AnnounceRequest announces a file the client wants to upload.
type AnnounceRequest struct {
	Path     string `json:"path"`
	BaseHash string `json:"base_hash"` // version the client had; "" for a new file
	NewHash  string `json:"new_hash"`
	Size     int64  `json:"size"`
	Mtime    int64  `json:"mtime"` // Unix nanoseconds
}

// AnnounceResult says whether to send the content and where.
type AnnounceResult struct {
	Status   string `json:"status"` // "upload" or "unchanged"
	Path     string `json:"path"`   // normalized path
	UploadID string `json:"upload_id,omitempty"`
	// Dest is where the transport puts the content, relative to the share
	// directory: tmp/<upload_id>.
	Dest string `json:"dest,omitempty"`
	Head int64  `json:"head"`
}

// Announce registers an upload. When new_hash already is the current
// content of the path there is nothing to send. Announcing again what
// the member already announced and did not commit returns the same
// upload, so its partial content is resumed.
func (s *Share) Announce(m *Member, req AnnounceRequest) (*AnnounceResult, error) {
	p, err := normPath(req.Path)
	if err != nil {
		return nil, err
	}
	if err := checkHash("base_hash", req.BaseHash, true); err != nil {
		return nil, err
	}
	if err := checkHash("new_hash", req.NewHash, false); err != nil {
		return nil, err
	}
	if req.Size < 0 {
		return nil, errf(CodeInvalidRequest, "size is negative")
	}
	head, err := s.Head()
	if err != nil {
		return nil, err
	}
	cur, _, err := resolve(s.db, p, "")
	if err != nil {
		return nil, err
	}
	if cur != nil && cur.Kind == "dir" {
		return nil, errf(CodeKindMismatch, "%q is a directory", p)
	}
	if cur != nil && cur.Hash == req.NewHash {
		return &AnnounceResult{Status: StatusUnchanged, Path: p, Head: head}, nil
	}
	// The member's open upload of the same content (a send that was
	// interrupted, also by a restart of the client) is handed out again,
	// so rsync --partial continues from what tmp/<id> already holds
	// instead of starting over. One whose commit has begun is not.
	var open string
	err = s.db.QueryRow(`SELECT id FROM uploads WHERE member_id = ? AND path = ? AND base_hash = ? AND new_hash = ? AND size = ?
		ORDER BY created_at DESC, rowid DESC LIMIT 1`, m.ID, p, req.BaseHash, req.NewHash, req.Size).Scan(&open)
	switch {
	case err == nil:
		if _, serr := os.Lstat(s.abs(TmpDir + "/" + open + ".commit")); errors.Is(serr, os.ErrNotExist) {
			if _, err := s.db.Exec(`UPDATE uploads SET mtime = ? WHERE id = ?`, req.Mtime, open); err != nil {
				return nil, err
			}
			return &AnnounceResult{Status: StatusUpload, Path: p, UploadID: open, Dest: TmpDir + "/" + open, Head: head}, nil
		}
	case !errors.Is(err, sql.ErrNoRows):
		return nil, err
	}
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return nil, err
	}
	id := hex.EncodeToString(b[:])
	if _, err := s.db.Exec(`INSERT INTO uploads (id, member_id, path, base_hash, new_hash, size, mtime, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`, id, m.ID, p, req.BaseHash, req.NewHash, req.Size, req.Mtime, fmtTime(s.now())); err != nil {
		return nil, err
	}
	return &AnnounceResult{Status: StatusUpload, Path: p, UploadID: id, Dest: TmpDir + "/" + id, Head: head}, nil
}

type upload struct {
	ID, Path, BaseHash, NewHash string
	Size, Mtime                 int64
}

func loadUpload(q querier, id string, memberID int64) (*upload, error) {
	u := upload{ID: id}
	err := q.QueryRow(`SELECT path, base_hash, new_hash, size, mtime FROM uploads WHERE id = ? AND member_id = ?`, id, memberID).
		Scan(&u.Path, &u.BaseHash, &u.NewHash, &u.Size, &u.Mtime)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, errf(CodeUnknownUpload, "no upload %q", id)
	}
	if err != nil {
		return nil, err
	}
	return &u, nil
}

// discardUpload forgets an upload and removes its files from tmp/.
func (s *Share) discardUpload(id string) {
	s.db.Exec(`DELETE FROM uploads WHERE id = ?`, id)
	os.Remove(s.abs(TmpDir + "/" + id))
	os.Remove(s.abs(TmpDir + "/" + id + ".commit"))
}

// Commit checks the content that arrived for an upload and applies the
// rules (docs/specifikacija.md, section 6):
//
//  1. new_hash is the current content: nothing changes, no event.
//  2. base_hash is the current content: the old version goes to the trash
//     (replaced) and the new one takes the name; event put.
//  3. base_hash differs from the current content (also when the client
//     thinks the file is new but it exists): the existing file stays and the
//     upload becomes name.conflict-{user}-{ts}.ext; event conflict.
//  4. the path does not exist (new, or deleted meanwhile): the file appears;
//     event put, after a mkdir event for every missing parent.
//
// Content whose size or hash differ from the announcement is discarded with
// size_mismatch or hash_mismatch and nothing changes.
func (s *Share) Commit(m *Member, uploadID string) (*Result, error) {
	if !ValidUploadID(uploadID) {
		return nil, errf(CodeUnknownUpload, "no upload %q", uploadID)
	}
	up, err := loadUpload(s.db, uploadID, m.ID)
	if err != nil {
		return nil, err
	}
	tmp := TmpDir + "/" + up.ID
	// The transport may write only tmp/<id>. Moving the content to a name
	// it cannot write freezes it before it is checked.
	staged := tmp + ".commit"
	if _, err := os.Lstat(s.abs(staged)); err != nil {
		if err := os.Rename(s.abs(tmp), s.abs(staged)); err != nil {
			if errors.Is(err, os.ErrNotExist) {
				return nil, errf(CodeUploadMissing, "no content received for upload %s", up.ID)
			}
			return nil, err
		}
	}
	if err := s.verifyStaged(s.abs(staged), up); err != nil {
		if CodeOf(err) != "" {
			s.discardUpload(up.ID)
		}
		return nil, err
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	res := &Result{Path: up.Path}
	evs, head, err := s.write(m, func(o *op) error {
		if _, err := loadUpload(o.tx, up.ID, m.ID); err != nil {
			return err // committed concurrently
		}
		if _, err := o.tx.Exec(`DELETE FROM uploads WHERE id = ?`, up.ID); err != nil {
			return err
		}
		cur, missing, err := resolve(o.tx, up.Path, "")
		if err != nil {
			return err
		}
		switch {
		case cur != nil && cur.Kind == "dir":
			return errf(CodeKindMismatch, "%q is a directory", up.Path)

		case cur != nil && cur.Hash == up.NewHash: // rule 1
			res.Status = StatusUnchanged
			o.step("remove", "", staged)
			return nil

		case cur == nil: // new file, or rule 4
			for _, d := range missing {
				if err := o.mkdir(d); err != nil {
					return err
				}
			}
			e, err := o.event(Event{Op: OpPut, Path: up.Path, Hash: up.NewHash, Size: up.Size, Mtime: up.Mtime})
			if err != nil {
				return err
			}
			if _, err := o.tx.Exec(`INSERT INTO files (path, fold_key, kind, hash, size, mtime, seq, updated_by)
				VALUES (?, ?, 'file', ?, ?, ?, ?, ?)`, up.Path, pathx.FoldKey(up.Path), up.NewHash, up.Size, up.Mtime, e.Seq, m.Username); err != nil {
				return err
			}
			o.step("rename", staged, storagePath(up.Path))
			res.Status = StatusApplied
			return nil

		case cur.Hash == up.BaseHash: // rule 2
			tid, err := o.toTrash(cur, pathx.Replaced)
			if err != nil {
				return err
			}
			e, err := o.event(Event{Op: OpPut, Path: up.Path, Hash: up.NewHash, PrevHash: cur.Hash, TrashID: &tid, Size: up.Size, Mtime: up.Mtime})
			if err != nil {
				return err
			}
			if _, err := o.tx.Exec(`UPDATE files SET hash = ?, size = ?, mtime = ?, seq = ?, updated_by = ? WHERE path = ?`,
				up.NewHash, up.Size, up.Mtime, e.Seq, m.Username, up.Path); err != nil {
				return err
			}
			o.step("rename", staged, storagePath(up.Path))
			res.Status = StatusApplied
			return nil

		default: // rule 3
			cp, err := o.conflictPath(up.Path)
			if err != nil {
				return err
			}
			e, err := o.event(Event{Op: OpConflict, Path: cp, OldPath: up.Path, Hash: up.NewHash, PrevHash: cur.Hash, Size: up.Size, Mtime: up.Mtime})
			if err != nil {
				return err
			}
			if _, err := o.tx.Exec(`INSERT INTO files (path, fold_key, kind, hash, size, mtime, seq, updated_by)
				VALUES (?, ?, 'file', ?, ?, ?, ?, ?)`, cp, pathx.FoldKey(cp), up.NewHash, up.Size, up.Mtime, e.Seq, m.Username); err != nil {
				return err
			}
			o.step("rename", staged, storagePath(cp))
			res.Status, res.ConflictPath = StatusConflict, cp
			return nil
		}
	})
	if err != nil {
		if CodeOf(err) != "" && CodeOf(err) != CodeUnknownUpload {
			s.discardUpload(up.ID)
		}
		return nil, err
	}
	res.Events, res.Head = nonNil(evs), head
	return res, nil
}

// verifyStaged checks size and hash of the received content and makes it
// durable with the announced mtime.
func (s *Share) verifyStaged(file string, up *upload) error {
	fi, err := os.Lstat(file)
	if err != nil {
		return err
	}
	if !fi.Mode().IsRegular() {
		return errf(CodeInvalidRequest, "upload %s is not a regular file", up.ID)
	}
	if fi.Size() != up.Size {
		return errf(CodeSizeMismatch, "received %d bytes, announced %d", fi.Size(), up.Size)
	}
	h, err := hashx.File(file)
	if err != nil {
		return err
	}
	if h != up.NewHash {
		return errf(CodeHashMismatch, "received content has hash %s, announced %s", h, up.NewHash)
	}
	f, err := os.OpenFile(file, os.O_RDWR, 0)
	if err != nil {
		return err
	}
	err = f.Sync()
	f.Close()
	if err != nil {
		return err
	}
	if up.Mtime > 0 {
		t := time.Unix(0, up.Mtime)
		if err := os.Chtimes(file, t, t); err != nil {
			return err
		}
	}
	return nil
}

// ---- ops -----------------------------------------------------------------

// OpRequest is one delete, move, mkdir or rmdir.
type OpRequest struct {
	Op       string `json:"op"`
	Path     string `json:"path"`
	NewPath  string `json:"new_path,omitempty"`  // move: destination
	BaseHash string `json:"base_hash,omitempty"` // delete, move: the version the client had
}

// Op applies one operation under the rules:
//
//   - delete: base_hash must be the current content, else stale_base and
//     nothing changes (rule 5). The file goes to trash/ as
//     name.deleted-by-{user}-{ts} with a trash row keeping the original
//     path; event delete (rule 6). Deleting a path that is already gone
//     changes nothing.
//   - move: base_hash must be the current content of path; no content is
//     transferred. A destination with other content is dest_exists; one
//     with the same content makes the move a delete of the source (rule 7).
//   - mkdir / rmdir: directories are events and rows of files, so empty ones
//     exist; rmdir succeeds only on an empty directory (rules 6, 8).
func (s *Share) Op(m *Member, req OpRequest) (*Result, error) {
	p, err := normPath(req.Path)
	if err != nil {
		return nil, err
	}
	var fn func(o *op, res *Result) error
	switch req.Op {
	case OpDelete:
		if err := checkHash("base_hash", req.BaseHash, true); err != nil {
			return nil, err
		}
		fn = func(o *op, res *Result) error { return o.deleteFile(p, req.BaseHash, res) }
	case OpMove:
		if err := checkHash("base_hash", req.BaseHash, false); err != nil {
			return nil, err
		}
		to, err := normPath(req.NewPath)
		if err != nil {
			return nil, err
		}
		if to == p {
			return nil, errf(CodeInvalidRequest, "move to the same path")
		}
		fn = func(o *op, res *Result) error { return o.move(p, to, req.BaseHash, res) }
	case OpMkdir:
		fn = func(o *op, res *Result) error { return o.mkdirOp(p, res) }
	case OpRmdir:
		fn = func(o *op, res *Result) error { return o.rmdir(p, res) }
	default:
		return nil, errf(CodeInvalidRequest, "unknown op %q (want delete, move, mkdir or rmdir)", req.Op)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	res := &Result{Path: p, Status: StatusApplied}
	evs, head, err := s.write(m, func(o *op) error { return fn(o, res) })
	if err != nil {
		return nil, err
	}
	if len(evs) == 0 {
		res.Status = StatusUnchanged
	}
	res.Events, res.Head = nonNil(evs), head
	return res, nil
}

func (o *op) deleteFile(p, base string, res *Result) error {
	cur, _, err := resolve(o.tx, p, "")
	if err != nil {
		return err
	}
	if cur == nil {
		return nil // already gone
	}
	if cur.Kind == "dir" {
		return errf(CodeIsDir, "%q is a directory; delete its files, then rmdir", p)
	}
	if cur.Hash != base {
		return errf(CodeStaleBase, "%q has changed (current %s, base %s)", p, cur.Hash, base)
	}
	return o.trashAndForget(cur)
}

// trashAndForget sends the current version of a file to the trash and
// records the delete.
func (o *op) trashAndForget(cur *fileRow) error {
	tid, err := o.toTrash(cur, pathx.Deleted)
	if err != nil {
		return err
	}
	if _, err := o.event(Event{Op: OpDelete, Path: cur.Path, PrevHash: cur.Hash, TrashID: &tid}); err != nil {
		return err
	}
	_, err = o.tx.Exec(`DELETE FROM files WHERE path = ?`, cur.Path)
	return err
}

func (o *op) move(from, to, base string, res *Result) error {
	src, _, err := resolve(o.tx, from, "")
	if err != nil {
		return err
	}
	if src == nil {
		return errf(CodeNotFound, "%q does not exist", from)
	}
	if src.Kind == "dir" {
		return errf(CodeIsDir, "%q is a directory; only files are moved", from)
	}
	if src.Hash != base {
		return errf(CodeStaleBase, "%q has changed (current %s, base %s)", from, src.Hash, base)
	}
	dst, missing, err := resolve(o.tx, to, from)
	if err != nil {
		return err
	}
	res.Path = to
	if dst != nil {
		if dst.Kind == "file" && dst.Hash == src.Hash {
			return o.trashAndForget(src) // destination already has this content
		}
		return errf(CodeDestExists, "%q exists with other content", to)
	}
	for _, d := range missing {
		if err := o.mkdir(d); err != nil {
			return err
		}
	}
	e, err := o.event(Event{Op: OpMove, Path: to, OldPath: from, Hash: src.Hash, Size: src.Size, Mtime: src.Mtime})
	if err != nil {
		return err
	}
	if _, err := o.tx.Exec(`UPDATE files SET path = ?, fold_key = ?, seq = ?, updated_by = ? WHERE path = ?`,
		to, pathx.FoldKey(to), e.Seq, o.m.Username, from); err != nil {
		return err
	}
	if pathx.FoldKey(from) == pathx.FoldKey(to) {
		// Only the case changes: go through tmp/ so it also works on a
		// case-insensitive disk, where the target "exists" already.
		via := TmpDir + "/move-" + fmt.Sprint(e.Seq)
		o.step("rename", storagePath(from), via)
		o.step("rename", via, storagePath(to))
	} else {
		o.step("rename", storagePath(from), storagePath(to))
	}
	return nil
}

func (o *op) mkdirOp(p string, res *Result) error {
	cur, missing, err := resolve(o.tx, p, "")
	if err != nil {
		return err
	}
	if cur != nil {
		if cur.Kind != "dir" {
			return errf(CodeKindMismatch, "%q is a file", p)
		}
		return nil
	}
	for _, d := range append(missing, p) {
		if err := o.mkdir(d); err != nil {
			return err
		}
	}
	return nil
}

func (o *op) rmdir(p string, res *Result) error {
	cur, _, err := resolve(o.tx, p, "")
	if err != nil {
		return err
	}
	if cur == nil {
		return nil
	}
	if cur.Kind != "dir" {
		return errf(CodeKindMismatch, "%q is a file; use delete", p)
	}
	// Children of p are exactly the paths in ["p/", "p0"): '0' follows '/'.
	var n int
	if err := o.tx.QueryRow(`SELECT count(*) FROM files WHERE path >= ? AND path < ?`, p+"/", p+"0").Scan(&n); err != nil {
		return err
	}
	if n > 0 {
		return errf(CodeDirNotEmpty, "%q has %d entries", p, n)
	}
	if _, err := o.event(Event{Op: OpRmdir, Path: p}); err != nil {
		return err
	}
	if _, err := o.tx.Exec(`DELETE FROM files WHERE path = ?`, p); err != nil {
		return err
	}
	o.step("rmdir", "", storagePath(p))
	return nil
}

// ---- sync report ---------------------------------------------------------

// SyncReport is a client's account of one sync cycle.
type SyncReport struct {
	StartedAt  time.Time `json:"started_at"`
	FinishedAt time.Time `json:"finished_at"`
	FromSeq    int64     `json:"from_seq"`
	ToSeq      int64     `json:"to_seq"`
	Sent       int64     `json:"sent"`
	Received   int64     `json:"received"`
	Conflicts  int64     `json:"conflicts"`
	Error      string    `json:"error,omitempty"`
}

// ReportSync stores a sync report in sync_log and as one line of
// service.log. A cycle without error also acknowledges to_seq.
func (s *Share) ReportSync(m *Member, r SyncReport) error {
	if r.FromSeq < 0 || r.ToSeq < r.FromSeq || r.Sent < 0 || r.Received < 0 || r.Conflicts < 0 {
		return errf(CodeInvalidRequest, "inconsistent sync report")
	}
	if r.StartedAt.IsZero() || r.FinishedAt.Before(r.StartedAt) {
		return errf(CodeInvalidRequest, "started_at and finished_at are required, in order")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`INSERT INTO sync_log (member, started_at, finished_at, from_seq, to_seq, sent, received, conflicts, error)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`, m.Username, fmtTime(r.StartedAt), fmtTime(r.FinishedAt),
		r.FromSeq, r.ToSeq, r.Sent, r.Received, r.Conflicts, r.Error); err != nil {
		return err
	}
	if r.Error == "" {
		if _, err := tx.Exec(`UPDATE members SET last_seq_acked = max(last_seq_acked, ?) WHERE id = ?`, r.ToSeq, m.ID); err != nil {
			return err
		}
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	line := fmt.Sprintf("%s sync member=%s from=%d to=%d sent=%d received=%d conflicts=%d took=%s",
		fmtTime(s.now()), m.Username, r.FromSeq, r.ToSeq, r.Sent, r.Received, r.Conflicts,
		r.FinishedAt.Sub(r.StartedAt).Round(time.Millisecond))
	if r.Error != "" {
		line += " error=" + fmt.Sprintf("%q", r.Error)
	}
	return s.writeLog([]byte(line + "\n"))
}

func nonNil(evs []Event) []Event {
	if evs == nil {
		return []Event{}
	}
	return evs
}
