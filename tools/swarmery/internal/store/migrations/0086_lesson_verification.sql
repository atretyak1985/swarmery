-- 0086: lesson verification, forgetting, and the effort a phase run ran at.
--
-- Opus 5.5 / learning-loop phase 16. A memory that only grows becomes noise:
-- swarmery keeps a lesson only while the surprise of runs in its area stays
-- lower than it was before the lesson existed.
--
-- lesson_effectiveness — ONE ROW PER surprise_lessons row, replaced on every
-- verification pass (internal/lessons/effectiveness.go). The median surprise
-- index of up to window_n scored runs in the lesson's area BEFORE its
-- activation against up to window_n runs AFTER it. median_drop = before −
-- after (positive = the area got less surprising). NULL MEANS NOT ENOUGH DATA,
-- never zero: fewer than min_runs on either side leaves every median column
-- NULL, because "we cannot tell yet" and "it made no difference" are opposite
-- verdicts and only the second may propose a retirement. relied_rate is the
-- share of the lesson's injections (lesson_uses, 0085) the run cited; NULL when
-- the lesson was never injected.
--
-- lesson_retirements — the retirement QUEUE. A pass PROPOSES; it never retires
-- silently. reason is one of: ineffective (no drop after ≥ min_runs area runs),
-- stale (the area's code churned past a share of its lines since activation),
-- unused_60d (not injected for 60 days), superseded (another active lesson now
-- carries the same identity). The operator confirms (→ the lesson is retired
-- with retire_reason = reason) or keeps it; a proposal nobody answers for the
-- auto-retire window (14 days by default) is retired by the daemon with the
-- same retire_reason and state 'auto_retired' — the one documented exception
-- to "proposed, not silent". At most ONE open proposal per lesson (the partial
-- unique index), which is what makes the periodic pass idempotent.
--
-- epic_phases.run_effort — the --effort a phase run was spawned with, stamped
-- in the same UPDATE that opens the run (internal/phaserun). The calibration
-- view groups by it; runs from before this column read as "unknown".
--
-- WHY 0086: migrate.go applies unapplied files in FILENAME order; 0085
-- (lesson_uses) is the highest existing one.
--
-- NO FOREIGN KEYS to hot tables (surprise_lessons, epic_phases) for the
-- reasons 0073/0077/0082 give: PRAGMA foreign_keys=ON plus an unindexed child
-- column wedged the first retention prune for 11.5 hours. Every lookup column
-- is indexed instead.

ALTER TABLE epic_phases ADD COLUMN run_effort TEXT;

CREATE TABLE IF NOT EXISTS lesson_effectiveness (
  lesson_id     INTEGER PRIMARY KEY,           -- surprise_lessons.id (no FK, see above)
  window_n      INTEGER NOT NULL,              -- runs taken on each side at most
  min_runs      INTEGER NOT NULL,              -- runs required on each side
  before_n      INTEGER NOT NULL DEFAULT 0,    -- area runs found before activation (≤ window_n)
  after_n       INTEGER NOT NULL DEFAULT 0,    -- area runs found after activation (≤ window_n)
  median_before REAL,                          -- NULL = not enough data
  median_after  REAL,
  median_drop   REAL,                          -- before − after; NULL = not enough data
  uses          INTEGER NOT NULL DEFAULT 0,    -- lesson_uses rows
  relied        INTEGER NOT NULL DEFAULT 0,    -- … of which the run cited the lesson
  relied_rate   REAL,                          -- relied / uses; NULL when never injected
  computed_at   TEXT    NOT NULL
);

CREATE TABLE IF NOT EXISTS lesson_retirements (
  id            INTEGER PRIMARY KEY,
  lesson_id     INTEGER NOT NULL,              -- surprise_lessons.id (no FK, see above)
  reason        TEXT    NOT NULL
                CHECK (reason IN ('ineffective','stale','unused_60d','superseded')),
  detail        TEXT    NOT NULL DEFAULT '',   -- one operator sentence
  evidence_json TEXT    NOT NULL DEFAULT '{}', -- the numbers the reason rests on
  state         TEXT    NOT NULL DEFAULT 'proposed'
                CHECK (state IN ('proposed','confirmed','kept','auto_retired','withdrawn')),
  proposed_at   TEXT    NOT NULL,
  decided_at    TEXT
);

-- one open proposal per lesson: the pass's idempotency
CREATE UNIQUE INDEX IF NOT EXISTS idx_lesson_retirements_open
  ON lesson_retirements(lesson_id) WHERE state = 'proposed';
-- the queue read and the auto-retire sweep: "open proposals, oldest first"
CREATE INDEX IF NOT EXISTS idx_lesson_retirements_state
  ON lesson_retirements(state, proposed_at);
-- per-lesson history (the keep cooldown)
CREATE INDEX IF NOT EXISTS idx_lesson_retirements_lesson
  ON lesson_retirements(lesson_id, id);
