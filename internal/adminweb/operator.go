package adminweb

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strings"

	"github.com/hec-ovi/censurado-web-backend/domain"
	"github.com/hec-ovi/censurado-web-backend/store"
)

// OperatorError is the typed failure the injected Operator closure returns when the
// operator mutation API answers with a 4xx/5xx. Status/Code/Detail drive a friendly
// admin message (e.g. a 409 edit_conflict or a 404), so the operator sees a clear
// reason rather than a stack trace. The binary's closure decodes the problem+json
// body into this; the handlers here never construct one except for the
// "not configured" case (a nil closure).
type OperatorError struct {
	Status int
	Code   string
	Detail string
}

func (e *OperatorError) Error() string {
	if e == nil {
		return "<nil>"
	}
	if e.Detail != "" {
		return e.Detail
	}
	if e.Code != "" {
		return e.Code
	}
	return http.StatusText(e.Status)
}

// operatorMessage renders an OperatorError as a short, operator-facing sentence,
// special-casing the two outcomes the operator most needs to understand.
func operatorMessage(err error) string {
	var oe *OperatorError
	if errors.As(err, &oe) {
		switch oe.Status {
		case http.StatusNotFound:
			return "not found (it may have already been removed)"
		case http.StatusConflict:
			return "this edit collides with an existing article (identical content); change the title or body"
		default:
			return oe.Error()
		}
	}
	return err.Error()
}

// operatorRenderStatus picks the HTTP status for a re-rendered form after an
// operator failure: a 4xx is a recoverable, inline problem (re-render at 422 like
// the create form does); anything else is a gateway/server fault (502).
func operatorRenderStatus(err error) int {
	var oe *OperatorError
	if errors.As(err, &oe) && oe.Status >= 400 && oe.Status < 500 {
		return http.StatusUnprocessableEntity
	}
	return http.StatusBadGateway
}

// operatorAuthorBody is the JSON the operator API's POST /authors expects.
type operatorAuthorBody struct {
	Handle   string         `json:"handle"`
	Name     string         `json:"name"`
	Bio      string         `json:"bio"`
	Avatar   string         `json:"avatar"`
	Metadata map[string]any `json:"metadata,omitempty"`
}

// operatorTopicBody is the JSON the operator API's POST /topics expects.
type operatorTopicBody struct {
	Slug        string         `json:"slug"`
	Label       string         `json:"label"`
	Description string         `json:"description"`
	Metadata    map[string]any `json:"metadata,omitempty"`
}

// ----- authors -----

func (h *Handler) handleAuthors(w http.ResponseWriter, r *http.Request) {
	h.renderAuthorsList(w, r, r.URL.Query().Get("show_deleted") == "true")
}

func (h *Handler) renderAuthorsList(w http.ResponseWriter, r *http.Request, showDeleted bool) {
	view := authorsView{
		layoutData:  h.layoutFor(r, "Authors"),
		Configured:  h.authors != nil && h.operator != nil,
		ShowDeleted: showDeleted,
		ToggleURL:   "/admin/authors",
	}
	if !showDeleted {
		view.ToggleURL = "/admin/authors?show_deleted=true"
	}
	if h.authors != nil {
		authors, err := h.authors.ListAuthors(r.Context(), showDeleted)
		if err != nil {
			h.serverError(w, "list authors", err)
			return
		}
		for _, a := range authors {
			view.Rows = append(view.Rows, authorRow{
				Handle: a.Handle, Name: a.Name, Bio: a.Bio, Avatar: a.Avatar, Deleted: a.Deleted,
				EditURL: "/admin/authors/" + url.PathEscape(a.Handle) + "/edit",
			})
		}
	}
	h.renderPage(w, h.set(r).authors, view)
}

func (h *Handler) handleAuthorNew(w http.ResponseWriter, r *http.Request) {
	h.renderPage(w, h.set(r).authorForm, authorFormView{
		layoutData: h.layoutFor(r, "New author"),
		Configured: h.operator != nil,
		ActionURL:  "/admin/authors",
	})
}

