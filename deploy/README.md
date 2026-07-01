# Censurado deploy

Self-hosting topology for the backend. Single-writer sqlite on one shared volume in
WAL mode. The compose runs one long-running service plus a backup sidecar:

- publish: the only db writer. Authenticated write API (POST /articles, POST
  /articles:batch), the JSON read API (GET /authors, /topics, /portadas, /articles,
  /articles/{slug}), the media store (POST /media, GET /media/{name}), the operator
  mutation lane (behind the `admin:write` scope), and an unauthenticated GET /healthz.
- litestream: a backup sidecar that continuously replicates the sqlite db.

The static-site generator lives in the separate `censurado-web` repo, not this
compose. Point it at the same db-data volume to build the site.

publish also runs a small self-hosted image store ("CDN"): it accepts authenticated
image uploads (POST /media) and serves them (GET /media/{name}) from the media-data
volume. The external web server serves /media/ from that same volume on the public origin.

publish is private: its host port binds to 127.0.0.1 only (reach it over an SSH
tunnel or private network). The only public surface is the generated static site on
the site-data volume, served by an external CDN or web server that is not part of
this compose.

## Setup

Everything below runs from the `deploy/` directory.

1. Create your env file and fill in real values:

   ```
   cp .env.example .env
   ```

2. Create the publish keys file, then mint at least one key:

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

3. (Optional) Mint an operator key for the edit lane. The operator mutation lane
   (edit, soft-delete, restore, portada management) sits behind the `admin:write`
   scope, and authoring as any persona needs the privileged `articles:publish-any`
   scope. Mint an operator key with both:

   ```
   docker compose run --rm publish -gen-key -author editor -scope articles:write -scope articles:publish-any -scope admin:write
   ```

   Add the printed entry to `keys.json` (next to the agent keys) and hand the printed
   `TOKEN` to the operator. Only the operator key gets `articles:publish-any`; agent
   keys carry `articles:write` alone and stay locked to their own author.

## Run

Bring up the private service (publish + a litestream backup sidecar):

```
docker compose up -d
```

publish is on 127.0.0.1:8080.

To build the static site, run the generator from the `censurado-web` repo against the
same db-data volume. It is not a service in this compose.

## Media (self-hosted images)

publish doubles as a small self-hosted image store. With `CENSURADO_MEDIA_DIR` set (it
is, in the compose), it accepts an authenticated image upload at `POST /media` (an
`articles:write` key, the same scope as publishing) and serves the stored file at
`GET /media/{name}`. Files are content-addressed (`/media/<sha256>.<ext>`), so the same
image always maps to one immutable, cacheable URL, and an upload is at most 10 MiB of
JPEG, PNG, GIF, or WebP (the type is sniffed from the bytes, not the client's claim).

An article references its image and a YouTube video through the open `metadata` object
(`image`, `image_alt`, `youtube`), which the generator already renders, so attaching
media needs no contract change.

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

To validate a restore, run `litestream restore` against the replica and then check
the restored file with `cmd/censurado/restorecheck`, which asserts integrity,
row counts, and the latest article slug and exits nonzero on any mismatch.

## Notes

- The db-data volume is mounted read-write for every service that touches it (publish,
  litestream, and the external censurado-web generator). WAL readers and litestream
  need to touch the -wal/-shm sidecars, so a read-only mount would break them. Only
  publish writes article data.
- Images are distroless, non-root, and cgo-free static builds (modernc.org/sqlite is
  pure Go). The litestream sidecar runs as root because it shares the WAL index with
  the nonroot publish writer and writes a Docker-initialized replica volume; it is
  still cap-dropped, no-new-privileges, and read-only-rootfs.
</content>
</invoke>
