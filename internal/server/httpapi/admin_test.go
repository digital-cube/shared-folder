package httpapi_test

import (
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/digital-cube/shared-folder/internal/server"
	"github.com/digital-cube/shared-folder/internal/server/httpapi"
)

// ---- admin helpers -------------------------------------------------------

func (c *client) trashList(q url.Values) (*server.TrashList, *apiError) {
	c.e.t.Helper()
	var l server.TrashList
	p := "/admin/trash"
	if len(q) > 0 {
		p += "?" + q.Encode()
	}
	return &l, c.do("GET", p, nil, &l)
}

func (c *client) mustTrashList(q url.Values) *server.TrashList {
	c.e.t.Helper()
	l, err := c.trashList(q)
	if err != nil {
		c.e.t.Fatal(err)
	}
	return l
}

func (c *client) restore(id int64, to string) (*server.RestoreResult, *apiError) {
	c.e.t.Helper()
	var res server.RestoreResult
	return &res, c.do("POST", "/admin/trash/"+strconv.FormatInt(id, 10)+"/restore", httpapi.RestoreRequest{To: to}, &res)
}

func (c *client) mustRestore(id int64, to string) *server.RestoreResult {
	c.e.t.Helper()
	res, err := c.restore(id, to)
	if err != nil {
		c.e.t.Fatal(err)
	}
	return res
}

func (c *client) purge(req server.PurgeRequest) (*server.PurgeResult, *apiError) {
	c.e.t.Helper()
	var res server.PurgeResult
	return &res, c.do("POST", "/admin/trash/purge", req, &res)
}

func (c *client) mustPurge(req server.PurgeRequest) *server.PurgeResult {
	c.e.t.Helper()
	res, err := c.purge(req)
	if err != nil {
		c.e.t.Fatal(err)
	}
	return res
}

func (c *client) verify(repair bool) *server.VerifyResult {
	c.e.t.Helper()
	var res server.VerifyResult
	if err := c.do("POST", "/admin/verify", httpapi.VerifyRequest{Repair: repair}, &res); err != nil {
		c.e.t.Fatal(err)
	}
	return &res
}

func trashPaths(l *server.TrashList) []string {
	var out []string
	for _, e := range l.Entries {
		out = append(out, e.OrigPath+" "+e.Reason+" "+e.By)
	}
	return out
}

func (c *client) del(p, base string) *server.Result {
	c.e.t.Helper()
	return c.mustOp(server.OpRequest{Op: server.OpDelete, Path: p, BaseHash: base})
}

// ---- trash list ----------------------------------------------------------

func TestTrashListFilters(t *testing.T) {
	e := newEnv(t)
	bob := e.member("bob")
	v1, v2 := []byte("v1\n"), []byte("v2-longer\n")

	e.ana.mustPut("docs/a.txt", "", v1)
	e.ana.mustPut("docs/b.txt", "", v1)
	e.ana.mustPut("x.txt", "", v1)
	e.clock.Advance(48 * time.Hour)
	bob.mustPut("docs/a.txt", hashOf(v1), v2) // a v1 replaced by bob
	bob.del("x.txt", hashOf(v1))              // x deleted by bob
	e.ana.del("docs/b.txt", hashOf(v1))       // b deleted by ana

	all := e.ana.mustTrashList(nil)
	eqList(t, "all", trashPaths(all), []string{"docs/a.txt replaced bob", "x.txt deleted bob", "docs/b.txt deleted ana"})
	eq(t, "count", all.Count, 3)
	eq(t, "total size", all.TotalSize, int64(3*len(v1)))
	for _, en := range all.Entries {
		eq(t, en.TrashName+" at", en.At.Equal(t0.Add(48*time.Hour)), true)
		eq(t, en.TrashName+" hash", en.Hash, hashOf(v1))
	}

	for _, tc := range []struct {
		q    url.Values
		want []string
	}{
		{url.Values{"path": {"docs/"}}, []string{"docs/a.txt replaced bob", "docs/b.txt deleted ana"}},
		{url.Values{"path": {"docs/b"}}, []string{"docs/b.txt deleted ana"}},
		{url.Values{"by": {"bob"}}, []string{"docs/a.txt replaced bob", "x.txt deleted bob"}},
		{url.Values{"reason": {"deleted"}}, []string{"x.txt deleted bob", "docs/b.txt deleted ana"}},
		{url.Values{"reason": {"replaced"}, "by": {"bob"}}, []string{"docs/a.txt replaced bob"}},
		{url.Values{"since": {"2026-11-25"}}, []string{"docs/a.txt replaced bob", "x.txt deleted bob", "docs/b.txt deleted ana"}},
		{url.Values{"since": {"2026-11-26"}}, nil},
		{url.Values{"since": {t0.Add(48 * time.Hour).Format(time.RFC3339)}}, []string{"docs/a.txt replaced bob", "x.txt deleted bob", "docs/b.txt deleted ana"}},
		{url.Values{"by": {"cecilija"}}, nil},
	} {
		l := e.ana.mustTrashList(tc.q)
		eqList(t, tc.q.Encode(), trashPaths(l), tc.want)
		eq(t, tc.q.Encode()+" count", l.Count, len(tc.want))
	}
	_, err := e.ana.trashList(url.Values{"reason": {"lost"}})
	wantErr(t, err, http.StatusBadRequest, server.CodeInvalidRequest)
	_, err = e.ana.trashList(url.Values{"since": {"yesterday"}})
	wantErr(t, err, http.StatusBadRequest, server.CodeInvalidRequest)
}

