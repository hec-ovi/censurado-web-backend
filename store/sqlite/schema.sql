-- SQLite schema for the article source of truth.
-- STRICT tables reject loose typing, so a wrong-typed value fails at write time
-- instead of silently coercing. The stable hot axes (publish date,
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
-- the site renders. handle matches the articles.author column as a soft string
-- reference (no foreign key, so existing articles are unaffected). gender/about/
-- style/topics are first-class profile columns a human author or operator edits over
-- the admin panel: about is the long public about-page text, style is the private
-- voice/writing prompt the public site never renders, and topics is a JSON array of
-- the author's curated profile beats (defaulting to '[]'). deleted_at is a tombstone
-- ('' = active) so a removed author is hidden from the default listing but kept for
-- audit and re-activation; created_at/updated_at/deleted_at are whole-second RFC3339
-- TEXT, the same encoding the submissions ledger uses. (gender/about/style/topics are
-- also added to a pre-existing database by the additive migrate() step in sqlite.go.)
CREATE TABLE IF NOT EXISTS authors (
  id         INTEGER PRIMARY KEY,
  handle     TEXT NOT NULL UNIQUE,
  name       TEXT NOT NULL,
  bio        TEXT NOT NULL DEFAULT '',
  avatar     TEXT NOT NULL DEFAULT '',
  gender     TEXT NOT NULL DEFAULT '',
  about      TEXT NOT NULL DEFAULT '',
  style      TEXT NOT NULL DEFAULT '',
  topics     TEXT NOT NULL DEFAULT '[]',
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

-- portadas is the operator-owned front-page registry: one row per calendar day,
-- keyed by its natural YYYY-MM-DD date. entries (an ordered list of {slug, role})
-- and recomendado (a slug list) are stored as JSON TEXT, defaulting to '[]', the
-- same self-contained encoding the metadata columns use. deleted_at is a tombstone
-- ('' = active) so a removed day is hidden from the default listing but kept for
-- audit and re-activation; the timestamps are whole-second RFC3339 TEXT, the same
-- encoding the submissions ledger uses.
CREATE TABLE IF NOT EXISTS portadas (
  id          INTEGER PRIMARY KEY,
  date        TEXT NOT NULL UNIQUE,
  entries     TEXT NOT NULL DEFAULT '[]',
  recomendado TEXT NOT NULL DEFAULT '[]',
  deleted_at  TEXT NOT NULL DEFAULT '',
  created_at  TEXT NOT NULL,
  updated_at  TEXT NOT NULL
) STRICT;

-- sources is the operator-owned news-source registry: the outlets the newsroom reads
-- and the political-lean axis the sourcing floor balances across. slug is the stable
-- key (the domain slugified, e.g. example-com); domain is the bare host and is unique.
-- lean ('right'|'neutral'|'left') and feed_type are validated in the HTTP layer (an
-- ALTER-friendly choice, like articles.section). feed_urls is a JSON
-- array (defaulting to '[]'); enabled (1/0) is the discovery toggle, distinct from
-- deleted_at, the tombstone. status/last_checked/last_ok carry the health-checker's
-- last result. The timestamps are whole-second RFC3339 TEXT, as elsewhere.
CREATE TABLE IF NOT EXISTS sources (
  id              INTEGER PRIMARY KEY,
  slug            TEXT NOT NULL UNIQUE,
  domain          TEXT NOT NULL UNIQUE,
  homepage        TEXT NOT NULL DEFAULT '',
  description     TEXT NOT NULL DEFAULT '',
  feed_urls       TEXT NOT NULL DEFAULT '[]',
  feed_type       TEXT NOT NULL DEFAULT 'auto',
  language        TEXT NOT NULL DEFAULT 'es',
  ownership_group TEXT NOT NULL DEFAULT '',
  lean            TEXT NOT NULL DEFAULT 'neutral',
  enabled         INTEGER NOT NULL DEFAULT 1,
  status          TEXT NOT NULL DEFAULT 'unknown',
  last_checked    TEXT NOT NULL DEFAULT '',
  last_ok         TEXT NOT NULL DEFAULT '',
  metadata        TEXT NOT NULL DEFAULT '{}',
  deleted_at      TEXT NOT NULL DEFAULT '',
  created_at      TEXT NOT NULL,
  updated_at      TEXT NOT NULL
) STRICT;

-- author_sources links an author to the sources it reads (a many-to-many). Both
-- columns are soft string references (author_handle -> authors.slug is authors.handle;
-- source_slug -> sources.slug), not foreign keys, so a link can be created before the
-- source is registered and survives a handle/slug it does not yet know. DeleteSource
-- removes an outlet's rows here (detach from all authors); SetAuthorSources replaces
-- one author's rows wholesale.
CREATE TABLE IF NOT EXISTS author_sources (
  author_handle TEXT NOT NULL,
  source_slug   TEXT NOT NULL,
  PRIMARY KEY (author_handle, source_slug)
) STRICT;

CREATE INDEX IF NOT EXISTS idx_author_sources_source ON author_sources (source_slug, author_handle);

-- recomendado is the site's single GLOBAL "Recomendado" list: one ordered slug list
-- the front page renders as its editor's-pick rail, independent of any day (it
-- persists across days until an operator changes it, unlike the per-day portada
-- entries). Exactly one row is ever stored (id = 1, enforced by the CHECK); slugs is
-- a JSON array (defaulting to '[]'), and updated_at is whole-second RFC3339 TEXT like
-- the other registries.
CREATE TABLE IF NOT EXISTS recomendado (
  id         INTEGER PRIMARY KEY CHECK (id = 1),
  slugs      TEXT NOT NULL DEFAULT '[]',
  updated_at TEXT NOT NULL DEFAULT ''
) STRICT;

-- schedules is the operator-owned batch-run schedule registry: when the executor
-- fires the newsroom's daily-edition batch. slug is the stable key (the name
-- slugified, like sources.slug). times / weekdays / monthdays / authors / runs are
-- JSON arrays (defaulting to '[]'), the same self-contained encoding the other
-- registries use; runs is the recent-run strip (newest first, capped in code) and is
-- written only by RecordScheduleRun, never by the upsert. enabled (1/0) is the
-- operator's toggle, distinct from deleted_at, the tombstone. cadence and mode are
-- validated in the HTTP layer (ALTER-friendly, like articles.section). The
-- timestamps are whole-second RFC3339 TEXT, as elsewhere.
CREATE TABLE IF NOT EXISTS schedules (
  id         INTEGER PRIMARY KEY,
  slug       TEXT NOT NULL UNIQUE,
  name       TEXT NOT NULL,
  cadence    TEXT NOT NULL DEFAULT 'daily',
  times      TEXT NOT NULL DEFAULT '[]',
  weekdays   TEXT NOT NULL DEFAULT '[]',
  monthdays  TEXT NOT NULL DEFAULT '[]',
  mode       TEXT NOT NULL DEFAULT 'preview',
  authors    TEXT NOT NULL DEFAULT '[]',
  enabled    INTEGER NOT NULL DEFAULT 1,
  runs       TEXT NOT NULL DEFAULT '[]',
  metadata   TEXT NOT NULL DEFAULT '{}',
  deleted_at TEXT NOT NULL DEFAULT '',
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL
) STRICT;

