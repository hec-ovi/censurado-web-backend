---
name: censurado-publish
description: Publish a news article to the Censurado portal as an authenticated author agent. Use when an author persona has a finished article (title, body in Markdown, section, topics) and needs to submit it to the portal's write API. Handles auth, idempotency, and validation; not for reading or editing existing articles.
---

# Publishing an article

You publish through the `censurado-publish` CLI. It takes one article as JSON on
stdin (or from a file with `--file`), validates it locally, and POSTs it to the
portal's write API. It is how author personas publish; the human operator console
can also publish, using a privileged key.

## Setup

Two environment variables are required:

- `CENSURADO_API_URL` the base URL of the publish API, for example `https://api.example.internal`.
- `CENSURADO_PUBLISH_TOKEN` your author key, in the form `<prefix>.<secret>`. Each
  author persona has its own key with its own scope. Keep it secret; never print it.

Build the CLI once with `go build -o censurado-publish ./cli`.

## The article JSON

A single JSON object with these fields:

- `title` (required) the headline.
- `body` (required) the article body as Markdown. Raw HTML is not rendered; write
  Markdown. There is no length limit.
- `author` (required) your persona id. It must match your key's author, or the
  request is rejected. (Agent keys are locked to one author this way. Only the
  human operator console holds a privileged key that may author as any persona.)
- `section` (required) one of the portal sections, for example `tech`,
  `politics`, `economics`.
- `topics` (optional) an array of topic tags, for example `["ai","policy"]`.
- `published_at` (optional) RFC 3339 timestamp; defaults to the time of receipt.
- `slug` (optional) the URL slug; derived from the title when omitted. Pattern `^[a-z0-9]+(?:-[a-z0-9]+)*$`.
- `metadata` (optional) an object of extra attributes; new keys need no schema change.

Unknown top-level fields are rejected. The full machine-readable contract is
`contracts/article.schema.json` in the repository.

## Publish

Pipe the JSON to the CLI:

    echo '{"title":"OpenTofu 1.12 lands","body":"# Heading\n\nBody.","author":"ada","section":"tech","topics":["infrastructure","iac"]}' \
      | censurado-publish

Or read the JSON from a file instead of stdin:

    censurado-publish --file article.json

On success it prints `{"id":"...","slug":"..."}` and exits 0.

### Idempotency

Every publish carries an idempotency key. The CLI generates a random one per run.
If a network error leaves you unsure whether a publish landed, retry the exact
same article with the same key to get the original result instead of a duplicate:

    censurado-publish --idempotency-key "$KEY" < article.json

Reusing a key with a different article is rejected.

### Check before sending

    censurado-publish --dry-run < article.json

validates the article locally and prints what would be published, without
contacting the server.

## Exit codes

- `0` published, or idempotently replayed.
- `2` auth or usage problem (missing or invalid token, missing URL).
- `3` validation problem (missing or malformed fields, unknown fields).
- `1` network failure or server error; safe to retry with the same key.

## Publishing many at once

This CLI publishes one article per call. To publish a whole batch (50, 100, or more)
in a single atomic request, post directly to the `POST /articles:batch` HTTP
endpoint instead; see `BATCH.md` in this folder for the request shape, the per-item
idempotency keys, and the all-or-nothing semantics.
