package sqlite

// The ScheduleStore implementation: the operator-owned batch-run schedule registry.
// It mirrors the source registry's conventions (slug key, JSON list columns,
// whole-second RFC3339 timestamps, deleted_at tombstone) and adds one wrinkle: the
// runs strip is written ONLY by RecordScheduleRun, so an upsert can never wipe run
// history.

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/hec-ovi/censurado-web-backend/store"
)

const scheduleCols = "id,slug,name,cadence,times,weekdays,monthdays,mode,task,prompt,authors,enabled,runs,metadata,deleted_at,created_at,updated_at"

// GetAutomationSettings returns the single global automation-settings object, or an
// empty map when none has been set (the table holds at most the id=1 row).
func (s *Store) GetAutomationSettings(ctx context.Context) (map[string]any, error) {
	var raw string
	err := s.db.QueryRowContext(ctx, `SELECT value FROM automation_settings WHERE id = 1`).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return map[string]any{}, nil
	}
	if err != nil {
		return nil, err
	}
	return unmarshalMeta(raw)
}

// SetAutomationSettings replaces the single global settings object wholesale (the
// id=1 row) and stamps updated_at. It is an upsert, so the first call creates the
// row and later calls overwrite it.
func (s *Store) SetAutomationSettings(ctx context.Context, settings map[string]any) error {
	return s.setSingleton(ctx, "automation_settings", settings)
}

// GetAutomationStatus returns the single global executor-heartbeat object, or an
// empty map when the executor never reported.
func (s *Store) GetAutomationStatus(ctx context.Context) (map[string]any, error) {
	var raw string
	err := s.db.QueryRowContext(ctx, `SELECT value FROM automation_status WHERE id = 1`).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return map[string]any{}, nil
	}
	if err != nil {
		return nil, err
	}
	return unmarshalMeta(raw)
}

// SetAutomationStatus replaces the single global status object wholesale.
func (s *Store) SetAutomationStatus(ctx context.Context, status map[string]any) error {
	return s.setSingleton(ctx, "automation_status", status)
}

// setSingleton upserts the id=1 row of one of the automation singleton tables. The
// table name is one of two compile-time constants, never caller input.
func (s *Store) setSingleton(ctx context.Context, table string, value map[string]any) error {
	blob, err := marshalMeta(value)
	if err != nil {
		return err
	}
	now := time.Now().UTC().Truncate(time.Second).Format(timeLayout)
	if _, err := s.db.ExecContext(ctx,
		`INSERT INTO `+table+` (id, value, updated_at) VALUES (1, ?, ?)
		 ON CONFLICT(id) DO UPDATE SET value = excluded.value, updated_at = excluded.updated_at`,
		blob, now); err != nil {
		return fmt.Errorf("set %s: %w", table, err)
	}
	return nil
}

// scheduleRunRow is the on-disk JSON shape of a store.ScheduleRun.
type scheduleRunRow struct {
	RunID      string `json:"run_id"`
	Status     string `json:"status"`
	Detail     string `json:"detail,omitempty"`
	StartedAt  string `json:"started_at,omitempty"`
	FinishedAt string `json:"finished_at,omitempty"`
}

// marshalInts encodes an int list as a JSON array, defaulting to "[]", mirroring
// marshalStrings.
func marshalInts(ns []int) (string, error) {
	if len(ns) == 0 {
		return "[]", nil
	}
	b, err := json.Marshal(ns)
	if err != nil {
		return "", fmt.Errorf("marshal ints: %w", err)
	}
	return string(b), nil
}

func unmarshalInts(s string) ([]int, error) {
	if s == "" || s == "[]" {
		return []int{}, nil
	}
	var out []int
	if err := json.Unmarshal([]byte(s), &out); err != nil {
		return nil, fmt.Errorf("unmarshal ints: %w", err)
	}
	return out, nil
}

func marshalRuns(rs []store.ScheduleRun) (string, error) {
	rows := make([]scheduleRunRow, 0, len(rs))
	for _, r := range rs {
		rows = append(rows, scheduleRunRow(r))
	}
	b, err := json.Marshal(rows)
	if err != nil {
		return "", fmt.Errorf("marshal schedule runs: %w", err)
	}
	return string(b), nil
}

func unmarshalRuns(s string) ([]store.ScheduleRun, error) {
	if s == "" || s == "[]" {
		return []store.ScheduleRun{}, nil
	}
	var rows []scheduleRunRow
	if err := json.Unmarshal([]byte(s), &rows); err != nil {
		return nil, fmt.Errorf("unmarshal schedule runs: %w", err)
	}
	out := make([]store.ScheduleRun, 0, len(rows))
	for _, r := range rows {
		out = append(out, store.ScheduleRun(r))
	}
	return out, nil
}