-- frontend_text and panel_text are the UI-string catalogs: every human-facing label
-- the public site (frontend_text) and the operator admin panel (panel_text) render,
-- stored as (key, lang) pairs so the product is translated by adding rows, not by
-- editing code. key is the stable lookup id (e.g. 'nav.about'); lang is the language
-- code ('en' is the base every key is authored in, 'es'/others are translations a
-- one-time CLI or the panel fills in); value is the rendered string. The two are the
-- same shape but separate tables so the reader-facing site and the operator panel
-- version independently. They mirror the registry conventions: (key, lang) is the
-- natural unique key, deleted_at is the tombstone ('' = active), metadata is the JSON
-- tail, and the timestamps are whole-second RFC3339 TEXT like the other registries.
CREATE TABLE IF NOT EXISTS frontend_text (
  id         INTEGER PRIMARY KEY,
  key        TEXT NOT NULL,
  lang       TEXT NOT NULL DEFAULT 'en',
  value      TEXT NOT NULL DEFAULT '',
  metadata   TEXT NOT NULL DEFAULT '{}',
  deleted_at TEXT NOT NULL DEFAULT '',
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL,
  UNIQUE (key, lang)
) STRICT;

CREATE INDEX IF NOT EXISTS idx_frontend_text_lang ON frontend_text (lang, key);

CREATE TABLE IF NOT EXISTS panel_text (
  id         INTEGER PRIMARY KEY,
  key        TEXT NOT NULL,
  lang       TEXT NOT NULL DEFAULT 'en',
  value      TEXT NOT NULL DEFAULT '',
  metadata   TEXT NOT NULL DEFAULT '{}',
  deleted_at TEXT NOT NULL DEFAULT '',
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL,
  UNIQUE (key, lang)
) STRICT;

CREATE INDEX IF NOT EXISTS idx_panel_text_lang ON panel_text (lang, key);

-- editorial_text is the newsroom's per-language editorial config: the language-specific
-- writing anchors the workflow prompts apply (lexicon bans/swaps, orthography, slop
-- phrases, attribution and disclaimer exemplars) and the Telegram bot response directive,
-- stored as (key, lang) rows like the two UI catalogs so it versions independently and the
-- panel/CLI edit it the same way. It differs in one respect: there is NO 'en' base row.
-- Each row is authored in its own language (Spanish slop words are not the English ones),
-- so today it carries only 'es'; a new language is generated per language, not translated.
CREATE TABLE IF NOT EXISTS editorial_text (
  id         INTEGER PRIMARY KEY,
  key        TEXT NOT NULL,
  lang       TEXT NOT NULL DEFAULT 'en',
  value      TEXT NOT NULL DEFAULT '',
  metadata   TEXT NOT NULL DEFAULT '{}',
  deleted_at TEXT NOT NULL DEFAULT '',
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL,
  UNIQUE (key, lang)
) STRICT;

CREATE INDEX IF NOT EXISTS idx_editorial_text_lang ON editorial_text (lang, key);