func (h *Handler) handleAuthorEdit(w http.ResponseWriter, r *http.Request) {
	handle := r.PathValue("handle")
	view := authorFormView{
		layoutData: h.layoutFor(r, "Edit author"),
		Configured: h.operator != nil,
		Editing:    true,
		ActionURL:  "/admin/authors/" + url.PathEscape(handle),
		Handle:     handle,
	}
	if h.authors != nil {
		a, found, err := h.authors.AuthorByHandle(r.Context(), handle)
		if err != nil {
			h.serverError(w, "author by handle", err)
			return
		}
		if !found {
			h.notFound(w, r)
			return
		}
		view.Name, view.Bio, view.Avatar = a.Name, a.Bio, a.Avatar
	}
	h.renderPage(w, h.set(r).authorForm, view)
}

func (h *Handler) handleAuthorCreate(w http.ResponseWriter, r *http.Request) {
	h.submitAuthor(w, r, strings.TrimSpace(r.PostFormValue("handle")), false)
}

func (h *Handler) handleAuthorUpdate(w http.ResponseWriter, r *http.Request) {
	h.submitAuthor(w, r, r.PathValue("handle"), true)
}

// submitAuthor handles both the create (handle from the form) and edit (handle from
// the path, immutable) POSTs: it upserts through the operator API and redirects to
// the list on success, or re-renders the form with an inline error.
func (h *Handler) submitAuthor(w http.ResponseWriter, r *http.Request, handle string, editing bool) {
	action := "/admin/authors"
	if editing {
		action = "/admin/authors/" + url.PathEscape(handle)
	}
	view := authorFormView{
		layoutData: h.layoutFor(r, "Author"),
		Configured: h.operator != nil,
		Editing:    editing,
		ActionURL:  action,
		Handle:     handle,
		Name:       r.PostFormValue("name"),
		Bio:        r.PostFormValue("bio"),
		Avatar:     r.PostFormValue("avatar"),
	}
	if h.operator == nil {
		h.renderPage(w, h.set(r).authorForm, view)
		return
	}
	if strings.TrimSpace(handle) == "" {
		view.Error = "handle is required"
		h.renderTemplate(w, http.StatusUnprocessableEntity, h.set(r).authorForm, "layout", view)
		return
	}
	body := operatorAuthorBody{Handle: handle, Name: view.Name, Bio: view.Bio, Avatar: view.Avatar}
	if _, err := h.operator(r.Context(), http.MethodPost, "/authors", body); err != nil {
		view.Error = operatorMessage(err)
		h.renderTemplate(w, operatorRenderStatus(err), h.set(r).authorForm, "layout", view)
		return
	}
	http.Redirect(w, r, "/admin/authors", http.StatusSeeOther)
}

func (h *Handler) handleAuthorDelete(w http.ResponseWriter, r *http.Request) {
	h.operatorButton(w, r, http.MethodDelete, "/authors/"+url.PathEscape(r.PathValue("handle")), "/admin/authors")
}

func (h *Handler) handleAuthorRestore(w http.ResponseWriter, r *http.Request) {
	h.operatorButton(w, r, http.MethodPost, "/authors/"+url.PathEscape(r.PathValue("handle"))+"/restore", "/admin/authors")
}

// ----- topics -----

func (h *Handler) handleTopics(w http.ResponseWriter, r *http.Request) {
	h.renderTopicsList(w, r, r.URL.Query().Get("show_deleted") == "true")
}

func (h *Handler) renderTopicsList(w http.ResponseWriter, r *http.Request, showDeleted bool) {
	view := topicsView{
		layoutData:  h.layoutFor(r, "Topics"),
		Configured:  h.topics != nil && h.operator != nil,
		ShowDeleted: showDeleted,
		ToggleURL:   "/admin/topics",
	}
	if !showDeleted {
		view.ToggleURL = "/admin/topics?show_deleted=true"
	}
	if h.topics != nil {
		topics, err := h.topics.ListTopics(r.Context(), showDeleted)
		if err != nil {
			h.serverError(w, "list topics", err)
			return
		}
		for _, t := range topics {
			view.Rows = append(view.Rows, topicRow{
				Slug: t.Slug, Label: t.Label, Description: t.Description, Deleted: t.Deleted,
				EditURL: "/admin/topics/" + url.PathEscape(t.Slug) + "/edit",
			})
		}
	}
	h.renderPage(w, h.set(r).topics, view)
}

