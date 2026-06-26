package publish

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"strings"
)

// ErrUnauthorized is returned when a token is missing, malformed, or invalid.
var ErrUnauthorized = errors.New("publish: unauthorized")

// Identity is the authenticated caller: which author persona they are and what
// scopes their key grants.
type Identity struct {
	Author string
	Scopes []string
}

// HasScope reports whether the identity holds the given scope.
func (id Identity) HasScope(scope string) bool {
	for _, s := range id.Scopes {
		if s == scope {
			return true
		}
	}
	return false
}

// Authenticator verifies a bearer token and returns the caller's identity. It is
// an interface so the hashed-key scheme used now can be swapped for OAuth2
// client-credentials later without touching the handler.
type Authenticator interface {
	Authenticate(token string) (Identity, error)
}

// StaticKeyAuth verifies tokens of the form "<prefix>.<secret>" against a fixed
// set of per-author keys. The secret is never stored, only its SHA-256, so a
// leak of the key table does not reveal usable credentials.
type StaticKeyAuth struct {
	keys map[string]hashedKey
}

type hashedKey struct {
	hashHex string
	author  string
	scopes  []string
}

// NewStaticKeyAuth returns an empty key set.
func NewStaticKeyAuth() *StaticKeyAuth {
	return &StaticKeyAuth{keys: make(map[string]hashedKey)}
}

// Add registers a key by its public prefix, the hex SHA-256 of its secret, the
// author it authenticates, and its scopes.
func (a *StaticKeyAuth) Add(prefix, secretSHA256Hex, author string, scopes ...string) {
	a.keys[prefix] = hashedKey{hashHex: secretSHA256Hex, author: author, scopes: scopes}
}

// Authenticate verifies the token and returns the caller's identity.
func (a *StaticKeyAuth) Authenticate(token string) (Identity, error) {
	prefix, secret, ok := strings.Cut(token, ".")
	if !ok || prefix == "" || secret == "" {
		return Identity{}, ErrUnauthorized
	}
	k, ok := a.keys[prefix]
	if !ok {
		return Identity{}, ErrUnauthorized
	}
	if subtle.ConstantTimeCompare([]byte(HashSecret(secret)), []byte(k.hashHex)) != 1 {
		return Identity{}, ErrUnauthorized
	}
	return Identity{Author: k.author, Scopes: k.scopes}, nil
}

// HashSecret returns the hex SHA-256 of a key secret. Used to provision keys and
// in tests; API key secrets are high-entropy, so a fast hash is appropriate.
func HashSecret(secret string) string {
	sum := sha256.Sum256([]byte(secret))
	return hex.EncodeToString(sum[:])
}
