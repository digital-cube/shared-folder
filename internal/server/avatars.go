package server

import (
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strconv"

	"github.com/digital-cube/shared-folder/internal/thumbs"
)

// Profile pictures of accounts (docs/admin.md, "Profilna slika"). The
// picture of an account is one JPEG of thumbs.AvatarPx × thumbs.AvatarPx,
// <db-root>/avatars/<users.id>.jpg, on the db-root's local disk next to
// registry.db (not on the data-root, which may be NFS). users.avatar_version
// says there is one and which: the URL carries it, so a browser keeps a
// picture for good and a new one has a new URL.

// AvatarsDir is the directory of the pictures in the db-root.
const AvatarsDir = "avatars"

// MaxAvatarBytes bounds an uploaded picture, before it is processed.
const MaxAvatarBytes = 5 << 20

// AvatarURL is the path of an account's picture of version v on shfd.
func AvatarURL(username string, v int64) string {
	return "/v1/users/" + url.PathEscape(username) + "/avatar?v=" + strconv.FormatInt(v, 10)
}

func (s *Server) avatarPath(userID int64) string {
	return filepath.Join(s.dbRoot, AvatarsDir, strconv.FormatInt(userID, 10)+".jpg")
}

// SetAvatar makes data, an uploaded PNG, JPEG, WebP or GIF, the picture of
// the account: checked and made square by thumbs.Avatar, written
// atomically, then recorded with a new version. A picture that is too
// large, not one of those formats (whatever its name says) or over
// thumbs.AvatarMaxPixels is refused and changes nothing.
func (s *Server) SetAvatar(userID int64, data []byte) (*User, error) {
	if len(data) > MaxAvatarBytes {
		return nil, errf(CodeImageTooLarge, "the picture has %d bytes, more than the limit of %d (5 MB)", len(data), MaxAvatarBytes)
	}
	if len(data) == 0 {
		return nil, errf(CodeUnsupportedImage, "no picture in the request")
	}
	jpg, err := thumbs.Avatar(data, thumbs.AvatarMaxPixels)
	switch {
	case errors.Is(err, thumbs.ErrAvatarPixels):
		return nil, errf(CodeImageTooManyPixels, "%v", err)
	case errors.Is(err, thumbs.ErrAvatarFormat):
		return nil, errf(CodeUnsupportedImage, "the picture must be a PNG, JPEG, WebP or GIF image")
	case err != nil:
		return nil, err
	}
	s.avatarMu.Lock()
	defer s.avatarMu.Unlock()
	u, err := s.UserByID(userID)
	if err != nil {
		return nil, err
	}
	dir := filepath.Join(s.dbRoot, AvatarsDir)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	if err := writeFileAtomic(s.avatarPath(userID), jpg); err != nil {
		return nil, err
	}
	// The time in milliseconds, so a picture uploaded after a removal does
	// not get the URL of an older one a browser may still keep.
	now := s.opts.Now()
	v := now.UnixMilli()
	if u.AvatarVersion != nil && *u.AvatarVersion >= v {
		v = *u.AvatarVersion + 1
	}
	if _, err := s.reg.Exec(`UPDATE users SET avatar_version = ?, avatar_updated_at = ? WHERE id = ?`, v, fmtTime(now), userID); err != nil {
		return nil, err
	}
	return s.UserByID(userID)
}

// RemoveAvatar removes the picture of the account; false when it had none.
func (s *Server) RemoveAvatar(userID int64) (bool, error) {
	s.avatarMu.Lock()
	defer s.avatarMu.Unlock()
	res, err := s.reg.Exec(`UPDATE users SET avatar_version = NULL, avatar_updated_at = ? WHERE id = ? AND avatar_version IS NOT NULL`, fmtTime(s.opts.Now()), userID)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	if err := os.Remove(s.avatarPath(userID)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return n > 0, err
	}
	return n > 0, nil
}

// RemoveUserAvatar removes the picture of the account username, for an
// admin (shfd user avatar rm); false when it had none.
func (s *Server) RemoveUserAvatar(username string) (bool, error) {
	u, err := s.UserByName(username)
	if err != nil {
		return false, err
	}
	return s.RemoveAvatar(u.ID)
}

// Avatar opens the picture of the account username and gives its
// version. An unknown account and one without a picture are the same
// CodeNotFound, so the answer does not say which accounts exist.
func (s *Server) Avatar(username string) (io.ReadSeekCloser, int64, error) {
	noPicture := errf(CodeNotFound, "no picture")
	u, err := s.UserByName(username)
	if CodeOf(err) == CodeUnknownUser {
		return nil, 0, noPicture
	}
	if err != nil {
		return nil, 0, err
	}
	if u.AvatarVersion == nil {
		return nil, 0, noPicture
	}
	f, err := os.Open(s.avatarPath(u.ID))
	if errors.Is(err, os.ErrNotExist) {
		return nil, 0, noPicture
	}
	if err != nil {
		return nil, 0, err
	}
	return f, *u.AvatarVersion, nil
}

// AuthenticateAny accepts a device token of an account or the token of an
// active member of any folder: what may see the pictures of accounts.
// A device token of an account with a temporary password is accepted too.
func (s *Server) AuthenticateAny(token string) error {
	if token == "" {
		return errf(CodeUnauthorized, "missing bearer token")
	}
	if IsDeviceToken(token) {
		_, err := s.AuthenticateDevice(token)
		return err
	}
	_, _, _, err := s.Whoami(token)
	return err
}

// AvatarURLsByID maps the id of every account that has a picture to its
// URL, for listings.
func (s *Server) AvatarURLsByID() (map[int64]string, error) {
	rows, err := s.reg.Query(`SELECT id, username, avatar_version FROM users WHERE avatar_version IS NOT NULL`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int64]string{}
	for rows.Next() {
		var id, v int64
		var n string
		if err := rows.Scan(&id, &n, &v); err != nil {
			return nil, err
		}
		out[id] = AvatarURL(n, v)
	}
	return out, rows.Err()
}

// writeFileAtomic writes data to a temporary file next to path and renames
// it over path, so a reader sees the old picture or the new one whole.
func writeFileAtomic(path string, data []byte) error {
	f, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	_, err = f.Write(data)
	if err == nil {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Chmod(tmp, 0o600)
	}
	if err == nil {
		err = os.Rename(tmp, path)
	}
	if err != nil {
		os.Remove(tmp)
		return fmt.Errorf("writing the picture: %w", err)
	}
	return nil
}
