package publish_test

import (
	"bytes"
	"net/http"
	"testing"

	"github.com/santhosh-tekuri/jsonschema/v6"

	"github.com/hec-ovi/censurado-web-backend/internal/publish"
)

func compileSchema(t *testing.T, path string) *jsonschema.Schema {
	t.Helper()
	sch, err := jsonschema.NewCompiler().Compile(path)
	if err != nil {
		t.Fatalf("compile %s: %v", path, err)
	}
	return sch
}

func validateAgainst(t *testing.T, sch *jsonschema.Schema, b []byte, what string) {
	t.Helper()
	v, err := jsonschema.UnmarshalJSON(bytes.NewReader(b))
	if err != nil {
		t.Fatalf("%s is not valid JSON: %v", what, err)
	}
	if err := sch.Validate(v); err != nil {
		t.Fatalf("%s does not match its schema: %v", what, err)
	}
}

// A real batch request and the handler's real success response both validate
// against the frozen batch contracts.
func TestBatchContracts_RequestAndResponse(t *testing.T) {
	h, _ := newHandler(t)
	srv := publish.NewServerHandler(h, nil, nil, nil, nil)

	reqBody := batchBody(t, []bItem{
		{Title: "Contract one", Body: "b1", Author: "ada", Section: "tech", Topics: []string{"go"}, Metadata: map[string]any{"image": "/media/" + hex64() + ".jpg"}, Key: "ck1"},
		{Title: "Contract two", Body: "b2", Author: "ada", Section: "world", Key: "ck2"},
	})

	validateAgainst(t, compileSchema(t, "../../contracts/batch-request.schema.json"), []byte(reqBody), "batch request")

	rec := postBatch(t, srv, "ak_ada."+adaSecret, reqBody)
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201 (%s)", rec.Code, truncate(rec.Body.String()))
	}
	validateAgainst(t, compileSchema(t, "../../contracts/batch-response.schema.json"), rec.Body.Bytes(), "batch response")
}

// hex64 returns a 64-char hex string, a plausible content-addressed /media/ stem.
func hex64() string {
	out := make([]byte, 64)
	for i := range out {
		out[i] = "0123456789abcdef"[i%16]
	}
	return string(out)
}
