# CDN caching and purge

The static site is built to be cached hard at the edge and corrected by an exact
purge after each generate batch, so it can serve millions of readers from the CDN
while still showing new articles quickly. Three URL classes, three policies. The
canonical header values live in one place in code, `internal/cachepolicy`, so a
deploy step can apply them programmatically; the table below mirrors it.

| URL class | examples | Cache-Control |
|---|---|---|
| immutable content | `/a/<slug>-<hash>/`, `/assets/*`, `/media/*` | `public, max-age=31536000, immutable` |
| listings and client data | `/`, `/latest/`, `/section/*`, `/topic/*`, `/author/*`, `feed.xml`, `feed.json`, `atom.xml`, `/shards/*`, `/manifest/*`, sitemaps | `public, max-age=45, stale-while-revalidate=120, stale-if-error=86400` |
| version sentinel | `/latest/version.json` | `public, max-age=10, stale-while-revalidate=60` |

- **Immutable content** is content-addressed: the hash is part of the path, so a new
  version is a new URL. Cache it forever. Article permalinks are never in the purge
  manifest and the purge tool never purges them; a changed article publishes under a
  fresh hashed URL and the old one ages out. Media (`/media/<sha256>.<ext>`) is the
  same, and the publish service already serves it with this exact immutable header,
  the one place this repo sets a header itself.
- **Listings and client data** are mutable. The short fresh window plus
  `stale-while-revalidate` lets the edge serve a stale copy instantly while it
  refetches once in the background, so one background fetch shields the origin from a
  spike while every user still gets a sub-second response. `stale-if-error` rides out
  an origin blip. If a purge is ever missed, the short window self-heals it by the
  next batch instead of pinning stale content.
- **The version sentinel** (`/latest/version.json`) is a tiny file clients poll with
  `If-None-Match` to learn when new content landed (see the live-refresh client). The
  edge answers almost every poll as a `304` with no body, so even millions of pollers
  barely touch the origin. Its tight window bounds the worst-case staleness for a
  reader who is not actively polling. The generator emits it as a byte-stable
  fingerprint of the latest window, so it returns `304` until content actually
  changes.

Why the listing window is short (45 seconds, not minutes): this is a news site whose
readers expect breaking stories fast. The short window plus `stale-while-revalidate`
keeps the worst-case staleness low without raising origin cost, because the edge
absorbs the load and the per-batch purge corrects the exact changed set immediately.

## Regenerate and purge on publish

Two paths keep the edge correct after a publish:

- **Automatic:** the publish service can run a debounced in-process regenerate and
  purge after each publish (set `-out` and `-base-url`, env `CENSURADO_PUBLISH_OUT`
  and `CENSURADO_BASE_URL`; `-regen-debounce` tunes the window). A burst of publishes,
  or a 100-item batch followed by a breaking single, collapses to one rebuild. The
  purge is a dry run unless `CENSURADO_PURGE_ENDPOINT` is set.
- **Manual / scheduled:** run a generate batch, then `censurado-purge` over the
  emitted manifest (below).

Either way the generator writes `<out>/.generated/purge.json` after every batch:
version, timestamp, and the exact root-relative paths that changed or were removed.
The purge step invalidates exactly those URLs, one single-file purge per URL (or one
batch carrying the list), never a wildcard. Purging exactly the changed set keeps the
edge consistent without dumping the whole cache. An active client then sees the
"N new stories" banner within its poll interval plus the purge propagation.

## Per-CDN guardrails

The header values are correct, but edges differ in how they honor the stale
directives. Confirm these when you wire up your CDN:

- **CloudFront** honors `stale-while-revalidate` and `stale-if-error` from the
  response header, but it caps the stale window at the cache policy's Maximum TTL, so
  set Maximum TTL to at least 86400 or the `stale-if-error` window is silently
  truncated. CloudFront has no plain-HTTP purge endpoint: invalidation is a
  SigV4-signed `CreateInvalidation` API call, so the bundled `censurado-purge`
  (`-provider http`) cannot purge it directly. Use a small adapter (a Lambda plus API
  Gateway that reads the path batch and calls `CreateInvalidation`, or a wrapper that
  reads `purge.json` and calls the AWS SDK). This adapter is documented, not bundled;
  with content-hashed immutable permalinks plus the short window, invalidation is the
  escape hatch, not the primary freshness mechanism.
- **Cloudflare** honors the stale directives by default, but `s-maxage`,
  `must-revalidate`, `proxy-revalidate`, and `no-cache` silently disable stale
  serving, so never emit them. Cloudflare has a native HTTP purge API the bundled tool
  works with as-is.
- **Fastly and Bunny** have native HTTP purge that works with the bundled tool. Note
  Bunny dropped RFC 5861 support in 2020 and ignores `stale-while-revalidate` /
  `stale-if-error` unless you enable stale serving per pull zone in the dashboard.

## Usage

Run a generate batch, then purge exactly what it changed:

```
go run ./cmd/censurado/generate -out ./public
go run ./cmd/censurado/purge -provider http \
  -file ./public/.generated/purge.json \
  -endpoint "$CENSURADO_PURGE_ENDPOINT" \
  -base-url "$CENSURADO_BASE_URL"
```

The generator writes the manifest under `<out>/.generated/` (its state dir), so the
purge `-file` default is `./public/.generated/purge.json`. When the generate batch
runs in Docker (`docker compose run --rm generate`, which writes `-out /site`), the
manifest lands at `/site/.generated/purge.json` on the site-data volume; point
`-file` (or `CENSURADO_PURGE_FILE`) there.

The default provider is `dryrun`, which lists the URLs it would purge and makes no
network calls. Drop `-provider http` to preview a batch safely. The auth secret is
read only from the `CENSURADO_PURGE_TOKEN` environment variable (never a flag, so it
stays out of shell history and `ps`) and is never printed. An empty manifest is a
successful no-op, and the tool exits nonzero if any purge fails so a deploy step can
gate on it.
