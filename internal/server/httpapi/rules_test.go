package httpapi_test

import (
	"net/http"
	"testing"
	"time"

	"github.com/digital-cube/shared-folder/internal/server"
)

var (
	v1 = []byte("verzija 1\n")
	v2 = []byte("verzija 2, duža\n")
	v3 = []byte("verzija 3\n")
)

// Rule 1: new_hash equal to the current one — no change, no event. Checked
// at the announce (nothing to send) and again at the commit, for an upload
// that became redundant while it was in flight.
func TestRule1SameContent(t *testing.T) {
	e := newEnv(t)
	bob := e.member("bob")
	e.ana.mustPut("a.txt", "", v1)

	ar, err := bob.announce("a.txt", hashOf(v1), v1)
	if err != nil {
		t.Fatal(err)
	}
	eq(t, "announce status", ar.Status, server.StatusUnchanged)
	eq(t, "upload_id", ar.UploadID, "")

	// In flight: bob announces v2 while ana already put v2.
	ar, err = bob.announce("a.txt", hashOf(v1), v2)
	if err != nil {
		t.Fatal(err)
	}
	eq(t, "announce status", ar.Status, server.StatusUpload)
	bob.send(ar, v2)
	e.ana.mustPut("a.txt", hashOf(v1), v2)
	res, err := bob.commit(ar.UploadID)
	if err != nil {
		t.Fatal(err)
	}
	eq(t, "commit status", res.Status, server.StatusUnchanged)
	eq(t, "commit events", len(res.Events), 0)

	eqList(t, "events", eventOps(e.events()), []string{"1 put a.txt", "2 put a.txt"})
	eqList(t, "storage", e.tree("storage"), []string{"a.txt"})
	eq(t, "storage a.txt", e.read("storage", "a.txt"), string(v2))
	eqList(t, "tmp", e.tree("tmp"), nil)
	eq(t, "files a.txt by", e.files()["a.txt"].UpdatedBy, "ana")
	e.checkLogMatchesEvents()
}

// Rule 2: base_hash equal to the current one — the old version goes to the
// trash as replaced, the new one takes the name; event put.
func TestRule2RegularEdit(t *testing.T) {
	e := newEnv(t)
	bob := e.member("bob")
	e.ana.mustPut("docs/plan.md", "", v1)
	res := bob.mustPut("docs/plan.md", hashOf(v1), v2)
	eq(t, "status", res.Status, server.StatusApplied)

	eq(t, "storage", e.read("storage", "docs/plan.md"), string(v2))
	trashName := "plan.md.replaced-by-bob-" + ts0
	eqList(t, "trash/", e.tree("trash"), []string{trashName})
	eq(t, "trash content", e.read("trash", trashName), string(v1))

	tr := e.trash()
	if len(tr) != 1 {
		t.Fatalf("trash rows = %d", len(tr))
	}
	eq(t, "trash.orig_path", tr[0].OrigPath, "docs/plan.md")
	eq(t, "trash.reason", tr[0].Reason, "replaced")
	eq(t, "trash.by_member", tr[0].ByMember, "bob")
	eq(t, "trash.hash", tr[0].Hash, hashOf(v1))
	eq(t, "trash.size", tr[0].Size, int64(len(v1)))

	evs := e.events()
	eqList(t, "events", eventOps(evs), []string{"1 mkdir docs", "2 put docs/plan.md", "3 put docs/plan.md"})
	last := evs[2]
	eq(t, "event member", last.Member, "bob")
	eq(t, "event hash", last.Hash, hashOf(v2))
	eq(t, "event prev_hash", last.PrevHash, hashOf(v1))
	if last.TrashID == nil || *last.TrashID != tr[0].ID {
		t.Errorf("event trash_id = %v, want %d", last.TrashID, tr[0].ID)
	}
	eq(t, "trash.event_seq", tr[0].EventSeq, last.Seq)

	f := e.files()["docs/plan.md"]
	eq(t, "files.hash", f.Hash, hashOf(v2))
	eq(t, "files.size", f.Size, int64(len(v2)))
	eq(t, "files.seq", f.Seq, int64(3))
	eq(t, "files.updated_by", f.UpdatedBy, "bob")
	eqList(t, "tmp", e.tree("tmp"), nil)
	e.checkLogMatchesEvents()
}

