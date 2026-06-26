package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hec-ovi/censurado-web-backend/internal/publish"
)

func envFromMap(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

// parseGenKey pulls the TOKEN= line and the JSON entry line out of -gen-key output.
func parseGenKey(t *testing.T, out string) (token string, entry keyEntry) {
	t.Helper()
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(line, "TOKEN="):
			token = strings.TrimPrefix(line, "TOKEN=")
		case strings.HasPrefix(line, "{"):
			if err := json.Unmarshal([]byte(line), &entry); err != nil {
				t.Fatalf("keys entry line is not valid JSON: %v (%q)", err, line)
			}
		}
	}
	return token, entry
}

func TestGenKey(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := run([]string{"-gen-key", "-author", "ada"}, envFromMap(nil), &stdout, &stderr)
	if code != exitOK {
		t.Fatalf("exit = %d, want %d (stderr=%q)", code, exitOK, stderr.String())
	}

	token, entry := parseGenKey(t, stdout.String())
	if token == "" {
		t.Fatal("no TOKEN= line in output")
	}

	prefix, secret, ok := strings.Cut(token, ".")
	if !ok || prefix == "" || secret == "" {
		t.Fatalf("token %q is not <prefix>.<secret>", token)
	}
	if !strings.HasPrefix(prefix, "ak_") {
		t.Errorf("prefix = %q, want an ak_ prefix", prefix)
	}
	if len(secret) < 32 {
		t.Errorf("secret is %d chars, want >= 32", len(secret))
	}

	// The printed hash must be the sha256 of the printed cleartext secret.
	if entry.Hash != publish.HashSecret(secret) {
		t.Errorf("entry hash = %q, want HashSecret(secret) = %q", entry.Hash, publish.HashSecret(secret))
	}
	if entry.Prefix != prefix {
		t.Errorf("entry prefix = %q, want %q", entry.Prefix, prefix)
	}
	if entry.Author != "ada" {
		t.Errorf("entry author = %q, want ada", entry.Author)
	}
	if len(entry.Scopes) != 1 || entry.Scopes[0] != publish.ScopeWrite {
		t.Errorf("entry scopes = %v, want [%s]", entry.Scopes, publish.ScopeWrite)
	}

	// The secret must NOT appear in the keys-file entry line.
	encoded, _ := json.Marshal(entry)
	if strings.Contains(string(encoded), secret) {
		t.Error("keys entry leaks the secret")
	}

	// The minted entry round-trips through the loader as a valid keys file.
	dir := t.TempDir()
	keysPath := filepath.Join(dir, "keys.json")
	if err := os.WriteFile(keysPath, []byte("["+string(encoded)+"]"), 0o600); err != nil {
		t.Fatalf("write keys file: %v", err)
	}
	entries, err := loadKeys(keysPath)
	if err != nil {
		t.Fatalf("loadKeys on minted entry: %v", err)
	}
	if len(entries) != 1 || entries[0].Prefix != prefix {
		t.Errorf("loaded %d entries (%+v), want the minted one", len(entries), entries)
	}
}

func TestGenKey_CustomScopesAndRequiresAuthor(t *testing.T) {
	t.Run("missing author -> usage error, nothing printed", func(t *testing.T) {
		var stdout, stderr bytes.Buffer
		code := run([]string{"-gen-key"}, envFromMap(nil), &stdout, &stderr)
		if code != exitUsage {
			t.Fatalf("exit = %d, want %d", code, exitUsage)
		}
		if strings.Contains(stdout.String(), "TOKEN=") {
			t.Error("printed a token despite the usage error")
		}
	})

	t.Run("repeatable -scope is honored", func(t *testing.T) {
		var stdout, stderr bytes.Buffer
		code := run([]string{"-gen-key", "-author", "bo", "-scope", "articles:write", "-scope", "articles:admin"},
			envFromMap(nil), &stdout, &stderr)
		if code != exitOK {
			t.Fatalf("exit = %d, want %d (stderr=%q)", code, exitOK, stderr.String())
		}
		_, entry := parseGenKey(t, stdout.String())
		want := []string{"articles:write", "articles:admin"}
		if len(entry.Scopes) != len(want) {
			t.Fatalf("scopes = %v, want %v", entry.Scopes, want)
		}
		for i := range want {
			if entry.Scopes[i] != want[i] {
				t.Errorf("scope[%d] = %q, want %q", i, entry.Scopes[i], want[i])
			}
		}
	})
}

