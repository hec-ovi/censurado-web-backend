// Command censurado-publish is the agent-facing CLI for publishing one article.
// It is non-interactive: it reads an article as JSON on stdin (or --file),
// validates it locally against the same contract the server enforces, posts it to
// the publish API with the caller's token and an idempotency key, prints the JSON
// response on stdout, and returns a distinct exit code per outcome.
package main

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/hec-ovi/censurado-web-backend/domain"
)

// Exit codes are stable so an orchestrating agent can branch on the outcome.
const (
	exitOK         = 0 // created or idempotently replayed
	exitError      = 1 // network failure, server 5xx, unreadable input
	exitAuth       = 2 // missing token, usage error, 401, 403
	exitValidation = 3 // local validation failure or server 4xx (422/400)
)

func main() { os.Exit(run(os.Args[1:], os.Stdin, os.Stdout, os.Stderr)) }

func run(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("censurado-publish", flag.ContinueOnError)
	fs.SetOutput(stderr)
	url := fs.String("url", os.Getenv("CENSURADO_API_URL"), "publish API base URL (or CENSURADO_API_URL)")
	file := fs.String("file", "", "read article JSON from this file instead of stdin")
	idemKey := fs.String("idempotency-key", "", "idempotency key (default: a random one is generated)")
	dryRun := fs.Bool("dry-run", false, "validate locally and exit without sending")
	if err := fs.Parse(args); err != nil {
		return exitAuth
	}

	raw, err := readInput(*file, stdin)
	if err != nil {
		fmt.Fprintf(stderr, "read input: %v\n", err)
		return exitError
	}

	// Local validation mirrors the server contract: strict decode (unknown fields
	// rejected) then build. This catches mistakes before any network call.
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	var in domain.PublishInput
	if err := dec.Decode(&in); err != nil {
		fmt.Fprintf(stderr, "invalid article JSON: %v\n", err)
		return exitValidation
	}
	article, err := domain.NewArticle(in, time.Now())
	if err != nil {
		fmt.Fprintf(stderr, "validation failed: %v\n", err)
		return exitValidation
	}
	if *dryRun {
		fmt.Fprintf(stdout, "ok: would publish %q (slug %s)\n", article.Title, article.Slug)
		return exitOK
	}

	token := os.Getenv("CENSURADO_PUBLISH_TOKEN")
	if token == "" {
		fmt.Fprintln(stderr, "missing CENSURADO_PUBLISH_TOKEN")
		return exitAuth
	}
	if *url == "" {
		fmt.Fprintln(stderr, "missing --url or CENSURADO_API_URL")
		return exitAuth
	}
	key := *idemKey
	if key == "" {
		if key, err = randomKey(); err != nil {
			fmt.Fprintf(stderr, "generate idempotency key: %v\n", err)
			return exitError
		}
	}

	req, err := http.NewRequest(http.MethodPost, strings.TrimRight(*url, "/")+"/articles", bytes.NewReader(raw))
	if err != nil {
		fmt.Fprintf(stderr, "build request: %v\n", err)
		return exitError
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Idempotency-Key", key)
	req.Header.Set("Content-Type", "application/json")

	resp, err := (&http.Client{Timeout: 30 * time.Second}).Do(req)
	if err != nil {
		fmt.Fprintf(stderr, "request failed: %v\n", err)
		return exitError
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)

	switch {
	case resp.StatusCode == http.StatusOK || resp.StatusCode == http.StatusCreated:
		writeLine(stdout, body)
		return exitOK
	case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
		writeLine(stderr, body)
		return exitAuth
	case resp.StatusCode == http.StatusUnprocessableEntity || resp.StatusCode == http.StatusBadRequest:
		writeLine(stderr, body)
		return exitValidation
	default:
		writeLine(stderr, body)
		return exitError
	}
}

func readInput(file string, stdin io.Reader) ([]byte, error) {
	if file != "" {
		return os.ReadFile(file)
	}
	return io.ReadAll(stdin)
}

func randomKey() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

func writeLine(w io.Writer, body []byte) {
	w.Write(body)
	if n := len(body); n == 0 || body[n-1] != '\n' {
		fmt.Fprintln(w)
	}
}