// ---- restore -------------------------------------------------------------

func TestRestoreToFreePath(t *testing.T) {
	e := newEnv(t)
	v1 := []byte("prva verzija\n")
	e.ana.mustPut("a.txt", "", v1)
	mtime := filepathMtime(t, e, "storage/a.txt")
	e.ana.del("a.txt", hashOf(v1))
	id := e.ana.mustTrashList(nil).Entries[0].ID

	res := e.ana.mustRestore(id, "")
	eq(t, "status", res.Status, server.StatusApplied)
	eq(t, "path", res.Path, "a.txt")
	eq(t, "renamed", res.Renamed, false)
	eqList(t, "events", eventOps(res.Events), []string{"3 restore a.txt"})
	ev := res.Events[0]
	eq(t, "event hash", ev.Hash, hashOf(v1))
	eq(t, "event size", ev.Size, int64(len(v1)))
	eq(t, "event mtime", ev.Mtime, mtime)
	eq(t, "event trash_id", *ev.TrashID, id)
	eq(t, "event member", ev.Member, "ana")
	eq(t, "storage", e.read("storage", "a.txt"), string(v1))
	eq(t, "files row", e.files()["a.txt"].Hash, hashOf(v1))
	eq(t, "restored mtime", filepathMtime(t, e, "storage/a.txt"), mtime)

	// Restoring copies: the entry stays and can be restored again.
	eq(t, "trash after restore", e.ana.mustTrashList(nil).Count, 1)
	eqList(t, "tmp is empty", e.tree("tmp"), nil)

	// The path has this content now: nothing to do.
	res = e.ana.mustRestore(id, "")
	eq(t, "second restore", res.Status, server.StatusUnchanged)
	eq(t, "no event", len(res.Events), 0)
	eqList(t, "tmp is still empty", e.tree("tmp"), nil)

	// --to puts it elsewhere; old_path says where it came from.
	res = e.ana.mustRestore(id, "kopije/a.txt")
	eqList(t, "restore --to", eventOps(res.Events), []string{"4 mkdir kopije", "5 restore kopije/a.txt <- a.txt"})
	eq(t, "copy", e.read("storage", "kopije/a.txt"), string(v1))

	// A client sees restore like any other change.
	ch, apiErr := e.ana.changes(2, 100)
	if apiErr != nil {
		t.Fatal(apiErr)
	}
	eqList(t, "changes", eventOps(ch.Events), []string{"3 restore a.txt", "4 mkdir kopije", "5 restore kopije/a.txt <- a.txt"})
	e.checkLogMatchesEvents()
}

func filepathMtime(t *testing.T, e *env, rel string) int64 {
	t.Helper()
	fi, err := os.Stat(filepath.Join(e.dir(), filepath.FromSlash(rel)))
	if err != nil {
		t.Fatal(err)
	}
	return fi.ModTime().UnixNano()
}

