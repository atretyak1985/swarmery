-- 0111: session_plan — which plan (and phase) a terminal session has open
-- (mods-swarmery-link phase 7, plan in the terminal; decision D5: the daemon
-- owns plan state and its one markdown parser, the mod only draws).
--
-- One row per Claude session, keyed by the session UUID the link mod carries
-- ($.session.id() = sessions.session_uuid), NOT sessions.id: the mod may pick a
-- plan before its first event minted the sessions row, and a uuid survives the
-- row being re-pointed by the ingest heal.
--
--   task_id    the plan's workspace task id as the link names it — tasks.external_id
--              (the canonical yyyy-mm-dd-slug, durable across a DB rebuild), or the
--              numeric tasks.id as text for a workspace task without one
--   phase_id   epic_phases.id as text; NULL = "the plan's active phase"
--   chosen_by  'terminal' — the operator picked it in the terminal
--                           (POST /api/link/plan/select); it is sticky
--              'auto'     — GET /api/link/plan resolved it from the session's
--                           project; a record of what was shown, never a
--                           choice. It never overwrites a 'terminal' row.
--   at         fixed-width ms UTC (the link's tsFormat) of the choice / of the
--              latest automatic resolution — the SPA's "recently fetched" window
--              and the prune horizon key
--
-- NO FOREIGN KEY, for the reason 0107 spells out: the daemon runs with
-- PRAGMA foreign_keys=ON, sessions and tasks rows are re-derived and pruned by
-- other writers, and an FK child column is what wedged the first retention
-- prune for 11.5 hours (0073). Retention is the prune pass's job: a row goes
-- once its session has ended and its `at` is older than the link_events cutoff.
CREATE TABLE IF NOT EXISTS session_plan (
  session_id TEXT PRIMARY KEY,   -- Claude session uuid (sessions.session_uuid, no FK)
  task_id    TEXT NOT NULL,      -- tasks.external_id (or numeric tasks.id as text)
  phase_id   TEXT,               -- epic_phases.id as text; NULL = active phase
  chosen_by  TEXT NOT NULL,      -- terminal | auto
  at         TEXT NOT NULL       -- fixed-width ms UTC
);

-- "which sessions have this plan open" — GET /api/epics/{taskId}/terminal-sessions.
CREATE INDEX IF NOT EXISTS idx_session_plan_task
  ON session_plan(task_id);
