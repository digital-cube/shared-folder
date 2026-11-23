package client

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	_ "modernc.org/sqlite" // registers the "sqlite" driver
)

// EnvStateHome overrides the directory that holds the state of every
// joined folder (one subdirectory per share-id).
const EnvStateHome = "SHF_STATE_HOME"

// StateHome returns the directory holding the state of every joined
// folder: $SHF_STATE_HOME, else $XDG_STATE_HOME/shf (~/.local/state/shf),
// on macOS ~/Library/Application Support/shf. getenv may be nil.
func StateHome(getenv func(string) string) (string, error) {
	if getenv == nil {
		getenv = os.Getenv
	}
	if d := getenv(EnvStateHome); d != "" {
		return filepath.Abs(d)
	}
	home := getenv("HOME")
	if home == "" {
		var err error
		if home, err = os.UserHomeDir(); err != nil {
			return "", fmt.Errorf("no home directory for the client state (set $%s): %w", EnvStateHome, err)
		}
	}
	if runtime.GOOS == "darwin" {
		return filepath.Join(home, "Library", "Application Support", "shf"), nil
	}
	if d := getenv("XDG_STATE_HOME"); filepath.IsAbs(d) {
		return filepath.Join(d, "shf"), nil
	}
	return filepath.Join(home, ".local", "state", "shf"), nil
}

// Files in the state directory of one folder.
const (
	configFile = "config.json"
	tokenFile  = "token"
	keyFile    = "id_ed25519"
	dbFile     = "state.db"
	lockFile   = "lock"
)

// SSH is where rsync over SSH connects.
type SSH struct {
	User string `json:"user"`
	Host string `json:"host"`
	Port int    `json:"port"`
}

// Config is a joined folder. It lives in <state home>/<share-id>/config.json;
// the token is beside it in "token", both 0600.
type Config struct {
	URL     string `json:"url"`
	ShareID string `json:"share_id"`
	Folder  string `json:"folder"` // absolute
	// SSH is empty when the folder was joined with a transport of the
	// caller's (tests).
	SSH *SSH `json:"ssh,omitempty"`
	// Joined is when join finished its setup.
	Joined time.Time `json:"joined"`

	Token string `json:"-"`
	Dir   string `json:"-"` // the state directory
}

// KeyFile is the member's SSH private key.
func (c *Config) KeyFile() string { return filepath.Join(c.Dir, keyFile) }

func writePrivate(path string, data []byte) error {
	tmp := path + ".new"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	if err := os.Chmod(tmp, 0o600); err != nil { // an existing .new keeps its mode
		return err
	}
	return os.Rename(tmp, path)
}

func (c *Config) save() error {
	b, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	if err := writePrivate(filepath.Join(c.Dir, tokenFile), []byte(c.Token+"\n")); err != nil {
		return err
	}
	return writePrivate(filepath.Join(c.Dir, configFile), append(b, '\n'))
}

func loadConfig(dir string) (*Config, error) {
	b, err := os.ReadFile(filepath.Join(dir, configFile))
	if err != nil {
		return nil, err
	}
	var c Config
	if err := json.Unmarshal(b, &c); err != nil {
		return nil, fmt.Errorf("%s: %w", filepath.Join(dir, configFile), err)
	}
	tok, err := os.ReadFile(filepath.Join(dir, tokenFile))
	if err != nil {
		return nil, err
	}
	c.Token, c.Dir = strings.TrimSpace(string(tok)), dir
	return &c, nil
}

// Joined lists the folders joined under home.
func Joined(home string) ([]*Config, error) {
	entries, err := os.ReadDir(home)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []*Config
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		c, err := loadConfig(filepath.Join(home, e.Name()))
		if errors.Is(err, os.ErrNotExist) {
			continue // a join that did not get far, or not ours
		}
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, nil
}

// Find returns the joined folder that contains dir (dir itself or one of
// its parents).
func Find(home, dir string) (*Config, error) {
	abs, err := absClean(dir)
	if err != nil {
		return nil, err
	}
	cs, err := Joined(home)
	if err != nil {
		return nil, err
	}
	for _, c := range cs {
		if within(abs, c.Folder) {
			return c, nil
		}
	}
	return nil, fmt.Errorf("%s is not inside a joined folder (shf join first)", abs)
}

func absClean(dir string) (string, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return "", err
	}
	if r, err := filepath.EvalSymlinks(abs); err == nil {
		abs = r
	}
	return abs, nil
}

// within reports whether p is root or below it.
func within(p, root string) bool {
	rel, err := filepath.Rel(root, p)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel)
}

// ---- state.db ------------------------------------------------------------

