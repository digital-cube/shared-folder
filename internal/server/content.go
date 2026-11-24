package server

import (
	"database/sql"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/digital-cube/shared-folder/internal/hashx"
	"github.com/digital-cube/shared-folder/internal/pathx"
)

// maxVerified bounds the memory of verifiedFiles; past it the set starts
// over, which costs only a hash of the next file asked for again.
const maxVerified = 4096

// verifiedFiles remembers, per path, the identity of a file on disk whose
// content was hashed and matched the database, so that the Range requests
// of one download (a video, a PDF viewer) do not hash it again each time.
// A file in storage/ is replaced by a rename, never rewritten in place, so
// a new version has another inode or another mtime.
type verifiedFiles struct {
	mu sync.Mutex
	m  map[string]fileIdentity
}

type fileIdentity struct {
	hash string
	fi   os.FileInfo
}

func (v *verifiedFiles) has(path, hash string, fi os.FileInfo) bool {
	v.mu.Lock()
	defer v.mu.Unlock()
	got, ok := v.m[path]
	return ok && got.hash == hash && os.SameFile(got.fi, fi) &&
		got.fi.Size() == fi.Size() && got.fi.ModTime().Equal(fi.ModTime())
}

func (v *verifiedFiles) add(path, hash string, fi os.FileInfo) {
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.m == nil || len(v.m) >= maxVerified {
		v.m = make(map[string]fileIdentity)
	}
	v.m[path] = fileIdentity{hash: hash, fi: fi}
}

// OpenContent opens a file of the folder for reading its content, for
// GET /admin/files/content. The path is checked by pathx.Normalize and
// must be a file of the files table; nothing on disk the database does not
// record is served. The file is opened inside storage/ through an os.Root,
// so a symlink that leads out of storage/ fails.
//
// The opened file is hashed (unless this very file, by inode, size and
// mtime, was hashed before) and must have the hash of the database; a
// missing file or other content is CodeFileChanged and nothing is served.
// The returned file is positioned at its start.
func (s *Share) OpenContent(p string) (*os.File, *FileEntry, error) {
	n, err := pathx.Normalize(p)
	if err != nil {
		return nil, nil, errf(CodeInvalidPath, "%v", err)
	}
	e := FileEntry{Path: n, Name: n[strings.LastIndex(n, "/")+1:]}
	var mtime int64
	err = s.db.QueryRow(`SELECT kind, size, mtime, hash, seq, updated_by FROM files WHERE path = ?`, n).
		Scan(&e.Kind, &e.Size, &mtime, &e.Hash, &e.Seq, &e.UpdatedBy)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil, errf(CodeNotFound, "no file %q", n)
	}
	if err != nil {
		return nil, nil, err
	}
	if e.Kind != "file" {
		return nil, nil, errf(CodeKindMismatch, "%q is a directory, not a file", n)
	}
	if mtime != 0 {
		t := time.Unix(0, mtime).UTC()
		e.Mtime = &t
	}

	root, err := os.OpenRoot(filepath.Join(s.dir, StorageDir))
	if err != nil {
		return nil, nil, err
	}
	defer root.Close()
	f, err := root.Open(filepath.FromSlash(n))
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil, errf(CodeFileChanged, "%q is in the database but not on disk; run verify", n)
		}
		// Among others: a symlink that leads out of storage/.
		return nil, nil, errf(CodeFileChanged, "%q cannot be opened: %v", n, err)
	}
	fi, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, nil, err
	}
	if !fi.Mode().IsRegular() || fi.Size() != e.Size {
		f.Close()
		return nil, nil, errf(CodeFileChanged, "%q on disk is not the version the database records; run verify", n)
	}
	if !s.verified.has(n, e.Hash, fi) {
		sum, err := hashx.Reader(f)
		if err != nil {
			f.Close()
			return nil, nil, err
		}
		if sum != e.Hash {
			f.Close()
			return nil, nil, errf(CodeFileChanged, "%q on disk does not have the hash the database records; run verify", n)
		}
		if _, err := f.Seek(0, io.SeekStart); err != nil {
			f.Close()
			return nil, nil, err
		}
		s.verified.add(n, e.Hash, fi)
	}
	return f, &e, nil
}
