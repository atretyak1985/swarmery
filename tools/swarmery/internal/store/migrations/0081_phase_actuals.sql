-- 0081: phase_actuals — what a finished phase run ACTUALLY did.
--
-- Opus 5.5 / learning-loop phase 12, the second half of the predictive loop:
-- phase_forecasts (0079/0080) holds what the planner and the executor EXPECTED a
-- phase to touch; this table holds what its run measurably touched, so a later
-- phase can score the difference. internal/actuals writes it, deterministically
-- and with no LLM involved: git numstat of the run branch against the SHA its
-- worktree was pinned to, plus facts the daemon already records (the run row,
-- turns, run_events, verification_runs, test_run events).
--
-- ONE ROW PER PHASE RUN. There is no phase_runs table: a phase's run state lives
-- on its epic_phases row and is overwritten by the next run. What identifies ONE
-- run is the session uuid the daemon pre-generates at spawn
-- (epic_phases.run_session_uuid) — unique per run, stable across a daemon
-- restart, and the key the transcript is ingested under. It is this table's
-- natural key. phase_id is the join to phase_forecasts (epic_phases ids are
-- stable across rescans: the upsert keys on doc_path).
--
-- NULL MEANS UNKNOWN, NEVER ZERO. Every measured column is nullable, and a
-- missing input leaves its column NULL rather than a fabricated value: no branch
-- or start point ⇒ no files/lines/size; no ingested session ⇒ no cost, no test
-- failures, no fallback; no verification run for THIS run ⇒ no verdict; a run
-- healed by a daemon restart ⇒ no duration (its run_ended_at is the restart
-- time). files_json '[]' is a MEASURED empty change set, distinct from NULL.
--
-- ADVISORY, like the forecast it is scored against: nothing gates on these rows,
-- and a failure to compute them is logged by the writer and never fails a run.
--
-- WHY 0081. migrate.go applies unapplied files in FILENAME order, so a new
-- migration takes the next number above the highest existing one (0080,
-- phase_forecasts.post_hoc_reason) — see 0074's note.
--
-- NO FOREIGN KEY on phase_id, for the reasons 0077 and 0079 give: epic_phases
-- rows are deleted when a phase leaves its plan, and the daemon runs with
-- PRAGMA foreign_keys=ON, where an unindexed FK child column wedged the first
-- retention prune for 11.5 hours (0073). What bounds growth instead: one row per
-- run (upserted on session_uuid, so a recompute replaces rather than appends),
-- and wsingest's phase prune sweeps rows whose phase no longer exists, beside
-- the phase_forecasts sweep.

CREATE TABLE IF NOT EXISTS phase_actuals (
  id                       INTEGER PRIMARY KEY,
  phase_id                 INTEGER NOT NULL,            -- epic_phases.id (no FK, see above)
  session_uuid             TEXT    NOT NULL,            -- the run's identity (epic_phases.run_session_uuid)
  run_state                TEXT    NOT NULL DEFAULT '', -- done | partial | blocked | failed, as stamped
  branch                   TEXT    NOT NULL DEFAULT '', -- the run branch measured ('' = none recorded)
  start_point              TEXT    NOT NULL DEFAULT '', -- the SHA the diff is measured from
  -- JSON [{path, added, removed, binary?}] — NULL when the diff could not be
  -- measured; '[]' when it was measured and empty.
  files_json               TEXT,
  -- JSON array of the distinct directories the files live in, truncated to
  -- area_depth segments from the repo root ('.' for a root-level file).
  areas_json               TEXT,
  area_depth               INTEGER NOT NULL DEFAULT 2,  -- depth areas_json was computed at
  lines_added              INTEGER,
  lines_removed            INTEGER,
  size_band                TEXT,                        -- XS | S | M | L | XL on added+removed
  duration_s               INTEGER,                     -- run_ended_at - run_started_at
  cost_usd                 REAL,                        -- SUM(turns.cost_usd) of the run session
  outcome                  TEXT    NOT NULL DEFAULT '', -- phasediag outcome: completed | partial | noop | failed …
  verify_verdict           TEXT,                        -- pass | fail | inconclusive, of a verification of THIS run
  test_failures            INTEGER,                     -- failing test_run events in the run session
  test_failures_unexpected INTEGER,                     -- … outside the forecast's areas/files/risks; NULL without a forecast
  continuations            INTEGER,                     -- completion-loop resumes (run_events kind='continuation')
  model_fallback           INTEGER,                     -- 1 when the run ENDED on a weaker model than it started on
  source                   TEXT    NOT NULL DEFAULT 'run-end', -- run-end | run-end-settled | backfill
  computed_at              TEXT    NOT NULL,            -- RFC3339
  UNIQUE (session_uuid)
);

-- "this phase's runs, oldest first" — the access path phase 13's scorer and the
-- orphan sweep use. The UNIQUE above already indexes session_uuid.
CREATE INDEX IF NOT EXISTS idx_phase_actuals_phase
  ON phase_actuals(phase_id, id);
