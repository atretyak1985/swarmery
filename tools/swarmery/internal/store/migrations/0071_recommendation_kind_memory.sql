-- 0071: extend recommendations.target_kind with 'memory' (agent-memory phase 3).
--
-- R10 (auto-memory index over budget) emits target_kind='memory': the finding is
-- about one project's always-loaded MEMORY.md, which is neither the project's
-- code ('project', R7) nor a session ('session', R9) — accepting it means
-- consolidating an index, and that is its own target class on the Retro page.
--
-- Without this the CHECK from 0063 rejects the row at upsert time and the whole
-- advisor pass fails, exactly as R7/R9 did before 0038. Verified against the
-- current constraint: ('tool','agent','error_group','process','config',
-- 'project','session') — 'memory' is absent.
--
-- SQLite cannot ALTER a CHECK constraint, so the table is rebuilt. The rebuild
-- is NOT free of foreign-key damage, and the `PRAGMA foreign_keys = OFF` that
-- the earlier rebuilds here open with does NOT prevent it:
--
--   * internal/store/migrate.go runs every migration inside db.Begin(), and
--     SQLite documents `PRAGMA foreign_keys` as a NO-OP inside a transaction;
--   * the DSN in internal/store/store.go sets foreign_keys(1), so enforcement is
--     ON for the DROP below;
--   * agent_change_proposals.recommendation_id (0021) is declared
--     `REFERENCES recommendations(id) ON DELETE SET NULL`, and that table is
--     live — written by internal/improve/generate.go, read by
--     internal/api/improve.go.
--
-- So `DROP TABLE recommendations` fires ON DELETE SET NULL and NULLs
-- recommendation_id on every existing proposal row. Hence the capture/restore
-- below: the child column is saved before the drop and written back after the
-- rename, when the same ids exist again in the rebuilt table. The restore joins
-- the rebuilt table, so a row whose parent was already missing is simply left
-- NULL instead of failing the migration.
--
-- Migrations 0038 and 0063 carry the same flaw. They are deliberately NOT fixed
-- here — they are already applied on live databases and re-running them is its
-- own risk; this migration only declines to add a third instance.
--
-- Column list, status vocabulary and the status index are carried over from 0063
-- unchanged. No row changes kind or status here; the vocabulary only widens.

CREATE TEMP TABLE _m0071_proposal_fk AS
  SELECT id, recommendation_id FROM agent_change_proposals;

CREATE TABLE recommendations_new (
  id          INTEGER PRIMARY KEY,
  rule        TEXT NOT NULL,
  target_kind TEXT NOT NULL CHECK (target_kind IN ('tool','agent','error_group','process','config','project','session','memory')),
  target      TEXT NOT NULL,
  title       TEXT NOT NULL,
  detail      TEXT NOT NULL,
  evidence    TEXT NOT NULL,
  status      TEXT NOT NULL DEFAULT 'proposed'
              CHECK (status IN ('proposed','accepted','dismissed','adopted','verified','resolved')),
  dedup_key   TEXT NOT NULL UNIQUE,
  baseline    TEXT,
  created_at  TEXT NOT NULL,
  updated_at  TEXT NOT NULL
);

INSERT INTO recommendations_new
  (id, rule, target_kind, target, title, detail, evidence, status, dedup_key, baseline, created_at, updated_at)
SELECT id, rule, target_kind, target, title, detail, evidence, status, dedup_key, baseline, created_at, updated_at
  FROM recommendations;

DROP TABLE recommendations;
ALTER TABLE recommendations_new RENAME TO recommendations;
CREATE INDEX idx_recommendations_status ON recommendations(status);

-- Put back what the DROP's ON DELETE SET NULL cleared. The JOIN restores only
-- ids that exist in the rebuilt table, so this can never fail the FK check.
UPDATE agent_change_proposals
   SET recommendation_id = (
         SELECT f.recommendation_id
           FROM _m0071_proposal_fk f
          WHERE f.id = agent_change_proposals.id)
 WHERE id IN (
         SELECT f.id
           FROM _m0071_proposal_fk f
           JOIN recommendations r ON r.id = f.recommendation_id);

DROP TABLE _m0071_proposal_fk;