const schema = `
CREATE TABLE IF NOT EXISTS meta (
    key   TEXT PRIMARY KEY,
    value TEXT NOT NULL
);
-- Each path as it was after its last successful sync.
CREATE TABLE IF NOT EXISTS manifest (
    path  TEXT PRIMARY KEY,
    kind  TEXT NOT NULL CHECK (kind IN ('file', 'dir')),
    hash  TEXT NOT NULL DEFAULT '',
    size  INTEGER NOT NULL DEFAULT 0,
    mtime INTEGER NOT NULL DEFAULT 0,  -- Unix nanoseconds
    state TEXT NOT NULL DEFAULT 'synced' CHECK (state IN ('synced', 'skipped', 'local'))
);
-- The server's state at remote_seq, built from its change log.
CREATE TABLE IF NOT EXISTS remote (
    path  TEXT PRIMARY KEY,
    kind  TEXT NOT NULL CHECK (kind IN ('file', 'dir')),
    hash  TEXT NOT NULL DEFAULT '',
    size  INTEGER NOT NULL DEFAULT 0,
    mtime INTEGER NOT NULL DEFAULT 0
);
-- Paths that need the user (rejected names, kind clashes), from the last cycle.
CREATE TABLE IF NOT EXISTS problems (
    path   TEXT PRIMARY KEY,
    reason TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS cycles (
    id          INTEGER PRIMARY KEY,
    started_at  TEXT NOT NULL,
    finished_at TEXT NOT NULL,
    from_seq    INTEGER NOT NULL,
    to_seq      INTEGER NOT NULL,
    head        INTEGER NOT NULL,
    sent        INTEGER NOT NULL,
    received    INTEGER NOT NULL,
    conflicts   INTEGER NOT NULL,
    error       TEXT NOT NULL DEFAULT ''
);
-- Deletions held back by the mass-delete guard (docs/specifikacija.md, 8.4);
-- rows with confirmed = 0 mean the folder is blocked.
CREATE TABLE IF NOT EXISTS held_deletes (
    path      TEXT PRIMARY KEY,
    kind      TEXT NOT NULL CHECK (kind IN ('file', 'dir')),
    base_hash TEXT NOT NULL DEFAULT '',
    confirmed INTEGER NOT NULL DEFAULT 0,
    since     TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS cycle_items (
    cycle_id INTEGER NOT NULL REFERENCES cycles (id) ON DELETE CASCADE,
    n        INTEGER NOT NULL,
    op       TEXT NOT NULL,
    path     TEXT NOT NULL,
    detail   TEXT NOT NULL DEFAULT '',
    PRIMARY KEY (cycle_id, n)
);
`

// Keys of meta.
const (
	// metaSeq is the seq up to which the folder was fully in sync at the
	// end of the last successful cycle ("the last applied seq").
	metaSeq = "applied_seq"
	// metaRemoteSeq is the seq the remote table reflects.
	metaRemoteSeq = "remote_seq"
	// metaHead is the server's HEAD as last seen.
	metaHead = "head"
)

// keepCycles is how many cycles the local log keeps.
const keepCycles = 200

// State is the client's database for one folder.
type State struct {
	db *sql.DB
}

// OpenState opens (creating if needed) state.db in dir.
func OpenState(dir string) (*State, error) {
	q := url.Values{}
	for _, p := range []string{"journal_mode(WAL)", "synchronous(NORMAL)", "foreign_keys(1)", "busy_timeout(10000)"} {
		q.Add("_pragma", p)
	}
	q.Set("_txlock", "immediate")
	file := filepath.Join(dir, dbFile)
	db, err := sql.Open("sqlite", "file:"+file+"?"+q.Encode())
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, fmt.Errorf("%s: %w", file, err)
	}
	os.Chmod(file, 0o600)
	return &State{db: db}, nil
}

// Close closes the database.
func (s *State) Close() error { return s.db.Close() }

type execer interface {
	Exec(query string, args ...any) (sql.Result, error)
}

func (s *State) metaInt(key string) (int64, error) {
	var v string
	err := s.db.QueryRow(`SELECT value FROM meta WHERE key = ?`, key).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	return strconv.ParseInt(v, 10, 64)
}

func setMeta(x execer, key string, v int64) error {
	_, err := x.Exec(`INSERT INTO meta (key, value) VALUES (?, ?) ON CONFLICT (key) DO UPDATE SET value = excluded.value`,
		key, strconv.FormatInt(v, 10))
	return err
}

// Seq is the last applied seq.
func (s *State) Seq() (int64, error) { return s.metaInt(metaSeq) }