func TestRestoreOntoDifferentFile(t *testing.T) {
	e := newEnv(t)
	bob := e.member("bob")
	v1, v2 := []byte("stara\n"), []byte("nova od boba\n")
	e.ana.mustPut("docs/plan.txt", "", v1)
	e.ana.del("docs/plan.txt", hashOf(v1))
	bob.mustPut("docs/plan.txt", "", v2)
	id := e.ana.mustTrashList(nil).Entries[0].ID
	before := e.files()["docs/plan.txt"]

	res := e.ana.mustRestore(id, "")
	want := "docs/plan.restored-" + ts0 + ".txt"
	eq(t, "status", res.Status, server.StatusApplied)
	eq(t, "renamed", res.Renamed, true)
	eq(t, "path", res.Path, want)
	eqList(t, "events", eventOps(res.Events), []string{"5 restore " + want + " <- docs/plan.txt"})
	eq(t, "restored copy", e.read("storage", want), string(v1))
	eq(t, "existing file untouched", e.read("storage", "docs/plan.txt"), string(v2))
	eq(t, "existing row untouched", e.files()["docs/plan.txt"], before)

	// Again in the same second: the next free name.
	res = e.ana.mustRestore(id, "")
	eq(t, "second copy", res.Path, "docs/plan.restored-"+ts0+"-2.txt")

	// A directory in the way counts as other content.
	e.ana.mustOp(server.OpRequest{Op: server.OpMkdir, Path: "mapa"})
	res = e.ana.mustRestore(id, "mapa")
	eq(t, "onto a directory", res.Path, "mapa.restored-"+ts0)
	eqList(t, "tree", e.tree("storage"), []string{
		"docs/", "docs/plan.restored-" + ts0 + "-2.txt", "docs/plan.restored-" + ts0 + ".txt", "docs/plan.txt",
		"mapa.restored-" + ts0, "mapa/",
	})
	e.checkLogMatchesEvents()
}

func TestRestoreIntoGoneDirectory(t *testing.T) {
	e := newEnv(t)
	v := []byte("duboko\n")
	e.ana.mustPut("a/b/c/f.txt", "", v)
	e.ana.del("a/b/c/f.txt", hashOf(v))
	for _, d := range []string{"a/b/c", "a/b", "a"} {
		e.ana.mustOp(server.OpRequest{Op: server.OpRmdir, Path: d})
	}
	eqList(t, "storage empty", e.tree("storage"), nil)
	id := e.ana.mustTrashList(nil).Entries[0].ID

	res := e.ana.mustRestore(id, "")
	eqList(t, "events", eventOps(res.Events), []string{"9 mkdir a", "10 mkdir a/b", "11 mkdir a/b/c", "12 restore a/b/c/f.txt"})
	eqList(t, "tree", e.tree("storage"), []string{"a/", "a/b/", "a/b/c/", "a/b/c/f.txt"})
	eq(t, "dir rows", e.files()["a/b"].Kind, "dir")
	e.checkLogMatchesEvents()
}

func TestRestoreEachOfManyDeletions(t *testing.T) {
	e := newEnv(t)
	versions := [][]byte{[]byte("jedan\n"), []byte("dva\n"), []byte("tri\n")}
	for _, v := range versions {
		e.ana.mustPut("isti.txt", "", v)
		e.ana.del("isti.txt", hashOf(v)) // same second: trash names get -2, -3
	}
	l := e.ana.mustTrashList(nil)
	if l.Count != 3 {
		t.Fatalf("trash has %d entries, want 3", l.Count)
	}
	eq(t, "names", l.Entries[2].TrashName, "isti.txt.deleted-by-ana-"+ts0+"-3")
	for i, en := range l.Entries {
		dest := "vraceno/" + strconv.Itoa(i) + ".txt"
		res := e.ana.mustRestore(en.ID, dest)
		eq(t, "path", res.Path, dest)
		eq(t, dest, e.read("storage", dest), string(versions[i]))
	}
	// Back at the original path, in turn: the first takes the name, the
	// others become restored copies.
	eq(t, "first", e.ana.mustRestore(l.Entries[0].ID, "").Path, "isti.txt")
	eq(t, "second", e.ana.mustRestore(l.Entries[1].ID, "").Path, "isti.restored-"+ts0+".txt")
	eq(t, "third", e.ana.mustRestore(l.Entries[2].ID, "").Path, "isti.restored-"+ts0+"-2.txt")
	eq(t, "third content", e.read("storage", "isti.restored-"+ts0+"-2.txt"), string(versions[2]))
}

