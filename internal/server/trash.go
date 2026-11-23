package server

import (
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/digital-cube/shared-folder/internal/pathx"
)

// Admin operations on the trash (docs/specifikacija.md, sections 3 and 4;
// docs/admin.md). Every one of them requires a member with the admin role
// and the changing ones run through the folder's single writer.

// RequireAdmin fails with CodeForbidden unless m is an active admin.
func RequireAdmin(m *Member) error {
	if m == nil || m.Role != RoleAdmin {
		return errf(CodeForbidden, "this operation needs the admin role")
	}
	if m.RevokedAt != nil {
		return errf(CodeUnauthorized, "member %s is revoked", m.Username)
	}
	return nil
}

// DefaultAdmin returns the oldest active admin of the folder. The shfd
// commands act in its name unless told otherwise (--as).
func (s *Share) DefaultAdmin() (*Member, error) {
	m, err := scanMember(s.db.QueryRow(`SELECT ` + memberCols + ` FROM members
		WHERE role = 'admin' AND revoked_at IS NULL ORDER BY id LIMIT 1`))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, errf(CodeUnknownMember, "the folder has no active admin")
	}
	return m, err
}

// MemberByName returns a member, revoked or not.
func (s *Share) MemberByName(username string) (*Member, error) {
	m, err := scanMember(s.db.QueryRow(`SELECT `+memberCols+` FROM members WHERE username = ?`, username))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, errf(CodeUnknownMember, "no member %q", username)
	}
	return m, err
}

// TrashEntry is a trash row that has not been purged.
type TrashEntry struct {
	ID        int64     `json:"id"`
	OrigPath  string    `json:"orig_path"`
	TrashName string    `json:"trash_name"` // file name inside trash/
	Hash      string    `json:"hash"`
	Size      int64     `json:"size"`
	Reason    string    `json:"reason"` // "deleted" or "replaced"
	By        string    `json:"by"`     // member whose operation sent it there
	At        time.Time `json:"at"`
	EventSeq  int64     `json:"event_seq"`
}

// TrashFilter narrows ListTrash. Zero fields do not filter.
type TrashFilter struct {
	PathPrefix string    // orig_path starts with this string
	By         string    // username
	Reason     string    // "deleted" or "replaced"
	Since      time.Time // at >= Since
}

// TrashList is a list of trash entries with their count and total size.
type TrashList struct {
	Entries   []TrashEntry `json:"entries"`
	Count     int          `json:"count"`
	TotalSize int64        `json:"total_size"`
}

const trashCols = `id, orig_path, trash_name, hash, size, reason, by_member, at, event_seq`

func scanTrash(sc interface{ Scan(...any) error }) (TrashEntry, error) {
	var e TrashEntry
	var at sql.NullString
	err := sc.Scan(&e.ID, &e.OrigPath, &e.TrashName, &e.Hash, &e.Size, &e.Reason, &e.By, &at, &e.EventSeq)
	if t := parseTime(at); t != nil {
		e.At = *t
	}
	return e, err
}

