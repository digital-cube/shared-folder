package httpapi_test

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/digital-cube/shared-folder/internal/server"
)

func TestChangesOrderPagingHead(t *testing.T) {
	e := newEnv(t)
	cr, err := e.ana.changes(0, 0)
	if err != nil {
		t.Fatal(err)
	}
	eq(t, "empty head", cr.Head, int64(0))
	eq(t, "empty events", len(cr.Events), 0)

	for i := range 7 {
		e.ana.mustPut(fmt.Sprintf("f%d.txt", i), "", []byte(fmt.Sprint(i)))
	}
	e.ana.mustOp(server.OpRequest{Op: "delete", Path: "f0.txt", BaseHash: hashOf([]byte("0"))})
	// HEAD = 8.

	var seqs []int64
	since, pages := int64(0), 0
	for {
		cr, err := e.ana.changes(since, 3)
		if err != nil {
			t.Fatal(err)
		}
		pages++
		eq(t, "head", cr.Head, int64(8))
		for _, ev := range cr.Events {
			seqs = append(seqs, ev.Seq)
		}
		if len(cr.Events) > 0 {
			eq(t, "next", cr.Next, cr.Events[len(cr.Events)-1].Seq)
		}
		if !cr.More {
			break
		}
		since = cr.Next
	}
	eq(t, "pages", pages, 3)
	eq(t, "seqs", fmt.Sprint(seqs), "[1 2 3 4 5 6 7 8]")

	cr, _ = e.ana.changes(5, 0)
	eqList(t, "since 5", eventOps(cr.Events), []string{"6 put f5.txt", "7 put f6.txt", "8 delete f0.txt"})
	eq(t, "since 5 more", cr.More, false)
	eq(t, "event member", cr.Events[0].Member, "ana")
	eq(t, "event ts", cr.Events[0].TS.Equal(t0), true)
	if cr.Events[2].TrashID == nil {
		t.Error("delete event without trash_id")
	}

	cr, _ = e.ana.changes(8, 0)
	eq(t, "at head", len(cr.Events), 0)
	eq(t, "at head next", cr.Next, int64(8))

	_, err = e.ana.changes(-1, 0)
	wantErr(t, err, http.StatusBadRequest, server.CodeInvalidRequest)
	aerr := e.ana.do("GET", "/changes?since=x", nil, nil)
	wantErr(t, aerr, http.StatusBadRequest, server.CodeInvalidRequest)
	aerr = e.ana.do("GET", "/changes?limit=0", nil, nil)
	wantErr(t, aerr, http.StatusBadRequest, server.CodeInvalidRequest)
}

