package httpapi_test

import (
	"encoding/json"
	"io"
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

// whoami calls GET /v1/whoami with the token.
func (e *env) whoami(token string) (*httpapi.Whoami, *apiError) {
	e.t.Helper()
	req, _ := http.NewRequest("GET", e.http.URL+"/v1/whoami", nil)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		e.t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		var eb httpapi.ErrorBody
		json.Unmarshal(raw, &eb)
		return nil, &apiError{Status: resp.StatusCode, Code: eb.Error.Code, Msg: eb.Error.Message}
	}
	var w httpapi.Whoami
	if err := json.Unmarshal(raw, &w); err != nil {
		e.t.Fatal(err)
	}
	return &w, nil
}

func (c *client) members() *server.MemberList {
	c.e.t.Helper()
	var l server.MemberList
	if err := c.do("GET", "/admin/members", nil, &l); err != nil {
		c.e.t.Fatal(err)
	}
	return &l
}

func memberByName(t *testing.T, l *server.MemberList, name string) server.MemberInfo {
	t.Helper()
	for _, m := range l.Members {
		if m.Username == name {
			return m
		}
	}
	t.Fatalf("no member %s in %+v", name, l.Members)
	return server.MemberInfo{}
}

func (c *client) logPage(q string) *server.LogPage {
	c.e.t.Helper()
	var p server.LogPage
	if err := c.do("GET", "/admin/log"+q, nil, &p); err != nil {
		c.e.t.Fatal(err)
	}
	return &p
}

func (c *client) dir(d string) (*server.DirListing, *apiError) {
	var l server.DirListing
	err := c.do("GET", "/admin/files?dir="+url.QueryEscape(d), nil, &l)
	return &l, err
}

func TestWhoami(t *testing.T) {
	e := newEnv(t)
	bob := e.member("bob")
	// A second folder: the token alone finds its folder.
	id2, tok2, err := e.srv.CreateShare("drugi", "zoran")
	if err != nil {
		t.Fatal(err)
	}

	w, aerr := e.whoami(e.ana.token)
	if aerr != nil {
		t.Fatal(aerr)
	}
	eq(t, "share", w.ShareID, e.share)
	eq(t, "share name", w.ShareName, "tim")
	eq(t, "username", w.Username, "ana")
	eq(t, "role", w.Role, server.RoleAdmin)

	w, aerr = e.whoami(bob.token)
	if aerr != nil {
		t.Fatal(aerr)
	}
	eq(t, "member role", w.Role, server.RoleMember)

	w, aerr = e.whoami(tok2)
	if aerr != nil {
		t.Fatal(aerr)
	}
	eq(t, "second folder", w.ShareID, id2)
	eq(t, "second folder admin", w.Username, "zoran")

	_, aerr = e.whoami("")
	wantErr(t, aerr, http.StatusUnauthorized, server.CodeUnauthorized)
	_, aerr = e.whoami("shf_nepostoji")
	wantErr(t, aerr, http.StatusUnauthorized, server.CodeUnauthorized)
	sh, _ := e.srv.Share(e.share)
	if err := sh.RevokeMember("bob"); err != nil {
		t.Fatal(err)
	}
	_, aerr = e.whoami(bob.token)
	wantErr(t, aerr, http.StatusUnauthorized, server.CodeUnauthorized)
}

func TestMemberRoutesNeedAdmin(t *testing.T) {
	e := newEnv(t)
	bob := e.member("bob")
	routes := []struct {
		method, path string
		body         any
	}{
		{"GET", "/admin/members", nil},
		{"POST", "/admin/members", httpapi.AddMemberRequest{Username: "eve", Admin: true}},
		{"POST", "/admin/members/ana/revoke", nil},
		{"POST", "/admin/members/bob/role", httpapi.SetRoleRequest{Role: "admin"}},
		{"POST", "/admin/members/ana/reset-token", nil},
		{"GET", "/admin/log", nil},
		{"GET", "/admin/log?after=0", nil},
		{"GET", "/admin/files", nil},
	}
	nobody := &client{e: e, share: e.share}
	for _, r := range routes {
		if err := bob.do(r.method, r.path, r.body, nil); err == nil || err.Status != http.StatusForbidden || err.Code != server.CodeForbidden {
			t.Errorf("member %s %s: got %v, want 403 forbidden", r.method, r.path, err)
		}
		if err := nobody.do(r.method, r.path, r.body, nil); err == nil || err.Status != http.StatusUnauthorized {
			t.Errorf("no token %s %s: got %v, want 401", r.method, r.path, err)
		}
	}
	// Nothing changed.
	l := e.ana.members()
	eq(t, "members", len(l.Members), 2)
	eq(t, "bob still a member", memberByName(t, l, "bob").Role, server.RoleMember)
	if _, aerr := e.whoami(e.ana.token); aerr != nil {
		t.Errorf("ana's token after the refused calls: %v", aerr)
	}
}

