package adminweb

import (
	"context"
	"time"

	"github.com/hec-ovi/censurado-web-backend/internal/publish"
)

// DefaultSessionTTL is the session lifetime. The cookie carries its own signed
// expiry, so this is just the issuance window.
const DefaultSessionTTL = 12 * time.Hour

// Config holds everything the admin panel needs. A zero SessionKey or empty
// LoginTokenHash disables the panel entirely (Handler returns the API unwrapped),
// so a pure-API deployment stays exactly as it was.
type Config struct {
	// SessionKey signs the session and CSRF HMACs. Decode it from the env with
	// DecodeKey. Empty disables the panel.
	SessionKey []byte
	// LoginTokenHash is the hex SHA-256 of the operator login token. The login form
	// compares the submitted token's hash to this in constant time. Empty disables
	// the panel.
	LoginTokenHash string
	// LoginToken, when set and matching LoginTokenHash, is prefilled into the login
	// form for a local box only (pure convenience; the hash is what gates). Leave
	// empty in any shared/remote deployment.
	LoginToken string
	// SecureCookies sets the Secure attribute on the session and CSRF cookies. Off
	// for plain-HTTP localhost, on behind TLS.
	SecureCookies bool
	// SessionTTL overrides DefaultSessionTTL when non-zero.
	SessionTTL time.Duration
	// Now is an injectable clock for tests; nil means time.Now.
	Now func() time.Time
	// PanelText returns the panel_text catalog for a language as a key->value map,
	// which the server injects into the SPA shell so t() renders from the DB. Nil (or
	// an error) leaves the panel on its compiled English identity fallback.
	PanelText func(ctx context.Context, lang string) (map[string]string, error)
	// SectionLabels returns the shared section labels (slug->label) the panel shows in
	// its section pickers, read from frontend_text via SectionLabelsFromFrontend. Nil
	// (or an error) falls the panel back to raw slugs.
	SectionLabels func(ctx context.Context, lang string) (map[string]string, error)
}

// Enabled reports whether the panel is configured. Both the signing key and the
// login hash must be present.
func (c Config) Enabled() bool {
	return len(c.SessionKey) > 0 && c.LoginTokenHash != ""
}

func (c Config) ttl() time.Duration {
	if c.SessionTTL > 0 {
		return c.SessionTTL
	}
	return DefaultSessionTTL
}

func (c Config) now() time.Time {
	if c.Now != nil {
		return c.Now()
	}
	return time.Now()
}

// operatorIdentity is the in-process identity a valid browser session maps to. It
// carries the full operator scope set so a session can create/edit content and
// publish as any author, exactly what the operator console needs, without ever
// holding a bearer token.
func operatorIdentity() publish.Identity {
	return publish.Identity{
		Author: "operator",
		Scopes: []string{publish.ScopeWrite, publish.ScopePublishAny, publish.ScopeAdminWrite},
	}
}
