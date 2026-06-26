package publish

import (
	"log"
	"math"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

// defaultMaxKeys bounds how many distinct buckets the limiter tracks at once, so a
// flood of unique prefixes (or spoofed addresses) cannot grow the map without
// limit. It is a soft cap: see evictLocked for the reclaim strategy.
const defaultMaxKeys = 4096

// RateLimiter is a per-key token-bucket limiter. Each key gets its own bucket of
// up to burst tokens, refilled at ratePerSec tokens per second. It is safe for
// concurrent use. The clock is injectable so tests can advance time deterministically.
type RateLimiter struct {
	rate    float64 // tokens added per second
	burst   float64 // bucket capacity
	now     func() time.Time
	maxKeys int
	logf    func(format string, args ...any)

	mu      sync.Mutex
	buckets map[string]*bucket
}

type bucket struct {
	tokens float64
	last   time.Time
}

// NewRateLimiter returns a limiter that allows bursts of burst requests per key and
// refills at ratePerSec. now may be nil (defaults to time.Now).
func NewRateLimiter(ratePerSec float64, burst int, now func() time.Time) *RateLimiter {
	if now == nil {
		now = time.Now
	}
	return &RateLimiter{
		rate:    ratePerSec,
		burst:   float64(burst),
		now:     now,
		maxKeys: defaultMaxKeys,
		logf:    log.Printf,
		buckets: make(map[string]*bucket),
	}
}

// allow consumes one token for key. It returns whether the request is permitted
// and, when denied, how long until a token becomes available.
func (rl *RateLimiter) allow(key string) (bool, time.Duration) {
	rl.mu.Lock()
	defer rl.mu.Unlock()

	now := rl.now()
	b, ok := rl.buckets[key]
	if !ok {
		if len(rl.buckets) >= rl.maxKeys {
			rl.evictLocked(now)
		}
		b = &bucket{tokens: rl.burst, last: now}
		rl.buckets[key] = b
	} else if elapsed := now.Sub(b.last).Seconds(); elapsed > 0 {
		b.tokens = math.Min(rl.burst, b.tokens+elapsed*rl.rate)
		b.last = now
	}

	if b.tokens >= 1 {
		b.tokens--
		return true, 0
	}
	// Tokens are below one: report the wait until the next whole token. A
	// non-positive rate never refills, so report a long, finite retry instead.
	need := 1 - b.tokens
	if rl.rate <= 0 {
		return false, time.Hour
	}
	return false, time.Duration(need / rl.rate * float64(time.Second))
}

// evictLocked reclaims map space when the key cap is hit. It first drops idle
// buckets that have fully refilled: a bucket at capacity is indistinguishable from
// a freshly created one, so removing it changes no caller's effective limit. Only
// if that is not enough does it drop the whole table (lenient: drained buckets
// reset to full), and it logs the count it shed. Callers hold rl.mu.
func (rl *RateLimiter) evictLocked(now time.Time) {
	before := len(rl.buckets)
	for k, b := range rl.buckets {
		tokens := b.tokens
		if elapsed := now.Sub(b.last).Seconds(); elapsed > 0 {
			tokens = math.Min(rl.burst, tokens+elapsed*rl.rate)
		}
		if tokens >= rl.burst {
			delete(rl.buckets, k)
		}
	}
	if len(rl.buckets) >= rl.maxKeys {
		rl.buckets = make(map[string]*bucket)
		rl.logf("publish: rate limiter at key cap %d, evicted all %d tracked keys", rl.maxKeys, before)
	}
}

// keyFor derives the bucket key for a request. It keys by the PUBLIC token prefix
// (the part before the first ".") so limiting is genuinely per key without
// verifying the secret. When there is no usable bearer token it falls back to the
// remote host, so unauthenticated floods are still bounded. The "key:"/"addr:"
// namespaces keep a token prefix from ever colliding with an IP address.
//
// Residual risk (intentional, pinned by TestRateLimiter_PrefixIsBucketSelector):
// the prefix is a bucket SELECTOR, not a secret, and limiting runs before auth.
// Anyone who learns a victim's public prefix can send "ak_victim.<garbage>" floods
// that drain the victim's bucket and get the legitimate holder 429'd without ever
// holding the secret (a targeted cross-tenant DoS). This is accepted because the
// prefix is ak_ + 8 hex = 32 bits from crypto/rand (not cheaply enumerable), the
// API never reveals valid prefixes (401/429 leak nothing), and the server binds to
// localhost by default. Pre-auth keying is deliberate: the limiter is a plain
// middleware that must shed load before any secret comparison or DB work runs, so
// it cannot depend on the auth result without coupling the two and moving work
// ahead of the shed point.
func (rl *RateLimiter) keyFor(r *http.Request) string {
	if token := bearerToken(r.Header.Get("Authorization")); token != "" {
		if prefix, _, ok := strings.Cut(token, "."); ok && prefix != "" {
			return "key:" + prefix
		}
	}
	host := r.RemoteAddr
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	return "addr:" + host
}

// Wrap returns next guarded by the limiter. A nil limiter is the identity wrapper
// (no limiting). This middleware runs BEFORE the handler authenticates, so it must
// not depend on auth success: it keys off the public token prefix only.
func (rl *RateLimiter) Wrap(next http.Handler) http.Handler {
	if rl == nil {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ok, wait := rl.allow(rl.keyFor(r))
		if !ok {
			retry := int(math.Ceil(wait.Seconds()))
			if retry < 1 {
				retry = 1
			}
			w.Header().Set("Retry-After", strconv.Itoa(retry))
			writeProblem(w, problem{
				Status: http.StatusTooManyRequests,
				Code:   "rate_limited",
				Detail: "rate limit exceeded; retry after " + strconv.Itoa(retry) + "s",
			})
			return
		}
		next.ServeHTTP(w, r)
	})
}