func TestMembersAPI(t *testing.T) {
	akPath, keys := withKeys(t)
	e := newEnv(t, keys)
	_ = akPath

	// add: the token is in the answer, and works.
	var add httpapi.TokenResponse
	if err := e.ana.do("POST", "/admin/members", httpapi.AddMemberRequest{Username: "bob"}, &add); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(add.Token, "shf_") || add.Reactivated {
		t.Fatalf("add: %+v", add)
	}
	eq(t, "new member status", add.Member.Status, server.StatusPending)
	eq(t, "new member role", add.Member.Role, server.RoleMember)
	bob := &client{e: e, share: e.share, user: "bob", token: add.Token}
	if err := bob.do("PUT", "/ssh-key", httpapi.SSHKeyRequest{PublicKey: sshKey(t, "bob")}, nil); err != nil {
		t.Fatal(err)
	}
	bob.mustPut("b.txt", "", []byte("b"))

	// The name of an active member is taken.
	err := e.ana.do("POST", "/admin/members", httpapi.AddMemberRequest{Username: "bob"}, nil)
	wantErr(t, err, http.StatusConflict, server.CodeMemberExists)
	err = e.ana.do("POST", "/admin/members", httpapi.AddMemberRequest{Username: "ne valja/"}, nil)
	wantErr(t, err, http.StatusBadRequest, server.CodeInvalidRequest)

	// list: status, fingerprint, behind; no token anywhere.
	var raw json.RawMessage
	if err := e.ana.do("GET", "/admin/members", nil, &raw); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "shf_") || strings.Contains(string(raw), "token") {
		t.Errorf("member list leaks a token: %s", raw)
	}
	l := e.ana.members()
	eq(t, "head", l.Head, 1)
	mb := memberByName(t, l, "bob")
	eq(t, "bob status", mb.Status, server.StatusActive)
	if !strings.HasPrefix(mb.SSHFingerprint, "SHA256:") {
		t.Errorf("bob fingerprint %q", mb.SSHFingerprint)
	}
	ma := memberByName(t, l, "ana")
	eq(t, "ana status (no key)", ma.Status, server.StatusPending)
	eq(t, "ana behind", ma.Behind, 1)

	// The last admin keeps the role and cannot be revoked.
	err = e.ana.do("POST", "/admin/members/ana/role", httpapi.SetRoleRequest{Role: "member"}, nil)
	wantErr(t, err, http.StatusConflict, server.CodeLastAdmin)
	err = e.ana.do("POST", "/admin/members/ana/revoke", nil, nil)
	wantErr(t, err, http.StatusConflict, server.CodeLastAdmin)
	err = e.ana.do("POST", "/admin/members/nema/revoke", nil, nil)
	wantErr(t, err, http.StatusNotFound, server.CodeUnknownMember)
	err = e.ana.do("POST", "/admin/members/nema/role", httpapi.SetRoleRequest{Role: "admin"}, nil)
	wantErr(t, err, http.StatusNotFound, server.CodeUnknownMember)
	err = e.ana.do("POST", "/admin/members/bob/role", httpapi.SetRoleRequest{Role: "boss"}, nil)
	wantErr(t, err, http.StatusBadRequest, server.CodeInvalidRequest)
	err = e.ana.do("POST", "/admin/members/nema/reset-token", nil, nil)
	wantErr(t, err, http.StatusNotFound, server.CodeUnknownMember)

	// role: bob becomes an admin, then ana may step down.
	var mr httpapi.MemberResponse
	if err := e.ana.do("POST", "/admin/members/bob/role", httpapi.SetRoleRequest{Role: "admin"}, &mr); err != nil {
		t.Fatal(err)
	}
	eq(t, "old role", mr.OldRole, server.RoleMember)
	eq(t, "new role", mr.Member.Role, server.RoleAdmin)
	if err := bob.do("POST", "/admin/members/ana/role", httpapi.SetRoleRequest{Role: "member"}, nil); err != nil {
		t.Fatal(err)
	}
	if err := e.ana.do("GET", "/admin/members", nil, nil); err == nil || err.Status != http.StatusForbidden {
		t.Errorf("ana after losing the role: %v, want 403", err)
	}

	// reset-token: the old token stops at once, the new one works.
	var rt httpapi.TokenResponse
	if err := bob.do("POST", "/admin/members/ana/reset-token", nil, &rt); err != nil {
		t.Fatal(err)
	}
	_, aerr := e.ana.changes(0, 10)
	wantErr(t, aerr, http.StatusUnauthorized, server.CodeUnauthorized)
	e.ana.token = rt.Token
	if _, aerr := e.ana.changes(0, 10); aerr != nil {
		t.Fatal(aerr)
	}

	// revoke: the token stops, the member is listed as revoked, and its
	// WebSocket is closed.
	hs, _ := e.wsServer()
	wa := mustDial(t, hs.URL, e.share, e.ana.token)
	wa.mustHead(1)
	if err := bob.do("POST", "/admin/members/ana/revoke", nil, &mr); err != nil {
		t.Fatal(err)
	}
	eq(t, "revoked status", mr.Member.Status, server.StatusRevoked)
	if st := closeStatus(t, wa, 2*time.Second); st != httpapi.CloseRevoked {
		t.Errorf("close status %v, want %v", st, httpapi.CloseRevoked)
	}
	_, aerr = e.whoami(e.ana.token)
	wantErr(t, aerr, http.StatusUnauthorized, server.CodeUnauthorized)
	// Revoking again is not an error; a reset of a revoked member is.
	if err := bob.do("POST", "/admin/members/ana/revoke", nil, nil); err != nil {
		t.Errorf("revoke twice: %v", err)
	}
	err = bob.do("POST", "/admin/members/ana/reset-token", nil, nil)
	wantErr(t, err, http.StatusNotFound, server.CodeUnknownMember)

	// add with the name of a revoked member brings it back.
	if err := bob.do("POST", "/admin/members", httpapi.AddMemberRequest{Username: "ana", Admin: true}, &add); err != nil {
		t.Fatal(err)
	}
	if !add.Reactivated || add.Member.Status != server.StatusPending || add.Member.Role != server.RoleAdmin {
		t.Errorf("reactivate: %+v %+v", add, add.Member)
	}
	w, aerr := e.whoami(add.Token)
	if aerr != nil || w.Username != "ana" || w.Role != server.RoleAdmin {
		t.Errorf("whoami after reactivation: %+v %v", w, aerr)
	}

	// The token never reaches service.log.
	b, _ := os.ReadFile(filepath.Join(e.dir(), "service.log"))
	for _, tok := range []string{add.Token, rt.Token, bob.token} {
		if strings.Contains(string(b), tok) {
			t.Error("a token is in service.log")
		}
	}
}

