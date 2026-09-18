-- 0072: extend recommendations.target_kind with 'skill' (agent-memory phase 4).
--
-- R11 (a retro lesson that recurred across >= 3 tasks) emits target_kind='skill':
-- the finding is about a procedure the fleet keeps re-learning, and the action it
-- asks for is an edit to the SKILL.md that should have carried it. Phase 5 of the
-- agent-memory plan routes exactly this kind into the improve loop's proposal
-- generator, so the kind IS the routing key — not cosmetic.
--
-- Without this the CHECK from 0071 rejects the row at upsert time and the whole
-- advisor pass fails, exactly as R7/R9/R10 did before 0038/0063/0071. Verified
-- against the current constraint: ('tool','agent','error_group','process',
-- 'config','project','session','memory') — 'skill' is absent.
--
-- WHY 0072 AND NOT 0070. internal/store/migrate.go applies migrations in FILENAME
-- order, and 0071 rebuilds this table with its vocabulary written out in full. A
-- widening numbered below 0071 would be silently undone by 0071's own CREATE
-- TABLE a moment later, on a fresh database, with no error anywhere. The
-- vocabulary therefore has to be widened AFTER the last rebuild, not before it.
-- For the same reason the agent-memory phase 5 proposals migration is reserved
-- slot 0073, NOT 0070: migrate.go applies any UNAPPLIED file in filename order,
-- so a 0070 written later would run BEFORE 0071/0072 on a fresh database and
-- AFTER them on an already-migrated one — two different schemas, no error
-- anywhere. New migrations always take the next number above the highest one.
--
-- SQLite cannot ALTER a CHECK constraint, so the table is rebuilt, and the
-- rebuild is NOT free of foreign-key damage — see the long note in 0071 for the
-- full account. In short: `PRAGMA foreign_keys` is a NO-OP inside migrate.go's
-- transaction, the DSN enables enforcement, and
-- agent_change_proposals.recommendation_id (0021) is declared
-- `REFERENCES recommendations(id) ON DELETE SET NULL` — so `DROP TABLE
-- recommendations` NULLs recommendation_id on every live proposal row. The
-- capture/restore around the drop puts it back; the restore JOINs the rebuilt
-- table, so a row whose parent was already missing is left NULL rather than
-- failing the migration.
--
-- Column list, status vocabulary and the status index are carried over from 0071
-- unchanged. No row changes kind or status here; the vocabulary only widens.

CREATE TEMP TABLE _m0072_proposal_fk AS
  SELECT id, recommendation_id FROM agent_change_proposals;

CREATE TABLE recommendations_new (
  id          INTEGER PRIMARY KEY,
  rule        TEXT NOT NULL,
  target_kind TEXT NOT NULL CHECK (target_kind IN ('tool','agent','error_group','process','config','project','session','memory','skill')),
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
           FROM _m0072_proposal_fk f
          WHERE f.id = agent_change_proposals.id)
 WHERE id IN (
         SELECT f.id
           FROM _m0072_proposal_fk f
           JOIN recommendations r ON r.id = f.recommendation_id);

DROP TABLE _m0072_proposal_fk;
