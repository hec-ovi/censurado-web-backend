package publish_test

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// These tests drive the schedule registry through the real HTTP surface wired by
// newOperatorServer, the same harness the other operator tests use. They pin the
// contract promises: slug derivation, canonical (deduped, sorted) times, the closed
// cadence/mode/status vocabularies, cadence-specific day validation, the agent-key
// 403 on the mutation lane, and the run strip's replace-by-run-id behavior.

type schedulesResp struct {
	Schedules []struct {
		Slug      string   `json:"slug"`
		Name      string   `json:"name"`
		Cadence   string   `json:"cadence"`
		Times     []string `json:"times"`
		Weekdays  []int    `json:"weekdays"`
		Monthdays []int    `json:"monthdays"`
		Mode      string   `json:"mode"`
		Authors   []string `json:"authors"`
		Enabled   bool     `json:"enabled"`
		Runs      []struct {
			RunID  string `json:"run_id"`
			Status string `json:"status"`
			Detail string `json:"detail"`
		} `json:"runs"`
		Deleted bool `json:"deleted"`
	} `json:"schedules"`
}

func TestSchedule_Lifecycle(t *testing.T) {
	srv := newOperatorServer(t)
	op := "ak_op." + opSecret

	// Create by name alone: slug derives, cadence/mode default, times are deduped
	// and sorted into canonical form, enabled defaults to true.
	body := `{"name":"Edición de la mañana","times":["18:00","07:30","18:00"],"authors":["borge"]}`
	if rec := doReq(t, srv, http.MethodPost, op, "/schedules", body); rec.Code != http.StatusOK {
		t.Fatalf("create schedule: %d (%s)", rec.Code, rec.Body.String())
	}
	var got schedulesResp
	decodeBody(t, getAuth(t, srv, op, "/schedules"), &got)
	if len(got.Schedules) != 1 {
		t.Fatalf("schedules = %+v, want one", got.Schedules)
	}
	s := got.Schedules[0]
	if s.Slug != "edicion-de-la-manana" {
		t.Errorf("slug = %q, want derived edicion-de-la-manana", s.Slug)
	}
	if s.Cadence != "daily" || s.Mode != "preview" || !s.Enabled {
		t.Errorf("defaults did not apply: %+v", s)
	}
	if len(s.Times) != 2 || s.Times[0] != "07:30" || s.Times[1] != "18:00" {
		t.Errorf("times = %v, want deduped and sorted [07:30 18:00]", s.Times)
	}
	if len(s.Authors) != 1 || s.Authors[0] != "borge" {
		t.Errorf("authors = %v, want [borge]", s.Authors)
	}

	// Upsert in place: same slug, weekly cadence with weekdays, auto mode, disabled.
	up := `{"slug":"edicion-de-la-manana","name":"Edición de la mañana","cadence":"weekly","times":["07:30"],"weekdays":[5,1,1],"mode":"auto","enabled":false}`
	if rec := doReq(t, srv, http.MethodPost, op, "/schedules", up); rec.Code != http.StatusOK {
		t.Fatalf("update schedule: %d (%s)", rec.Code, rec.Body.String())
	}
	decodeBody(t, getAuth(t, srv, op, "/schedules"), &got)
	if len(got.Schedules) != 1 {
		t.Fatalf("upsert created a duplicate: %+v", got.Schedules)
	}
	s = got.Schedules[0]
	if s.Cadence != "weekly" || s.Mode != "auto" || s.Enabled {
		t.Errorf("update did not apply: %+v", s)
	}
	if len(s.Weekdays) != 2 || s.Weekdays[0] != 1 || s.Weekdays[1] != 5 {
		t.Errorf("weekdays = %v, want deduped and sorted [1 5]", s.Weekdays)
	}

	// Run strip: "queued" then "running" then the outcome under the same run id
	// replace in place (the executor's queue -> fire -> outcome protocol).
	if rec := doReq(t, srv, http.MethodPost, op, "/schedules/edicion-de-la-manana/runs",
		`{"run_id":"lote-1","status":"queued"}`); rec.Code != http.StatusOK {
		t.Fatalf("record queued: %d (%s)", rec.Code, rec.Body.String())
	}
	if rec := doReq(t, srv, http.MethodPost, op, "/schedules/edicion-de-la-manana/runs",
		`{"run_id":"lote-1","status":"running","started_at":"2026-08-13T07:30:00Z"}`); rec.Code != http.StatusOK {
		t.Fatalf("record running: %d (%s)", rec.Code, rec.Body.String())
	}
	if rec := doReq(t, srv, http.MethodPost, op, "/schedules/edicion-de-la-manana/runs",
		`{"run_id":"lote-1","status":"ok","detail":"5/8 published","started_at":"2026-08-13T07:30:00Z","finished_at":"2026-08-13T07:52:00Z"}`); rec.Code != http.StatusOK {
		t.Fatalf("record outcome: %d (%s)", rec.Code, rec.Body.String())
	}
	decodeBody(t, getAuth(t, srv, op, "/schedules"), &got)
	if runs := got.Schedules[0].Runs; len(runs) != 1 || runs[0].Status != "ok" || runs[0].Detail != "5/8 published" {
		t.Errorf("runs = %+v, want the running entry replaced by the ok outcome", got.Schedules[0].Runs)
	}

	// Delete tombstones: gone from the default listing, visible with include_deleted.
	if rec := doReq(t, srv, http.MethodDelete, op, "/schedules/edicion-de-la-manana", ""); rec.Code != http.StatusNoContent {
		t.Fatalf("delete schedule: %d", rec.Code)
	}
	decodeBody(t, getAuth(t, srv, op, "/schedules"), &got)
	if len(got.Schedules) != 0 {
		t.Errorf("default listing after delete = %+v, want empty", got.Schedules)
	}
	decodeBody(t, getAuth(t, srv, op, "/schedules?include_deleted=true"), &got)
	if len(got.Schedules) != 1 || !got.Schedules[0].Deleted {
		t.Errorf("include_deleted listing = %+v, want the tombstoned row", got.Schedules)
	}
}

