-- Postgres schema for the article source of truth. Mirrors the SQLite adapter's
-- shape so the same conformance suite passes against both: stable hot axes
-- (publish date, author, section) as indexed columns, topics normalized into a
-- join table, and the open-ended tail in a JSONB column.

CREATE TABLE IF NOT EXISTS articles (
  id           BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  slug         TEXT NOT NULL UNIQUE,
  title        TEXT NOT NULL,
  body         TEXT NOT NULL,
  author       TEXT NOT NULL,
  section      TEXT NOT NULL,
  published_at TIMESTAMPTZ NOT NULL,
  content_hash TEXT NOT NULL UNIQUE,
  metadata     JSONB NOT NULL DEFAULT '{}'::jsonb,
  deleted_at   TEXT NOT NULL DEFAULT '',
  created_at   TIMESTAMPTZ NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_articles_published_at ON articles (published_at);
CREATE INDEX IF NOT EXISTS idx_articles_author       ON articles (author, published_at);
CREATE INDEX IF NOT EXISTS idx_articles_section      ON articles (section, published_at);

CREATE TABLE IF NOT EXISTS article_topics (
  article_id BIGINT NOT NULL REFERENCES articles (id) ON DELETE CASCADE,
  topic      TEXT NOT NULL,
  PRIMARY KEY (article_id, topic)
);

CREATE INDEX IF NOT EXISTS idx_article_topics_topic ON article_topics (topic, article_id);

-- submissions is the append-only audit log and idempotency ledger for the
-- publish path: one row per accepted publish attempt, keyed by idempotency key.
-- scopes is a space-joined TEXT (not a TEXT[]) so the encoding is byte-identical
-- to the SQLite adapter; created_at is TIMESTAMPTZ like articles.created_at.
CREATE TABLE IF NOT EXISTS submissions (
  idempotency_key TEXT PRIMARY KEY,
  content_hash    TEXT NOT NULL,
  article_id      TEXT NOT NULL,
  slug            TEXT NOT NULL,
  author          TEXT NOT NULL,
  scopes          TEXT NOT NULL DEFAULT '',
  created_at      TIMESTAMPTZ NOT NULL
);

-- authors is the operator-owned author registry (see the SQLite schema for the
-- contract). handle is a soft string reference to articles.author (no FK).
-- gender/about/style are TEXT profile columns and topics is a JSONB array of profile
-- beats (style is the private voice prompt the public site never renders).
-- created_at/updated_at/deleted_at are whole-second RFC3339 TEXT (not TIMESTAMPTZ)
-- for byte-identical parity with the SQLite adapter and so deleted_at can use ''
-- as the active sentinel.
CREATE TABLE IF NOT EXISTS authors (
  id         BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  handle     TEXT NOT NULL UNIQUE,
  name       TEXT NOT NULL,
  bio        TEXT NOT NULL DEFAULT '',
  avatar     TEXT NOT NULL DEFAULT '',
  gender     TEXT NOT NULL DEFAULT '',
  about      TEXT NOT NULL DEFAULT '',
  style      TEXT NOT NULL DEFAULT '',
  topics     JSONB NOT NULL DEFAULT '[]'::jsonb,
  metadata   JSONB NOT NULL DEFAULT '{}'::jsonb,
  deleted_at TEXT NOT NULL DEFAULT '',
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL
);

-- Additive columns for a pre-existing authors table: CREATE TABLE IF NOT EXISTS
-- leaves an existing table untouched, so gender/about/style/topics are added
-- explicitly. ADD COLUMN IF NOT EXISTS is idempotent (a no-op on a fresh table that
-- already has them from the CREATE above), the Postgres analogue of the SQLite
-- migrate() step.
ALTER TABLE authors ADD COLUMN IF NOT EXISTS gender TEXT NOT NULL DEFAULT '';
ALTER TABLE authors ADD COLUMN IF NOT EXISTS about  TEXT NOT NULL DEFAULT '';
ALTER TABLE authors ADD COLUMN IF NOT EXISTS style  TEXT NOT NULL DEFAULT '';
ALTER TABLE authors ADD COLUMN IF NOT EXISTS topics JSONB NOT NULL DEFAULT '[]'::jsonb;

-- topics is the operator-owned topic registry (see the SQLite schema for the
-- contract). slug is a soft string reference to article_topics.topic (no FK).
-- created_at/updated_at/deleted_at are whole-second RFC3339 TEXT (not TIMESTAMPTZ)
-- for byte-identical parity with the SQLite adapter and so deleted_at can use ''
-- as the active sentinel.
CREATE TABLE IF NOT EXISTS topics (
  id          BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  slug        TEXT NOT NULL UNIQUE,
  label       TEXT NOT NULL,
  description TEXT NOT NULL DEFAULT '',
  metadata    JSONB NOT NULL DEFAULT '{}'::jsonb,
  deleted_at  TEXT NOT NULL DEFAULT '',
  created_at  TEXT NOT NULL,
  updated_at  TEXT NOT NULL
);

-- portadas is the operator-owned front-page registry (see the SQLite schema for the
-- contract). date is the natural YYYY-MM-DD key. entries and recomendado are JSONB
-- (mirroring the metadata columns), defaulting to '[]'. created_at/updated_at/
-- deleted_at are whole-second RFC3339 TEXT (not TIMESTAMPTZ) for byte-identical
-- parity with the SQLite adapter and so deleted_at can use '' as the active sentinel.
CREATE TABLE IF NOT EXISTS portadas (
  id          BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  date        TEXT NOT NULL UNIQUE,
  entries     JSONB NOT NULL DEFAULT '[]'::jsonb,
  recomendado JSONB NOT NULL DEFAULT '[]'::jsonb,
  deleted_at  TEXT NOT NULL DEFAULT '',
  created_at  TEXT NOT NULL,
  updated_at  TEXT NOT NULL
);

-- sources is the operator-owned news-source registry (see the SQLite schema for the
-- contract). slug is the stable key; domain is the unique bare host. lean/feed_type
-- are validated in the HTTP layer. feed_urls is a JSONB array; enabled is a BOOLEAN
-- discovery toggle (distinct from the deleted_at tombstone). The timestamps are
-- whole-second RFC3339 TEXT for byte-identical parity with the SQLite adapter.
CREATE TABLE IF NOT EXISTS sources (
  id              BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  slug            TEXT NOT NULL UNIQUE,
  domain          TEXT NOT NULL UNIQUE,
  homepage        TEXT NOT NULL DEFAULT '',
  description     TEXT NOT NULL DEFAULT '',
  feed_urls       JSONB NOT NULL DEFAULT '[]'::jsonb,
  feed_type       TEXT NOT NULL DEFAULT 'auto',
  language        TEXT NOT NULL DEFAULT 'es',
  ownership_group TEXT NOT NULL DEFAULT '',
  lean            TEXT NOT NULL DEFAULT 'neutral',
  enabled         BOOLEAN NOT NULL DEFAULT TRUE,
  status          TEXT NOT NULL DEFAULT 'unknown',
  last_checked    TEXT NOT NULL DEFAULT '',
  last_ok         TEXT NOT NULL DEFAULT '',
  metadata        JSONB NOT NULL DEFAULT '{}'::jsonb,
  deleted_at      TEXT NOT NULL DEFAULT '',
  created_at      TEXT NOT NULL,
  updated_at      TEXT NOT NULL
);

-- author_sources links an author to the sources it reads (see the SQLite schema for
-- the contract). Both columns are soft string references (no FK).
CREATE TABLE IF NOT EXISTS author_sources (
  author_handle TEXT NOT NULL,
  source_slug   TEXT NOT NULL,
  PRIMARY KEY (author_handle, source_slug)
);

CREATE INDEX IF NOT EXISTS idx_author_sources_source ON author_sources (source_slug, author_handle);