// Rule 3: base_hash different from the current one — the existing file
// stays and the upload becomes name.conflict-{user}-{ts}.ext; event
// conflict with both paths. Also when the client thinks the file is new.
func TestRule3Conflict(t *testing.T) {
	e := newEnv(t)
	bob := e.member("bob")
	e.ana.mustPut("slika.png", "", v1)
	e.ana.mustPut("slika.png", hashOf(v1), v2)

	// bob still has v1 and edits it.
	res := bob.mustPut("slika.png", hashOf(v1), v3)
	cp := "slika.conflict-bob-" + ts0 + ".png"
	eq(t, "status", res.Status, server.StatusConflict)
	eq(t, "conflict_path", res.ConflictPath, cp)

	// cecilija never saw the file and creates her own.
	e.clock.Advance(time.Second)
	cec := e.member("cecilija")
	v4 := []byte("cecilijin\n")
	res = cec.mustPut("slika.png", "", v4)
	cp2 := "slika.conflict-cecilija-20261123T145847Z.png"
	eq(t, "status (new)", res.Status, server.StatusConflict)
	eq(t, "conflict_path (new)", res.ConflictPath, cp2)

	eq(t, "original kept", e.read("storage", "slika.png"), string(v2))
	eq(t, "bob's copy", e.read("storage", cp), string(v3))
	eq(t, "cecilija's copy", e.read("storage", cp2), string(v4))
	eqList(t, "storage", e.tree("storage"), []string{cp, cp2, "slika.png"})
	eqList(t, "trash", e.tree("trash"), []string{"slika.png.replaced-by-ana-" + ts0})

	evs := e.events()
	eqList(t, "events", eventOps(evs), []string{
		"1 put slika.png", "2 put slika.png",
		"3 conflict " + cp + " <- slika.png",
		"4 conflict " + cp2 + " <- slika.png",
	})
	eq(t, "conflict hash", evs[2].Hash, hashOf(v3))
	eq(t, "conflict prev_hash (kept version)", evs[2].PrevHash, hashOf(v2))
	files := e.files()
	eq(t, "files original", files["slika.png"].Hash, hashOf(v2))
	eq(t, "files copy", files[cp].Hash, hashOf(v3))
	eq(t, "files copy by", files[cp].UpdatedBy, "bob")
	e.checkLogMatchesEvents()
}

// A second conflict copy in the same second gets "-2".
func TestRule3ConflictCopyNameTaken(t *testing.T) {
	e := newEnv(t)
	bob := e.member("bob")
	e.ana.mustPut("Makefile", "", v1)
	r1 := bob.mustPut("Makefile", "", v2)
	r2 := bob.mustPut("Makefile", "", v3)
	eq(t, "first", r1.ConflictPath, "Makefile.conflict-bob-"+ts0)
	eq(t, "second", r2.ConflictPath, "Makefile.conflict-bob-"+ts0+"-2")
}

// Rule 4: a file deleted on the server and edited by a client comes back
// with the new content; event put.
func TestRule4EditDeletedFile(t *testing.T) {
	e := newEnv(t)
	bob := e.member("bob")
	e.ana.mustPut("a.txt", "", v1)
	e.ana.mustOp(server.OpRequest{Op: "delete", Path: "a.txt", BaseHash: hashOf(v1)})

	res := bob.mustPut("a.txt", hashOf(v1), v2)
	eq(t, "status", res.Status, server.StatusApplied)
	eq(t, "storage", e.read("storage", "a.txt"), string(v2))
	eqList(t, "trash", e.tree("trash"), []string{"a.txt.deleted-by-ana-" + ts0})
	eq(t, "deleted version kept", e.read("trash", "a.txt.deleted-by-ana-"+ts0), string(v1))
	evs := e.events()
	eqList(t, "events", eventOps(evs), []string{"1 put a.txt", "2 delete a.txt", "3 put a.txt"})
	eq(t, "event member", evs[2].Member, "bob")
	eq(t, "files", e.files()["a.txt"].Hash, hashOf(v2))
	e.checkLogMatchesEvents()
}