func TestLoadKeys(t *testing.T) {
	write := func(t *testing.T, content string) string {
		t.Helper()
		p := filepath.Join(t.TempDir(), "keys.json")
		if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
			t.Fatalf("write: %v", err)
		}
		return p
	}

	t.Run("valid file is accepted", func(t *testing.T) {
		p := write(t, `[
			{"prefix":"ak_ada","hash":"`+publish.HashSecret("s1")+`","author":"ada","scopes":["articles:write"]},
			{"prefix":"ak_bo","hash":"`+publish.HashSecret("s2")+`","author":"bo","scopes":["articles:write","articles:read"]}
		]`)
		entries, err := loadKeys(p)
		if err != nil {
			t.Fatalf("loadKeys: %v", err)
		}
		if len(entries) != 2 {
			t.Fatalf("got %d entries, want 2", len(entries))
		}
		// buildAuth must yield an Authenticator that accepts the matching token.
		auth := buildAuth(entries)
		id, err := auth.Authenticate("ak_ada.s1")
		if err != nil {
			t.Fatalf("authenticate minted token: %v", err)
		}
		if id.Author != "ada" || !id.HasScope(publish.ScopeWrite) {
			t.Errorf("identity = %+v, want author ada with %s", id, publish.ScopeWrite)
		}
	})

	t.Run("empty path -> error", func(t *testing.T) {
		if _, err := loadKeys("   "); err == nil {
			t.Fatal("want error for empty keys path")
		}
	})

	t.Run("missing file -> error", func(t *testing.T) {
		if _, err := loadKeys(filepath.Join(t.TempDir(), "nope.json")); err == nil {
			t.Fatal("want error for missing file")
		}
	})

	t.Run("malformed JSON -> error", func(t *testing.T) {
		if _, err := loadKeys(write(t, `{not json`)); err == nil {
			t.Fatal("want error for malformed JSON")
		}
	})

	t.Run("empty array -> error", func(t *testing.T) {
		if _, err := loadKeys(write(t, `[]`)); err == nil {
			t.Fatal("want error for empty keys array")
		}
	})

	t.Run("entry missing scopes -> error", func(t *testing.T) {
		p := write(t, `[{"prefix":"ak_x","hash":"`+publish.HashSecret("s")+`","author":"x","scopes":[]}]`)
		if _, err := loadKeys(p); err == nil {
			t.Fatal("want error for entry with no scopes")
		}
	})

	t.Run("entry missing author -> error", func(t *testing.T) {
		p := write(t, `[{"prefix":"ak_x","hash":"`+publish.HashSecret("s")+`","author":"","scopes":["articles:write"]}]`)
		if _, err := loadKeys(p); err == nil {
			t.Fatal("want error for entry with empty author")
		}
	})

	t.Run("unknown field -> error", func(t *testing.T) {
		p := write(t, `[{"prefix":"ak_x","hash":"h","author":"x","scopes":["articles:write"],"secret":"oops"}]`)
		if _, err := loadKeys(p); err == nil {
			t.Fatal("want error for unknown field (strict decode)")
		}
	})

	t.Run("duplicate prefix -> error", func(t *testing.T) {
		p := write(t, `[
			{"prefix":"ak_dup","hash":"`+publish.HashSecret("a")+`","author":"a","scopes":["articles:write"]},
			{"prefix":"ak_dup","hash":"`+publish.HashSecret("b")+`","author":"b","scopes":["articles:write"]}
		]`)
		if _, err := loadKeys(p); err == nil {
			t.Fatal("want error for duplicate prefix")
		}
	})
}

