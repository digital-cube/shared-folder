package authkeys

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

const (
	share1 = "690a2702-1bb6-4866-b49f-cc8bee54f08e"
	share2 = "0b6b4a2e-5f0c-4b8e-9a51-3d7f1c2e8a90"
)

// newKey returns a fresh ed25519 public key in authorized_keys form.
func newKey(t *testing.T, comment string) string {
	t.Helper()
	pub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	var blob []byte
	for _, part := range [][]byte{[]byte("ssh-ed25519"), pub} {
		blob = binary.BigEndian.AppendUint32(blob, uint32(len(part)))
		blob = append(blob, part...)
	}
	return "ssh-ed25519 " + base64.StdEncoding.EncodeToString(blob) + " " + comment
}

func newFile(t *testing.T) *File {
	return &File{
		Path:    filepath.Join(t.TempDir(), ".ssh", "authorized_keys"),
		Command: []string{"/usr/local/bin/shfd", "rsync-gate", "--data-root", "/srv/shfd data"},
	}
}

const outsideBefore = "# admin's own key, not shfd's business\nssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIOMqqnkVzrm0SdG6UOoqKLsabgH5C9okWi0dh2l9GKJl admin@laptop\n"
const outsideAfter = "\n# trailing comment without a final newline"

func TestSetRemovePreservesOutside(t *testing.T) {
	f := newFile(t)
	os.MkdirAll(filepath.Dir(f.Path), 0o700)
	if err := os.WriteFile(f.Path, []byte(outsideBefore), 0o640); err != nil {
		t.Fatal(err)
	}
	ana, bob := newKey(t, "ana@laptop"), newKey(t, "bob")
	if err := f.Set(Entry{ShareID: share1, MemberID: 1, Username: "ana", Key: ana}); err != nil {
		t.Fatal(err)
	}
	// A human appends something after the block.
	data, _ := os.ReadFile(f.Path)
	os.WriteFile(f.Path, append(data, outsideAfter...), 0o640)

	if err := f.Set(Entry{ShareID: share2, MemberID: 7, Username: "bob", Key: bob}); err != nil {
		t.Fatal(err)
	}
	data, _ = os.ReadFile(f.Path)
	s := string(data)
	if !strings.HasPrefix(s, outsideBefore+BeginMarker+"\n") || !strings.HasSuffix(s, EndMarker+"\n"+outsideAfter) {
		t.Fatalf("outside lines changed:\n%s", s)
	}
	anaKey, _ := ParsePublicKey(ana)
	wantAna := `command="/usr/local/bin/shfd rsync-gate --data-root '/srv/shfd data' --share ` + share1 + ` --member 1",` +
		`no-port-forwarding,no-agent-forwarding,no-X11-forwarding,no-pty,no-user-rc ` + anaKey + ` shfd:ana@` + share1 + "\n"
	if !strings.Contains(s, "\n"+wantAna) {
		t.Errorf("ana's line missing; want\n%s\nin\n%s", wantAna, s)
	}
	if fi, _ := os.Stat(f.Path); fi.Mode().Perm() != 0o640 {
		t.Errorf("mode changed to %v", fi.Mode().Perm())
	}

	es, err := f.Entries()
	// Sorted by share id, then member id.
	if err != nil || len(es) != 2 || es[0].Username != "bob" || es[0].MemberID != 7 || es[0].ShareID != share2 || es[1].Username != "ana" {
		t.Fatalf("entries %+v %v", es, err)
	}

	// Replacing ana's key keeps one line for her.
	ana2 := newKey(t, "ana@desktop")
	if err := f.Set(Entry{ShareID: share1, MemberID: 1, Username: "ana", Key: ana2}); err != nil {
		t.Fatal(err)
	}
	data, _ = os.ReadFile(f.Path)
	if strings.Contains(string(data), anaKey) || strings.Count(string(data), "--member 1\"") != 1 {
		t.Errorf("old key still there or line duplicated:\n%s", data)
	}

	// Revoke.
	if err := f.Remove(share1, 1); err != nil {
		t.Fatal(err)
	}
	if err := f.Remove(share1, 1); err != nil {
		t.Fatalf("second remove: %v", err)
	}
	data, _ = os.ReadFile(f.Path)
	s = string(data)
	if strings.Contains(s, "--member 1\"") || !strings.Contains(s, "shfd:bob@") {
		t.Errorf("after remove:\n%s", s)
	}
	if !strings.HasPrefix(s, outsideBefore) || !strings.HasSuffix(s, outsideAfter) {
		t.Errorf("outside lines changed after remove:\n%s", s)
	}
	f.Remove(share2, 7)
	data, _ = os.ReadFile(f.Path)
	if string(data) != outsideBefore+BeginMarker+"\n"+EndMarker+"\n"+outsideAfter {
		t.Errorf("empty block:\n%q", data)
	}
}

