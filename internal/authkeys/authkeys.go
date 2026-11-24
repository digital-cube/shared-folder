// Package authkeys maintains the members' lines in the authorized_keys file
// of the system user shfd runs as (docs/specifikacija.md, section 5).
//
// A member who registered an SSH key with its own token has one line (the
// legacy credential), and so has every device of the member's account that
// registered a key with its folder token. sshd turns each into a forced
// command:
//
//	command="/usr/local/bin/shfd rsync-gate --data-root /var/lib/shfd --share <id> --member <n>",no-port-forwarding,no-agent-forwarding,no-X11-forwarding,no-pty,no-user-rc ssh-ed25519 AAAA… shfd:<username>@<share-id>
//	command="/usr/local/bin/shfd rsync-gate --data-root /var/lib/shfd --share <id> --member <n> --device <d>",no-port-forwarding,… ssh-ed25519 AAAA… shfd:<username>/<device-name>@<share-id>
//
// A key is on one line at most.
//
// The lines live between two marker comments. shfd rewrites only what is
// between them; everything before and after the block stays byte for byte.
// Each change takes an exclusive lock on <path>.lock, writes a temporary
// file next to authorized_keys and renames it over the original, so sshd
// never reads a half-written file and two shfd processes (serve and a CLI
// command) do not lose each other's changes.
package authkeys

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// The markers around the managed block.
const (
	BeginMarker = "# >>> shfd managed block: do not edit by hand, `shfd keys sync` rewrites it >>>"
	EndMarker   = "# <<< shfd managed block <<<"
)

// Restrictions is what every managed line carries next to its command.
const Restrictions = "no-port-forwarding,no-agent-forwarding,no-X11-forwarding,no-pty,no-user-rc"

// ErrKeyInUse means the key already gives access to something else in the
// file: another member's line, or a line outside the block (which sshd
// would match first, without the forced command).
var ErrKeyInUse = errors.New("this SSH key is already in use")

// Entry is one line: a member's own key (DeviceID 0) or the key of one of
// its account's devices.
type Entry struct {
	ShareID  string
	MemberID int64
	// DeviceID is the account's device (registry.db devices.id) whose key
	// this is; 0 for the member's own, legacy key.
	DeviceID int64
	Username string // only for the comment at the end of the line
	Device   string // the device's name, only for the comment
	Key      string // "type base64", as returned by ParsePublicKey
}

// same says whether e and x are the line of the same credential.
func (e Entry) same(x Entry) bool {
	return e.ShareID == x.ShareID && e.MemberID == x.MemberID && e.DeviceID == x.DeviceID
}

// DeviceLabel is the device name as the comment of a line carries it:
// letters, digits, ".", "_" and "-", the rest replaced by "-", at most 32
// characters; "device-<id>" when nothing is left.
func DeviceLabel(name string, id int64) string {
	var b strings.Builder
	for _, r := range name {
		if b.Len() >= 32 {
			break
		}
		if r < 0x80 && usernameRE.MatchString(string(r)) {
			b.WriteRune(r)
		} else {
			b.WriteByte('-')
		}
	}
	if l := strings.Trim(b.String(), "-"); l != "" {
		return l
	}
	return "device-" + strconv.FormatInt(id, 10)
}

// File is an authorized_keys file with a managed block.
type File struct {
	// Path of authorized_keys.
	Path string
	// Command is the forced command up to the member's arguments, e.g.
	// {"/usr/local/bin/shfd", "rsync-gate", "--data-root", "/var/lib/shfd"}.
	// "--share <id> --member <n>" (and "--device <d>") is appended per
	// line.
	Command []string
}

// ---- public keys ---------------------------------------------------------

var keyTypes = map[string]bool{
	"ssh-ed25519":                        true,
	"ecdsa-sha2-nistp256":                true,
	"ecdsa-sha2-nistp384":                true,
	"ecdsa-sha2-nistp521":                true,
	"ssh-rsa":                            true,
	"sk-ssh-ed25519@openssh.com":         true,
	"sk-ecdsa-sha2-nistp256@openssh.com": true,
}

