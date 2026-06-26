package publish

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"
)

// okHandler records that it ran and returns 200, so a test can tell a request that
// reached the wrapped handler from one the limiter short-circuited with 429.
func okHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
}

// doReq drives one request through h with the given bearer token (empty for none).
func doReq(h http.Handler, token string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(http.MethodPost, "/articles", nil)
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, r)
	return rec
}

func TestRateLimiter_BurstThenLimit(t *testing.T) {
	clock := time.Date(2026, 6, 22, 12, 0, 0, 0, time.UTC)
	rl := NewRateLimiter(1, 3, func() time.Time { return clock }) // burst 3
	h := rl.Wrap(okHandler())

	for i := 0; i < 3; i++ {
		if code := doReq(h, "ak_ada.secret").Code; code != http.StatusOK {
			t.Fatalf("request %d: code = %d, want 200 (within burst)", i+1, code)
		}
	}
	rec := doReq(h, "ak_ada.secret")
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("4th request: code = %d, want 429 (burst exhausted)", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/problem+json" {
		t.Errorf("Content-Type = %q, want application/problem+json", ct)
	}
	if !strings.Contains(rec.Body.String(), `"rate_limited"`) {
		t.Errorf("body = %q, want rate_limited code", rec.Body.String())
	}
}

func TestRateLimiter_RefillAfterAdvance(t *testing.T) {
	clock := time.Date(2026, 6, 22, 12, 0, 0, 0, time.UTC)
	rl := NewRateLimiter(2, 2, func() time.Time { return clock }) // 2 tokens/sec, burst 2
	h := rl.Wrap(okHandler())

	// Drain the bucket, then confirm it is empty.
	for i := 0; i < 2; i++ {
		if code := doReq(h, "ak_ada.secret").Code; code != http.StatusOK {
			t.Fatalf("drain request %d: code = %d, want 200", i+1, code)
		}
	}
	if code := doReq(h, "ak_ada.secret").Code; code != http.StatusTooManyRequests {
		t.Fatalf("after drain: code = %d, want 429", code)
	}

	// Advance half a second: at 2 tokens/sec that restores exactly one token.
	clock = clock.Add(500 * time.Millisecond)
	if code := doReq(h, "ak_ada.secret").Code; code != http.StatusOK {
		t.Fatalf("after 0.5s refill: code = %d, want 200", code)
	}
	if code := doReq(h, "ak_ada.secret").Code; code != http.StatusTooManyRequests {
		t.Fatalf("after spending the refilled token: code = %d, want 429", code)
	}
}

func TestRateLimiter_IndependentBucketsPerPrefix(t *testing.T) {
	clock := time.Date(2026, 6, 22, 12, 0, 0, 0, time.UTC)
	rl := NewRateLimiter(1, 1, func() time.Time { return clock }) // burst 1
	h := rl.Wrap(okHandler())

	// Exhaust ada's single token.
	if code := doReq(h, "ak_ada.secret").Code; code != http.StatusOK {
		t.Fatalf("ada first: code = %d, want 200", code)
	}
	if code := doReq(h, "ak_ada.secret").Code; code != http.StatusTooManyRequests {
		t.Fatalf("ada second: code = %d, want 429", code)
	}
	// bob has his own full bucket and is unaffected.
	if code := doReq(h, "ak_bob.other").Code; code != http.StatusOK {
		t.Fatalf("bob first: code = %d, want 200 (independent bucket)", code)
	}
}

func TestRateLimiter_RetryAfterHeader(t *testing.T) {
	clock := time.Date(2026, 6, 22, 12, 0, 0, 0, time.UTC)
	rl := NewRateLimiter(1, 1, func() time.Time { return clock }) // 1 token/sec, burst 1
	h := rl.Wrap(okHandler())

	if code := doReq(h, "ak_ada.secret").Code; code != http.StatusOK {
		t.Fatalf("first: code = %d, want 200", code)
	}
	rec := doReq(h, "ak_ada.secret")
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("second: code = %d, want 429", rec.Code)
	}
	ra := rec.Header().Get("Retry-After")
	n, err := strconv.Atoi(ra)
	if err != nil {
		t.Fatalf("Retry-After = %q, want an integer: %v", ra, err)
	}
	if n < 1 {
		t.Errorf("Retry-After = %d, want >= 1", n)
	}
}

