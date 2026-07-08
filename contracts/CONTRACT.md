# The frozen backend contract (Layer 2)

The backend (`censurado-web-backend`) is the source of truth for all content. It exposes **two**
contract surfaces, and both are frozen: a consumer that changes on either side must change this file
and the conformance tests in the same commit.

- **Seam A (Go data shape).** The static-site generator (`censurado-web`, Layer 1) depends on this
  repo at compile time (a Go `replace` in its `go.mod`) and reads content by calling the `store`
  interfaces over `domain.Article`. Seam A is the exported `domain` + `store` types the generator
  touches, plus the `Metadata` key vocabulary.
- **Seam B (HTTP API).** The brain / CLI (Layer 3) and the admin panel consume the backend only over
  HTTP, bearer-authenticated, `application/problem+json` on error. Seam B is the route table below.

The rule: Layer 1 and Layer 3 can be edited in isolation as long as their seam holds. The
conformance tests fail the moment a seam drifts, so the freeze is enforced, not documented on trust.

Guardrails:
- Seam A shapes: `domain/contract_shape_test.go`, `store/contract_interface_test.go`.
- Seam A `Metadata` vocabulary: `censurado-web/internal/generate/contract_metadata_test.go` (lives
  with its consumer).
- Seam B route table + read shapes: `internal/publish/contract_conformance_test.go`.
- Publish input shape: `contracts/article.schema.json` (+ batch request/response schemas).
- Author write shape: `contracts/authors.schema.json` (POST /authors upsert body; guarded by
  `internal/publish/authors_contract_test.go`, which mirrors it against the Go `authorInput`).

## How to change the contract (deliberately)

1. Edit this file and the relevant conformance test in the same change.
2. Update every consumer that the change touches (generator for Seam A, panel + CLI for Seam B).
3. Re-run all suites. A green run with an updated CONTRACT.md is a conscious re-freeze; a red run
   without one is drift.

---

## Seam A: the Go data shape (Layer 1 consumes)

Layer 1 imports this module and reads content through the `store` interfaces. It never fetches over
HTTP at build time.

### `domain.Article` (the record the generator renders)

Exact exported field set and JSON tags (frozen by `domain/contract_shape_test.go`):

| Field | Type | JSON tag |
|---|---|---|
| ID | string | `id` |
| Slug | string | `slug` |
| Title | string | `title` |
| Body | string (markdown) | `body` |
| Author | string | `author` |
| Section | string | `section` |
| Topics | []string | `topics` |
| PublishedAt | time.Time | `published_at` |
| ContentHash | string | `content_hash` |
| Metadata | map[string]any | `metadata,omitempty` |
| Deleted | bool | `deleted,omitempty` (server-derived, never hashed) |
| CreatedAt | time.Time | `created_at` |

`ContentHash` is SHA-256 over length-prefixed `title|body|author|section` (dedup/idempotency
identity). `HasMedia()` and `CardType()` are pure server-derived functions of body + metadata,
never stored: `HasMedia()` reports whether the body/metadata carry any image or video (content);
`CardType()` returns the card label (`text|image|youtube|video`) the listing preview shows.

### `domain.PublishInput` (what an author agent submits)

Fields: `title`, `body`, `author`, `section`, `topics?`, `slug?`, `published_at?`, `metadata?`.
Server-owned `id`/`content_hash`/`created_at` are never accepted. Mirrors
`contracts/article.schema.json`.

### The `store` interfaces the generator depends on

Frozen method sets (see `store/contract_interface_test.go`). The generator reads via a subset; the
whole set is frozen so an internal store change cannot silently break Layer 1's build:

- `Repository`: Upsert, UpsertMany, BySlug, **Find**, Count, Facets, UpdateArticle, DeleteArticle,
  RestoreArticle, Close. The generator uses **`Find(ctx, store.Filter{Order: OldestFirst})`** with the
  zero Filter, so tombstoned rows never reach it.
