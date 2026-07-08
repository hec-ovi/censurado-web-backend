package sqlite

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/hec-ovi/censurado-web-backend/store"
)

// textCols is the column list shared by the frontend_text and panel_text catalogs,
// which are identical in shape (see schema.sql); the scope selects which table.
const textCols = "key,lang,value,metadata,deleted_at,created_at,updated_at"

// textTable maps a scope to its physical table name. It is the ONLY place a scope
// becomes a table string, so a table name is never assembled from caller input.
func textTable(scope store.TextScope) (string, error) {
	switch scope {
	case store.ScopeFrontend:
		return "frontend_text", nil
	case store.ScopePanel:
		return "panel_text", nil
	default:
		return "", fmt.Errorf("store: unknown text scope %q", scope)
	}
}

// scanTextEntry decodes one catalog row, mirroring scanTopic: metadata round-trips
// through the shared helpers and Deleted is the tombstone being non-empty.
func scanTextEntry(sc scanner) (store.TextEntry, error) {
	var key, lang, value, meta, deleted, created, updated string
	if err := sc.Scan(&key, &lang, &value, &meta, &deleted, &created, &updated); err != nil {
		return store.TextEntry{}, err
	}
	m, err := unmarshalMeta(meta)
	if err != nil {
		return store.TextEntry{}, err
	}
	createdT, err := time.Parse(timeLayout, created)
	if err != nil {
		return store.TextEntry{}, fmt.Errorf("parse text created_at: %w", err)
	}
	updatedT, err := time.Parse(timeLayout, updated)
	if err != nil {
		return store.TextEntry{}, fmt.Errorf("parse text updated_at: %w", err)
	}
	return store.TextEntry{
		Key: key, Lang: lang, Value: value, Metadata: m,
		Deleted: deleted != "", CreatedAt: createdT, UpdatedAt: updatedT,
	}, nil
}

// Text returns all live entries for the scope and language as a key->value map (a bulk
// load the generator renders one language from). Unknown scope is an error; an unknown
// language yields an empty (non-nil) map.
func (s *Store) Text(ctx context.Context, scope store.TextScope, lang string) (map[string]string, error) {
	table, err := textTable(scope)
	if err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT key, value FROM `+table+` WHERE lang = ? AND deleted_at = ''`, lang)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var k, v string
		if err := rows.Scan(&k, &v); err != nil {
			return nil, err
		}
		out[k] = v
	}
	return out, rows.Err()
}

// ListText returns entries for the scope ordered by key ascending then lang (SQLite's
// BINARY default), so the order is deterministic. A blank lang returns every
// language's rows; tombstoned rows are excluded unless includeDeleted.
func (s *Store) ListText(ctx context.Context, scope store.TextScope, lang string, includeDeleted bool) ([]store.TextEntry, error) {
	table, err := textTable(scope)
	if err != nil {
		return nil, err
	}
	q := `SELECT ` + textCols + ` FROM ` + table
	var conds []string
	var args []any
	if lang != "" {
		conds = append(conds, "lang = ?")
		args = append(args, lang)
	}
	if !includeDeleted {
		conds = append(conds, "deleted_at = ''")
	}
	if len(conds) > 0 {
		q += " WHERE " + strings.Join(conds, " AND ")
	}
	q += " ORDER BY key ASC, lang ASC"
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []store.TextEntry
	for rows.Next() {
		e, err := scanTextEntry(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// UpsertText creates or replaces one (scope, key, lang) entry, clearing any tombstone,
// and returns the stored row. It mirrors UpsertTopic.
func (s *Store) UpsertText(ctx context.Context, scope store.TextScope, e store.TextEntry) (store.TextEntry, error) {
	table, err := textTable(scope)
	if err != nil {
		return store.TextEntry{}, err
	}
	meta, err := marshalMeta(e.Metadata)
	if err != nil {
		return store.TextEntry{}, err
	}
	created := e.CreatedAt
	if created.IsZero() {
		created = time.Now()
	}
	updated := e.UpdatedAt
	if updated.IsZero() {
		updated = created
	}
	if _, err := s.db.ExecContext(ctx,
		`INSERT INTO `+table+` (key,lang,value,metadata,deleted_at,created_at,updated_at)
		 VALUES (?,?,?,?,'',?,?)
		 ON CONFLICT(key,lang) DO UPDATE SET
		   value=excluded.value, metadata=excluded.metadata, deleted_at='', updated_at=excluded.updated_at`,
		e.Key, e.Lang, e.Value, meta,
		created.UTC().Truncate(time.Second).Format(timeLayout),
		updated.UTC().Truncate(time.Second).Format(timeLayout),
	); err != nil {
		return store.TextEntry{}, fmt.Errorf("upsert text: %w", err)
	}
	return scanTextEntry(s.db.QueryRowContext(ctx,
		`SELECT `+textCols+` FROM `+table+` WHERE key = ? AND lang = ?`, e.Key, e.Lang))
}

// DeleteText soft-deletes the (scope, key, lang) entry by setting a whole-second
// RFC3339 tombstone, mirroring DeleteTopic. Deleting an absent entry returns
// store.ErrNotFound.
func (s *Store) DeleteText(ctx context.Context, scope store.TextScope, key, lang string) error {
	table, err := textTable(scope)
	if err != nil {
		return err
	}
	now := time.Now().UTC().Truncate(time.Second).Format(timeLayout)
	res, err := s.db.ExecContext(ctx,
		`UPDATE `+table+` SET deleted_at = ?, updated_at = ? WHERE key = ? AND lang = ?`,
		now, now, key, lang)
	if err != nil {
		return fmt.Errorf("delete text: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return store.ErrNotFound
	}
	return nil
}
