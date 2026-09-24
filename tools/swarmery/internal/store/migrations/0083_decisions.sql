-- 0083: decisions + session_labels + decide_modes — the local decision classifier.
--
-- Opus 5.5 / learning-loop phase 9. internal/decide asks a cheap classifier
-- (rules → a LOCAL OpenAI-compatible server → optionally headless Claude haiku)
-- a typed question at a fork the deterministic rules leave open, and writes
-- EVERY call here so accuracy is measurable and each question can be switched
-- off. The classifier works AROUND Claude runs, never inside them: it informs
-- what the control plane does after a run ends (D1) and adds analytics labels
-- (D2). No hook, context, tool or model/effort choice consults these tables.
--
-- decisions — one row per classifier call, success or failure.
--   question_id   'd1.run_end' | 'd2.task_type' | 'd2.outcome' | 'd2.failure_cause'
--   subject       what was asked about: 'phaserun:<phase id>', 'planrun:<task id>',
--                 or a session uuid for D2
--   input_hash    sha256 of the (truncated) input; the input itself is NOT stored
--   mode          the question's mode at call time: shadow | active
--   rule_value    what the deterministic rules did at that fork (D1: 'continue')
--   acted         1 when an active-mode answer changed what the control plane did
--   ground_truth  what actually happened, recorded later (step 9.3); NULL = unknown
--
-- session_labels — D2's labels, one row per session, written only in active mode.
--
-- decide_modes — the dashboard's per-question mode switch; overrides the env
-- default (SWARMERY_DECIDE_D1 / SWARMERY_DECIDE_D2). No row ⇒ env ⇒ shadow.
--
-- WHY 0083: migrate.go applies unapplied files in FILENAME order; 0082
-- (phase_surprise) is the highest existing one.
--
-- NO FOREIGN KEYS to hot tables (sessions, epic_phases, plan_runs), for the
-- reasons 0073/0077 give: the daemon runs with PRAGMA foreign_keys=ON and an
-- unindexed FK child column wedged the first retention prune for 11.5 hours.

CREATE TABLE IF NOT EXISTS decisions (
  id              INTEGER PRIMARY KEY,
  question_id     TEXT    NOT NULL,
  subject         TEXT    NOT NULL DEFAULT '',
  session_uuid    TEXT    NOT NULL DEFAULT '',
  input_hash      TEXT    NOT NULL,
  answer          TEXT    NOT NULL DEFAULT '',
  probs_json      TEXT    NOT NULL DEFAULT '{}',
  confidence      REAL,
  calibrated      INTEGER NOT NULL DEFAULT 0,
  backend         TEXT    NOT NULL DEFAULT '',
  latency_ms      INTEGER NOT NULL DEFAULT 0,
  mode            TEXT    NOT NULL DEFAULT 'shadow',
  rule_value      TEXT    NOT NULL DEFAULT '',
  acted           INTEGER NOT NULL DEFAULT 0,
  error           TEXT    NOT NULL DEFAULT '',
  ground_truth    TEXT,
  ground_truth_at TEXT,
  created_at      TEXT    NOT NULL
);

-- per-question stats for the Decisions view, newest first
CREATE INDEX IF NOT EXISTS idx_decisions_question ON decisions(question_id, created_at);
-- ground-truth lookups by run / session
CREATE INDEX IF NOT EXISTS idx_decisions_subject ON decisions(subject, id);
CREATE INDEX IF NOT EXISTS idx_decisions_session ON decisions(session_uuid);

CREATE TABLE IF NOT EXISTS session_labels (
  session_uuid   TEXT PRIMARY KEY,
  task_type      TEXT NOT NULL DEFAULT 'unknown',
  outcome        TEXT NOT NULL DEFAULT 'unknown',
  failure_cause  TEXT NOT NULL DEFAULT 'unknown',
  backend        TEXT NOT NULL DEFAULT '',
  labeled_at     TEXT NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_session_labels_labeled ON session_labels(labeled_at);

CREATE TABLE IF NOT EXISTS decide_modes (
  question_id TEXT PRIMARY KEY,
  mode        TEXT NOT NULL CHECK (mode IN ('off','shadow','active')),
  updated_at  TEXT NOT NULL
);
