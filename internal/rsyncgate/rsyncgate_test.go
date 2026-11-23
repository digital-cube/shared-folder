package rsyncgate

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/digital-cube/shared-folder/internal/server"
)

type fixture struct {
	sh             *server.Share
	dir            string
	ana, bob, cec  int64  // cec is revoked
	anaUp, bobUp   string // open uploads
	committedUpID  string // an upload of ana that was committed already
	storageAbsDocs string
}

func setup(t *testing.T) *fixture {
	t.Helper()
	srv, err := server.Open(t.TempDir(), server.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { srv.Close() })
	id, anaTok, err := srv.CreateShare("tim", "ana")
	if err != nil {
		t.Fatal(err)
	}
	sh, _ := srv.Share(id)
	sh.AddMember("bob", false)
	sh.AddMember("cec", false)
	sh.RevokeMember("cec")
	f := &fixture{sh: sh, dir: sh.Dir(), ana: 1, bob: 2, cec: 3}
	ana, _ := sh.Authenticate(anaTok)
	bob, _ := sh.MemberByID(2)

	hash := strings.Repeat("ab", 32)
	announce := func(m *server.Member, p string) string {
		ar, err := sh.Announce(m, server.AnnounceRequest{Path: p, NewHash: hash, Size: 1})
		if err != nil {
			t.Fatal(err)
		}
		return ar.UploadID
	}
	f.anaUp, f.bobUp = announce(ana, "a.txt"), announce(bob, "b.txt")

	// A committed upload is not open any more.
	content := []byte("x")
	ar, _ := sh.Announce(ana, server.AnnounceRequest{Path: "c.txt", NewHash: "2d711642b726b04401627ca9fbac32f5c8530fb1903cc4db02258717921a4881", Size: 1})
	os.WriteFile(filepath.Join(f.dir, "tmp", ar.UploadID), content, 0o600)
	if _, err := sh.Commit(ana, ar.UploadID); err != nil {
		t.Fatal(err)
	}
	f.committedUpID = ar.UploadID

	st := filepath.Join(f.dir, "storage")
	os.MkdirAll(filepath.Join(st, "docs"), 0o700)
	os.WriteFile(filepath.Join(st, "docs", "a.txt"), []byte("a"), 0o600)
	os.WriteFile(filepath.Join(st, "a b$`x`;*.txt"), []byte("odd"), 0o600)
	os.WriteFile(filepath.Join(f.dir, "secret"), []byte("outside"), 0o600)
	os.Symlink("../service.db", filepath.Join(st, "db-link"))
	os.Symlink(f.dir, filepath.Join(st, "dir-link"))
	os.Symlink("docs", filepath.Join(st, "inner-link")) // stays inside: allowed
	os.MkdirAll(filepath.Join(f.dir, "tmp", f.bobUp+"x"), 0o700)
	f.storageAbsDocs = filepath.Join(st, "docs", "a.txt")
	return f
}

