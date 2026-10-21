-- 0119: the cache behind the Plans page's "In plain words" tabs.
--
-- A plain-language retelling of a phase doc, a plan README, a phase's
-- Completion Report or a plan's summary is generated once by a headless
-- `claude` run (internal/plainsum) and kept here. `key` is the sha256 of the
-- prompt version, kind, language and the exact source text, so an edited doc
-- simply misses the cache and the next view regenerates it; stale rows are
-- never read again and cost only their bytes.
--
-- Rollback is `DROP TABLE IF EXISTS plain_summaries;` — the cache refills on
-- demand.
CREATE TABLE IF NOT EXISTS plain_summaries (
  key        TEXT PRIMARY KEY,
  kind       TEXT NOT NULL,
  lang       TEXT NOT NULL,
  text       TEXT NOT NULL,
  model      TEXT NOT NULL,
  created_at TEXT NOT NULL
);
