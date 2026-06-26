package adminweb

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// cookieName is the session cookie. "cnz" = censurado.
const cookieName = "cnz_admin"

// sessionVersion prefixes the signed payload so the wire format can evolve
// without silently accepting an old shape.
const sessionVersion = "v1"

// hashToken returns the lowercase hex SHA-256 of s. The operator login token is
// only ever stored and compared as this hash; the cleartext is never persisted.
// adminweb keeps its own copy (rather than importing internal/publish) so the
// admin layer has no dependency on the write path.
func hashToken(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

// b64 is unpadded URL-safe base64, used for both cookie halves and the CSRF
// token so the values are URL- and attribute-safe.
var b64 = base64.RawURLEncoding

// sign returns the HMAC-SHA256 of msg under the session key.
func (h *Handler) sign(msg []byte) []byte {
	mac := hmac.New(sha256.New, h.sessionKey)
	mac.Write(msg)
	return mac.Sum(nil)
}

// sessionPayload is the signed bytes: "v1|<expUnix>".
func sessionPayload(expUnix int64) []byte {
	return []byte(sessionVersion + "|" + strconv.FormatInt(expUnix, 10))
}

// sessionValue is the cookie value: base64url(payload) + "." + base64url(HMAC).
func (h *Handler) sessionValue(expUnix int64) string {
	payload := sessionPayload(expUnix)
	return b64.EncodeToString(payload) + "." + b64.EncodeToString(h.sign(payload))
}

// issueSession writes a fresh session cookie expiring now+TTL and returns the
// new expiry (unix seconds) so callers can bind the page CSRF token to the very
// cookie the browser will present next.
func (h *Handler) issueSession(w http.ResponseWriter, now time.Time) int64 {
	exp := now.Add(h.sessionTTL)
	http.SetCookie(w, &http.Cookie{
		Name:     cookieName,
		Value:    h.sessionValue(exp.Unix()),
		Path:     "/admin",
		HttpOnly: true,
		Secure:   h.secureCookies,
		SameSite: http.SameSiteStrictMode,
		MaxAge:   int(h.sessionTTL.Seconds()),
	})
	return exp.Unix()
}

// clearSession overwrites the session cookie with an empty, immediately-expired
// value so logout invalidates the browser's copy.
func (h *Handler) clearSession(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name:     cookieName,
		Value:    "",
		Path:     "/admin",
		HttpOnly: true,
		Secure:   h.secureCookies,
		SameSite: http.SameSiteStrictMode,
		MaxAge:   -1,
	})
}

// verifySession reads and validates the session cookie. It returns the session's
// expiry (unix seconds) and ok=true only when the cookie is present, the HMAC
// verifies under the session key (constant-time), the version matches, and the
// session has not expired relative to now. Any failure returns ok=false with no
// distinguishing error, so a forged or tampered cookie is indistinguishable from
// an absent one to the caller.
func (h *Handler) verifySession(r *http.Request, now time.Time) (int64, bool) {
	c, err := r.Cookie(cookieName)
	if err != nil || c.Value == "" {
		return 0, false
	}
	// Split on the LAST '.', so a future payload that itself contained a '.' (it
	// does not today) would still separate cleanly from the trailing MAC.
	dot := strings.LastIndex(c.Value, ".")
	if dot < 0 {
		return 0, false
	}
	payload, err := b64.DecodeString(c.Value[:dot])
	if err != nil {
		return 0, false
	}
	gotMAC, err := b64.DecodeString(c.Value[dot+1:])
	if err != nil {
		return 0, false
	}
	wantMAC := h.sign(payload)
	if subtle.ConstantTimeCompare(gotMAC, wantMAC) != 1 {
		return 0, false
	}
	// Signature verified; now parse and bound-check the payload.
	prefix := sessionVersion + "|"
	s := string(payload)
	if !strings.HasPrefix(s, prefix) {
		return 0, false
	}
	expUnix, err := strconv.ParseInt(s[len(prefix):], 10, 64)
	if err != nil {
		return 0, false
	}
	if now.Unix() >= expUnix {
		return 0, false // expired
	}
	return expUnix, true
}

// csrfToken derives a per-session CSRF token bound to the session expiry. It is a
// distinct HMAC (domain-separated by the "csrf|" prefix) so it can be embedded in
// pages and form fields without revealing the session MAC. Because it is a pure
// function of the session expiry and the secret key, the server re-derives and
// compares it on every POST without server-side state.
func (h *Handler) csrfToken(expUnix int64) string {
	return b64.EncodeToString(h.sign([]byte("csrf|" + strconv.FormatInt(expUnix, 10))))
}

// validCSRF reports whether submitted matches the token expected for the session
// expiry, compared in constant time.
func (h *Handler) validCSRF(expUnix int64, submitted string) bool {
	want := h.csrfToken(expUnix)
	return subtle.ConstantTimeCompare([]byte(submitted), []byte(want)) == 1
}

// ctxKey is the unexported context key type for the session expiry.
type ctxKey int

const sessionExpKey ctxKey = 0

// sessionExpFromContext returns the session expiry stashed by requireSession.
func sessionExpFromContext(r *http.Request) (int64, bool) {
	v, ok := r.Context().Value(sessionExpKey).(int64)
	return v, ok
}