// ParsePublicKey checks one OpenSSH public key ("type base64 [comment]",
// the content of id_ed25519.pub) and returns it as "type base64": the
// comment is dropped, and so is anything that is not a plain key — options,
// several keys, unknown types, a blob that does not decode or does not
// name its own type.
func ParsePublicKey(s string) (string, error) {
	s = strings.TrimSpace(s)
	if strings.ContainsAny(s, "\r\n\x00") {
		return "", errors.New("public key must be a single line")
	}
	f := strings.Fields(s)
	if len(f) < 2 {
		return "", errors.New(`public key must look like "ssh-ed25519 AAAA… [comment]"`)
	}
	typ, b64 := f[0], f[1]
	if !keyTypes[typ] {
		return "", fmt.Errorf("unsupported key type %q", typ)
	}
	blob, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		return "", errors.New("public key is not valid base64")
	}
	if len(blob) < 4 {
		return "", errors.New("public key is too short")
	}
	n := binary.BigEndian.Uint32(blob)
	if uint64(n) > uint64(len(blob)-4) || string(blob[4:4+n]) != typ {
		return "", fmt.Errorf("public key data does not match its type %q", typ)
	}
	if len(blob) < 4+int(n)+16 {
		return "", errors.New("public key is too short")
	}
	return typ + " " + b64, nil
}

// Fingerprint is the SHA256 fingerprint ssh-keygen -l prints for key.
func Fingerprint(key string) string {
	f := strings.Fields(key)
	if len(f) < 2 {
		return ""
	}
	blob, err := base64.StdEncoding.DecodeString(f[1])
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(blob)
	return "SHA256:" + base64.RawStdEncoding.EncodeToString(sum[:])
}

// ---- lines ---------------------------------------------------------------

var (
	shareIDRE  = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)
	usernameRE = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)
	lineRE     = regexp.MustCompile(`^command="[^"]* --share ([0-9a-f-]+) --member ([0-9]+)(?: --device ([0-9]+))?",` + regexp.QuoteMeta(Restrictions) + ` (\S+ \S+)(?: shfd:([^/\s@]*)(?:/([^/\s@]*))?@\S*)?$`)
)

// shellQuote quotes one word for the POSIX shell sshd runs the forced
// command with. Words of safe characters stay as they are.
func shellQuote(w string) string {
	if w != "" && strings.Trim(w, "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789/._-+=:@,") == "" {
		return w
	}
	return "'" + strings.ReplaceAll(w, "'", `'\''`) + "'"
}

func (f *File) line(e Entry) (string, error) {
	if !shareIDRE.MatchString(e.ShareID) || e.MemberID <= 0 || e.DeviceID < 0 {
		return "", fmt.Errorf("invalid share %q / member %d / device %d", e.ShareID, e.MemberID, e.DeviceID)
	}
	key, err := ParsePublicKey(e.Key)
	if err != nil {
		return "", err
	}
	if len(f.Command) == 0 {
		return "", errors.New("authorized_keys: no forced command configured")
	}
	var words []string
	for _, w := range f.Command {
		// The command sits inside "…" in authorized_keys; sshd has no
		// escape for a double quote in there that every version agrees on.
		if strings.ContainsAny(w, "\"\\\n\r\x00") {
			return "", fmt.Errorf("authorized_keys: command word %q contains a quote, backslash or newline", w)
		}
		words = append(words, shellQuote(w))
	}
	cmd := strings.Join(words, " ") + " --share " + e.ShareID + " --member " + strconv.FormatInt(e.MemberID, 10)
	user := e.Username
	if !usernameRE.MatchString(user) {
		user = ""
	}
	if e.DeviceID > 0 {
		cmd += " --device " + strconv.FormatInt(e.DeviceID, 10)
		user += "/" + DeviceLabel(e.Device, e.DeviceID)
	}
	return fmt.Sprintf(`command="%s",%s %s shfd:%s@%s`, cmd, Restrictions, key, user, e.ShareID), nil
}

// ---- the file ------------------------------------------------------------

type parsed struct {
	before, after []byte // outside the block, verbatim
	entries       []Entry
	hasBlock      bool
	mode          os.FileMode
}

