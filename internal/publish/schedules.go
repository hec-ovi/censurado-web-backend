package publish

// The schedules HTTP surface: the read listing any valid key can consume, and the
// operator lane's upsert / delete / run-record endpoints behind admin:write. The
// executor (the automation container) reads the listing each minute and records run
// outcomes; the panel's Automation tab edits the registry.

import (
	"errors"
	"net/http"
	"regexp"
	"sort"
	"strings"

	"github.com/hec-ovi/censurado-web-backend/domain"
	"github.com/hec-ovi/censurado-web-backend/store"
)

var (
	validCadences       = map[string]bool{"daily": true, "weekly": true, "monthly": true}
	validScheduleModes  = map[string]bool{"preview": true, "auto": true}
	validRunStatuses    = map[string]bool{"queued": true, "running": true, "ok": true, "failed": true}
	scheduleTimePattern = regexp.MustCompile(`^([01][0-9]|2[0-3]):[0-5][0-9]$`)
)

// scheduleRunJSON is the wire shape of one recorded firing.
type scheduleRunJSON struct {
	RunID      string `json:"run_id"`
	Status     string `json:"status"`
	Detail     string `json:"detail"`
	StartedAt  string `json:"started_at"`
	FinishedAt string `json:"finished_at"`
}

// scheduleJSON is the wire shape of a schedule. times are "HH:MM" wall-clock fire
// times in the executor's local timezone; weekdays are 0=Sunday..6 (weekly only) and
// monthdays 1..31 (monthly only). runs is the recent-run strip, newest first.
type scheduleJSON struct {
	Slug      string            `json:"slug"`
	Name      string            `json:"name"`
	Cadence   string            `json:"cadence"`
	Times     []string          `json:"times"`
	Weekdays  []int             `json:"weekdays"`
	Monthdays []int             `json:"monthdays"`
	Mode      string            `json:"mode"`
	Prompt    string            `json:"prompt"`
	Authors   []string          `json:"authors"`
	Enabled   bool              `json:"enabled"`
	Runs      []scheduleRunJSON `json:"runs"`
	Metadata  map[string]any    `json:"metadata"`
	Deleted   bool              `json:"deleted"`
	CreatedAt string            `json:"created_at"`
	UpdatedAt string            `json:"updated_at"`
}

type schedulesResponse struct {
	Schedules []scheduleJSON `json:"schedules"`
}

// toScheduleJSON maps a stored schedule to its JSON shape, coalescing every list to
// [] and the metadata to {} so the wire form never carries a null.
func toScheduleJSON(s store.Schedule) scheduleJSON {
	runs := make([]scheduleRunJSON, 0, len(s.Runs))
	for _, r := range s.Runs {
		runs = append(runs, scheduleRunJSON(r))
	}
	weekdays := s.Weekdays
	if weekdays == nil {
		weekdays = []int{}
	}
	monthdays := s.Monthdays
	if monthdays == nil {
		monthdays = []int{}
	}
	return scheduleJSON{
		Slug: s.Slug, Name: s.Name, Cadence: s.Cadence, Times: coalesceTopics(s.Times),
		Weekdays: weekdays, Monthdays: monthdays, Mode: s.Mode, Prompt: s.Prompt,
		Authors: coalesceTopics(s.Authors), Enabled: s.Enabled, Runs: runs,
		Metadata: coalesceMeta(s.Metadata), Deleted: s.Deleted,
		CreatedAt: rfc3339(s.CreatedAt), UpdatedAt: rfc3339(s.UpdatedAt),
	}
}

