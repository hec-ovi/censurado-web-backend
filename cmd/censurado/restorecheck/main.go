// Command restorecheck proves that a restored SQLite database is actually good,
// not merely present. It is the assertion teeth of the restore drill: a backup is
// only "proven" when a restored copy passes these checks against the live source.
//
// Compare mode (the default) opens both databases and runs three checks:
//
//  1. PRAGMA integrity_check on the restored DB must return exactly "ok".
//  2. The articles (and submissions) row counts must match the live DB.
//  3. The newest article slug, by the same order the app uses
//     (published_at DESC, id DESC), must match the live DB.
//
// It prints a clear PASS/FAIL report with the numbers and exits 0 only when every
// check passes, nonzero otherwise. The comparison is factored into compare() so it
// is unit-tested directly (see main_test.go).
//
// The tool also carries two helper modes used by scripts/restore-drill.sh so the
// drill needs a single static binary for its whole flow:
//
//	-seed PATH    create and seed a fresh source DB (WAL mode, a few articles
//	              with a known latest slug) for the self-contained drill.
//	-append PATH  insert one newer article into an existing DB. The drill uses
//	              this for the post-snapshot write that exercises WAL replication.
//
// Both databases are opened READ-ONLY (mode=ro), so pointing -live at a real
// production database never writes to it and never disturbs the single-writer
// topology.
package main

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"

	_ "modernc.org/sqlite"

	"github.com/hec-ovi/censurado-web-backend/domain"
	"github.com/hec-ovi/censurado-web-backend/store/sqlite"
)

func main() {
	live := flag.String("live", "", "path to the live (source) database to compare against")
	restored := flag.String("restored", "", "path to the restored database to verify")
	seed := flag.String("seed", "", "seed a fresh source DB at this path and exit (drill helper)")
	appendTo := flag.String("append", "", "append one newer article to the DB at this path and exit (drill helper)")
	flag.Parse()

	switch {
	case *seed != "":
		if err := seedDB(*seed); err != nil {
			fmt.Fprintln(os.Stderr, "seed:", err)
			os.Exit(1)
		}
		fmt.Printf("seeded %s\n", *seed)
		return
	case *appendTo != "":
		slug, err := appendArticle(*appendTo)
		if err != nil {
			fmt.Fprintln(os.Stderr, "append:", err)
			os.Exit(1)
		}
		fmt.Printf("appended %s (slug=%s)\n", *appendTo, slug)
		return
	case *live != "" && *restored != "":
		r := compare(*live, *restored)
		r.Print(os.Stdout)
		if !r.Pass() {
			os.Exit(1)
		}
		return
	default:
		fmt.Fprintln(os.Stderr, "usage:")
		fmt.Fprintln(os.Stderr, "  restorecheck -live <path> -restored <path>   compare a restored DB against the live DB")
		fmt.Fprintln(os.Stderr, "  restorecheck -seed <path>                     seed a fresh source DB (drill helper)")
		fmt.Fprintln(os.Stderr, "  restorecheck -append <path>                   append one newer article (drill helper)")
		os.Exit(2)
	}
}

// Check is one named assertion and its outcome.
type Check struct {
	Name   string
	OK     bool
	Detail string
}

// Result is the full outcome of comparing a restored DB against the live DB. It
// carries the raw numbers (for the report) and the per-check pass/fail list.
type Result struct {
	LivePath     string
	RestoredPath string

	Integrity string // restored DB's integrity_check result (or an error string)

	LiveArticles     int
	RestoredArticles int

	LiveSubmissions     int
	RestoredSubmissions int

	LiveLatestSlug     string
	RestoredLatestSlug string

	Checks []Check
}

func (r *Result) pass(name, detail string) { r.Checks = append(r.Checks, Check{name, true, detail}) }
func (r *Result) fail(name, detail string) { r.Checks = append(r.Checks, Check{name, false, detail}) }

// Pass reports whether every check passed. An empty check list is not a pass: the
// comparison must have actually run something.
func (r Result) Pass() bool {
	if len(r.Checks) == 0 {
		return false
	}
	for _, c := range r.Checks {
		if !c.OK {
			return false
		}
	}
	return true
}

