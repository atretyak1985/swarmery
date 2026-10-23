-- 0132: decision evidence (verify-not-trust phase 12, D19). Every decision (D-n,
-- phase_decisions) and spec criterion (SC-n, spec_criteria) has to survive to
-- proof. wsingest scans each phase doc's `## Completion Report` for the ids it
-- cites (kind 'report'), and the plan review's `round: plan` block maps each id
-- to the phase/commit that implements it or to "missing" (kind 'review'). The
-- dashboard's decision table and the phase PR body's `## Remaining` are built
-- from these rows. Kind 'pr' is reserved for landing PR bodies; the daemon does
-- not store those today (the body is rendered from the doc's own report).
--
-- ref_id is the decision/criterion id ('D-2', 'SC-4'); id stays the rowid, as on
-- every other table.
--
-- status: 'evidence' — the id is cited as done; 'not-implemented' — the report
-- says `not implemented: D-n — <why>` (reason holds the why), never counted as
-- remaining; 'missing' — a plan reviewer looked and found no implementation.
--
-- Keyed by doc_path like review_findings (md is the truth, a renamed doc mints a
-- new epic_phases id): one row per (task, doc, kind, id). A daemon plan review is
-- not written into any doc, so its rows carry the synthetic doc_path
-- 'plan-review:<taskID>' (wsingest.PlanReviewDocPath) and are replaced whole by
-- the next settled plan review. No foreign keys, for the reasons 0123 gives.
--
-- Rollback: DROP TABLE IF EXISTS decision_evidence;
CREATE TABLE IF NOT EXISTS decision_evidence (
  id                INTEGER PRIMARY KEY,
  workspace_task_id INTEGER NOT NULL,
  ref_id            TEXT NOT NULL,                 -- 'D-2' | 'SC-4'
  phase_id          INTEGER,                       -- the phase the evidence names; NULL when none
  doc_path          TEXT NOT NULL DEFAULT '',
  kind              TEXT NOT NULL CHECK (kind IN ('report','pr','review')),
  status            TEXT NOT NULL DEFAULT 'evidence'
                    CHECK (status IN ('evidence','not-implemented','missing')),
  reason            TEXT NOT NULL DEFAULT '',
  excerpt           TEXT NOT NULL DEFAULT '',
  created_at        TEXT NOT NULL,
  UNIQUE (workspace_task_id, doc_path, kind, ref_id)
);
CREATE INDEX IF NOT EXISTS idx_decision_evidence_task ON decision_evidence(workspace_task_id, ref_id);
