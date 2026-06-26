package publish_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/hec-ovi/censurado-web-backend/media"
	"github.com/hec-ovi/censurado-web-backend/internal/publish"
)

var pngBytes = []byte("\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR")

// mediaServer builds the publish HTTP surface with the media CDN mounted over a
// fresh store. The store cap is small so the oversize path is cheap to exercise. The
// article handler has nil deps because these tests only hit /media.
func mediaServer(t *testing.T, maxBytes int64) (http.Handler, *media.Store) {
	t.Helper()
	store, err := media.NewStore(t.TempDir(), maxBytes)
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	auth := publish.NewStaticKeyAuth()
	auth.Add("ak_ada", publish.HashSecret(adaSecret), "ada", publish.ScopeWrite)
	auth.Add("ak_ro", publish.HashSecret(roSecret), "ro", "articles:read")
	mediaH := publish.NewMediaHandler(store, auth)
	h := publish.NewHandler(nil, nil, auth, nil)
	return publish.NewServerHandler(h, nil, mediaH, nil, nil), store
}

func postMedia(t *testing.T, h http.Handler, token string, body []byte, contentType string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/media", bytes.NewReader(body))
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestMedia_UploadRequiresWriteScope(t *testing.T) {
	srv, _ := mediaServer(t, media.DefaultMaxBytes)
	if rec := postMedia(t, srv, "", pngBytes, "image/png"); rec.Code != http.StatusUnauthorized {
		t.Errorf("no token: status = %d, want 401", rec.Code)
	}
	// A read-only key authenticates but lacks articles:write.
	if rec := postMedia(t, srv, "ak_ro."+roSecret, pngBytes, "image/png"); rec.Code != http.StatusForbidden {
		t.Errorf("read-only key: status = %d, want 403", rec.Code)
	}
}

func TestMedia_UploadThenServeRoundTrip(t *testing.T) {
	srv, _ := mediaServer(t, media.DefaultMaxBytes)
	token := "ak_ada." + adaSecret

	rec := postMedia(t, srv, token, pngBytes, "image/png")
	if rec.Code != http.StatusCreated {
		t.Fatalf("upload status = %d, want 201 (%s)", rec.Code, rec.Body.String())
	}
	var stored media.Stored
	if err := json.Unmarshal(rec.Body.Bytes(), &stored); err != nil {
		t.Fatalf("decode upload response: %v (%s)", err, rec.Body.String())
	}
	if !strings.HasPrefix(stored.URL, "/media/") || stored.ContentType != "image/png" {
		t.Fatalf("stored = %+v, want a /media png url", stored)
	}

	// The returned URL serves the exact bytes, immutably cached, sniff-protected.
	req := httptest.NewRequest(http.MethodGet, stored.URL, nil)
	get := httptest.NewRecorder()
	srv.ServeHTTP(get, req)
	if get.Code != http.StatusOK {
		t.Fatalf("serve status = %d, want 200", get.Code)
	}
	if ct := get.Header().Get("Content-Type"); ct != "image/png" {
		t.Errorf("serve content-type = %q, want image/png", ct)
	}
	if cc := get.Header().Get("Cache-Control"); !strings.Contains(cc, "immutable") {
		t.Errorf("serve cache-control = %q, want immutable", cc)
	}
	if x := get.Header().Get("X-Content-Type-Options"); x != "nosniff" {
		t.Errorf("serve X-Content-Type-Options = %q, want nosniff", x)
	}
	if !bytes.Equal(get.Body.Bytes(), pngBytes) {
		t.Errorf("served bytes differ from uploaded")
	}

	// Re-uploading the same bytes is an idempotent dedup: a clean 201 with the same URL.
	rec2 := postMedia(t, srv, token, pngBytes, "image/png")
	if rec2.Code != http.StatusCreated {
		t.Fatalf("dedup upload status = %d, want 201 (%s)", rec2.Code, rec2.Body.String())
	}
	var stored2 media.Stored
	if err := json.Unmarshal(rec2.Body.Bytes(), &stored2); err != nil {
		t.Fatalf("decode dedup response: %v (%s)", err, rec2.Body.String())
	}
	if stored2.URL != stored.URL {
		t.Errorf("dedup: second upload url = %q, want %q", stored2.URL, stored.URL)
	}
}

func TestMedia_RejectsNonImageAndOversize(t *testing.T) {
	srv, _ := mediaServer(t, 1<<20) // 1 MiB cap
	token := "ak_ada." + adaSecret

	if rec := postMedia(t, srv, token, []byte("plain text, not an image at all"), "image/png"); rec.Code != http.StatusUnsupportedMediaType {
		t.Errorf("non-image (despite image/png header): status = %d, want 415", rec.Code)
	}
	oversize := make([]byte, (1<<20)+100)
	copy(oversize, pngBytes)
	if rec := postMedia(t, srv, token, oversize, "image/png"); rec.Code != http.StatusRequestEntityTooLarge {
		t.Errorf("oversize: status = %d, want 413", rec.Code)
	}
}

func TestMedia_ServeRejectsBadNames(t *testing.T) {
	srv, _ := mediaServer(t, media.DefaultMaxBytes)
	for _, path := range []string{"/media/not-a-hash.png", "/media/deadbeef.png"} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, req)
		if rec.Code != http.StatusNotFound {
			t.Errorf("GET %s: status = %d, want 404", path, rec.Code)
		}
	}
}
