package storetest

// The ScheduleStore conformance suite: the executable spec every implementation of
// the operator-owned batch-run schedule registry must pass. It mirrors
// RunSourceStore's conventions and adds the schedule-specific promises: the runs
// strip survives an upsert, RecordScheduleRun replaces by run id and caps the strip.

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/hec-ovi/censurado-web-backend/store"
)

// SampleSchedule returns a valid weekly schedule keyed on slug, for seeding
// conformance state.
func SampleSchedule(slug string) store.Schedule {
	return store.Schedule{
		Slug:     slug,
		Name:     "Edicion de la manana",
		Cadence:  "weekly",
		Times:    []string{"07:30", "18:00"},
		Weekdays: []int{1, 3, 5},
		Mode:     "preview",
		Task:     "batch",
		Prompt:   "Editorial sobre energia con foco en tarifas.",
		Authors:  []string{"borge", "giuliano"},
		Enabled:  true,
		Metadata: map[string]any{"note": "conformance"},
	}
}

func scheduleSlugsOf(ss []store.Schedule) []string {
	out := make([]string, len(ss))
	for i, s := range ss {
		out[i] = s.Slug
	}
	return out
}

func equalInts(a, b []int) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// assertScheduleEqual checks every observable field round-trips, including the
// order of the times/weekdays/authors lists and the metadata.
func assertScheduleEqual(t *testing.T, got, want store.Schedule) {
	t.Helper()
	if got.Slug != want.Slug {
		t.Errorf("Slug = %q, want %q", got.Slug, want.Slug)
	}
	if got.Name != want.Name {
		t.Errorf("Name = %q, want %q", got.Name, want.Name)
	}
	if got.Cadence != want.Cadence {
		t.Errorf("Cadence = %q, want %q", got.Cadence, want.Cadence)
	}
	if !equalOrdered(got.Times, want.Times) {
		t.Errorf("Times = %v, want %v (order preserved)", got.Times, want.Times)
	}
	if !equalInts(got.Weekdays, want.Weekdays) {
		t.Errorf("Weekdays = %v, want %v", got.Weekdays, want.Weekdays)
	}
	if !equalInts(got.Monthdays, want.Monthdays) {
		t.Errorf("Monthdays = %v, want %v", got.Monthdays, want.Monthdays)
	}
	if got.Mode != want.Mode {
		t.Errorf("Mode = %q, want %q", got.Mode, want.Mode)
	}
	if got.Task != want.Task {
		t.Errorf("Task = %q, want %q", got.Task, want.Task)
	}
	if got.Prompt != want.Prompt {
		t.Errorf("Prompt = %q, want %q", got.Prompt, want.Prompt)
	}
	if !equalOrdered(got.Authors, want.Authors) {
		t.Errorf("Authors = %v, want %v", got.Authors, want.Authors)
	}
	if got.Enabled != want.Enabled {
		t.Errorf("Enabled = %v, want %v", got.Enabled, want.Enabled)
	}
	if len(got.Metadata) != len(want.Metadata) {
		t.Errorf("Metadata = %v, want %v", got.Metadata, want.Metadata)
	}
	for k, v := range want.Metadata {
		if got.Metadata[k] != v {
			t.Errorf("Metadata[%q] = %v, want %v", k, got.Metadata[k], v)
		}
	}
	if !got.CreatedAt.UTC().Equal(want.CreatedAt.UTC().Truncate(time.Second)) {
		t.Errorf("CreatedAt = %v, want %v", got.CreatedAt.UTC(), want.CreatedAt.UTC().Truncate(time.Second))
	}
}

