package client

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"golang.org/x/text/unicode/norm"

	"github.com/digital-cube/shared-folder/internal/hashx"
	"github.com/digital-cube/shared-folder/internal/ignore"
	"github.com/digital-cube/shared-folder/internal/pathx"
)

// scanResult is the local folder as one cycle sees it.
type scanResult struct {
	// Local holds the paths that take part in synchronization.
	Local map[string]Entry
	// Disk maps a path (NFC, "/") to its name on disk relative to the
	// root, which may differ in normalization (NFD on macOS).
	Disk map[string]string
	// Untracked are paths that exist but are left alone: symlinks, other
	// non-regular files and names the server would refuse.
	Untracked map[string]bool
	// Problems are the rejected names, for shf status.
	Problems []Problem
	// Hashed counts the files whose content was hashed (size or mtime
	// differed from the manifest).
	Hashed int
}

// scan walks root. Ignored paths are left out (an ignored directory is
// not entered), symbolic links are skipped, names are compared in NFC, and
// a file is hashed only when its size or mtime differs from the manifest.
func scan(root string, m *ignore.Matcher, manifest map[string]ManifestEntry) (*scanResult, error) {
	s := &scanResult{Local: map[string]Entry{}, Disk: map[string]string{}, Untracked: map[string]bool{}}
	if err := s.walk(root, "", "", m, manifest); err != nil {
		return nil, err
	}
	sort.Slice(s.Problems, func(i, j int) bool { return s.Problems[i].Path < s.Problems[j].Path })
	return s, nil
}

func (s *scanResult) walk(root, diskRel, rel string, m *ignore.Matcher, manifest map[string]ManifestEntry) error {
	entries, err := os.ReadDir(filepath.Join(root, diskRel))
	if err != nil {
		return err
	}
	// Two names on disk that are equal in NFC are one path for the server.
	byNFC := map[string][]os.DirEntry{}
	for _, e := range entries {
		n := norm.NFC.String(e.Name())
		byNFC[n] = append(byNFC[n], e)
	}
	for name, es := range byNFC {
		p := name
		if rel != "" {
			p = rel + "/" + name
		}
		e := es[0]
		isDir := e.IsDir()
		if m.Match(p, isDir) {
			continue
		}
		if len(es) > 1 {
			s.Untracked[p] = true
			s.Problems = append(s.Problems, Problem{Path: p, Reason: "several names on disk are the same in Unicode NFC; keep one"})
			continue
		}
		if n, err := pathx.Normalize(p); err != nil || n != p {
			s.Untracked[p] = true
			reason := "name the server does not accept"
			var pe *pathx.PathError
			if errors.As(err, &pe) {
				reason = "name the server does not accept: " + pe.Err.Error()
			}
			s.Problems = append(s.Problems, Problem{Path: p, Reason: reason + "; rename it"})
			continue
		}
		d := filepath.Join(diskRel, e.Name())
		switch t := e.Type(); {
		case t&fs.ModeSymlink != 0:
			s.Untracked[p] = true // symlinks are not synchronized
		case isDir:
			s.Local[p] = Entry{Kind: Dir}
			s.Disk[p] = d
			if err := s.walk(root, d, p, m, manifest); err != nil {
				return err
			}
		case t.IsRegular():
			fi, err := e.Info()
			if errors.Is(err, fs.ErrNotExist) {
				continue // removed while we looked
			}
			if err != nil {
				return err
			}
			ent := Entry{Kind: File, Size: fi.Size(), Mtime: fi.ModTime().UnixNano()}
			if me, ok := manifest[p]; ok && me.State == Synced && me.Kind == File && me.Size == ent.Size && me.Mtime == ent.Mtime {
				ent.Hash = me.Hash
			} else {
				h, err := hashx.File(filepath.Join(root, d))
				if errors.Is(err, fs.ErrNotExist) {
					continue
				}
				if err != nil {
					return err
				}
				ent.Hash = h
				s.Hashed++
			}
			s.Local[p] = ent
			s.Disk[p] = d
		default:
			s.Untracked[p] = true // devices, sockets, fifos
		}
	}
	return nil
}

// untracked reports whether p or one of its parents exists but is left
// alone (a symlinked directory is not entered, whatever the server has
// below its name).
func (s *scanResult) untracked(p string) bool {
	if s.Untracked[p] {
		return true
	}
	for i := strings.LastIndexByte(p, '/'); i > 0; i = strings.LastIndexByte(p[:i], '/') {
		if s.Untracked[p[:i]] {
			return true
		}
	}
	return false
}

// diskPath returns where path p is (or would be) on disk, relative to the
// root: below the deepest ancestor the scan found, the rest in NFC.
func (s *scanResult) diskPath(p string) string {
	if d, ok := s.Disk[p]; ok {
		return d
	}
	segs := strings.Split(p, "/")
	for i := len(segs) - 1; i > 0; i-- {
		if d, ok := s.Disk[strings.Join(segs[:i], "/")]; ok {
			return filepath.Join(append([]string{d}, segs[i:]...)...)
		}
	}
	return filepath.FromSlash(p)
}

// localEntry stats one path on disk the way scan would (hashing a file
// only when it differs from want in size or mtime).
func localEntry(abs string, want Entry) (Entry, error) {
	fi, err := os.Lstat(abs)
	if errors.Is(err, fs.ErrNotExist) {
		return Entry{}, nil
	}
	if err != nil {
		return Entry{}, err
	}
	switch {
	case fi.IsDir():
		return Entry{Kind: Dir}, nil
	case fi.Mode().IsRegular():
		e := Entry{Kind: File, Size: fi.Size(), Mtime: fi.ModTime().UnixNano()}
		if want.Kind == File && want.Size == e.Size && want.Mtime == e.Mtime && want.Hash != "" {
			e.Hash = want.Hash
			return e, nil
		}
		h, err := hashx.File(abs)
		if err != nil {
			return Entry{}, err
		}
		e.Hash = h
		return e, nil
	}
	return Entry{Kind: Kind(255)}, nil // something we do not touch
}

// unchanged reports whether the disk at abs still is what the plan saw:
// same kind, and for a file the same size and mtime.
func unchanged(abs string, want Entry) (bool, error) {
	fi, err := os.Lstat(abs)
	if errors.Is(err, fs.ErrNotExist) {
		return want.Kind == None, nil
	}
	if err != nil {
		return false, err
	}
	switch want.Kind {
	case File:
		return fi.Mode().IsRegular() && fi.Size() == want.Size && fi.ModTime().UnixNano() == want.Mtime, nil
	case Dir:
		return fi.IsDir(), nil
	}
	return false, nil
}