func TestLogAPI(t *testing.T) {
	e := newEnv(t)
	sh, _ := e.srv.Share(e.share)

	// An empty log.
	p := e.ana.logPage("")
	eq(t, "empty lines", len(p.Lines), 0)
	eq(t, "empty next", p.NextOffset, 0)

	e.ana.mustPut("a.txt", "", []byte("a"))
	e.ana.mustOp(server.OpRequest{Op: "mkdir", Path: "docs"})
	e.ana.mustOp(server.OpRequest{Op: "move", Path: "a.txt", NewPath: "docs/a.txt", BaseHash: hashOf([]byte("a"))})
	if err := sh.LogTransfer(server.Transfer{Member: "bob", Path: "storage/x", Outcome: "denied", Detail: "revoked"}); err != nil {
		t.Fatal(err)
	}

	// Without after: the last lines, oldest first.
	p = e.ana.logPage("")
	eq(t, "lines", len(p.Lines), 4)
	eq(t, "offset", p.Offset, 0)
	eq(t, "next", p.NextOffset, p.Size)
	l0 := p.Lines[0]
	if l0.Type != server.LogEvent || l0.Seq != 1 || l0.Op != "put" || l0.Path != "a.txt" || l0.Member != "ana" || l0.TS.IsZero() {
		t.Errorf("line 0: %+v", l0)
	}
	if l := p.Lines[2]; l.Op != "move" || l.Path != "docs/a.txt" || l.OldPath != "a.txt" {
		t.Errorf("move line: %+v", l)
	}
	if l := p.Lines[3]; l.Type != server.LogTransfer || l.Outcome != "denied" || l.Detail != "revoked" || l.Dir != "-" || l.Member != "bob" {
		t.Errorf("transfer line: %+v", l)
	}
	for i := 1; i < len(p.Lines); i++ {
		if p.Lines[i].Offset <= p.Lines[i-1].Offset {
			t.Errorf("offsets not increasing: %d, %d", p.Lines[i-1].Offset, p.Lines[i].Offset)
		}
	}

	// limit without after: the newest lines.
	last := e.ana.logPage("?limit=2")
	eq(t, "tail lines", len(last.Lines), 2)
	eq(t, "tail first", last.Lines[0].Op, "move")
	eq(t, "tail offset", last.Offset, p.Lines[2].Offset)

	// after=next_offset: nothing new, then only what follows.
	next := p.NextOffset
	p = e.ana.logPage("?after=" + strconv.FormatInt(next, 10))
	eq(t, "nothing new", len(p.Lines), 0)
	eq(t, "next unchanged", p.NextOffset, next)
	e.ana.mustPut("b.txt", "", []byte("b"))
	p = e.ana.logPage("?after=" + strconv.FormatInt(next, 10))
	if len(p.Lines) != 1 || p.Lines[0].Path != "b.txt" || p.Lines[0].Seq != 4 || p.Lines[0].Offset != next {
		t.Errorf("after: %+v", p.Lines)
	}
	// after=0 with a limit pages from the start.
	p = e.ana.logPage("?after=0&limit=2")
	eq(t, "page lines", len(p.Lines), 2)
	eq(t, "page next", p.NextOffset, p.Lines[1].Offset+int64(len(p.Lines[1].Raw))+1)
	// An offset inside a line skips to the next one.
	p = e.ana.logPage("?after=3&limit=1")
	if len(p.Lines) != 1 || p.Lines[0].Seq != 2 {
		t.Errorf("after inside a line: %+v", p.Lines)
	}

	err := e.ana.do("GET", "/admin/log?after=-1", nil, nil)
	wantErr(t, err, http.StatusBadRequest, server.CodeInvalidRequest)
	err = e.ana.do("GET", "/admin/log?limit=0", nil, nil)
	wantErr(t, err, http.StatusBadRequest, server.CodeInvalidRequest)
}

