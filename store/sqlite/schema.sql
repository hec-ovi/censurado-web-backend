-- SQLite schema for the article source of truth.
-- STRICT tables reject the loose typing that a Postgres adapter would also
-- reject, keeping the two dialects honest. The stable hot axes (publish date,
-- author, section) are indexed columns; topics are normalized into a join table
-- so /topic/<slug> navigation is an indexed lookup. The open-ended tail lives in
-- the metadata JSON column.

CREATE TABLE IF NOT EXISTS articles (
  id           INTEGER PRIMARY KEY,
  slug         TEXT NOT NULL UNIQUE,
  title        TEXT NOT NULL,
  body         TEXT NOT NULL,
  author       TEXT NOT NULL,
  section      TEXT NOT NULL,
  published_at TEXT NOT NULL,
  content_hash TEXT NOT NULL UNIQUE,
  metadata     TEXT NOT NULL DEFAULT '{}',
  deleted_at   TEXT NOT NULL DEFAULT '',
  created_at   TEXT NOT NULL
) STRICT;

CREATE INDEX IF NOT EXISTS idx_articles_published_at ON articles (published_at);
CREATE INDEX IF NOT EXISTS idx_articles_author       ON articles (author, published_at);
CREATE INDEX IF NOT EXISTS idx_articles_section      ON articles (section, published_at);

CREATE TABLE IF NOT EXISTS article_topics (
  article_id INTEGER NOT NULL REFERENCES articles (id) ON DELETE CASCADE,
  topic      TEXT NOT NULL,
  PRIMARY KEY (article_id, topic)
) STRICT;

CREATE INDEX IF NOT EXISTS idx_article_topics_topic ON article_topics (topic, article_id);

-- submissions is the append-only audit log and idempotency ledger for the
-- publish path: one row per accepted publish attempt, keyed by idempotency key.
CREATE TABLE IF NOT EXISTS submissions (
  idempotency_key TEXT PRIMARY KEY,
  content_hash    TEXT NOT NULL,
  article_id      TEXT NOT NULL,
  slug            TEXT NOT NULL,
  author          TEXT NOT NULL,
  scopes          TEXT NOT NULL DEFAULT '',
  created_at      TEXT NOT NULL
) STRICT;

-- authors is the operator-owned author registry: the canonical public identity
-- the site renders and the brain mirrors. handle matches the articles.author
-- column as a soft string reference (no foreign key, so existing articles are
-- unaffected). deleted_at is a tombstone ('' = active) so a removed author is
-- hidden from the default listing but kept for audit and re-activation;
-- created_at/updated_at/deleted_at are whole-second RFC3339 TEXT, the same
-- encoding the submissions ledger uses.
CREATE TABLE IF NOT EXISTS authors (
  id         INTEGER PRIMARY KEY,
  handle     TEXT NOT NULL UNIQUE,
  name       TEXT NOT NULL,
  bio        TEXT NOT NULL DEFAULT '',
  avatar     TEXT NOT NULL DEFAULT '',
  metadata   TEXT NOT NULL DEFAULT '{}',
  deleted_at TEXT NOT NULL DEFAULT '',
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL
) STRICT;

-- topics is the operator-owned topic registry: the canonical topic identity the
-- site renders and the brain mirrors. slug matches the article_topics.topic column
-- as a soft string reference (no foreign key, so existing memberships are
-- unaffected). deleted_at is a tombstone ('' = active) so a removed topic is hidden
-- from the default listing but kept for audit and re-activation; the timestamps are
-- whole-second RFC3339 TEXT, the same encoding the submissions ledger uses.
CREATE TABLE IF NOT EXISTS topics (
  id          INTEGER PRIMARY KEY,
  slug        TEXT NOT NULL UNIQUE,
  label       TEXT NOT NULL,
  description TEXT NOT NULL DEFAULT '',
  metadata    TEXT NOT NULL DEFAULT '{}',
  deleted_at  TEXT NOT NULL DEFAULT '',
  created_at  TEXT NOT NULL,
  updated_at  TEXT NOT NULL
) STRICT;
