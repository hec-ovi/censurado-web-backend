package publish

import "net/http"

// NewServerHandler assembles the publish HTTP surface: an unauthenticated liveness
// probe, the authenticated POST /articles route, and POST /articles:batch, all
// behind the rate limiter.
//
// Routing uses the Go 1.22 ServeMux. GET /healthz is matched by method so it never
// touches auth or the limiter. /articles is registered for ALL methods (no method
// in the pattern) so the Handler keeps doing its own dispatch: it answers 405 for
// non-POST, 401/403 for auth failures, and so on. POST /articles:batch is a
// distinct static route (the ':' is a literal path byte to ServeMux, so it never
// collides with /articles), bound to the batch handler. Both run through one
// limiter.Wrap, so a batch is charged exactly one token no matter how many articles
// it carries. A nil limiter disables limiting.
//
// When mediaH is non-nil, the self-hosted image CDN is mounted too: POST /media
// (authenticated upload, rate-limited like a write) and GET /media/{name} (public,
// immutable read, not rate-limited since it is cacheable and keyless). A nil mediaH
// leaves media off entirely.
//
// When readH is non-nil, the authenticated JSON read API is mounted: GET /authors,
// GET /topics, GET /articles, and GET /articles/{slug}. These are method-specific
// patterns, so GET /articles takes precedence over the method-less /articles write
// route for GET while POST still reaches the write handler; reads are not rate
// limited (idempotent, cacheable). A nil readH leaves the read API off, so the
// write handler keeps answering 405 for a GET /articles.
//
// When opH is non-nil, the operator mutation lane is mounted behind ScopeAdminWrite:
// POST/DELETE/restore for /authors and /topics, and PUT /articles/{slug},
// DELETE /articles/{slug}, POST /articles/{slug}/restore. These are method-specific
// patterns that coexist with the method-less /articles write route (PUT/DELETE reach
// the operator handler, POST still reaches the append-only write handler) and with
// the read API's GET /articles/{slug}. The operator lane is not rate limited (it is
// a trusted, low-frequency console). A nil opH leaves the mutation lane off.
func NewServerHandler(h *Handler, limiter *RateLimiter, mediaH *MediaHandler, readH *ReadHandler, opH *OperatorHandler) http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})

	mux.Handle("/articles", limiter.Wrap(h))
	mux.Handle("POST /articles:batch", limiter.Wrap(http.HandlerFunc(h.ServeBatch)))

	if mediaH != nil {
		mux.Handle("POST /media", limiter.Wrap(http.HandlerFunc(mediaH.ServeUpload)))
		mux.HandleFunc("GET /media/{name}", mediaH.ServeFile)
	}

	if readH != nil {
		mux.HandleFunc("GET /authors", readH.ServeAuthors)
		mux.HandleFunc("GET /topics", readH.ServeTopics)
		mux.HandleFunc("GET /articles", readH.ServeArticles)
		mux.HandleFunc("GET /articles/{slug}", readH.ServeArticle)
	}

	if opH != nil {
		mux.HandleFunc("POST /authors", opH.ServeUpsertAuthor)
		mux.HandleFunc("DELETE /authors/{handle}", opH.ServeDeleteAuthor)
		mux.HandleFunc("POST /authors/{handle}/restore", opH.ServeRestoreAuthor)
		mux.HandleFunc("POST /topics", opH.ServeUpsertTopic)
		mux.HandleFunc("DELETE /topics/{slug}", opH.ServeDeleteTopic)
		mux.HandleFunc("POST /topics/{slug}/restore", opH.ServeRestoreTopic)
		mux.HandleFunc("PUT /articles/{slug}", opH.ServeUpdateArticle)
		mux.HandleFunc("DELETE /articles/{slug}", opH.ServeDeleteArticle)
		mux.HandleFunc("POST /articles/{slug}/restore", opH.ServeRestoreArticle)
	}

	return mux
}
