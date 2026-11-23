package server

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"
)

var t0 = time.Date(2026, 11, 23, 14, 58, 46, 0, time.UTC)

func sum(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

type fixture struct {
	t     *testing.T
	root  string
	srv   *Server
	id    string
	token string
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	f := &fixture{t: t, root: t.TempDir()}
	f.open()
	id, tok, err := f.srv.CreateShare("tim", "ana")
	if err != nil {
		t.Fatal(err)
	}
	f.id, f.token = id, tok
	return f
}

func (f *fixture) open() {
	f.t.Helper()
	srv, err := Open(f.root, Options{Now: func() time.Time { return t0 }})
	if err != nil {
		f.t.Fatal(err)
	}
	f.srv = srv
	f.t.Cleanup(func() { srv.Close() })
}

func (f *fixture) share() (*Share, *Member) {
	f.t.Helper()
	sh, err := f.srv.Share(f.id)
	if err != nil {
		f.t.Fatal(err)
	}
	m, err := sh.Authenticate(f.token)
	if err != nil {
		f.t.Fatal(err)
	}
	return sh, m
}

func (f *fixture) put(p, base string, content []byte) (*Result, error) {
	f.t.Helper()
	sh, m := f.share()
	ar, err := sh.Announce(m, AnnounceRequest{Path: p, BaseHash: base, NewHash: sum(content), Size: int64(len(content))})
	if err != nil {
		f.t.Fatal(err)
	}
	if err := os.WriteFile(sh.abs(ar.Dest), content, 0o600); err != nil {
		f.t.Fatal(err)
	}
	return sh.Commit(m, ar.UploadID)
}

func (f *fixture) ls(sub string) []string {
	f.t.Helper()
	ents, err := os.ReadDir(filepath.Join(f.root, f.id, sub))
	if err != nil {
		f.t.Fatal(err)
	}
	var out []string
	for _, e := range ents {
		out = append(out, e.Name())
	}
	sort.Strings(out)
	return out
}

func (f *fixture) read(sub, name string) string {
	f.t.Helper()
	b, err := os.ReadFile(filepath.Join(f.root, f.id, sub, name))
	if err != nil {
		f.t.Fatal(err)
	}
	return string(b)
}

func (f *fixture) journalLen() int {
	sh, _ := f.share()
	var n int
	sh.db.QueryRow(`SELECT count(*) FROM fs_journal`).Scan(&n)
	return n
}

var errCrash = errors.New("simulated crash")

// A crash after the database commit and before (or in the middle of, or
// right after) the disk steps leaves a journal; the next open finishes it,
// and service.log catches up with events.
func TestCrashAfterCommitIsFinishedOnOpen(t *testing.T) {
	v1, v2 := []byte("prva\n"), []byte("druga\n")
	for _, tc := range []struct {
		name     string
		runSteps int // how many disk steps happen before the "crash"
	}{{"before steps", 0}, {"after first step", 1}, {"after all steps", 2}} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t)
			if _, err := f.put("a.txt", "", v1); err != nil {
				t.Fatal(err)
			}
			sh, _ := f.share()
			sh.crashAfterCommit = func() error {
				rows, _ := sh.db.Query(`SELECT op, src, dst FROM fs_journal ORDER BY id`)
				var steps []fsStep
				for rows.Next() {
					var st fsStep
					rows.Scan(&st.Op, &st.Src, &st.Dst)
					steps = append(steps, st)
				}
				rows.Close()
				for _, st := range steps[:tc.runSteps] {
					if err := sh.runStep(st); err != nil {
						t.Fatal(err)
					}
				}
				return errCrash
			}
			if _, err := f.put("a.txt", sum(v1), v2); !errors.Is(err, errCrash) {
				t.Fatalf("put = %v, want the simulated crash", err)
			}
			if n := f.journalLen(); n != 2 {
				t.Fatalf("journal rows after crash = %d, want 2", n)
			}

			f.srv.Close()
			f.open()
			if err := f.srv.OpenAll(); err != nil {
				t.Fatal(err)
			}

			trashName := "a.txt.replaced-by-ana-20261123T145846Z"
			if got := f.ls("storage"); strings.Join(got, ",") != "a.txt" {
				t.Errorf("storage = %v", got)
			}
			if got := f.read("storage", "a.txt"); got != string(v2) {
				t.Errorf("storage/a.txt = %q, want the new version", got)
			}
			if got := f.ls("trash"); strings.Join(got, ",") != trashName {
				t.Errorf("trash = %v", got)
			}
			if got := f.read("trash", trashName); got != string(v1) {
				t.Errorf("trash content = %q", got)
			}
			if got := f.ls("tmp"); len(got) != 0 {
				t.Errorf("tmp = %v", got)
			}
			if n := f.journalLen(); n != 0 {
				t.Errorf("journal rows after open = %d", n)
			}
			log := f.read("", "service.log")
			if c := strings.Count(log, " seq="); c != 2 {
				t.Errorf("service.log has %d event lines, want 2:\n%s", c, log)
			}
			if !strings.Contains(log, " seq=2 member=ana op=put ") {
				t.Errorf("service.log misses seq 2:\n%s", log)
			}

			// And the folder keeps working.
			if _, err := f.put("a.txt", sum(v2), []byte("treća\n")); err != nil {
				t.Fatal(err)
			}
		})
	}
}

