package server

import (
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/digital-cube/shared-folder/internal/hashx"
	"github.com/digital-cube/shared-folder/internal/pathx"
)

// Kinds of a verify finding (docs/admin.md).
const (
	IssueUntracked = "untracked" // on disk, not in the database
	IssueMissing   = "missing"   // in the database, not on disk
	IssueMismatch  = "mismatch"  // hash or size differ from the database
	IssueStaleTmp  = "stale_tmp" // a leftover in tmp/ older than StaleTmpAge, of no open upload
)

// Areas of a verify finding.
const (
	AreaStorage = "storage"
	AreaTrash   = "trash"
	AreaTmp     = "tmp"
)

// StaleTmpAge is how old a leftover in tmp/ must be before verify reports it.
const StaleTmpAge = 24 * time.Hour

// VerifyIssue is one difference between the disk and the database.
type VerifyIssue struct {
	Kind   string `json:"kind"`
	Area   string `json:"area"`
	Path   string `json:"path"` // folder path (storage), trash name (trash) or name in tmp/
	Detail string `json:"detail,omitempty"`
	// Repaired says --repair fixed it; Seq is the event that recorded the
	// repair, when there is one.
	Repaired bool  `json:"repaired"`
	Seq      int64 `json:"seq,omitempty"`
	// Warning says what a repair could not save (lost content).
	Warning string `json:"warning,omitempty"`
}

// VerifyResult is the report of Verify.
type VerifyResult struct {
	Repair bool          `json:"repair"`
	Issues []VerifyIssue `json:"issues"`
	// Checked counts what was compared.
	Checked struct {
		Files int `json:"files"`
		Dirs  int `json:"dirs"`
		Trash int `json:"trash"`
		Tmp   int `json:"tmp"`
	} `json:"checked"`
	Events []Event `json:"events"`
	Head   int64   `json:"head"`
}

// Unrepaired counts the issues that are still there.
func (r *VerifyResult) Unrepaired() int {
	n := 0
	for _, is := range r.Issues {
		if !is.Repaired {
			n++
		}
	}
	return n
}

type diskEntry struct {
	kind  string // "file", "dir" or "other"
	size  int64
	mtime int64
	hash  string // files only
}

