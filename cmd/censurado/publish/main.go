// Command censurado-publish serves the authenticated publish API: the only write
// path into the system (POST /articles). It binds to localhost by default because
// the publish API is meant to sit off the public internet, reachable only by the
// agents that hold a key (over a private network or an SSH tunnel).
//
// Keys live in a JSON file (-keys / CENSURADO_PUBLISH_KEYS_FILE), an array of
// entries that carry only the SHA-256 of each secret, never the secret itself:
//
//	[ {"prefix":"ak_ada","hash":"<hex sha256 of the secret>","author":"ada","scopes":["articles:write"]} ]
//
// Mint a key with -gen-key -author NAME: it prints the agent TOKEN once (it cannot
// be recovered) and the ready-to-paste keys-file entry (no secret in it). Hand the
// TOKEN to the agent as CENSURADO_PUBLISH_TOKEN and add the entry to the keys file.
package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/hec-ovi/censurado-web-backend/internal/adminweb"
	"github.com/hec-ovi/censurado-web-backend/internal/publish"
	"github.com/hec-ovi/censurado-web-backend/media"
	"github.com/hec-ovi/censurado-web-backend/store"
	"github.com/hec-ovi/censurado-web-backend/store/sqlite"
)

// Exit codes are distinct per failure class so a supervisor can tell a usage
// mistake from a config error, a database fault, or a serve failure.
const (
	exitOK     = 0
	exitUsage  = 2 // flag parse error or bad usage (missing -author for -gen-key)
	exitConfig = 3 // missing/invalid keys file or limiter settings
	exitDB     = 4 // store open failure
	exitServe  = 5 // listen/serve/shutdown failure
	exitGenErr = 1 // entropy failure while minting a key
)

func main() { os.Exit(run(os.Args[1:], os.Getenv, os.Stdout, os.Stderr)) }

// stringSlice collects a repeatable flag (e.g. -scope a -scope b) into a list.
type stringSlice []string

func (s *stringSlice) String() string { return strings.Join(*s, ",") }
func (s *stringSlice) Set(v string) error {
	*s = append(*s, v)
	return nil
}

// cliFlags holds the parsed flag pointers so run and the tests share one wiring.
type cliFlags struct {
	addr     *string
	db       *string
	keys     *string
	archive  *string
	mediaDir *string
	rate     *float64
	burst    *int
	maxBody  *int
	maxItems *int
	genKey   *bool
	author   *string
	scopes   *stringSlice

	panelSessionKey *string
	panelLoginHash  *string
	panelLoginToken *string
	panelSecure     *bool
}