func (s *State) loadEntries(table string) (map[string]Entry, error) {
	rows, err := s.db.Query(`SELECT path, kind, hash, size, mtime FROM ` + table)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]Entry{}
	for rows.Next() {
		var p, k string
		var e Entry
		if err := rows.Scan(&p, &k, &e.Hash, &e.Size, &e.Mtime); err != nil {
			return nil, err
		}
		e.Kind = parseKind(k)
		out[p] = e
	}
	return out, rows.Err()
}

// Remote is the server's state as of remote_seq.
func (s *State) Remote() (map[string]Entry, error) { return s.loadEntries("remote") }

// Manifest returns every manifest row.
func (s *State) Manifest() (map[string]ManifestEntry, error) {
	rows, err := s.db.Query(`SELECT path, kind, hash, size, mtime, state FROM manifest`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]ManifestEntry{}
	for rows.Next() {
		var p, k, st string
		var e ManifestEntry
		if err := rows.Scan(&p, &k, &e.Hash, &e.Size, &e.Mtime, &st); err != nil {
			return nil, err
		}
		e.Kind, e.State = parseKind(k), ManifestState(st)
		out[p] = e
	}
	return out, rows.Err()
}

// racyWindow: a file whose mtime is this close to the time it is recorded
// may still be rewritten within the same clock tick, keeping its size and
// mtime. Such a row is stored with mtime 0, so the next scan hashes the
// file again instead of trusting the cached hash.
const racyWindow = 2 * time.Second

// putManifest writes one row; an entry of kind None deletes it.
func putManifest(x execer, p string, e Entry, st ManifestState) error {
	if e.Kind == None {
		_, err := x.Exec(`DELETE FROM manifest WHERE path = ?`, p)
		return err
	}
	if st == Synced && e.Kind == File {
		if d := time.Since(time.Unix(0, e.Mtime)); d < racyWindow && d > -racyWindow {
			e.Mtime = 0
		}
	}
	_, err := x.Exec(`INSERT INTO manifest (path, kind, hash, size, mtime, state) VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT (path) DO UPDATE SET kind = excluded.kind, hash = excluded.hash, size = excluded.size,
		mtime = excluded.mtime, state = excluded.state`, p, e.Kind.String(), e.Hash, e.Size, e.Mtime, string(st))
	return err
}

// SetManifest records one path; Kind None removes it.
func (s *State) SetManifest(p string, e Entry, st ManifestState) error {
	return putManifest(s.db, p, e, st)
}

// MoveManifest moves a synced row from one path to another in one step.
func (s *State) MoveManifest(from, to string, e Entry) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := putManifest(tx, from, Entry{}, ""); err != nil {
		return err
	}
	if err := putManifest(tx, to, e, Synced); err != nil {
		return err
	}
	return tx.Commit()
}

// applyEvents moves a remote state forward with events (in seq order) and
// returns the paths they touched.
func applyEvents(remote map[string]Entry, evs []Event) map[string]bool {
	touched := map[string]bool{}
	for _, ev := range evs {
		switch ev.Op {
		case "put", "conflict", "restore":
			remote[ev.Path] = Entry{Kind: File, Hash: ev.Hash, Size: ev.Size, Mtime: ev.Mtime}
			touched[ev.Path] = true
		case "move":
			delete(remote, ev.OldPath)
			remote[ev.Path] = Entry{Kind: File, Hash: ev.Hash, Size: ev.Size, Mtime: ev.Mtime}
			touched[ev.OldPath], touched[ev.Path] = true, true
		case "delete", "rmdir":
			delete(remote, ev.Path)
			touched[ev.Path] = true
		case "mkdir":
			remote[ev.Path] = Entry{Kind: Dir}
			touched[ev.Path] = true
		default: // purge and future operations do not change storage/
		}
	}
	return touched
}

// ApplyEvents brings the remote table forward with events (in seq order)
// and stores the new remote_seq and HEAD, in one transaction.
func (s *State) ApplyEvents(evs []Event, toSeq, head int64) error {
	remote, err := s.Remote()
	if err != nil {
		return err
	}
	touched := applyEvents(remote, evs)
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for p := range touched {
		e, ok := remote[p]
		if !ok {
			if _, err := tx.Exec(`DELETE FROM remote WHERE path = ?`, p); err != nil {
				return err
			}
			continue
		}
		if _, err := tx.Exec(`INSERT INTO remote (path, kind, hash, size, mtime) VALUES (?, ?, ?, ?, ?)
			ON CONFLICT (path) DO UPDATE SET kind = excluded.kind, hash = excluded.hash, size = excluded.size, mtime = excluded.mtime`,
			p, e.Kind.String(), e.Hash, e.Size, e.Mtime); err != nil {
			return err
		}
	}
	if err := setMeta(tx, metaRemoteSeq, toSeq); err != nil {
		return err
	}
	if err := setMeta(tx, metaHead, head); err != nil {
		return err
	}
	return tx.Commit()
}