// Verify compares storage/ and trash/ with the tables files and trash, and
// looks for leftovers in tmp/. It reports:
//
//   - untracked: a file or directory on disk the database does not know;
//   - missing: a row whose file or directory is not on disk;
//   - mismatch: a file whose hash or size differ from its row;
//   - stale_tmp: an entry of tmp/ older than 24 h that belongs to no open
//     upload.
//
// With repair, in one write and through the change log so clients see it:
// an untracked file becomes a put (a directory a mkdir) in the name of m;
// a missing row becomes a delete (rmdir), without a trash entry, with a
// warning that the content is lost; a mismatched file becomes a new
// version (put), the old one cannot be kept; stale tmp/ entries are
// removed. In trash/, an untracked file gets a trash row (original name and
// reason read from its name when possible), a missing one is purged and a
// mismatched row takes the hash and size on disk. Names a client could not
// receive (invalid or clashing in letter case), symlinks and other special
// files are reported and left alone.
func (s *Share) Verify(m *Member, repair bool) (*VerifyResult, error) {
	if err := RequireAdmin(m); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.replayJournal(); err != nil {
		return nil, fmt.Errorf("finishing earlier disk steps: %w", err)
	}
	res := &VerifyResult{Repair: repair, Issues: []VerifyIssue{}}

	disk, err := s.scanStorage()
	if err != nil {
		return nil, err
	}
	rows, err := s.fileRows()
	if err != nil {
		return nil, err
	}
	var missing, mismatch, untracked []string
	for p, r := range rows {
		if r.Kind == "dir" {
			res.Checked.Dirs++
		} else {
			res.Checked.Files++
		}
		d, ok := disk[p]
		switch {
		case !ok || d.kind != r.Kind:
			missing = append(missing, p)
		case r.Kind == "file" && (d.size != r.Size || d.hash != r.Hash):
			mismatch = append(mismatch, p)
		}
	}
	for p, d := range disk {
		if r, ok := rows[p]; !ok || r.Kind != d.kind {
			untracked = append(untracked, p)
		}
	}

	trashDisk, err := s.scanTrash()
	if err != nil {
		return nil, err
	}
	trashRows, err := queryTrash(s.db, "")
	if err != nil {
		return nil, err
	}
	res.Checked.Trash = len(trashRows)
	var trashMissing, trashMismatch []TrashEntry
	known := map[string]bool{}
	for _, e := range trashRows {
		known[e.TrashName] = true
		d, ok := trashDisk[e.TrashName]
		switch {
		case !ok || d.kind != "file":
			trashMissing = append(trashMissing, e)
		case d.size != e.Size || d.hash != e.Hash:
			trashMismatch = append(trashMismatch, e)
		}
	}
	var trashUntracked []string
	for name := range trashDisk {
		if !known[name] {
			trashUntracked = append(trashUntracked, name)
		}
	}
	sort.Strings(trashUntracked)

	stale, nTmp, err := s.staleTmp()
	if err != nil {
		return nil, err
	}
	res.Checked.Tmp = nTmp

	// Deepest first, so files go before the directories that held them.
	sort.Slice(missing, func(i, j int) bool {
		di, dj := strings.Count(missing[i], "/"), strings.Count(missing[j], "/")
		if di != dj {
			return di > dj
		}
		return missing[i] < missing[j]
	})
	sort.Strings(mismatch)
	sort.Strings(untracked) // a parent sorts before its children

	issue := func(kind, area, p, detail string) *VerifyIssue {
		res.Issues = append(res.Issues, VerifyIssue{Kind: kind, Area: area, Path: p, Detail: detail})
		return &res.Issues[len(res.Issues)-1]
	}
	// Indexes into res.Issues, so repairs can mark them.
	type pending struct {
		i  int
		fn func(o *op, is *VerifyIssue) error
	}
	var todo []pending
	add := func(is *VerifyIssue, fn func(o *op, is *VerifyIssue) error) {
		if fn != nil {
			todo = append(todo, pending{len(res.Issues) - 1, fn})
		}
	}

	for _, p := range missing {
		r := rows[p]
		detail := fmt.Sprintf("%s in the database, not on disk", r.Kind)
		if d, ok := disk[p]; ok {
			detail = fmt.Sprintf("%s in the database, %s on disk", r.Kind, d.kind)
		}
		add(issue(IssueMissing, AreaStorage, p, detail), func(o *op, is *VerifyIssue) error {
			ev := Event{Op: OpRmdir, Path: p}
			if r.Kind == "file" {
				ev = Event{Op: OpDelete, Path: p, PrevHash: r.Hash}
				is.Warning = fmt.Sprintf("content %s (%d bytes) is lost; no trash entry", r.Hash, r.Size)
			}
			e, err := o.event(ev)
			if err != nil {
				return err
			}
			is.Seq = e.Seq
			_, err = o.tx.Exec(`DELETE FROM files WHERE path = ?`, p)
			return err
		})
	}
	for _, p := range mismatch {
		r, d := rows[p], disk[p]
		add(issue(IssueMismatch, AreaStorage, p, fmt.Sprintf("database %s (%d bytes), disk %s (%d bytes)", r.Hash, r.Size, d.hash, d.size)),
			func(o *op, is *VerifyIssue) error {
				e, err := o.event(Event{Op: OpPut, Path: p, Hash: d.hash, PrevHash: r.Hash, Size: d.size, Mtime: d.mtime})
				if err != nil {
					return err
				}
				is.Seq = e.Seq
				is.Warning = fmt.Sprintf("the recorded version %s could not be kept", r.Hash)
				_, err = o.tx.Exec(`UPDATE files SET hash = ?, size = ?, mtime = ?, seq = ?, updated_by = ? WHERE path = ?`,
					d.hash, d.size, d.mtime, e.Seq, o.m.Username, p)
				return err
			})
	}
	for _, p := range untracked {
		d := disk[p]
		if d.kind == "other" {
			issue(IssueUntracked, AreaStorage, p, "not a regular file or directory (symlink?); left alone")
			continue
		}
		if n, err := pathx.Normalize(p); err != nil || n != p {
			detail := "name is not allowed in a folder; left alone"
			if err != nil {
				detail = fmt.Sprintf("name is not allowed (%v); left alone", err)
			}
			issue(IssueUntracked, AreaStorage, p, detail)
			continue
		}
		detail := d.kind + " on disk, not in the database"
		if d.kind == "file" {
			detail = fmt.Sprintf("file %s (%d bytes) on disk, not in the database", d.hash, d.size)
		}
		add(issue(IssueUntracked, AreaStorage, p, detail), func(o *op, is *VerifyIssue) error {
			cur, parents, err := resolve(o.tx, p, "")
			var se *Error
			if errors.As(err, &se) {
				is.Detail += "; not repaired: " + se.Message
				return errSkip
			}
			if err != nil {
				return err
			}
			if cur != nil {
				return nil // a directory recorded by an earlier repair
			}
			for _, dir := range parents {
				if err := o.mkdir(dir); err != nil {
					return err
				}
			}
			if d.kind == "dir" {
				e, err := o.event(Event{Op: OpMkdir, Path: p})
				if err != nil {
					return err
				}
				is.Seq = e.Seq
				_, err = o.tx.Exec(`INSERT INTO files (path, fold_key, kind, seq, updated_by) VALUES (?, ?, 'dir', ?, ?)`,
					p, pathx.FoldKey(p), e.Seq, o.m.Username)
				return err
			}
			e, err := o.event(Event{Op: OpPut, Path: p, Hash: d.hash, Size: d.size, Mtime: d.mtime})
			if err != nil {
				return err
			}
			is.Seq = e.Seq
			_, err = o.tx.Exec(`INSERT INTO files (path, fold_key, kind, hash, size, mtime, seq, updated_by)
				VALUES (?, ?, 'file', ?, ?, ?, ?, ?)`, p, pathx.FoldKey(p), d.hash, d.size, d.mtime, e.Seq, o.m.Username)
			return err
		})
	}

	for _, e := range trashMissing {
		e := e
		add(issue(IssueMissing, AreaTrash, e.TrashName, fmt.Sprintf("trash entry %d (%s) has no file", e.ID, e.OrigPath)),
			func(o *op, is *VerifyIssue) error {
				if err := o.purge(e); err != nil {
					return err
				}
				is.Seq = o.head
				is.Warning = fmt.Sprintf("content %s (%d bytes) is lost; the entry is recorded as purged", e.Hash, e.Size)
				return nil
			})
	}
	for _, e := range trashMismatch {
		e, d := e, trashDisk[e.TrashName]
		add(issue(IssueMismatch, AreaTrash, e.TrashName, fmt.Sprintf("trash entry %d: database %s (%d bytes), disk %s (%d bytes)", e.ID, e.Hash, e.Size, d.hash, d.size)),
			func(o *op, is *VerifyIssue) error {
				is.Warning = fmt.Sprintf("the recorded version %s could not be kept", e.Hash)
				_, err := o.tx.Exec(`UPDATE trash SET hash = ?, size = ? WHERE id = ?`, d.hash, d.size, e.ID)
				return err
			})
	}
	for _, name := range trashUntracked {
		d := trashDisk[name]
		if d.kind != "file" {
			issue(IssueUntracked, AreaTrash, name, "not a regular file; left alone")
			continue
		}
		add(issue(IssueUntracked, AreaTrash, name, fmt.Sprintf("file %s (%d bytes) in trash/, not in the database", d.hash, d.size)),
			func(o *op, is *VerifyIssue) error { return o.adoptTrash(name, d, is) })
	}
	// tmp/ is not part of the folder's state: its leftovers are removed
	// after the write, without an event.
	var tmpRemove []int
	for _, st := range stale {
		issue(IssueStaleTmp, AreaTmp, st.name, fmt.Sprintf("last modified %s, belongs to no open upload", st.mtime.UTC().Format(time.RFC3339)))
		tmpRemove = append(tmpRemove, len(res.Issues)-1)
	}

	if repair && len(todo) > 0 {
		evs, head, err := s.writeAdmin(m, func(o *op) error {
			for _, t := range todo {
				is := &res.Issues[t.i]
				switch err := t.fn(o, is); err {
				case nil:
					is.Repaired = true
				case errSkip:
				default:
					return fmt.Errorf("%s %s %s: %w", is.Area, is.Kind, is.Path, err)
				}
			}
			return nil
		})
		if err != nil {
			return nil, err
		}
		res.Events = evs
		res.Head = head
	}
	if repair {
		for _, i := range tmpRemove {
			is := &res.Issues[i]
			if err := os.RemoveAll(s.abs(TmpDir + "/" + is.Path)); err != nil {
				is.Detail += "; not removed: " + err.Error()
				continue
			}
			is.Repaired = true
		}
		if err := s.logRepairs(m, res.Issues); err != nil {
			return nil, err
		}
	}
	if res.Head == 0 {
		if res.Head, err = s.Head(); err != nil {
			return nil, err
		}
	}
	res.Events = nonNil(res.Events)
	return res, nil
}

