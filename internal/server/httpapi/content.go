package httpapi

// The content of a file, for an admin of the folder (docs/admin.md,
// "Sadržaj fajla"):
//
//	GET /v1/shares/{id}/admin/files/content?path=<path>[&download=1]
//
// Served on the public API (token of an admin) and on the local admin
// socket. Only a file of the files table is served, and only when its
// content on disk has the hash the database records (else 409
// file_changed). A few harmless types (images, PDF, text, audio and
// video) are shown inline; everything else,
// and anything with download=1, is an attachment.

import (
	"net/http"
	"path"
	"strings"
	"time"

	"github.com/digital-cube/shared-folder/internal/server"
)

// ContentCSP is the Content-Security-Policy of every file served: a
// sandbox with no permissions, so even a file a browser renders cannot run
// script or reach the GUI's origin.
const ContentCSP = "sandbox"

// ContentCacheControl lets a browser keep the content but ask again each
// time; the ETag (the content hash) makes that a 304.
const ContentCacheControl = "private, no-cache"

// inlineTypes are the extensions served inline, with their Content-Type.
// Everything else is application/octet-stream as an attachment, among
// them html, htm, svg, xml and js, which a browser would run.
var inlineTypes = map[string]string{
	"jpg":  "image/jpeg",
	"jpeg": "image/jpeg",
	"png":  "image/png",
	"gif":  "image/gif",
	"webp": "image/webp",
	"pdf":  "application/pdf",
	"txt":  "text/plain; charset=utf-8",
	"md":   "text/plain; charset=utf-8",
	"csv":  "text/plain; charset=utf-8",
	"log":  "text/plain; charset=utf-8",
	"json": "application/json",
	// Audio and video play in the GUI's <audio>/<video>; a media element
	// runs no script, and Range (ServeContent) lets it seek.
	"mp4":  "video/mp4",
	"m4v":  "video/mp4",
	"webm": "video/webm",
	"ogv":  "video/ogg",
	"mov":  "video/quicktime",
	"mp3":  "audio/mpeg",
	"m4a":  "audio/mp4",
	"aac":  "audio/aac",
	"wav":  "audio/wav",
	"ogg":  "audio/ogg",
	"oga":  "audio/ogg",
	"opus": "audio/ogg",
	"flac": "audio/flac",
}

// InlineType returns the Content-Type a file of this name is shown inline
// with, and false when it is only ever downloaded.
func InlineType(name string) (string, bool) {
	ext := strings.ToLower(strings.TrimPrefix(path.Ext(name), "."))
	t, ok := inlineTypes[ext]
	return t, ok
}

// ContentDisposition builds the header for a file name (RFC 6266): a
// quoted ASCII fallback in filename (non-ASCII, control characters, the
// quote, the backslash and "%", which some browsers decode, become "_"),
// and the exact UTF-8 name in filename* (RFC 5987) when the two differ.
func ContentDisposition(disposition, name string) string {
	var b strings.Builder
	ascii := true
	for _, r := range name {
		switch {
		case r < 0x20 || r == 0x7f || r >= 0x80:
			b.WriteByte('_')
			ascii = false
		case r == '"' || r == '\\' || r == '%':
			b.WriteByte('_')
			ascii = false
		default:
			b.WriteRune(r)
		}
	}
	out := disposition + `; filename="` + b.String() + `"`
	if !ascii {
		out += "; filename*=UTF-8''" + rfc5987(name)
	}
	return out
}

// rfc5987 percent-encodes everything outside attr-char (RFC 5987, 3.2.1).
func rfc5987(s string) string {
	const hex = "0123456789ABCDEF"
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' ||
			strings.IndexByte("!#$&+-.^_`|~", c) >= 0 {
			b.WriteByte(c)
			continue
		}
		b.WriteByte('%')
		b.WriteByte(hex[c>>4])
		b.WriteByte(hex[c&15])
	}
	return b.String()
}

// rawHandler is an admin route that writes its own answer.
type rawHandler func(w http.ResponseWriter, r *http.Request, sh *server.Share, m *server.Member)

// adminRaw is auth plus the admin role, for a rawHandler. Like the
// thumbnails it does not refresh last_seen_at.
func (a *api) adminRaw(h rawHandler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sh, m, ok := a.authenticate(w, r)
		if !ok {
			return
		}
		if err := server.RequireAdmin(m); err != nil {
			a.fail(w, r, err)
			return
		}
		h(w, r, sh, m)
	})
}

// localRaw is the admin socket's version of adminRaw (see local).
func (a *api) localRaw(h rawHandler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sh, m, err := a.localMember(r)
		if err != nil {
			a.fail(w, r, err)
			return
		}
		h(w, r, sh, m)
	})
}

func (a *api) fileContent(w http.ResponseWriter, r *http.Request, sh *server.Share, _ *server.Member) {
	q := r.URL.Query()
	p := q.Get("path")
	if p == "" {
		writeErr(w, http.StatusBadRequest, server.CodeInvalidRequest, "path is required")
		return
	}
	f, e, err := sh.OpenContent(p)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	defer f.Close()
	h := w.Header()
	ctype, inline := InlineType(e.Name)
	if !inline || q.Get("download") == "1" {
		if !inline {
			ctype = "application/octet-stream"
		}
		h.Set("Content-Disposition", ContentDisposition("attachment", e.Name))
	} else {
		h.Set("Content-Disposition", ContentDisposition("inline", e.Name))
	}
	h.Set("Content-Type", ctype)
	h.Set("ETag", `"`+e.Hash+`"`)
	h.Set("Cache-Control", ContentCacheControl)
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Content-Security-Policy", ContentCSP)
	// Range, If-Range and If-None-Match (304) against the ETag.
	http.ServeContent(w, r, "", time.Time{}, f)
}