// scanSchedule decodes one schedules row, mirroring scanSource: the JSON list
// columns round-trip through the shared helpers; enabled is an INTEGER (1/0) read as
// a bool; the timestamps are whole-second RFC3339 TEXT (deleted_at "" means active).
// Shared by ScheduleBySlug and ListSchedules.
func scanSchedule(sc scanner) (store.Schedule, error) {
	var (
		id                                                    int64
		slug, name, cadence, times, weekdays, monthdays, mode string
		task, prompt, authors, runs, meta                     string
		deleted, created, updated                             string
		enabled                                               int64
	)
	if err := sc.Scan(&id, &slug, &name, &cadence, &times, &weekdays, &monthdays, &mode, &task, &prompt, &authors, &enabled, &runs, &meta, &deleted, &created, &updated); err != nil {
		return store.Schedule{}, err
	}
	timesL, err := unmarshalStrings(times)
	if err != nil {
		return store.Schedule{}, fmt.Errorf("unmarshal schedule times: %w", err)
	}
	weekdaysL, err := unmarshalInts(weekdays)
	if err != nil {
		return store.Schedule{}, fmt.Errorf("unmarshal schedule weekdays: %w", err)
	}
	monthdaysL, err := unmarshalInts(monthdays)
	if err != nil {
		return store.Schedule{}, fmt.Errorf("unmarshal schedule monthdays: %w", err)
	}
	authorsL, err := unmarshalStrings(authors)
	if err != nil {
		return store.Schedule{}, fmt.Errorf("unmarshal schedule authors: %w", err)
	}
	runsL, err := unmarshalRuns(runs)
	if err != nil {
		return store.Schedule{}, err
	}
	m, err := unmarshalMeta(meta)
	if err != nil {
		return store.Schedule{}, err
	}
	createdT, err := time.Parse(timeLayout, created)
	if err != nil {
		return store.Schedule{}, fmt.Errorf("parse schedule created_at: %w", err)
	}
	updatedT, err := time.Parse(timeLayout, updated)
	if err != nil {
		return store.Schedule{}, fmt.Errorf("parse schedule updated_at: %w", err)
	}
	return store.Schedule{
		ID:        strconv.FormatInt(id, 10),
		Slug:      slug,
		Name:      name,
		Cadence:   cadence,
		Times:     timesL,
		Weekdays:  weekdaysL,
		Monthdays: monthdaysL,
		Mode:      mode,
		Task:      task,
		Prompt:    prompt,
		Authors:   authorsL,
		Enabled:   enabled != 0,
		Runs:      runsL,
		Metadata:  m,
		Deleted:   deleted != "",
		CreatedAt: createdT,
		UpdatedAt: updatedT,
	}, nil
}

// UpsertSchedule creates or updates a schedule keyed on slug, mirroring
// UpsertSource: on an existing slug it updates the mutable fields, advances
// updated_at, preserves created_at AND the stored runs strip (runs is written only
// by RecordScheduleRun), and clears the tombstone.
func (s *Store) UpsertSchedule(ctx context.Context, sch store.Schedule) (store.Schedule, error) {
	times, err := marshalStrings(sch.Times)
	if err != nil {
		return store.Schedule{}, fmt.Errorf("marshal schedule times: %w", err)
	}
	weekdays, err := marshalInts(sch.Weekdays)
	if err != nil {
		return store.Schedule{}, fmt.Errorf("marshal schedule weekdays: %w", err)
	}
	monthdays, err := marshalInts(sch.Monthdays)
	if err != nil {
		return store.Schedule{}, fmt.Errorf("marshal schedule monthdays: %w", err)
	}
	authors, err := marshalStrings(sch.Authors)
	if err != nil {
		return store.Schedule{}, fmt.Errorf("marshal schedule authors: %w", err)
	}
	meta, err := marshalMeta(sch.Metadata)
	if err != nil {
		return store.Schedule{}, err
	}
	created := sch.CreatedAt
	if created.IsZero() {
		created = time.Now()
	}
	updated := sch.UpdatedAt
	if updated.IsZero() {
		updated = created
	}
	enabled := 0
	if sch.Enabled {
		enabled = 1
	}
	if _, err := s.db.ExecContext(ctx,
		`INSERT INTO schedules (slug,name,cadence,times,weekdays,monthdays,mode,task,prompt,authors,enabled,runs,metadata,deleted_at,created_at,updated_at)
		 VALUES (?,?,?,?,?,?,?,?,?,?,?,'[]',?,'',?,?)
		 ON CONFLICT(slug) DO UPDATE SET
		   name=excluded.name, cadence=excluded.cadence, times=excluded.times,
		   weekdays=excluded.weekdays, monthdays=excluded.monthdays, mode=excluded.mode,
		   task=excluded.task, prompt=excluded.prompt, authors=excluded.authors,
		   enabled=excluded.enabled,
		   metadata=excluded.metadata, deleted_at='', updated_at=excluded.updated_at`,
		sch.Slug, sch.Name, sch.Cadence, times, weekdays, monthdays, sch.Mode, sch.Task, sch.Prompt, authors,
		enabled, meta,
		created.UTC().Truncate(time.Second).Format(timeLayout),
		updated.UTC().Truncate(time.Second).Format(timeLayout),
	); err != nil {
		return store.Schedule{}, fmt.Errorf("upsert schedule: %w", err)
	}
	got, _, err := s.ScheduleBySlug(ctx, sch.Slug)
	if err != nil {
		return store.Schedule{}, err
	}
	return got, nil
}