// newFlagSet builds the flag set with env-derived defaults. It is factored out so a
// test can parse args and inspect the resolved config without binding a socket.
func newFlagSet(getenv func(string) string, stderr io.Writer) (*flag.FlagSet, *cliFlags) {
	fs := flag.NewFlagSet("censurado-publish", flag.ContinueOnError)
	fs.SetOutput(stderr)

	var scopes stringSlice
	f := &cliFlags{
		addr:     fs.String("addr", envOr(getenv, "CENSURADO_PUBLISH_ADDR", "127.0.0.1:8080"), "listen address (or CENSURADO_PUBLISH_ADDR); localhost by default"),
		db:       fs.String("db", envOr(getenv, "CENSURADO_DB", "./censurado.db"), "sqlite database path (or CENSURADO_DB)"),
		keys:     fs.String("keys", envOr(getenv, "CENSURADO_PUBLISH_KEYS_FILE", ""), "path to the JSON keys file (or CENSURADO_PUBLISH_KEYS_FILE); required to serve"),
		archive:  fs.String("payload-archive", envOr(getenv, "CENSURADO_PUBLISH_ARCHIVE", ""), "directory to append accepted raw payloads to for replay recovery (or CENSURADO_PUBLISH_ARCHIVE); empty = no archiving"),
		mediaDir: fs.String("media-dir", envOr(getenv, "CENSURADO_MEDIA_DIR", ""), "directory for the self-hosted image store / CDN, enabling POST /media and GET /media/{name} (or CENSURADO_MEDIA_DIR); empty = media disabled"),
		rate:     fs.Float64("rate", envFloat(getenv, "CENSURADO_PUBLISH_RATE", 5), "per-key request rate, tokens/sec (or CENSURADO_PUBLISH_RATE)"),
		burst:    fs.Int("burst", envInt(getenv, "CENSURADO_PUBLISH_BURST", 10), "per-key burst size (or CENSURADO_PUBLISH_BURST)"),
		maxBody:  fs.Int("max-body", envInt(getenv, "CENSURADO_PUBLISH_MAX_BODY", 8<<20), "request body byte cap, a transport safeguard not a content limit (or CENSURADO_PUBLISH_MAX_BODY)"),
		maxItems: fs.Int("max-batch-items", envInt(getenv, "CENSURADO_PUBLISH_BATCH_MAX_ITEMS", 500), "max articles per POST /articles:batch (or CENSURADO_PUBLISH_BATCH_MAX_ITEMS)"),
		genKey:   fs.Bool("gen-key", false, "mint a fresh key, print the token + keys-file entry, and exit without serving"),
		author:   fs.String("author", "", "author the minted key publishes as (required with -gen-key)"),

		panelSessionKey: fs.String("panel-session-key", envOr(getenv, "PANEL_SESSION_KEY", ""), "HMAC key (hex or base64) signing the admin panel session + CSRF cookies (or PANEL_SESSION_KEY); with -panel-login-token-hash, enables the folded-in admin panel"),
		panelLoginHash:  fs.String("panel-login-token-hash", envOr(getenv, "PANEL_LOGIN_TOKEN_HASH", ""), "hex SHA-256 of the admin panel login token (or PANEL_LOGIN_TOKEN_HASH); with -panel-session-key, enables the panel"),
		panelLoginToken: fs.String("panel-login-token", envOr(getenv, "PANEL_LOGIN_TOKEN", ""), "cleartext login token for local-box form prefill only (or PANEL_LOGIN_TOKEN); leave empty on any shared deployment"),
		panelSecure:     fs.Bool("panel-secure-cookies", envBool(getenv, "PANEL_SECURE_COOKIES", false), "set the Secure attribute on the panel cookies (or PANEL_SECURE_COOKIES); enable behind TLS"),
	}
	fs.Var(&scopes, "scope", "scope to grant the minted key; repeatable (default articles:write)")
	f.scopes = &scopes

	fs.Usage = func() {
		fmt.Fprintln(stderr, "censurado-publish: the authenticated publish API server (POST /articles).")
		fmt.Fprintln(stderr, "\nUsage:")
		fmt.Fprintln(stderr, "  censurado-publish [flags]                  serve the API")
		fmt.Fprintln(stderr, "  censurado-publish -gen-key -author NAME    mint a key and exit")
		fmt.Fprintln(stderr, "\nThe keys file (-keys / CENSURADO_PUBLISH_KEYS_FILE) is a JSON array:")
		fmt.Fprintln(stderr, `  [ {"prefix":"ak_ada","hash":"<hex sha256 of the secret>","author":"ada","scopes":["articles:write"]} ]`)
		fmt.Fprintln(stderr, "It carries only secret hashes, never secrets. Mint entries with -gen-key.")
		fmt.Fprintln(stderr, "\nSet -payload-archive DIR (or CENSURADO_PUBLISH_ARCHIVE) to append every accepted")
		fmt.Fprintln(stderr, "payload to DIR. After a restore, replay payloads received past the restore point")
		fmt.Fprintln(stderr, "with censurado-replay to recover writes lost in the replication window. DIR holds")
		fmt.Fprintln(stderr, "article content (files 0600); access-control it and ship it to durable storage.")
		fmt.Fprintln(stderr, "\nFlags:")
		fs.PrintDefaults()
	}

	return fs, f
}