func TestRestoreErrors(t *testing.T) {
	e := newEnv(t)
	v := []byte("x\n")
	e.ana.mustPut("f.txt", "", v)
	e.ana.mustPut("dat", "", v)
	e.ana.del("f.txt", hashOf(v))
	id := e.ana.mustTrashList(nil).Entries[0].ID

	_, err := e.ana.restore(999, "")
	wantErr(t, err, http.StatusNotFound, server.CodeUnknownTrash)
	_, err = e.ana.restore(id, "../van.txt")
	wantErr(t, err, http.StatusBadRequest, server.CodeInvalidPath)
	_, err = e.ana.restore(id, "dat/f.txt") // a parent is a file
	wantErr(t, err, http.StatusConflict, server.CodeParentNotDir)

	// The trash file changed behind the database's back.
	en := e.ana.mustTrashList(nil).Entries[0]
	os.WriteFile(filepath.Join(e.dir(), "trash", en.TrashName), []byte("other"), 0o600)
	_, err = e.ana.restore(id, "")
	wantErr(t, err, http.StatusConflict, server.CodeTrashDamaged)
	os.Remove(filepath.Join(e.dir(), "trash", en.TrashName))
	_, err = e.ana.restore(id, "")
	wantErr(t, err, http.StatusConflict, server.CodeTrashDamaged)
	eqList(t, "nothing left in tmp", e.tree("tmp"), nil)
	eqList(t, "storage unchanged", e.tree("storage"), []string{"dat"})
}

// ---- purge ---------------------------------------------------------------

func TestPurge(t *testing.T) {
	e := newEnv(t)
	bob := e.member("bob")
	old, mid, fresh := []byte("star\n"), []byte("srednji\n"), []byte("svez fajl\n")
	e.ana.mustPut("old.txt", "", old)
	e.ana.del("old.txt", hashOf(old)) // t0
	e.clock.Advance(20 * 24 * time.Hour)
	e.ana.mustPut("mid.txt", "", mid)
	bob.del("mid.txt", hashOf(mid)) // t0+20d
	e.clock.Advance(9 * 24 * time.Hour)
	e.ana.mustPut("fresh.txt", "", fresh)
	e.ana.del("fresh.txt", hashOf(fresh)) // t0+29d
	e.clock.Advance(24 * time.Hour)       // now t0+30d
	l := e.ana.mustTrashList(nil)
	ids := map[string]int64{}
	for _, en := range l.Entries {
		ids[en.OrigPath] = en.ID
	}
	headBefore := len(e.events())

	// Dry run: lists and changes nothing.
	res := e.ana.mustPurge(server.PurgeRequest{OlderThan: "7d", DryRun: true})
	eq(t, "dry run", res.DryRun, true)
	eqList(t, "dry run selection", trashPaths(&res.TrashList), []string{"old.txt deleted ana", "mid.txt deleted bob"})
	eq(t, "dry run size", res.TotalSize, int64(len(old)+len(mid)))
	eq(t, "no events", len(e.events()), headBefore)
	eq(t, "trash intact", len(e.tree("trash")), 3)
	eq(t, "list intact", e.ana.mustTrashList(nil).Count, 3)
	dry := e.ana.mustPurge(server.PurgeRequest{All: true, DryRun: true})
	eq(t, "dry run all", dry.Count, 3)
	eq(t, "still intact", len(e.tree("trash")), 3)

	// By age.
	res = e.ana.mustPurge(server.PurgeRequest{OlderThan: "25d"})
	eqList(t, "older than 25d", trashPaths(&res.TrashList), []string{"old.txt deleted ana"})
	eq(t, "one event", len(res.Events), 1)
	ev := res.Events[0]
	eq(t, "op", ev.Op, server.OpPurge)
	eq(t, "path", ev.Path, "old.txt")
	eq(t, "hash", ev.Hash, hashOf(old))
	eq(t, "size", ev.Size, int64(len(old)))
	eq(t, "member", ev.Member, "ana")
	eq(t, "trash_id", *ev.TrashID, ids["old.txt"])
	eqList(t, "trash after age purge", trashPaths(e.ana.mustTrashList(nil)), []string{"mid.txt deleted bob", "fresh.txt deleted ana"})
	eq(t, "files on disk", len(e.tree("trash")), 2)
	_, err := e.ana.restore(ids["old.txt"], "")
	wantErr(t, err, http.StatusNotFound, server.CodeUnknownTrash)

	// By id.
	res = e.ana.mustPurge(server.PurgeRequest{IDs: []int64{ids["fresh.txt"]}})
	eqList(t, "by id", trashPaths(&res.TrashList), []string{"fresh.txt deleted ana"})
	eqList(t, "trash after id purge", trashPaths(e.ana.mustTrashList(nil)), []string{"mid.txt deleted bob"})
	_, err = e.ana.purge(server.PurgeRequest{IDs: []int64{ids["fresh.txt"]}})
	wantErr(t, err, http.StatusNotFound, server.CodeUnknownTrash)

	// Selection errors.
	for _, bad := range []server.PurgeRequest{{}, {All: true, OlderThan: "1d"}, {OlderThan: "soon"}, {OlderThan: "-1h"}} {
		_, err = e.ana.purge(bad)
		wantErr(t, err, http.StatusBadRequest, server.CodeInvalidRequest)
	}

	// All.
	res = e.ana.mustPurge(server.PurgeRequest{All: true})
	eq(t, "all", res.Count, 1)
	eq(t, "trash empty", e.ana.mustTrashList(nil).Count, 0)
	eqList(t, "trash dir empty", e.tree("trash"), nil)
	eq(t, "nothing left", e.ana.mustPurge(server.PurgeRequest{All: true}).Count, 0)

	// The purge does not touch storage/ and a client ignores the events.
	ch, apiErr := bob.changes(0, 100)
	if apiErr != nil {
		t.Fatal(apiErr)
	}
	purges := 0
	for _, ev := range ch.Events {
		if ev.Op == server.OpPurge {
			purges++
		}
	}
	eq(t, "purge events", purges, 3)
	// A trash name of a purged entry is not handed out again.
	e.ana.mustPut("old.txt", "", old)
	e.clock.Advance(-30 * 24 * time.Hour)
	e.ana.del("old.txt", hashOf(old))
	eq(t, "new entry name", e.ana.mustTrashList(nil).Entries[0].TrashName, "old.txt.deleted-by-ana-"+ts0+"-2")
	e.checkLogMatchesEvents()
}

