package main

import (
	"bytes"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hec-ovi/censurado-web-backend/internal/publish"
	"github.com/hec-ovi/censurado-web-backend/store/sqlite"
)

const validArticle = `{"title":"Hello","body":"b","author":"ada","section":"tech"}`

func newServer(t *testing.T) *httptest.Server {
	t.Helper()
	repo, err := sqlite.Open(filepath.Join(t.TempDir(), "cli.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = repo.Close() })
	auth := publish.NewStaticKeyAuth()
	auth.Add("ak_ada", publish.HashSecret("sekret"), "ada", publish.ScopeWrite)
	srv := httptest.NewServer(publish.NewHandler(repo, repo, auth, nil))
	t.Cleanup(srv.Close)
	return srv
}

func TestCLI_DryRunValidatesWithoutServer(t *testing.T) {
	var out, errb bytes.Buffer
	code := run([]string{"--dry-run"}, strings.NewReader(validArticle), &out, &errb)
	if code != exitOK {
		t.Fatalf("code = %d, want 0 (%s)", code, errb.String())
	}
	if !strings.Contains(out.String(), "slug") {
		t.Errorf("dry-run output missing slug: %s", out.String())
	}
}

func TestCLI_LocalValidationFailsBeforeNetwork(t *testing.T) {
	var out, errb bytes.Buffer
	code := run([]string{"--url", "http://127.0.0.1:0"}, strings.NewReader(`{"body":"b","author":"ada","section":"tech"}`), &out, &errb)
	if code != exitValidation {
		t.Fatalf("code = %d, want 3", code)
	}
	if !strings.Contains(errb.String(), "title") {
		t.Errorf("expected a title validation error, got %s", errb.String())
	}
}

func TestCLI_MissingTokenIsUsageError(t *testing.T) {
	srv := newServer(t)
	t.Setenv("CENSURADO_PUBLISH_TOKEN", "")
	var out, errb bytes.Buffer
	code := run([]string{"--url", srv.URL}, strings.NewReader(validArticle), &out, &errb)
	if code != exitAuth {
		t.Fatalf("code = %d, want 2", code)
	}
}

func TestCLI_BadTokenRejected(t *testing.T) {
	srv := newServer(t)
	t.Setenv("CENSURADO_PUBLISH_TOKEN", "ak_ada.wrong")
	var out, errb bytes.Buffer
	code := run([]string{"--url", srv.URL}, strings.NewReader(validArticle), &out, &errb)
	if code != exitAuth {
		t.Fatalf("code = %d, want 2 (%s)", code, errb.String())
	}
}

func TestCLI_PublishesAndReplaysIdempotently(t *testing.T) {
	srv := newServer(t)
	t.Setenv("CENSURADO_PUBLISH_TOKEN", "ak_ada.sekret")

	var out, errb bytes.Buffer
	code := run([]string{"--url", srv.URL, "--idempotency-key", "fixed-1"}, strings.NewReader(validArticle), &out, &errb)
	if code != exitOK {
		t.Fatalf("publish code = %d, want 0 (%s)", code, errb.String())
	}
	if !strings.Contains(out.String(), `"slug"`) {
		t.Errorf("response missing slug: %s", out.String())
	}

	// Same payload + same key -> idempotent replay, still success.
	var out2, errb2 bytes.Buffer
	code = run([]string{"--url", srv.URL, "--idempotency-key", "fixed-1"}, strings.NewReader(validArticle), &out2, &errb2)
	if code != exitOK {
		t.Fatalf("replay code = %d, want 0 (%s)", code, errb2.String())
	}
}
