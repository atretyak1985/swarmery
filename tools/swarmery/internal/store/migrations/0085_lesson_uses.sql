-- 0085: lesson injection and graduation (Opus 5.5 / learning-loop phase 15).
--
-- lesson_uses — one row per (run, lesson) that a headless phase or plan run's
-- prompt carried (internal/lessons/inject.go). session_uuid IS the phase-run id
-- the plan calls phase_run_id: the same value surprise_lessons.source_phase_run
-- and phase_surprise.session_uuid hold (epic_phases.run_session_uuid /
-- plan_runs.run_session_uuid). relied_on is set after the run when the
-- executor cited the lesson's id ("[L-12]") in its assistant text or in the
-- phase doc's Completion Report — advisory, never a gate. Phase 16 reads this
-- table to score a lesson's effectiveness, hence the lesson_id index.
--
-- lesson_promotions — the log of the operator's "promote to CLAUDE.md" action
-- (internal/lessons/promote.go): which lesson was written into which nested
-- CLAUDE.md on which NEW branch, and the commit that holds it. A failed attempt
-- is logged too (error non-empty), so the dashboard can tell "never tried" from
-- "tried and refused".
--
-- WHY 0085: migrate.go applies unapplied files in FILENAME order; 0084
-- (lessons from surprise) is the highest existing one.
--
-- NO FOREIGN KEYS to hot tables (epic_phases, sessions, surprise_lessons) for
-- the reasons 0073/0077/0082 give: PRAGMA foreign_keys=ON plus an unindexed
-- child column wedged the first retention prune for 11.5 hours.

CREATE TABLE IF NOT EXISTS lesson_uses (
  id            INTEGER PRIMARY KEY,
  session_uuid  TEXT    NOT NULL,             -- the run's session uuid (phase_run_id)
  run_kind      TEXT    NOT NULL CHECK (run_kind IN ('phaserun','planrun')),
  phase_id      INTEGER NOT NULL DEFAULT 0,   -- epic_phases.id for a phase run; 0 for a plan run
  task_id       INTEGER NOT NULL DEFAULT 0,   -- the plan's workspace task id (tasks.id)
  lesson_id     INTEGER NOT NULL,             -- surprise_lessons.id (no FK, see above)
  rank          INTEGER NOT NULL,             -- 1-based position in the injected block
  est_tokens    INTEGER NOT NULL DEFAULT 0,   -- the line's estimated cost against the budget
  injected_at   TEXT    NOT NULL,
  relied_on     INTEGER NOT NULL DEFAULT 0,   -- 1 when the run cited the id
  relied_where  TEXT    NOT NULL DEFAULT '',  -- 'transcript' | 'report' | 'transcript,report'
  relied_at     TEXT,
  -- 1 when the id was ALREADY cited in the phase doc's Completion Report at
  -- injection time (an earlier run wrote it): a report citation of such a lesson
  -- is not evidence this run relied on it.
  prior_in_report INTEGER NOT NULL DEFAULT 0,
  UNIQUE (session_uuid, lesson_id)
);

-- phase 16's effectiveness read: every use of one lesson
CREATE INDEX IF NOT EXISTS idx_lesson_uses_lesson ON lesson_uses(lesson_id, id);
-- per-phase reads (the dashboard, the retention sweep)
CREATE INDEX IF NOT EXISTS idx_lesson_uses_phase ON lesson_uses(phase_id);

CREATE TABLE IF NOT EXISTS lesson_promotions (
  id          INTEGER PRIMARY KEY,
  lesson_id   INTEGER NOT NULL,               -- surprise_lessons.id (no FK, see above)
  repo_root   TEXT    NOT NULL,               -- the consumer checkout the branch was cut in
  area_dir    TEXT    NOT NULL,               -- repo-relative directory ('.' = the repo root)
  file_path   TEXT    NOT NULL,               -- repo-relative CLAUDE.md path
  branch      TEXT    NOT NULL DEFAULT '',
  commit_sha  TEXT    NOT NULL DEFAULT '',
  error       TEXT    NOT NULL DEFAULT '',    -- '' on success
  created_at  TEXT    NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_lesson_promotions_lesson ON lesson_promotions(lesson_id, id);
