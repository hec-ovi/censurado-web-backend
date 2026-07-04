package publish

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/hec-ovi/censurado-web-backend/domain"
	"github.com/hec-ovi/censurado-web-backend/store"
)

// batchItem is one element of a batch publish: the existing single-article
// PublishInput plus a per-item idempotency key, since one HTTP header cannot carry
// N keys. It is strict-decoded (DisallowUnknownFields) exactly like the single
// payload, so the article shape is unchanged and unknown fields are rejected.
type batchItem struct {
	domain.PublishInput
	IdempotencyKey string `json:"idempotency_key"`
}

// batchResultItem reports the outcome of one accepted item, in input order.
// Status is "created" for a brand-new article or "deduplicated" for an idempotent
// replay or a content-hash duplicate (both of which wrote no new article).
type batchResultItem struct {
	Index  int    `json:"index"`
	ID     string `json:"id"`
	Slug   string `json:"slug"`
	Status string `json:"status"`
}

type batchResponse struct {
	Results []batchResultItem `json:"results"`
}

// batchItemError names one rejected item by its index and why. The whole batch is
// rejected (atomic, nothing written) when any item is invalid.
type batchItemError struct {
	Index  int               `json:"index"`
	Code   string            `json:"code"`
	Detail string            `json:"detail,omitempty"`
	Fields map[string]string `json:"fields,omitempty"`
}

type batchProblem struct {
	Status int              `json:"status"`
	Code   string           `json:"code"`
	Detail string           `json:"detail,omitempty"`
	Errors []batchItemError `json:"errors,omitempty"`
}

// ServeBatch handles POST /articles:batch. It accepts {"articles":[ <PublishInput
// + idempotency_key>, ... ]}, validates every item with no writes, and on success
// commits all of them in ONE transaction (store.UpsertMany), so a batch is atomic:
// if any item is invalid it returns 422 with a per-item error list and writes
// nothing. The body is decoded element by element off the wire under a byte cap,
// so the raw batch is never buffered whole; the validated articles are held only
// because committing them atomically requires it. A batch is one HTTP request, so
// the per-key rate limiter charges it one token regardless of item count.
func (h *Handler) ServeBatch(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeProblem(w, problem{Status: http.StatusMethodNotAllowed, Code: "method_not_allowed"})
		return
	}

	id, ok := authenticateWrite(h.auth, w, r)
	if !ok {
		return
	}

	// Stream-decode under a byte cap. http.MaxBytesReader bounds the total entity;
	// the json.Decoder reads one item at a time through it, so peak buffering is one
	// element, not the whole batch. A cap overrun surfaces as *http.MaxBytesError on
	// any Token/Decode call and maps to 413.
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, int64(h.maxBody)))
	dec.DisallowUnknownFields()

	if err := expectDelim(dec, '{'); err != nil {
		batchReadErr(w, err)
		return
	}
	keyTok, err := dec.Token()
	if err != nil {
		batchReadErr(w, err)
		return
	}
	if key, ok := keyTok.(string); !ok || key != "articles" {
		writeBatchProblem(w, http.StatusBadRequest, "invalid_json", nil,
			`request body must be a JSON object with a single "articles" array`)
		return
	}
	if err := expectDelim(dec, '['); err != nil {
		batchReadErr(w, err)
		return
	}

	now := h.now()
	var (
		items    []batchItem
		itemErrs []batchItemError
		upserts  []store.UpsertItem
		seenKeys = map[string]int{}
		seenSlug = map[string]int{}
	)
	for dec.More() {
		if len(items) >= h.maxBatchItems {
			writeBatchProblem(w, http.StatusUnprocessableEntity, "too_many_items", nil,
				fmt.Sprintf("a batch may carry at most %d articles", h.maxBatchItems))
			return
		}
		var it batchItem
		if err := dec.Decode(&it); err != nil {
			var mbe *http.MaxBytesError
			if errors.As(err, &mbe) {
				writeBatchProblem(w, http.StatusRequestEntityTooLarge, "payload_too_large", nil,
					fmt.Sprintf("request body exceeds the %d byte limit", mbe.Limit))
				return
			}
			writeBatchProblem(w, http.StatusBadRequest, "invalid_json",
				[]batchItemError{{Index: len(items), Code: "invalid_json", Detail: err.Error()}},
				fmt.Sprintf("item %d is not valid JSON", len(items)))
			return
		}
		idx := len(items)
		items = append(items, it)

		// Validate this item with no writes, collecting a per-item error rather than
		// stopping, so the response lists every offending index.
		key := strings.TrimSpace(it.IdempotencyKey)
		if key == "" {
			itemErrs = append(itemErrs, batchItemError{Index: idx, Code: "missing_idempotency_key", Detail: "each item requires a non-empty idempotency_key"})
			continue
		}
		if prev, dup := seenKeys[key]; dup {
			itemErrs = append(itemErrs, batchItemError{Index: idx, Code: "duplicate_idempotency_key", Detail: fmt.Sprintf("idempotency_key duplicates item %d in this batch", prev)})
			continue
		}
		seenKeys[key] = idx

		if !authorAllowed(id, it.Author) {
			itemErrs = append(itemErrs, batchItemError{Index: idx, Code: "author_mismatch", Detail: "author must match the authenticated key (a multi-author batch requires articles:publish-any)"})
			continue
		}

		article, fk, fields, verr := validateInput(it.PublishInput, now)
		if fk != FailureNone {
			ie := batchItemError{Index: idx, Code: fk.String(), Fields: fields}
			if fk != FailureValidation && verr != nil {
				ie.Detail = verr.Error()
			}
			itemErrs = append(itemErrs, ie)
			continue
		}
		if prev, dup := seenSlug[article.Slug]; dup {
			itemErrs = append(itemErrs, batchItemError{Index: idx, Code: "duplicate_slug", Detail: fmt.Sprintf("slug %q duplicates item %d in this batch", article.Slug, prev)})
			continue
		}
		seenSlug[article.Slug] = idx

		upserts = append(upserts, store.UpsertItem{Article: article, IdempotencyKey: key, Scopes: id.Scopes})
	}

	// Close the array and the object, rejecting any extra top-level field or trailing
	// data so the wrapper is as strict as the per-item decode.
	if err := expectDelim(dec, ']'); err != nil {
		batchReadErr(w, err)
		return
	}
	if dec.More() {
		writeBatchProblem(w, http.StatusBadRequest, "invalid_json", nil, `only the "articles" field is allowed`)
		return
	}
	if err := expectDelim(dec, '}'); err != nil {
		batchReadErr(w, err)
		return
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		writeBatchProblem(w, http.StatusBadRequest, "invalid_json", nil, "unexpected data after the batch object")
		return
	}

	// Any invalid item fails the whole batch: 422, nothing written.
	if len(itemErrs) > 0 {
		writeBatchProblem(w, http.StatusUnprocessableEntity, "validation_failed",
			itemErrs, fmt.Sprintf("%d of %d item(s) failed validation; nothing was written", len(itemErrs), len(items)))
		return
	}

	// Every item validated, so upserts is 1:1 with items in order: a BatchConflictError
	// index is therefore the original item index.
	ctx := r.Context()
	results, err := h.repo.UpsertMany(ctx, upserts)
	if err != nil {
		var ce *store.BatchConflictError
		if errors.As(err, &ce) {
			writeBatchProblem(w, http.StatusUnprocessableEntity, "validation_failed",
				[]batchItemError{{Index: ce.Index, Code: "idempotency_key_reused", Detail: "this idempotency key was used for a different article"}},
				"nothing was written")
			return
		}
		writeProblem(w, problem{Status: http.StatusInternalServerError, Code: "store_error"})
		return
	}

	out := make([]batchResultItem, len(results))
	anyCreated := false
	for i, res := range results {
		status := "deduplicated"
		if res.Created {
			status = "created"
			anyCreated = true
		}
		out[i] = batchResultItem{Index: i, ID: res.Article.ID, Slug: res.Article.Slug, Status: status}
	}

	// Archive every newly created item after the write commits, one envelope per
	// item, so a batch is recoverable by replay exactly like single publishes.
	h.recordBatchArchive(ctx, items, results, now)

	status := http.StatusOK // all items deduplicated (or an empty batch)
	if anyCreated {
		status = http.StatusCreated
	}
	writeJSON(w, status, batchResponse{Results: out})
}