// Print writes a human-readable PASS/FAIL report with the numbers.
func (r Result) Print(w io.Writer) {
	fmt.Fprintln(w, "restorecheck report")
	fmt.Fprintf(w, "  live       : %s\n", r.LivePath)
	fmt.Fprintf(w, "  restored   : %s\n", r.RestoredPath)
	fmt.Fprintf(w, "  integrity  : %s (restored)\n", r.Integrity)
	fmt.Fprintf(w, "  articles   : live=%d restored=%d\n", r.LiveArticles, r.RestoredArticles)
	fmt.Fprintf(w, "  submissions: live=%d restored=%d\n", r.LiveSubmissions, r.RestoredSubmissions)
	fmt.Fprintf(w, "  latest slug: live=%q restored=%q\n", r.LiveLatestSlug, r.RestoredLatestSlug)
	for _, c := range r.Checks {
		status := "PASS"
		if !c.OK {
			status = "FAIL"
		}
		fmt.Fprintf(w, "  [%s] %s: %s\n", status, c.Name, c.Detail)
	}
	if r.Pass() {
		fmt.Fprintln(w, "RESTORECHECK: PASS")
	} else {
		fmt.Fprintln(w, "RESTORECHECK: FAIL")
	}
}

// compare opens both databases read-only and runs the integrity, row-count, and
// latest-slug checks. Any open or query failure becomes a failed check rather than
// a returned error, so the tool always prints a report and main maps Pass to the
// exit code. This is what lets a garbage or empty restored file produce a clean
// FAIL instead of a crash.
func compare(livePath, restoredPath string) Result {
	r := Result{LivePath: livePath, RestoredPath: restoredPath}

	rdb, err := openRO(restoredPath)
	if err != nil {
		r.Integrity = "error: " + err.Error()
		r.fail("open-restored", err.Error())
		return r
	}
	defer rdb.Close()

	// Check 1: integrity of the restored DB.
	integ, err := integrityCheck(rdb)
	if err != nil {
		r.Integrity = "error: " + err.Error()
		r.fail("integrity_check", "restored: "+err.Error())
	} else {
		r.Integrity = integ
		if integ == "ok" {
			r.pass("integrity_check", "restored integrity_check = ok")
		} else {
			r.fail("integrity_check", "restored integrity_check = "+integ)
		}
	}

	ldb, err := openRO(livePath)
	if err != nil {
		r.fail("open-live", err.Error())
		return r
	}
	defer ldb.Close()

	// Check 2a: articles row count.
	la, laErr := countRows(ldb, "articles")
	ra, raErr := countRows(rdb, "articles")
	r.LiveArticles, r.RestoredArticles = la, ra
	switch {
	case laErr != nil || raErr != nil:
		r.fail("articles-count", joinErrs("live", laErr, "restored", raErr))
	case la != ra:
		r.fail("articles-count", fmt.Sprintf("mismatch: live=%d restored=%d", la, ra))
	default:
		r.pass("articles-count", fmt.Sprintf("live=%d restored=%d", la, ra))
	}

	// Check 2b: submissions row count.
	ls, lsErr := countRows(ldb, "submissions")
	rs, rsErr := countRows(rdb, "submissions")
	r.LiveSubmissions, r.RestoredSubmissions = ls, rs
	switch {
	case lsErr != nil || rsErr != nil:
		r.fail("submissions-count", joinErrs("live", lsErr, "restored", rsErr))
	case ls != rs:
		r.fail("submissions-count", fmt.Sprintf("mismatch: live=%d restored=%d", ls, rs))
	default:
		r.pass("submissions-count", fmt.Sprintf("live=%d restored=%d", ls, rs))
	}

	// Check 3: latest article slug (same order the app uses).
	lSlug, lSlugErr := latestSlug(ldb)
	rSlug, rSlugErr := latestSlug(rdb)
	r.LiveLatestSlug, r.RestoredLatestSlug = lSlug, rSlug
	switch {
	case lSlugErr != nil || rSlugErr != nil:
		r.fail("latest-slug", joinErrs("live", lSlugErr, "restored", rSlugErr))
	case lSlug != rSlug:
		r.fail("latest-slug", fmt.Sprintf("mismatch: live=%q restored=%q", lSlug, rSlug))
	default:
		r.pass("latest-slug", fmt.Sprintf("live=%q restored=%q", lSlug, rSlug))
	}

	return r
}

