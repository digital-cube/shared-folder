// Package rsyncgate is the forced command sshd runs for every member's key
// (docs/specifikacija.md, section 5):
//
//	command="shfd rsync-gate --data-root D --share <share-id> --member <member-id>",no-pty,… <key>
//
// It reads the command the client asked for from SSH_ORIGINAL_COMMAND and
// lets through exactly two shapes of rsync in server mode:
//
//	rsync --server          <options> . tmp/<upload_id>    upload: rsync receives
//	rsync --server --sender <options> . storage/<path>     download: rsync sends
//
// An upload may write only tmp/<upload_id> of its folder, and only while
// that upload_id is an open upload of the same member. A download may read
// only a regular file inside storage/, checked after symlinks and ".." are
// resolved. Options come from a short allow-list; everything else
// (deleting, --remove-source-files, -e beyond rsync's own capability
// string, -s which would hide the arguments from this check, …) is
// refused. A revoked member is refused even while the line in
// authorized_keys is still there.
//
// The command is split into words here, without a shell, and rsync is
// started directly with absolute paths the gate computed itself. Every
// call leaves one line in service.log: member, direction, path, outcome.
package rsyncgate

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/digital-cube/shared-folder/internal/server"
)

// Direction of a transfer, seen from the client.
type Direction string

const (
	Upload   Direction = "upload"   // the server's rsync receives into tmp/
	Download Direction = "download" // the server's rsync sends from storage/
)

// Reasons of a refusal. The client reads the code from stderr; NotFound
// tells it the file is gone from the server (fetch the newer state).
const (
	CodeNotRsync = "not_rsync" // not "rsync --server …"
	CodeSyntax   = "syntax"    // quoting, missing ".", several paths
	CodeOption   = "option"    // option not on the allow-list
	CodePath     = "path"      // outside tmp/<upload_id> or storage/
	CodeUpload   = "upload"    // upload_id not open for this member
	CodeNotFound = "not_found" // download of a file that does not exist
	CodeMember   = "member"    // unknown or revoked member
)

// Denied is a refused command.
type Denied struct {
	Code string
	Msg  string
}

func (d *Denied) Error() string { return "denied (" + d.Code + "): " + d.Msg }

func deny(code, format string, a ...any) *Denied {
	return &Denied{Code: code, Msg: fmt.Sprintf(format, a...)}
}

// Request is a syntactically valid rsync server command.
type Request struct {
	Direction Direction
	Options   []string // between --server/--sender and "."; each one allowed
	Path      string   // as requested: tmp/<upload_id> or storage/<path>
}

// Split breaks a command line into words the way a POSIX shell does for
// quoting (blanks separate words; '…' is literal; "…" is literal except
// for \" \\ \$ \` and \newline; \ outside quotes takes the next character
// literally) but performs no expansion of any kind: $, `, *, ; and the
// like are ordinary characters. rsync (3.2.4 and later) backslash-escapes
// the remote file names it puts on the command line for exactly this
// kind of splitting.
func Split(s string) ([]string, error) {
	var words []string
	var cur strings.Builder
	inWord := false
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c == ' ' || c == '\t' || c == '\n':
			if inWord {
				words = append(words, cur.String())
				cur.Reset()
				inWord = false
			}
		case c == '\\':
			if i+1 >= len(s) {
				return nil, errors.New("trailing backslash")
			}
			i++
			if s[i] != '\n' { // backslash-newline is a line continuation
				cur.WriteByte(s[i])
				inWord = true
			}
		case c == '\'':
			j := strings.IndexByte(s[i+1:], '\'')
			if j < 0 {
				return nil, errors.New("unterminated single quote")
			}
			cur.WriteString(s[i+1 : i+1+j])
			i += j + 1
			inWord = true
		case c == '"':
			i++
			for ; i < len(s) && s[i] != '"'; i++ {
				if s[i] == '\\' && i+1 < len(s) && strings.IndexByte("\"\\$`\n", s[i+1]) >= 0 {
					i++
					if s[i] == '\n' {
						continue
					}
				}
				cur.WriteByte(s[i])
			}
			if i >= len(s) {
				return nil, errors.New("unterminated double quote")
			}
			inWord = true
		default:
			cur.WriteByte(c)
			inWord = true
		}
	}
	if inWord {
		words = append(words, cur.String())
	}
	return words, nil
}