func TestParseAge(t *testing.T) {
	for in, want := range map[string]time.Duration{"36h": 36 * time.Hour, "30d": 30 * 24 * time.Hour, "2w": 14 * 24 * time.Hour, "90m": 90 * time.Minute, "0d": 0} {
		got, err := server.ParseAge(in)
		if err != nil || got != want {
			t.Errorf("ParseAge(%q) = %v, %v; want %v", in, got, err, want)
		}
	}
	for _, in := range []string{"", "d", "1.5d", "-2d", "-1h", "tomorrow"} {
		if _, err := server.ParseAge(in); err == nil {
			t.Errorf("ParseAge(%q) succeeded", in)
		}
	}
}

// ---- verify --------------------------------------------------------------

func issueKinds(res *server.VerifyResult) []string {
	var out []string
	for _, is := range res.Issues {
		s := is.Area + " " + is.Kind + " " + is.Path
		if is.Repaired {
			s += " repaired"
		}
		out = append(out, s)
	}
	return out
}

func TestVerifyCleanFolder(t *testing.T) {
	e := newEnv(t)
	v := []byte("ok\n")
	e.ana.mustPut("a/b.txt", "", v)
	e.ana.mustPut("a/c.txt", "", v)
	e.ana.del("a/c.txt", hashOf(v))
	res := e.ana.verify(false)
	eqList(t, "issues", issueKinds(res), nil)
	eq(t, "files", res.Checked.Files, 1)
	eq(t, "dirs", res.Checked.Dirs, 1)
	eq(t, "trash", res.Checked.Trash, 1)
}