func (f *File) read() (*parsed, error) {
	p := &parsed{mode: 0o600}
	data, err := os.ReadFile(f.Path)
	if errors.Is(err, os.ErrNotExist) {
		return p, nil
	}
	if err != nil {
		return nil, err
	}
	if fi, err := os.Stat(f.Path); err == nil {
		p.mode = fi.Mode().Perm()
	}
	begin := findLine(data, BeginMarker, 0)
	if begin < 0 {
		if findLine(data, EndMarker, 0) >= 0 {
			return nil, fmt.Errorf("%s: end marker without begin marker; fix the file by hand", f.Path)
		}
		p.before = data
		return p, nil
	}
	bodyStart := begin + len(BeginMarker) + 1
	if bodyStart > len(data) {
		return nil, fmt.Errorf("%s: begin marker without end marker; fix the file by hand", f.Path)
	}
	end := findLine(data, EndMarker, bodyStart)
	if end < 0 {
		return nil, fmt.Errorf("%s: begin marker without end marker; fix the file by hand", f.Path)
	}
	if findLine(data, BeginMarker, bodyStart) >= 0 || findLine(data, EndMarker, end+1) >= 0 {
		return nil, fmt.Errorf("%s: more than one shfd block; fix the file by hand", f.Path)
	}
	afterStart := end + len(EndMarker)
	if afterStart < len(data) && data[afterStart] == '\n' {
		afterStart++
	}
	p.before, p.after, p.hasBlock = data[:begin], data[afterStart:], true
	for _, ln := range strings.Split(string(data[bodyStart:end]), "\n") {
		ln = strings.TrimSpace(ln)
		if ln == "" {
			continue
		}
		m := lineRE.FindStringSubmatch(ln)
		if m == nil {
			return nil, fmt.Errorf("%s: unexpected line inside the shfd block: %.60q", f.Path, ln)
		}
		id, _ := strconv.ParseInt(m[2], 10, 64)
		var dev int64
		if m[3] != "" {
			dev, _ = strconv.ParseInt(m[3], 10, 64)
		}
		p.entries = append(p.entries, Entry{ShareID: m[1], MemberID: id, DeviceID: dev, Key: m[4], Username: m[5], Device: m[6]})
	}
	return p, nil
}

// findLine returns the offset of a line that is exactly s, at or after
// from, or -1.
func findLine(data []byte, s string, from int) int {
	for i := from; i <= len(data); {
		j := bytes.Index(data[i:], []byte(s))
		if j < 0 {
			return -1
		}
		at := i + j
		startOK := at == 0 || data[at-1] == '\n'
		endOK := at+len(s) == len(data) || data[at+len(s)] == '\n'
		if startOK && endOK {
			return at
		}
		i = at + 1
	}
	return -1
}

func (f *File) write(p *parsed) error {
	sort.Slice(p.entries, func(i, j int) bool {
		a, b := p.entries[i], p.entries[j]
		if a.ShareID != b.ShareID {
			return a.ShareID < b.ShareID
		}
		if a.MemberID != b.MemberID {
			return a.MemberID < b.MemberID
		}
		return a.DeviceID < b.DeviceID
	})
	var b bytes.Buffer
	b.Write(p.before)
	if b.Len() > 0 && !bytes.HasSuffix(b.Bytes(), []byte("\n")) {
		b.WriteByte('\n')
	}
	b.WriteString(BeginMarker + "\n")
	for _, e := range p.entries {
		ln, err := f.line(e)
		if err != nil {
			return err
		}
		b.WriteString(ln + "\n")
	}
	b.WriteString(EndMarker + "\n")
	b.Write(p.after)

	dir := filepath.Dir(f.Path)
	tmp, err := os.CreateTemp(dir, ".authorized_keys.shfd-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(b.Bytes()); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(p.mode); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmp.Name(), f.Path); err != nil {
		return err
	}
	if d, err := os.Open(dir); err == nil {
		d.Sync()
		d.Close()
	}
	return nil
}

