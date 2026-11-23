// Package ignore implements .shfignore: the rules that keep paths out of
// synchronization (docs/specifikacija.md, section 7).
//
// The syntax and semantics are those of .gitignore:
//
//   - blank lines are skipped, "#" starts a comment, "\" escapes the next
//     character ("\#", "\!", "\ " for a trailing space);
//   - "*", "?", "[...]" and "**" globs, matched case-sensitively;
//   - a trailing "/" makes a pattern match directories only;
//   - a "/" at the start or in the middle anchors the pattern to the
//     directory holding the .shfignore; without one the pattern matches a
//     name at any depth below it;
//   - "!" negates: a path matched by it is not ignored;
//   - a .shfignore applies to its own directory and everything below; the
//     deepest one with a matching pattern decides, and within one file the
//     last matching pattern wins;
//   - a path inside an ignored directory is ignored, and no "!" can bring it
//     back (as in git);
//   - .shfignore is not special: if it matches itself, it is ignored.
//
// Below all .shfignore files sits a built-in list (DefaultPatterns) that a
// "!" in any .shfignore overrides. Above everything sits the client's own
// temporary files (ClientTempPattern), which are always ignored.
package ignore

import (
	"bytes"
	"errors"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/text/unicode/norm"
)

// FileName is the name of an ignore file.
const FileName = ".shfignore"

// ClientTempPrefix starts the name of every temporary file the client
// creates inside the shared folder (a download before its atomic rename).
const ClientTempPrefix = ".shf-tmp-"

// ClientTempPattern is always ignored; no "!" can undo it.
const ClientTempPattern = ClientTempPrefix + "*"

// DefaultPatterns are ignored unless a .shfignore says otherwise with "!".
var DefaultPatterns = []string{
	".DS_Store",
	"Thumbs.db",
	"desktop.ini",
	"~$*",
	"*.swp",
	"*.tmp",
	"*.part",
	"*.crdownload",
}

// Matcher answers whether a path is ignored. It is immutable and safe for
// concurrent use.
type Matcher struct {
	// files maps a directory ("" for the root, "a/b" otherwise) to the
	// patterns of the .shfignore in it.
	files    map[string][]pattern
	defaults []pattern
	forced   []pattern
}

// New builds a matcher from .shfignore contents held in memory. The map key
// is the directory that holds the file, relative to the root of the shared
// folder: "" (or ".") for the root, "docs/img" for docs/img/.shfignore.
func New(files map[string][]byte) *Matcher {
	m := &Matcher{
		files:    make(map[string][]pattern, len(files)),
		defaults: parseLines(DefaultPatterns),
		forced:   parseLines([]string{ClientTempPattern}),
	}
	for dir, content := range files {
		m.files[cleanDir(dir)] = parse(content)
	}
	return m
}

// With returns a copy of m in which the .shfignore of dir has the given
// content (nil removes it). It is how a new .shfignore is applied before it
// is written to disk; m itself is unchanged.
func (m *Matcher) With(dir string, content []byte) *Matcher {
	c := *m
	c.files = maps.Clone(m.files)
	if content == nil {
		delete(c.files, cleanDir(dir))
	} else {
		c.files[cleanDir(dir)] = parse(content)
	}
	return &c
}

// FromDir builds a matcher from every .shfignore under root. Directories
// that are themselves ignored are not descended into (their .shfignore
// could not un-ignore anything anyway), and symbolic links are not
// followed.
func FromDir(root string) (*Matcher, error) {
	m := New(nil)
	if err := m.load(root, ""); err != nil {
		return nil, err
	}
	return m, nil
}