// ScheduleBySlug returns the schedule for the slug, or found=false. A soft-deleted
// schedule is still returned (found=true, Deleted=true) so callers can re-activate it.
func (s *Store) ScheduleBySlug(ctx context.Context, slug string) (store.Schedule, bool, error) {
	sch, err := scanSchedule(s.db.QueryRowContext(ctx,
		`SELECT `+scheduleCols+` FROM schedules WHERE slug = ?`, slug))
	if errors.Is(err, sql.ErrNoRows) {
		return store.Schedule{}, false, nil
	}
	if err != nil {
		return store.Schedule{}, false, err
	}
	return sch, true, nil
}

// ListSchedules returns schedules ordered by slug ascending (SQLite's BINARY
// default), so the order is deterministic. Tombstoned schedules are excluded unless
// includeDeleted.
func (s *Store) ListSchedules(ctx context.Context, includeDeleted bool) ([]store.Schedule, error) {
	q := `SELECT ` + scheduleCols + ` FROM schedules`
	if !includeDeleted {
		q += ` WHERE deleted_at = ''`
	}
	q += ` ORDER BY slug ASC`
	rows, err := s.db.QueryContext(ctx, q)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []store.Schedule
	for rows.Next() {
		sch, err := scanSchedule(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, sch)
	}
	return out, rows.Err()
}

// DeleteSchedule soft-deletes the schedule by setting a whole-second RFC3339
// tombstone. Deleting an absent slug returns store.ErrNotFound.
func (s *Store) DeleteSchedule(ctx context.Context, slug string) error {
	now := time.Now().UTC().Truncate(time.Second).Format(timeLayout)
	res, err := s.db.ExecContext(ctx,
		`UPDATE schedules SET deleted_at = ?, updated_at = ? WHERE slug = ?`, now, now, slug)
	if err != nil {
		return fmt.Errorf("delete schedule: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return store.ErrNotFound
	}
	return nil
}

// RecordScheduleRun writes one run record onto the schedule's strip inside a
// transaction: an existing entry with the same RunID is replaced in place (the
// executor records "running" at fire time and overwrites it with the outcome),
// otherwise the record is prepended; the strip is trimmed to store.ScheduleRunsCap
// and updated_at advances. ErrNotFound on an absent slug.
func (s *Store) RecordScheduleRun(ctx context.Context, slug string, run store.ScheduleRun) (store.Schedule, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return store.Schedule{}, err
	}
	defer tx.Rollback()

	var raw string
	err = tx.QueryRowContext(ctx, `SELECT runs FROM schedules WHERE slug = ?`, slug).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return store.Schedule{}, store.ErrNotFound
	}
	if err != nil {
		return store.Schedule{}, err
	}
	runs, err := unmarshalRuns(raw)
	if err != nil {
		return store.Schedule{}, err
	}
	replaced := false
	for i, r := range runs {
		if r.RunID == run.RunID {
			runs[i], replaced = run, true
			break
		}
	}
	if !replaced {
		runs = append([]store.ScheduleRun{run}, runs...)
	}
	if len(runs) > store.ScheduleRunsCap {
		runs = runs[:store.ScheduleRunsCap]
	}
	blob, err := marshalRuns(runs)
	if err != nil {
		return store.Schedule{}, err
	}
	now := time.Now().UTC().Truncate(time.Second).Format(timeLayout)
	if _, err := tx.ExecContext(ctx,
		`UPDATE schedules SET runs = ?, updated_at = ? WHERE slug = ?`, blob, now, slug); err != nil {
		return store.Schedule{}, fmt.Errorf("record schedule run: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return store.Schedule{}, err
	}
	got, _, err := s.ScheduleBySlug(ctx, slug)
	if err != nil {
		return store.Schedule{}, err
	}
	return got, nil
}