// errSkip leaves one repair undone without failing the others.
var errSkip = errors.New("skipped")

// logRepairs writes one service.log line per repair. Caller holds s.mu.
func (s *Share) logRepairs(m *Member, issues []VerifyIssue) error {
	var b strings.Builder
	for _, is := range issues {
		if !is.Repaired {
			continue
		}
		fmt.Fprintf(&b, "%s verify-repair member=%s area=%s kind=%s path=%s", fmtTime(s.now()), m.Username, is.Area, is.Kind, strconv.Quote(is.Path))
		if is.Seq > 0 {
			fmt.Fprintf(&b, " seq=%d", is.Seq)
		}
		if is.Warning != "" {
			fmt.Fprintf(&b, " warning=%s", strconv.Quote(is.Warning))
		}
		b.WriteByte('\n')
	}
	return s.writeLog([]byte(b.String()))
}

// trashNameRE takes apart the name TrashName gives: <name>.<reason>-by-<user>-<ts>[-N].
var trashNameRE = regexp.MustCompile(`^(.+)\.(deleted|replaced)-by-([a-z0-9_-]{1,32})-(\d{8}T\d{6}Z)(?:-\d+)?$`)

// adoptTrash records a file found in trash/ without a row. A name made by
// TrashName gives back the file name, reason, member and time; anything
// else is recorded as deleted by m now, under its own name. A name that
// belongs to a purged entry is the leftover of an interrupted purge and is
// removed instead.
func (o *op) adoptTrash(name string, d diskEntry, is *VerifyIssue) error {
	var purged sql.NullString
	err := o.tx.QueryRow(`SELECT purged_at FROM trash WHERE fold_key = ?`, pathx.FoldKey(name)).Scan(&purged)
	switch {
	case err == nil && purged.Valid && purged.String != "":
		is.Detail += "; it belongs to a purged entry and is removed"
		o.step("remove", "", TrashDir+"/"+name)
		return nil
	case err == nil:
		is.Detail += "; not repaired: its name differs from another entry's only in letter case"
		return errSkip
	case !errors.Is(err, sql.ErrNoRows):
		return err
	}
	orig, reason, by, at := name, string(pathx.Deleted), o.m.Username, o.now
	if mm := trashNameRE.FindStringSubmatch(name); mm != nil {
		if t, err := time.Parse(pathx.TimestampLayout, mm[4]); err == nil {
			orig, reason, by, at = mm[1], mm[2], mm[3], t
		}
	}
	if n, err := pathx.Normalize(orig); err != nil || n != orig {
		orig = "recovered-from-trash"
	}
	_, err = o.tx.Exec(`INSERT INTO trash (orig_path, trash_name, fold_key, hash, size, reason, by_member, at, event_seq)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, 0)`, orig, name, pathx.FoldKey(name), d.hash, d.size, reason, by, fmtTime(at))
	if err != nil {
		return err
	}
	is.Warning = fmt.Sprintf("recorded with original path %q (the directory is not known)", orig)
	return nil
}