// Rule 5: a delete whose base_hash is not the current content is refused
// with a clear code, and nothing changes.
func TestRule5DeleteModifiedFile(t *testing.T) {
	e := newEnv(t)
	bob := e.member("bob")
	e.ana.mustPut("a.txt", "", v1)
	e.ana.mustPut("a.txt", hashOf(v1), v2)
	evsBefore, logBefore := eventOps(e.events()), e.logLines()

	_, err := bob.op(server.OpRequest{Op: "delete", Path: "a.txt", BaseHash: hashOf(v1)})
	wantErr(t, err, http.StatusConflict, server.CodeStaleBase)

	eq(t, "storage", e.read("storage", "a.txt"), string(v2))
	eqList(t, "events", eventOps(e.events()), evsBefore)
	eqList(t, "trash", e.tree("trash"), []string{"a.txt.replaced-by-ana-" + ts0})
	eq(t, "trash rows", len(e.trash()), 1)
	eq(t, "files", e.files()["a.txt"].Hash, hashOf(v2))
	eqList(t, "service.log", e.logLines(), logBefore)
}

// Rule 6: a deleted file goes to trash/ as name.deleted-by-{user}-{ts}
// with a trash row keeping the original path; event delete. rmdir works
// only on an empty directory.
func TestRule6Delete(t *testing.T) {
	e := newEnv(t)
	bob := e.member("bob")
	e.ana.mustPut("projekat/izvestaj.pdf", "", v1)

	res := bob.mustOp(server.OpRequest{Op: "delete", Path: "projekat/izvestaj.pdf", BaseHash: hashOf(v1)})
	eq(t, "status", res.Status, server.StatusApplied)
	name := "izvestaj.pdf.deleted-by-bob-" + ts0
	eqList(t, "storage", e.tree("storage"), []string{"projekat/"})
	eqList(t, "trash", e.tree("trash"), []string{name})
	eq(t, "trash content", e.read("trash", name), string(v1))
	tr := e.trash()
	eq(t, "trash rows", len(tr), 1)
	eq(t, "trash.orig_path", tr[0].OrigPath, "projekat/izvestaj.pdf")
	eq(t, "trash.trash_name", tr[0].Name, name)
	eq(t, "trash.reason", tr[0].Reason, "deleted")
	eq(t, "trash.by_member", tr[0].ByMember, "bob")
	evs := e.events()
	del := evs[len(evs)-1]
	eq(t, "event op", del.Op, "delete")
	eq(t, "event prev_hash", del.PrevHash, hashOf(v1))
	if del.TrashID == nil || *del.TrashID != tr[0].ID {
		t.Errorf("event trash_id = %v, want %d", del.TrashID, tr[0].ID)
	}
	if _, ok := e.files()["projekat/izvestaj.pdf"]; ok {
		t.Error("deleted file still in files")
	}

	// Deleting what is already gone changes nothing.
	res = e.ana.mustOp(server.OpRequest{Op: "delete", Path: "projekat/izvestaj.pdf", BaseHash: hashOf(v1)})
	eq(t, "repeat delete", res.Status, server.StatusUnchanged)

	// A directory is not deleted with delete, and rmdir needs it empty.
	e.ana.mustPut("projekat/b.txt", "", v2)
	_, err := e.ana.op(server.OpRequest{Op: "delete", Path: "projekat"})
	wantErr(t, err, http.StatusConflict, server.CodeIsDir)
	_, err = e.ana.op(server.OpRequest{Op: "rmdir", Path: "projekat"})
	wantErr(t, err, http.StatusConflict, server.CodeDirNotEmpty)
	eqList(t, "storage", e.tree("storage"), []string{"projekat/", "projekat/b.txt"})

	e.ana.mustOp(server.OpRequest{Op: "delete", Path: "projekat/b.txt", BaseHash: hashOf(v2)})
	res = e.ana.mustOp(server.OpRequest{Op: "rmdir", Path: "projekat"})
	eq(t, "rmdir", res.Status, server.StatusApplied)
	eqList(t, "storage", e.tree("storage"), nil)
	eqList(t, "trash", e.tree("trash"), []string{"b.txt.deleted-by-ana-" + ts0, name})
	eq(t, "files", len(e.files()), 0)
	eqList(t, "events", eventOps(e.events()), []string{
		"1 mkdir projekat", "2 put projekat/izvestaj.pdf", "3 delete projekat/izvestaj.pdf",
		"4 put projekat/b.txt", "5 delete projekat/b.txt", "6 rmdir projekat",
	})
	e.checkLogMatchesEvents()
}

