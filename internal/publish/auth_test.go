package publish

import (
	"errors"
	"testing"
)

func TestStaticKeyAuth(t *testing.T) {
	a := NewStaticKeyAuth()
	a.Add("ak_ada", HashSecret("super-secret-value"), "ada", "articles:write")

	t.Run("valid token returns identity and scope", func(t *testing.T) {
		id, err := a.Authenticate("ak_ada.super-secret-value")
		if err != nil {
			t.Fatalf("authenticate: %v", err)
		}
		if id.Author != "ada" {
			t.Errorf("author = %q, want ada", id.Author)
		}
		if !id.HasScope("articles:write") {
			t.Error("missing expected scope articles:write")
		}
		if id.HasScope("admin") {
			t.Error("unexpected scope admin")
		}
	})

	for name, token := range map[string]string{
		"wrong secret":   "ak_ada.wrong",
		"unknown prefix": "ak_bo.super-secret-value",
		"no separator":   "ak_adasuper-secret-value",
		"empty":          "",
		"empty secret":   "ak_ada.",
	} {
		t.Run("rejects "+name, func(t *testing.T) {
			if _, err := a.Authenticate(token); !errors.Is(err, ErrUnauthorized) {
				t.Errorf("err = %v, want ErrUnauthorized", err)
			}
		})
	}
}