func TestRateLimiter_FallsBackToRemoteAddr(t *testing.T) {
	clock := time.Date(2026, 6, 22, 12, 0, 0, 0, time.UTC)
	rl := NewRateLimiter(1, 1, func() time.Time { return clock })
	h := rl.Wrap(okHandler())

	// No bearer token: requests from the same RemoteAddr share one bucket.
	req := func() int {
		r := httptest.NewRequest(http.MethodPost, "/articles", nil)
		r.RemoteAddr = "203.0.113.7:5555"
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, r)
		return rec.Code
	}
	if code := req(); code != http.StatusOK {
		t.Fatalf("first anonymous: code = %d, want 200", code)
	}
	if code := req(); code != http.StatusTooManyRequests {
		t.Fatalf("second anonymous: code = %d, want 429 (same addr bucket)", code)
	}
}

func TestServerHandler_HealthzNeverLimited(t *testing.T) {
	clock := time.Date(2026, 6, 22, 12, 0, 0, 0, time.UTC)
	// A stingy limiter (tiny rate, burst 1): if healthz were limited, the second
	// probe would 429.
	rl := NewRateLimiter(0.0001, 1, func() time.Time { return clock })
	// healthz never touches the handler's dependencies, so nil deps are safe here.
	srv := NewServerHandler(NewHandler(nil, nil, nil, nil), rl, nil, nil, nil)

	for i := 0; i < 5; i++ {
		r := httptest.NewRequest(http.MethodGet, "/healthz", nil)
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, r)
		if rec.Code != http.StatusOK {
			t.Fatalf("healthz probe %d: code = %d, want 200 (never limited)", i+1, rec.Code)
		}
		if rec.Body.String() != "ok" {
			t.Errorf("healthz body = %q, want \"ok\"", rec.Body.String())
		}
	}
}

// TestRateLimiter_KeyCapBoundsMemory pins the primary DoS bound: a flood of unique
// keys must never grow the bucket map past the cap. Without the evictLocked call in
// allow() this map is unbounded; a future refactor that drops it would fail here.
func TestRateLimiter_KeyCapBoundsMemory(t *testing.T) {
	clock := time.Date(2026, 6, 22, 12, 0, 0, 0, time.UTC)
	rl := NewRateLimiter(1, 1, func() time.Time { return clock }) // burst 1, frozen clock
	rl.maxKeys = 8                                                // test-only smaller cap keeps the flood cheap
	rl.logf = func(string, ...any) {}                             // silence the repeated shed logs

	// With a frozen clock nothing ever refills, so the first-pass reclaim finds no
	// full buckets and the whole-table drop carries the bound. Either way the map
	// must stay at or below the cap after every insertion.
	const flood = 200
	for i := 0; i < flood; i++ {
		rl.allow("key:k" + strconv.Itoa(i))
		if got := len(rl.buckets); got > rl.maxKeys {
			t.Fatalf("after %d distinct keys: len(buckets) = %d, want <= maxKeys (%d)", i+1, got, rl.maxKeys)
		}
	}
}

// TestRateLimiter_EvictReclaimsFullBucketsFirst pins the first eviction pass: when
// the cap is hit, fully-refilled idle buckets (indistinguishable from fresh ones)
// are dropped first, and the lenient whole-table reset does NOT fire while anything
// is reclaimable.
func TestRateLimiter_EvictReclaimsFullBucketsFirst(t *testing.T) {
	clock := time.Date(2026, 6, 22, 12, 0, 0, 0, time.UTC)
	rl := NewRateLimiter(1, 2, func() time.Time { return clock }) // burst 2, 1 token/sec
	rl.maxKeys = 3

	var resets int
	rl.logf = func(string, ...any) { resets++ } // the whole-table drop is the only logf caller

	rl.allow("k0") // tokens 2 -> 1, last = t0
	rl.allow("k1") // tokens 2 -> 1, last = t0

	// Advance 10s so k0 and k1 refill to full (>= burst) when evict inspects them.
	clock = clock.Add(10 * time.Second)

	rl.allow("k2") // fresh at t+10s, tokens 2 -> 1: NOT full, must survive. len == maxKeys.
	rl.allow("k3") // trips the cap -> evictLocked reclaims the two full idle buckets.

	if resets != 0 {
		t.Fatalf("whole-table reset fired %d time(s); the first-pass reclaim should have sufficed", resets)
	}
	for _, gone := range []string{"k0", "k1"} {
		if _, ok := rl.buckets[gone]; ok {
			t.Errorf("bucket %q survived; full idle buckets must be reclaimed first", gone)
		}
	}
	for _, kept := range []string{"k2", "k3"} {
		if _, ok := rl.buckets[kept]; !ok {
			t.Errorf("bucket %q missing; non-full buckets must be kept", kept)
		}
	}
	if got := len(rl.buckets); got != 2 {
		t.Errorf("len(buckets) = %d, want 2 (k2 + k3)", got)
	}
}