// RunScheduleStore executes the ScheduleStore conformance suite against ss, which
// must back an empty schedules table. It pins that the registry round-trips every
// field, orders by slug in byte order, tombstones and re-activates, that an upsert
// never touches the runs strip, and that RecordScheduleRun replaces by run id and
// caps the strip at store.ScheduleRunsCap. Subtests run sequentially and share the
// seeded state; it does not call t.Parallel.
func RunScheduleStore(t *testing.T, ss store.ScheduleStore) {
	ctx := context.Background()
	base := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)

	t.Run("ScheduleBySlug on a missing slug reports not found", func(t *testing.T) {
		got, found, err := ss.ScheduleBySlug(ctx, "ghost")
		if err != nil {
			t.Fatalf("ScheduleBySlug: %v", err)
		}
		if found {
			t.Errorf("found = true for missing slug, want false")
		}
		if got.Slug != "" || got.ID != "" {
			t.Errorf("got = %+v for missing slug, want zero Schedule", got)
		}
	})

	manana := SampleSchedule("edicion-manana")
	manana.CreatedAt = base
	manana.UpdatedAt = base

	t.Run("UpsertSchedule creates then ScheduleBySlug round-trips every field", func(t *testing.T) {
		stored, err := ss.UpsertSchedule(ctx, manana)
		if err != nil {
			t.Fatalf("UpsertSchedule: %v", err)
		}
		if stored.ID == "" {
			t.Errorf("empty ID, want store-assigned")
		}
		if stored.Deleted {
			t.Errorf("Deleted = true on create, want false")
		}
		if len(stored.Runs) != 0 {
			t.Errorf("Runs = %v on create, want empty", stored.Runs)
		}
		got, found, err := ss.ScheduleBySlug(ctx, manana.Slug)
		if err != nil {
			t.Fatalf("ScheduleBySlug: %v", err)
		}
		if !found {
			t.Fatalf("found = false after create, want true")
		}
		assertScheduleEqual(t, got, manana)
	})

	t.Run("RecordScheduleRun prepends newest first and replaces by run id", func(t *testing.T) {
		first := store.ScheduleRun{RunID: "lote-1", Status: "running", StartedAt: "2026-08-01T07:30:00Z"}
		if _, err := ss.RecordScheduleRun(ctx, manana.Slug, first); err != nil {
			t.Fatalf("RecordScheduleRun: %v", err)
		}
		done := store.ScheduleRun{RunID: "lote-1", Status: "ok", Detail: "5/8 published",
			StartedAt: "2026-08-01T07:30:00Z", FinishedAt: "2026-08-01T07:52:00Z"}
		got, err := ss.RecordScheduleRun(ctx, manana.Slug, done)
		if err != nil {
			t.Fatalf("RecordScheduleRun replace: %v", err)
		}
		if len(got.Runs) != 1 {
			t.Fatalf("Runs = %v, want the running entry replaced in place (len 1)", got.Runs)
		}
		if got.Runs[0] != done {
			t.Errorf("Runs[0] = %+v, want %+v", got.Runs[0], done)
		}
		second := store.ScheduleRun{RunID: "lote-2", Status: "failed", Detail: "adapter down"}
		got, err = ss.RecordScheduleRun(ctx, manana.Slug, second)
		if err != nil {
			t.Fatalf("RecordScheduleRun second: %v", err)
		}
		if len(got.Runs) != 2 || got.Runs[0] != second || got.Runs[1] != done {
			t.Errorf("Runs = %+v, want [lote-2 lote-1] newest first", got.Runs)
		}
	})

	t.Run("UpsertSchedule on an existing slug updates in place and preserves the runs strip", func(t *testing.T) {
		before, _, _ := ss.ScheduleBySlug(ctx, manana.Slug)
		edit := manana
		edit.Name = "Edicion editada"
		edit.Cadence = "daily"
		edit.Times = []string{"09:00"}
		edit.Weekdays = nil
		edit.Mode = "auto"
		edit.Enabled = false
		edit.UpdatedAt = base.Add(48 * time.Hour)
		stored, err := ss.UpsertSchedule(ctx, edit)
		if err != nil {
			t.Fatalf("UpsertSchedule update: %v", err)
		}
		if stored.ID != before.ID {
			t.Errorf("ID changed on update: %s -> %s (want same row)", before.ID, stored.ID)
		}
		if stored.Name != "Edicion editada" || stored.Cadence != "daily" || stored.Mode != "auto" || stored.Enabled {
			t.Errorf("mutable fields not updated: %+v", stored)
		}
		if !equalOrdered(stored.Times, []string{"09:00"}) || len(stored.Weekdays) != 0 {
			t.Errorf("times/weekdays not replaced: %v / %v", stored.Times, stored.Weekdays)
		}
		if len(stored.Runs) != len(before.Runs) {
			t.Errorf("Runs = %v after upsert, want the strip preserved (%v)", stored.Runs, before.Runs)
		}
		if !stored.CreatedAt.UTC().Equal(base) {
			t.Errorf("CreatedAt = %v, want preserved %v", stored.CreatedAt.UTC(), base)
		}
		if !stored.UpdatedAt.UTC().Equal(base.Add(48 * time.Hour)) {
			t.Errorf("UpdatedAt = %v, want advanced to %v", stored.UpdatedAt.UTC(), base.Add(48*time.Hour))
		}
	})

	t.Run("RecordScheduleRun caps the strip at ScheduleRunsCap", func(t *testing.T) {
		for i := 0; i < store.ScheduleRunsCap+5; i++ {
			run := store.ScheduleRun{RunID: fmt.Sprintf("cap-%d", i), Status: "ok"}
			if _, err := ss.RecordScheduleRun(ctx, manana.Slug, run); err != nil {
				t.Fatalf("RecordScheduleRun cap-%d: %v", i, err)
			}
		}
		got, _, err := ss.ScheduleBySlug(ctx, manana.Slug)
		if err != nil {
			t.Fatalf("ScheduleBySlug: %v", err)
		}
		if len(got.Runs) != store.ScheduleRunsCap {
			t.Errorf("len(Runs) = %d, want capped at %d", len(got.Runs), store.ScheduleRunsCap)
		}
		if got.Runs[0].RunID != fmt.Sprintf("cap-%d", store.ScheduleRunsCap+4) {
			t.Errorf("Runs[0] = %s, want the newest record", got.Runs[0].RunID)
		}
	})

	t.Run("RecordScheduleRun on a missing slug returns ErrNotFound", func(t *testing.T) {
		if _, err := ss.RecordScheduleRun(ctx, "ghost", store.ScheduleRun{RunID: "x", Status: "ok"}); !errors.Is(err, store.ErrNotFound) {
			t.Errorf("err = %v, want ErrNotFound", err)
		}
	})

	t.Run("ListSchedules orders by slug in byte order", func(t *testing.T) {
		noche := SampleSchedule("edicion-noche")
		if _, err := ss.UpsertSchedule(ctx, noche); err != nil {
			t.Fatalf("UpsertSchedule: %v", err)
		}
		got, err := ss.ListSchedules(ctx, false)
		if err != nil {
			t.Fatalf("ListSchedules: %v", err)
		}
		want := []string{"edicion-manana", "edicion-noche"}
		if !equalOrdered(scheduleSlugsOf(got), want) {
			t.Errorf("ListSchedules = %v, want %v", scheduleSlugsOf(got), want)
		}
	})

	t.Run("DeleteSchedule tombstones, hides from the default listing, and upsert re-activates", func(t *testing.T) {
		if err := ss.DeleteSchedule(ctx, "edicion-noche"); err != nil {
			t.Fatalf("DeleteSchedule: %v", err)
		}
		got, found, err := ss.ScheduleBySlug(ctx, "edicion-noche")
		if err != nil {
			t.Fatalf("ScheduleBySlug: %v", err)
		}
		if !found || !got.Deleted {
			t.Errorf("deleted schedule: found=%v Deleted=%v, want found with tombstone", found, got.Deleted)
		}
		listed, _ := ss.ListSchedules(ctx, false)
		if contains(scheduleSlugsOf(listed), "edicion-noche") {
			t.Errorf("default listing still shows the tombstoned slug: %v", scheduleSlugsOf(listed))
		}
		all, _ := ss.ListSchedules(ctx, true)
		if !contains(scheduleSlugsOf(all), "edicion-noche") {
			t.Errorf("includeDeleted listing hides the tombstoned slug: %v", scheduleSlugsOf(all))
		}
		revived, err := ss.UpsertSchedule(ctx, SampleSchedule("edicion-noche"))
		if err != nil {
			t.Fatalf("UpsertSchedule revive: %v", err)
		}
		if revived.Deleted {
			t.Errorf("Deleted = true after re-upsert, want tombstone cleared")
		}
	})

	t.Run("DeleteSchedule on a missing slug returns ErrNotFound", func(t *testing.T) {
		if err := ss.DeleteSchedule(ctx, "ghost"); !errors.Is(err, store.ErrNotFound) {
			t.Errorf("err = %v, want ErrNotFound", err)
		}
	})
}

