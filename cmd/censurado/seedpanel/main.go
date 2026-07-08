// Command censurado-seedpanel populates the panel_text catalog with the operator
// panel's live UI strings: the English base row for every key the panel renders (which
// equals the key, since English source strings ARE the keys), plus the preserved
// Spanish translation where the former in-JS catalog carried one. It is the one-time
// bootstrap that turns the panel's compiled strings into operator-editable database
// rows the SPA reads at boot.
//
// By default it only INSERTS rows that are missing, so re-running it fills gaps without
// clobbering values an operator later edited through POST /panel-text. With -force it
// overwrites every seeded row back to the shipped catalog.
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/hec-ovi/censurado-web-backend/internal/adminweb"
	"github.com/hec-ovi/censurado-web-backend/store"
	"github.com/hec-ovi/censurado-web-backend/store/sqlite"
)

func main() { os.Exit(run(os.Args[1:], os.Stdout, os.Stderr)) }

func run(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("censurado-seedpanel", flag.ContinueOnError)
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

	written, skipped, err := seedPanelText(context.Background(), repo, *force)
	if err != nil {
		fmt.Fprintf(stderr, "seed: %v\n", err)
		return 1
	}
	fmt.Fprintf(stdout, "seeded %d row(s), skipped %d existing (keys=%d)\n",
		written, skipped, len(adminweb.PanelTextSeed()))
	return 0
}

// seedPanelText upserts the English base row for every live panel key and the preserved
// Spanish row where one exists. Without force it first reads the existing (key,lang)
// pairs (including tombstoned, so a deleted row is never silently resurrected) and
// inserts only the missing ones, so operator edits survive a re-run.
func seedPanelText(ctx context.Context, repo store.TextStore, force bool) (written, skipped int, err error) {
	existing := map[[2]string]bool{}
	if !force {
		rows, lerr := repo.ListText(ctx, store.ScopePanel, "", true)
		if lerr != nil {
			return 0, 0, lerr
		}
		for _, r := range rows {
			existing[[2]string{r.Key, r.Lang}] = true
		}
	}
	for _, e := range adminweb.PanelTextSeed() {
		langs := []struct{ lang, val string }{{"en", e.En}}
		if e.Es != "" {
			langs = append(langs, struct{ lang, val string }{"es", e.Es})
		}
		for _, lv := range langs {
			if !force && existing[[2]string{e.Key, lv.lang}] {
				skipped++
				continue
			}
			if _, uerr := repo.UpsertText(ctx, store.ScopePanel, store.TextEntry{
				Key: e.Key, Lang: lv.lang, Value: lv.val,
			}); uerr != nil {
				return written, skipped, fmt.Errorf("upsert %s/%s: %w", lv.lang, e.Key, uerr)
			}
			written++
		}
	}
	return written, skipped, nil
}