func TestFlagEnvPrecedence(t *testing.T) {
	env := envFromMap(map[string]string{
		"CENSURADO_PUBLISH_ADDR":            "0.0.0.0:9000",
		"CENSURADO_DB":                      "/data/env.db",
		"CENSURADO_PUBLISH_KEYS_FILE":       "/etc/env-keys.json",
		"CENSURADO_PUBLISH_RATE":            "12",
		"CENSURADO_PUBLISH_BURST":           "30",
		"CENSURADO_PUBLISH_MAX_BODY":        "33554432",
		"CENSURADO_PUBLISH_BATCH_MAX_ITEMS": "250",
	})

	t.Run("env supplies defaults", func(t *testing.T) {
		fs, f := newFlagSet(env, &bytes.Buffer{})
		if err := fs.Parse(nil); err != nil {
			t.Fatalf("parse: %v", err)
		}
		if *f.addr != "0.0.0.0:9000" {
			t.Errorf("addr = %q, want env value", *f.addr)
		}
		if *f.db != "/data/env.db" {
			t.Errorf("db = %q, want env value", *f.db)
		}
		if *f.keys != "/etc/env-keys.json" {
			t.Errorf("keys = %q, want env value", *f.keys)
		}
		if *f.rate != 12 {
			t.Errorf("rate = %v, want 12 from env", *f.rate)
		}
		if *f.burst != 30 {
			t.Errorf("burst = %v, want 30 from env", *f.burst)
		}
		if *f.maxBody != 33554432 {
			t.Errorf("maxBody = %v, want 33554432 from env", *f.maxBody)
		}
		if *f.maxItems != 250 {
			t.Errorf("maxItems = %v, want 250 from env", *f.maxItems)
		}
	})

	t.Run("explicit flags override env", func(t *testing.T) {
		fs, f := newFlagSet(env, &bytes.Buffer{})
		err := fs.Parse([]string{"-addr", "127.0.0.1:7777", "-rate", "3", "-burst", "4"})
		if err != nil {
			t.Fatalf("parse: %v", err)
		}
		if *f.addr != "127.0.0.1:7777" {
			t.Errorf("addr = %q, want flag override", *f.addr)
		}
		if *f.rate != 3 {
			t.Errorf("rate = %v, want flag override 3", *f.rate)
		}
		if *f.burst != 4 {
			t.Errorf("burst = %v, want flag override 4", *f.burst)
		}
		// keys was not overridden, so it stays at the env default.
		if *f.keys != "/etc/env-keys.json" {
			t.Errorf("keys = %q, want env value (not overridden)", *f.keys)
		}
	})

	t.Run("builtin defaults when env empty", func(t *testing.T) {
		fs, f := newFlagSet(envFromMap(nil), &bytes.Buffer{})
		if err := fs.Parse(nil); err != nil {
			t.Fatalf("parse: %v", err)
		}
		if *f.addr != "127.0.0.1:8080" {
			t.Errorf("addr default = %q, want 127.0.0.1:8080 (private)", *f.addr)
		}
		if *f.db != "./censurado.db" {
			t.Errorf("db default = %q, want ./censurado.db", *f.db)
		}
		if *f.rate != 5 || *f.burst != 10 {
			t.Errorf("limiter defaults = %v/%v, want 5/10", *f.rate, *f.burst)
		}
		if *f.maxBody != 8<<20 {
			t.Errorf("maxBody default = %v, want %v (8 MiB)", *f.maxBody, 8<<20)
		}
		if *f.maxItems != 500 {
			t.Errorf("maxItems default = %v, want 500", *f.maxItems)
		}
	})
}

func TestRun_BadLimiterAndMissingKeys(t *testing.T) {
	t.Run("non-positive rate -> config error", func(t *testing.T) {
		var stdout, stderr bytes.Buffer
		code := run([]string{"-rate", "0", "-keys", "/tmp/whatever.json"}, envFromMap(nil), &stdout, &stderr)
		if code != exitConfig {
			t.Fatalf("exit = %d, want %d", code, exitConfig)
		}
	})

	t.Run("missing keys file -> config error before binding", func(t *testing.T) {
		var stdout, stderr bytes.Buffer
		code := run(nil, envFromMap(nil), &stdout, &stderr)
		if code != exitConfig {
			t.Fatalf("exit = %d, want %d (no keys configured)", code, exitConfig)
		}
		if !strings.Contains(stderr.String(), "config:") {
			t.Errorf("stderr = %q, want a clear config error", stderr.String())
		}
	})
}
