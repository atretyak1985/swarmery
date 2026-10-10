-- 0106: the independent code review of a phase run (and, later, of a whole plan
-- branch). A phase doc carrying `**Review:** on` gets a second, read-only headless
-- session after its run settles done|partial and before the verifier grades it:
-- the reviewer reads the phase doc and the run's diff and answers PASS | FAIL |
-- INCONCLUSIVE. One row per review.
--
-- No foreign key, like phase_actuals: wsingest may replace an epic_phases row (a
-- renamed doc mints a new id), and the review history must not vanish with it.
-- workspace_task_id holds the epic's tasks.id (epic_phases.workspace_task_id) as
-- decimal text.
CREATE TABLE IF NOT EXISTS phase_reviews (
  id            INTEGER PRIMARY KEY,
  scope         TEXT NOT NULL CHECK (scope IN ('phase','plan')),
  phase_id      INTEGER,                      -- scope=phase
  workspace_task_id TEXT NOT NULL,
  session_uuid  TEXT NOT NULL DEFAULT '',     -- the reviewer session
  run_session_uuid TEXT NOT NULL DEFAULT '',  -- the run being reviewed (scope=phase)
  tree_before   TEXT NOT NULL DEFAULT '', tree_after TEXT NOT NULL DEFAULT '',
  verdict       TEXT NOT NULL,                -- pass | fail | inconclusive
  detail        TEXT NOT NULL DEFAULT '',     -- class: detail, as verify does
  findings      TEXT NOT NULL DEFAULT '',     -- the reviewer's findings block (<= 64 KB)
  fix_round     INTEGER NOT NULL DEFAULT 0,
  cost_usd      REAL,
  started_at    TEXT NOT NULL, finished_at TEXT,
  acked_at      TEXT
);
CREATE INDEX IF NOT EXISTS idx_phase_reviews_phase ON phase_reviews(phase_id, id);
CREATE INDEX IF NOT EXISTS idx_phase_reviews_plan ON phase_reviews(workspace_task_id, scope, id);
-- review_mode is DOC-owned (wsingest re-derives it from the `**Review:**` header on
-- every scan, like verify_mode). review_fix_round is DAEMON-owned (the review stage
-- counts the fix re-runs it started, at most 1) and therefore must never appear in
-- wsingest.PhaseUpsertSQL's DO UPDATE SET list.
ALTER TABLE epic_phases ADD COLUMN review_mode TEXT NOT NULL DEFAULT 'off';
ALTER TABLE epic_phases ADD COLUMN review_fix_round INTEGER NOT NULL DEFAULT 0;
