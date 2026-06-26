package publish_test

import (
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/hec-ovi/censurado-web-backend/internal/publish"
)

// frozenServer wires the full server handler (healthz + limited /articles) over a
// real sqlite store and the provisioned keys, with a frozen clock for the limiter.
// It reuses newHandler from publish_test.go (same package) for the store + auth.
func frozenServer(t *testing.T, rate float64, burst int) http.Handler {
	t.Helper()
	h, _ := newHandler(t)
	clock := time.Date(2026, 6, 22, 12, 0, 0, 0, time.UTC)
	limiter := publish.NewRateLimiter(rate, burst, func() time.Time { return clock })
	return publish.NewServerHandler(h, limiter, nil, nil, nil)
}

func get(t *testing.T, h http.Handler, path string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestServer_HealthzAndArticles(t *testing.T) {
	srv := frozenServer(t, 5, 10)
	adaToken := "ak_ada." + adaSecret

	t.Run("healthz -> 200 ok, no auth", func(t *testing.T) {
		rec := get(t, srv, "/healthz")
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200", rec.Code)
		}
		if rec.Body.String() != "ok" {
			t.Errorf("body = %q, want \"ok\"", rec.Body.String())
		}
	})

	t.Run("POST /articles create -> 201, replay -> 200", func(t *testing.T) {
		rec := post(t, srv, adaToken, "srv-key-1", validBody)
		if rec.Code != http.StatusCreated {
			t.Fatalf("create status = %d, want 201 (%s)", rec.Code, rec.Body.String())
		}
		id, slug := decodeCreate(t, rec)
		if id == "" || slug == "" {
			t.Fatalf("create returned empty id/slug: %s", rec.Body.String())
		}
		rec = post(t, srv, adaToken, "srv-key-1", validBody)
		if rec.Code != http.StatusOK {
			t.Fatalf("replay status = %d, want 200", rec.Code)
		}
		if id2, slug2 := decodeCreate(t, rec); id2 != id || slug2 != slug {
			t.Errorf("replay returned %s/%s, want %s/%s", id2, slug2, id, slug)
		}
	})

	t.Run("POST without auth -> 401", func(t *testing.T) {
		if rec := post(t, srv, "", "srv-key-2", validBody); rec.Code != http.StatusUnauthorized {
			t.Errorf("status = %d, want 401", rec.Code)
		}
	})

	t.Run("GET /articles -> 405 (handler still dispatches)", func(t *testing.T) {
		rec := get(t, srv, "/articles")
		if rec.Code != http.StatusMethodNotAllowed {
			t.Errorf("status = %d, want 405", rec.Code)
		}
	})
}

// TestServer_BatchRouting pins that POST /articles:batch and POST /articles are
// distinct routes on the same mux (the ':' is a literal path byte, so the two
// never collide) and each reaches its own handler. It guards against a future
// routing change silently sending batch traffic to the single handler.
func TestServer_BatchRouting(t *testing.T) {
	srv := frozenServer(t, 100, 100)
	adaToken := "ak_ada." + adaSecret

	t.Run("POST /articles:batch reaches the batch handler", func(t *testing.T) {
		body := batchBody(t, []bItem{{Title: "Routed to batch", Body: "b", Author: "ada", Section: "tech", Key: "rt-1"}})
		rec := postBatch(t, srv, adaToken, body)
		if rec.Code != http.StatusCreated {
			t.Fatalf("status = %d, want 201 (%s)", rec.Code, truncate(rec.Body.String()))
		}
		// The batch handler returns {"results":[...]}, which the single handler never does.
		if !strings.Contains(rec.Body.String(), `"results"`) {
			t.Errorf("batch response shape missing: %s", rec.Body.String())
		}
	})

	t.Run("POST /articles still reaches the single handler", func(t *testing.T) {
		rec := post(t, srv, adaToken, "single-route-1", validBody)
		if rec.Code != http.StatusCreated {
			t.Fatalf("status = %d, want 201 (%s)", rec.Code, truncate(rec.Body.String()))
		}
		if strings.Contains(rec.Body.String(), `"results"`) {
			t.Errorf("single response should not be a batch envelope: %s", rec.Body.String())
		}
	})

	t.Run("GET /articles:batch -> 405", func(t *testing.T) {
		if rec := get(t, srv, "/articles:batch"); rec.Code != http.StatusMethodNotAllowed {
			t.Errorf("status = %d, want 405", rec.Code)
		}
	})
}

func TestServer_RateLimit(t *testing.T) {
	srv := frozenServer(t, 1, 2) // burst 2 per key
	adaToken := "ak_ada." + adaSecret

	// First two posts pass the limiter (created, then idempotent replay).
	if rec := post(t, srv, adaToken, "rl-key", validBody); rec.Code != http.StatusCreated {
		t.Fatalf("first post status = %d, want 201 (%s)", rec.Code, rec.Body.String())
	}
	if rec := post(t, srv, adaToken, "rl-key", validBody); rec.Code != http.StatusOK {
		t.Fatalf("second post status = %d, want 200 (replay)", rec.Code)
	}

	// Third from the same prefix exceeds the burst -> 429 with Retry-After.
	rec := post(t, srv, adaToken, "rl-key", validBody)
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("third post status = %d, want 429", rec.Code)
	}
	if ra := rec.Header().Get("Retry-After"); ra == "" {
		t.Error("429 response missing Retry-After header")
	} else if n, err := strconv.Atoi(ra); err != nil || n < 1 {
		t.Errorf("Retry-After = %q, want integer >= 1", ra)
	}
	if !strings.Contains(rec.Body.String(), "rate_limited") {
		t.Errorf("429 body = %q, want rate_limited", rec.Body.String())
	}

	// A different token prefix has its own bucket: it is not limited (here it is
	// rejected for scope, 403, but crucially not 429).
	if rec := post(t, srv, "ak_ro."+roSecret, "rl-other", validBody); rec.Code == http.StatusTooManyRequests {
		t.Errorf("different prefix got 429, want an independent bucket")
	} else if rec.Code != http.StatusForbidden {
		t.Errorf("different prefix status = %d, want 403 (read-only scope, not limited)", rec.Code)
	}

	// healthz is never limited, even after ada's bucket is exhausted.
	if rec := get(t, srv, "/healthz"); rec.Code != http.StatusOK {
		t.Errorf("healthz status = %d, want 200 (never limited)", rec.Code)
	}
}