- `AuthorStore`: UpsertAuthor, AuthorByHandle, **ListAuthors**, DeleteAuthor, SetAuthorSources,
  AuthorSources. `ListAuthors`/`AuthorByHandle` hydrate each author's `Sources` from the
  `author_sources` join; `UpsertAuthor` never writes the join (use `SetAuthorSources`).
- `TopicStore`: UpsertTopic, TopicBySlug, **ListTopics**, DeleteTopic.
- `PortadaStore`: UpsertPortada, PortadaByDate, **ListPortadas**, DeletePortada.
- `SourceStore`: UpsertSource, SourceBySlug, **ListSources**, DeleteSource. `DeleteSource` soft-deletes
  the source AND detaches it from every author (removes its `author_sources` rows) in one transaction.
- `SubmissionLog`: FindSubmission, RecordSubmission, ListSubmissions (audit/idempotency, not read by
  the generator).

`store.Filter` public axes (frozen): the scalar hot axes `Section`, `Author`, `Topic` plus
`From`, `To`, `Order`, `Limit`, `Offset`, `IncludeDeleted` are the **public generator surface** and
keep their exact meaning. The plural `Sections`/`Authors`/`Topics` slices, `Query`,
`TitleSubtitleQuery`, and `Facets` are an admin-only widening.

Overlay types the generator reads:
- `store.Author` (ID, Handle, Name, Bio, Avatar, **Gender, About, Style, Topics, Sources**, Metadata,
  Deleted, CreatedAt, UpdatedAt). `About` is the long public about-page text; `Style` is the private
  voice/writing prompt (a plain field the public site never renders); `Topics` is the author's curated
  profile beats; `Sources` is the attached-source slug set, hydrated from the join.
- `store.Topic`, `store.PortadaDay` (Date, Entries, Recomendado, ...), `store.PortadaEntry` (Slug, Role).
- `store.Source` (ID, Slug, Domain, Homepage, Description, FeedURLs, FeedType, Language,
  OwnershipGroup, Lean, Enabled, Status, LastChecked, LastOK, Metadata, Deleted, CreatedAt, UpdatedAt).
  `Slug` is the stable key (the domain slugified); `Domain` is unique; `Lean` is `right|neutral|left`;
  `FeedType` is `auto|native_rss|atom|news_sitemap|site_search`. `Enabled` (discovery toggle) is
  distinct from `Deleted` (tombstone).

### The `Metadata` key vocabulary (a hidden, shared contract)

`Article.Metadata` is untyped JSON at the DB, but Layer 3 writes specific keys at publish and Layer 1
reads them at render. The key set IS a contract even though nothing enforces it at the column. Frozen
keys (guarded in the generator by `contract_metadata_test.go`):

`subtitle`, `description`, `card`, `image`, `image_alt`, `alt`, `image_caption`, `image_credit`,
`author_name`, `author_bio`, `author_avatar`, `avatar`, `youtube`, `youtube_id`, `video`, `keywords`,
`tweets`, `media_checks`, `gender`, `beat`, `profile_topics`.

`image_caption` (a short epígrafe) and `image_credit` (a source credit) are OPTIONAL text the site
renders as a `<figcaption>` under the hero, so a credit is never baked into the image pixels.

`card` is the authored front-page CARD (the listing preview), an object `{type, src, alt}` where
`type` is one of `text | image | youtube | video`. It states EXPLICITLY what the card shows and is
decoupled from the body (a body may embed many videos/images/tweets; the card shows exactly one
thing). It is separate from `image`/`youtube`/`youtube_id`/`video`, which now govern the article
HERO (top of the article page) and, for legacy pieces with no `card` block, the back-compat card
derivation. `CardType()` returns the card label (authored `card.type`, else derived from the legacy
signals); the read API exposes it as `card_type`.

`tweets` entries carry: id, text, url, name, handle, avatar, verified, erased, views, replies,
retweets, likes, bookmarks, created_timestamp, created_at. `media_checks[id].available` gates the
"video eliminado" placeholder.

