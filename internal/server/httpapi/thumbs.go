package httpapi

// Thumbnails (docs/admin.md, "Thumbnails").
//
// On the local admin socket only (NewLocal), for shfd thumbs, which has no
// database of its own:
//
//	GET  /v1/thumbs/jobs?limit=50&kinds=image,pdf[&rescan=1]   server.ThumbJobs
//	POST /v1/thumbs/done                                       ThumbsDoneRequest
//	POST /v1/shares/{id}/admin/thumbs/unreferenced             HashesBody
//
// On the public API, for an admin of the folder:
//
//	GET  /v1/shares/{id}/admin/thumbs/{hash}?size=s|m|l        image/jpeg

import (
	"net/http"
	"strings"
	"time"

	"github.com/digital-cube/shared-folder/internal/server"
	"github.com/digital-cube/shared-folder/internal/thumbs"
)

// Page size of GET /v1/thumbs/jobs.
const (
	DefaultThumbJobs = 50
	MaxThumbJobs     = 500
)

// ThumbsDoneRequest is the body of POST /v1/thumbs/done.
type ThumbsDoneRequest struct {
	ShareID string               `json:"share_id"`
	Results []server.ThumbResult `json:"results"`
}

// HashesBody is the body of POST /admin/thumbs/unreferenced and of its
// answer: the hashes asked about, and those of them nothing holds.
type HashesBody struct {
	Hashes []string `json:"hashes"`
}

// ThumbCacheControl lets a browser keep a thumbnail for good: its URL
// names the content hash, so the image behind it never changes.
const ThumbCacheControl = "private, max-age=31536000, immutable"

func registerThumbsLocal(mux *http.ServeMux, a *api) {
	mux.HandleFunc("GET /v1/thumbs/jobs", a.thumbJobs)
	mux.HandleFunc("POST /v1/thumbs/done", a.thumbsDone)
	mux.HandleFunc("POST /v1/shares/{id}/admin/thumbs/unreferenced", a.thumbsUnreferenced)
}

func registerThumbs(mux *http.ServeMux, a *api) {
	mux.HandleFunc("GET /v1/shares/{id}/admin/thumbs/{hash}", a.thumb)
}

func (a *api) thumbJobs(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	limit, err := intParam(q.Get("limit"), DefaultThumbJobs)
	if err != nil || limit < 1 {
		writeErr(w, http.StatusBadRequest, server.CodeInvalidRequest, "limit must be a positive integer")
		return
	}
	kinds := thumbs.AllKinds
	if q.Has("kinds") {
		kinds = nil
		for _, s := range strings.Split(q.Get("kinds"), ",") {
			if s = strings.TrimSpace(s); s == "" {
				continue
			}
			k, ok := thumbs.ParseKind(s)
			if !ok {
				writeErr(w, http.StatusBadRequest, server.CodeInvalidRequest, "unknown kind "+s)
				return
			}
			kinds = append(kinds, k)
		}
	}
	res, err := a.srv.ThumbJobs(kinds, int(min(limit, MaxThumbJobs)), q.Get("rescan") == "1")
	if err != nil {
		a.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, res)
}

func (a *api) thumbsDone(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxBody)
	var req ThumbsDoneRequest
	if err := decode(r, &req); err != nil {
		a.fail(w, r, err)
		return
	}
	sh, err := a.srv.Share(req.ShareID)
	if err == nil {
		err = sh.ThumbsDone(req.Results)
	}
	if err != nil {
		a.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (a *api) thumbsUnreferenced(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxBody)
	sh, err := a.srv.Share(r.PathValue("id"))
	if err != nil {
		a.fail(w, r, err)
		return
	}
	var req HashesBody
	if err := decode(r, &req); err != nil {
		a.fail(w, r, err)
		return
	}
	out, err := sh.UnreferencedThumbs(req.Hashes)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, HashesBody{Hashes: out})
}

// thumb sends one thumbnail of the folder. The hash and the size are
// checked before anything else, the token and admin role next; the file
// is read only from this folder's thumbnails/. It does not refresh the
// member's last_seen_at: a directory of images asks for hundreds.
func (a *api) thumb(w http.ResponseWriter, r *http.Request) { a.serveThumb(w, r, true) }

// memberThumb is thumb for any active member of the folder.
func (a *api) memberThumb(w http.ResponseWriter, r *http.Request) { a.serveThumb(w, r, false) }

func (a *api) serveThumb(w http.ResponseWriter, r *http.Request, admin bool) {
	hash := r.PathValue("hash")
	size := r.URL.Query().Get("size")
	if size == "" {
		size = thumbs.DefaultSize
	}
	if !thumbs.ValidHash(hash) {
		writeErr(w, http.StatusBadRequest, server.CodeInvalidRequest, "a thumbnail hash is 64 lowercase hex digits")
		return
	}
	if !thumbs.ValidSize(size) {
		writeErr(w, http.StatusBadRequest, server.CodeInvalidRequest, "size must be s, m or l")
		return
	}
	sh, m, ok := a.authenticate(w, r)
	if !ok {
		return
	}
	if admin {
		if err := server.RequireAdmin(m); err != nil {
			a.fail(w, r, err)
			return
		}
	}
	f, _, err := sh.OpenThumb(hash, size)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	defer f.Close()
	h := w.Header()
	h.Set("Content-Type", "image/jpeg")
	h.Set("Cache-Control", ThumbCacheControl)
	h.Set("ETag", `"`+hash+"."+size+`"`)
	h.Set("X-Content-Type-Options", "nosniff")
	http.ServeContent(w, r, "", time.Time{}, f) // answers If-None-Match with 304
}
