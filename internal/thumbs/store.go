// Package thumbs makes and keeps the thumbnails of a folder's images and
// PDFs (docs/specifikacija.md, section 3, "Thumbnails").
//
// A thumbnail is keyed by the SHA-256 of the content (files.hash), not by
// its path, so a rename, move, copy or restore from the trash needs no new
// one and its URL never changes. Every source gets three sizes, JPEG:
//
//	<data-root>/<share-id>/thumbnails/<hash[:2]>/<hash>.l.jpg   512 px
//	                                            <hash>.m.jpg   256 px
//	                                            <hash>.s.jpg    96 px
//	                                            <hash>.fail    why it failed
//	                                            <hash>.work    being made
//
// The sizes are written l, m, s, each atomically, so a thumbnail is ready
// when <hash>.s.jpg exists. A .fail marker keeps the hash from being tried
// again (shfd thumbs --retry-failed clears them); a .work marker left
// behind by a crash becomes a .fail when the service starts again.
//
// Only shfd thumbs writes here; shfd serve reads the directory once per
// folder to build its index and then serves the JPEG files.
package thumbs

import (
	"errors"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// Dir is the name of the thumbnails directory inside a share directory,
// next to storage/, trash/ and tmp/.
const Dir = "thumbnails"

// Size is one thumbnail size: the longer side in pixels.
type Size struct {
	Name string
	Px   int
}

// Sizes are made largest first (each smaller one is scaled from the one
// before it); the last one marks a thumbnail as ready.
var Sizes = []Size{{"l", 512}, {"m", 256}, {"s", 96}}

// DefaultSize is the size GET /admin/thumbs/{hash} sends without ?size=.
const DefaultSize = "m"

// ReadySize is the size whose file says a thumbnail is complete.
var ReadySize = Sizes[len(Sizes)-1].Name

// ValidSize reports whether name is one of Sizes.
func ValidSize(name string) bool {
	for _, s := range Sizes {
		if s.Name == name {
			return true
		}
	}
	return false
}

// Kind is what kind of source a thumbnail is made from.
type Kind string

// Kinds of source.
const (
	KindImage Kind = "image"
	KindPDF   Kind = "pdf"
)

// AllKinds are the kinds this version can make thumbnails of.
var AllKinds = []Kind{KindImage, KindPDF}

var exts = map[string]Kind{
	"jpg": KindImage, "jpeg": KindImage, "png": KindImage, "gif": KindImage,
	"webp": KindImage, "bmp": KindImage, "tif": KindImage, "tiff": KindImage,
	"pdf": KindPDF,
}

// KindOf is the kind of a file by the extension of its name (any letter
// case); "" when it gets no thumbnail.
func KindOf(name string) Kind {
	ext := path.Ext(name)
	if ext == "" {
		return ""
	}
	return exts[strings.ToLower(ext[1:])]
}

// ParseKind reads a kind; ok is false for an unknown one.
func ParseKind(s string) (Kind, bool) {
	for _, k := range AllKinds {
		if string(k) == s {
			return k, true
		}
	}
	return "", false
}

var hashRE = regexp.MustCompile(`^[0-9a-f]{64}$`)

// ValidHash reports whether h is a lowercase hex SHA-256, the only form a
// thumbnail name is ever built from.
func ValidHash(h string) bool { return hashRE.MatchString(h) }

// Marker suffixes.
const (
	failSuffix = ".fail"
	workSuffix = ".work"
)

// Store is the thumbnails directory of one folder.
type Store struct {
	Dir string // <data-root>/<share-id>/thumbnails
}

// NewStore is the store of a share directory.
func NewStore(shareDir string) Store { return Store{Dir: filepath.Join(shareDir, Dir)} }

func (st Store) sub(hash string) string { return filepath.Join(st.Dir, hash[:2]) }

// Path is the file of one size of a thumbnail. hash must be ValidHash and
// size ValidSize; nothing else may reach the file system.
func (st Store) Path(hash, size string) string {
	return filepath.Join(st.sub(hash), hash+"."+size+".jpg")
}

func (st Store) failPath(hash string) string { return filepath.Join(st.sub(hash), hash+failSuffix) }
func (st Store) workPath(hash string) string { return filepath.Join(st.sub(hash), hash+workSuffix) }

// Ready reports whether the thumbnail of hash is complete.
func (st Store) Ready(hash string) bool {
	_, err := os.Stat(st.Path(hash, ReadySize))
	return err == nil
}

// Failed reports whether hash has a .fail marker.
func (st Store) Failed(hash string) bool {
	_, err := os.Stat(st.failPath(hash))
	return err == nil
}

// FailReason is the text of the .fail marker of hash.
func (st Store) FailReason(hash string) (string, error) {
	b, err := os.ReadFile(st.failPath(hash))
	return strings.TrimSpace(string(b)), err
}

// parseName splits "<hash>.<rest>" in a thumbnails subdirectory.
func parseName(name string) (hash, rest string, ok bool) {
	if len(name) < 65 || name[64] != '.' || !ValidHash(name[:64]) {
		return "", "", false
	}
	return name[:64], name[64:], true
}

// walk calls fn for every file named "<hash>.<…>" in the two-letter
// subdirectories. A missing thumbnails directory is empty.
func (st Store) walk(fn func(dir, name, hash, rest string) error) error {
	subs, err := os.ReadDir(st.Dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	for _, sd := range subs {
		if !sd.IsDir() || len(sd.Name()) != 2 {
			continue
		}
		dir := filepath.Join(st.Dir, sd.Name())
		des, err := os.ReadDir(dir)
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				continue
			}
			return err
		}
		for _, de := range des {
			hash, rest, ok := parseName(de.Name())
			if !ok || hash[:2] != sd.Name() {
				continue
			}
			if err := fn(dir, de.Name(), hash, rest); err != nil {
				return err
			}
		}
	}
	return nil
}

