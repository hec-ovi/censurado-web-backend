package publish_test

import (
	"net/http"
	"testing"
)

type portadasResp struct {
	Portadas []struct {
		Date    string `json:"date"`
		Entries []struct {
			Slug string `json:"slug"`
			Role string `json:"role"`
		} `json:"entries"`
		Recomendado []string `json:"recomendado"`
		Deleted     bool     `json:"deleted"`
	} `json:"portadas"`
}

func portadaDates(r portadasResp) map[string]bool {
	out := map[string]bool{}
	for _, p := range r.Portadas {
		out[p.Date] = true
	}
	return out
}

func TestOperator_PortadaLifecycle(t *testing.T) {
	srv, trig := newOperatorServer(t)
	op := "ak_op." + opSecret
	ada := "ak_ada." + adaSecret // articles:write only, no admin:write

	// An agent key without admin:write is rejected by the mutation lane.
	if rec := doReq(t, srv, http.MethodPost, ada, "/portadas",
		`{"date":"2026-06-22","entries":[{"slug":"lead"}]}`); rec.Code != http.StatusForbidden {
		t.Fatalf("agent key POST /portadas: status = %d, want 403 (%s)", rec.Code, rec.Body.String())
	}

	// The operator posts a day plan with roles and a recomendado list.
	body := `{"date":"2026-06-22","entries":[{"slug":"lead-story","role":"important"},{"slug":"second-story","role":""},{"slug":"third-story"}],"recomendado":["rec-a","rec-b"]}`
	if rec := doReq(t, srv, http.MethodPost, op, "/portadas", body); rec.Code != http.StatusOK {
		t.Fatalf("create portada: %d (%s)", rec.Code, rec.Body.String())
	}

	// A missing date is a 400.
	if rec := doReq(t, srv, http.MethodPost, op, "/portadas", `{"entries":[]}`); rec.Code != http.StatusBadRequest {
		t.Errorf("missing date: status = %d, want 400", rec.Code)
	}

	// GET returns the plan with entry order, roles, and recomendado intact.
	var got portadasResp
	decodeBody(t, getAuth(t, srv, op, "/portadas"), &got)
	found := false
	for _, p := range got.Portadas {
		if p.Date != "2026-06-22" {
			continue
		}
		found = true
		if len(p.Entries) != 3 {
			t.Fatalf("entries len = %d, want 3 (%+v)", len(p.Entries), p.Entries)
		}
		if p.Entries[0].Slug != "lead-story" || p.Entries[0].Role != "important" {
			t.Errorf("entry[0] = %+v, want {lead-story important}", p.Entries[0])
		}
		if p.Entries[1].Slug != "second-story" || p.Entries[1].Role != "" {
			t.Errorf("entry[1] = %+v, want {second-story normal}", p.Entries[1])
		}
		if p.Entries[2].Slug != "third-story" || p.Entries[2].Role != "" {
			t.Errorf("entry[2] = %+v, want {third-story normal}", p.Entries[2])
		}
		if len(p.Recomendado) != 2 || p.Recomendado[0] != "rec-a" || p.Recomendado[1] != "rec-b" {
			t.Errorf("recomendado = %v, want [rec-a rec-b]", p.Recomendado)
		}
	}
	if !found {
		t.Fatal("created portada missing from the listing")
	}

	// Delete tombstones it: default GET omits it, include_deleted shows it flagged.
	if rec := doReq(t, srv, http.MethodDelete, op, "/portadas/2026-06-22", ""); rec.Code != http.StatusNoContent {
		t.Fatalf("delete: %d", rec.Code)
	}
	decodeBody(t, getAuth(t, srv, op, "/portadas"), &got)
	if portadaDates(got)["2026-06-22"] {
		t.Error("deleted portada still in the default listing")
	}
	decodeBody(t, getAuth(t, srv, op, "/portadas?include_deleted=true"), &got)
	sawDeleted := false
	for _, p := range got.Portadas {
		if p.Date == "2026-06-22" && p.Deleted {
			sawDeleted = true
		}
	}
	if !sawDeleted {
		t.Error("deleted portada missing or not flagged with include_deleted=true")
	}

	// Restore brings it back into the default listing.
	if rec := doReq(t, srv, http.MethodPost, op, "/portadas/2026-06-22/restore", ""); rec.Code != http.StatusOK {
		t.Fatalf("restore: %d", rec.Code)
	}
	decodeBody(t, getAuth(t, srv, op, "/portadas"), &got)
	if !portadaDates(got)["2026-06-22"] {
		t.Error("restored portada missing from the default listing")
	}

	// Missing date is a 404 on both delete and restore.
	if rec := doReq(t, srv, http.MethodDelete, op, "/portadas/1999-01-01", ""); rec.Code != http.StatusNotFound {
		t.Errorf("delete missing date: status = %d, want 404", rec.Code)
	}
	if rec := doReq(t, srv, http.MethodPost, op, "/portadas/1999-01-01/restore", ""); rec.Code != http.StatusNotFound {
		t.Errorf("restore missing date: status = %d, want 404", rec.Code)
	}
	if trig.count() == 0 {
		t.Error("regenerate never fired on a portada mutation")
	}
}