> Note: `author_name`, `author_bio`, `author_avatar`, `gender`, `beat`, and `profile_topics` in the
> ARTICLE metadata are render-time denormalization (copied onto the article at publish so the byline
> and author card render without a join). They are distinct from the author REGISTRY, whose gender /
> about / style / topics are now first-class `store.Author` columns (the source of truth, see the
> overlay types above). Both exist on purpose; the article-metadata vocabulary is unchanged.

---

## Seam B: the HTTP API (Layer 3 + panel consume)

One Go 1.22 `net/http` ServeMux (`internal/publish/server.go`). Auth: `Authorization: Bearer
<prefix>.<secret>` (SHA-256 hashed key table), each key bound to one author with a scope set. Errors:
`application/problem+json {status, code, detail, fields}`. The `/articles` pattern is registered
method-less so it dispatches by method: POST -> publish, GET -> read, PUT/DELETE -> operator; the `:`
in `/articles:batch` is a literal path byte, no collision.

### Lane 1: liveness (no auth)

| Method | Path | Response |
|---|---|---|
| GET | `/healthz` | 200 `text/plain` `ok`. Matched before auth/limiter. |

### Lane 2: publish / write (scope `articles:write`, rate-limited)

| Method | Path | In | Out |
|---|---|---|---|
| POST | `/articles` | `Idempotency-Key` header (required) + PublishInput JSON (strict, max 8 MiB). `author` must equal the key's author unless it holds `articles:publish-any`. | 201 created / 200 idempotent replay `{id, slug}`. Errors: 401, 403 insufficient_scope/author_mismatch, 400 missing_idempotency_key/invalid_json, 422 validation_failed{fields}/unrenderable_body/idempotency_key_reused, 405. |
| POST | `/articles:batch` | `{articles:[PublishInput + idempotency_key]}`, max 500 items, atomic. | 201/200 `{results:[{index,id,slug,status}]}`. Errors add 413, 422 too_many_items, per-item duplicate_idempotency_key/duplicate_slug. Charged one rate token. |
| POST | `/media` | Raw image bytes (jpg/png/gif/webp), 16 MiB cap. Only mounted when a media dir is configured. | 201 `{name, url:"/media/<sha256>.<ext>", content_type, size, sha256}`. Errors: 401/403, 415, 413, 400 empty_image, 405. |

### Lane 3: read (any valid token, no scope, not rate-limited)

| Method | Path | Out |
|---|---|---|
| GET | `/authors` | `{authors:[{handle,name,bio,avatar,gender,about,style,topics,sources,metadata,deleted,created_at,updated_at}]}`. `?include_deleted=true`. `style` is served (the panel edits it) but the public site never renders it; `sources` is the slug-sorted attached-source set. |
| GET | `/authors/{handle}/sources` | `{handle, sources}` (slug-sorted). 404 on an absent author. |
| GET | `/topics` | `{topics:[{slug,label,description,metadata,deleted,created_at,updated_at}]}`. |
| GET | `/portadas` | `{portadas:[{date,entries:[{slug,role}],recomendado,deleted,created_at,updated_at}]}`. |
| GET | `/sources` | `{sources:[{slug,domain,homepage,description,feed_urls,feed_type,language,ownership_group,lean,enabled,status,last_checked,last_ok,metadata,deleted,created_at,updated_at}]}`. `?include_deleted=true`. |
| GET | `/articles` | `{articles:[{slug,title,section,author,published_at,topics,metadata,has_media,card_type,deleted,content_hash}], total}` (body omitted from list items). `card_type` is the card label (text/image/youtube/video). Query -> Filter: section, author, topic, q, title_subtitle_q, from, to, limit, offset, order, include_deleted. 400 invalid_query. |
| GET | `/articles:days` | `{days:[{date,count}]}` with distinct UTC publication dates matching the same read filters used by `/articles`, including author/topic/q/title_subtitle_q and order. The admin UI uses it as a lightweight date index. |
| GET | `/articles/{slug}` | Full article incl. `body`. A soft-deleted article is still returned with `deleted=true`. 404 not_found. |
| GET | `/media/{name}` | Public, keyless, immutable-cached raw image. `name` must match `^[a-f0-9]{64}\.(jpg\|png\|gif\|webp)$`. 404 not_found. |

