package server

import (
	"bytes"
	"database/sql"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/digital-cube/shared-folder/internal/authkeys"
	"github.com/digital-cube/shared-folder/internal/pathx"
)

// Read-only views of a folder for the admin API (docs/admin.md, "Admin
// API"): the members with their status, service.log as parsed lines and
// one level of the files table. The web GUI (shfd-gui) is built on them.

// ---- whoami --------------------------------------------------------------

// Whoami finds the folder and the active member a token belongs to. A
// token is bound to one folder, so it is enough to sign in with. An empty,
// unknown or revoked token fails with CodeUnauthorized.
func (s *Server) Whoami(token string) (ShareInfo, *Member, *Share, error) {
	if token == "" {
		return ShareInfo{}, nil, nil, errf(CodeUnauthorized, "missing token")
	}
	shares, err := s.Shares()
	if err != nil {
		return ShareInfo{}, nil, nil, err
	}
	var last error = errf(CodeUnauthorized, "unknown token")
	for _, si := range shares {
		sh, err := s.Share(si.ID)
		if err != nil {
			return ShareInfo{}, nil, nil, err
		}
		m, err := sh.Authenticate(token)
		if err == nil {
			return si, m, sh, nil
		}
		if CodeOf(err) != CodeUnauthorized {
			return ShareInfo{}, nil, nil, err
		}
		if err.Error() != errf(CodeUnauthorized, "unknown token").Error() {
			last = err // revoked: say so, unless another folder knows the token
		}
	}
	return ShareInfo{}, nil, nil, last
}

// ---- members -------------------------------------------------------------

// Member status in MemberInfo.
const (
	StatusActive  = "active"
	StatusPending = "pending" // active, but no SSH key yet: shf join has not run
	StatusRevoked = "revoked"
)

// MemberInfo is one member as the admin API lists it: shfd member list
// plus the numbers of shfd stats. There is no token or token hash in it.
type MemberInfo struct {
	Username     string     `json:"username"`
	Role         string     `json:"role"`
	Status       string     `json:"status"`
	CreatedAt    time.Time  `json:"created_at"`
	RevokedAt    *time.Time `json:"revoked_at"`
	LastSeenAt   *time.Time `json:"last_seen_at"`
	LastPushAt   *time.Time `json:"last_push_at"`
	LastSeqAcked int64      `json:"last_seq_acked"`
	// Behind is HEAD minus LastSeqAcked.
	Behind int64 `json:"behind"`
	// SSHFingerprint is the SHA256 fingerprint of the member's one SSH
	// key, "" when none is registered.
	SSHFingerprint string `json:"ssh_fingerprint"`
	// Connected counts the member's open WebSockets to this process.
	Connected int `json:"connected"`
}

// MemberList is the body of GET /admin/members.
type MemberList struct {
	Head    int64        `json:"head"`
	Members []MemberInfo `json:"members"`
}

// MemberInfos lists every member, revoked ones included, oldest first.
func (s *Share) MemberInfos() (*MemberList, error) {
	var head int64
	if err := s.db.QueryRow(`SELECT coalesce(max(seq), 0) FROM events`).Scan(&head); err != nil {
		return nil, err
	}
	ms, err := s.Members()
	if err != nil {
		return nil, err
	}
	conn := s.connectedByMember()
	out := &MemberList{Head: head, Members: make([]MemberInfo, 0, len(ms))}
	for _, m := range ms {
		mi := MemberInfo{
			Username: m.Username, Role: m.Role, Status: StatusActive,
			CreatedAt: m.CreatedAt, RevokedAt: m.RevokedAt,
			LastSeenAt: m.LastSeenAt, LastPushAt: m.LastPushAt,
			LastSeqAcked: m.LastSeqAcked, Behind: max(head-m.LastSeqAcked, 0),
			Connected: conn[m.ID],
		}
		if m.SSHPubkey != "" {
			mi.SSHFingerprint = authkeys.Fingerprint(m.SSHPubkey)
		} else {
			mi.Status = StatusPending
		}
		if m.RevokedAt != nil {
			mi.Status, mi.Connected = StatusRevoked, 0
		}
		out.Members = append(out.Members, mi)
	}
	return out, nil
}

func (s *Share) connectedByMember() map[int64]int {
	s.watch.mu.Lock()
	defer s.watch.mu.Unlock()
	out := map[int64]int{}
	for w := range s.watch.set {
		out[w.MemberID]++
	}
	return out
}