func TestAuthorization(t *testing.T) {
	e := newEnv(t)
	bob := e.member("bob")
	e.ana.mustPut("a.txt", "", v1)

	try := func(name string, c *client) {
		t.Helper()
		_, err := c.changes(0, 0)
		wantErr(t, err, http.StatusUnauthorized, server.CodeUnauthorized)
		_, err = c.announce("b.txt", "", v2)
		wantErr(t, err, http.StatusUnauthorized, server.CodeUnauthorized)
		_, err = c.op(server.OpRequest{Op: "delete", Path: "a.txt", BaseHash: hashOf(v1)})
		wantErr(t, err, http.StatusUnauthorized, server.CodeUnauthorized)
	}
	try("no token", &client{e: e, share: e.share})
	try("wrong token", &client{e: e, share: e.share, token: server.NewToken()})

	// Revoked token.
	if _, err := bob.changes(0, 0); err != nil {
		t.Fatalf("bob before revoke: %v", err)
	}
	sh, _ := e.srv.Share(e.share)
	if err := sh.RevokeMember("bob"); err != nil {
		t.Fatal(err)
	}
	try("revoked", bob)

	// A token of another folder.
	otherID, otherTok, err := e.srv.CreateShare("drugi", "zoran")
	if err != nil {
		t.Fatal(err)
	}
	try("other folder's token", &client{e: e, share: e.share, token: otherTok})
	// ...which works on its own folder.
	if _, err := (&client{e: e, share: otherID, token: otherTok}).changes(0, 0); err != nil {
		t.Fatalf("own folder: %v", err)
	}
	// An unknown folder looks like a bad token.
	try("unknown folder", &client{e: e, share: "00000000-0000-4000-8000-000000000000", token: e.ana.token})

	// Malformed header.
	req, _ := http.NewRequest("GET", e.http.URL+"/v1/shares/"+e.share+"/changes", nil)
	req.Header.Set("Authorization", "Basic "+e.ana.token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	eq(t, "basic auth status", resp.StatusCode, http.StatusUnauthorized)

	eq(t, "nothing changed", len(e.events()), 1)
}

// A successful call refreshes last_seen_at; a refused one does not.
// A commit sets last_push_at; a sync report sets last_seq_acked.
func TestMemberActivity(t *testing.T) {
	e := newEnv(t)
	bob := e.member("bob")
	sh, _ := e.srv.Share(e.share)
	member := func(name string) server.Member {
		ms, err := sh.Members()
		if err != nil {
			t.Fatal(err)
		}
		for _, m := range ms {
			if m.Username == name {
				return m
			}
		}
		t.Fatalf("no member %s", name)
		return server.Member{}
	}
	if m := member("bob"); m.LastSeenAt != nil || m.LastPushAt != nil {
		t.Fatal("fresh member has activity")
	}
	e.clock.Advance(time.Minute)
	if _, err := bob.changes(0, 0); err != nil {
		t.Fatal(err)
	}
	m := member("bob")
	if m.LastSeenAt == nil || !m.LastSeenAt.Equal(t0.Add(time.Minute)) {
		t.Fatalf("last_seen_at = %v", m.LastSeenAt)
	}
	eq(t, "role", m.Role, "member")
	eq(t, "ana is admin", member("ana").Role, "admin")

	e.clock.Advance(time.Minute)
	bob.op(server.OpRequest{Op: "delete", Path: "nema", BaseHash: "x"}) // refused: bad hash
	if m := member("bob"); !m.LastSeenAt.Equal(t0.Add(time.Minute)) {
		t.Errorf("refused call moved last_seen_at to %v", m.LastSeenAt)
	}

	bob.mustPut("a.txt", "", v1)
	if m := member("bob"); m.LastPushAt == nil || !m.LastPushAt.Equal(t0.Add(2*time.Minute)) {
		t.Errorf("last_push_at = %v", m.LastPushAt)
	}
}

func TestSyncReport(t *testing.T) {
	e := newEnv(t)
	e.ana.mustPut("a.txt", "", v1)
	rep := server.SyncReport{
		StartedAt: t0, FinishedAt: t0.Add(1500 * time.Millisecond),
		FromSeq: 0, ToSeq: 1, Sent: 1, Received: 0, Conflicts: 0,
	}
	if err := e.ana.do("POST", "/sync-report", rep, nil); err != nil {
		t.Fatal(err)
	}
	rep2 := rep
	rep2.FromSeq, rep2.Error = 1, "disk full"
	if err := e.ana.do("POST", "/sync-report", rep2, nil); err != nil {
		t.Fatal(err)
	}
	bad := rep
	bad.ToSeq = -5
	wantErr(t, e.ana.do("POST", "/sync-report", bad, nil), http.StatusBadRequest, server.CodeInvalidRequest)

	var n int
	var errText string
	if err := e.db().QueryRow(`SELECT count(*), max(error) FROM sync_log WHERE member = 'ana'`).Scan(&n, &errText); err != nil {
		t.Fatal(err)
	}
	eq(t, "sync_log rows", n, 2)
	eq(t, "sync_log error", errText, "disk full")
	var acked int64
	e.db().QueryRow(`SELECT last_seq_acked FROM members WHERE username = 'ana'`).Scan(&acked)
	eq(t, "last_seq_acked", acked, int64(1))

	lines := e.logLines()
	var syncLines []string
	for _, l := range lines {
		if strings.Contains(l, " sync ") {
			syncLines = append(syncLines, l)
		}
	}
	eq(t, "sync lines", len(syncLines), 2)
	if len(syncLines) == 2 {
		if !strings.Contains(syncLines[0], "member=ana from=0 to=1 sent=1 received=0 conflicts=0 took=1.5s") {
			t.Errorf("sync line: %s", syncLines[0])
		}
		if !strings.Contains(syncLines[1], `error="disk full"`) {
			t.Errorf("sync line with error: %s", syncLines[1])
		}
	}
	e.checkLogMatchesEvents()
}

// Two simultaneous uploads of different content to the same path give
// exactly one file under the original name and one conflict copy.
func TestConcurrentUploadsSamePath(t *testing.T) {
	for round := range 10 {
		t.Run(fmt.Sprint(round), func(t *testing.T) {
			e := newEnv(t)
			bob := e.member("bob")
			ca, cb := []byte("ana je bila ovde\n"), []byte("bob je bio ovde\n")
			ara, err := e.ana.announce("deljeno.txt", "", ca)
			if err != nil {
				t.Fatal(err)
			}
			arb, err := bob.announce("deljeno.txt", "", cb)
			if err != nil {
				t.Fatal(err)
			}
			e.ana.send(ara, ca)
			bob.send(arb, cb)

			var wg sync.WaitGroup
			results := make([]*server.Result, 2)
			errs := make([]*apiError, 2)
			start := make(chan struct{})
			for i, job := range []struct {
				c  *client
				id string
			}{{e.ana, ara.UploadID}, {bob, arb.UploadID}} {
				wg.Add(1)
				go func() {
					defer wg.Done()
					<-start
					results[i], errs[i] = job.c.commit(job.id)
				}()
			}
			close(start)
			wg.Wait()
			for i, err := range errs {
				if err != nil {
					t.Fatalf("commit %d: %v", i, err)
				}
			}
			statuses := []string{results[0].Status, results[1].Status}
			if !(statuses[0] == server.StatusApplied && statuses[1] == server.StatusConflict ||
				statuses[0] == server.StatusConflict && statuses[1] == server.StatusApplied) {
				t.Fatalf("statuses = %v, want one applied and one conflict", statuses)
			}
			winner, loser, loserUser := ca, cb, "bob"
			if results[1].Status == server.StatusApplied {
				winner, loser, loserUser = cb, ca, "ana"
			}
			cp := "deljeno.conflict-" + loserUser + "-" + ts0 + ".txt"
			eqList(t, "storage", e.tree("storage"), []string{cp, "deljeno.txt"})
			eq(t, "original", e.read("storage", "deljeno.txt"), string(winner))
			eq(t, "copy", e.read("storage", cp), string(loser))
			eqList(t, "events", eventOps(e.events()), []string{"1 put deljeno.txt", "2 conflict " + cp + " <- deljeno.txt"})
			eqList(t, "trash", e.tree("trash"), nil)
			eqList(t, "tmp", e.tree("tmp"), nil)
			eq(t, "files", len(e.files()), 2)
			e.checkLogMatchesEvents()
		})
	}
}

// A commit whose content does not match the announced hash changes
// nothing and cleans tmp/.
func TestCommitWrongHash(t *testing.T) {
	e := newEnv(t)
	e.ana.mustPut("a.txt", "", v1)
	evsBefore, logBefore := eventOps(e.events()), e.logLines()

	ar, err := e.ana.announce("a.txt", hashOf(v1), v2)
	if err != nil {
		t.Fatal(err)
	}
	forged := []byte("verzija 2, DUŽA\n") // same size, other content
	if len(forged) != len(v2) {
		t.Fatal("test content sizes differ")
	}
	e.ana.send(ar, forged)
	eqList(t, "tmp before", e.tree("tmp"), []string{ar.UploadID})

	_, err = e.ana.commit(ar.UploadID)
	wantErr(t, err, http.StatusUnprocessableEntity, server.CodeHashMismatch)
	eqList(t, "tmp after", e.tree("tmp"), nil)
	eqList(t, "storage", e.tree("storage"), []string{"a.txt"})
	eq(t, "content", e.read("storage", "a.txt"), string(v1))
	eqList(t, "trash", e.tree("trash"), nil)
	eqList(t, "events", eventOps(e.events()), evsBefore)
	eq(t, "files", e.files()["a.txt"].Hash, hashOf(v1))
	eqList(t, "service.log", e.logLines(), logBefore)

	// The upload is gone: committing again is unknown_upload.
	_, err = e.ana.commit(ar.UploadID)
	wantErr(t, err, http.StatusNotFound, server.CodeUnknownUpload)

	// Wrong size is caught the same way.
	ar, _ = e.ana.announce("a.txt", hashOf(v1), v2)
	e.ana.send(ar, []byte("kratko"))
	_, err = e.ana.commit(ar.UploadID)
	wantErr(t, err, http.StatusUnprocessableEntity, server.CodeSizeMismatch)
	eqList(t, "tmp after size mismatch", e.tree("tmp"), nil)
	eqList(t, "events", eventOps(e.events()), evsBefore)
}

func TestCommitEdgeCases(t *testing.T) {
	e := newEnv(t)
	bob := e.member("bob")

	// Commit before the content arrived: upload_missing, and the upload
	// stays usable.
	ar, _ := e.ana.announce("a.txt", "", v1)
	_, err := e.ana.commit(ar.UploadID)
	wantErr(t, err, http.StatusConflict, server.CodeUploadMissing)
	e.ana.send(ar, v1)
	if _, err := e.ana.commit(ar.UploadID); err != nil {
		t.Fatalf("commit after send: %v", err)
	}

	// Another member cannot commit my upload.
	ar, _ = e.ana.announce("b.txt", "", v2)
	e.ana.send(ar, v2)
	_, err = bob.commit(ar.UploadID)
	wantErr(t, err, http.StatusNotFound, server.CodeUnknownUpload)

	// The commit sets the announced mtime on the stored file.
	res, _ := e.ana.commit(ar.UploadID)
	eq(t, "status", res.Status, server.StatusApplied)
	fi, statErr := os.Stat(filepath.Join(e.dir(), "storage", "b.txt"))
	if statErr != nil {
		t.Fatal(statErr)
	}
	eq(t, "mtime", fi.ModTime().UTC().Equal(t0), true)
	eq(t, "head", res.Head, int64(2))
}

func TestUnknownEndpointAndBadJSON(t *testing.T) {
	e := newEnv(t)
	resp, err := http.Post(e.http.URL+"/v2/nesto", "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	eq(t, "unknown endpoint", resp.StatusCode, http.StatusNotFound)

	req, _ := http.NewRequest("POST", e.http.URL+"/v1/shares/"+e.share+"/ops", strings.NewReader("{"))
	req.Header.Set("Authorization", "Bearer "+e.ana.token)
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	eq(t, "bad json", resp.StatusCode, http.StatusBadRequest)
}