// Scan reads the directory once: the hashes whose thumbnail is ready and
// those with a .fail marker.
func (st Store) Scan() (ready, failed map[string]bool, err error) {
	ready, failed = map[string]bool{}, map[string]bool{}
	err = st.walk(func(_, _, hash, rest string) error {
		switch rest {
		case "." + ReadySize + ".jpg":
			ready[hash] = true
		case failSuffix:
			failed[hash] = true
		}
		return nil
	})
	return ready, failed, err
}

// Hashes lists every hash that has a thumbnail file or a .fail marker.
func (st Store) Hashes() ([]string, error) {
	seen := map[string]bool{}
	var out []string
	err := st.walk(func(_, _, hash, rest string) error {
		if rest == workSuffix || seen[hash] {
			return nil
		}
		seen[hash] = true
		out = append(out, hash)
		return nil
	})
	return out, err
}

// Remove deletes every size of hash and its .fail marker.
func (st Store) Remove(hash string) error {
	var errs []error
	for _, s := range Sizes {
		if err := os.Remove(st.Path(hash, s.Name)); err != nil && !errors.Is(err, fs.ErrNotExist) {
			errs = append(errs, err)
		}
	}
	if err := os.Remove(st.failPath(hash)); err != nil && !errors.Is(err, fs.ErrNotExist) {
		errs = append(errs, err)
	}
	return errors.Join(errs...)
}

// CrashReason is the text of the .fail marker that a .work marker left by
// a crash becomes.
const CrashReason = "crashed while generating"

// RecoverWork turns every .work marker, which a process that died while
// making that thumbnail left behind, into a .fail marker, so a file that
// kills the process is not tried again. It returns the hashes.
func (st Store) RecoverWork() ([]string, error) {
	var out []string
	err := st.walk(func(dir, name, hash, rest string) error {
		if rest != workSuffix {
			return nil
		}
		if err := os.WriteFile(st.failPath(hash), []byte(CrashReason+"\n"), 0o600); err != nil {
			return err
		}
		os.Remove(filepath.Join(dir, name))
		for _, s := range Sizes { // a partial set is not a thumbnail
			os.Remove(st.Path(hash, s.Name))
		}
		out = append(out, hash)
		return nil
	})
	return out, err
}

// ClearFailed removes every .fail marker, so those hashes are tried again;
// it returns how many there were.
func (st Store) ClearFailed() (int, error) {
	n := 0
	err := st.walk(func(dir, name, _, rest string) error {
		if rest != failSuffix {
			return nil
		}
		if err := os.Remove(filepath.Join(dir, name)); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
		n++
		return nil
	})
	return n, err
}

// RemoveStaleTemp deletes temporary files (".<name>.*") older than age
// that an interrupted write left in the subdirectories.
func (st Store) RemoveStaleTemp(age time.Duration, now time.Time) (int, error) {
	subs, err := os.ReadDir(st.Dir)
	if errors.Is(err, fs.ErrNotExist) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	n := 0
	for _, sd := range subs {
		if !sd.IsDir() || len(sd.Name()) != 2 {
			continue
		}
		dir := filepath.Join(st.Dir, sd.Name())
		des, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, de := range des {
			if !strings.HasPrefix(de.Name(), ".") {
				continue
			}
			if fi, err := de.Info(); err == nil && now.Sub(fi.ModTime()) > age {
				if os.Remove(filepath.Join(dir, de.Name())) == nil {
					n++
				}
			}
		}
	}
	return n, nil
}

// writeAtomic writes data as name in dir through a temporary file and a
// rename, so a reader sees the whole file or none.
func writeAtomic(dir, name string, data []byte) error {
	f, err := os.CreateTemp(dir, "."+name+".*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	_, err = f.Write(data)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Chmod(tmp, 0o600)
	}
	if err == nil {
		err = os.Rename(tmp, filepath.Join(dir, name))
	}
	if err != nil {
		os.Remove(tmp)
	}
	return err
}