// MemberInfoByName is one row of MemberInfos.
func (s *Share) MemberInfoByName(username string) (*MemberInfo, error) {
	l, err := s.MemberInfos()
	if err != nil {
		return nil, err
	}
	for i := range l.Members {
		if l.Members[i].Username == username {
			return &l.Members[i], nil
		}
	}
	return nil, errf(CodeUnknownMember, "no member %q", username)
}

// ---- files ---------------------------------------------------------------

// FileEntry is one row of the files table, for GET /admin/files.
type FileEntry struct {
	Path      string     `json:"path"`
	Name      string     `json:"name"`
	Kind      string     `json:"kind"` // "file" or "dir"
	Size      int64      `json:"size"`
	Mtime     *time.Time `json:"mtime"` // as sent by the uploading client; null for a directory
	Hash      string     `json:"hash"`
	Seq       int64      `json:"seq"`
	UpdatedBy string     `json:"updated_by"`
	// Thumb is the state of the file's thumbnail: "ready", "pending" (a
	// thumbnail service that makes this kind runs and it has none yet),
	// "failed", or "" (a directory, a type without thumbnails, an empty
	// file, or no service running). GET /admin/thumbs/{hash} sends it.
	Thumb string `json:"thumb"`
}

// DirListing is the body of GET /admin/files.
type DirListing struct {
	Dir     string      `json:"dir"` // "" for the root
	Entries []FileEntry `json:"entries"`
}

