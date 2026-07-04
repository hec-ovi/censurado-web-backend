// Package adminweb serves the operator admin panel from inside the backend: a
// buildless single-page app (embedded via go:embed) plus the stdlib signed-cookie
// login/session that gates it, folded in so the backend is one deployable that
// serves the JSON API, the admin UI, and the data together. It replaces the former
// Python backend-for-frontend: the browser session is an ADDITIONAL front door
// beside the existing bearer-token API lanes, never a replacement for them.
//
// The session/CSRF math here is the same wire format the removed Python panel used
// (which was itself a port of an earlier Go admin), so its unit vectors carry over.
package adminweb

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"strconv"
	"strings"
)

// SessionCookie is the signed session cookie. "cnz" = censurado.
const SessionCookie = "cnz_panel"

// CSRFCookie carries the CSRF token to the SPA. It is intentionally NOT HttpOnly so
// the page JS can read it and echo it back in the X-CSRF-Token header (double
// submit). It is useless on its own: a pure function of the secret key and the
// session expiry, so a value not bound to a valid session never validates.
const CSRFCookie = "cnz_panel_csrf"

// sessionVersion prefixes the signed payload so the wire format can evolve without
// silently accepting an old shape.
const sessionVersion = "v1"

// b64 is unpadded URL-safe base64, used for both cookie halves and the CSRF token so
// the values are URL- and attribute-safe.
var b64 = base64.RawURLEncoding

// hashToken returns the lowercase hex SHA-256 of s. The login token is only ever
// compared as this hash; the cleartext is never persisted.
func hashToken(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

// sign returns the HMAC-SHA256 of msg under the session key.
func sign(key, msg []byte) []byte {
	mac := hmac.New(sha256.New, key)
	mac.Write(msg)
	return mac.Sum(nil)
}

// sessionPayload is the signed bytes: "v1|<expUnix>".
func sessionPayload(expUnix int64) []byte {
	return []byte(sessionVersion + "|" + strconv.FormatInt(expUnix, 10))
}

// SessionValue is the cookie value: base64url(payload) + "." + base64url(HMAC).
func SessionValue(key []byte, expUnix int64) string {
	payload := sessionPayload(expUnix)
	return b64.EncodeToString(payload) + "." + b64.EncodeToString(sign(key, payload))
}

// VerifySession validates a session cookie value. It returns the session's expiry
// (unix seconds) and ok=true only when the value is present, the HMAC verifies under
// the key (constant-time), the version matches, and the session has not expired
// relative to nowUnix. Any failure returns (0,false) with no distinguishing reason,
// so a forged or tampered value is indistinguishable from an absent one.
func VerifySession(key []byte, value string, nowUnix int64) (int64, bool) {
	if value == "" {
		return 0, false
	}
	// Split on the LAST '.', so a future payload that itself contained a '.' (it
	// does not today) would still separate cleanly from the trailing MAC.
	dot := strings.LastIndex(value, ".")
	if dot < 0 {
		return 0, false
	}
	payload, err := b64.DecodeString(value[:dot])
	if err != nil {
		return 0, false
	}
	gotMAC, err := b64.DecodeString(value[dot+1:])
	if err != nil {
		return 0, false
	}
	wantMAC := sign(key, payload)
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
	if nowUnix >= expUnix {
		return 0, false // expired
	}
	return expUnix, true
}

// CSRFValue derives a per-session CSRF token bound to the session expiry. It is a
// distinct HMAC (domain-separated by the "csrf|" prefix) so it can be exposed to the
// page without revealing the session MAC. Because it is a pure function of the
// session expiry and the secret key, the server re-derives and compares it on every
// write without server-side state.
func CSRFValue(key []byte, expUnix int64) string {
	return b64.EncodeToString(sign(key, []byte("csrf|"+strconv.FormatInt(expUnix, 10))))
}

// ValidCSRF reports whether submitted matches the token expected for the session
// expiry, compared in constant time.
func ValidCSRF(key []byte, expUnix int64, submitted string) bool {
	want := CSRFValue(key, expUnix)
	return subtle.ConstantTimeCompare([]byte(submitted), []byte(want)) == 1
}

// DecodeKey accepts the session key as hex or any common base64 alphabet, hex tried
// first (the mint script emits token_hex, i.e. 64 hex chars for a 32-byte key), so a
// key that is valid hex is decoded as hex.
func DecodeKey(s string) ([]byte, bool) {
	s = strings.TrimSpace(s)
	if b, err := hex.DecodeString(s); err == nil {
		return b, true
	}
	for _, enc := range []*base64.Encoding{
		base64.StdEncoding, base64.RawStdEncoding,
		base64.URLEncoding, base64.RawURLEncoding,
	} {
		if b, err := enc.DecodeString(s); err == nil {
			return b, true
		}
	}
	return nil, false
}
