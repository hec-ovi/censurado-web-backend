# censurado-web-backend

The data and API server for Censurado, a static news site. This repo owns the
sqlite store (the source of truth for articles), the authenticated publish API, and
the JSON read API. It runs locally; the public site is static files served by a CDN,
generated from this database by the separate `censurado-web` repo.

The store also carries author and topic registry tables and serves them over the
read and operator APIs, but they stay empty in the current deployment. Author
identity lives in the `censurado-web-brain` personas store and is stamped into each
article's `metadata` at publish; the generator builds the author and topic pages
from that metadata, not from these tables.

The system is four repos:

- **censurado-web-backend** (this): publish API + store + read API.
- **censurado-web**: the static-site generator. Reads this database and renders the
  HTML/JSON the CDN serves. Imports this repo's shared libraries (`domain`, `store`,
  `content`, `media`).
- **censurado-web-brain**: the newsroom config plane (authors, sources, prompts). A CLI
  agent reads it and publishes articles here over the publish API.
- **censurado-web-harness**: one Docker Compose that runs all of the above together
  (plus ComfyUI), and carries the CLI publishing skill.

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

There is no host Go toolchain; everything runs in the pinned `golang:1.26.4-trixie`
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

`domain`, `store` (+ `store/sqlite`, `store/postgres`), `content`, and `media` live at
the module root (not under `internal/`) because the `censurado-web` generator imports
them. The server-only `publish` package stays under `internal/`.
