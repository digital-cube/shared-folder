// Package pathx normalizes and validates paths inside a shared folder and
// builds the names of derived files (conflict copies, trash entries).
//
// Every path in the system is relative to the root of the shared folder,
// uses "/" as the only separator and is in Unicode NFC form
// (docs/specifikacija.md, section 6 "Imena").
package pathx

import (
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"golang.org/x/text/cases"
	"golang.org/x/text/unicode/norm"
)

// Reasons a path is rejected. Normalize wraps them in a *PathError, so test
// them with errors.Is.
var (
	ErrEmpty            = errors.New("empty path")
	ErrInvalidUTF8      = errors.New("path is not valid UTF-8")
	ErrAbsolute         = errors.New("absolute path")
	ErrDotSegment       = errors.New(`"." or ".." segment`)
	ErrControlChar      = errors.New("control character")
	ErrForbiddenChar    = errors.New(`forbidden character (one of < > : " | ? *)`)
	ErrTrailingDotSpace = errors.New("segment ends with a dot or a space")
	ErrReservedName     = errors.New("reserved Windows device name")
	ErrInvalidUser      = errors.New("invalid username")
	ErrInvalidReason    = errors.New("invalid trash reason")
	ErrNoDerivedName    = errors.New("cannot derive a name from an empty base name")
)

// PathError reports why a path was rejected.
type PathError struct {
	Path    string // the path as given
	Segment string // offending segment, if the problem is in one segment
	Err     error  // one of the Err* values above
}

func (e *PathError) Error() string {
	if e.Segment != "" {
		return fmt.Sprintf("invalid path %q: segment %q: %v", e.Path, e.Segment, e.Err)
	}
	return fmt.Sprintf("invalid path %q: %v", e.Path, e.Err)
}

func (e *PathError) Unwrap() error { return e.Err }

// Normalize returns the canonical form of a path inside the shared folder:
// NFC, "/"-separated, without empty segments (a//b becomes a/b, a trailing
// "/" is dropped). A backslash is an ordinary character, not a separator.
//
// It rejects an empty path, an absolute path, "." and ".." segments, control
// characters (U+0000–U+001F, U+007F), the characters < > : " | ? *, a segment
// ending in a dot or a space, and the reserved Windows device names (CON,
// PRN, AUX, NUL, COM1–COM9, LPT1–LPT9) regardless of case and extension.
func Normalize(p string) (string, error) {
	if !utf8.ValidString(p) {
		return "", &PathError{Path: p, Err: ErrInvalidUTF8}
	}
	n := norm.NFC.String(p)
	if n == "" {
		return "", &PathError{Path: p, Err: ErrEmpty}
	}
	if n[0] == '/' {
		return "", &PathError{Path: p, Err: ErrAbsolute}
	}
	segs := strings.Split(n, "/")
	out := segs[:0]
	for _, s := range segs {
		if s == "" {
			continue
		}
		if err := checkSegment(s); err != nil {
			return "", &PathError{Path: p, Segment: s, Err: err}
		}
		out = append(out, s)
	}
	if len(out) == 0 {
		return "", &PathError{Path: p, Err: ErrEmpty}
	}
	return strings.Join(out, "/"), nil
}

// ValidName reports whether name is acceptable as a single path segment
// (no "/" allowed) and returns the reason when it is not.
func ValidName(name string) error {
	if name == "" {
		return ErrEmpty
	}
	if strings.Contains(name, "/") {
		return fmt.Errorf("name %q contains %q", name, "/")
	}
	return checkSegment(norm.NFC.String(name))
}

func checkSegment(s string) error {
	if s == "." || s == ".." {
		return ErrDotSegment
	}
	for _, r := range s {
		switch {
		case r < 0x20 || r == 0x7f:
			return ErrControlChar
		case strings.ContainsRune(`<>:"|?*`, r):
			return ErrForbiddenChar
		}
	}
	if last := s[len(s)-1]; last == '.' || last == ' ' {
		return ErrTrailingDotSpace
	}
	if isReserved(s) {
		return ErrReservedName
	}
	return nil
}

// isReserved reports whether Windows treats the segment as a device name:
// the part before the first dot (with trailing spaces trimmed, as Windows
// does) is CON, PRN, AUX, NUL, COM1–COM9 or LPT1–LPT9, in any case.
// COM¹ COM² COM³ and LPT¹ LPT² LPT³ are reserved by Windows as well.
func isReserved(s string) bool {
	stem := s
	if i := strings.IndexByte(stem, '.'); i >= 0 {
		stem = stem[:i]
	}
	stem = strings.ToUpper(strings.TrimRight(stem, " "))
	switch stem {
	case "CON", "PRN", "AUX", "NUL":
		return true
	}
	if rest, ok := strings.CutPrefix(stem, "COM"); ok {
		return isPortDigit(rest)
	}
	if rest, ok := strings.CutPrefix(stem, "LPT"); ok {
		return isPortDigit(rest)
	}
	return false
}

func isPortDigit(s string) bool {
	switch s {
	case "1", "2", "3", "4", "5", "6", "7", "8", "9", "¹", "²", "³":
		return true
	}
	return false
}

var folder = cases.Fold()

// FoldKey returns the key under which two names that differ only in letter
// case collide: Unicode case folding over the NFC form, renormalized to NFC.
// The server keeps one path per key. FoldKey does not validate p.
func FoldKey(p string) string {
	return norm.NFC.String(folder.String(norm.NFC.String(p)))
}
