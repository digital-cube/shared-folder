package server

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	sqlite "modernc.org/sqlite"
)

// MovedDB is one database copied by MoveDBs.
type MovedDB struct {
	Name string `json:"name"` // "registry" or the share id
	From string `json:"from"`
	To   string `json:"to"`
}

// MoveDBs copies registry.db and every folder's service.db from the
// db-root from to the db-root to, for shfd db move; the profile pictures
// of accounts (avatars/) go along as files. The caller makes sure
// no server runs (it holds serve.lock of from). Each database is copied
// with SQLite's online backup API, so what sits in its WAL is part of the
// copy, and the copy must pass PRAGMA integrity_check and carry the
// schema version of the original. The originals are left where they are;
// a marker in the data-root (MovedMarker) names the new db-root, so a
// server started without it is refused instead of running on the old
// copies. A to that already holds databases is refused.
func MoveDBs(dataRoot, from, to string) ([]MovedDB, error) {
	abs, fromAbs, err := Roots(dataRoot, from)
	if err != nil {
		return nil, err
	}
	toAbs, err := filepath.Abs(to)
	if err != nil {
		return nil, err
	}
	if toAbs == fromAbs {
		return nil, fmt.Errorf("the databases are already in %s", toAbs)
	}
	if moved, err := MovedTo(abs); err != nil {
		return nil, err
	} else if moved != "" && moved != fromAbs {
		return nil, fmt.Errorf("the databases of data-root %s were moved to %s; give that as --db-root to move them again", abs, moved)
	}
	regFrom := filepath.Join(fromAbs, RegistryFile)
	if _, err := os.Stat(regFrom); err != nil {
		return nil, fmt.Errorf("no registry to move: %w", err)
	}
	ids, err := registryIDs(regFrom)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", regFrom, err)
	}
	plan := []MovedDB{{Name: "registry", From: regFrom, To: filepath.Join(toAbs, RegistryFile)}}
	for _, id := range ids {
		plan = append(plan, MovedDB{Name: id, From: filepath.Join(fromAbs, id, DBFile), To: filepath.Join(toAbs, id, DBFile)})
	}
	for _, m := range plan {
		if _, err := os.Stat(m.From); err != nil {
			return nil, fmt.Errorf("database of %s: %w", m.Name, err)
		}
		if _, err := os.Lstat(m.To); err == nil {
			return nil, fmt.Errorf("%s already exists; move it away first, nothing was copied", m.To)
		}
	}
	avFrom, avTo := filepath.Join(fromAbs, AvatarsDir), filepath.Join(toAbs, AvatarsDir)
	if _, err := os.Lstat(avTo); err == nil {
		return nil, fmt.Errorf("%s already exists; move it away first, nothing was copied", avTo)
	}
	for _, m := range plan {
		if err := os.MkdirAll(filepath.Dir(m.To), 0o700); err != nil {
			return nil, err
		}
		if err := backupDB(m.From, m.To); err != nil {
			return nil, fmt.Errorf("copying %s to %s: %w", m.From, m.To, err)
		}
		if err := checkCopy(m.From, m.To); err != nil {
			return nil, fmt.Errorf("checking %s: %w", m.To, err)
		}
	}
	if err := copyAvatars(avFrom, avTo); err != nil {
		return nil, fmt.Errorf("copying %s to %s: %w", avFrom, avTo, err)
	}
	if err := writeMovedMarker(abs, toAbs, fromAbs); err != nil {
		return plan, err
	}
	return plan, nil
}

// copyAvatars copies the pictures in from (none when it does not exist)
// into the new directory to.
func copyAvatars(from, to string) error {
	entries, err := os.ReadDir(from)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if err := os.MkdirAll(to, 0o700); err != nil {
		return err
	}
	for _, e := range entries {
		if !e.Type().IsRegular() || strings.HasPrefix(e.Name(), ".") {
			continue // a temporary file of an upload that did not finish
		}
		data, err := os.ReadFile(filepath.Join(from, e.Name()))
		if err != nil {
			return err
		}
		if err := writeFileAtomic(filepath.Join(to, e.Name()), data); err != nil {
			return err
		}
	}
	return nil
}

func registryIDs(file string) ([]string, error) {
	db, err := sql.Open("sqlite", "file:"+file+"?_pragma=busy_timeout(10000)")
	if err != nil {
		return nil, err
	}
	defer db.Close()
	rows, err := db.Query(`SELECT id FROM shares ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		if !ValidShareID(id) {
			return nil, fmt.Errorf("registry holds an invalid share id %q", id)
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// backupDB copies the database src to the new file dst with
// sqlite3_backup_*: a consistent copy of every page, the ones still in
// the WAL included, unlike copying the files.
func backupDB(src, dst string) error {
	db, err := sql.Open("sqlite", "file:"+src+"?_pragma=busy_timeout(10000)")
	if err != nil {
		return err
	}
	defer db.Close()
	ctx := context.Background()
	conn, err := db.Conn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	return conn.Raw(func(dc any) error {
		b, ok := dc.(interface {
			NewBackup(string) (*sqlite.Backup, error)
		})
		if !ok {
			return errors.New("the SQLite driver has no backup API")
		}
		bk, err := b.NewBackup(dst)
		if err != nil {
			return err
		}
		for more := true; more; {
			if more, err = bk.Step(-1); err != nil {
				bk.Finish()
				return err
			}
		}
		return bk.Finish()
	})
}

// checkCopy runs PRAGMA integrity_check on the copy and compares its
// schema version with the original's.
func checkCopy(src, dst string) error {
	db, err := sql.Open("sqlite", "file:"+dst+"?_pragma=busy_timeout(10000)")
	if err != nil {
		return err
	}
	defer db.Close()
	var res string
	if err := db.QueryRow(`PRAGMA integrity_check`).Scan(&res); err != nil {
		return err
	}
	if res != "ok" {
		return fmt.Errorf("integrity_check: %s", res)
	}
	var vDst, vSrc int
	if err := db.QueryRow(`PRAGMA user_version`).Scan(&vDst); err != nil {
		return err
	}
	sdb, err := sql.Open("sqlite", "file:"+src+"?_pragma=busy_timeout(10000)")
	if err != nil {
		return err
	}
	defer sdb.Close()
	if err := sdb.QueryRow(`PRAGMA user_version`).Scan(&vSrc); err != nil {
		return err
	}
	if vDst != vSrc {
		return fmt.Errorf("schema version %d, the original has %d", vDst, vSrc)
	}
	return nil
}

// writeMovedMarker records the new db-root in the data-root; moving the
// databases back into the data-root removes the marker.
func writeMovedMarker(abs, toAbs, fromAbs string) error {
	p := filepath.Join(abs, MovedMarker)
	if toAbs == abs {
		if err := os.Remove(p); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		return nil
	}
	body := fmt.Sprintf("%s\n# Written by shfd db move at %s: the databases of this data-root are in\n# the directory above (--db-root / $SHFD_DB_ROOT). The ones in %s\n# are the copies from before the move and are no longer used.\n",
		toAbs, time.Now().UTC().Format(time.RFC3339), fromAbs)
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, []byte(body), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, p)
}