// scanStorage walks storage/ and hashes every regular file.
func (s *Share) scanStorage() (map[string]diskEntry, error) {
	root := s.abs(StorageDir)
	out := map[string]diskEntry{}
	err := filepath.WalkDir(root, func(p string, de fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if p == root {
			return nil
		}
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return err
		}
		d, err := statEntry(p, de)
		if err != nil {
			return err
		}
		out[filepath.ToSlash(rel)] = d
		return nil
	})
	return out, err
}

// scanTrash reads the flat trash/ directory.
func (s *Share) scanTrash() (map[string]diskEntry, error) {
	dir := s.abs(TrashDir)
	des, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	out := map[string]diskEntry{}
	for _, de := range des {
		d, err := statEntry(filepath.Join(dir, de.Name()), de)
		if err != nil {
			return nil, err
		}
		out[de.Name()] = d
	}
	return out, nil
}

func statEntry(p string, de fs.DirEntry) (diskEntry, error) {
	fi, err := de.Info()
	if err != nil {
		return diskEntry{}, err
	}
	d := diskEntry{kind: "other", size: fi.Size(), mtime: fi.ModTime().UnixNano()}
	switch {
	case fi.IsDir():
		d.kind, d.size, d.mtime = "dir", 0, 0
	case fi.Mode().IsRegular():
		d.kind = "file"
		if d.hash, err = hashx.File(p); err != nil {
			return d, err
		}
	}
	return d, nil
}