func TestVerifyStorage(t *testing.T) {
	e := newEnv(t)
	bob := e.member("bob")
	keep, gone, changed := []byte("ostaje\n"), []byte("nestaje\n"), []byte("menja se\n")
	e.ana.mustPut("keep.txt", "", keep)
	e.ana.mustPut("d/gone.txt", "", gone)
	e.ana.mustPut("changed.txt", "", changed)
	e.ana.mustOp(server.OpRequest{Op: server.OpMkdir, Path: "prazan"})
	head := int64(len(e.events()))
	st := filepath.Join(e.dir(), "storage")

	// 1. on disk, not in the database (a file in a new directory too)
	os.WriteFile(filepath.Join(st, "extra.txt"), []byte("doneo neko\n"), 0o600)
	os.MkdirAll(filepath.Join(st, "novi", "dublje"), 0o700)
	os.WriteFile(filepath.Join(st, "novi", "dublje", "f.txt"), []byte("f\n"), 0o600)
	os.WriteFile(filepath.Join(st, "CON"), []byte("windows ga ne da\n"), 0o600)
	// 2. in the database, not on disk
	os.Remove(filepath.Join(st, "d", "gone.txt"))
	os.Remove(filepath.Join(st, "prazan"))
	// 3. other content
	os.WriteFile(filepath.Join(st, "changed.txt"), []byte("izmenjeno na disku\n"), 0o600)

	res := e.ana.verify(false)
	want := []string{
		"storage missing d/gone.txt",
		"storage missing prazan",
		"storage mismatch changed.txt",
		"storage untracked CON",
		"storage untracked extra.txt",
		"storage untracked novi",
		"storage untracked novi/dublje",
		"storage untracked novi/dublje/f.txt",
	}
	eqList(t, "found", issueKinds(res), want)
	eq(t, "no events without repair", int64(len(e.events())), head)
	eq(t, "unrepaired", res.Unrepaired(), len(want))

	// A member without the admin role cannot repair.
	wantErr(t, bob.do("POST", "/admin/verify", httpapi.VerifyRequest{Repair: true}, nil), http.StatusForbidden, server.CodeForbidden)
	eq(t, "still no events", int64(len(e.events())), head)

	res = e.ana.verify(true)
	eqList(t, "repaired", issueKinds(res), []string{
		"storage missing d/gone.txt repaired",
		"storage missing prazan repaired",
		"storage mismatch changed.txt repaired",
		"storage untracked CON", // a name the folder cannot hold is left alone
		"storage untracked extra.txt repaired",
		"storage untracked novi repaired",
		"storage untracked novi/dublje repaired",
		"storage untracked novi/dublje/f.txt repaired",
	})
	n := head
	next := func() string { n++; return strconv.FormatInt(n, 10) }
	eqList(t, "events", eventOps(res.Events), []string{
		next() + " delete d/gone.txt",
		next() + " rmdir prazan",
		next() + " put changed.txt",
		next() + " put extra.txt",
		next() + " mkdir novi",
		next() + " mkdir novi/dublje",
		next() + " put novi/dublje/f.txt",
	})
	for _, ev := range res.Events {
		eq(t, ev.Path+" member", ev.Member, "ana")
		eq(t, ev.Path+" trash_id", ev.TrashID == nil, true)
	}
	eq(t, "delete keeps the lost hash", res.Events[0].PrevHash, hashOf(gone))
	eq(t, "put has the old hash", res.Events[2].PrevHash, hashOf(changed))
	eq(t, "put has the new hash", res.Events[2].Hash, hashOf([]byte("izmenjeno na disku\n")))
	for _, is := range res.Issues {
		switch is.Path {
		case "d/gone.txt", "changed.txt":
			if is.Warning == "" {
				t.Errorf("%s: no warning about the lost version", is.Path)
			}
		}
	}
	eq(t, "no trash entry", len(e.trash()), 0)
	f := e.files()
	eq(t, "extra row", f["extra.txt"].UpdatedBy, "ana")
	eq(t, "changed row", f["changed.txt"].Hash, hashOf([]byte("izmenjeno na disku\n")))
	_, ok := f["d/gone.txt"]
	eq(t, "gone row", ok, false)

	// Only the name the folder cannot hold is still there.
	eqList(t, "after repair", issueKinds(e.ana.verify(false)), []string{"storage untracked CON"})
	ch, apiErr := bob.changes(head, 100)
	if apiErr != nil {
		t.Fatal(apiErr)
	}
	eq(t, "clients see the repairs", len(ch.Events), 7)
	e.checkLogMatchesEvents()
	repairs := 0
	for _, l := range e.logLines() {
		if strings.Contains(l, " verify-repair ") && strings.Contains(l, "member=ana") {
			repairs++
		}
	}
	eq(t, "service.log repair lines", repairs, 7)
}

func TestVerifyKindMismatch(t *testing.T) {
	e := newEnv(t)
	v := []byte("x\n")
	e.ana.mustPut("a", "", v)
	e.ana.mustPut("b/c.txt", "", v)
	st := filepath.Join(e.dir(), "storage")
	// "a" becomes a directory, "b" a file.
	os.Remove(filepath.Join(st, "a"))
	os.MkdirAll(filepath.Join(st, "a"), 0o700)
	os.WriteFile(filepath.Join(st, "a", "in.txt"), v, 0o600)
	os.RemoveAll(filepath.Join(st, "b"))
	os.WriteFile(filepath.Join(st, "b"), v, 0o600)

	res := e.ana.verify(true)
	eqList(t, "repairs", eventOps(res.Events), []string{
		"4 delete b/c.txt", "5 delete a", "6 rmdir b", "7 mkdir a", "8 put a/in.txt", "9 put b",
	})
	eq(t, "all repaired", res.Unrepaired(), 0)
	eqList(t, "clean", issueKinds(e.ana.verify(false)), nil)
}