func (h *Handler) handleTopicNew(w http.ResponseWriter, r *http.Request) {
	h.renderPage(w, h.set(r).topicForm, topicFormView{
		layoutData: h.layoutFor(r, "New topic"),
		Configured: h.operator != nil,
		ActionURL:  "/admin/topics",
	})
}

func (h *Handler) handleTopicEdit(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("slug")
	view := topicFormView{
		layoutData: h.layoutFor(r, "Edit topic"),
		Configured: h.operator != nil,
		Editing:    true,
		ActionURL:  "/admin/topics/" + url.PathEscape(slug),
		Slug:       slug,
	}
	if h.topics != nil {
		t, found, err := h.topics.TopicBySlug(r.Context(), slug)
		if err != nil {
			h.serverError(w, "topic by slug", err)
			return
		}
		if !found {
			h.notFound(w, r)
			return
		}
		view.Label, view.Description = t.Label, t.Description
	}
	h.renderPage(w, h.set(r).topicForm, view)
}

func (h *Handler) handleTopicCreate(w http.ResponseWriter, r *http.Request) {
	h.submitTopic(w, r, strings.TrimSpace(r.PostFormValue("slug")), false)
}

func (h *Handler) handleTopicUpdate(w http.ResponseWriter, r *http.Request) {
	h.submitTopic(w, r, r.PathValue("slug"), true)
}

func (h *Handler) submitTopic(w http.ResponseWriter, r *http.Request, slug string, editing bool) {
	action := "/admin/topics"
	if editing {
		action = "/admin/topics/" + url.PathEscape(slug)
	}
	view := topicFormView{
		layoutData:  h.layoutFor(r, "Topic"),
		Configured:  h.operator != nil,
		Editing:     editing,
		ActionURL:   action,
		Slug:        slug,
		Label:       r.PostFormValue("label"),
		Description: r.PostFormValue("description"),
	}
	if h.operator == nil {
		h.renderPage(w, h.set(r).topicForm, view)
		return
	}
	if strings.TrimSpace(slug) == "" {
		view.Error = "slug is required"
		h.renderTemplate(w, http.StatusUnprocessableEntity, h.set(r).topicForm, "layout", view)
		return
	}
	body := operatorTopicBody{Slug: slug, Label: view.Label, Description: view.Description}
	if _, err := h.operator(r.Context(), http.MethodPost, "/topics", body); err != nil {
		view.Error = operatorMessage(err)
		h.renderTemplate(w, operatorRenderStatus(err), h.set(r).topicForm, "layout", view)
		return
	}
	http.Redirect(w, r, "/admin/topics", http.StatusSeeOther)
}

func (h *Handler) handleTopicDelete(w http.ResponseWriter, r *http.Request) {
	h.operatorButton(w, r, http.MethodDelete, "/topics/"+url.PathEscape(r.PathValue("slug")), "/admin/topics")
}

func (h *Handler) handleTopicRestore(w http.ResponseWriter, r *http.Request) {
	h.operatorButton(w, r, http.MethodPost, "/topics/"+url.PathEscape(r.PathValue("slug"))+"/restore", "/admin/topics")
}

// ----- articles (edit/delete/restore; browse + detail stay read-only) -----

func (h *Handler) handleArticleEdit(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("slug")
	a, err := h.repo.BySlug(r.Context(), slug)
	if errors.Is(err, store.ErrNotFound) {
		h.notFound(w, r)
		return
	}
	if err != nil {
		h.serverError(w, "byslug", err)
		return
	}
	view := articleEditView{
		layoutData:  h.layoutFor(r, "Edit "+a.Title),
		Configured:  h.operator != nil,
		Slug:        a.Slug,
		Title:       a.Title,
		Body:        a.Body,
		Author:      a.Author,
		Section:     a.Section,
		Topics:      strings.Join(a.Topics, ", "),
		PublishedAt: a.PublishedAt.UTC().Format("2006-01-02T15:04"),
		Deleted:     a.Deleted,
	}
	if len(a.Metadata) > 0 {
		if raw, mErr := json.MarshalIndent(a.Metadata, "", "  "); mErr == nil {
			view.Metadata = string(raw)
		}
	}
	h.renderPage(w, h.set(r).articleEdit, view)
}

