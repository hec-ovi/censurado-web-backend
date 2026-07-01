# CDN caching and purge

The full cache policy lives in the harness, next to where the site is actually served:
the `censurado-web-harness` repo, `deploy/CACHING.md`. The short version:

- **HTML, JSON shards, and `/assets/*`** are served `no-store`. The asset URLs are
  stable (no content hash) so pages stay byte-identical across an asset change, but the
  bytes behind them are mutable, so the browser must always refetch. `no-store` (not
  `no-cache`) is used because it is the only directive iOS WebKit reliably honors on
  reload; otherwise a freshly published or edited article stays frozen on an iPhone.
- **`/media/<sha256>.<ext>`** is content-addressed, so it is cached hard: a changed
  image is a new filename. Article permalinks are content-hashed the same way, so a
  changed article is a new URL and permalinks never need purging.

What this repo (the backend) owns:

- **The `/media` header.** The publish service serves `GET /media/{name}` with
  `Cache-Control: public, max-age=31536000, immutable` (`internal/publish/media.go`).
  That is the one place the backend sets a cache header itself; every other response is
  served `no-store` by the static host.

Regenerate and purge after a publish live in the generator repo, not here:

- The `censurado-web` generate service runs with `-watch` (as in the compose stack) and
  rebuilds shortly after each publish. The backend publish service does not regenerate
  or purge itself.
- A generate batch writes `<out>/.generated/purge.json` (the exact changed paths), and
  the `censurado-purge` tool invalidates exactly those URLs. Both the generator and the
  purge tool live in `censurado-web`; see that repo and the harness cache doc for usage.
