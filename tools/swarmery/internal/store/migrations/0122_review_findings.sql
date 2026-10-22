-- 0122: review findings become data. wsingest projects every `kind: review` block
-- of a phase doc's `## Review` section (mdfence.ParseReviews, D7/D42) — whether
-- the daemon's reviewer or an interactive run-plan wrote it, or an operator
-- backfilled it — into one review_blocks row per (doc, round) and one
-- review_findings row per finding.
--
-- Keyed by doc_path, not by phase id: md is the truth, and a renamed doc mints a
-- new epic_phases id (wsingest.carryAcrossRenames), so like phase_reviews these
-- tables carry no foreign key. phase_id is the row the doc resolved to at the last
-- scan, for joins; wsingest deletes the rows of a block that left the doc.
--
-- The cutoff for D21 ("legacy review") is THIS migration's own
-- schema_migrations.applied_at (store.MigrationAppliedAt): a phase Done before it
-- without a block is legacy_review = 1 — badge only, no status change.
--
-- Rollback: DROP TABLE IF EXISTS review_findings; DROP TABLE IF EXISTS
-- review_blocks; ALTER TABLE epic_phases DROP COLUMN legacy_review;
CREATE TABLE IF NOT EXISTS review_blocks (
  id                   INTEGER PRIMARY KEY,
  workspace_task_id    INTEGER NOT NULL,
  phase_id             INTEGER,
  doc_path             TEXT NOT NULL,
  round                TEXT NOT NULL,            -- "1", "2", … or "plan"
  verdict              TEXT NOT NULL,            -- PASS | FAIL | INCONCLUSIVE
  complete             INTEGER NOT NULL DEFAULT 1,
  scope_left_json      TEXT NOT NULL DEFAULT '[]',
  checked_callers_json TEXT NOT NULL DEFAULT '[]',
  resolves_json        TEXT NOT NULL DEFAULT '[]',
  -- The daemon's prose fallback: the reviewer produced no readable block, so the
  -- daemon wrote a minimal one around the quoted prose (wsingest.AppendReviewFindings).
  unparsable           INTEGER NOT NULL DEFAULT 0,
  source               TEXT NOT NULL DEFAULT 'interactive'
                       CHECK (source IN ('daemon','interactive','backfill')),
  -- The block's own `written_at:` when it carries one, else when the scan first saw it.
  written_at           TEXT NOT NULL,
  UNIQUE (doc_path, round)
);
CREATE INDEX IF NOT EXISTS idx_review_blocks_task ON review_blocks(workspace_task_id);

CREATE TABLE IF NOT EXISTS review_findings (
  id                INTEGER PRIMARY KEY,
  workspace_task_id INTEGER NOT NULL,
  phase_id          INTEGER,
  doc_path          TEXT NOT NULL,
  round             TEXT NOT NULL,
  finding_id        TEXT NOT NULL,             -- the block's `id`, else r<round>-<index>
  type              TEXT CHECK (type IN ('correctness','silent-failure','contract','plan-conformance','quality')),
  file              TEXT NOT NULL DEFAULT '',
  line              INTEGER,
  criterion         TEXT NOT NULL DEFAULT '',
  test_caught       INTEGER NOT NULL DEFAULT 0,
  evidence          TEXT NOT NULL DEFAULT '',
  note              TEXT NOT NULL DEFAULT '',
  -- The round whose `resolves:` cites this finding; NULL while open. Derived from
  -- the doc on every scan, but never cleared by one: a value set outside the doc
  -- survives a rescan (COALESCE in wsingest's upsert).
  resolved_by       TEXT,
  source            TEXT NOT NULL DEFAULT 'interactive'
                    CHECK (source IN ('daemon','interactive','backfill')),
  created_at        TEXT NOT NULL,
  UNIQUE (doc_path, round, finding_id)
);
CREATE INDEX IF NOT EXISTS idx_review_findings_task ON review_findings(workspace_task_id, phase_id);

-- D21: computed by wsingest on every scan (DOC-owned, listed in PhaseUpsertSQL's
-- neighbour UPDATE, never in the run_* family).
ALTER TABLE epic_phases ADD COLUMN legacy_review INTEGER NOT NULL DEFAULT 0;
