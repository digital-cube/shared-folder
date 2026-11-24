package httpapi

import (
	"errors"
	"io"
	"mime"
	"net/http"
	"strconv"
	"time"

	"github.com/digital-cube/shared-folder/internal/server"
)

// Profile pictures of accounts (server/avatars.go, docs/admin.md,
// "Profilna slika"):
//
//	PUT    /v1/me/avatar   the picture as the raw body (any Content-Type) or
//	                       as the first file of a multipart/form-data body;
//	                       at most 5 MB -> AvatarResponse
//	DELETE /v1/me/avatar   -> {"status": "ok", "removed": bool, "avatar_url": null}
//	GET    /v1/users/{username}/avatar?v=<version>
//
// The GET takes the device token of any account or the token of an active
// member of any folder; without one it is 401. It is image/jpeg; with the
// current version in v it may be kept for good (Cache-Control: private,
// max-age=31536000, immutable), with another or none it is revalidated
// (private, no-cache, and an ETag). An unknown account and one without a
// picture are both 404.

// maxAvatarBody bounds the body of PUT /v1/me/avatar: the picture and
// what a multipart body wraps it in.
const maxAvatarBody = server.MaxAvatarBytes + 64<<10

// AvatarCacheForever is the Cache-Control of a picture asked for by its
// current version.
const AvatarCacheForever = "private, max-age=31536000, immutable"

// AvatarResponse is the body of PUT /v1/me/avatar.
type AvatarResponse struct {
	AvatarURL string       `json:"avatar_url"`
	User      *server.User `json:"user"`
}

var errImageTooLarge = &server.Error{Code: server.CodeImageTooLarge, Message: "the picture is larger than 5 MB"}

func (a *api) putAvatar(w http.ResponseWriter, r *http.Request, sess *server.Session) (any, error) {
	data, err := readAvatar(r)
	var mbe *http.MaxBytesError
	if errors.As(err, &mbe) {
		return nil, errImageTooLarge
	}
	if err != nil {
		return nil, err
	}
	u, err := a.srv.SetAvatar(sess.User.ID, data)
	if err != nil {
		return nil, err
	}
	return AvatarResponse{AvatarURL: *u.AvatarURL, User: u}, nil
}

// readAvatar reads the picture of a PUT: the first file part of a
// multipart/form-data body, or else the whole body. It reads one byte
// over server.MaxAvatarBytes at most, so SetAvatar can tell a picture
// that is too large.
func readAvatar(r *http.Request) ([]byte, error) {
	mt, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if mt != "multipart/form-data" {
		return io.ReadAll(io.LimitReader(r.Body, server.MaxAvatarBytes+1))
	}
	mr, err := r.MultipartReader()
	if err != nil {
		return nil, &server.Error{Code: server.CodeInvalidRequest, Message: "invalid multipart body: " + err.Error()}
	}
	for {
		p, err := mr.NextPart()
		if err == io.EOF {
			return nil, &server.Error{Code: server.CodeUnsupportedImage, Message: "no file in the multipart body"}
		}
		if err != nil {
			var mbe *http.MaxBytesError
			if errors.As(err, &mbe) {
				return nil, err
			}
			return nil, &server.Error{Code: server.CodeInvalidRequest, Message: "invalid multipart body: " + err.Error()}
		}
		if p.FileName() == "" && p.FormName() != "avatar" {
			p.Close()
			continue
		}
		defer p.Close()
		return io.ReadAll(io.LimitReader(p, server.MaxAvatarBytes+1))
	}
}

func (a *api) deleteAvatar(w http.ResponseWriter, r *http.Request, sess *server.Session) (any, error) {
	removed, err := a.srv.RemoveAvatar(sess.User.ID)
	if err != nil {
		return nil, err
	}
	return map[string]any{"status": "ok", "removed": removed, "avatar_url": nil}, nil
}

func (a *api) getAvatar(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if err := a.srv.AuthenticateAny(bearerToken(r)); err != nil {
		a.fail(w, r, err)
		return
	}
	f, v, err := a.srv.Avatar(r.PathValue("username"))
	if err != nil {
		a.fail(w, r, err)
		return
	}
	defer f.Close()
	ver := strconv.FormatInt(v, 10)
	h := w.Header()
	if r.URL.Query().Get("v") == ver {
		h.Set("Cache-Control", AvatarCacheForever)
	} else {
		h.Set("Cache-Control", "private, no-cache")
	}
	h.Set("ETag", `"avatar-`+ver+`"`)
	h.Set("Content-Type", "image/jpeg")
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Content-Security-Policy", "default-src 'none'; sandbox")
	// ServeContent answers If-None-Match (304) and ranges from the ETag.
	http.ServeContent(w, r, "", time.Time{}, f)
}