func run(args []string, getenv func(string) string, stdout, stderr io.Writer) int {
	fs, f := newFlagSet(getenv, stderr)
	if err := fs.Parse(args); err != nil {
		return exitUsage
	}

	// -gen-key is mutually exclusive with serving: mint and exit.
	if *f.genKey {
		scopes := append([]string(nil), *f.scopes...)
		if len(scopes) == 0 {
			scopes = []string{publish.ScopeWrite}
		}
		if strings.TrimSpace(*f.author) == "" {
			fmt.Fprintln(stderr, "gen-key: -author is required")
			return exitUsage
		}
		if err := genKey(stdout, *f.author, scopes); err != nil {
			fmt.Fprintln(stderr, "gen-key:", err)
			return exitGenErr
		}
		return exitOK
	}

	if *f.rate <= 0 {
		fmt.Fprintln(stderr, "config: -rate must be > 0")
		return exitConfig
	}
	if *f.burst < 1 {
		fmt.Fprintln(stderr, "config: -burst must be >= 1")
		return exitConfig
	}

	entries, err := loadKeys(*f.keys)
	if err != nil {
		fmt.Fprintln(stderr, "config:", err)
		return exitConfig
	}
	auth := buildAuth(entries)

	repo, err := sqlite.Open(*f.db)
	if err != nil {
		fmt.Fprintf(stderr, "open db: %v\n", err)
		return exitDB
	}
	defer repo.Close()

	// The same *sqlite.Store is both the article Repository and the SubmissionLog.
	// WithLimits applies the configurable transport safeguards (body byte cap and
	// per-batch item cap); neither is a content-length cap on an article body.
	h := publish.NewHandler(repo, repo, auth, time.Now).WithLimits(*f.maxBody, *f.maxItems)

	// Optional payload archive: when configured, every newly created publish is
	// appended to this directory so an operator can replay payloads received after a
	// restore point (see cmd/censurado/replay) and close the replication-window RPO.
	archiveDir := strings.TrimSpace(*f.archive)
	if archiveDir != "" {
		arch, err := publish.NewDirArchive(archiveDir)
		if err != nil {
			fmt.Fprintln(stderr, "config:", err)
			return exitConfig
		}
		h = h.WithArchive(arch)
	}

	// Optional self-hosted image store / CDN: when a media dir is configured, mount
	// POST /media (authenticated upload, same articles:write scope) and
	// GET /media/{name} (public, immutable read). Empty leaves media off.
	var mediaH *publish.MediaHandler
	mediaDir := strings.TrimSpace(*f.mediaDir)
	if mediaDir != "" {
		mstore, err := media.NewStore(mediaDir, media.DefaultMaxBytes)
		if err != nil {
			fmt.Fprintln(stderr, "config:", err)
			return exitConfig
		}
		mediaH = publish.NewMediaHandler(mstore, auth)
	}

	// The same *sqlite.Store backs the authenticated JSON read API (GET /authors,
	// /topics, /articles, /articles/{slug}): it satisfies the Repository plus the
	// author and topic registries, so a CLI and the brain can list and mirror.
	readH := publish.NewReadHandler(repo, auth)

	// ...and the operator mutation lane (authors/topics CRUD + article edit/delete/
	// restore) behind the admin:write scope. This server is a pure data/API service;
	// building the static site is the censurado-web generator's job.
	opH := publish.NewOperatorHandler(repo, auth, time.Now)

	limiter := publish.NewRateLimiter(*f.rate, *f.burst, time.Now)
	handler := publish.NewServerHandler(h, limiter, mediaH, readH, opH)

	// Fold in the operator admin panel: when a session key + login hash are set, the
	// same server also serves the buildless SPA and its signed-cookie login, so the
	// backend is one deployable (API + admin UI + data). A valid session maps to the
	// operator identity in-process; the bearer API lanes are untouched. Unset =
	// pure-API deployment, byte-identical to before.
	panelKey, keyOK := adminweb.DecodeKey(*f.panelSessionKey)
	if *f.panelSessionKey != "" && !keyOK {
		fmt.Fprintln(stderr, "config: -panel-session-key is not valid hex or base64")
		return exitConfig
	}
	handler, panelOn := adminweb.Handler(adminweb.Config{
		SessionKey:     panelKey,
		LoginTokenHash: *f.panelLoginHash,
		LoginToken:     *f.panelLoginToken,
		SecureCookies:  *f.panelSecure,
		// The panel reads its own chrome strings from panel_text and its section labels
		// from the shared frontend_text rows, both injected into the SPA shell.
		PanelText: func(ctx context.Context, lang string) (map[string]string, error) {
			return repo.Text(ctx, store.ScopePanel, lang)
		},
		SectionLabels: func(ctx context.Context, lang string) (map[string]string, error) {
			front, err := repo.Text(ctx, store.ScopeFrontend, lang)
			if err != nil {
				return nil, err
			}
			return adminweb.SectionLabelsFromFrontend(front), nil
		},
	}, handler)

	srv := newServer(*f.addr, handler)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// Log the bind address and key count only, never tokens or hashes, then serve
	// until a signal or a listener failure.
	listen := func() error {
		archiveNote := "off"
		if archiveDir != "" {
			archiveNote = archiveDir
		}
		mediaNote := "off"
		if mediaDir != "" {
			mediaNote = mediaDir
		}
		panelNote := "off"
		if panelOn {
			panelNote = "on"
		}
		fmt.Fprintf(stdout, "censurado-publish listening on %s (%d key(s) loaded, payload archive: %s, media store: %s, admin panel: %s)\n", *f.addr, len(entries), archiveNote, mediaNote, panelNote)
		return srv.ListenAndServe()
	}
	return serve(ctx, stop, srv, listen, shutdownGrace, stderr)
}

