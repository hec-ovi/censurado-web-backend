package publish

import (
	"context"
	"net/http"
)

// identityCtxKey is the unexported context key carrying a pre-authenticated
// identity injected ahead of the handlers.
type identityCtxKey struct{}

// ContextWithIdentity returns a copy of ctx carrying a pre-authenticated identity.
// The adminweb session gate uses it so a valid browser session maps to an operator
// identity IN-PROCESS, with no bearer token: the handlers resolve this context
// identity before parsing the Authorization header. External bearer callers never
// set it, so their path is byte-identical to before.
func ContextWithIdentity(ctx context.Context, id Identity) context.Context {
	return context.WithValue(ctx, identityCtxKey{}, id)
}

// identityFromContext returns an identity injected by the session gate, if any.
func identityFromContext(ctx context.Context) (Identity, bool) {
	id, ok := ctx.Value(identityCtxKey{}).(Identity)
	return id, ok
}

// resolveIdentity returns the caller identity: a context identity injected by the
// session gate takes precedence; otherwise the bearer token is parsed and
// authenticated. On the bearer path it writes the same missing_token/invalid_token
// problem the handlers used before and returns ok=false; scope enforcement stays
// with each caller so the per-lane scope errors are unchanged. The context path
// never fails here (the gate only injects a valid identity), so it applies scope
// checks uniformly without a distinct error shape.
func resolveIdentity(auth Authenticator, w http.ResponseWriter, r *http.Request) (Identity, bool) {
	if id, ok := identityFromContext(r.Context()); ok {
		return id, true
	}
	token := bearerToken(r.Header.Get("Authorization"))
	if token == "" {
		writeProblem(w, problem{Status: http.StatusUnauthorized, Code: "missing_token"})
		return Identity{}, false
	}
	id, err := auth.Authenticate(token)
	if err != nil {
		writeProblem(w, problem{Status: http.StatusUnauthorized, Code: "invalid_token"})
		return Identity{}, false
	}
	return id, true
}