func (s *Share) fileRows() (map[string]fileRow, error) {
	rows, err := s.db.Query(`SELECT path, kind, hash, size, mtime, seq, updated_by FROM files`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]fileRow{}
	for rows.Next() {
		var r fileRow
		if err := rows.Scan(&r.Path, &r.Kind, &r.Hash, &r.Size, &r.Mtime, &r.Seq, &r.UpdatedBy); err != nil {
			return nil, err
		}
		out[r.Path] = r
	}
	return out, rows.Err()
}

type tmpEntry struct {
	name  string
	mtime time.Time
}

var uploadIDInName = regexp.MustCompile(`[0-9a-f]{32}`)

// staleTmp lists the entries of tmp/ older than StaleTmpAge that belong to
// no open upload: their name carries no upload id that is still in the
// uploads table (rsync's partial files are named after the upload too).
func (s *Share) staleTmp() ([]tmpEntry, int, error) {
	des, err := os.ReadDir(s.abs(TmpDir))
	if err != nil {
		return nil, 0, err
	}
	cut := s.now().Add(-StaleTmpAge)
	var out []tmpEntry
	for _, de := range des {
		fi, err := de.Info()
		if err != nil {
			return nil, 0, err
		}
		if !fi.ModTime().Before(cut) {
			continue
		}
		open := false
		for _, id := range uploadIDInName.FindAllString(de.Name(), -1) {
			var n int
			if err := s.db.QueryRow(`SELECT count(*) FROM uploads WHERE id = ?`, id).Scan(&n); err != nil {
				return nil, 0, err
			}
			if n > 0 {
				open = true
			}
		}
		if !open {
			out = append(out, tmpEntry{name: de.Name(), mtime: fi.ModTime()})
		}
	}
	return out, len(des), nil
}
