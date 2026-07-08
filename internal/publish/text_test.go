package publish_test

import (
	"net/http"
	"testing"
)

// textEntry / textResp mirror the wire shapes the /frontend-text and /panel-text
// routes return, so the test asserts against the frozen JSON without importing the
// unexported handler types.
type textEntry struct {
	Key   string `json:"key"`
	Lang  string `json:"lang"`
	Value string `json:"value"`
}

type textResp struct {
	Scope   string      `json:"scope"`
	Lang    string      `json:"lang"`
	Entries []textEntry `json:"entries"`
}

func TestUIText_ReadWriteScopesAndAuth(t *testing.T) {
	srv := newOperatorServer(t)
	op := "ak_op." + opSecret
	ada := "ak_ada." + adaSecret // articles:write only, no admin:write

	t.Run("empty before any write, default lang is en", func(t *testing.T) {
		var got textResp
		decodeBody(t, doReq(t, srv, http.MethodGet, op, "/frontend-text", ""), &got)
		if len(got.Entries) != 0 {
			t.Fatalf("entries = %v, want empty", got.Entries)
		}
		if got.Lang != "en" {
			t.Errorf("default lang = %q, want en", got.Lang)
		}
	})

	t.Run("POST upserts and GET reflects it", func(t *testing.T) {
		rec := doReq(t, srv, http.MethodPost, op, "/frontend-text", `{"key":"nav.about","value":"About"}`)
		if rec.Code != http.StatusOK {
			t.Fatalf("POST status = %d (%s)", rec.Code, rec.Body.String())
		}
		var got textResp
		decodeBody(t, doReq(t, srv, http.MethodGet, op, "/frontend-text", ""), &got)
		if len(got.Entries) != 1 || got.Entries[0].Key != "nav.about" || got.Entries[0].Value != "About" || got.Entries[0].Lang != "en" {
			t.Errorf("GET after POST = %+v", got.Entries)
		}
	})

	t.Run("a second language is a separate row, invisible under en", func(t *testing.T) {
		if rec := doReq(t, srv, http.MethodPost, op, "/frontend-text", `{"key":"nav.about","lang":"es","value":"Nosotros"}`); rec.Code != http.StatusOK {
			t.Fatalf("POST es status = %d", rec.Code)
		}
		var en textResp
		decodeBody(t, doReq(t, srv, http.MethodGet, op, "/frontend-text", ""), &en)
		if len(en.Entries) != 1 || en.Entries[0].Value != "About" {
			t.Errorf("en catalog = %+v, want just the English row", en.Entries)
		}
		var es textResp
		decodeBody(t, doReq(t, srv, http.MethodGet, op, "/frontend-text?lang=es", ""), &es)
		if es.Lang != "es" || len(es.Entries) != 1 || es.Entries[0].Value != "Nosotros" {
			t.Errorf("es catalog = %+v, want the Spanish row", es.Entries)
		}
	})

	t.Run("panel-text is a separate catalog from frontend-text", func(t *testing.T) {
		if rec := doReq(t, srv, http.MethodPost, op, "/panel-text", `{"key":"nav.articles","value":"Articles"}`); rec.Code != http.StatusOK {
			t.Fatalf("POST panel status = %d", rec.Code)
		}
		var front textResp
		decodeBody(t, doReq(t, srv, http.MethodGet, op, "/frontend-text", ""), &front)
		for _, e := range front.Entries {
			if e.Key == "nav.articles" {
				t.Error("panel key leaked into frontend-text GET")
			}
		}
		var panel textResp
		decodeBody(t, doReq(t, srv, http.MethodGet, op, "/panel-text", ""), &panel)
		if len(panel.Entries) != 1 || panel.Entries[0].Key != "nav.articles" {
			t.Errorf("panel catalog = %+v", panel.Entries)
		}
	})

	t.Run("POST without a key is a 400", func(t *testing.T) {
		if rec := doReq(t, srv, http.MethodPost, op, "/frontend-text", `{"value":"x"}`); rec.Code != http.StatusBadRequest {
			t.Errorf("keyless POST status = %d, want 400", rec.Code)
		}
	})

	t.Run("POST needs admin:write; GET needs only a valid token", func(t *testing.T) {
		if rec := doReq(t, srv, http.MethodPost, ada, "/frontend-text", `{"key":"x","value":"y"}`); rec.Code != http.StatusForbidden {
			t.Errorf("agent-key POST status = %d, want 403", rec.Code)
		}
		if rec := doReq(t, srv, http.MethodGet, "", "/frontend-text", ""); rec.Code != http.StatusUnauthorized {
			t.Errorf("no-token GET status = %d, want 401", rec.Code)
		}
		if rec := doReq(t, srv, http.MethodGet, ada, "/frontend-text", ""); rec.Code != http.StatusOK {
			t.Errorf("agent-key GET status = %d, want 200 (any valid key may read)", rec.Code)
		}
	})
}