// expectDelim reads one token and verifies it is the wanted JSON delimiter. A read
// error (including a *http.MaxBytesError) is returned for the caller to classify.
func expectDelim(dec *json.Decoder, want rune) error {
	tok, err := dec.Token()
	if err != nil {
		return err
	}
	d, ok := tok.(json.Delim)
	if !ok || rune(d) != want {
		return fmt.Errorf("expected %q", string(want))
	}
	return nil
}

// batchReadErr maps a streaming-decode read error to the right response: a byte-cap
// overrun is 413, anything else is a malformed-body 400.
func batchReadErr(w http.ResponseWriter, err error) {
	var mbe *http.MaxBytesError
	if errors.As(err, &mbe) {
		writeBatchProblem(w, http.StatusRequestEntityTooLarge, "payload_too_large", nil,
			fmt.Sprintf("request body exceeds the %d byte limit", mbe.Limit))
		return
	}
	writeBatchProblem(w, http.StatusBadRequest, "invalid_json", nil, err.Error())
}

func writeBatchProblem(w http.ResponseWriter, status int, code string, errs []batchItemError, detail string) {
	w.Header().Set("Content-Type", "application/problem+json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(batchProblem{Status: status, Code: code, Detail: detail, Errors: errs})
}

// recordBatchArchive writes one archive envelope per NEWLY created batch item,
// after the transaction commits, so a batch is recoverable by replay exactly like
// single publishes: each envelope carries one article's PublishInput and its own
// idempotency key, which is what the replay tool already decodes. items and results
// are 1:1 in order. Best-effort: a marshal or write failure is logged and never
// fails the publish that already committed. No-op when archiving is off.
func (h *Handler) recordBatchArchive(ctx context.Context, items []batchItem, results []store.UpsertResult, now time.Time) {
	if h.archive == nil {
		return
	}
	for i, res := range results {
		if !res.Created {
			continue
		}
		body, err := json.Marshal(items[i].PublishInput)
		if err != nil {
			h.warnf("publish: batch archive marshal failed (idempotency_key=%q): %v", items[i].IdempotencyKey, err)
			continue
		}
		h.recordArchive(ctx, items[i].IdempotencyKey, items[i].Author, now, body)
	}
}
