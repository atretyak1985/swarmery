-- 0120: decision ids. Every interview question becomes a stable `D-n` the plan,
-- the phase gate and the dashboard can name.
--
-- One row per decision of a plan. `D-n` is the interview question's sequence
-- (planning_turns.seq); stop-trigger (source 'stop') and review (source 'review')
-- decisions continue the per-task sequence from MAX(seq)+1.
--
-- Two owners, two keys. PROCEED writes the interview's rows BEFORE the planner
-- has written a plan, so no workspace task exists yet: those rows carry only
-- planning_session_id. The workspace scan adopts them onto the task once the plan
-- the wizard wrote is indexed (workspace_task_id set), and from then on the
-- README's `## Decisions` section is the source of truth, upserted on
-- (workspace_task_id, decision_id). SQLite treats NULLs as distinct in a UNIQUE,
-- so neither key collides while the other is still NULL.
--
-- No foreign keys, like phase_reviews: a renamed phase doc mints a new
-- epic_phases id, and a decision must not vanish with it.
--
-- epic_phases.decision_deps is DOC-owned: the JSON list of `D-n` a phase doc
-- declares on its `**Depends on decisions:**` header line, re-derived on every
-- scan. The phase-run start gate refuses a phase while any of them is undecided
-- and expensive to change.
--
-- Rollback: DROP TABLE IF EXISTS phase_decisions; and
-- ALTER TABLE epic_phases DROP COLUMN decision_deps;
CREATE TABLE IF NOT EXISTS phase_decisions (
  id                  INTEGER PRIMARY KEY,
  workspace_task_id   INTEGER,                 -- tasks.id; NULL until the plan is scanned
  planning_session_id INTEGER,                 -- planning_sessions.id; NULL for README-born rows
  decision_id         TEXT NOT NULL,           -- 'D-3'
  seq                 INTEGER NOT NULL,        -- 3
  source              TEXT NOT NULL DEFAULT 'interview'
                      CHECK (source IN ('interview','stop','review')),
  question_id         TEXT NOT NULL DEFAULT '',
  question            TEXT NOT NULL DEFAULT '',
  cost                TEXT NOT NULL DEFAULT 'cheap' CHECK (cost IN ('cheap','expensive')),
  cost_reason         TEXT NOT NULL DEFAULT '',
  status              TEXT NOT NULL CHECK (status IN ('decided','undecided','recommended')),
  answer              TEXT NOT NULL DEFAULT '',
  option_ids_json     TEXT NOT NULL DEFAULT '[]',
  phase_id            INTEGER,                 -- the phase a stop/review decision belongs to
  created_at          TEXT NOT NULL,
  decided_at          TEXT,
  UNIQUE (workspace_task_id, decision_id),
  UNIQUE (planning_session_id, decision_id)
);
CREATE INDEX IF NOT EXISTS idx_phase_decisions_session ON phase_decisions(planning_session_id, seq);

ALTER TABLE epic_phases ADD COLUMN decision_deps TEXT NOT NULL DEFAULT '[]';