func TestVerifyTrashAndTmp(t *testing.T) {
	e := newEnv(t)
	v1, v2, v3 := []byte("1\n"), []byte("22\n"), []byte("333\n")
	e.ana.mustPut("docs/a.txt", "", v1)
	e.ana.del("docs/a.txt", hashOf(v1))
	e.ana.mustPut("b.txt", "", v2)
	e.ana.del("b.txt", hashOf(v2))
	tr := e.trash()
	trashDir := filepath.Join(e.dir(), "trash")
	tmpDir := filepath.Join(e.dir(), "tmp")

	// trash: a file without a row (a name TrashName makes), a row without
	// a file, a file with other content
	stray := "c.txt.replaced-by-bob-20261120T101010Z"
	os.WriteFile(filepath.Join(trashDir, stray), v3, 0o600)
	os.Remove(filepath.Join(trashDir, tr[0].Name))
	os.WriteFile(filepath.Join(trashDir, tr[1].Name), []byte("pokvareno\n"), 0o600)

	// tmp: an old leftover, an old file of an open upload, a fresh leftover
	ar, apiErr := e.ana.announce("u.txt", "", v1)
	if apiErr != nil {
		t.Fatal(apiErr)
	}
	old := t0.Add(-25 * time.Hour)
	for _, name := range []string{"0123456789abcdef0123456789abcdef.commit", ar.UploadID, "." + ar.UploadID + ".Xy12Ab"} {
		os.WriteFile(filepath.Join(tmpDir, name), v1, 0o600)
		os.Chtimes(filepath.Join(tmpDir, name), old, old)
	}
	os.MkdirAll(filepath.Join(tmpDir, "move-7"), 0o700)
	os.Chtimes(filepath.Join(tmpDir, "move-7"), old, old)
	os.WriteFile(filepath.Join(tmpDir, "svez"), v1, 0o600)
	os.Chtimes(filepath.Join(tmpDir, "svez"), t0, t0)

	res := e.ana.verify(false)
	found := []string{
		"trash missing " + tr[0].Name,
		"trash mismatch " + tr[1].Name,
		"trash untracked " + stray,
		"tmp stale_tmp 0123456789abcdef0123456789abcdef.commit",
		"tmp stale_tmp move-7",
	}
	eqList(t, "found", issueKinds(res), found)

	res = e.ana.verify(true)
	for i := range found {
		found[i] += " repaired"
	}
	eqList(t, "repaired", issueKinds(res), found)
	eqList(t, "events", eventOps(res.Events), []string{"6 purge docs/a.txt"})
	eq(t, "purge event hash", res.Events[0].Hash, hashOf(v1))

	l := e.ana.mustTrashList(nil)
	eqList(t, "trash list", trashPaths(l), []string{"b.txt deleted ana", "c.txt replaced bob"})
	eq(t, "mismatch takes the disk's hash", l.Entries[0].Hash, hashOf([]byte("pokvareno\n")))
	eq(t, "adopted hash", l.Entries[1].Hash, hashOf(v3))
	eq(t, "adopted time", l.Entries[1].At.Equal(time.Date(2026, 11, 20, 10, 10, 10, 0, time.UTC)), true)
	eq(t, "adopted entry restores", e.ana.mustRestore(l.Entries[1].ID, "").Path, "c.txt")
	eqList(t, "tmp keeps the open upload and fresh files", e.tree("tmp"), []string{"." + ar.UploadID + ".Xy12Ab", ar.UploadID, "svez"})
	eqList(t, "clean", issueKinds(e.ana.verify(false)), nil)
	e.checkLogMatchesEvents()
}

// ---- stats ---------------------------------------------------------------

