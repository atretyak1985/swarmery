-- 0077: run_events — the per-run timeline of what the completion loop DECIDED.
--
-- Opus 5.5 / learning-loop phase 3. phaserun and planrun no longer stamp `done`
-- on exit 0: they read the run's final assistant text, compare it with the
-- acceptance criteria ticked in the document, and may RESUME the same session up
-- to runcore.MaxContinuations times before settling on done/blocked/partial.
-- That decision chain is invisible in the single run_state column it collapses
-- into — an operator looking at `partial` cannot tell whether the run was nudged
-- twice and made progress each time or stalled on the first turn and repeated
-- itself — so each step is recorded as its own row here and rendered on the
-- phase/plan run panel.
--
-- SHAPE. Deliberately engine-agnostic (engine + subject_id) rather than two
-- tables or two nullable FK columns: phaserun's subject is epic_phases.id and
-- planrun's is plan_runs.workspace_task_id, the two ids are not comparable, and
-- every reader wants "the events of THIS run" — one composite key answers that
-- for both and leaves room for a third engine without a schema change.
--
-- WHY 0077. migrate.go applies any unapplied file in FILENAME order, so a new
-- migration always takes the next number above the highest existing one (0076,
-- planning effort). Re-using a lower slot would order differently on a fresh
-- database than on a migrated one — see 0074's note.
--
-- NO FOREIGN KEY, ON PURPOSE, and this is the interesting decision:
--
--   1. The parent rows are NOT stable. internal/wsingest deletes and re-inserts
--      epic_phases rows on a plan rescan (that is why phaserun.stamp has to log a
--      "row vanished mid-run" case at all). An ON DELETE CASCADE would silently
--      erase a finished run's decision history every time the operator renamed a
--      phase doc; an ON DELETE RESTRICT would make the rescan fail instead.
--   2. The daemon runs with PRAGMA foreign_keys=ON, and an unindexed FK child
--      column is what wedged the first scheduled retention prune for 11.5 hours
--      (fixed by 0073_fk_child_indexes). A history table that only ever grows is
--      precisely the shape that hurts there.
--
-- The cost of no FK is orphan rows after a phase is deleted. NOTHING SWEEPS THEM:
-- internal/prune covers session-scoped tables and worktree_sweeps only, and this
-- table is not in it. What bounds growth instead is runcore.ClearRunEvents, which
-- every engine calls at run START — a subject keeps the events of its CURRENT run
-- and no more, so the table grows with live subjects, not with run history. An
-- orphan event is inert: nothing joins FROM run_events, readers always come the
-- other way with a known (engine, subject).

CREATE TABLE IF NOT EXISTS run_events (
  id           INTEGER PRIMARY KEY,
  engine       TEXT    NOT NULL,             -- 'phaserun' | 'planrun'
  subject_id   INTEGER NOT NULL,             -- epic_phases.id / plan_runs.workspace_task_id
  session_uuid TEXT    NOT NULL DEFAULT '',  -- the resumed session, for cross-linking to the transcript
  kind         TEXT    NOT NULL,             -- 'continuation' | 'blocked' | 'partial' | 'done'
  attempt      INTEGER NOT NULL DEFAULT 0,   -- 1..MaxContinuations for 'continuation', 0 otherwise
  detail       TEXT    NOT NULL DEFAULT '',  -- blocked reason / the unticked list handed to the resume
  created_at   TEXT    NOT NULL              -- RFC3339, the engine's own clock (phaserun/planrun both write RFC3339)
);

-- The ONLY access path: "give me this run's events, oldest first". id is in the
-- index so the ordering is served from it rather than from a sort.
CREATE INDEX IF NOT EXISTS idx_run_events_subject
  ON run_events(engine, subject_id, id);