// Short options allowed in the cluster rsync sends ("-vtze.LsfxCIvu"):
// verbose, quiet, times, compress, checksum, ignore-times, whole-file.
const allowedShort = "vqtzcIW"

// The "e" in a server cluster is not --rsh: it introduces the client's
// capability string, which rsync puts there by itself.
var capsRE = regexp.MustCompile(`^\.[A-Za-z0-9]*$`)

var (
	digitsRE  = regexp.MustCompile(`^[0-9]{1,9}$`)
	bwlimitRE = regexp.MustCompile(`^[0-9]{1,9}(\.[0-9]{1,3})?[KkMmGg]?$`)
)

// Long options and the values they accept. A nil check means the option
// takes no value.
var allowedLong = map[string]struct {
	dir   Direction // "" for both directions
	value *regexp.Regexp
}{
	"--partial":        {Upload, nil}, // keep an interrupted upload as tmp/<id>, resumed next time
	"--inplace":        {Download, nil},
	"--stats":          {"", nil},
	"--timeout":        {"", digitsRE},
	"--bwlimit":        {"", bwlimitRE},
	"--compress-level": {"", digitsRE},
}

// Refusals worth a precise message; anything else not allowed gets a
// generic one.
var refusedLong = map[string]string{
	"--remove-source-files": "would delete files on the server",
	"--remove-sent-files":   "would delete files on the server",
	"--delete":              "deleting is the control API's job",
	"--partial-dir":         "writes outside tmp/<upload_id>",
	"--temp-dir":            "writes outside tmp/<upload_id>",
	"--backup":              "writes outside tmp/<upload_id>",
	"--backup-dir":          "writes outside tmp/<upload_id>",
	"--link-dest":           "reads outside the allowed path",
	"--copy-dest":           "reads outside the allowed path",
	"--compare-dest":        "reads outside the allowed path",
	"--files-from":          "reads a file list on the server",
	"--exclude-from":        "reads a file on the server",
	"--include-from":        "reads a file on the server",
	"--log-file":            "writes a file on the server",
	"--write-batch":         "writes a file on the server",
	"--only-write-batch":    "writes a file on the server",
	"--read-batch":          "reads a file on the server",
	"--rsh":                 "remote shell options are not accepted on the server side",
	"--rsync-path":          "no other program may be started",
	"--protect-args":        "hides the arguments from rsync-gate",
	"--secluded-args":       "hides the arguments from rsync-gate",
	"--inplace":             "an upload must go through a temporary file and a rename",
	"--append":              "not used by shf",
	"--append-verify":       "not used by shf",
}

func checkOption(opt string, dir Direction) error {
	if strings.HasPrefix(opt, "--") {
		name, val, hasVal := strings.Cut(opt, "=")
		a, ok := allowedLong[name]
		if ok && (a.dir == "" || a.dir == dir) && (a.value == nil) == !hasVal && (a.value == nil || a.value.MatchString(val)) {
			return nil
		}
		if strings.HasPrefix(name, "--delete") || strings.HasPrefix(name, "--del") || name == "--max-delete" {
			return deny(CodeOption, "%s: deleting is the control API's job", name)
		}
		if why, ok := refusedLong[name]; ok {
			return deny(CodeOption, "%s: %s", name, why)
		}
		if ok {
			return deny(CodeOption, "%s is not allowed here", opt)
		}
		return deny(CodeOption, "option %s is not allowed", name)
	}
	if len(opt) < 2 || opt[0] != '-' {
		return deny(CodeSyntax, "unexpected argument %q before \".\"", opt)
	}
	cluster := opt[1:]
	letters, caps, hasCaps := strings.Cut(cluster, "e")
	if hasCaps && !capsRE.MatchString(caps) {
		return deny(CodeOption, "-e is accepted only as rsync's own capability string, not %q", opt)
	}
	for _, c := range letters {
		switch {
		case c == 's':
			return deny(CodeOption, "-s (--secluded-args) hides the arguments from rsync-gate")
		case !strings.ContainsRune(allowedShort, c):
			return deny(CodeOption, "option -%c is not allowed", c)
		}
	}
	return nil
}