// ListDir lists one level of the folder as the database records it (the
// disk is not read): directories first, then by name. dir "" (or "/") is
// the root; a missing directory fails with CodeNotFound and a file with
// CodeKindMismatch.
func (s *Share) ListDir(dir string) (*DirListing, error) {
	dir = strings.Trim(dir, "/")
	if dir != "" {
		p, err := pathx.Normalize(dir)
		if err != nil {
			return nil, errf(CodeInvalidPath, "%v", err)
		}
		dir = p
		var kind string
		err = s.db.QueryRow(`SELECT kind FROM files WHERE path = ?`, dir).Scan(&kind)
		if errors.Is(err, sql.ErrNoRows) {
			return nil, errf(CodeNotFound, "no directory %q", dir)
		}
		if err != nil {
			return nil, err
		}
		if kind != "dir" {
			return nil, errf(CodeKindMismatch, "%q is a file, not a directory", dir)
		}
	}
	var rows *sql.Rows
	var err error
	const cols = `SELECT path, kind, size, mtime, hash, seq, updated_by FROM files `
	if dir == "" {
		rows, err = s.db.Query(cols + `WHERE instr(path, '/') = 0`)
	} else {
		prefix := dir + "/"
		rows, err = s.db.Query(cols+`WHERE substr(path, 1, ?) = ? AND instr(substr(path, ?), '/') = 0`,
			len(prefix), prefix, len(prefix)+1)
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := &DirListing{Dir: dir, Entries: []FileEntry{}}
	for rows.Next() {
		var e FileEntry
		var mtime int64
		if err := rows.Scan(&e.Path, &e.Kind, &e.Size, &mtime, &e.Hash, &e.Seq, &e.UpdatedBy); err != nil {
			return nil, err
		}
		e.Name = e.Path[strings.LastIndex(e.Path, "/")+1:]
		if mtime != 0 {
			t := time.Unix(0, mtime).UTC()
			e.Mtime = &t
		}
		out.Entries = append(out.Entries, e)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if err := s.setThumbStates(out.Entries); err != nil {
		return nil, err
	}
	sort.Slice(out.Entries, func(i, j int) bool {
		a, b := out.Entries[i], out.Entries[j]
		if (a.Kind == "dir") != (b.Kind == "dir") {
			return a.Kind == "dir"
		}
		if la, lb := strings.ToLower(a.Name), strings.ToLower(b.Name); la != lb {
			return la < lb
		}
		return a.Name < b.Name
	})
	return out, nil
}

// ---- service.log ---------------------------------------------------------

// Types of a LogLine.
const (
	LogEvent       = "event"
	LogTransfer    = "transfer"
	LogKeyReplaced = "key-replaced"
	LogSync        = "sync"  // a client's sync report (POST /sync-report)
	LogOther       = "other" // a line that could not be parsed; only raw is set
)

// LogLine is one parsed line of service.log.
type LogLine struct {
	// Offset is the byte offset of the line in service.log: stable, so a
	// client can use it as the line's id.
	Offset  int64     `json:"offset"`
	TS      time.Time `json:"ts"`
	Type    string    `json:"type"`
	Seq     int64     `json:"seq,omitempty"`
	Member  string    `json:"member"`
	Op      string    `json:"op"` // the event's op; "upload"/"download" for a transfer; "key-replaced"
	Path    string    `json:"path"`
	OldPath string    `json:"old_path,omitempty"`
	Dir     string    `json:"dir,omitempty"`     // transfer: upload, download or "-"
	Outcome string    `json:"outcome,omitempty"` // transfer: ok, denied, failed; sync: ok, failed
	Detail  string    `json:"detail,omitempty"`  // transfer: reason; key-replaced: "old=… new=…"; sync: the numbers and the error
	Raw     string    `json:"raw"`
}

// LogPage is the body of GET /admin/log.
type LogPage struct {
	// Offset is where the first returned line starts; NextOffset is where
	// the next call continues (after=NextOffset). Size is the length of
	// service.log when it was read.
	Offset     int64     `json:"offset"`
	NextOffset int64     `json:"next_offset"`
	Size       int64     `json:"size"`
	Lines      []LogLine `json:"lines"`
}

// Bounds of the limit of ReadLog.
const (
	DefaultLogLimit = 200
	MaxLogLimit     = 5000
)

// ReadLog reads complete lines of service.log. With after >= 0 it returns
// up to limit lines starting at that byte offset (a following call passes
// NextOffset); an offset past the end of the file, which only a replaced
// file has, starts again at 0, and one inside a line skips to the next.
// With after < 0 it returns the last limit lines. A line still being
// written (no newline yet) is left for the next call.
func (s *Share) ReadLog(after int64, limit int) (*LogPage, error) {
	if limit <= 0 {
		limit = DefaultLogLimit
	}
	limit = min(limit, MaxLogLimit)
	f, err := os.Open(filepath.Join(s.dir, logFile))
	if err != nil {
		return nil, err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return nil, err
	}
	size := st.Size()
	page := &LogPage{Size: size, Lines: []LogLine{}}
	if after < 0 {
		return page, readLogTail(f, size, limit, page)
	}
	if after > size {
		after = 0
	}
	if after > 0 {
		var b [1]byte
		if _, err := f.ReadAt(b[:], after-1); err != nil {
			return nil, err
		}
		if b[0] != '\n' {
			// Inside a line: continue after it.
			buf := make([]byte, 64<<10)
			n, err := f.ReadAt(buf, after)
			if err != nil && err != io.EOF {
				return nil, err
			}
			i := bytes.IndexByte(buf[:n], '\n')
			if i < 0 {
				page.Offset, page.NextOffset = after, after
				return page, nil
			}
			after += int64(i) + 1
		}
	}
	page.Offset, page.NextOffset = after, after
	const chunk = 256 << 10
	pos := after
	var carry []byte
	for len(page.Lines) < limit && pos < size {
		buf := make([]byte, min(chunk, size-pos))
		n, err := f.ReadAt(buf, pos)
		if err != nil && err != io.EOF {
			return nil, err
		}
		data := append(carry, buf[:n]...)
		start := pos - int64(len(carry))
		pos += int64(n)
		for len(page.Lines) < limit {
			i := bytes.IndexByte(data, '\n')
			if i < 0 {
				break
			}
			page.Lines = append(page.Lines, ParseLogLine(start, string(data[:i])))
			start += int64(i) + 1
			data = data[i+1:]
			page.NextOffset = start
		}
		carry = append([]byte(nil), data...)
		if n == 0 {
			break
		}
	}
	return page, nil
}

// readLogTail fills page with the last limit complete lines before size.
// It reads a growing window from the end of the file; when the window does
// not start at 0 its first line may be cut, so it is dropped, and the
// window grows until enough whole lines remain.
func readLogTail(f *os.File, size int64, limit int, page *LogPage) error {
	for window := int64(64 << 10); ; window *= 2 {
		from := max(size-window, 0)
		buf := make([]byte, size-from)
		if _, err := f.ReadAt(buf, from); err != nil && err != io.EOF {
			return err
		}
		last := bytes.LastIndexByte(buf, '\n')
		start := from
		if from > 0 && last >= 0 {
			i := bytes.IndexByte(buf, '\n')
			start += int64(i) + 1
			buf, last = buf[i+1:], last-(i+1)
		}
		if from > 0 && (last < 0 || bytes.Count(buf[:last+1], []byte{'\n'}) < limit) {
			continue
		}
		if last < 0 { // no complete line in the whole file
			page.Offset, page.NextOffset = 0, 0
			return nil
		}
		end := start + int64(last) + 1
		buf = buf[:last+1]
		var offs []int64
		var lines []string
		for len(buf) > 0 {
			i := bytes.IndexByte(buf, '\n')
			offs = append(offs, start)
			lines = append(lines, string(buf[:i]))
			start += int64(i) + 1
			buf = buf[i+1:]
		}
		if len(lines) > limit {
			offs, lines = offs[len(offs)-limit:], lines[len(lines)-limit:]
		}
		for i := range lines {
			page.Lines = append(page.Lines, ParseLogLine(offs[i], lines[i]))
		}
		page.Offset, page.NextOffset = offs[0], end
		return nil
	}
}

// ParseLogLine reads one line of service.log (without its newline). The
// three forms are those written by logEvents, LogTransfer and SetSSHKey:
//
//	<ts> seq=N member=M op=O path="P" [old_path="Q"]
//	<ts> transfer member=M dir=D path="P" outcome=X [detail="Y"]
//	<ts> key-replaced member=M old=FP new=FP
//	<ts> sync member=M from=N to=N sent=N received=N conflicts=N took=D [error="E"]
//
// Anything else is a line of type "other" with only Raw set.
func ParseLogLine(offset int64, raw string) LogLine {
	other := LogLine{Offset: offset, Type: LogOther, Raw: raw}
	tsStr, rest, ok := strings.Cut(raw, " ")
	if !ok {
		return other
	}
	ts, err := time.Parse(time.RFC3339Nano, tsStr)
	if err != nil {
		return other
	}
	l := LogLine{Offset: offset, TS: ts, Raw: raw}
	kind := ""
	if w, r, ok := strings.Cut(rest, " "); ok && !strings.Contains(w, "=") {
		kind, rest = w, r
	}
	kv, ok := parseKV(rest)
	if !ok {
		return other
	}
	l.Member = kv["member"]
	switch kind {
	case "":
		seq, err := strconv.ParseInt(kv["seq"], 10, 64)
		if err != nil || kv["op"] == "" {
			return other
		}
		l.Type, l.Seq, l.Op, l.Path, l.OldPath = LogEvent, seq, kv["op"], kv["path"], kv["old_path"]
	case "transfer":
		l.Type, l.Dir, l.Path, l.Outcome, l.Detail = LogTransfer, kv["dir"], kv["path"], kv["outcome"], kv["detail"]
		l.Op = l.Dir
	case "key-replaced":
		l.Type, l.Op = LogKeyReplaced, LogKeyReplaced
		l.Detail = "old=" + kv["old"] + " new=" + kv["new"]
	case "sync":
		l.Type, l.Op, l.Outcome = LogSync, LogSync, "ok"
		var parts []string
		for _, k := range []string{"from", "to", "sent", "received", "conflicts", "took"} {
			if v, ok := kv[k]; ok {
				parts = append(parts, k+"="+v)
			}
		}
		if e := kv["error"]; e != "" {
			l.Outcome = "failed"
			parts = append(parts, "error="+strconv.Quote(e))
		}
		l.Detail = strings.Join(parts, " ")
	default:
		return other
	}
	return l
}

// parseKV reads space-separated key=value pairs; a value is either bare
// (up to the next space) or a Go-quoted string.
func parseKV(s string) (map[string]string, bool) {
	out := map[string]string{}
	for {
		s = strings.TrimLeft(s, " ")
		if s == "" {
			return out, true
		}
		k, rest, ok := strings.Cut(s, "=")
		if !ok || k == "" || strings.Contains(k, " ") {
			return nil, false
		}
		if strings.HasPrefix(rest, `"`) {
			q, err := strconv.QuotedPrefix(rest)
			if err != nil {
				return nil, false
			}
			v, _ := strconv.Unquote(q)
			out[k], s = v, rest[len(q):]
			continue
		}
		v, r, _ := strings.Cut(rest, " ")
		out[k], s = v, r
	}
}