// RunAutomationSettingsStore executes the AutomationSettingsStore conformance suite
// against as, which must back an empty automation_settings table: an unset singleton
// reads as an empty map, and Set replaces the object wholesale.
func RunAutomationSettingsStore(t *testing.T, as store.AutomationSettingsStore) {
	ctx := context.Background()

	t.Run("unset settings read as an empty map", func(t *testing.T) {
		got, err := as.GetAutomationSettings(ctx)
		if err != nil {
			t.Fatalf("GetAutomationSettings: %v", err)
		}
		if len(got) != 0 {
			t.Errorf("got = %v, want empty map", got)
		}
	})

	t.Run("the status sibling round-trips independently of the settings", func(t *testing.T) {
		st, ok := as.(store.AutomationStatusStore)
		if !ok {
			t.Fatalf("implementation must also carry AutomationStatusStore")
		}
		if err := st.SetAutomationStatus(ctx, map[string]any{"llama_ok": true}); err != nil {
			t.Fatalf("SetAutomationStatus: %v", err)
		}
		got, err := st.GetAutomationStatus(ctx)
		if err != nil {
			t.Fatalf("GetAutomationStatus: %v", err)
		}
		if got["llama_ok"] != true {
			t.Errorf("status round-trip lost the value: %v", got)
		}
		settings, _ := as.GetAutomationSettings(ctx)
		if len(settings) != 0 {
			t.Errorf("writing status touched the settings singleton: %v", settings)
		}
	})

	t.Run("Set then Get round-trips, and a second Set replaces wholesale", func(t *testing.T) {
		first := map[string]any{"lanes": map[string]any{"local": map[string]any{"model": "qwen"}}}
		if err := as.SetAutomationSettings(ctx, first); err != nil {
			t.Fatalf("SetAutomationSettings: %v", err)
		}
		got, err := as.GetAutomationSettings(ctx)
		if err != nil {
			t.Fatalf("GetAutomationSettings: %v", err)
		}
		lanes, _ := got["lanes"].(map[string]any)
		local, _ := lanes["local"].(map[string]any)
		if local["model"] != "qwen" {
			t.Errorf("round-trip lost the nested value: %v", got)
		}
		if err := as.SetAutomationSettings(ctx, map[string]any{"stages": map[string]any{}}); err != nil {
			t.Fatalf("SetAutomationSettings replace: %v", err)
		}
		got, _ = as.GetAutomationSettings(ctx)
		if _, still := got["lanes"]; still {
			t.Errorf("replace kept a stale key: %v", got)
		}
	})
}