// Parse checks the shape of an rsync server command and its options. It
// does not look at the disk or the database; Gate.Authorize does.
func Parse(cmd string) (*Request, error) {
	words, err := Split(cmd)
	if err != nil {
		return nil, deny(CodeSyntax, "%v", err)
	}
	if len(words) == 0 || words[0] != "rsync" {
		return nil, deny(CodeNotRsync, "only rsync is allowed on this key")
	}
	if len(words) < 2 || words[1] != "--server" {
		return nil, deny(CodeNotRsync, "only rsync in server mode (rsync --server …) is allowed")
	}
	req := &Request{Direction: Upload}
	i := 2
	if i < len(words) && words[i] == "--sender" {
		req.Direction = Download
		i++
	}
	for ; i < len(words) && words[i] != "."; i++ {
		if err := checkOption(words[i], req.Direction); err != nil {
			return nil, err
		}
		req.Options = append(req.Options, words[i])
	}
	if i >= len(words) {
		return nil, deny(CodeSyntax, `missing "." before the path`)
	}
	paths := words[i+1:]
	if len(paths) != 1 {
		return nil, deny(CodeSyntax, "exactly one path is allowed, got %d", len(paths))
	}
	req.Path = paths[0]
	return req, nil
}

// Plan is an authorized command: run Rsync with Args in Dir.
type Plan struct {
	Request
	Abs  string   // absolute path on the server
	Args []string // after the rsync binary: --server [--sender] options . Abs
	Dir  string
}

// Gate checks commands for one member of one folder.
type Gate struct {
	Share    *server.Share
	MemberID int64
	// Rsync is the rsync binary to run; "" looks it up in PATH.
	Rsync string
}

// Authorize checks the member, the command and the path it names.
func (g *Gate) Authorize(cmd string) (*server.Member, *Plan, error) {
	m, err := g.Share.MemberByID(g.MemberID)
	if server.CodeOf(err) == server.CodeUnknownMember {
		return nil, nil, deny(CodeMember, "unknown member %d", g.MemberID)
	}
	if err != nil {
		return nil, nil, err
	}
	if m.RevokedAt != nil {
		return m, nil, deny(CodeMember, "member %s is revoked", m.Username)
	}
	req, err := Parse(cmd)
	if err != nil {
		return m, nil, err
	}
	p := &Plan{Request: *req, Dir: g.Share.Dir()}
	switch req.Direction {
	case Upload:
		err = g.upload(m, p)
	case Download:
		err = g.download(p)
	}
	if err != nil {
		return m, p, err
	}
	p.Args = []string{"--server"}
	if req.Direction == Download {
		p.Args = append(p.Args, "--sender")
	}
	p.Args = append(p.Args, req.Options...)
	p.Args = append(p.Args, ".", p.Abs)
	return m, p, nil
}

func (g *Gate) upload(m *server.Member, p *Plan) error {
	id, ok := strings.CutPrefix(p.Path, server.TmpDir+"/")
	if !ok || !server.ValidUploadID(id) {
		return deny(CodePath, "an upload may write only tmp/<upload_id>, not %q", p.Path)
	}
	if err := g.Share.CheckUpload(m.ID, id); err != nil {
		if server.CodeOf(err) == server.CodeUnknownUpload {
			return deny(CodeUpload, "%s is not an open upload of %s", id, m.Username)
		}
		return err
	}
	p.Abs = filepath.Join(g.Share.Dir(), server.TmpDir, id)
	if fi, err := os.Lstat(p.Abs); err == nil && !fi.Mode().IsRegular() {
		return deny(CodePath, "tmp/%s exists and is not a regular file", id)
	}
	return nil
}

