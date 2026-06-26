package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/hec-ovi/censurado-web-backend/internal/adminweb"
	"github.com/hec-ovi/censurado-web-backend/domain"
)

// TestBuildPublish_Disabled proves the manual-publish action is off (nil closure)
// unless BOTH the publish URL and an operator token are supplied, so adminweb stays
// in its disabled state instead of POSTing to nowhere.
func TestBuildPublish_Disabled(t *testing.T) {
	cases := []map[string]string{
		nil,
		{"CENSURADO_ADMIN_PUBLISH_URL": "http://publish:8080"},                       // no token
		{"CENSURADO_ADMIN_PUBLISH_TOKEN": "ak_op.secret"},                            // no url
		{"CENSURADO_ADMIN_PUBLISH_URL": "  ", "CENSURADO_ADMIN_PUBLISH_TOKEN": "  "}, // blank
	}
	for i, env := range cases {
		if buildPublish(envFromMap(env)) != nil {
			t.Errorf("case %d: buildPublish = non-nil, want nil (disabled)", i)
		}
	}
}

// TestBuildPublish_PostsAndMapsSuccess proves the closure shapes the request like
// the CLI (POST /articles, Bearer token, Idempotency-Key, JSON body threading every
// field) and maps a 201 to a Created result.
func TestBuildPublish_PostsAndMapsSuccess(t *testing.T) {
	var gotMethod, gotPath, gotAuth, gotIdem, gotCType string
	var got domain.PublishInput
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod, gotPath = r.Method, r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		gotIdem = r.Header.Get("Idempotency-Key")
		gotCType = r.Header.Get("Content-Type")
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &got)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"id":"a1","slug":"the-slug"}`))
	}))
	defer srv.Close()

	pub := buildPublish(envFromMap(map[string]string{
		"CENSURADO_ADMIN_PUBLISH_URL":   srv.URL + "/", // trailing slash trimmed
		"CENSURADO_ADMIN_PUBLISH_TOKEN": "ak_op.secret",
	}))
	if pub == nil {
		t.Fatal("buildPublish = nil with url+token set")
	}

	when := time.Date(2026, 6, 20, 8, 30, 0, 0, time.UTC)
	res, err := pub(context.Background(), adminweb.CreateArticleInput{
		Title: "T", Body: "B", Author: "zoe", Section: "world",
		Topics: []string{"go", "ai"}, PublishedAt: &when,
		Metadata: map[string]any{"image": "https://x/y.jpg"},
	})
	if err != nil {
		t.Fatalf("publish closure: %v", err)
	}
	if res.ID != "a1" || res.Slug != "the-slug" || !res.Created {
		t.Errorf("result = %+v, want a1/the-slug/created=true", res)
	}

	if gotMethod != http.MethodPost || gotPath != "/articles" {
		t.Errorf("method/path = %s %s, want POST /articles", gotMethod, gotPath)
	}
	if gotAuth != "Bearer ak_op.secret" {
		t.Errorf("Authorization = %q, want the bearer token", gotAuth)
	}
	if gotIdem == "" {
		t.Errorf("Idempotency-Key header missing")
	}
	if !strings.HasPrefix(gotCType, "application/json") {
		t.Errorf("Content-Type = %q, want application/json", gotCType)
	}
	if got.Title != "T" || got.Author != "zoe" || got.Section != "world" {
		t.Errorf("decoded body fields wrong: %+v", got)
	}
	if got.PublishedAt == nil || !got.PublishedAt.Equal(when) {
		t.Errorf("published_at not threaded through: %v", got.PublishedAt)
	}
	if got.Metadata["image"] != "https://x/y.jpg" {
		t.Errorf("metadata not threaded through: %v", got.Metadata)
	}
	if strings.Join(got.Topics, ",") != "go,ai" {
		t.Errorf("topics not threaded through: %v", got.Topics)
	}
}

// TestBuildPublish_Maps200AsDedup proves an HTTP 200 (idempotent replay or
// content-hash dedup) maps to Created=false, the whole point of the Created flag.
func TestBuildPublish_Maps200AsDedup(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"id":"a1","slug":"dup"}`))
	}))
	defer srv.Close()
	pub := buildPublish(envFromMap(map[string]string{
		"CENSURADO_ADMIN_PUBLISH_URL":   srv.URL,
		"CENSURADO_ADMIN_PUBLISH_TOKEN": "ak.sec",
	}))
	res, err := pub(context.Background(), adminweb.CreateArticleInput{})
	if err != nil {
		t.Fatalf("closure: %v", err)
	}
	if res.Created {
		t.Errorf("200 must map to Created=false (idempotent/dedup replay)")
	}
	if res.ID != "a1" || res.Slug != "dup" {
		t.Errorf("result = %+v, want a1/dup threaded through", res)
	}
}