// TestRateLimiter_EvictDropsWholeTableWhenNothingReclaimable pins the fallback: when
// no bucket has refilled (all drained), the first pass reclaims nothing, so the whole
// table is dropped and the shed count is logged exactly once.
func TestRateLimiter_EvictDropsWholeTableWhenNothingReclaimable(t *testing.T) {
	clock := time.Date(2026, 6, 22, 12, 0, 0, 0, time.UTC)
	rl := NewRateLimiter(1, 1, func() time.Time { return clock }) // burst 1, frozen clock
	rl.maxKeys = 3

	var logged []string
	rl.logf = func(format string, args ...any) { logged = append(logged, fmt.Sprintf(format, args...)) }

	rl.allow("k0") // tokens 1 -> 0
	rl.allow("k1") // tokens 1 -> 0
	rl.allow("k2") // tokens 1 -> 0; len == maxKeys, none reclaimable
	rl.allow("k3") // trips the cap -> whole-table drop

	if len(logged) != 1 {
		t.Fatalf("logf called %d time(s), want exactly 1 shed log: %v", len(logged), logged)
	}
	if !strings.Contains(logged[0], "3") { // both the cap and the shed count are 3
		t.Errorf("shed log = %q, want it to report the shed count", logged[0])
	}
	for _, gone := range []string{"k0", "k1", "k2"} {
		if _, ok := rl.buckets[gone]; ok {
			t.Errorf("bucket %q survived the whole-table drop", gone)
		}
	}
	if got := len(rl.buckets); got != 1 {
		t.Errorf("len(buckets) = %d, want 1 (only k3 after the reset)", got)
	}
}

// TestRateLimiter_PrefixIsBucketSelector pins the accepted residual risk documented
// on keyFor: limiting runs before auth and keys on the PUBLIC prefix, so a caller who
// knows only a victim's prefix (not the secret) can drain that prefix's bucket and get
// the legitimate holder 429'd. If a future change keys on the verified secret instead,
// this expectation flips and the test fails, surfacing the design shift.
func TestRateLimiter_PrefixIsBucketSelector(t *testing.T) {
	clock := time.Date(2026, 6, 22, 12, 0, 0, 0, time.UTC)
	rl := NewRateLimiter(1, 1, func() time.Time { return clock }) // burst 1 per key
	h := rl.Wrap(okHandler())

	// Attacker holds only the public prefix and sends a wrong secret; keyFor buckets
	// on the prefix, so this consumes the shared token.
	if code := doReq(h, "ak_victim.attacker-garbage").Code; code != http.StatusOK {
		t.Fatalf("attacker first (drains the prefix bucket): code = %d, want 200", code)
	}
	// The legitimate holder with the CORRECT secret is now limited before auth runs.
	if code := doReq(h, "ak_victim.the-real-secret").Code; code != http.StatusTooManyRequests {
		t.Fatalf("victim with correct secret: code = %d, want 429 (prefix bucket already drained)", code)
	}
}

func TestRateLimiter_NilIsIdentity(t *testing.T) {
	var rl *RateLimiter // nil
	h := rl.Wrap(okHandler())
	for i := 0; i < 100; i++ {
		if code := doReq(h, "ak_ada.secret").Code; code != http.StatusOK {
			t.Fatalf("nil limiter request %d: code = %d, want 200 (no limiting)", i+1, code)
		}
	}
}