func TestSchedule_Validation(t *testing.T) {
	srv := newOperatorServer(t)
	op := "ak_op." + opSecret

	cases := []struct {
		name, body string
	}{
		{"missing name", `{"times":["07:00"]}`},
		{"no times", `{"name":"X","times":[]}`},
		{"bad time format", `{"name":"X","times":["7:00"]}`},
		{"out-of-range time", `{"name":"X","times":["24:00"]}`},
		{"bad cadence", `{"name":"X","cadence":"hourly","times":["07:00"]}`},
		{"bad mode", `{"name":"X","times":["07:00"],"mode":"live"}`},
		{"weekly without weekdays", `{"name":"X","cadence":"weekly","times":["07:00"]}`},
		{"weekday out of range", `{"name":"X","cadence":"weekly","times":["07:00"],"weekdays":[7]}`},
		{"monthly without monthdays", `{"name":"X","cadence":"monthly","times":["07:00"]}`},
		{"monthday out of range", `{"name":"X","cadence":"monthly","times":["07:00"],"monthdays":[0]}`},
	}
	for _, c := range cases {
		if rec := doReq(t, srv, http.MethodPost, op, "/schedules", c.body); rec.Code != http.StatusBadRequest {
			t.Errorf("%s: status=%d, want 400 (%s)", c.name, rec.Code, rec.Body.String())
		}
	}

	// Run-record validation: run_id and a closed status set.
	if rec := doReq(t, srv, http.MethodPost, op, "/schedules", `{"name":"Valida","times":["07:00"]}`); rec.Code != http.StatusOK {
		t.Fatalf("seed schedule: %d", rec.Code)
	}
	if rec := doReq(t, srv, http.MethodPost, op, "/schedules/valida/runs", `{"status":"ok"}`); rec.Code != http.StatusBadRequest {
		t.Errorf("run without run_id: status=%d, want 400", rec.Code)
	}
	if rec := doReq(t, srv, http.MethodPost, op, "/schedules/valida/runs", `{"run_id":"r","status":"exploded"}`); rec.Code != http.StatusBadRequest {
		t.Errorf("run with unknown status: status=%d, want 400", rec.Code)
	}
	if rec := doReq(t, srv, http.MethodPost, op, "/schedules/ghost/runs", `{"run_id":"r","status":"ok"}`); rec.Code != http.StatusNotFound {
		t.Errorf("run on missing schedule: status=%d, want 404", rec.Code)
	}
	if rec := doReq(t, srv, http.MethodDelete, op, "/schedules/ghost", ""); rec.Code != http.StatusNotFound {
		t.Errorf("delete missing schedule: status=%d, want 404", rec.Code)
	}
}

