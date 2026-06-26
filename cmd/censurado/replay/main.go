// Command censurado-replay recovers writes lost in the asynchronous replication
// window after a restore. Litestream replication lags the live database by about a
// second, so an ungraceful crash can drop the last ~1s of accepted publishes. The
// publish API archives every accepted raw payload to a durable directory
// (-payload-archive); after restoring the database to point T, this tool reads the
// archived payloads received AFTER T and replays each through the SAME idempotent
// publish path (publish.Apply).
//
// Exactly-once is a property of that path, not of this tool: the Idempotency-Key
// ledger plus the content-hash dedup make a payload that is already present a no-op.
// So replaying the whole post-T window is safe even if some of those writes survived
// the restore, and running replay twice never double-publishes.
//
//	censurado-replay -archive DIR [-db PATH] [-since T] [-dry-run]
//
// -since takes an RFC3339 timestamp or unix seconds; only payloads with ReceivedAt
// strictly after it are replayed (default: all). The archived ReceivedAt is used as
// the article clock so recovered articles keep timestamps close to the originals.
//
// -dry-run writes nothing. It parses and validates each in-scope payload and uses
// the idempotency ledger to project the outcome (would-recover vs already-present vs
// would-fail) without calling Upsert. The authoritative recovered/already-present
// split is produced by a real (non-dry-run) run, which is itself idempotent.
//
// Exit status: 0 when every in-scope payload replayed (or projected) cleanly, 1 when
// any payload failed, plus distinct codes for usage/config/db faults.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/hec-ovi/censurado-web-backend/content"
	"github.com/hec-ovi/censurado-web-backend/domain"
	"github.com/hec-ovi/censurado-web-backend/internal/publish"
	"github.com/hec-ovi/censurado-web-backend/store"
	"github.com/hec-ovi/censurado-web-backend/store/sqlite"
)

const (
	exitOK     = 0
	exitFailed = 1 // some payload failed to replay (operator must notice)
	exitUsage  = 2 // flag parse error or missing required flag
	exitConfig = 3 // bad -since or archive open failure
	exitDB     = 4 // store open failure
)

func main() { os.Exit(run(os.Args[1:], os.Getenv, os.Stdout, os.Stderr)) }

func run(args []string, getenv func(string) string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("censurado-replay", flag.ContinueOnError)
	fs.SetOutput(stderr)
	db := fs.String("db", envOr(getenv, "CENSURADO_DB", "./censurado.db"), "sqlite database path (or CENSURADO_DB)")
	archive := fs.String("archive", "", "archive directory written by the publish API (-payload-archive); required")
	since := fs.String("since", "", "replay only payloads received strictly after this point (RFC3339 or unix seconds); default all")
	dryRun := fs.Bool("dry-run", false, "report what would be replayed without writing")
	fs.Usage = func() {
		fmt.Fprintln(stderr, "censurado-replay: replay archived publish payloads after a restore.")
		fmt.Fprintln(stderr, "\nUsage:")
		fmt.Fprintln(stderr, "  censurado-replay -archive DIR [-db PATH] [-since T] [-dry-run]")
		fmt.Fprintln(stderr, "\nFlags:")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return exitUsage
	}

	if strings.TrimSpace(*archive) == "" {
		fmt.Fprintln(stderr, "usage: -archive DIR is required")
		return exitUsage
	}

	cutoff, err := parseSince(*since)
	if err != nil {
		fmt.Fprintln(stderr, "config:", err)
		return exitConfig
	}

	arch, err := publish.NewDirArchive(*archive)
	if err != nil {
		fmt.Fprintln(stderr, "config:", err)
		return exitConfig
	}

	repo, err := sqlite.Open(*db)
	if err != nil {
		fmt.Fprintf(stderr, "open db: %v\n", err)
		return exitDB
	}
	defer repo.Close()

	// The same *sqlite.Store is both the article Repository and the SubmissionLog.
	t, err := replay(context.Background(), repo, repo, arch, cutoff, *dryRun, stdout)
	if err != nil {
		fmt.Fprintln(stderr, "replay:", err)
		return exitConfig
	}
	if t.Failed > 0 {
		return exitFailed
	}
	return exitOK
}

// tally is the outcome of a replay pass.
type tally struct {
	Considered     int
	Recovered      int      // Created==true: a write that was missing and is now restored
	AlreadyPresent int      // idempotent no-op: the write survived (or a prior replay)
	Failed         int      // could not replay (decode/validation/conflict/store)
	Skipped        int      // malformed archive files skipped by List
	Failures       []string // one line per failure, for the report
}

