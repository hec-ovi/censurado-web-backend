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
-- created_at/updated_at/deleted_at are whole-second RFC3339 TEXT (not TIMESTAMPTZ)
-- for byte-identical parity with the SQLite adapter and so deleted_at can use ''
-- as the active sentinel.
CREATE TABLE IF NOT EXISTS authors (
  id         BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  handle     TEXT NOT NULL UNIQUE,
  name       TEXT NOT NULL,
  bio        TEXT NOT NULL DEFAULT '',
  avatar     TEXT NOT NULL DEFAULT '',
  metadata   JSONB NOT NULL DEFAULT '{}'::jsonb,
  deleted_at TEXT NOT NULL DEFAULT '',
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL
);

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