func TestReadLogLargeTail(t *testing.T) {
	e := newEnv(t)
	sh, _ := e.srv.Share(e.share)
	// More than the first window of the tail reader (64 KiB).
	for i := range 1500 {
		if err := sh.LogTransfer(server.Transfer{Member: "ana", Direction: "upload", Path: "tmp/" + strings.Repeat("x", 40) + strconv.Itoa(i), Outcome: "ok"}); err != nil {
			t.Fatal(err)
		}
	}
	// A line being written is not returned.
	f, _ := os.OpenFile(filepath.Join(e.dir(), "service.log"), os.O_APPEND|os.O_WRONLY, 0)
	f.WriteString("2026-11-23T14:58:46Z transfer member=ana")
	f.Close()
	p, err := sh.ReadLog(-1, 1000)
	if err != nil {
		t.Fatal(err)
	}
	eq(t, "lines", len(p.Lines), 1000)
	if !strings.HasSuffix(p.Lines[999].Path, "1499") || !strings.HasSuffix(p.Lines[0].Path, "500") {
		t.Errorf("tail: first %q last %q", p.Lines[0].Path, p.Lines[999].Path)
	}
	eq(t, "next stops before the partial line", p.NextOffset, p.Size-int64(len("2026-11-23T14:58:46Z transfer member=ana")))
	p2, err := sh.ReadLog(p.NextOffset, 10)
	if err != nil {
		t.Fatal(err)
	}
	eq(t, "partial line waits", len(p2.Lines), 0)
}

