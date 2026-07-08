package publish

import (
	"encoding/json"
	"os"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/santhosh-tekuri/jsonschema/v6"
)

// TestAuthorsSchemaMirrorsAuthorInput freezes contracts/authors.schema.json against the Go
// authorInput write shape (the author analog of the article publish-input contract): the schema's
// property set must equal authorInput's json tags, handle must be the only required field, and a
// representative payload must validate while a handle-less one must not. A field added to authorInput
// without the schema (or vice versa) fails here, so the author contract cannot drift silently.
func TestAuthorsSchemaMirrorsAuthorInput(t *testing.T) {
	const path = "../../contracts/authors.schema.json"
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Required   []string       `json:"required"`
		Properties map[string]any `json:"properties"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("authors.schema.json is not valid JSON: %v", err)
	}

	var want []string
	rt := reflect.TypeOf(authorInput{})
	for i := 0; i < rt.NumField(); i++ {
		name := strings.Split(rt.Field(i).Tag.Get("json"), ",")[0]
		if name != "" && name != "-" {
			want = append(want, name)
		}
	}
	got := make([]string, 0, len(doc.Properties))
	for k := range doc.Properties {
		got = append(got, k)
	}
	sort.Strings(want)
	sort.Strings(got)
	if !reflect.DeepEqual(got, want) {
		t.Errorf("authors.schema.json properties %v != authorInput json tags %v", got, want)
	}
	if len(doc.Required) != 1 || doc.Required[0] != "handle" {
		t.Errorf("required = %v, want [handle]", doc.Required)
	}

	sch, err := jsonschema.NewCompiler().Compile(path)
	if err != nil {
		t.Fatalf("compile authors schema: %v", err)
	}
	good, _ := jsonschema.UnmarshalJSON(strings.NewReader(
		`{"handle":"ada","name":"Ada L.","topics":["tech"],"sources":[]}`))
	if err := sch.Validate(good); err != nil {
		t.Errorf("a valid author payload was rejected: %v", err)
	}
	bad, _ := jsonschema.UnmarshalJSON(strings.NewReader(`{"name":"no handle"}`))
	if err := sch.Validate(bad); err == nil {
		t.Error("an author payload without handle must fail validation")
	}
}