// ServeSchedules answers GET /schedules with the batch-run schedule registry.
// ?include_deleted=true includes tombstoned schedules (default excludes them).
func (rh *ReadHandler) ServeSchedules(w http.ResponseWriter, r *http.Request) {
	if !rh.authn(w, r) {
		return
	}
	schedules, err := rh.store.ListSchedules(r.Context(), includeDeleted(r))
	if err != nil {
		writeProblem(w, problem{Status: http.StatusInternalServerError, Code: "store_error"})
		return
	}
	out := schedulesResponse{Schedules: make([]scheduleJSON, 0, len(schedules))}
	for _, s := range schedules {
		out.Schedules = append(out.Schedules, toScheduleJSON(s))
	}
	writeJSON(w, http.StatusOK, out)
}

// automationSettingsResponse wraps the opaque settings object; the shape inside is
// owned by the automation boxes (the panel form and the executor's config merge).
type automationSettingsResponse struct {
	Settings map[string]any `json:"settings"`
}

// ServeAutomationSettings answers GET /automation-settings with the single global
// automation-settings object ({} when never set).
func (rh *ReadHandler) ServeAutomationSettings(w http.ResponseWriter, r *http.Request) {
	if !rh.authn(w, r) {
		return
	}
	settings, err := rh.store.GetAutomationSettings(r.Context())
	if err != nil {
		writeProblem(w, problem{Status: http.StatusInternalServerError, Code: "store_error"})
		return
	}
	writeJSON(w, http.StatusOK, automationSettingsResponse{Settings: coalesceMeta(settings)})
}

