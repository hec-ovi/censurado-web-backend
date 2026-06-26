# Censurado deploy (Phase 6B)

Self-hosting topology for the three Go binaries. Single-writer sqlite on one shared
volume in WAL mode:

- publish: the only db writer. Authenticated write API (POST /articles, GET /healthz).
- admin: a db reader. Private operator console (/admin/*); writes the site only on regenerate.
- generate: a db reader. One-shot static site builder, run on demand.

publish also runs a small self-hosted image store ("CDN"): it accepts authenticated
image uploads (POST /media) and serves them (GET /media/{name}) from the media-data
volume. The external web server serves /media/ from that same volume on the public origin.

publish and admin are private: their host ports bind to 127.0.0.1 only (reach them
over an SSH tunnel or private network). The only public surface is the generated
static site on the site-data volume, served by an external CDN or web server that is
not part of this compose.

## Setup

Everything below runs from the `deploy/` directory.

1. Create your env file and fill in real values:

   ```
   cp .env.example .env
   ```

2. Generate the admin credentials (prints token, hash, and session key once):

   ```
   docker compose run --rm admin -gen-credentials
   ```

   Put `CENSURADO_ADMIN_TOKEN_HASH` and `CENSURADO_ADMIN_SESSION_KEY` in `.env`.
   Keep the `CENSURADO_ADMIN_TOKEN` for the operator; do not store it on the server.

3. Create the publish keys file, then mint at least one key:

   ```
   printf '[]\n' > keys.json
   docker compose run --rm publish -gen-key -author ada -scope articles:write
   ```

   Seed `keys.json` as an empty JSON array FIRST. The publish service bind-mounts
   `keys.json`, so if the file is missing Docker creates an empty directory in its
   place and this first `compose run` breaks. (`-gen-key` itself never reads the
   file; it mints and exits.) Then replace the `[]` with the printed JSON entry
   (the file is a JSON array of entries), and hand the printed `TOKEN` to the agent
   as `CENSURADO_PUBLISH_TOKEN`.

4. (Optional) Enable the admin's manual "New article" form. The form publishes
   through the write API, so the admin never writes the database directly. Mint an
   operator key with the privileged publish-any scope so the operator can author as
   any persona, then set `CENSURADO_ADMIN_PUBLISH_TOKEN` in `.env`:

   ```
   docker compose run --rm publish -gen-key -author editor -scope articles:write -scope articles:publish-any
   ```

   Add the printed entry to `keys.json` (next to the agent keys) and put the printed
   `TOKEN` in `.env` as `CENSURADO_ADMIN_PUBLISH_TOKEN`. Leave it blank to keep the
   create form disabled. `CENSURADO_ADMIN_PUBLISH_URL` is already set in the compose
   file. Only the operator key gets `articles:publish-any`; agent keys carry
   `articles:write` alone and stay locked to their own author.

## Run

Bring up the private services (publish + admin):

```
docker compose up -d
```

publish is on 127.0.0.1:8080, admin on 127.0.0.1:8081.

Run a static site build on demand (writes the site-data volume, then exits):

```
docker compose run --rm generate
```

## Media (self-hosted images)

publish doubles as a small self-hosted image store. With `CENSURADO_MEDIA_DIR` set (it
is, in the compose), it accepts an authenticated image upload at `POST /media` (an
`articles:write` key, the same scope as publishing) and serves the stored file at
`GET /media/{name}`. Files are content-addressed (`/media/<sha256>.<ext>`), so the same
image always maps to one immutable, cacheable URL, and an upload is at most 10 MiB of
JPEG, PNG, GIF, or WebP (the type is sniffed from the bytes, not the client's claim).

The admin's New-article form uploads through this endpoint, so the admin stays a
non-writer (it proxies the bytes to publish). An article references its image and a
YouTube video through the open `metadata` object (`image`, `image_alt`, `youtube`),
which the generator already renders, so attaching media needs no contract change.

In production, point the external web server at the media-data volume so it serves
`/media/` on the public origin (next to `/` from site-data). The publish service's own
`GET /media/` is for development and as a fallback. Litestream backs up only the sqlite
database, so back up the media-data volume separately (it holds binary files, not rows).

## Backups (litestream + restore drill)

A litestream sidecar (pinned to 0.3.14) continuously replicates the sqlite db
(snapshots + WAL) to the `replica-data` volume. It only reads article data, so
publish stays the only writer. Config is `deploy/litestream.yml`. For off-site
backups, set the `LITESTREAM_S3_*` vars in `.env` and uncomment the s3 block in
`litestream.yml`.

Backups are only worth having if a restore is proven, so `scripts/restore-drill.sh`
restores a replica end to end and asserts the restored db is good (integrity,
row counts, latest article slug) via `cmd/censurado/restorecheck`. It runs in CI on
every push/PR (the `restore-drill` job) and can be run locally with Docker:

```
./scripts/restore-drill.sh
```

It prints `RESTORE DRILL: PASS (RTO=Ns)` and exits 0 only when every check passes.
The drill is teeth-proved: `DRILL_BREAK=mismatch|garbage|truncate ./scripts/restore-drill.sh`
deliberately breaks the restore and confirms it exits nonzero with `RESTORE DRILL:
FAIL (sev1)`. Point it at a copy of a real db with `DRILL_SEED_DB=/path/to/db`.

## Notes

- The db-data volume is mounted read-write for all four services. WAL readers (admin,
  generate) and litestream need to touch the -wal/-shm sidecars, so a read-only mount
  would break them. Only publish writes article data.
- Images are distroless, non-root, and cgo-free static builds (modernc.org/sqlite is
  pure Go). The litestream sidecar runs as root because it shares the WAL index with
  the nonroot publish writer and writes a Docker-initialized replica volume; it is
  still cap-dropped, no-new-privileges, and read-only-rootfs.