func TestSetCreatesFile(t *testing.T) {
	f := newFile(t)
	if err := f.Set(Entry{ShareID: share1, MemberID: 3, Username: "ana", Key: newKey(t, "")}); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(f.Path)
	if err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("stat %v %v", fi, err)
	}
	data, _ := os.ReadFile(f.Path)
	if !strings.HasPrefix(string(data), BeginMarker+"\n") {
		t.Errorf("%s", data)
	}
}

func TestKeyInUse(t *testing.T) {
	f := newFile(t)
	os.MkdirAll(filepath.Dir(f.Path), 0o700)
	os.WriteFile(f.Path, []byte(outsideBefore), 0o600)
	k := newKey(t, "x")
	if err := f.Set(Entry{ShareID: share1, MemberID: 1, Key: k}); err != nil {
		t.Fatal(err)
	}
	if err := f.Set(Entry{ShareID: share2, MemberID: 1, Key: k}); !errors.Is(err, ErrKeyInUse) {
		t.Errorf("same key for another member: %v", err)
	}
	// The admin's unrestricted key is outside the block; sshd would match
	// it first, without the forced command.
	admin := strings.Fields(strings.Split(outsideBefore, "\n")[1])
	if err := f.Set(Entry{ShareID: share1, MemberID: 2, Key: admin[0] + " " + admin[1]}); !errors.Is(err, ErrKeyInUse) {
		t.Errorf("key that has its own line: %v", err)
	}
	// The same member may set the same key again.
	if err := f.Set(Entry{ShareID: share1, MemberID: 1, Key: k}); err != nil {
		t.Errorf("same key again: %v", err)
	}
}