// shutdownGrace bounds how long Shutdown waits for in-flight requests to drain
// before the process exits.
const shutdownGrace = 10 * time.Second

// newServer constructs the publish HTTP server with bounded timeouts. ReadHeaderTimeout
// bounds the header phase; ReadTimeout additionally bounds the BODY phase, which
// ReadHeaderTimeout does NOT, closing the body-trickle slowloris that a header-only
// guard leaves open; WriteTimeout bounds the response phase; IdleTimeout bounds idle
// keep-alive connections. The read budget is generous for the 8 MiB body cap even on a
// slow link (30s tolerates well under 1 KiB/s before a trickle is cut off).
func newServer(addr string, handler http.Handler) *http.Server {
	return &http.Server{
		Addr:              addr,
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
	}
}

// serve runs srv until ctx is cancelled (a SIGINT/SIGTERM signal) or the listener
// fails, then shuts down gracefully within shutdownTimeout so in-flight publishes
// drain instead of being dropped. listen is the serving call (srv.ListenAndServe in
// production; a test injects an ephemeral listener so the lifecycle is exercised
// without binding a fixed socket). It returns the process exit code: exitOK on a clean
// shutdown or a benign ErrServerClosed, exitServe on a Shutdown error or a listener
// failure. stop restores default signal handling so a second signal during the drain
// window terminates forcefully.
func serve(ctx context.Context, stop func(), srv *http.Server, listen func() error, shutdownTimeout time.Duration, stderr io.Writer) int {
	errCh := make(chan error, 1)
	go func() { errCh <- listen() }()

	select {
	case <-ctx.Done():
		stop()
		shutCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
		defer cancel()
		if err := srv.Shutdown(shutCtx); err != nil {
			fmt.Fprintln(stderr, "shutdown:", err)
			return exitServe
		}
		return exitOK
	case err := <-errCh:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			fmt.Fprintln(stderr, "serve:", err)
			return exitServe
		}
		return exitOK
	}
}

// keyEntry is one record in the keys file. It never holds a secret, only its hash.
type keyEntry struct {
	Prefix string   `json:"prefix"`
	Hash   string   `json:"hash"`
	Author string   `json:"author"`
	Scopes []string `json:"scopes"`
}

