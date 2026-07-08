// Command censurado-seededitorial populates the editorial_text catalog with the
// newsroom's per-language editorial config: the language-specific writing anchors the
// workflow prompts apply (lexicon bans/swaps, orthography, slop phrases, attribution and
// disclaimer exemplars) and the Telegram bot's response directive. It is the one-time
// bootstrap that turns the anchors formerly hardcoded across the brain repo's prompt files
// into operator-editable database rows the workflow and the bridge read.
//
// By default it only INSERTS rows that are missing, so re-running it fills gaps without
// clobbering values an operator later edited through POST /editorial-text. With -force it
// overwrites every seeded row back to the shipped catalog.
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/hec-ovi/censurado-web-backend/internal/editorial"
	"github.com/hec-ovi/censurado-web-backend/store"
	"github.com/hec-ovi/censurado-web-backend/store/sqlite"
)

func main() { os.Exit(run(os.Args[1:], os.Stdout, os.Stderr)) }

func run(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("censurado-seededitorial", flag.ContinueOnError)
	fs.SetOutput(stderr)
	db := fs.String("db", os.Getenv("CENSURADO_DB"), "sqlite database path (or CENSURADO_DB)")
	force := fs.Bool("force", false, "overwrite existing rows back to the shipped catalog (default: only insert missing rows)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *db == "" {
		fmt.Fprintln(stderr, "missing -db (or CENSURADO_DB)")
		return 2
	}

	repo, err := sqlite.Open(*db)
	if err != nil {
		fmt.Fprintf(stderr, "open db: %v\n", err)
		return 1
	}
	defer repo.Close()

	written, skipped, err := seedEditorialText(context.Background(), repo, *force)
	if err != nil {
		fmt.Fprintf(stderr, "seed: %v\n", err)
		return 1
	}
	fmt.Fprintf(stdout, "seeded %d row(s), skipped %d existing (keys=%d)\n",
		written, skipped, len(editorial.EditorialSeed()))
	return 0
}

// seedEditorialText upserts every editorial-config row. Without force it first reads the
// existing (key,lang) pairs (including tombstoned, so a deleted row is never silently
// resurrected) and inserts only the missing ones, so operator edits survive a re-run.
func seedEditorialText(ctx context.Context, repo store.TextStore, force bool) (written, skipped int, err error) {
	existing := map[[2]string]bool{}
	if !force {
		rows, lerr := repo.ListText(ctx, store.ScopeEditorial, "", true)
		if lerr != nil {
			return 0, 0, lerr
		}
		for _, r := range rows {
			existing[[2]string{r.Key, r.Lang}] = true
		}
	}
	for _, e := range editorial.EditorialSeed() {
		if !force && existing[[2]string{e.Key, e.Lang}] {
			skipped++
			continue
		}
		if _, uerr := repo.UpsertText(ctx, store.ScopeEditorial, store.TextEntry{
			Key: e.Key, Lang: e.Lang, Value: e.Value,
		}); uerr != nil {
			return written, skipped, fmt.Errorf("upsert %s/%s: %w", e.Lang, e.Key, uerr)
		}
		written++
	}
	return written, skipped, nil
}