func TestReplace(t *testing.T) {
	f := newFile(t)
	os.MkdirAll(filepath.Dir(f.Path), 0o700)
	os.WriteFile(f.Path, []byte(outsideBefore), 0o600)
	f.Set(Entry{ShareID: share1, MemberID: 9, Key: newKey(t, "stale")})
	a, b := newKey(t, "a"), newKey(t, "b")
	admin := strings.Fields(strings.Split(outsideBefore, "\n")[1])
	skipped, err := f.Replace([]Entry{
		{ShareID: share1, MemberID: 1, Username: "ana", Key: a},
		{ShareID: share2, MemberID: 2, Username: "bob", Key: b},
		{ShareID: share2, MemberID: 3, Username: "dup", Key: a},
		{ShareID: share2, MemberID: 4, Username: "adm", Key: admin[0] + " " + admin[1]},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(skipped) != 2 || skipped[0].Username != "dup" || skipped[1].Username != "adm" {
		t.Errorf("skipped %+v", skipped)
	}
	es, _ := f.Entries()
	if len(es) != 2 || es[0].MemberID != 2 || es[1].MemberID != 1 {
		t.Errorf("entries %+v", es)
	}
}

func TestMalformedBlockIsNotTouched(t *testing.T) {
	for name, content := range map[string]string{
		"no end":       BeginMarker + "\n",
		"no begin":     EndMarker + "\n",
		"two blocks":   BeginMarker + "\n" + EndMarker + "\n" + BeginMarker + "\n" + EndMarker + "\n",
		"foreign line": BeginMarker + "\nssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIOMqqnkVzrm0SdG6UOoqKLsabgH5C9okWi0dh2l9GKJl hand-added\n" + EndMarker + "\n",
	} {
		f := newFile(t)
		os.MkdirAll(filepath.Dir(f.Path), 0o700)
		os.WriteFile(f.Path, []byte(content), 0o600)
		if err := f.Set(Entry{ShareID: share1, MemberID: 1, Key: newKey(t, "")}); err == nil {
			t.Errorf("%s: accepted", name)
		}
		if data, _ := os.ReadFile(f.Path); string(data) != content {
			t.Errorf("%s: file changed", name)
		}
	}
}

func TestConcurrentSet(t *testing.T) {
	f := newFile(t)
	var wg sync.WaitGroup
	for i := 1; i <= 20; i++ {
		wg.Add(1)
		k := newKey(t, "")
		go func(id int64) {
			defer wg.Done()
			// Separate File values, as in separate processes.
			g := &File{Path: f.Path, Command: f.Command}
			if err := g.Set(Entry{ShareID: share1, MemberID: id, Key: k}); err != nil {
				t.Error(err)
			}
		}(int64(i))
	}
	wg.Wait()
	if es, _ := f.Entries(); len(es) != 20 {
		t.Errorf("%d entries, want 20", len(es))
	}
}

func TestParsePublicKey(t *testing.T) {
	good := newKey(t, "comment with spaces")
	norm, err := ParsePublicKey("  " + good + "\n")
	if err != nil || norm != strings.Join(strings.Fields(good)[:2], " ") {
		t.Fatalf("%q %v", norm, err)
	}
	if fp := Fingerprint(norm); !strings.HasPrefix(fp, "SHA256:") || len(fp) != 50 {
		t.Errorf("fingerprint %q", fp)
	}
	f := strings.Fields(good)
	for _, bad := range []string{
		"",
		f[1],
		`command="sh" ` + good,
		"ssh-dss " + f[1],
		"ssh-rsa " + f[1], // blob says ed25519
		"ssh-ed25519 !!!notbase64",
		"ssh-ed25519 AAAA",
		good + "\n" + good,
	} {
		if _, err := ParsePublicKey(bad); err == nil {
			t.Errorf("accepted %q", bad)
		}
	}
}

func TestDeviceLines(t *testing.T) {
	f := newFile(t)
	legacy, mac, lin := newKey(t, "a"), newKey(t, "b"), newKey(t, "c")
	for _, e := range []Entry{
		{ShareID: share1, MemberID: 2, Username: "marko", Key: legacy},
		{ShareID: share1, MemberID: 2, DeviceID: 5, Username: "marko", Device: "Marko's Mac/Book@home", Key: mac},
		{ShareID: share1, MemberID: 2, DeviceID: 9, Username: "marko", Device: "linux", Key: lin},
	} {
		if err := f.Set(e); err != nil {
			t.Fatal(err)
		}
	}
	data, _ := os.ReadFile(f.Path)
	norm, _ := ParsePublicKey(mac)
	want := `command="/usr/local/bin/shfd rsync-gate --data-root '/srv/shfd data' --share ` + share1 + ` --member 2 --device 5",` +
		Restrictions + " " + norm + " shfd:marko/Marko-s-Mac-Book-home@" + share1 + "\n"
	if !strings.Contains(string(data), want) {
		t.Fatalf("no device line\n%s\nin\n%s", want, data)
	}
	es, err := f.Entries()
	if err != nil || len(es) != 3 {
		t.Fatalf("entries %+v %v", es, err)
	}
	if es[0].DeviceID != 0 || es[1].DeviceID != 5 || es[1].Device != "Marko-s-Mac-Book-home" || es[1].Username != "marko" || es[2].DeviceID != 9 {
		t.Errorf("entries %+v", es)
	}
	// One key on two devices: refused; a device replacing its own key: fine.
	if err := f.Set(Entry{ShareID: share1, MemberID: 2, DeviceID: 9, Key: mac}); !errors.Is(err, ErrKeyInUse) {
		t.Errorf("key of another device: %v", err)
	}
	if err := f.Set(Entry{ShareID: share1, MemberID: 2, DeviceID: 9, Username: "marko", Device: "linux", Key: newKey(t, "d")}); err != nil {
		t.Errorf("new key of the same device: %v", err)
	}
	// RemoveDevice takes one line (0: the member's own key); Remove all.
	if err := f.RemoveDevice(share1, 2, 5); err != nil {
		t.Fatal(err)
	}
	if es, _ := f.Entries(); len(es) != 2 || es[0].DeviceID != 0 || es[1].DeviceID != 9 {
		t.Errorf("after RemoveDevice %+v", es)
	}
	if err := f.RemoveDevice(share1, 2, 0); err != nil {
		t.Fatal(err)
	}
	if es, _ := f.Entries(); len(es) != 1 || es[0].DeviceID != 9 {
		t.Errorf("after removing the legacy line %+v", es)
	}
	f.Set(Entry{ShareID: share1, MemberID: 2, Key: legacy})
	f.Set(Entry{ShareID: share1, MemberID: 3, DeviceID: 5, Key: mac})
	if err := f.Remove(share1, 2); err != nil {
		t.Fatal(err)
	}
	if es, _ := f.Entries(); len(es) != 1 || es[0].MemberID != 3 {
		t.Errorf("after Remove %+v", es)
	}
}

func TestDeviceLabel(t *testing.T) {
	for in, want := range map[string]string{
		"macbook":               "macbook",
		"Mac Book":              "Mac-Book",
		"čačak-pc":              "a-ak-pc",
		"   ":                   "device-7",
		"ш":                     "device-7",
		"a@b/c":                 "a-b-c",
		"x.y_z-1":               "x.y_z-1",
		strings.Repeat("a", 40): strings.Repeat("a", 32),
	} {
		if got := DeviceLabel(in, 7); got != want {
			t.Errorf("DeviceLabel(%q) = %q, want %q", in, got, want)
		}
	}
}