func queryTrash(q interface {
	Query(string, ...any) (*sql.Rows, error)
}, where string, args ...any) ([]TrashEntry, error) {
	rows, err := q.Query(`SELECT `+trashCols+` FROM trash WHERE purged_at IS NULL`+where+` ORDER BY id`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []TrashEntry{}
	for rows.Next() {
		e, err := scanTrash(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

func newTrashList(es []TrashEntry) *TrashList {
	l := &TrashList{Entries: es, Count: len(es)}
	for _, e := range es {
		l.TotalSize += e.Size
	}
	return l
}

// ListTrash lists the entries of the trash, oldest first.
func (s *Share) ListTrash(m *Member, f TrashFilter) (*TrashList, error) {
	if err := RequireAdmin(m); err != nil {
		return nil, err
	}
	var where strings.Builder
	var args []any
	if f.PathPrefix != "" {
		where.WriteString(` AND substr(orig_path, 1, ?) = ?`)
		args = append(args, len(f.PathPrefix), f.PathPrefix)
	}
	if f.By != "" {
		where.WriteString(` AND by_member = ?`)
		args = append(args, f.By)
	}
	if f.Reason != "" {
		if f.Reason != string(pathx.Deleted) && f.Reason != string(pathx.Replaced) {
			return nil, errf(CodeInvalidRequest, "reason must be deleted or replaced, not %q", f.Reason)
		}
		where.WriteString(` AND reason = ?`)
		args = append(args, f.Reason)
	}
	es, err := queryTrash(s.db, where.String(), args...)
	if err != nil {
		return nil, err
	}
	if !f.Since.IsZero() {
		// at is RFC 3339 text with a variable number of fraction digits,
		// so it is compared as time, not as a string.
		kept := es[:0]
		for _, e := range es {
			if !e.At.Before(f.Since) {
				kept = append(kept, e)
			}
		}
		es = kept
	}
	return newTrashList(es), nil
}

func loadTrash(q querier, id int64) (*TrashEntry, error) {
	e, err := scanTrash(q.QueryRow(`SELECT `+trashCols+` FROM trash WHERE id = ? AND purged_at IS NULL`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, errf(CodeUnknownTrash, "no trash entry %d", id)
	}
	if err != nil {
		return nil, err
	}
	return &e, nil
}

// RestoreResult is what RestoreTrash did.
type RestoreResult struct {
	TrashID int64  `json:"trash_id"`
	Status  string `json:"status"` // "applied", or "unchanged" when the path already has this content
	// Path is where the file is now: the requested path, or its
	// name.restored-{ts}.ext when that path holds other content.
	Path string `json:"path"`
	// Renamed says the requested path was taken and Path is the
	// ".restored-" name; the existing file was not touched.
	Renamed bool    `json:"renamed"`
	Events  []Event `json:"events"`
	Head    int64   `json:"head"`
}

// RestoreTrash puts a copy of trash entry id back into the folder: at its
// original path, or at to when that is not empty.
//
//   - A free path gets the file, after a mkdir for every missing parent.
//   - A path that already has the same content changes nothing.
//   - A path with other content (or a directory) keeps it; the restored
//     file is written as name.restored-{ts}.ext next to it.
//
// The entry stays in the trash (restoring copies), and the change log gets
// a restore event, so every client fetches the file like any other change.
func (s *Share) RestoreTrash(m *Member, id int64, to string) (*RestoreResult, error) {
	if err := RequireAdmin(m); err != nil {
		return nil, err
	}
	if to != "" {
		var err error
		if to, err = normPath(to); err != nil {
			return nil, err
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	e, err := loadTrash(s.db, id)
	if err != nil {
		return nil, err
	}
	dest := e.OrigPath
	if to != "" {
		dest = to
	}
	staged, mtime, err := s.stageTrashCopy(e)
	if err != nil {
		return nil, err
	}
	res := &RestoreResult{TrashID: id, Path: dest}
	evs, head, err := s.writeAdmin(m, func(o *op) error {
		cur, missing, err := resolve(o.tx, dest, "")
		taken := cur != nil && (cur.Kind == "dir" || cur.Hash != e.Hash)
		if CodeOf(err) == CodeCaseConflict {
			// The name, or one of its parents, differs from an existing
			// one only in letter case. Next to it, under a restored
			// name, only works when the parents are fine.
			taken, err = true, nil
		}
		if err != nil {
			return err
		}
		if cur != nil && !taken {
			res.Status = StatusUnchanged
			o.step("remove", "", staged)
			return nil
		}
		if taken {
			rp, err := pathx.RestoredPath(dest, o.now)
			if err != nil {
				return errf(CodeInvalidPath, "%v", err)
			}
			if rp, err = o.freeDerived(rp); err != nil {
				return err
			}
			if cur, missing, err = resolve(o.tx, rp, ""); err != nil {
				return err
			}
			if cur != nil { // freeDerived checked the name; this is a race we cannot have under mu
				return fmt.Errorf("restored name %q is taken", rp)
			}
			dest, res.Renamed = rp, true
		}
		for _, d := range missing {
			if err := o.mkdir(d); err != nil {
				return err
			}
		}
		ev := Event{Op: OpRestore, Path: dest, Hash: e.Hash, Size: e.Size, Mtime: mtime, TrashID: &e.ID}
		if dest != e.OrigPath {
			ev.OldPath = e.OrigPath
		}
		ev, err = o.event(ev)
		if err != nil {
			return err
		}
		if _, err := o.tx.Exec(`INSERT INTO files (path, fold_key, kind, hash, size, mtime, seq, updated_by)
			VALUES (?, ?, 'file', ?, ?, ?, ?, ?)`, dest, pathx.FoldKey(dest), e.Hash, e.Size, mtime, ev.Seq, m.Username); err != nil {
			return err
		}
		o.step("rename", staged, storagePath(dest))
		res.Status = StatusApplied
		return nil
	})
	if err != nil {
		os.Remove(s.abs(staged))
		return nil, err
	}
	res.Path, res.Events, res.Head = dest, nonNil(evs), head
	return res, nil
}

// stageTrashCopy copies the file of a trash entry to tmp/restore-<random>,
// checking that it still has the entry's content, with its mtime. It
// returns the share-relative name of the copy and that mtime (Unix ns).
// Caller holds s.mu.
func (s *Share) stageTrashCopy(e *TrashEntry) (string, int64, error) {
	src := s.abs(TrashDir + "/" + e.TrashName)
	in, err := os.Open(src)
	if errors.Is(err, os.ErrNotExist) {
		return "", 0, errf(CodeTrashDamaged, "the file of trash entry %d (%s) is missing; run shfd verify", e.ID, e.TrashName)
	}
	if err != nil {
		return "", 0, err
	}
	defer in.Close()
	fi, err := in.Stat()
	if err != nil {
		return "", 0, err
	}
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", 0, err
	}
	staged := TmpDir + "/restore-" + hex.EncodeToString(b[:])
	out, err := os.OpenFile(s.abs(staged), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return "", 0, err
	}
	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(out, h), in)
	if err == nil {
		err = out.Sync()
	}
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	if err == nil && (n != e.Size || hex.EncodeToString(h.Sum(nil)) != e.Hash) {
		err = errf(CodeTrashDamaged, "the file of trash entry %d (%s) does not have the recorded content; run shfd verify", e.ID, e.TrashName)
	}
	mtime := fi.ModTime()
	if err == nil {
		err = os.Chtimes(s.abs(staged), mtime, mtime)
	}
	if err != nil {
		os.Remove(s.abs(staged))
		return "", 0, err
	}
	return staged, mtime.UnixNano(), nil
}

// PurgeRequest selects trash entries to delete for good. Exactly one of
// OlderThan, IDs and All is set.
type PurgeRequest struct {
	// OlderThan is an age: a Go duration ("36h") or a whole number of
	// days or weeks ("30d", "2w"). Entries that went to the trash before
	// now minus that age are selected.
	OlderThan string  `json:"older_than,omitempty"`
	IDs       []int64 `json:"ids,omitempty"`
	All       bool    `json:"all,omitempty"`
	// DryRun only lists what would be purged.
	DryRun bool `json:"dry_run,omitempty"`
}

// PurgeResult lists the purged (or, with DryRun, the selected) entries.
type PurgeResult struct {
	DryRun bool `json:"dry_run"`
	TrashList
	Events []Event `json:"events"`
	Head   int64   `json:"head"`
}

// ParseAge reads the age of --older-than: a Go duration, or a whole number
// followed by d (days) or w (weeks).
func ParseAge(s string) (time.Duration, error) {
	s = strings.TrimSpace(s)
	for suffix, unit := range map[string]time.Duration{"d": 24 * time.Hour, "w": 7 * 24 * time.Hour} {
		if n, ok := strings.CutSuffix(s, suffix); ok {
			v, err := strconv.ParseUint(n, 10, 32)
			if err != nil {
				return 0, fmt.Errorf("age %q: want a duration such as 36h, 30d or 2w", s)
			}
			return time.Duration(v) * unit, nil
		}
	}
	d, err := time.ParseDuration(s)
	if err != nil || d < 0 {
		return 0, fmt.Errorf("age %q: want a duration such as 36h, 30d or 2w", s)
	}
	return d, nil
}

// PurgeTrash deletes trash entries for good: their files leave trash/, and
// for each one a purge event records what went (orig path, hash, size) and
// who ran it. This is the only operation that removes content
// irreversibly. The trash row stays, marked purged, because events refer
// to it; lists, restore and verify no longer see it.
func (s *Share) PurgeTrash(m *Member, req PurgeRequest) (*PurgeResult, error) {
	if err := RequireAdmin(m); err != nil {
		return nil, err
	}
	n := 0
	if req.OlderThan != "" {
		n++
	}
	if len(req.IDs) > 0 {
		n++
	}
	if req.All {
		n++
	}
	if n != 1 {
		return nil, errf(CodeInvalidRequest, "choose exactly one of older_than, ids and all")
	}
	var age time.Duration
	if req.OlderThan != "" {
		var err error
		if age, err = ParseAge(req.OlderThan); err != nil {
			return nil, errf(CodeInvalidRequest, "%v", err)
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	res := &PurgeResult{DryRun: req.DryRun}
	evs, head, err := s.writeAdmin(m, func(o *op) error {
		var sel []TrashEntry
		switch {
		case req.All:
			all, err := queryTrash(o.tx, "")
			if err != nil {
				return err
			}
			sel = all
		case len(req.IDs) > 0:
			seen := map[int64]bool{}
			for _, id := range req.IDs {
				if seen[id] {
					continue
				}
				seen[id] = true
				e, err := loadTrash(o.tx, id)
				if err != nil {
					return err
				}
				sel = append(sel, *e)
			}
			sort.Slice(sel, func(i, j int) bool { return sel[i].ID < sel[j].ID })
		default:
			all, err := queryTrash(o.tx, "")
			if err != nil {
				return err
			}
			cut := o.now.Add(-age)
			for _, e := range all {
				if e.At.Before(cut) {
					sel = append(sel, e)
				}
			}
		}
		res.TrashList = *newTrashList(nonNilTrash(sel))
		if req.DryRun {
			return nil
		}
		for _, e := range sel {
			if err := o.purge(e); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	res.Events, res.Head = nonNil(evs), head
	return res, nil
}

// purge records the purge of one entry and queues the removal of its file.
func (o *op) purge(e TrashEntry) error {
	id := e.ID
	ev, err := o.event(Event{Op: OpPurge, Path: e.OrigPath, Hash: e.Hash, Size: e.Size, TrashID: &id})
	if err != nil {
		return err
	}
	if _, err := o.tx.Exec(`UPDATE trash SET purged_at = ?, purged_by = ?, purge_seq = ? WHERE id = ?`,
		fmtTime(o.now), o.m.Username, ev.Seq, e.ID); err != nil {
		return err
	}
	o.step("remove", "", TrashDir+"/"+e.TrashName)
	return nil
}

func nonNilTrash(es []TrashEntry) []TrashEntry {
	if es == nil {
		return []TrashEntry{}
	}
	return es
}