// openRO opens a database read-only. mode=ro guarantees the tool never writes to
// the file (so -live can point at a real production DB safely) and never creates a
// fresh DB if the path is missing or garbage.
func openRO(path string) (*sql.DB, error) {
	db, err := sql.Open("sqlite", "file:"+path+"?mode=ro&_pragma=busy_timeout(5000)")
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	return db, nil
}

// integrityCheck runs PRAGMA integrity_check and returns "ok" when the DB is sound.
// A non-database (garbage) file surfaces as a query error here, which compare turns
// into a failed check.
func integrityCheck(db *sql.DB) (string, error) {
	rows, err := db.QueryContext(context.Background(), "PRAGMA integrity_check")
	if err != nil {
		return "", err
	}
	defer rows.Close()
	var lines []string
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			return "", err
		}
		lines = append(lines, s)
	}
	if err := rows.Err(); err != nil {
		return "", err
	}
	if len(lines) == 1 && lines[0] == "ok" {
		return "ok", nil
	}
	return strings.Join(lines, "; "), nil
}

// countRows returns COUNT(*) for a fixed table name. The table name is a compile-time
// constant from compare, never user input.
func countRows(db *sql.DB, table string) (int, error) {
	var n int
	err := db.QueryRowContext(context.Background(), "SELECT COUNT(*) FROM "+table).Scan(&n)
	return n, err
}

// latestSlug returns the newest article's slug using the exact order the app uses
// for "newest first" (published_at DESC, then id DESC as a stable tiebreak). An
// empty table yields "" with no error.
func latestSlug(db *sql.DB) (string, error) {
	var slug string
	err := db.QueryRowContext(context.Background(),
		"SELECT slug FROM articles ORDER BY published_at DESC, id DESC LIMIT 1").Scan(&slug)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return slug, nil
}

func joinErrs(aLabel string, a error, bLabel string, b error) string {
	var parts []string
	if a != nil {
		parts = append(parts, aLabel+": "+a.Error())
	}
	if b != nil {
		parts = append(parts, bLabel+": "+b.Error())
	}
	return strings.Join(parts, "; ")
}

// seedBase is the deterministic set of base articles the drill seeds. Fixed
// timestamps in the past keep them older than any -append (which uses now), so the
// appended post-snapshot write is always the latest article.
var seedBase = []struct{ title, body string }{
	{"Drill base article one", "Seed body one for the restore drill."},
	{"Drill base article two", "Seed body two for the restore drill."},
	{"Drill base article three", "Seed body three for the restore drill."},
	{"Drill base article four", "Seed body four for the restore drill."},
	{"Drill base article five", "Seed body five for the restore drill."},
}

// seedDB creates a fresh WAL-mode SQLite database and seeds the base articles.
func seedDB(path string) error {
	repo, err := sqlite.Open(path)
	if err != nil {
		return err
	}
	defer repo.Close()
	ctx := context.Background()
	base := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	for i, a := range seedBase {
		art, err := domain.NewArticle(domain.PublishInput{
			Title:   a.title,
			Body:    a.body,
			Author:  "drill-bot",
			Section: "news",
			Topics:  []string{"backup", "drill"},
		}, base.Add(time.Duration(i)*time.Hour))
		if err != nil {
			return err
		}
		if _, err := repo.Upsert(ctx, art); err != nil {
			return err
		}
	}
	return nil
}

// appendArticle inserts one new article dated now, so it becomes the latest. Each
// call is content-unique (nanosecond + random suffix) so repeated appends are
// distinct rows, never deduplicated by content hash. Returns the new slug.
func appendArticle(path string) (string, error) {
	repo, err := sqlite.Open(path)
	if err != nil {
		return "", err
	}
	defer repo.Close()
	now := time.Now().UTC()
	uniq := strconv.FormatInt(now.UnixNano(), 10)
	rb := make([]byte, 6)
	if _, err := rand.Read(rb); err != nil {
		return "", err
	}
	suffix := hex.EncodeToString(rb)
	art, err := domain.NewArticle(domain.PublishInput{
		Title:   "Drill WAL append " + uniq + " " + suffix,
		Body:    "Post-snapshot WAL write at " + uniq + " " + suffix,
		Author:  "drill-bot",
		Section: "updates",
		Topics:  []string{"backup", "drill", "wal"},
	}, now)
	if err != nil {
		return "", err
	}
	res, err := repo.Upsert(context.Background(), art)
	if err != nil {
		return "", err
	}
	return res.Article.Slug, nil
}
