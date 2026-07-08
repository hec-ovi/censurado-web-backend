package editorial_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/hec-ovi/censurado-web-backend/internal/editorial"
)

// TestEditorialSeed_SelfConsistent pins the shape of the editorial-config catalog: unique
// (key, lang) rows, no empty fields, every current row authored in Spanish (the live
// language, no English base), and the structured anchors valid JSON of the expected type.
func TestEditorialSeed_SelfConsistent(t *testing.T) {
	seen := map[[2]string]bool{}
	byKey := map[string]string{}
	for _, e := range editorial.EditorialSeed() {
		pair := [2]string{e.Key, e.Lang}
		if seen[pair] {
			t.Errorf("duplicate seed row %q/%q", e.Key, e.Lang)
		}
		seen[pair] = true
		if e.Key == "" || e.Lang == "" || e.Value == "" {
			t.Errorf("seed row has an empty field (key=%q lang=%q value=%q)", e.Key, e.Lang, e.Value)
		}
		if e.Lang != "es" {
			t.Errorf("seed row %q lang=%q; the editorial catalog is Spanish-only today", e.Key, e.Lang)
		}
		byKey[e.Key] = e.Value
	}

	// The list-typed anchors decode to a non-empty JSON array of strings.
	for _, key := range []string{"lexicon.bans", "slop.phrases", "slop.candor_tics"} {
		v, ok := byKey[key]
		if !ok {
			t.Errorf("missing list anchor %q", key)
			continue
		}
		var arr []string
		if err := json.Unmarshal([]byte(v), &arr); err != nil {
			t.Errorf("%q is not a JSON array of strings: %v", key, err)
		} else if len(arr) == 0 {
			t.Errorf("%q decoded to an empty list", key)
		}
	}

	// The map-typed anchors decode to a non-empty JSON object of strings.
	for _, key := range []string{"lexicon.swaps", "orthography.examples"} {
		v, ok := byKey[key]
		if !ok {
			t.Errorf("missing map anchor %q", key)
			continue
		}
		var m map[string]string
		if err := json.Unmarshal([]byte(v), &m); err != nil {
			t.Errorf("%q is not a JSON object of strings: %v", key, err)
		} else if len(m) == 0 {
			t.Errorf("%q decoded to an empty map", key)
		}
	}

	// The scalar anchors are present (plain text, not JSON).
	for _, key := range []string{"attribution.example", "disclaimer.satire", "orthography.charset", "bot.directive"} {
		if byKey[key] == "" {
			t.Errorf("missing scalar anchor %q", key)
		}
	}

	// The bot directive is the register the bridge prepends; pin its core so it never drifts
	// to a language-agnostic blank while the brain assumes it carries the Spanish directive.
	if d := strings.ToLower(byKey["bot.directive"]); !strings.Contains(d, "español") || !strings.Contains(d, "voseo") {
		t.Errorf("bot.directive lost its español/voseo register: %q", byKey["bot.directive"])
	}
}
