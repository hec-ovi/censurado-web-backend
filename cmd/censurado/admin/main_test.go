package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"strings"
	"testing"
)

// envFromMap returns a getenv-shaped lookup over a fixed map, so the config and
// credential logic is testable without touching the process environment.
func envFromMap(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

// parseEnvLines extracts KEY=VALUE pairs from gen-credentials output.
func parseEnvLines(s string) map[string]string {
	out := map[string]string{}
	for _, line := range strings.Split(s, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if k, v, ok := strings.Cut(line, "="); ok {
			out[strings.TrimSpace(k)] = strings.TrimSpace(v)
		}
	}
	return out
}

func TestGenCredentials(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := run([]string{"-gen-credentials"}, envFromMap(nil), &stdout, &stderr)
	if code != exitOK {
		t.Fatalf("exit = %d, want %d (stderr=%q)", code, exitOK, stderr.String())
	}
	env := parseEnvLines(stdout.String())

	token := env["CENSURADO_ADMIN_TOKEN"]
	hash := env["CENSURADO_ADMIN_TOKEN_HASH"]
	key := env["CENSURADO_ADMIN_SESSION_KEY"]

	if len(token) < 32 {
		t.Errorf("token %q is %d chars, want >=32", token, len(token))
	}
	// The printed hash must be the sha256 of the printed cleartext token.
	sum := sha256.Sum256([]byte(token))
	wantHash := hex.EncodeToString(sum[:])
	if hash != wantHash {
		t.Errorf("printed hash = %q, want sha256(token) = %q", hash, wantHash)
	}
	// The session key must decode to at least 32 bytes.
	decoded, err := decodeKey(key)
	if err != nil {
		t.Fatalf("session key %q does not decode: %v", key, err)
	}
	if len(decoded) < 32 {
		t.Errorf("session key decodes to %d bytes, want >=32", len(decoded))
	}

	// The minted credentials must satisfy buildConfig as-is (token hash + key).
	cfg, err := buildConfig(envFromMap(map[string]string{
		"CENSURADO_ADMIN_TOKEN_HASH":  hash,
		"CENSURADO_ADMIN_SESSION_KEY": key,
	}))
	if err != nil {
		t.Fatalf("buildConfig with generated creds: %v", err)
	}
	if cfg.TokenHash != hash || len(cfg.SessionKey) < 32 {
		t.Errorf("buildConfig result mismatch: %+v", cfg)
	}
	if !cfg.SecureCookies {
		t.Errorf("SecureCookies default = false, want true")
	}
}

func TestBuildConfig(t *testing.T) {
	validKey := strings.Repeat("ab", 32) // 64 hex chars -> 32 bytes

	t.Run("missing token and hash -> error", func(t *testing.T) {
		_, err := buildConfig(envFromMap(map[string]string{
			"CENSURADO_ADMIN_SESSION_KEY": validKey,
		}))
		if err == nil {
			t.Fatal("want error for missing token/hash, got nil")
		}
	})

	t.Run("missing session key -> error", func(t *testing.T) {
		_, err := buildConfig(envFromMap(map[string]string{
			"CENSURADO_ADMIN_TOKEN_HASH": strings.Repeat("0", 64),
		}))
		if err == nil {
			t.Fatal("want error for missing session key, got nil")
		}
	})

	t.Run("short session key -> error", func(t *testing.T) {
		_, err := buildConfig(envFromMap(map[string]string{
			"CENSURADO_ADMIN_TOKEN_HASH":  strings.Repeat("0", 64),
			"CENSURADO_ADMIN_SESSION_KEY": "abcd", // 2 bytes
		}))
		if err == nil {
			t.Fatal("want error for short session key, got nil")
		}
	})

	t.Run("CENSURADO_ADMIN_TOKEN is hashed at boot", func(t *testing.T) {
		cfg, err := buildConfig(envFromMap(map[string]string{
			"CENSURADO_ADMIN_TOKEN":       "operator-token-fixture",
			"CENSURADO_ADMIN_SESSION_KEY": validKey,
		}))
		if err != nil {
			t.Fatalf("buildConfig: %v", err)
		}
		if cfg.TokenHash != hashTokenHex("operator-token-fixture") {
			t.Errorf("TokenHash = %q, want hash of the cleartext token", cfg.TokenHash)
		}
	})

	t.Run("explicit hash + secure cookies off", func(t *testing.T) {
		cfg, err := buildConfig(envFromMap(map[string]string{
			"CENSURADO_ADMIN_TOKEN_HASH":     strings.Repeat("a", 64),
			"CENSURADO_ADMIN_SESSION_KEY":    validKey,
			"CENSURADO_ADMIN_SECURE_COOKIES": "false",
		}))
		if err != nil {
			t.Fatalf("buildConfig: %v", err)
		}
		if cfg.SecureCookies {
			t.Errorf("SecureCookies = true, want false (env override)")
		}
	})

	t.Run("base64 session key is accepted", func(t *testing.T) {
		// 36 bytes (non-hex content) base64-encoded, to exercise the base64 path.
		raw := []byte("admin-session-key-base64-path-0123456")
		b64 := base64.StdEncoding.EncodeToString(raw)
		cfg, err := buildConfig(envFromMap(map[string]string{
			"CENSURADO_ADMIN_TOKEN_HASH":  strings.Repeat("0", 64),
			"CENSURADO_ADMIN_SESSION_KEY": b64,
		}))
		if err != nil {
			t.Fatalf("buildConfig with base64 key: %v", err)
		}
		if len(cfg.SessionKey) < 32 {
			t.Errorf("decoded key = %d bytes, want >=32", len(cfg.SessionKey))
		}
	})
}

// TestRegenerateUnavailable proves the backend no longer rebuilds the static site in
// process: the admin's regenerate closure reports the work moved to the censurado-web
// generator (returning a zero RegenResult and a clear error) instead of running it.
func TestRegenerateUnavailable(t *testing.T) {
	res, err := unavailableRegenerate(context.Background())
	if err == nil {
		t.Fatal("want an error: regeneration is not available in the backend")
	}
	if !strings.Contains(err.Error(), "censurado-web generator") {
		t.Errorf("err = %v, want it to point at the censurado-web generator", err)
	}
	if res.Written != 0 || res.Unchanged != 0 || res.Deleted != 0 || res.ScopeCount != 0 || len(res.Purge) != 0 {
		t.Errorf("want a zero RegenResult, got %+v", res)
	}
}

// TestRun_MissingEnvNoServer proves a config error returns a distinct nonzero
// exit BEFORE any socket is bound (run reaches buildConfig and returns).
func TestRun_MissingEnvNoServer(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := run(nil, envFromMap(nil), &stdout, &stderr)
	if code != exitConfig {
		t.Fatalf("exit = %d, want %d (config error)", code, exitConfig)
	}
	if !strings.Contains(stderr.String(), "config:") {
		t.Errorf("stderr = %q, want a clear config error", stderr.String())
	}
}