func TestAutomationSettings_RoundTripAndScope(t *testing.T) {
	srv := newOperatorServer(t)
	op := "ak_op." + opSecret
	agent := "ak_ada." + adaSecret

	// Never set: an empty object, not a null.
	var got struct {
		Settings map[string]any `json:"settings"`
	}
	decodeBody(t, getAuth(t, srv, op, "/automation-settings"), &got)
	if got.Settings == nil || len(got.Settings) != 0 {
		t.Errorf("unset settings = %v, want {}", got.Settings)
	}

	// PUT replaces wholesale; the response echoes; GET round-trips.
	body := `{"settings":{"lanes":{"openrouter":{"model":"deepseek/deepseek-chat"}},"stages":{"evaluate":{"lane":"openrouter"}}}}`
	if rec := doReq(t, srv, http.MethodPut, op, "/automation-settings", body); rec.Code != http.StatusOK {
		t.Fatalf("PUT settings: %d (%s)", rec.Code, rec.Body.String())
	}
	decodeBody(t, getAuth(t, srv, op, "/automation-settings"), &got)
	lanes, _ := got.Settings["lanes"].(map[string]any)
	if _, ok := lanes["openrouter"]; !ok {
		t.Errorf("settings did not round-trip: %v", got.Settings)
	}

	// A missing settings object is a 400; an agent key cannot write.
	if rec := doReq(t, srv, http.MethodPut, op, "/automation-settings", `{}`); rec.Code != http.StatusBadRequest {
		t.Errorf("PUT without settings: %d, want 400", rec.Code)
	}
	if rec := doReq(t, srv, http.MethodPut, agent, "/automation-settings", body); rec.Code != http.StatusForbidden {
		t.Errorf("agent PUT settings: %d, want 403", rec.Code)
	}
	if rec := getAuth(t, srv, agent, "/automation-settings"); rec.Code != http.StatusOK {
		t.Errorf("agent GET settings: %d, want 200 (the executor may read on any key)", rec.Code)
	}

	// Lane API keys are secret-aware: stored on PUT, redacted to api_key_set on
	// every read unless an admin:write holder explicitly asks for secrets (the
	// executor's lane), preserved server-side when a later save omits them, and
	// cleared by an explicit empty string.
	withKey := `{"settings":{"lanes":{"openrouter":{"model":"laguna","api_key":"sk-or-123"}}}}`
	if rec := doReq(t, srv, http.MethodPut, op, "/automation-settings", withKey); rec.Code != http.StatusOK {
		t.Fatalf("PUT settings with key: %d (%s)", rec.Code, rec.Body.String())
	}
	keyOf := func(rec *httptest.ResponseRecorder) (string, bool, bool) {
		var out struct {
			Settings struct {
				Lanes struct {
					Openrouter map[string]any `json:"openrouter"`
				} `json:"lanes"`
			} `json:"settings"`
		}
		decodeBody(t, rec, &out)
		key, hasKey := out.Settings.Lanes.Openrouter["api_key"].(string)
		_, hasFlag := out.Settings.Lanes.Openrouter["api_key_set"]
		return key, hasKey, hasFlag
	}
	if key, hasKey, hasFlag := keyOf(getAuth(t, srv, op, "/automation-settings")); hasKey || !hasFlag {
		t.Errorf("plain GET must redact: key=%q hasKey=%v flag=%v", key, hasKey, hasFlag)
	}
	if key, hasKey, _ := keyOf(getAuth(t, srv, op, "/automation-settings?include_secrets=true")); !hasKey || key != "sk-or-123" {
		t.Errorf("admin include_secrets GET must serve the raw key, got %q (has=%v)", key, hasKey)
	}
	if key, hasKey, hasFlag := keyOf(getAuth(t, srv, agent, "/automation-settings?include_secrets=true")); hasKey || !hasFlag {
		t.Errorf("an agent key never sees the secret even asking: key=%q flag=%v", key, hasFlag)
	}
	// A save that carries the redacted flag but no key keeps the stored key.
	noKey := `{"settings":{"lanes":{"openrouter":{"model":"laguna-2","api_key_set":true}},"stages":{"draft":{"lane":"local"}}}}`
	if rec := doReq(t, srv, http.MethodPut, op, "/automation-settings", noKey); rec.Code != http.StatusOK {
		t.Fatalf("PUT settings without key: %d", rec.Code)
	}
	if key, hasKey, _ := keyOf(getAuth(t, srv, op, "/automation-settings?include_secrets=true")); !hasKey || key != "sk-or-123" {
		t.Errorf("the stored key must survive a keyless save, got %q (has=%v)", key, hasKey)
	}
	// An explicit empty string clears it.
	clearKey := `{"settings":{"lanes":{"openrouter":{"model":"laguna-2","api_key":""}}}}`
	if rec := doReq(t, srv, http.MethodPut, op, "/automation-settings", clearKey); rec.Code != http.StatusOK {
		t.Fatalf("PUT settings clearing key: %d", rec.Code)
	}
	if key, hasKey, _ := keyOf(getAuth(t, srv, op, "/automation-settings?include_secrets=true")); hasKey {
		t.Errorf("an explicit empty api_key must clear the stored one, still %q", key)
	}

	// The status sibling (the executor's heartbeat) round-trips independently,
	// so the panel's settings and the executor's status never clobber each other.
	hb := `{"settings":{"at":"2026-08-13T16:00:00-03:00","llama_ok":true,"running":null,"queued":[]}}`
	if rec := doReq(t, srv, http.MethodPut, op, "/automation-status", hb); rec.Code != http.StatusOK {
		t.Fatalf("PUT status: %d (%s)", rec.Code, rec.Body.String())
	}
	decodeBody(t, getAuth(t, srv, op, "/automation-status"), &got)
	if got.Settings["llama_ok"] != true {
		t.Errorf("status did not round-trip: %v", got.Settings)
	}
	decodeBody(t, getAuth(t, srv, op, "/automation-settings"), &got)
	if _, ok := got.Settings["lanes"]; !ok {
		t.Errorf("writing status clobbered the settings singleton: %v", got.Settings)
	}
}

func TestSchedule_AgentKeyCannotMutate(t *testing.T) {
	srv := newOperatorServer(t)
	agent := "ak_ada." + adaSecret

	if rec := doReq(t, srv, http.MethodPost, agent, "/schedules", `{"name":"X","times":["07:00"]}`); rec.Code != http.StatusForbidden {
		t.Errorf("agent POST /schedules: status=%d, want 403", rec.Code)
	}
	if rec := doReq(t, srv, http.MethodDelete, agent, "/schedules/x", ""); rec.Code != http.StatusForbidden {
		t.Errorf("agent DELETE /schedules/{slug}: status=%d, want 403", rec.Code)
	}
	// Reads stay open to any valid key, so the executor could also run on a
	// lesser key for listing (it still needs admin:write to record runs).
	if rec := getAuth(t, srv, agent, "/schedules"); rec.Code != http.StatusOK {
		t.Errorf("agent GET /schedules: status=%d, want 200", rec.Code)
	}
}
