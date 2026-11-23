// Package transport moves file content between a client and the server.
// The control API (uploads, commit) decides what moves; a Transport only
// carries the bytes (docs/specifikacija.md, section 5).
//
// This package holds the interface and Local, which reaches the server's
// data-root directly on the same machine and serves the tests. rsync over
// SSH comes as a second implementation.
package transport

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/digital-cube/shared-folder/internal/hashx"
	"github.com/digital-cube/shared-folder/internal/pathx"
)

// The server's share layout as far as a transport sees it. Kept here, not
// imported from internal/server, so a client binary does not link the server.
const (
	storageDir = "storage"
	tmpDir     = "tmp"
)

var (
	shareIDRE  = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)
	uploadIDRE = regexp.MustCompile(`^[0-9a-f]{32}$`)
)

// Upload names where the content of an announced upload goes, as returned
// by POST /v1/shares/{id}/uploads.
type Upload struct {
	ShareID  string
	UploadID string
	Dest     string // relative to the share directory: tmp/<upload_id>
}

// Transport carries file content.
type Transport interface {
	// Send delivers the local file src as the content of upload u.
	Send(ctx context.Context, u Upload, src string) error
	// Fetch downloads the file at path p of the share, which must have
	// content hash, into dst (replaced atomically). It fails with
	// ErrChanged when the server's file no longer has that hash.
	Fetch(ctx context.Context, shareID, p, hash, dst string) error
}

// ErrChanged means the file was replaced on the server between the
// listing and the download; fetch the newer version.
var ErrChanged = errors.New("file changed on the server")

// Local is a Transport over the server's data-root on the local disk.
type Local struct {
	DataRoot string
}

var _ Transport = Local{}

func (l Local) shareDir(id string) (string, error) {
	if !shareIDRE.MatchString(id) {
		return "", fmt.Errorf("invalid share id %q", id)
	}
	return filepath.Join(l.DataRoot, id), nil
}

// Send copies src to tmp/<upload_id>. Like rsync-gate it writes nowhere else.
func (l Local) Send(ctx context.Context, u Upload, src string) error {
	dir, err := l.shareDir(u.ShareID)
	if err != nil {
		return err
	}
	if !uploadIDRE.MatchString(u.UploadID) || u.Dest != tmpDir+"/"+u.UploadID {
		return fmt.Errorf("invalid upload target %q / %q", u.UploadID, u.Dest)
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(filepath.Join(dir, filepath.FromSlash(u.Dest)), os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, ctxReader{ctx, in}); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

// Fetch copies storage/<p> to dst through a temporary file next to dst and
// checks the hash before the rename.
func (l Local) Fetch(ctx context.Context, shareID, p, hash, dst string) error {
	dir, err := l.shareDir(shareID)
	if err != nil {
		return err
	}
	norm, err := pathx.Normalize(p)
	if err != nil || norm != p {
		return fmt.Errorf("invalid path %q", p)
	}
	in, err := os.Open(filepath.Join(dir, storageDir, filepath.FromSlash(p)))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("%s: %w", p, ErrChanged)
		}
		return err
	}
	defer in.Close()
	tmp, err := os.CreateTemp(filepath.Dir(dst), ".shf-fetch-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	got, err := hashx.Reader(io.TeeReader(ctxReader{ctx, in}, tmp))
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return err
	}
	if !strings.EqualFold(got, hash) {
		return fmt.Errorf("%s: %w", p, ErrChanged)
	}
	return os.Rename(tmp.Name(), dst)
}

type ctxReader struct {
	ctx context.Context
	r   io.Reader
}

func (c ctxReader) Read(p []byte) (int, error) {
	if err := c.ctx.Err(); err != nil {
		return 0, err
	}
	return c.r.Read(p)
}
