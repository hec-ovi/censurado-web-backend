# censurado-web-backend

The data and API server for Censurado, a static news site. This repo owns the
sqlite store (the source of truth for articles), the authenticated publish API, and
the JSON read API. It runs locally; the public site is static files served by a CDN,
generated from this database by the separate `censurado-web` repo.

The store also owns the author, source, topic, and portada registries and serves them
over the read and operator APIs. Authors (name, voice/style, gender, topics, attached
sources), sources, topics, and the front-page layout all live here; the CLI agent in
`censurado-web-brain` reads and writes them over this API, and each published article
also carries its author fields in `metadata` so the generator can render author and
topic pages.

The system is three code repos plus an image backend:

- **censurado-web-backend** (this): publish API + store + read API + the operator admin panel.
- **censurado-web**: the static-site generator. Reads this database and renders the
  HTML/JSON the CDN serves. Imports this repo's shared libraries (`domain`, `store`,
  `content`, `media`).
- **censurado-web-brain**: the agentic layer (CLI + SKILL + editorial prompts). A CLI
  agent walks it to write and publish articles here over the API, and it carries the one
  Docker Compose that wires the whole stack together (plus ComfyUI).
- **comfyui-strix-docker**: ComfyUI on ROCm, the image backend for hero art.

## What runs here

One module, one long-running binary plus CLI tools, all sharing one sqlite file in
WAL mode (one writer, concurrent readers):

- **publish** (`cmd/censurado/publish`) is the only database writer. It serves:
  - the append-only write API: `POST /articles`, `POST /articles:batch` (idempotency
    key required; deduplicated on content hash);
  - the self-hosted media store: `POST /media`, `GET /media/{name}`;
  - the JSON read API: `GET /authors`, `/topics`, `/portadas`, `/articles`, `/articles/{slug}`
    (any valid key; this is what the generator and the brain read);
  - the operator mutation lane (edit, soft-delete, restore for articles/authors/
    topics, plus portada create/soft-delete/restore for the front-page day-plan
    registry) behind the `admin:write` scope. An operator key carrying `admin:write`
    (plus `articles:publish-any` to author as any persona) unlocks this edit lane;
    the unified panel drives it over the API.

Plus the `cli` publish client (what an author agent uses to POST an article) and the
`replay`/`restorecheck` operator tools.

Keys live in a JSON file that holds only the SHA-256 of each secret. Mint one with
`publish -gen-key -author NAME`; it prints the token once and the ready-to-paste keys
entry.

## Build and test

There is no host Go toolchain; everything runs in the pinned `golang:1.26.4-alpine`
container. With `make` available:

```sh
make test     # go test ./... in the container
make build    # go build ./...
make vet
```

Without `make`, run the same command the Makefile wraps (`docker run ... go test ./...`).

The container writes Go's module and build caches into repo-local `.gomodcache/` and
`.gocache/` (both gitignored, a few hundred MB each). Treat `.gomodcache/` like a Go
`node_modules`: it is vendored dependency code, not ours. Skip it when grepping, auditing,
or counting tests; it is full of third-party `*_test.go` files that are not part of this repo.

## Run (self-hosting)

`deploy/docker-compose.yml` brings up publish + a litestream backup sidecar,
sharing the database and media volumes. The API binds to `127.0.0.1`; reach it over
a private network or an SSH tunnel. Copy `deploy/.env.example` to `deploy/.env`, seed
`deploy/keys.json` with `[]`, then mint keys. See `deploy/README.md`.

## Public packages

`domain`, `store` (+ `store/sqlite`), `content`, and `media` live at
the module root (not under `internal/`) because the `censurado-web` generator imports
them. The server-only `publish` package stays under `internal/`.

## Pending (panel)

Two admin-panel features are on the roadmap, not built yet (the canonical list lives in
the `censurado-web-brain` README under "Pending features"):

- **Drag-and-drop layout organizer.** The portada organizer reorders with up/down
  buttons today; a visual drag-and-drop swap is a nice-to-have.
- **Analytics / BI dashboard.** One surface for growth: a most-popular-topics chart
  (filtered totals, built to scale to thousands of topics), authors ranked by likes,
  authors with the fewest articles, and statistical/growth modeling. Note: author-likes
  needs a reactions data source the backend does not hold yet (reactions live in the
  downstream Cloudflare Pages reactions function, not this store).