### Lane 4: operator / admin write (scope `admin:write`, not rate-limited)

`admin:write` is deliberately distinct from `articles:write`, so an agent publish key can never reach
edit/delete.

| Method | Path | In | Out |
|---|---|---|---|
| POST | `/authors` | `{handle*, name, bio, avatar, gender, about, style, topics?, sources?, metadata}` (upsert). A present `sources` array replaces the attached-source set in the same call; an omitted `sources` leaves the join untouched. | 200 (400 on missing handle) |
| DELETE | `/authors/{handle}` | (source links are kept, so a restore brings them back) | 204 (404 on absent handle) |
| POST | `/authors/{handle}/restore` | | 200 (404 on absent) |
| PUT | `/authors/{handle}/sources` | `{sources:[slug]}` (replaced wholesale; deduped, blanks dropped) | 200 `{handle, sources}` (404 on absent author) |
| POST | `/topics` | `{slug*, label, description, metadata}` (upsert) | 200 |
| DELETE | `/topics/{slug}` | | 204 |
| POST | `/topics/{slug}/restore` | | 200 |
| POST | `/portadas` | `{date*, entries:[{slug,role}], recomendado}` (replaced wholesale) | 200 (400 on missing date) |
| DELETE | `/portadas/{date}` | | 204 |
| POST | `/portadas/{date}/restore` | | 200 |
| POST | `/sources` | `{domain*, slug?, homepage, description, feed_urls?, feed_type, language, ownership_group, lean, enabled?, status, last_checked, last_ok, metadata}` (upsert). `slug` defaults to the slugified domain; `lean`/`feed_type` are validated. | 200 (400 on missing domain or invalid lean/feed_type) |
| DELETE | `/sources/{slug}` | (also detaches the source from every author) | 204 (404 on absent slug) |
| POST | `/sources/{slug}/restore` | (author links are NOT re-attached) | 200 (404 on absent) |
| PUT | `/articles/{slug}` | `{title,body,author,section,topics?,metadata?}`; `id/slug/created_at` preserved | 200 (404 not_found, 409 edit_conflict on content-hash collision) |
| DELETE | `/articles/{slug}` | | 204 |
| POST | `/articles/{slug}/restore` | | 204 |

### Lane 5: browser session (the folded-in admin panel)

The operator panel is served by this same process (`internal/adminweb`, an outer wrap around the
Lanes 1-4 mux; enabled only when a panel session key + login-token hash are configured). It is an
ADDITIONAL front door beside the bearer lanes, never a replacement: a request carrying
`Authorization: Bearer ...` passes through untouched, so Lanes 1-4 are byte-identical for the CLI and
the brain. This lane is exercised by `internal/adminweb/adminweb_test.go` (not the Seam B conformance
test, which covers the bearer API only).

| Method | Path | Behavior |
|---|---|---|
| GET | `/login` | Login page (open). A valid session redirects to `/`. |
| POST | `/login` | Form/JSON `token`; on a constant-time hex-SHA-256 match sets the `cnz_panel` session cookie (HttpOnly) + `cnz_panel_csrf` cookie (readable), both signed HMACs bound to the expiry, then 303 `/`. A miss re-renders the form (no cookie). |
| POST | `/logout` | Clears both cookies, 303 `/login`. |
| GET | `/`, `/app.js`, `/api.js`, `/styles.css`, `/slugify.js`, `/components/*` | The embedded buildless SPA, behind the session gate (the login stylesheet stays open). |

Session gate rules: a valid `cnz_panel` session maps the request to the operator identity in-process
(scopes `articles:write` + `articles:publish-any` + `admin:write`) so the SPA reaches Lanes 3-4 with
NO bearer token; any state-changing method from a session must carry a matching `X-CSRF-Token`
(double-submit) or it is 403 `invalid_csrf`; an unauthenticated browser navigation is 303 `/login`,
any other unauthenticated request is 401. `/healthz` and `/media/*` stay open (public keyless reads).