// load reads the .shfignore in dir (on disk) whose path relative to the
// shared folder is rel (NFC; on-disk names may be NFD, as on macOS), then
// descends into the subdirectories that are not ignored.
func (m *Matcher) load(dir, rel string) error {
	name := filepath.Join(dir, FileName)
	fi, err := os.Lstat(name)
	switch {
	case err == nil && fi.Mode().IsRegular():
		content, err := os.ReadFile(name)
		if err != nil {
			return err
		}
		m.files[rel] = parse(content)
	case err == nil, errors.Is(err, fs.ErrNotExist):
		// none here, or not a regular file (a directory or a symlink,
		// neither of which is synchronized): no rules
	default:
		return err
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	for _, e := range entries {
		if !e.IsDir() { // symlinks report false here: not followed
			continue
		}
		child := norm.NFC.String(e.Name())
		if rel != "" {
			child = rel + "/" + child
		}
		if m.Match(child, true) {
			continue
		}
		if err := m.load(filepath.Join(dir, e.Name()), child); err != nil {
			return err
		}
	}
	return nil
}

// Match reports whether relPath (relative to the root of the shared folder,
// "/"-separated, as produced by pathx.Normalize) is ignored. isDir says
// whether it names a directory.
func (m *Matcher) Match(relPath string, isDir bool) bool {
	p := strings.Trim(norm.NFC.String(relPath), "/")
	if p == "" {
		return false // the root itself is never ignored
	}
	// A path inside an ignored directory is ignored, whatever follows.
	for i := 0; i < len(p); i++ {
		if p[i] == '/' && m.decide(p[:i], true) {
			return true
		}
	}
	return m.decide(p, isDir)
}

// decide applies the rules to p alone, ignoring its parents.
func (m *Matcher) decide(p string, isDir bool) bool {
	full := []rune(p)
	base := full
	for i := len(full) - 1; i >= 0; i-- {
		if full[i] == '/' {
			base = full[i+1:]
			break
		}
	}
	if v, ok := lastMatch(m.forced, full, base, isDir); ok && v {
		return true
	}
	// Deepest .shfignore first: walk p's ancestors from its parent up.
	dir := p
	for {
		i := strings.LastIndexByte(dir, '/')
		if i < 0 {
			dir = ""
		} else {
			dir = dir[:i]
		}
		if pats, ok := m.files[dir]; ok {
			rel := full
			if dir != "" {
				rel = full[len([]rune(dir))+1:]
			}
			if v, ok := lastMatch(pats, rel, base, isDir); ok {
				return v
			}
		}
		if dir == "" {
			break
		}
	}
	if v, ok := lastMatch(m.defaults, full, base, isDir); ok {
		return v
	}
	return false
}

// lastMatch returns the verdict of the last pattern in pats that matches:
// true for ignored, false for a negated (re-included) match. ok is false
// when nothing matches. rel is the path relative to the directory of the
// patterns, base its last segment.
func lastMatch(pats []pattern, rel, base []rune, isDir bool) (ignored, ok bool) {
	for i := len(pats) - 1; i >= 0; i-- {
		if pats[i].matches(rel, base, isDir) {
			return !pats[i].negate, true
		}
	}
	return false, false
}

func cleanDir(dir string) string {
	dir = strings.Trim(norm.NFC.String(filepath.ToSlash(dir)), "/")
	if dir == "." {
		return ""
	}
	return dir
}

// pattern is one parsed line of a .shfignore.
type pattern struct {
	glob     []rune
	negate   bool
	dirOnly  bool
	anchored bool // matched against the relative path, not the base name
}

func (p pattern) matches(rel, base []rune, isDir bool) bool {
	if p.dirOnly && !isDir {
		return false
	}
	if p.anchored {
		return wildmatch(p.glob, rel)
	}
	return wildmatch(p.glob, base)
}

// parse returns the patterns of one .shfignore file.
func parse(content []byte) []pattern {
	content = bytes.TrimPrefix(content, []byte("\xef\xbb\xbf")) // UTF-8 BOM
	lines := strings.Split(string(content), "\n")
	return parseLines(lines)
}

func parseLines(lines []string) []pattern {
	var out []pattern
	for _, line := range lines {
		if p, ok := parseLine(line); ok {
			out = append(out, p)
		}
	}
	return out
}

func parseLine(line string) (pattern, bool) {
	line = strings.TrimSuffix(line, "\r")
	line = trimTrailingSpaces(line)
	if line == "" || line[0] == '#' {
		return pattern{}, false
	}
	var p pattern
	if line[0] == '!' {
		p.negate = true
		line = line[1:]
	}
	if strings.HasSuffix(line, "/") && !escaped(line, len(line)-1) {
		p.dirOnly = true
		line = strings.TrimRight(line, "/")
	}
	if line == "" {
		return pattern{}, false
	}
	if i := unescapedSlash(line); i >= 0 {
		p.anchored = true
		if i == 0 {
			line = line[1:]
		}
	}
	if line == "" {
		return pattern{}, false
	}
	p.glob = []rune(norm.NFC.String(line))
	return p, true
}

// trimTrailingSpaces drops trailing spaces that are not escaped with "\".
func trimTrailingSpaces(s string) string {
	end := len(s)
	for end > 0 && s[end-1] == ' ' && !escaped(s, end-1) {
		end--
	}
	return s[:end]
}

// escaped reports whether s[i] is preceded by an odd number of backslashes.
func escaped(s string, i int) bool {
	n := 0
	for j := i - 1; j >= 0 && s[j] == '\\'; j-- {
		n++
	}
	return n%2 == 1
}

// unescapedSlash returns the index of the first "/" that is not escaped.
func unescapedSlash(s string) int {
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '\\':
			i++
		case '/':
			return i
		}
	}
	return -1
}