func TestAuthorizeTable(t *testing.T) {
	f := setup(t)
	real := func(p string) string { r, _ := filepath.EvalSymlinks(p); return r }
	st := real(filepath.Join(f.dir, "storage"))
	caps := "-e.LsfxCIvu"
	up := func(id string, opts ...string) string {
		return "rsync --server " + strings.Join(append([]string{caps, "--timeout=60"}, opts...), " ") + " . tmp/" + id
	}
	down := func(p string, opts ...string) string {
		return "rsync --server --sender " + strings.Join(append([]string{caps, "--timeout=60"}, opts...), " ") + " . " + p
	}

	for _, tc := range []struct {
		name    string
		member  int64
		cmd     string
		code    string // "" = allowed
		wantAbs string
	}{
		// allowed
		{"upload own open upload", f.ana, up(f.anaUp, "--partial"), "", filepath.Join(f.dir, "tmp", f.anaUp)},
		{"upload with stats and bwlimit", f.bob, up(f.bobUp, "--partial", "--stats", "--bwlimit=4096"), "", filepath.Join(f.dir, "tmp", f.bobUp)},
		{"upload compressed cluster", f.ana, "rsync --server -vzte.LsfxCIvu --partial . tmp/" + f.anaUp, "", filepath.Join(f.dir, "tmp", f.anaUp)},
		{"download", f.ana, down("storage/docs/a.txt", "--inplace", "--stats"), "", filepath.Join(st, "docs", "a.txt")},
		{"download name escaped by rsync", f.bob, down(`storage/a\ b\$` + "`x`" + `\;*.txt`), "", filepath.Join(st, "a b$`x`;*.txt")},
		{"download name in quotes", f.bob, down(`'storage/a b$` + "`x`" + `;*.txt'`), "", filepath.Join(st, "a b$`x`;*.txt")},
		{"download through a symlink that stays inside", f.ana, down("storage/inner-link/a.txt"), "", filepath.Join(st, "docs", "a.txt")},
		{"dot-dot that stays inside", f.ana, down("storage/docs/../docs/a.txt"), "", filepath.Join(st, "docs", "a.txt")},

		// not rsync
		{"shell", f.ana, "sh -c 'cat /etc/passwd'", CodeNotRsync, ""},
		{"empty command (interactive login)", f.ana, "", CodeNotRsync, ""},
		{"rsync client mode", f.ana, "rsync -av /etc/ /tmp/x", CodeNotRsync, ""},
		{"rsync with a path", f.ana, "/usr/bin/rsync --server . tmp/" + f.anaUp, CodeNotRsync, ""},
		{"rsync daemon", f.ana, "rsync --server --daemon .", CodeOption, ""},
		{"chained command", f.ana, up(f.anaUp) + "; rm -rf /", CodeSyntax, ""},
		{"unterminated quote", f.ana, down("'storage/docs/a.txt"), CodeSyntax, ""},

		// upload ids
		{"wrong upload id", f.ana, up(strings.Repeat("0", 32)), CodeUpload, ""},
		{"someone else's upload id", f.ana, up(f.bobUp), CodeUpload, ""},
		{"committed upload id", f.ana, up(f.committedUpID), CodeUpload, ""},
		{"malformed upload id", f.ana, up("../storage/x"), CodePath, ""},
		{"upload into storage", f.ana, "rsync --server " + caps + " --partial . storage/a.txt", CodePath, ""},
		{"upload to tmp itself", f.ana, "rsync --server " + caps + " . tmp/", CodePath, ""},
		{"upload to tmp/<id>/sub", f.ana, "rsync --server " + caps + " . tmp/" + f.anaUp + "/x", CodePath, ""},
		{"upload to an absolute path", f.ana, "rsync --server " + caps + " . /tmp/" + f.anaUp, CodePath, ""},
		{"two destinations", f.ana, "rsync --server " + caps + " . tmp/" + f.anaUp + " tmp/" + f.anaUp, CodeSyntax, ""},
		{"no dot", f.ana, "rsync --server " + caps + " tmp/" + f.anaUp, CodeSyntax, ""},

		// leaving storage/
		{"download dot-dot", f.ana, down("storage/../service.db"), CodePath, ""},
		{"download dot-dot deep", f.ana, down("storage/docs/../../secret"), CodePath, ""},
		{"download dot-dot to registry", f.ana, down("storage/../../registry.db"), CodePath, ""},
		{"download symlink to a file outside", f.ana, down("storage/db-link"), CodePath, ""},
		{"download through a symlinked directory", f.ana, down("storage/dir-link/secret"), CodePath, ""},
		{"download from tmp", f.ana, down("tmp/" + f.anaUp), CodePath, ""},
		{"download absolute", f.ana, down("/etc/passwd"), CodePath, ""},
		{"download storage itself", f.ana, down("storage/"), CodePath, ""},
		{"download a directory", f.ana, down("storage/docs"), CodePath, ""},
		{"download missing file", f.ana, down("storage/docs/nope.txt"), CodeNotFound, ""},
		{"download two files", f.ana, down("storage/docs/a.txt storage/a.txt"), CodeSyntax, ""},

		// options
		{"remove-source-files", f.ana, down("storage/docs/a.txt", "--remove-source-files"), CodeOption, ""},
		{"delete", f.ana, up(f.anaUp, "--delete"), CodeOption, ""},
		{"delete-after", f.ana, up(f.anaUp, "--delete-after"), CodeOption, ""},
		{"del", f.ana, up(f.anaUp, "--del"), CodeOption, ""},
		{"partial-dir", f.ana, up(f.anaUp, "--partial-dir=../../x"), CodeOption, ""},
		{"temp-dir", f.ana, up(f.anaUp, "--temp-dir=/tmp"), CodeOption, ""},
		{"backup-dir", f.ana, up(f.anaUp, "--backup-dir=../storage"), CodeOption, ""},
		{"files-from", f.ana, down("storage/docs/a.txt", "--files-from=/etc/passwd"), CodeOption, ""},
		{"rsh in long form", f.ana, up(f.anaUp, "--rsh=sh"), CodeOption, ""},
		{"-e beyond capabilities", f.ana, "rsync --server -e sh . tmp/" + f.anaUp, CodeOption, ""},
		{"-e glued with a command", f.ana, "rsync --server -esh . tmp/" + f.anaUp, CodeOption, ""},
		{"-s secluded args", f.ana, "rsync --server -se.LsfxCIvu . tmp/" + f.anaUp, CodeOption, ""},
		{"recursive", f.ana, "rsync --server -re.LsfxCIvu . tmp/" + f.anaUp, CodeOption, ""},
		{"links", f.ana, down("storage/docs/a.txt", "-L"), CodeOption, ""},
		{"inplace upload", f.ana, up(f.anaUp, "--inplace"), CodeOption, ""},
		{"partial on download", f.ana, down("storage/docs/a.txt", "--partial"), CodeOption, ""},
		{"timeout not a number", f.ana, up(f.anaUp, "--timeout=x"), CodeOption, ""},
		{"stats with a value", f.ana, up(f.anaUp, "--stats=1"), CodeOption, ""},
		{"rsync-path", f.ana, up(f.anaUp, "--rsync-path=sh"), CodeOption, ""},
		{"unknown option", f.ana, up(f.anaUp, "--chmod=u+s"), CodeOption, ""},

		// members
		{"revoked member", f.cec, down("storage/docs/a.txt"), CodeMember, ""},
		{"unknown member", 99, down("storage/docs/a.txt"), CodeMember, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			g := &Gate{Share: f.sh, MemberID: tc.member}
			_, p, err := g.Authorize(tc.cmd)
			if tc.code == "" {
				if err != nil {
					t.Fatalf("refused: %v", err)
				}
				if p.Abs != tc.wantAbs {
					t.Errorf("abs = %s, want %s", p.Abs, tc.wantAbs)
				}
				if p.Args[0] != "--server" || p.Args[len(p.Args)-2] != "." || p.Args[len(p.Args)-1] != p.Abs {
					t.Errorf("args %q", p.Args)
				}
				return
			}
			d, ok := err.(*Denied)
			if !ok || d.Code != tc.code {
				t.Fatalf("got %v, want denial %s", err, tc.code)
			}
		})
	}
}

