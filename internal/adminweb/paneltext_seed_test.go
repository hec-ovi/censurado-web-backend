package adminweb_test

import (
	"reflect"
	"testing"

	"github.com/hec-ovi/censurado-web-backend/internal/adminweb"
)

// The seed catalog is the live panel-key set: unique keys, each English base equal to
// the key (English source strings ARE the keys), with the dead panels dropped and the
// two Spanish-as-key Portada bugs replaced by their English keys.
func TestPanelTextSeed_SelfConsistent(t *testing.T) {
	seen := map[string]bool{}
	for _, e := range adminweb.PanelTextSeed() {
		if seen[e.Key] {
			t.Errorf("duplicate seed key %q", e.Key)
		}
		seen[e.Key] = true
		if e.Key == "" || e.En == "" {
			t.Errorf("seed row has empty key/En (key=%q en=%q)", e.Key, e.En)
		}
		if e.En != e.Key {
			t.Errorf("seed En %q != key %q; the English base must equal the source-string key", e.En, e.Key)
		}
	}
	// The seed is the actual t()-argument set: removed-panel keys, option VALUES that are
	// never t() args (active/inactive/off), and the server-rendered login literals are all
	// absent.
	for _, dead := range []string{
		"Runs", "Editorial", "Prompts", "Agentic workflow", "Health", "New persona", "Create persona",
		"active", "inactive", "off", "Welcome", "Operator token", "OPERATOR TOKEN",
	} {
		if seen[dead] {
			t.Errorf("dead key %q was seeded (not a t() argument)", dead)
		}
	}
	// The fixed Portada keys, the gender chrome (label + option labels), and the theme
	// labels are all present.
	for _, live := range []string{
		"Save portada", "Portada saved.", "Female", "Male", "Nonbinary", "Gender", "Unspecified",
		"System", "Light", "Dark", "Articles", "Save", "Recomendado",
	} {
		if !seen[live] {
			t.Errorf("expected live key %q missing from the seed", live)
		}
	}
	// The buggy Spanish-as-key entries are gone.
	for _, gone := range []string{"Guardar portada", "Portada guardada."} {
		if seen[gone] {
			t.Errorf("buggy Spanish-as-key %q should not be seeded", gone)
		}
	}
}

// SectionLabelsFromFrontend keeps only the section.<slug>.label rows and projects them
// to slug -> label, so the panel shares one source of record with the site.
func TestSectionLabelsFromFrontend(t *testing.T) {
	front := map[string]string{
		"section.world.label":                   "World",
		"section.tech.label":                    "Technology",
		"section.misterio-y-conspiracion.label": "Mystery and conspiracy",
		"footer.tagline":                        "not a section",
		"nav.about":                             "About",
		"section.oops":                          "no .label suffix, ignored",
	}
	got := adminweb.SectionLabelsFromFrontend(front)
	want := map[string]string{
		"world":                   "World",
		"tech":                    "Technology",
		"misterio-y-conspiracion": "Mystery and conspiracy",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("SectionLabelsFromFrontend = %v want %v", got, want)
	}
}