func TestParseLogLine(t *testing.T) {
	cases := []struct {
		raw  string
		want server.LogLine
	}{
		{`2026-11-23T14:58:46.5Z seq=7 member=ana op=put path="dir/a b.txt"`,
			server.LogLine{Type: server.LogEvent, Seq: 7, Member: "ana", Op: "put", Path: "dir/a b.txt"}},
		{`2026-11-23T14:58:46Z seq=8 member=bob op=move path="n\"ovo.txt" old_path="staro.txt"`,
			server.LogLine{Type: server.LogEvent, Seq: 8, Member: "bob", Op: "move", Path: `n"ovo.txt`, OldPath: "staro.txt"}},
		{`2026-11-23T14:58:46Z transfer member=bob dir=download path="storage/a.txt" outcome=ok`,
			server.LogLine{Type: server.LogTransfer, Member: "bob", Op: "download", Dir: "download", Path: "storage/a.txt", Outcome: "ok"}},
		{`2026-11-23T14:58:46Z transfer member=#12 dir=- path="" outcome=denied detail="unknown member"`,
			server.LogLine{Type: server.LogTransfer, Member: "#12", Op: "-", Dir: "-", Outcome: "denied", Detail: "unknown member"}},
		{`2026-11-23T14:58:46Z key-replaced member=bob old=SHA256:aaa new=SHA256:bbb`,
			server.LogLine{Type: server.LogKeyReplaced, Member: "bob", Op: "key-replaced", Detail: "old=SHA256:aaa new=SHA256:bbb"}},
		{`2026-11-24T12:00:18.8Z sync member=bob from=0 to=3 sent=3 received=0 conflicts=0 took=1.719s`,
			server.LogLine{Type: server.LogSync, Member: "bob", Op: "sync", Outcome: "ok", Detail: "from=0 to=3 sent=3 received=0 conflicts=0 took=1.719s"}},
		{`2026-11-24T12:00:18.8Z sync member=bob from=3 to=3 sent=0 received=0 conflicts=0 took=5ms error="ssh: key refused"`,
			server.LogLine{Type: server.LogSync, Member: "bob", Op: "sync", Outcome: "failed", Detail: `from=3 to=3 sent=0 received=0 conflicts=0 took=5ms error="ssh: key refused"`}},
		{`nešto sasvim drugo`, server.LogLine{Type: server.LogOther}},
		{`2026-11-23T14:58:46Z seq=x member=ana op=put path="a"`, server.LogLine{Type: server.LogOther}},
		{`2026-11-23T14:58:46Z seq=1 member=ana op=put path="nezatvoren`, server.LogLine{Type: server.LogOther}},
		{`2026-11-23T14:58:46Z unknown-kind member=ana`, server.LogLine{Type: server.LogOther}},
		{``, server.LogLine{Type: server.LogOther}},
	}
	for _, c := range cases {
		got := server.ParseLogLine(42, c.raw)
		want := c.want
		want.Offset, want.Raw = 42, c.raw
		if want.Type != server.LogOther {
			want.TS = got.TS
			if got.TS.IsZero() {
				t.Errorf("%q: no time", c.raw)
			}
		}
		if got != want {
			t.Errorf("%q:\n got %+v\nwant %+v", c.raw, got, want)
		}
	}
}

func TestFilesAPI(t *testing.T) {
	e := newEnv(t)
	bob := e.member("bob")
	e.ana.mustPut("Zeta.txt", "", []byte("z"))
	e.ana.mustPut("alfa.txt", "", []byte("alfa"))
	e.ana.mustOp(server.OpRequest{Op: "mkdir", Path: "docs"})
	bob.mustPut("docs/plan.md", "", []byte("plan"))
	bob.mustPut("docs/arhiva/stari.md", "", []byte("s"))
	e.ana.mustOp(server.OpRequest{Op: "mkdir", Path: "Bilten"})

	root, err := e.ana.dir("")
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, f := range root.Entries {
		names = append(names, f.Kind+":"+f.Name)
	}
	eqList(t, "root", names, []string{"dir:Bilten", "dir:docs", "file:alfa.txt", "file:Zeta.txt"})
	alfa := root.Entries[2]
	if alfa.Path != "alfa.txt" || alfa.Size != 4 || alfa.Hash != hashOf([]byte("alfa")) || alfa.UpdatedBy != "ana" || alfa.Seq != 2 || alfa.Mtime == nil {
		t.Errorf("alfa: %+v", alfa)
	}
	if root.Entries[0].Mtime != nil {
		t.Errorf("a directory has an mtime: %+v", root.Entries[0])
	}

	sub, err := e.ana.dir("docs")
	if err != nil {
		t.Fatal(err)
	}
	eq(t, "sub dir", sub.Dir, "docs")
	names = nil
	for _, f := range sub.Entries {
		names = append(names, f.Kind+":"+f.Path+":"+f.UpdatedBy)
	}
	eqList(t, "docs", names, []string{"dir:docs/arhiva:bob", "file:docs/plan.md:bob"})
	sub, err = e.ana.dir("/docs/arhiva/")
	if err != nil || len(sub.Entries) != 1 || sub.Entries[0].Name != "stari.md" {
		t.Errorf("docs/arhiva: %+v %v", sub, err)
	}
	sub, err = e.ana.dir("Bilten")
	if err != nil || len(sub.Entries) != 0 {
		t.Errorf("empty dir: %+v %v", sub, err)
	}

	_, err = e.ana.dir("nema")
	wantErr(t, err, http.StatusNotFound, server.CodeNotFound)
	_, err = e.ana.dir("alfa.txt")
	wantErr(t, err, http.StatusConflict, server.CodeKindMismatch)
	_, err = e.ana.dir("../x")
	wantErr(t, err, http.StatusBadRequest, server.CodeInvalidPath)
}