func (h *Handler) handleArticleUpdate(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("slug")
	view := articleEditView{
		layoutData:  h.layoutFor(r, "Edit article"),
		Configured:  h.operator != nil,
		Slug:        slug,
		Title:       r.PostFormValue("title"),
		Body:        r.PostFormValue("body"),
		Author:      r.PostFormValue("author"),
		Section:     r.PostFormValue("section"),
		Topics:      r.PostFormValue("topics"),
		PublishedAt: r.PostFormValue("published_at"),
		Metadata:    r.PostFormValue("metadata"),
	}
	if h.operator == nil {
		h.renderPage(w, h.set(r).articleEdit, view)
		return
	}

	// Admin-side checks that need no round trip, mirroring the create form: the four
	// required fields, the optional timestamp, and the optional metadata JSON.
	fields := map[string]string{}
	in := domain.PublishInput{
		Title:   strings.TrimSpace(view.Title),
		Body:    strings.TrimSpace(view.Body),
		Author:  strings.TrimSpace(view.Author),
		Section: strings.TrimSpace(view.Section),
		Topics:  splitTopics(view.Topics),
	}
	if in.Title == "" {
		fields["title"] = "required"
	}
	if in.Body == "" {
		fields["body"] = "required"
	}
	if in.Author == "" {
		fields["author"] = "required"
	}
	if in.Section == "" {
		fields["section"] = "required"
	}
	if pa := strings.TrimSpace(view.PublishedAt); pa != "" {
		if t, err := parsePublishedAt(pa); err != nil {
			fields["published_at"] = "use YYYY-MM-DD, a datetime-local value, or RFC 3339"
		} else {
			in.PublishedAt = &t
		}
	}
	if md := strings.TrimSpace(view.Metadata); md != "" {
		if m, err := parseMetadataObject(md); err != nil {
			fields["metadata"] = `must be a JSON object, e.g. {"image":"https://example/x.jpg"}`
		} else {
			in.Metadata = m
		}
	}
	if len(fields) > 0 {
		view.Fields = fields
		h.renderTemplate(w, http.StatusUnprocessableEntity, h.set(r).articleEdit, "layout", view)
		return
	}

	if _, err := h.operator(r.Context(), http.MethodPut, "/articles/"+url.PathEscape(slug), in); err != nil {
		view.Error = operatorMessage(err)
		h.renderTemplate(w, operatorRenderStatus(err), h.set(r).articleEdit, "layout", view)
		return
	}
	http.Redirect(w, r, "/admin/articles/"+url.PathEscape(slug), http.StatusSeeOther)
}

func (h *Handler) handleArticleDelete(w http.ResponseWriter, r *http.Request) {
	h.operatorButton(w, r, http.MethodDelete, "/articles/"+url.PathEscape(r.PathValue("slug")), "/admin/articles")
}

func (h *Handler) handleArticleRestore(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("slug")
	h.operatorButton(w, r, http.MethodPost, "/articles/"+url.PathEscape(slug)+"/restore", "/admin/articles/"+url.PathEscape(slug))
}

// operatorButton runs a no-body mutation (delete/restore) through the operator API
// and redirects to redirectTo. A disabled (nil) operator or an unexpected fault is a
// serverError; an operator 4xx (e.g. a 404 because the row is already gone) is benign
// and still redirects, since the list/detail the operator lands on shows the result.
func (h *Handler) operatorButton(w http.ResponseWriter, r *http.Request, method, path, redirectTo string) {
	if h.operator == nil {
		h.serverError(w, "operator", errors.New("operator API not configured"))
		return
	}
	if _, err := h.operator(r.Context(), method, path, nil); err != nil {
		var oe *OperatorError
		if !errors.As(err, &oe) || oe.Status >= 500 {
			h.serverError(w, "operator "+method+" "+path, err)
			return
		}
		// A 4xx (typically 404: already removed) is not a server fault; fall through to
		// the redirect so the operator sees the current state.
	}
	http.Redirect(w, r, redirectTo, http.StatusSeeOther)
}
