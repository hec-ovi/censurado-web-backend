---
name: censurado-publish-batch
description: Publish MANY news articles to the Censurado portal in one atomic request as an authenticated producer. Use when an agentic newsroom has a batch of finished articles (50, 100, or more) to submit together instead of one POST per article. Posts directly to the HTTP write API; handles per-item idempotency and all-or-nothing semantics. For a single article use the censurado-publish CLI (SKILL.md) instead.
---

# Publishing a batch of articles

When you have many finished articles to publish at once (a scheduled newsroom run,
for example), post them together to `POST /articles:batch` instead of calling the
single endpoint once per article. The whole batch commits in one transaction:
either every article is stored or none is. This is for producers that POST to the
API directly; the `censurado-publish` CLI publishes one article at a time (see
`SKILL.md`).

## Setup

- `CENSURADO_API_URL` the base URL of the publish API, for example
  `https://api.example.internal`.
- `CENSURADO_PUBLISH_TOKEN` your key, in the form `<prefix>.<secret>`. A batch that
  mixes authors needs a key holding the `articles:publish-any` scope (the operator or
  newsroom key); a plain author key may only publish as its own author. Keep it
  secret; never print it.

## The request

`POST {CENSURADO_API_URL}/articles:batch` with header
`Authorization: Bearer <token>` and a JSON body:

```json
{
  "articles": [
    { "title": "...", "body": "# H\n\nbody", "author": "ada", "section": "tech", "topics": ["ai"], "idempotency_key": "run7-0001" },
    { "title": "...", "body": "...",          "author": "bo",  "section": "world",                  "idempotency_key": "run7-0002" }
  ]
}
```

Each item is the same article object you would send to the single endpoint (title,
body in Markdown, author, section, optional topics, slug, published_at, and a
metadata object including the media keys `image`, `image_alt`, `youtube`), plus a
required `idempotency_key`. There is no `Idempotency-Key` header for a batch: one
header cannot carry many keys, so each item carries its own. The key must be unique
within the batch. There is no length limit on any body.

The full machine-readable contract is `contracts/batch-request.schema.json` and
`contracts/batch-response.schema.json` in the repository; the per-item article shape
is the same `contracts/article.schema.json` as the single endpoint.

## Rules

- **Atomic.** Every item is validated first. If any item is invalid (a missing
  field, an author you may not publish as, or a duplicate idempotency_key or slug
  within the batch), the whole request returns `422` and NOTHING is written. Fix the
  named items and resend.
- **Idempotent per item.** Resending the same batch with the same per-item keys is
  safe: already-published items come back as `deduplicated` and nothing doubles.
  After an uncertain network result, resend the exact same batch.
- **One author unless privileged.** With a plain author key, every item's `author`
  must be your own. A multi-author batch needs the `articles:publish-any` scope.
- **Limits.** A batch may carry up to a configured maximum (default 500 items;
  `CENSURADO_PUBLISH_BATCH_MAX_ITEMS`). The only other limit is a generous request
  byte cap, never a per-article content limit.
- **One request.** A batch counts as a single request against the rate limiter, so a
  large batch is not throttled item by item.

## The response

On success, `201` if any item was created or `200` if every item was already
present, with one result per item, in order:

```json
{ "results": [
  { "index": 0, "id": "42", "slug": "go-1-26-ships", "status": "created" },
  { "index": 1, "id": "7",  "slug": "markets-dip",   "status": "deduplicated" }
] }
```

On a failed validation, `422` with a per-item error list and no writes:

```json
{ "status": 422, "code": "validation_failed",
  "errors": [ { "index": 1, "code": "author_mismatch", "detail": "..." } ] }
```

Status codes: `201`/`200` success, `422` validation failure (atomic, nothing
written), `401` missing or invalid token, `403` missing the `articles:write` scope,
`413` body over the byte cap.

## Example (curl)

```sh
curl -sS -X POST "$CENSURADO_API_URL/articles:batch" \
  -H "Authorization: Bearer $CENSURADO_PUBLISH_TOKEN" \
  -H "Content-Type: application/json" \
  --data @batch.json
```

## Example (Python)

```python
import os, requests

items = [
    {"title": "...", "body": "...", "author": "ada", "section": "tech", "idempotency_key": "run7-0001"},
    # ... 50 or 100 more ...
]
resp = requests.post(
    f"{os.environ['CENSURADO_API_URL']}/articles:batch",
    headers={"Authorization": f"Bearer {os.environ['CENSURADO_PUBLISH_TOKEN']}"},
    json={"articles": items},
)
if resp.status_code == 422:
    for e in resp.json()["errors"]:
        print("rejected item", e["index"], e["code"], e.get("detail", ""))
else:
    resp.raise_for_status()
    for r in resp.json()["results"]:
        print(r["index"], r["status"], r["slug"])
```
