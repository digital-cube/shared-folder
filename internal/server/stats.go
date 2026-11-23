package server

import "time"

// Stats is the state of a folder at a glance (shfd stats).
type Stats struct {
	ShareID string `json:"share_id"`
	Head    int64  `json:"head"`
	// Files and FilesSize count the regular files of storage/ as the
	// database records them (shfd verify compares it with the disk).
	Files     int64 `json:"files"`
	FilesSize int64 `json:"files_size"`
	Dirs      int64 `json:"dirs"`
	// Trash and TrashSize count the entries that were not purged; Purged
	// counts the purged ones.
	Trash       int64         `json:"trash"`
	TrashSize   int64         `json:"trash_size"`
	Purged      int64         `json:"purged"`
	OpenUploads int64         `json:"open_uploads"`
	Members     []MemberStats `json:"members"`
}

// MemberStats is one member in Stats.
type MemberStats struct {
	Username     string     `json:"username"`
	Role         string     `json:"role"`
	LastSeenAt   *time.Time `json:"last_seen_at"`
	LastPushAt   *time.Time `json:"last_push_at"`
	LastSeqAcked int64      `json:"last_seq_acked"`
	// Behind is HEAD minus LastSeqAcked: events the member has not
	// confirmed yet.
	Behind    int64      `json:"behind"`
	Revoked   bool       `json:"revoked"`
	RevokedAt *time.Time `json:"revoked_at"`
}

// Stats returns counts and sizes of the folder and its members.
func (s *Share) Stats(m *Member) (*Stats, error) {
	if err := RequireAdmin(m); err != nil {
		return nil, err
	}
	st := &Stats{ShareID: s.ID, Members: []MemberStats{}}
	tx, err := s.db.Begin() // one snapshot for every number
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	for _, q := range []struct {
		sql  string
		dest []any
	}{
		{`SELECT coalesce(max(seq), 0) FROM events`, []any{&st.Head}},
		{`SELECT count(*), coalesce(sum(size), 0) FROM files WHERE kind = 'file'`, []any{&st.Files, &st.FilesSize}},
		{`SELECT count(*) FROM files WHERE kind = 'dir'`, []any{&st.Dirs}},
		{`SELECT count(*), coalesce(sum(size), 0) FROM trash WHERE purged_at IS NULL`, []any{&st.Trash, &st.TrashSize}},
		{`SELECT count(*) FROM trash WHERE purged_at IS NOT NULL`, []any{&st.Purged}},
		{`SELECT count(*) FROM uploads`, []any{&st.OpenUploads}},
	} {
		if err := tx.QueryRow(q.sql).Scan(q.dest...); err != nil {
			return nil, err
		}
	}
	rows, err := tx.Query(`SELECT ` + memberCols + ` FROM members ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		mm, err := scanMember(rows)
		if err != nil {
			return nil, err
		}
		st.Members = append(st.Members, MemberStats{
			Username: mm.Username, Role: mm.Role,
			LastSeenAt: mm.LastSeenAt, LastPushAt: mm.LastPushAt,
			LastSeqAcked: mm.LastSeqAcked, Behind: max(st.Head-mm.LastSeqAcked, 0),
			Revoked: mm.RevokedAt != nil, RevokedAt: mm.RevokedAt,
		})
	}
	return st, rows.Err()
}
