package publish

import (
	"testing"
	"time"

	"github.com/hec-ovi/censurado-web-backend/domain"
)

func TestValidateInput(t *testing.T) {
	now := time.Date(2026, 6, 24, 12, 0, 0, 0, time.UTC)

	t.Run("valid input builds the article with no failure", func(t *testing.T) {
		art, fk, fields, err := validateInput(domain.PublishInput{
			Title: "Batch ready", Body: "a body", Author: "ada", Section: "tech",
		}, now)
		if fk != FailureNone {
			t.Fatalf("FailureKind = %v, want FailureNone", fk)
		}
		if err != nil {
			t.Fatalf("err = %v, want nil", err)
		}
		if fields != nil {
			t.Errorf("fields = %v, want nil", fields)
		}
		if art.Slug == "" || art.ContentHash == "" {
			t.Errorf("article not built: %+v", art)
		}
	})

	t.Run("empty title is a field validation failure", func(t *testing.T) {
		_, fk, fields, err := validateInput(domain.PublishInput{
			Title: "   ", Body: "a body", Author: "ada", Section: "tech",
		}, now)
		if fk != FailureValidation {
			t.Fatalf("FailureKind = %v, want FailureValidation", fk)
		}
		if err == nil {
			t.Fatal("err = nil, want a validation error")
		}
		if _, ok := fields["title"]; !ok {
			t.Errorf("fields = %v, want a title entry", fields)
		}
	})
}

func TestAuthorAllowed(t *testing.T) {
	writeOnly := Identity{Author: "ada", Scopes: []string{ScopeWrite}}
	operator := Identity{Author: "editor", Scopes: []string{ScopeWrite, ScopePublishAny}}

	cases := []struct {
		name   string
		id     Identity
		author string
		want   bool
	}{
		{"own author with write scope", writeOnly, "ada", true},
		{"own author, surrounding whitespace tolerated", writeOnly, "  ada ", true},
		{"different author without publish-any is rejected", writeOnly, "bo", false},
		{"different author with publish-any is allowed", operator, "bo", true},
		{"operator publishing as anyone", operator, "someone-else", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := authorAllowed(tc.id, tc.author); got != tc.want {
				t.Errorf("authorAllowed(%+v, %q) = %v, want %v", tc.id, tc.author, got, tc.want)
			}
		})
	}
}