// TestBuildPublish_IdempotencyKeyUniquePerCall proves each manual create carries a
// fresh idempotency key. A constant key would make every second real publish either
// 422-conflict or silently replay the first article.
func TestBuildPublish_IdempotencyKeyUniquePerCall(t *testing.T) {
	var keys []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		keys = append(keys, r.Header.Get("Idempotency-Key"))
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"id":"a","slug":"s"}`))
	}))
	defer srv.Close()
	pub := buildPublish(envFromMap(map[string]string{
		"CENSURADO_ADMIN_PUBLISH_URL":   srv.URL,
		"CENSURADO_ADMIN_PUBLISH_TOKEN": "ak.sec",
	}))
	for i := 0; i < 2; i++ {
		if _, err := pub(context.Background(), adminweb.CreateArticleInput{Title: "t"}); err != nil {
			t.Fatalf("call %d: %v", i, err)
		}
	}
	if len(keys) != 2 || keys[0] == "" || keys[1] == "" {
		t.Fatalf("captured keys = %v, want two non-empty", keys)
	}
	if keys[0] == keys[1] {
		t.Errorf("Idempotency-Key reused across calls (%q); each create must be unique", keys[0])
	}
	if len(keys[0]) != 32 {
		t.Errorf("Idempotency-Key length = %d, want 32 hex chars", len(keys[0]))
	}
}

// TestBuildPublish_400Branching proves a protocol 400 (no field map) is a generic
// server error, while a 400 that carries field errors is still inline validation.
func TestBuildPublish_400Branching(t *testing.T) {
	t.Run("protocol fault -> generic error", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/problem+json")
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"status":400,"code":"missing_idempotency_key","detail":"Idempotency-Key header is required"}`))
		}))
		defer srv.Close()
		pub := buildPublish(envFromMap(map[string]string{
			"CENSURADO_ADMIN_PUBLISH_URL":   srv.URL,
			"CENSURADO_ADMIN_PUBLISH_TOKEN": "ak.sec",
		}))
		_, err := pub(context.Background(), adminweb.CreateArticleInput{})
		var ve *adminweb.CreateValidationError
		if errors.As(err, &ve) {
			t.Fatalf("protocol 400 mapped to a validation error; want a generic server error: %v", err)
		}
		if err == nil || !strings.Contains(err.Error(), "Idempotency-Key header is required") {
			t.Errorf("err = %v, want the 400 detail in a server error", err)
		}
	})

	t.Run("with fields -> validation error", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/problem+json")
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"status":400,"code":"validation_failed","fields":{"title":"required"}}`))
		}))
		defer srv.Close()
		pub := buildPublish(envFromMap(map[string]string{
			"CENSURADO_ADMIN_PUBLISH_URL":   srv.URL,
			"CENSURADO_ADMIN_PUBLISH_TOKEN": "ak.sec",
		}))
		_, err := pub(context.Background(), adminweb.CreateArticleInput{})
		var ve *adminweb.CreateValidationError
		if !errors.As(err, &ve) || ve.Fields["title"] != "required" {
			t.Fatalf("err = %v, want a CreateValidationError with title:required", err)
		}
	})
}

// TestBuildPublish_MapsErrors covers the two error classes the form depends on: a
// 422 field map becomes a typed CreateValidationError (rendered inline), while a
// 403 becomes a generic server error (rendered as a top-level message, not inline).
func TestBuildPublish_MapsErrors(t *testing.T) {
	t.Run("422 -> validation error with fields", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/problem+json")
			w.WriteHeader(http.StatusUnprocessableEntity)
			_, _ = w.Write([]byte(`{"status":422,"code":"validation_failed","fields":{"title":"required"}}`))
		}))
		defer srv.Close()
		pub := buildPublish(envFromMap(map[string]string{
			"CENSURADO_ADMIN_PUBLISH_URL":   srv.URL,
			"CENSURADO_ADMIN_PUBLISH_TOKEN": "ak.sec",
		}))
		_, err := pub(context.Background(), adminweb.CreateArticleInput{})
		var ve *adminweb.CreateValidationError
		if !errors.As(err, &ve) {
			t.Fatalf("err = %v, want *adminweb.CreateValidationError", err)
		}
		if ve.Fields["title"] != "required" {
			t.Errorf("fields = %v, want title:required", ve.Fields)
		}
	})

	t.Run("403 -> generic server error", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/problem+json")
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(`{"status":403,"code":"author_mismatch","detail":"author must match the authenticated key"}`))
		}))
		defer srv.Close()
		pub := buildPublish(envFromMap(map[string]string{
			"CENSURADO_ADMIN_PUBLISH_URL":   srv.URL,
			"CENSURADO_ADMIN_PUBLISH_TOKEN": "ak.sec",
		}))
		_, err := pub(context.Background(), adminweb.CreateArticleInput{})
		var ve *adminweb.CreateValidationError
		if errors.As(err, &ve) {
			t.Fatalf("403 mapped to a validation error; want a generic server error: %v", err)
		}
		if err == nil || !strings.Contains(err.Error(), "author must match") {
			t.Errorf("err = %v, want it to carry the 403 detail", err)
		}
	})
}

// TestBuildUploadMedia_Disabled proves the upload proxy is off (nil) unless BOTH the
// publish URL and an operator token are set, mirroring buildPublish.
func TestBuildUploadMedia_Disabled(t *testing.T) {
	for i, env := range []map[string]string{
		nil,
		{"CENSURADO_ADMIN_PUBLISH_URL": "http://publish:8080"},
		{"CENSURADO_ADMIN_PUBLISH_TOKEN": "ak_op.secret"},
	} {
		if buildUploadMedia(envFromMap(env)) != nil {
			t.Errorf("case %d: buildUploadMedia = non-nil, want nil (disabled)", i)
		}
	}
}

// TestBuildUploadMedia_PostsRawBytesAndReturnsURL proves the closure POSTs the image
// bytes raw to /media with the operator bearer token and returns the stored URL.
func TestBuildUploadMedia_PostsRawBytesAndReturnsURL(t *testing.T) {
	var gotMethod, gotPath, gotAuth, gotCType string
	var gotBody []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod, gotPath = r.Method, r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		gotCType = r.Header.Get("Content-Type")
		gotBody, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"name":"abc.png","url":"/media/abc.png","content_type":"image/png"}`))
	}))
	defer srv.Close()

	up := buildUploadMedia(envFromMap(map[string]string{
		"CENSURADO_ADMIN_PUBLISH_URL":   srv.URL + "/", // trailing slash trimmed
		"CENSURADO_ADMIN_PUBLISH_TOKEN": "ak_op.secret",
	}))
	if up == nil {
		t.Fatal("buildUploadMedia = nil with url+token set")
	}
	data := []byte("\x89PNG\r\n\x1a\nrest")
	url, err := up(context.Background(), "shot.png", "image/png", data)
	if err != nil {
		t.Fatalf("upload closure: %v", err)
	}
	if url != "/media/abc.png" {
		t.Errorf("url = %q, want /media/abc.png", url)
	}
	if gotMethod != http.MethodPost || gotPath != "/media" {
		t.Errorf("method/path = %s %s, want POST /media", gotMethod, gotPath)
	}
	if gotAuth != "Bearer ak_op.secret" {
		t.Errorf("Authorization = %q, want the bearer token", gotAuth)
	}
	if gotCType != "image/png" {
		t.Errorf("Content-Type = %q, want image/png", gotCType)
	}
	if !bytes.Equal(gotBody, data) {
		t.Errorf("posted body differs from the image bytes")
	}
}

// TestBuildUploadMedia_NonCreatedIsError proves a non-201 media response (e.g. an
// unsupported type) surfaces as an error rather than a bogus URL.
func TestBuildUploadMedia_NonCreatedIsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnsupportedMediaType)
		_, _ = w.Write([]byte(`{"status":415,"code":"unsupported_media_type"}`))
	}))
	defer srv.Close()
	up := buildUploadMedia(envFromMap(map[string]string{
		"CENSURADO_ADMIN_PUBLISH_URL":   srv.URL,
		"CENSURADO_ADMIN_PUBLISH_TOKEN": "ak.sec",
	}))
	if _, err := up(context.Background(), "x.png", "image/png", []byte("x")); err == nil {
		t.Errorf("a non-201 media response must be an error")
	}
}