func TestSplit(t *testing.T) {
	for in, want := range map[string][]string{
		`a  b	c`:                 {"a", "b", "c"},
		`a\ b`:                   {"a b"},
		`'a b' "c d"`:            {"a b", "c d"},
		`"a\"b\\c\$d\e"`:         {`a"b\c$d\e`},
		`it\'s`:                  {"it's"},
		"x\\\ny":                 {"xy"},
		`$HOME ` + "`id`" + ` *`: {"$HOME", "`id`", "*"},
		`a'b'"c"d`:               {"abcd"},
		`''`:                     {""},
	} {
		got, err := Split(in)
		if err != nil || !reflect.DeepEqual(got, want) {
			t.Errorf("Split(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	for _, bad := range []string{`'a`, `"a`, `a\`} {
		if _, err := Split(bad); err == nil {
			t.Errorf("Split(%q) accepted", bad)
		}
	}
}

// fakeRsync writes a script that records its arguments and working
// directory and exits with code.
func fakeRsync(t *testing.T, code int) (bin, record string) {
	dir := t.TempDir()
	record = filepath.Join(dir, "args")
	bin = filepath.Join(dir, "rsync")
	script := "#!/bin/sh\npwd > " + record + "\nfor a in \"$@\"; do echo \"$a\" >> " + record + "; done\necho payload\nexit " + string(rune('0'+code)) + "\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return bin, record
}

func logTail(t *testing.T, f *fixture) string {
	b, err := os.ReadFile(filepath.Join(f.dir, "service.log"))
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(b)), "\n")
	return lines[len(lines)-1]
}

func TestRunExecsRsyncAndLogs(t *testing.T) {
	f := setup(t)
	bin, record := fakeRsync(t, 0)
	g := &Gate{Share: f.sh, MemberID: f.ana, Rsync: bin}
	var out, errb bytes.Buffer
	code := g.Run(context.Background(), `rsync --server --sender -e.LsfxCIvu --inplace . storage/docs/a.txt`, strings.NewReader(""), &out, &errb)
	if code != 0 || out.String() != "payload\n" {
		t.Fatalf("code %d out %q err %q", code, out.String(), errb.String())
	}
	rec, _ := os.ReadFile(record)
	realDir, _ := filepath.EvalSymlinks(f.dir)
	realFile, _ := filepath.EvalSymlinks(f.storageAbsDocs)
	want := realDir + "\n--server\n--sender\n-e.LsfxCIvu\n--inplace\n.\n" + realFile + "\n"
	if string(rec) != want {
		t.Errorf("rsync got\n%s\nwant\n%s", rec, want)
	}
	if l := logTail(t, f); !strings.Contains(l, ` transfer member=ana dir=download path="storage/docs/a.txt" outcome=ok`) {
		t.Errorf("log line %q", l)
	}

	bin, _ = fakeRsync(t, 5)
	g.Rsync = bin
	code = g.Run(context.Background(), "rsync --server -e.LsfxCIvu --partial . tmp/"+f.anaUp, strings.NewReader(""), &out, &errb)
	if code != 5 {
		t.Errorf("exit code %d, want rsync's 5", code)
	}
	if l := logTail(t, f); !strings.Contains(l, `member=ana dir=upload path="tmp/`+f.anaUp+`" outcome=failed detail="rsync: exit status 5"`) {
		t.Errorf("log line %q", l)
	}
}

func TestRunDenied(t *testing.T) {
	f := setup(t)
	bin, record := fakeRsync(t, 0)
	for _, tc := range []struct {
		member int64
		cmd    string
		log    string
	}{
		{f.ana, "cat /etc/passwd", `member=ana dir=- path="" outcome=denied detail="not_rsync: only rsync is allowed on this key; command cat /etc/passwd"`},
		{f.bob, "rsync --server -e.LsfxCIvu . tmp/" + f.anaUp, `member=bob dir=upload path="tmp/` + f.anaUp + `" outcome=denied detail="upload: `},
		{f.cec, "rsync --server --sender . storage/docs/a.txt", `member=cec dir=- path="" outcome=denied detail="member: member cec is revoked"`},
		{f.ana, "rsync --server --sender . storage/../secret", `member=ana dir=download path="storage/../secret" outcome=denied detail="path: `},
		{f.ana, "rsync --server --sender . storage/docs/gone", `outcome=denied detail="not_found: storage/docs/gone does not exist"`},
	} {
		var out, errb bytes.Buffer
		g := &Gate{Share: f.sh, MemberID: tc.member, Rsync: bin}
		if code := g.Run(context.Background(), tc.cmd, strings.NewReader(""), &out, &errb); code == 0 {
			t.Errorf("%q: exit 0", tc.cmd)
		}
		if !strings.HasPrefix(errb.String(), "shfd rsync-gate: denied (") {
			t.Errorf("%q: stderr %q", tc.cmd, errb.String())
		}
		if l := logTail(t, f); !strings.Contains(l, tc.log) {
			t.Errorf("%q: log line\n%s\nwant it to contain\n%s", tc.cmd, l, tc.log)
		}
	}
	if _, err := os.Stat(record); err == nil {
		t.Error("rsync was started for a refused command")
	}
}