// loadKeys reads and validates the keys file. A missing, empty, or malformed file
// is a fatal config error: the server has no usable credentials without it.
func loadKeys(path string) ([]keyEntry, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return nil, errors.New("no keys file configured: set -keys or CENSURADO_PUBLISH_KEYS_FILE")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read keys file: %w", err)
	}

	var entries []keyEntry
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&entries); err != nil {
		return nil, fmt.Errorf("parse keys file %s: %w", path, err)
	}
	if len(entries) == 0 {
		return nil, fmt.Errorf("keys file %s contains no keys", path)
	}

	seen := make(map[string]bool, len(entries))
	for i, e := range entries {
		if strings.TrimSpace(e.Prefix) == "" {
			return nil, fmt.Errorf("keys[%d]: empty prefix", i)
		}
		if strings.TrimSpace(e.Hash) == "" {
			return nil, fmt.Errorf("keys[%d] (%s): empty hash", i, e.Prefix)
		}
		if strings.TrimSpace(e.Author) == "" {
			return nil, fmt.Errorf("keys[%d] (%s): empty author", i, e.Prefix)
		}
		if len(e.Scopes) == 0 {
			return nil, fmt.Errorf("keys[%d] (%s): needs at least one scope", i, e.Prefix)
		}
		for _, s := range e.Scopes {
			if strings.TrimSpace(s) == "" {
				return nil, fmt.Errorf("keys[%d] (%s): empty scope", i, e.Prefix)
			}
		}
		if seen[e.Prefix] {
			return nil, fmt.Errorf("keys[%d]: duplicate prefix %q", i, e.Prefix)
		}
		seen[e.Prefix] = true
	}
	return entries, nil
}

// buildAuth registers every validated entry with a StaticKeyAuth.
func buildAuth(entries []keyEntry) *publish.StaticKeyAuth {
	auth := publish.NewStaticKeyAuth()
	for _, e := range entries {
		auth.Add(e.Prefix, e.Hash, e.Author, e.Scopes...)
	}
	return auth
}

// genKey mints a fresh secret, derives a prefix, and prints the agent token once
// plus the keys-file entry (which carries the hash, never the secret).
func genKey(stdout io.Writer, author string, scopes []string) error {
	secretBytes := make([]byte, 32)
	if _, err := rand.Read(secretBytes); err != nil {
		return err
	}
	// RawURLEncoding of 32 bytes is a 43-char, URL-safe secret containing no ".",
	// so it never collides with the "<prefix>.<secret>" token separator.
	secret := base64.RawURLEncoding.EncodeToString(secretBytes)

	prefixBytes := make([]byte, 4)
	if _, err := rand.Read(prefixBytes); err != nil {
		return err
	}
	prefix := "ak_" + hex.EncodeToString(prefixBytes)

	entry := keyEntry{Prefix: prefix, Hash: publish.HashSecret(secret), Author: author, Scopes: scopes}
	encoded, err := json.Marshal(entry)
	if err != nil {
		return err
	}

	fmt.Fprintf(stdout, "# New publish key for author %q. The TOKEN below is shown ONLY now and\n", author)
	fmt.Fprintln(stdout, "# cannot be recovered; give it to the agent as CENSURADO_PUBLISH_TOKEN.")
	fmt.Fprintln(stdout, "# Add the JSON entry to your keys file (it carries no secret, only the hash).")
	fmt.Fprintf(stdout, "TOKEN=%s.%s\n", prefix, secret)
	fmt.Fprintf(stdout, "%s\n", encoded)
	return nil
}

func envOr(getenv func(string) string, key, def string) string {
	if v := strings.TrimSpace(getenv(key)); v != "" {
		return v
	}
	return def
}

func envFloat(getenv func(string) string, key string, def float64) float64 {
	if v := strings.TrimSpace(getenv(key)); v != "" {
		if f, err := strconv.ParseFloat(v, 64); err == nil {
			return f
		}
	}
	return def
}

func envInt(getenv func(string) string, key string, def int) int {
	if v := strings.TrimSpace(getenv(key)); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return def
}
func envBool(getenv func(string) string, key string, def bool) bool {
	switch strings.ToLower(strings.TrimSpace(getenv(key))) {
	case "1", "true", "yes", "on":
		return true
	case "0", "false", "no", "off":
		return false
	}
	return def
}