// replay reads in-scope archived payloads (ReceivedAt strictly after since) and,
// for each, runs the idempotent publish path. In a real run it calls publish.Apply,
// using the archived ReceivedAt as the clock so recovered articles keep their
// original-ish timestamps; exactly-once is guaranteed by Apply's idempotency ledger
// and content-hash dedup. In a dry run it parses and validates each payload and
// projects the outcome from the idempotency ledger without writing. It prints a
// summary to out and returns the tally.
func replay(ctx context.Context, repo store.Repository, log store.SubmissionLog, arch *publish.DirArchive, since time.Time, dryRun bool, out io.Writer) (tally, error) {
	payloads, skipped, err := arch.List(ctx, since)
	if err != nil {
		return tally{}, err
	}
	t := tally{Skipped: skipped}

	for _, p := range payloads {
		t.Considered++
		in, derr := decodePayload(p.Body)
		if derr != nil {
			t.Failed++
			t.Failures = append(t.Failures, fmt.Sprintf("%s: decode: %v", p.IdempotencyKey, derr))
			continue
		}

		if dryRun {
			outcome, reason := project(ctx, log, in, p.IdempotencyKey, p.ReceivedAt)
			switch outcome {
			case "recover":
				t.Recovered++
			case "present":
				t.AlreadyPresent++
			default:
				t.Failed++
				t.Failures = append(t.Failures, fmt.Sprintf("%s: %s", p.IdempotencyKey, reason))
			}
			continue
		}

		res, _ := publish.Apply(ctx, repo, log, in, p.IdempotencyKey, nil, p.ReceivedAt)
		switch res.Failure {
		case publish.FailureNone:
			if res.Created {
				t.Recovered++
			} else {
				t.AlreadyPresent++
			}
		default:
			t.Failed++
			reason := res.Failure.String()
			if res.Err != nil {
				reason += ": " + res.Err.Error()
			}
			t.Failures = append(t.Failures, fmt.Sprintf("%s: %s", p.IdempotencyKey, reason))
		}
	}

	printSummary(out, t, since, dryRun)
	return t, nil
}

// decodePayload turns an archived raw body into a PublishInput. The body was already
// validated (additionalProperties:false) when it was accepted, so a plain decode is
// enough here.
func decodePayload(body []byte) (domain.PublishInput, error) {
	var in domain.PublishInput
	if err := json.Unmarshal(body, &in); err != nil {
		return domain.PublishInput{}, err
	}
	return in, nil
}

// project mirrors Apply's classification WITHOUT writing, for -dry-run. It runs the
// same validation gates, then reads the idempotency ledger: a key already present
// with the same content hash is an already-present no-op; a different hash is a
// conflict; an absent key would create the article (the common post-restore case).
// A small number of "recover" projections may instead dedup against a surviving
// article under a different key; the real run reports that exactly.
func project(ctx context.Context, log store.SubmissionLog, in domain.PublishInput, idemKey string, received time.Time) (outcome, reason string) {
	article, err := domain.NewArticle(in, received)
	if err != nil {
		return "fail", "validation: " + err.Error()
	}
	if _, err := content.RenderMarkdown(article.Body); err != nil {
		return "fail", "unrenderable_body: " + err.Error()
	}
	prev, found, err := log.FindSubmission(ctx, idemKey)
	if err != nil {
		return "fail", "lookup_error: " + err.Error()
	}
	if found {
		if prev.ContentHash != article.ContentHash {
			return "fail", "idempotency_key_reused"
		}
		return "present", ""
	}
	return "recover", ""
}

func printSummary(out io.Writer, t tally, since time.Time, dryRun bool) {
	mode := "replay"
	if dryRun {
		mode = "replay (dry-run)"
	}
	fmt.Fprintf(out, "censurado-%s\n", mode)
	if since.IsZero() {
		fmt.Fprintln(out, "  since      : (all payloads)")
	} else {
		fmt.Fprintf(out, "  since      : %s\n", since.UTC().Format(time.RFC3339Nano))
	}
	fmt.Fprintf(out, "  considered : %d\n", t.Considered)
	fmt.Fprintf(out, "  recovered  : %d\n", t.Recovered)
	fmt.Fprintf(out, "  present    : %d\n", t.AlreadyPresent)
	fmt.Fprintf(out, "  failed     : %d\n", t.Failed)
	fmt.Fprintf(out, "  skipped    : %d (malformed archive files)\n", t.Skipped)
	for _, f := range t.Failures {
		fmt.Fprintf(out, "  [FAIL] %s\n", f)
	}
	if t.Failed > 0 {
		fmt.Fprintln(out, "REPLAY: FAIL")
	} else {
		fmt.Fprintln(out, "REPLAY: OK")
	}
}

// parseSince accepts an empty string (zero time = replay all), a unix-seconds
// integer, or an RFC3339 timestamp (with or without fractional seconds).
func parseSince(s string) (time.Time, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}, nil
	}
	if n, err := strconv.ParseInt(s, 10, 64); err == nil {
		return time.Unix(n, 0).UTC(), nil
	}
	if ts, err := time.Parse(time.RFC3339Nano, s); err == nil {
		return ts.UTC(), nil
	}
	if ts, err := time.Parse(time.RFC3339, s); err == nil {
		return ts.UTC(), nil
	}
	return time.Time{}, fmt.Errorf("invalid -since %q: want RFC3339 or unix seconds", s)
}

func envOr(getenv func(string) string, key, def string) string {
	if v := strings.TrimSpace(getenv(key)); v != "" {
		return v
	}
	return def
}
