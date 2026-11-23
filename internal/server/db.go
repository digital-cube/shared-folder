package server

import (
	"context"
	"database/sql"
	"embed"
	"fmt"
	"io/fs"
	"net/url"
	"path"
	"sort"
	"strconv"
	"strings"
	"time"

	_ "modernc.org/sqlite" // registers the "sqlite" driver
)

//go:embed migrations
var migrationFS embed.FS

// openDB opens (creating if needed) an SQLite database in WAL mode with
// foreign keys on, durable commits and IMMEDIATE transactions, and brings
// its schema up to date with the numbered migrations in migrations/<set>/.
func openDB(file, set string) (*sql.DB, error) {
	q := url.Values{}
	for _, p := range []string{
		"journal_mode(WAL)",
		"synchronous(FULL)",
		"foreign_keys(1)",
		"busy_timeout(10000)",
	} {
		q.Add("_pragma", p)
	}
	q.Set("_txlock", "immediate")
	db, err := sql.Open("sqlite", "file:"+file+"?"+q.Encode())
	if err != nil {
		return nil, err
	}
	if err := migrate(db, set); err != nil {
		db.Close()
		return nil, fmt.Errorf("%s: %w", file, err)
	}
	return db, nil
}

type migration struct {
	version int
	name    string
	sql     string
}

func migrations(set string) ([]migration, error) {
	dir := path.Join("migrations", set)
	entries, err := fs.ReadDir(migrationFS, dir)
	if err != nil {
		return nil, err
	}
	var out []migration
	for _, e := range entries {
		name := e.Name()
		if !strings.HasSuffix(name, ".sql") {
			continue
		}
		num, _, ok := strings.Cut(name, "_")
		v, err := strconv.Atoi(num)
		if !ok || err != nil || v <= 0 {
			return nil, fmt.Errorf("migration %s: name must be NNNN_description.sql", name)
		}
		body, err := fs.ReadFile(migrationFS, path.Join(dir, name))
		if err != nil {
			return nil, err
		}
		out = append(out, migration{version: v, name: name, sql: string(body)})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].version < out[j].version })
	for i, m := range out {
		if m.version != i+1 {
			return nil, fmt.Errorf("migrations of %q must be numbered 1..N without gaps, found %s", set, m.name)
		}
	}
	return out, nil
}

// migrate applies every migration newer than PRAGMA user_version, each in
// its own transaction together with the version bump.
func migrate(db *sql.DB, set string) error {
	ms, err := migrations(set)
	if err != nil {
		return err
	}
	ctx := context.Background()
	for _, m := range ms {
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		var cur int
		if err := tx.QueryRow(`PRAGMA user_version`).Scan(&cur); err != nil {
			tx.Rollback()
			return err
		}
		if cur >= m.version {
			tx.Rollback()
			continue
		}
		if _, err := tx.Exec(m.sql); err != nil {
			tx.Rollback()
			return fmt.Errorf("migration %s: %w", m.name, err)
		}
		if _, err := tx.Exec(fmt.Sprintf(`PRAGMA user_version = %d`, m.version)); err != nil {
			tx.Rollback()
			return err
		}
		if err := tx.Commit(); err != nil {
			return err
		}
	}
	var cur int
	if err := db.QueryRow(`PRAGMA user_version`).Scan(&cur); err != nil {
		return err
	}
	if cur > len(ms) {
		return fmt.Errorf("schema version %d is newer than this binary knows (%d)", cur, len(ms))
	}
	return nil
}

// Times are stored as RFC 3339 text in UTC with nanoseconds.
const timeLayout = time.RFC3339Nano

func fmtTime(t time.Time) string { return t.UTC().Format(timeLayout) }

func parseTime(s sql.NullString) *time.Time {
	if !s.Valid || s.String == "" {
		return nil
	}
	t, err := time.Parse(timeLayout, s.String)
	if err != nil {
		return nil
	}
	return &t
}