func (g *Gate) download(p *Plan) error {
	rel, ok := strings.CutPrefix(p.Path, server.StorageDir+"/")
	if !ok || rel == "" || strings.ContainsRune(rel, 0) {
		return deny(CodePath, "a download may read only storage/<path>, not %q", p.Path)
	}
	root := filepath.Join(g.Share.Dir(), server.StorageDir)
	abs := filepath.Join(root, filepath.FromSlash(rel)) // cleans ".."
	if !within(abs, root) {
		return deny(CodePath, "%q is outside storage/", p.Path)
	}
	realRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return err
	}
	real, err := filepath.EvalSymlinks(abs)
	if errors.Is(err, os.ErrNotExist) {
		return deny(CodeNotFound, "%s does not exist", p.Path)
	}
	if err != nil {
		return deny(CodePath, "%s: %v", p.Path, err)
	}
	if !within(real, realRoot) {
		return deny(CodePath, "%q leads outside storage/", p.Path)
	}
	fi, err := os.Stat(real)
	if err != nil {
		return deny(CodeNotFound, "%s does not exist", p.Path)
	}
	if !fi.Mode().IsRegular() {
		return deny(CodePath, "%s is not a regular file", p.Path)
	}
	p.Abs = real
	return nil
}

// within reports whether p is strictly inside dir (both clean).
func within(p, dir string) bool {
	return strings.HasPrefix(p, dir+string(filepath.Separator))
}

// Run authorizes cmd, runs rsync with stdin/stdout/stderr, logs the call
// to service.log and returns the exit code: rsync's own, or 1 for a
// refused command (with a "shfd rsync-gate: denied (<code>): …" line on
// stderr).
func (g *Gate) Run(ctx context.Context, cmd string, stdin io.Reader, stdout, stderr io.Writer) int {
	m, p, err := g.Authorize(cmd)
	t := server.Transfer{Member: fmt.Sprintf("#%d", g.MemberID)}
	if m != nil {
		t.Member = m.Username
	}
	if p != nil {
		t.Direction, t.Path = string(p.Direction), p.Path
	}
	logIt := func() {
		if lerr := g.Share.LogTransfer(t); lerr != nil {
			fmt.Fprintf(stderr, "shfd rsync-gate: writing service.log: %v\n", lerr)
		}
	}
	if err != nil {
		var d *Denied
		if errors.As(err, &d) {
			t.Outcome, t.Detail = "denied", d.Code+": "+d.Msg
			if d.Code == CodeNotRsync || d.Code == CodeSyntax {
				t.Detail += "; command " + truncate(cmd, 200)
			}
		} else {
			t.Outcome, t.Detail = "failed", err.Error()
		}
		logIt()
		fmt.Fprintf(stderr, "shfd rsync-gate: %v\n", err)
		return 1
	}
	bin := g.Rsync
	if bin == "" {
		if bin, err = exec.LookPath("rsync"); err != nil {
			t.Outcome, t.Detail = "failed", "rsync is not installed on the server"
			logIt()
			fmt.Fprintln(stderr, "shfd rsync-gate: rsync is not installed on the server")
			return 1
		}
	}
	c := exec.CommandContext(ctx, bin, p.Args...)
	c.Dir, c.Stdin, c.Stdout, c.Stderr = p.Dir, stdin, stdout, stderr
	c.Cancel = func() error { return c.Process.Signal(os.Interrupt) }
	err = c.Run()
	code := 0
	var ee *exec.ExitError
	switch {
	case err == nil:
		t.Outcome = "ok"
	case errors.As(err, &ee):
		code = ee.ExitCode()
		if code < 0 {
			code = 1
		}
		t.Outcome, t.Detail = "failed", "rsync: "+ee.String()
	default:
		code = 1
		t.Outcome, t.Detail = "failed", err.Error()
		fmt.Fprintf(stderr, "shfd rsync-gate: %v\n", err)
	}
	logIt()
	return code
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