// keyBlob is the base64 part of a key line or of "type base64".
func keyBlob(s string) string {
	for _, w := range strings.Fields(s) {
		if strings.HasPrefix(w, "AAAA") {
			return w
		}
	}
	return ""
}

// update runs fn on the parsed file under the lock and writes the result.
func (f *File) update(fn func(p *parsed) error) error {
	if err := os.MkdirAll(filepath.Dir(f.Path), 0o700); err != nil {
		return err
	}
	unlock, err := lockFile(f.Path + ".lock")
	if err != nil {
		return err
	}
	defer unlock()
	p, err := f.read()
	if err != nil {
		return err
	}
	if err := fn(p); err != nil {
		return err
	}
	return f.write(p)
}

// Set adds the line of a credential (a member's own key, or one device's)
// or replaces the key on it. It fails with ErrKeyInUse when the same key is
// on another line, inside the block or outside it.
func (f *File) Set(e Entry) error {
	key, err := ParsePublicKey(e.Key)
	if err != nil {
		return err
	}
	e.Key = key
	if _, err := f.line(e); err != nil {
		return err
	}
	blob := keyBlob(key)
	return f.update(func(p *parsed) error {
		for _, part := range [][]byte{p.before, p.after} {
			for _, ln := range strings.Split(string(part), "\n") {
				if t := strings.TrimSpace(ln); t != "" && !strings.HasPrefix(t, "#") && strings.Contains(" "+t+" ", " "+blob+" ") {
					return fmt.Errorf("%w: it already has a line of its own in %s", ErrKeyInUse, f.Path)
				}
			}
		}
		out := p.entries[:0]
		for _, x := range p.entries {
			if x.same(e) {
				continue
			}
			if keyBlob(x.Key) == blob {
				return fmt.Errorf("%w by another member or device", ErrKeyInUse)
			}
			out = append(out, x)
		}
		p.entries = append(out, e)
		return nil
	})
}

// Remove deletes every line of the member: its own key and those of its
// devices. Removing lines that are not there is not an error.
func (f *File) Remove(shareID string, memberID int64) error {
	return f.removeWhere(func(x Entry) bool { return x.ShareID == shareID && x.MemberID == memberID })
}

// RemoveDevice deletes the line of one device of the member; deviceID 0
// is the member's own key. Removing a line that is not there is not an
// error.
func (f *File) RemoveDevice(shareID string, memberID, deviceID int64) error {
	e := Entry{ShareID: shareID, MemberID: memberID, DeviceID: deviceID}
	return f.removeWhere(e.same)
}

func (f *File) removeWhere(drop func(Entry) bool) error {
	return f.update(func(p *parsed) error {
		out := p.entries[:0]
		for _, x := range p.entries {
			if !drop(x) {
				out = append(out, x)
			}
		}
		p.entries = out
		return nil
	})
}

// Replace rewrites the whole block with entries (shfd keys sync). Entries
// whose key clashes with one before them or with a line outside the block
// are left out and returned as skipped.
func (f *File) Replace(entries []Entry) (skipped []Entry, err error) {
	err = f.update(func(p *parsed) error {
		outside := map[string]bool{}
		for _, part := range [][]byte{p.before, p.after} {
			for _, ln := range strings.Split(string(part), "\n") {
				if t := strings.TrimSpace(ln); t != "" && !strings.HasPrefix(t, "#") {
					for _, w := range strings.Fields(t) {
						outside[w] = true
					}
				}
			}
		}
		seen := map[string]bool{}
		p.entries = nil
		for _, e := range entries {
			key, err := ParsePublicKey(e.Key)
			if err != nil {
				skipped = append(skipped, e)
				continue
			}
			e.Key = key
			blob := keyBlob(key)
			if outside[blob] || seen[blob] {
				skipped = append(skipped, e)
				continue
			}
			seen[blob] = true
			p.entries = append(p.entries, e)
		}
		return nil
	})
	return skipped, err
}

// Entries lists the lines of the block.
func (f *File) Entries() ([]Entry, error) {
	p, err := f.read()
	if err != nil {
		return nil, err
	}
	return p.entries, nil
}
