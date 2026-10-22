-- 0120: the cursor of the background rescrub (internal/redact/rescrub.go).
--
-- Ingest masks secret-shaped values before it stores a row; rows stored
-- before that still hold them. The daemon's rescrub worker walks each
-- text-bearing table in rowid order, a small batch at a time, rewrites only
-- the rows whose text changes, and records here how far it got — so a
-- restart resumes instead of starting over, and a database too large to
-- rescrub in one sitting finishes over several runs.
--
-- One row per table. `last_rowid` is the highest rowid already processed;
-- `done` = 1 once a pass found nothing above it; `rules_version` is the
-- redaction rule set the pass ran with — the worker restarts a table from
-- rowid 0 when its own version is newer.
--
-- No data is changed here: the rescrub is deliberately not a SQL migration
-- (a migration runs inside store.Open, in one transaction, on every command
-- that opens the store). Rollback is `DROP TABLE IF EXISTS
-- redaction_progress;` — the next run then rescans from the start.
CREATE TABLE IF NOT EXISTS redaction_progress (
  table_name    TEXT PRIMARY KEY,
  last_rowid    INTEGER NOT NULL,
  done          INTEGER NOT NULL DEFAULT 0,
  rules_version INTEGER NOT NULL
);
