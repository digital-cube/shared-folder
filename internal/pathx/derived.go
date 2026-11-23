package pathx

import (
	"fmt"
	"regexp"
	"strings"
	"time"
)

// TimestampLayout is the UTC timestamp used in derived names: 20261123T145846Z.
const TimestampLayout = "20060102T150405Z"

// Timestamp formats t in UTC with TimestampLayout.
func Timestamp(t time.Time) string {
	return t.UTC().Format(TimestampLayout)
}

var usernameRE = regexp.MustCompile(`^[a-z0-9_-]{1,32}$`)

// ValidateUsername accepts 1–32 characters of a–z, 0–9, "-" and "_", so that
// a username is always safe inside a file name.
func ValidateUsername(u string) error {
	if !usernameRE.MatchString(u) {
		return fmt.Errorf("%w %q: want 1-32 characters of a-z, 0-9, '-', '_'", ErrInvalidUser, u)
	}
	return nil
}

// TrashReason says why a version went to the trash.
type TrashReason string

const (
	Deleted  TrashReason = "deleted"
	Replaced TrashReason = "replaced"
)

// ConflictPath returns the path of the conflict copy of p, in the same
// directory: the file name gets ".conflict-{user}-{ts}" before its extension.
//
//	docs/slika.png    -> docs/slika.conflict-ana-20261123T145846Z.png
//	arhiva.tar.gz     -> arhiva.tar.conflict-ana-20261123T145846Z.gz
//	Makefile          -> Makefile.conflict-ana-20261123T145846Z
//	.bashrc           -> .bashrc.conflict-ana-20261123T145846Z
//
// The extension is the part after the last dot of the file name; a leading
// dot (a hidden file) does not start an extension.
func ConflictPath(p, user string, t time.Time) (string, error) {
	if err := ValidateUsername(user); err != nil {
		return "", err
	}
	dir, name := splitDir(p)
	if name == "" {
		return "", ErrNoDerivedName
	}
	stem, ext := splitExt(name)
	return dir + stem + ".conflict-" + user + "-" + Timestamp(t) + ext, nil
}

// TrashName returns the name of p's entry in the (flat) trash directory:
// the file name of p followed by ".deleted-by-{user}-{ts}" or
// ".replaced-by-{user}-{ts}".
//
//	docs/slika.png -> slika.png.deleted-by-ana-20261123T145846Z
//
// Collisions inside the trash (the "-2", "-3" suffix) are the server's job.
func TrashName(p string, reason TrashReason, user string, t time.Time) (string, error) {
	if reason != Deleted && reason != Replaced {
		return "", fmt.Errorf("%w %q", ErrInvalidReason, reason)
	}
	if err := ValidateUsername(user); err != nil {
		return "", err
	}
	_, name := splitDir(p)
	if name == "" {
		return "", ErrNoDerivedName
	}
	return name + "." + string(reason) + "-by-" + user + "-" + Timestamp(t), nil
}

// splitDir splits p into its directory (with the trailing "/", or "") and
// file name.
func splitDir(p string) (dir, name string) {
	p = strings.TrimRight(p, "/")
	i := strings.LastIndexByte(p, '/')
	return p[:i+1], p[i+1:]
}

// splitExt splits a file name into stem and extension (with its dot).
func splitExt(name string) (stem, ext string) {
	i := strings.LastIndexByte(name, '.')
	if i <= 0 { // no dot, or only a leading dot (".bashrc")
		return name, ""
	}
	return name[:i], name[i:]
}
