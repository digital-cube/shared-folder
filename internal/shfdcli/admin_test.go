package shfdcli

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/digital-cube/shared-folder/internal/server"
)

func sha(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

// seed creates a folder whose admin is ana, with member bob, puts files
// through the server and deletes some of them, so the trash has entries:
//
//	1  docs/a.txt  deleted by ana
//	2  b.txt       replaced by bob
func seed(t *testing.T, root string) (id string) {
	t.Helper()
	env := map[string]string{EnvDataRoot: root}
	_, out, _ := run(t, env, "share", "create", "tim", "--admin", "ana")
	id = idRE.FindStringSubmatch(out)[1]
	srv, err := server.Open(root, server.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()
	sh, err := srv.Share(id)
	if err != nil {
		t.Fatal(err)
	}
	ana, _ := sh.DefaultAdmin()
	if _, err := sh.AddMember("bob", false); err != nil {
		t.Fatal(err)
	}
	bob, _ := sh.MemberByName("bob")
	put := func(m *server.Member, p, base string, content []byte) {
		ar, err := sh.Announce(m, server.AnnounceRequest{Path: p, BaseHash: base, NewHash: sha(content), Size: int64(len(content))})
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(sh.Dir(), ar.Dest), content, 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := sh.Commit(m, ar.UploadID); err != nil {
			t.Fatal(err)
		}
	}
	put(ana, "docs/a.txt", "", []byte("aaa\n"))
	if _, err := sh.Op(ana, server.OpRequest{Op: server.OpDelete, Path: "docs/a.txt", BaseHash: sha([]byte("aaa\n"))}); err != nil {
		t.Fatal(err)
	}
	put(ana, "b.txt", "", []byte("b1\n"))
	put(bob, "b.txt", sha([]byte("b1\n")), []byte("b2\n"))
	return id
}

func runIn(t *testing.T, env map[string]string, stdin string, args ...string) (code int, stdout, stderr string) {
	t.Helper()
	var o, e bytes.Buffer
	code = Run(args, Env{Stdin: strings.NewReader(stdin), Stdout: &o, Stderr: &e, Getenv: func(k string) string { return env[k] }})
	return code, o.String(), e.String()
}

func mustMatch(t *testing.T, what, out string, res ...string) {
	t.Helper()
	for _, re := range res {
		if !regexp.MustCompile(re).MatchString(out) {
			t.Errorf("%s: no match for %q in:\n%s", what, re, out)
		}
	}
}

// adminCommands runs the trash, verify and stats commands against a seeded
// data-root, however they reach it.
func adminCommands(t *testing.T, root, id string) {
	env := map[string]string{EnvDataRoot: root}

	code, out, errOut := run(t, env, "trash", "list", id)
	if code != 0 {
		t.Fatalf("trash list: %d %s", code, errOut)
	}
	mustMatch(t, "trash list", out,
		`(?m)^ID\s+PATH\s+REASON\s+BY\s+AT\s+SIZE$`,
		`(?m)^1\s+docs/a.txt\s+deleted\s+ana\s+\S+\s+4$`,
		`(?m)^2\s+b.txt\s+replaced\s+bob\s+\S+\s+3$`,
		`(?m)^2 entries, 7 bytes$`)
	_, out, _ = run(t, env, "trash", "list", id, "--by", "bob", "--reason", "replaced", "--since", "2020-01-01")
	mustMatch(t, "trash list --by", out, `(?m)^1 entry, 3 bytes$`)
	_, out, _ = run(t, env, "trash", "list", id, "--path", "docs/")
	mustMatch(t, "trash list --path", out, `docs/a.txt`, `(?m)^1 entry`)

	code, out, errOut = run(t, env, "trash", "restore", id, "1")
	if code != 0 {
		t.Fatalf("trash restore: %d %s", code, errOut)
	}
	mustMatch(t, "restore", out, `^restored trash entry 1 to docs/a.txt \(seq \d+\)`)
	if b, _ := os.ReadFile(filepath.Join(root, id, "storage", "docs", "a.txt")); string(b) != "aaa\n" {
		t.Errorf("restored content %q", b)
	}
	_, out, _ = run(t, env, "trash", "restore", id, "2")
	mustMatch(t, "restore onto other content", out, `as b.restored-\d{8}T\d{6}Z.txt .*the original path holds other content`)
	_, out, _ = run(t, env, "trash", "restore", id, "1", "--to", "kopija/a.txt")
	mustMatch(t, "restore --to", out, `to kopija/a.txt`)
	if code, _, errOut = run(t, env, "trash", "restore", id, "77"); code != 1 || !strings.Contains(errOut, "unknown_trash") {
		t.Errorf("restore unknown: %d %s", code, errOut)
	}

	// purge: a question unless --yes; "no" changes nothing.
	code, out, errOut = runIn(t, env, "n\n", "trash", "purge", id, "--all")
	if code != 1 || !strings.Contains(errOut, "not confirmed") {
		t.Errorf("purge answered no: %d %s %s", code, out, errOut)
	}
	mustMatch(t, "purge question", out, `docs/a.txt`, `Purge 2 entries, 7 bytes, for good\? This cannot be undone\. \[y/N\]`)
	_, out, _ = run(t, env, "trash", "list", id)
	mustMatch(t, "after no", out, `(?m)^2 entries`)
	_, out, _ = run(t, env, "trash", "purge", id, "--id", "1", "--dry-run")
	mustMatch(t, "dry run", out, `would purge 1 entry, 4 bytes \(dry run, nothing changed\)`)
	_, out, _ = run(t, env, "trash", "purge", id, "--older-than", "3650d", "--yes")
	mustMatch(t, "nothing old", out, `nothing to purge`)
	code, out, errOut = runIn(t, env, "y\n", "trash", "purge", id, "--id", "1")
	if code != 0 {
		t.Fatalf("purge: %d %s", code, errOut)
	}
	mustMatch(t, "purge yes", out, `purged 1 entry, 4 bytes`)
	_, out, _ = run(t, env, "trash", "purge", id, "--all", "--yes")
	mustMatch(t, "purge --yes", out, `purged 1 entry, 3 bytes`)
	if code, _, _ = run(t, env, "trash", "purge", id); code != 2 {
		t.Errorf("purge without a selection: exit %d", code)
	}
	if code, _, _ = run(t, env, "trash", "purge", id, "--all", "--id", "3"); code != 2 {
		t.Errorf("purge with two selections: exit %d", code)
	}

	// verify: clean, then a file appears behind the server's back.
	if code, out, errOut = run(t, env, "verify", id); code != 0 {
		t.Fatalf("verify clean: %d %s %s", code, out, errOut)
	}
	mustMatch(t, "verify clean", out, `no issues`)
	os.WriteFile(filepath.Join(root, id, "storage", "extra.txt"), []byte("x"), 0o600)
	code, out, errOut = run(t, env, "verify", id)
	if code != 1 || !strings.Contains(errOut, "1 issue found") {
		t.Errorf("verify: %d %s", code, errOut)
	}
	mustMatch(t, "verify", out, `(?m)^storage\s+untracked\s+extra.txt\s+file `)
	code, out, errOut = run(t, env, "verify", id, "--repair")
	if code != 0 {
		t.Errorf("verify --repair: %d %s %s", code, out, errOut)
	}
	mustMatch(t, "verify --repair", out, `(?m)extra.txt .*repaired \(seq \d+\)$`, `1 issue, 1 repaired`)
	code, out, _ = run(t, env, "verify", id, "--json")
	var vr server.VerifyResult
	if code != 0 || json.Unmarshal([]byte(out), &vr) != nil || len(vr.Issues) != 0 {
		t.Errorf("verify --json: %d %s", code, out)
	}

	code, out, errOut = run(t, env, "stats", id, "--json")
	var st server.Stats
	if code != 0 || json.Unmarshal([]byte(out), &st) != nil {
		t.Fatalf("stats --json: %d %s %s", code, out, errOut)
	}
	if st.ShareID != id || st.Files != 5 || st.Trash != 0 || st.Purged != 2 || len(st.Members) != 2 || st.Members[1].Username != "bob" {
		t.Errorf("stats: %+v", st)
	}
	_, out, _ = run(t, env, "stats", id)
	mustMatch(t, "stats", out, `(?m)^head:\s+\d+$`, `(?m)^storage:\s+5 files, 15 bytes; 2 directories$`, `(?m)^trash:\s+0 entries, 0 bytes; 2 purged$`,
		`(?m)^USERNAME\s+ROLE\s+STATUS\s+LAST SEEN\s+LAST PUSH\s+ACKED SEQ\s+BEHIND$`, `(?m)^bob\s+member\s+active`)

	// Only an admin may act; --as picks who.
	if code, _, errOut = run(t, env, "stats", id, "--as", "bob"); code != 1 || !strings.Contains(errOut, "forbidden") {
		t.Errorf("--as bob: %d %s", code, errOut)
	}
	if code, _, errOut = run(t, env, "stats", id, "--as", "nikola"); code != 1 || !strings.Contains(errOut, "unknown_member") {
		t.Errorf("--as unknown: %d %s", code, errOut)
	}
	if code, _, _ = run(t, env, "stats", id, "--as", "ana"); code != 0 {
		t.Errorf("--as ana: %d", code)
	}
	if code, _, _ = run(t, env, "stats", "nije-id"); code != 2 {
		t.Errorf("bad share id: exit %d", code)
	}
	if code, _, errOut = run(t, env, "stats", "00000000-0000-4000-8000-000000000000"); code != 1 || !strings.Contains(errOut, "unknown_share") {
		t.Errorf("unknown share: %d %s", code, errOut)
	}
}

func TestAdminCommandsWithoutServer(t *testing.T) {
	root := t.TempDir()
	id := seed(t, root)
	adminCommands(t, root, id)
}

// startServe runs shfd serve on root until the test ends.
func startServe(t *testing.T, root string) (stop func()) {
	t.Helper()
	_, stop = startServeAt(t, root)
	return stop
}

// startServeAt is startServe that also returns the address of the API.
func startServeAt(t *testing.T, root string) (addr string, stop func()) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	ready := make(chan string, 1)
	done := make(chan int, 1)
	var stderr bytes.Buffer
	go func() {
		done <- Run([]string{"serve", "--listen", "127.0.0.1:0", "--data-root", root}, Env{
			Stdout: &bytes.Buffer{}, Stderr: &stderr, Ctx: ctx,
			Getenv: func(string) string { return "" },
			Ready:  func(addr string) { ready <- addr },
		})
	}()
	select {
	case addr = <-ready:
	case code := <-done:
		t.Fatalf("serve exited %d: %s", code, stderr.String())
	case <-time.After(10 * time.Second):
		t.Fatal("serve did not start")
	}
	if strings.Contains(stderr.String(), "admin socket not available") {
		t.Fatalf("serve: %s", stderr.String())
	}
	stopped := false
	stop = func() {
		if stopped {
			return
		}
		stopped = true
		cancel()
		select {
		case code := <-done:
			if code != 0 {
				t.Errorf("serve exit %d: %s", code, stderr.String())
			}
		case <-time.After(10 * time.Second):
			t.Fatal("serve did not stop")
		}
	}
	t.Cleanup(stop)
	return addr, stop
}

// While shfd serve runs, the commands go through its admin socket: the
// data-root lock is taken, so they could not work in-process.
func TestAdminCommandsThroughServe(t *testing.T) {
	for _, tc := range []struct {
		name string
		root func(t *testing.T) string
	}{
		{"short data-root", func(t *testing.T) string { return t.TempDir() }},
		// <data-root>/admin.sock would not fit in a unix socket address.
		{"long data-root", func(t *testing.T) string {
			return filepath.Join(t.TempDir(), strings.Repeat("dugacko-ime-", 10))
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := tc.root(t)
			id := seed(t, root)
			stop := startServe(t, root)
			abs, _ := filepath.Abs(root)
			sock, err := adminSocketPath(abs, false)
			if err != nil {
				t.Fatal(err)
			}
			if fi, err := os.Stat(sock); err != nil || fi.Mode()&os.ModeSocket == 0 || fi.Mode().Perm() != 0o600 {
				t.Fatalf("admin socket %s: %v %v", sock, fi, err)
			}
			if strings.HasPrefix(sock, abs) != (tc.name == "short data-root") {
				t.Errorf("socket at %s for data-root %s", sock, abs)
			}
			adminCommands(t, root, id)
			stop()
			if _, err := os.Stat(sock); err == nil {
				t.Errorf("admin socket %s left behind", sock)
			}
			// Without the server, the same commands work in-process.
			if code, _, errOut := run(t, map[string]string{EnvDataRoot: root}, "stats", id); code != 0 {
				t.Errorf("stats after serve stopped: %d %s", code, errOut)
			}
		})
	}
}

// A stale socket file (a crashed serve) does not stop the commands.
func TestAdminCommandsStaleSocket(t *testing.T) {
	root := t.TempDir()
	id := seed(t, root)
	os.WriteFile(filepath.Join(root, AdminSocket), nil, 0o600)
	if code, _, errOut := run(t, map[string]string{EnvDataRoot: root}, "stats", id); code != 0 {
		t.Errorf("stats with a stale socket file: %d %s", code, errOut)
	}
	// serve replaces it.
	startServe(t, root)
	if code, _, errOut := run(t, map[string]string{EnvDataRoot: root}, "stats", id); code != 0 {
		t.Errorf("stats through serve: %d %s", code, errOut)
	}
}