func TestStats(t *testing.T) {
	e := newEnv(t)
	bob := e.member("bob")
	cec := e.member("cecilija")
	v1, v2 := []byte("12345"), []byte("1234567890")
	e.ana.mustPut("a.txt", "", v1)
	e.clock.Advance(time.Hour)
	bob.mustPut("d/b.txt", "", v2)
	bob.del("d/b.txt", hashOf(v2))
	bob.mustPut("d/c.txt", "", v1)
	if err := bob.do("POST", "/sync-report", server.SyncReport{StartedAt: t0, FinishedAt: t0, FromSeq: 0, ToSeq: 3}, nil); err != nil {
		t.Fatal(err)
	}
	sh, _ := e.srv.Share(e.share)
	if err := sh.RevokeMember("cecilija"); err != nil {
		t.Fatal(err)
	}
	_ = cec

	var st server.Stats
	if err := e.ana.do("GET", "/admin/stats", nil, &st); err != nil {
		t.Fatal(err)
	}
	eq(t, "head", st.Head, int64(5))
	eq(t, "files", st.Files, int64(2))
	eq(t, "files size", st.FilesSize, int64(10))
	eq(t, "dirs", st.Dirs, int64(1))
	eq(t, "trash", st.Trash, int64(1))
	eq(t, "trash size", st.TrashSize, int64(10))
	if len(st.Members) != 3 {
		t.Fatalf("members: %+v", st.Members)
	}
	ana, b, c := st.Members[0], st.Members[1], st.Members[2]
	eq(t, "ana role", ana.Role, "admin")
	eq(t, "bob role", b.Role, "member")
	eq(t, "bob acked", b.LastSeqAcked, int64(3))
	eq(t, "bob behind", b.Behind, int64(2))
	eq(t, "bob last push", b.LastPushAt != nil && b.LastPushAt.Equal(t0.Add(time.Hour)), true)
	eq(t, "bob last seen", b.LastSeenAt != nil, true)
	eq(t, "bob revoked", b.Revoked, false)
	eq(t, "cecilija revoked", c.Revoked, true)
	eq(t, "cecilija never seen", c.LastSeenAt == nil, true)

	// Admin operations are not pushes.
	e.clock.Advance(time.Hour)
	e.ana.mustPurge(server.PurgeRequest{All: true})
	if err := e.ana.do("GET", "/admin/stats", nil, &st); err != nil {
		t.Fatal(err)
	}
	eq(t, "purged", st.Purged, int64(1))
	eq(t, "trash after purge", st.Trash, int64(0))
	eq(t, "ana last push unchanged", st.Members[0].LastPushAt.Equal(t0), true)
}

// ---- authorization -------------------------------------------------------

func TestAdminRoutesNeedAdmin(t *testing.T) {
	e := newEnv(t)
	bob := e.member("bob")
	v := []byte("v\n")
	e.ana.mustPut("a.txt", "", v)
	e.ana.del("a.txt", hashOf(v))
	id := strconv.FormatInt(e.ana.mustTrashList(nil).Entries[0].ID, 10)
	routes := []struct {
		method, path string
		body         any
	}{
		{"GET", "/admin/trash", nil},
		{"GET", "/admin/trash?by=ana", nil},
		{"POST", "/admin/trash/" + id + "/restore", httpapi.RestoreRequest{}},
		{"POST", "/admin/trash/999/restore", nil},
		{"POST", "/admin/trash/purge", server.PurgeRequest{All: true}},
		{"POST", "/admin/trash/purge", server.PurgeRequest{All: true, DryRun: true}},
		{"POST", "/admin/verify", httpapi.VerifyRequest{Repair: true}},
		{"POST", "/admin/verify", nil},
		{"GET", "/admin/stats", nil},
	}
	for _, r := range routes {
		err := bob.do(r.method, r.path, r.body, nil)
		if err == nil || err.Status != http.StatusForbidden || err.Code != server.CodeForbidden {
			t.Errorf("member %s %s: got %v, want 403 forbidden", r.method, r.path, err)
		}
	}
	// No token, or a revoked admin: 401, as everywhere else.
	nobody := &client{e: e, share: e.share}
	sh, _ := e.srv.Share(e.share)
	tok, _ := sh.AddMember("staribos", true)
	boss := &client{e: e, share: e.share, user: "staribos", token: tok}
	if err := sh.RevokeMember("staribos"); err != nil {
		t.Fatal(err)
	}
	for _, c := range []*client{nobody, boss} {
		for _, r := range routes {
			if err := c.do(r.method, r.path, r.body, nil); err == nil || err.Status != http.StatusUnauthorized {
				t.Errorf("%q %s %s: got %v, want 401", c.user, r.method, r.path, err)
			}
		}
	}
	// Nothing changed.
	eq(t, "trash intact", len(e.tree("trash")), 1)
	eqList(t, "storage intact", e.tree("storage"), nil)
	// An admin added later may use them.
	tok2, _ := sh.AddMember("drugiadmin", true)
	second := &client{e: e, share: e.share, user: "drugiadmin", token: tok2}
	eq(t, "second admin lists", second.mustTrashList(nil).Count, 1)
}