// Rule 7: move with base_hash, without content; refused when the target
// exists with other content or when the base is stale.
func TestRule7Move(t *testing.T) {
	e := newEnv(t)
	e.ana.mustPut("a.txt", "", v1)
	e.ana.mustPut("c.txt", "", v2)

	res := e.ana.mustOp(server.OpRequest{Op: "move", Path: "a.txt", NewPath: "arhiva/2026/a.txt", BaseHash: hashOf(v1)})
	eq(t, "status", res.Status, server.StatusApplied)
	eqList(t, "storage", e.tree("storage"), []string{"arhiva/", "arhiva/2026/", "arhiva/2026/a.txt", "c.txt"})
	eq(t, "moved content", e.read("storage", "arhiva/2026/a.txt"), string(v1))
	eqList(t, "tmp (no content transferred)", e.tree("tmp"), nil)
	eqList(t, "trash", e.tree("trash"), nil)

	files := e.files()
	if _, ok := files["a.txt"]; ok {
		t.Error("old path still in files")
	}
	eq(t, "files new", files["arhiva/2026/a.txt"].Hash, hashOf(v1))

	// Stale base.
	_, err := e.ana.op(server.OpRequest{Op: "move", Path: "c.txt", NewPath: "d.txt", BaseHash: hashOf(v1)})
	wantErr(t, err, http.StatusConflict, server.CodeStaleBase)
	// Target exists with other content.
	_, err = e.ana.op(server.OpRequest{Op: "move", Path: "c.txt", NewPath: "arhiva/2026/a.txt", BaseHash: hashOf(v2)})
	wantErr(t, err, http.StatusConflict, server.CodeDestExists)
	// Missing source.
	_, err = e.ana.op(server.OpRequest{Op: "move", Path: "nema.txt", NewPath: "x.txt", BaseHash: hashOf(v2)})
	wantErr(t, err, http.StatusNotFound, server.CodeNotFound)
	eq(t, "c.txt untouched", e.read("storage", "c.txt"), string(v2))

	// A case-only rename works.
	e.ana.mustOp(server.OpRequest{Op: "move", Path: "c.txt", NewPath: "C.txt", BaseHash: hashOf(v2)})
	eqList(t, "storage", e.tree("storage"), []string{"C.txt", "arhiva/", "arhiva/2026/", "arhiva/2026/a.txt"})

	evs := e.events()
	eqList(t, "events", eventOps(evs), []string{
		"1 put a.txt", "2 put c.txt",
		"3 mkdir arhiva", "4 mkdir arhiva/2026", "5 move arhiva/2026/a.txt <- a.txt",
		"6 move C.txt <- c.txt",
	})
	eq(t, "move hash", evs[4].Hash, hashOf(v1))
	e.checkLogMatchesEvents()
}

