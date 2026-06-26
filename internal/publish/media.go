package publish

import (
	"errors"
	"net/http"
	"time"

	"github.com/hec-ovi/censurado-web-backend/media"
)

// maxMediaUpload is a coarse transport guard on a media upload so an abusive body is
// cut off before it is buffered. The store enforces its own (smaller) per-image
// limit; this sits above it.
const maxMediaUpload = 16 << 20 // 16 MiB

// MediaHandler serves the self-hosted image CDN: an authenticated upload
// (POST /media) and a public, immutable read (GET /media/{name}). It shares the
// publish service's authenticator, so uploading an image needs the same
// articles:write scope as publishing an article. The bytes live on disk in the
// store; an article references one only by the returned URL placed in its metadata.
type MediaHandler struct {
	store *media.Store
	auth  Authenticator
}

// NewMediaHandler wires the media routes over a store and the shared authenticator.
func NewMediaHandler(store *media.Store, auth Authenticator) *MediaHandler {
	return &MediaHandler{store: store, auth: auth}
}

// ServeUpload handles POST /media. The image bytes ARE the request body (the declared
// Content-Type is advisory; the store sniffs the real type from the bytes). It is
// content-addressed, so unlike POST /articles it needs no Idempotency-Key: the same
// bytes always map to the same URL. On success it returns 201 with the stored
// descriptor (name, url, content_type, size, sha256).
func (m *MediaHandler) ServeUpload(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeProblem(w, problem{Status: http.StatusMethodNotAllowed, Code: "method_not_allowed"})
		return
	}
	if _, ok := authenticateWrite(m.auth, w, r); !ok {
		return
	}
	stored, err := m.store.Put(http.MaxBytesReader(w, r.Body, maxMediaUpload))
	switch {
	case err == nil:
		writeJSON(w, http.StatusCreated, stored)
	case errors.Is(err, media.ErrUnsupportedType):
		writeProblem(w, problem{Status: http.StatusUnsupportedMediaType, Code: "unsupported_media_type", Detail: err.Error()})
	case errors.Is(err, media.ErrTooLarge):
		writeProblem(w, problem{Status: http.StatusRequestEntityTooLarge, Code: "image_too_large"})
	case errors.Is(err, media.ErrEmpty):
		writeProblem(w, problem{Status: http.StatusBadRequest, Code: "empty_image"})
	case errors.As(err, new(*http.MaxBytesError)):
		// Defensive: the store's own cap normally trips first (it is below the transport
		// cap), but if it is ever configured at/above maxMediaUpload, a body that hits the
		// transport cap is still a too-large image, not a 500.
		writeProblem(w, problem{Status: http.StatusRequestEntityTooLarge, Code: "image_too_large"})
	default:
		writeProblem(w, problem{Status: http.StatusInternalServerError, Code: "store_error"})
	}
}

// ServeFile handles GET /media/{name}: a public read of one stored image. The name is
// content-addressed (immutable), so it is served with a long, immutable cache
// directive. In production an external web server usually serves /media/ straight
// from the media volume; this handler makes the store self-contained for dev and as
// a fallback. A name that is not a valid content-addressed file is a 404.
func (m *MediaHandler) ServeFile(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	f, ctype, err := m.store.Open(name)
	if err != nil {
		writeProblem(w, problem{Status: http.StatusNotFound, Code: "not_found"})
		return
	}
	defer f.Close()
	w.Header().Set("Content-Type", ctype)
	w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	http.ServeContent(w, r, name, time.Time{}, f)
}