// ServePutAutomationSettings answers PUT /automation-settings: replace the settings
// object wholesale. The body is `{"settings": {...}}`, capped at 64 KiB so a runaway
// blob cannot bloat the singleton row.
func (oh *OperatorHandler) ServePutAutomationSettings(w http.ResponseWriter, r *http.Request) {
	if !oh.authz(w, r) {
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	var in automationSettingsResponse
	if !decodeStrict(w, r, &in) {
		return
	}
	if in.Settings == nil {
		writeProblem(w, problem{Status: http.StatusBadRequest, Code: "invalid_request", Detail: "settings object is required"})
		return
	}
	if err := oh.store.SetAutomationSettings(r.Context(), in.Settings); err != nil {
		writeProblem(w, problem{Status: http.StatusInternalServerError, Code: "store_error"})
		return
	}
	writeJSON(w, http.StatusOK, automationSettingsResponse{Settings: in.Settings})
}

// ServeAutomationStatus answers GET /automation-status with the executor's last
// heartbeat ({} when it never reported).
func (rh *ReadHandler) ServeAutomationStatus(w http.ResponseWriter, r *http.Request) {
	if !rh.authn(w, r) {
		return
	}
	status, err := rh.store.GetAutomationStatus(r.Context())
	if err != nil {
		writeProblem(w, problem{Status: http.StatusInternalServerError, Code: "store_error"})
		return
	}
	writeJSON(w, http.StatusOK, automationSettingsResponse{Settings: coalesceMeta(status)})
}

// ServePutAutomationStatus answers PUT /automation-status: the executor's heartbeat,
// replaced wholesale each tick. Same shape discipline as the settings.
func (oh *OperatorHandler) ServePutAutomationStatus(w http.ResponseWriter, r *http.Request) {
	if !oh.authz(w, r) {
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	var in automationSettingsResponse
	if !decodeStrict(w, r, &in) {
		return
	}
	if in.Settings == nil {
		writeProblem(w, problem{Status: http.StatusBadRequest, Code: "invalid_request", Detail: "settings object is required"})
		return
	}
	if err := oh.store.SetAutomationStatus(r.Context(), in.Settings); err != nil {
		writeProblem(w, problem{Status: http.StatusInternalServerError, Code: "store_error"})
		return
	}
	writeJSON(w, http.StatusOK, automationSettingsResponse{Settings: in.Settings})
}

// scheduleInput is the POST /schedules body. slug is optional: when blank it is
// derived from the name. enabled is a pointer so an omitted flag defaults to true.
type scheduleInput struct {
	Slug      string         `json:"slug"`
	Name      string         `json:"name"`
	Cadence   string         `json:"cadence"`
	Times     []string       `json:"times"`
	Weekdays  []int          `json:"weekdays,omitempty"`
	Monthdays []int          `json:"monthdays,omitempty"`
	Mode      string         `json:"mode"`
	Prompt    string         `json:"prompt"`
	Authors   []string       `json:"authors,omitempty"`
	Enabled   *bool          `json:"enabled,omitempty"`
	Metadata  map[string]any `json:"metadata,omitempty"`
}

// normalizeTimes trims, validates ("HH:MM", 24h), dedupes, and sorts the fire
// times, so the stored form is canonical and a duplicate time cannot double-fire.
// Returns an error message on the first invalid entry, "" on success.
func normalizeTimes(times []string) ([]string, string) {
	seen := map[string]bool{}
	out := make([]string, 0, len(times))
	for _, t := range times {
		t = strings.TrimSpace(t)
		if t == "" {
			continue
		}
		if !scheduleTimePattern.MatchString(t) {
			return nil, "times entries must be HH:MM (24h): " + t
		}
		if seen[t] {
			continue
		}
		seen[t] = true
		out = append(out, t)
	}
	if len(out) == 0 {
		return nil, "at least one HH:MM time is required"
	}
	sort.Strings(out)
	return out, ""
}

// normalizeDays validates, dedupes, and sorts a day list against [min, max].
func normalizeDays(days []int, min, max int, label string) ([]int, string) {
	seen := map[int]bool{}
	out := make([]int, 0, len(days))
	for _, d := range days {
		if d < min || d > max {
			return nil, label
		}
		if seen[d] {
			continue
		}
		seen[d] = true
		out = append(out, d)
	}
	sort.Ints(out)
	return out, ""
}

// ServeUpsertSchedule answers POST /schedules: create or update a schedule keyed on
// slug. name is required; slug defaults to the slugified name. cadence and mode must
// be in their closed vocabularies, times must be valid HH:MM entries (deduped and
// sorted), weekly needs at least one weekday (0=Sunday..6), monthly at least one
// monthday (1..31), and daily carries neither.
func (oh *OperatorHandler) ServeUpsertSchedule(w http.ResponseWriter, r *http.Request) {
	if !oh.authz(w, r) {
		return
	}
	var in scheduleInput
	if !decodeStrict(w, r, &in) {
		return
	}
	name := strings.TrimSpace(in.Name)
	if name == "" {
		writeProblem(w, problem{Status: http.StatusBadRequest, Code: "invalid_request", Detail: "name is required"})
		return
	}
	slug := strings.TrimSpace(in.Slug)
	if slug == "" {
		slug = domain.Slugify(name)
	}
	if slug == "" {
		writeProblem(w, problem{Status: http.StatusBadRequest, Code: "invalid_request", Detail: "could not derive a slug from the name"})
		return
	}
	cadence := in.Cadence
	if cadence == "" {
		cadence = "daily"
	}
	if !validCadences[cadence] {
		writeProblem(w, problem{Status: http.StatusBadRequest, Code: "invalid_request", Detail: "cadence must be one of daily, weekly, monthly"})
		return
	}
	mode := in.Mode
	if mode == "" {
		mode = "preview"
	}
	if !validScheduleModes[mode] {
		writeProblem(w, problem{Status: http.StatusBadRequest, Code: "invalid_request", Detail: "mode must be one of preview, auto"})
		return
	}
	times, msg := normalizeTimes(in.Times)
	if msg != "" {
		writeProblem(w, problem{Status: http.StatusBadRequest, Code: "invalid_request", Detail: msg})
		return
	}
	var weekdays, monthdays []int
	switch cadence {
	case "weekly":
		weekdays, msg = normalizeDays(in.Weekdays, 0, 6, "weekdays entries must be 0 (Sunday) through 6")
		if msg == "" && len(weekdays) == 0 {
			msg = "a weekly schedule needs at least one weekday"
		}
	case "monthly":
		monthdays, msg = normalizeDays(in.Monthdays, 1, 31, "monthdays entries must be 1 through 31")
		if msg == "" && len(monthdays) == 0 {
			msg = "a monthly schedule needs at least one monthday"
		}
	}
	if msg != "" {
		writeProblem(w, problem{Status: http.StatusBadRequest, Code: "invalid_request", Detail: msg})
		return
	}
	authors := make([]string, 0, len(in.Authors))
	for _, a := range in.Authors {
		if a = strings.TrimSpace(a); a != "" {
			authors = append(authors, a)
		}
	}
	enabled := true
	if in.Enabled != nil {
		enabled = *in.Enabled
	}
	sch, err := oh.store.UpsertSchedule(r.Context(), store.Schedule{
		Slug: slug, Name: name, Cadence: cadence, Times: times,
		Weekdays: weekdays, Monthdays: monthdays, Mode: mode,
		Prompt: strings.TrimSpace(in.Prompt), Authors: authors,
		Enabled: enabled, Metadata: in.Metadata,
	})
	if err != nil {
		writeProblem(w, problem{Status: http.StatusInternalServerError, Code: "store_error"})
		return
	}
	writeJSON(w, http.StatusOK, toScheduleJSON(sch))
}

// ServeDeleteSchedule answers DELETE /schedules/{slug}: soft-delete the schedule.
// 404 on a missing slug.
func (oh *OperatorHandler) ServeDeleteSchedule(w http.ResponseWriter, r *http.Request) {
	if !oh.authz(w, r) {
		return
	}
	err := oh.store.DeleteSchedule(r.Context(), r.PathValue("slug"))
	if errors.Is(err, store.ErrNotFound) {
		writeProblem(w, problem{Status: http.StatusNotFound, Code: "not_found"})
		return
	}
	if err != nil {
		writeProblem(w, problem{Status: http.StatusInternalServerError, Code: "store_error"})
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// scheduleRunInput is the POST /schedules/{slug}/runs body: one firing record from
// the executor. run_id is required; status must be running, ok, or failed.
type scheduleRunInput struct {
	RunID      string `json:"run_id"`
	Status     string `json:"status"`
	Detail     string `json:"detail"`
	StartedAt  string `json:"started_at"`
	FinishedAt string `json:"finished_at"`
}

// ServeRecordScheduleRun answers POST /schedules/{slug}/runs: record one firing on
// the schedule's recent-run strip. A record with an already-seen run_id replaces
// that entry (the executor posts "running" at fire time and the outcome at finish);
// the strip keeps the newest store.ScheduleRunsCap records. 404 on a missing slug.
func (oh *OperatorHandler) ServeRecordScheduleRun(w http.ResponseWriter, r *http.Request) {
	if !oh.authz(w, r) {
		return
	}
	var in scheduleRunInput
	if !decodeStrict(w, r, &in) {
		return
	}
	runID := strings.TrimSpace(in.RunID)
	if runID == "" {
		writeProblem(w, problem{Status: http.StatusBadRequest, Code: "invalid_request", Detail: "run_id is required"})
		return
	}
	if !validRunStatuses[in.Status] {
		writeProblem(w, problem{Status: http.StatusBadRequest, Code: "invalid_request", Detail: "status must be one of running, ok, failed"})
		return
	}
	sch, err := oh.store.RecordScheduleRun(r.Context(), r.PathValue("slug"), store.ScheduleRun{
		RunID: runID, Status: in.Status, Detail: in.Detail,
		StartedAt: in.StartedAt, FinishedAt: in.FinishedAt,
	})
	if errors.Is(err, store.ErrNotFound) {
		writeProblem(w, problem{Status: http.StatusNotFound, Code: "not_found"})
		return
	}
	if err != nil {
		writeProblem(w, problem{Status: http.StatusInternalServerError, Code: "store_error"})
		return
	}
	writeJSON(w, http.StatusOK, toScheduleJSON(sch))
}