// replaceRemote replaces the remote table with the server's state rebuilt
// from its whole log (a full comparison).
func (s *State) replaceRemote(remote map[string]Entry, toSeq, head int64) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`DELETE FROM remote`); err != nil {
		return err
	}
	for p, e := range remote {
		if _, err := tx.Exec(`INSERT INTO remote (path, kind, hash, size, mtime) VALUES (?, ?, ?, ?, ?)`,
			p, e.Kind.String(), e.Hash, e.Size, e.Mtime); err != nil {
			return err
		}
	}
	if err := setMeta(tx, metaRemoteSeq, toSeq); err != nil {
		return err
	}
	if err := setMeta(tx, metaHead, head); err != nil {
		return err
	}
	return tx.Commit()
}

// RemoteSeq is the seq the server's state is known up to.
func (s *State) RemoteSeq() (int64, error) { return s.metaInt(metaRemoteSeq) }

// Problem is a path the user has to resolve.
type Problem struct {
	Path, Reason string
}

// Cycle is one row of the local log.
type Cycle struct {
	ID                        int64
	StartedAt, FinishedAt     time.Time
	FromSeq, ToSeq, Head      int64
	Sent, Received, Conflicts int64
	Error                     string
	Items                     []Item
}

// Item is one applied step of a cycle.
type Item struct {
	Op, Path, Detail string
}

// finishCycle stores the cycle and its problems; when it succeeded it
// also stores the applied seq.
func (s *State) finishCycle(c *Cycle, problems []Problem, ok bool) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	res, err := tx.Exec(`INSERT INTO cycles (started_at, finished_at, from_seq, to_seq, head, sent, received, conflicts, error)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`, c.StartedAt.UTC().Format(time.RFC3339Nano), c.FinishedAt.UTC().Format(time.RFC3339Nano),
		c.FromSeq, c.ToSeq, c.Head, c.Sent, c.Received, c.Conflicts, c.Error)
	if err != nil {
		return err
	}
	if c.ID, err = res.LastInsertId(); err != nil {
		return err
	}
	for i, it := range c.Items {
		if _, err := tx.Exec(`INSERT INTO cycle_items (cycle_id, n, op, path, detail) VALUES (?, ?, ?, ?, ?)`,
			c.ID, i, it.Op, it.Path, it.Detail); err != nil {
			return err
		}
	}
	if _, err := tx.Exec(`DELETE FROM cycle_items WHERE cycle_id <= ?`, c.ID-keepCycles); err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM cycles WHERE id <= ?`, c.ID-keepCycles); err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM problems`); err != nil {
		return err
	}
	for _, p := range problems {
		if _, err := tx.Exec(`INSERT OR REPLACE INTO problems (path, reason) VALUES (?, ?)`, p.Path, p.Reason); err != nil {
			return err
		}
	}
	if ok {
		if err := setMeta(tx, metaSeq, c.ToSeq); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// Problems returns the problems found by the last cycle.
func (s *State) Problems() ([]Problem, error) {
	rows, err := s.db.Query(`SELECT path, reason FROM problems ORDER BY path`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Problem
	for rows.Next() {
		var p Problem
		if err := rows.Scan(&p.Path, &p.Reason); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// Cycles returns the newest n cycles, newest first, with their items.
func (s *State) Cycles(n int) ([]Cycle, error) {
	rows, err := s.db.Query(`SELECT id, started_at, finished_at, from_seq, to_seq, head, sent, received, conflicts, error
		FROM cycles ORDER BY id DESC LIMIT ?`, n)
	if err != nil {
		return nil, err
	}
	var out []Cycle
	for rows.Next() {
		var c Cycle
		var st, fin string
		if err := rows.Scan(&c.ID, &st, &fin, &c.FromSeq, &c.ToSeq, &c.Head, &c.Sent, &c.Received, &c.Conflicts, &c.Error); err != nil {
			rows.Close()
			return nil, err
		}
		c.StartedAt, _ = time.Parse(time.RFC3339Nano, st)
		c.FinishedAt, _ = time.Parse(time.RFC3339Nano, fin)
		out = append(out, c)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for i := range out {
		items, err := s.db.Query(`SELECT op, path, detail FROM cycle_items WHERE cycle_id = ? ORDER BY n`, out[i].ID)
		if err != nil {
			return nil, err
		}
		for items.Next() {
			var it Item
			if err := items.Scan(&it.Op, &it.Path, &it.Detail); err != nil {
				items.Close()
				return nil, err
			}
			out[i].Items = append(out[i].Items, it)
		}
		items.Close()
	}
	return out, nil
}