// Moving onto a file with the same content leaves the target and sends the
// source to the trash: nothing is lost and both sides end equal.
func TestRule7MoveOntoSameContent(t *testing.T) {
	e := newEnv(t)
	e.ana.mustPut("a.txt", "", v1)
	e.ana.mustPut("b.txt", "", v1)
	e.ana.mustOp(server.OpRequest{Op: "move", Path: "a.txt", NewPath: "b.txt", BaseHash: hashOf(v1)})
	eqList(t, "storage", e.tree("storage"), []string{"b.txt"})
	eqList(t, "trash", e.tree("trash"), []string{"a.txt.deleted-by-ana-" + ts0})
	eqList(t, "events", eventOps(e.events()), []string{"1 put a.txt", "2 put b.txt", "3 delete a.txt"})
}

// Rule 8: mkdir and rmdir are events, so empty directories exist in files;
// an upload into a missing directory creates the parents with a mkdir each.
func TestRule8Directories(t *testing.T) {
	e := newEnv(t)
	res := e.ana.mustOp(server.OpRequest{Op: "mkdir", Path: "prazan/ugnjezden"})
	eq(t, "status", res.Status, server.StatusApplied)
	eqList(t, "mkdir events", eventOps(res.Events), []string{"1 mkdir prazan", "2 mkdir prazan/ugnjezden"})
	res = e.ana.mustOp(server.OpRequest{Op: "mkdir", Path: "prazan"})
	eq(t, "mkdir existing", res.Status, server.StatusUnchanged)

	res = e.ana.mustPut("a/b/c/d.txt", "", v1)
	eqList(t, "put events", eventOps(res.Events), []string{"3 mkdir a", "4 mkdir a/b", "5 mkdir a/b/c", "6 put a/b/c/d.txt"})

	eqList(t, "storage", e.tree("storage"), []string{"a/", "a/b/", "a/b/c/", "a/b/c/d.txt", "prazan/", "prazan/ugnjezden/"})
	files := e.files()
	for _, d := range []string{"prazan", "prazan/ugnjezden", "a", "a/b", "a/b/c"} {
		eq(t, "files kind "+d, files[d].Kind, "dir")
	}
	eq(t, "files kind d.txt", files["a/b/c/d.txt"].Kind, "file")

	// A file where a directory is wanted, and the other way round.
	_, err := e.ana.op(server.OpRequest{Op: "mkdir", Path: "a/b/c/d.txt"})
	wantErr(t, err, http.StatusConflict, server.CodeKindMismatch)
	_, err = e.ana.put("a/b/c/d.txt/e.txt", "", v2)
	wantErr(t, err, http.StatusConflict, server.CodeParentNotDir)
	_, err = e.ana.put("prazan", "", v2)
	wantErr(t, err, http.StatusConflict, server.CodeKindMismatch)

	res = e.ana.mustOp(server.OpRequest{Op: "rmdir", Path: "prazan/ugnjezden"})
	eqList(t, "rmdir events", eventOps(res.Events), []string{"7 rmdir prazan/ugnjezden"})
	eqList(t, "storage", e.tree("storage"), []string{"a/", "a/b/", "a/b/c/", "a/b/c/d.txt", "prazan/"})
	if _, ok := e.files()["prazan/ugnjezden"]; ok {
		t.Error("removed directory still in files")
	}
	e.checkLogMatchesEvents()
}