// A failure before the commit leaves neither rows nor files nor a gap in seq.
func TestFailureBeforeCommitChangesNothing(t *testing.T) {
	f := newFixture(t)
	f.put("a.txt", "", []byte("x"))
	sh, m := f.share()
	_, _, err := sh.write(m, func(o *op) error {
		if err := o.mkdir("novi"); err != nil {
			return err
		}
		return errCrash
	})
	if !errors.Is(err, errCrash) {
		t.Fatal(err)
	}
	if head, _ := sh.Head(); head != 1 {
		t.Errorf("head = %d, want 1", head)
	}
	if got := f.ls("storage"); strings.Join(got, ",") != "a.txt" {
		t.Errorf("storage = %v", got)
	}
	res, err := f.put("b.txt", "", []byte("y"))
	if err != nil {
		t.Fatal(err)
	}
	if res.Events[0].Seq != 2 {
		t.Errorf("next seq = %d, want 2 (no gap)", res.Events[0].Seq)
	}
}

// A file on disk that the database does not know is never overwritten.
func TestStorageDriftIsRefused(t *testing.T) {
	f := newFixture(t)
	if err := os.WriteFile(filepath.Join(f.root, f.id, "storage", "a.txt"), []byte("ručno"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := f.put("a.txt", "", []byte("x")); err == nil || !strings.Contains(err.Error(), "verify") {
		t.Fatalf("put over an unknown disk file = %v", err)
	}
	if got := f.read("storage", "a.txt"); got != "ručno" {
		t.Errorf("unknown file overwritten: %q", got)
	}
	sh, _ := f.share()
	if head, _ := sh.Head(); head != 0 {
		t.Errorf("head = %d", head)
	}
}

func TestMigrations(t *testing.T) {
	f := newFixture(t)
	sh, _ := f.share()
	var v int
	sh.db.QueryRow(`PRAGMA user_version`).Scan(&v)
	ms, _ := migrations("service")
	if v != len(ms) || v < 2 {
		t.Errorf("service user_version = %d, migrations = %d", v, len(ms))
	}
	f.srv.reg.QueryRow(`PRAGMA user_version`).Scan(&v)
	if v != 1 {
		t.Errorf("registry user_version = %d", v)
	}
	// Reopening applies nothing twice.
	f.srv.Close()
	f.open()
	f.share()

	// A database from a newer binary is refused.
	sh, _ = f.share()
	sh.db.Exec(`PRAGMA user_version = 99`)
	if err := migrate(sh.db, "service"); err == nil {
		t.Error("newer schema accepted")
	}
}

func TestTokensAreStoredHashed(t *testing.T) {
	f := newFixture(t)
	sh, _ := f.share()
	var stored string
	sh.db.QueryRow(`SELECT token_hash FROM members WHERE username = 'ana'`).Scan(&stored)
	if stored == f.token || stored != HashToken(f.token) {
		t.Errorf("token_hash = %q", stored)
	}
	if !strings.HasPrefix(f.token, "shf_") || len(f.token) != 4+43 {
		t.Errorf("token format %q", f.token)
	}
	if _, err := sh.AddMember("ana", false); CodeOf(err) != CodeMemberExists {
		t.Errorf("duplicate member: %v", err)
	}
	if _, err := sh.AddMember("Ana!", false); CodeOf(err) != CodeInvalidRequest {
		t.Errorf("bad username: %v", err)
	}
	if err := sh.RevokeMember("nema"); CodeOf(err) != CodeUnknownMember {
		t.Errorf("revoke unknown: %v", err)
	}
}

func TestRegistry(t *testing.T) {
	f := newFixture(t)
	if _, _, err := f.srv.CreateShare("tim", "ana"); err == nil {
		t.Error("duplicate folder name accepted")
	}
	shares, err := f.srv.Shares()
	if err != nil || len(shares) != 1 || shares[0].ID != f.id || shares[0].Name != "tim" || !shares[0].CreatedAt.Equal(t0) {
		t.Fatalf("shares = %+v, %v", shares, err)
	}
	if !ValidShareID(f.id) {
		t.Errorf("id %q is not a UUID", f.id)
	}
	for _, d := range []string{"storage", "trash", "tmp", "service.db", "service.log"} {
		if _, err := os.Stat(filepath.Join(f.root, f.id, d)); err != nil {
			t.Error(err)
		}
	}
	if _, err := os.Stat(filepath.Join(f.root, "registry.db")); err != nil {
		t.Error(err)
	}
	if _, err := f.srv.Share("00000000-0000-4000-8000-000000000000"); CodeOf(err) != CodeUnknownShare {
		t.Errorf("unknown share: %v", err)
	}
}