// Rule 9: paths go through pathx; a name that differs from an existing one
// only in letter case is refused with its own code.
func TestRule9Names(t *testing.T) {
	e := newEnv(t)
	for _, bad := range []string{"../izvan.txt", "/apsolutna", "a/./b", "CON.txt", "pitanje?.txt", "tačka.", "kontrolni\x01"} {
		_, err := e.ana.announce(bad, "", v1)
		wantErr(t, err, http.StatusBadRequest, server.CodeInvalidPath)
		_, err = e.ana.op(server.OpRequest{Op: "mkdir", Path: bad})
		wantErr(t, err, http.StatusBadRequest, server.CodeInvalidPath)
	}

	e.ana.mustPut("Dokumenti/Plan.md", "", v1)
	_, err := e.ana.announce("dokumenti/novo.md", "", v2)
	wantErr(t, err, http.StatusConflict, server.CodeCaseConflict)
	_, err = e.ana.announce("Dokumenti/PLAN.md", "", v2)
	wantErr(t, err, http.StatusConflict, server.CodeCaseConflict)
	_, err = e.ana.op(server.OpRequest{Op: "mkdir", Path: "DOKUMENTI"})
	wantErr(t, err, http.StatusConflict, server.CodeCaseConflict)
	_, err = e.ana.op(server.OpRequest{Op: "move", Path: "Dokumenti/Plan.md", NewPath: "dokumenti/x.md", BaseHash: hashOf(v1)})
	wantErr(t, err, http.StatusConflict, server.CodeCaseConflict)

	// The check also runs at commit: the name was free at announce time.
	bob := e.member("bob")
	ar, aerr := bob.announce("Beleske.txt", "", v2)
	if aerr != nil {
		t.Fatal(aerr)
	}
	bob.send(ar, v2)
	e.ana.mustPut("beleske.txt", "", v3)
	_, err = bob.commit(ar.UploadID)
	wantErr(t, err, http.StatusConflict, server.CodeCaseConflict)
	eqList(t, "tmp after refused commit", e.tree("tmp"), nil)

	// NFD input is stored in NFC.
	res := e.ana.mustPut("Café.txt", "", v1)
	eq(t, "normalized", res.Path, "Café.txt")

	eqList(t, "storage", e.tree("storage"), []string{"Café.txt", "Dokumenti/", "Dokumenti/Plan.md", "beleske.txt"})
	eqList(t, "events", eventOps(e.events()), []string{"1 mkdir Dokumenti", "2 put Dokumenti/Plan.md", "3 put beleske.txt", "4 put Café.txt"})
	e.checkLogMatchesEvents()
}

// Rule 10: two trash entries with the same name get "-2", "-3".
func TestRule10TrashNameCollision(t *testing.T) {
	e := newEnv(t)
	for _, d := range []string{"a", "b", "c"} {
		e.ana.mustPut(d+"/x.txt", "", v1)
	}
	for _, d := range []string{"a", "b", "c"} {
		e.ana.mustOp(server.OpRequest{Op: "delete", Path: d + "/x.txt", BaseHash: hashOf(v1)})
	}
	base := "x.txt.deleted-by-ana-" + ts0
	eqList(t, "trash", e.tree("trash"), []string{base, base + "-2", base + "-3"})
	var orig []string
	for _, r := range e.trash() {
		orig = append(orig, r.OrigPath+"="+r.Name)
	}
	eqList(t, "trash rows", orig, []string{"a/x.txt=" + base, "b/x.txt=" + base + "-2", "c/x.txt=" + base + "-3"})
}

func TestOpValidation(t *testing.T) {
	e := newEnv(t)
	_, err := e.ana.op(server.OpRequest{Op: "chmod", Path: "a"})
	wantErr(t, err, http.StatusBadRequest, server.CodeInvalidRequest)
	_, err = e.ana.op(server.OpRequest{Op: "move", Path: "a", NewPath: "b"})
	wantErr(t, err, http.StatusBadRequest, server.CodeInvalidRequest)
	_, err = e.ana.announce("a.txt", "nije-hash", v1)
	wantErr(t, err, http.StatusBadRequest, server.CodeInvalidRequest)
	_, err = e.ana.commit("0123456789abcdef0123456789abcdef")
	wantErr(t, err, http.StatusNotFound, server.CodeUnknownUpload)
}
