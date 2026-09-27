-- 0088: route_decisions — what the complexity router would pick, and what ran.
--
-- Complexity-routing phase 2. internal/route scores a unit of work from cheap
-- pre-run signals (prompt size, declared scope, risky paths, dependencies, the
-- phase's size forecast, recent failure history) and picks a tier: model,
-- effort and — for board cards — playbook. Every dispatched card and every
-- phase run writes ONE row here, at spawn, so the pick can be compared with
-- what the existing ladders actually chose before the router is ever allowed
-- to change a spawn.
--
-- SHADOW FIRST. In mode 'shadow' (the default) the pick is recorded with
-- applied=0 and the spawn is untouched: used_* and won_rung hold what the
-- EXISTING model/effort/playbook ladders chose, which is what makes a shadow
-- row comparable with an active one later. Mode 'off' writes nothing. The
-- outcome columns stay NULL until a later phase fills them in — NULL means
-- unknown, never "succeeded".
--
--   surface   'dispatch' | 'phaserun'
--   subject   'task:<tasks.id>' | 'phase:<epic_phases.id>' — the <kind>:<id>
--             shape verification_runs.target_key and runcore.SlotKey use
--   won_rung  which rung of the existing MODEL ladder produced used_model:
--             card | playbook | request | doc | route | env | default
--
-- WHY 0088: migrate.go applies unapplied files in FILENAME order, so a new
-- migration takes the next number above the highest existing one (0087,
-- phase_surprise.divergence_cause) — see 0074's note.
--
-- NO FOREIGN KEYS to tasks or epic_phases, for the reasons 0073/0077/0079 give:
-- epic_phases rows are deleted and re-inserted on a plan rescan (a renamed doc
-- is a delete + insert), board cards are deleted by the operator, and the
-- daemon runs with PRAGMA foreign_keys=ON, where an unindexed FK child column
-- wedged the first retention prune for 11.5 hours. A routing decision is a
-- historical fact about a spawn and must outlive the row it was about.

CREATE TABLE IF NOT EXISTS route_decisions (
  id            INTEGER PRIMARY KEY,
  surface       TEXT    NOT NULL,             -- dispatch | phaserun
  subject       TEXT    NOT NULL,             -- 'task:<id>' | 'phase:<id>'
  session_uuid  TEXT    NOT NULL DEFAULT '',  -- first spawned session of the run
  mode          TEXT    NOT NULL,             -- shadow | active
  signals_json  TEXT    NOT NULL,
  score         INTEGER NOT NULL,
  tier          TEXT    NOT NULL,
  pick_model    TEXT    NOT NULL,
  pick_effort   TEXT    NOT NULL,
  pick_playbook TEXT    NOT NULL DEFAULT '',
  reasons_json  TEXT    NOT NULL DEFAULT '[]',
  applied       INTEGER NOT NULL DEFAULT 0,   -- 1 when the pick reached the spawn
  used_model    TEXT    NOT NULL DEFAULT '',  -- what actually ran
  used_effort   TEXT    NOT NULL DEFAULT '',
  used_playbook TEXT    NOT NULL DEFAULT '',
  won_rung      TEXT    NOT NULL DEFAULT '',  -- card | playbook | request | doc | route | env | default
  outcome       TEXT,                         -- filled by phase 3; NULL = unknown
  verify_status TEXT,
  cost_usd      REAL,
  outcome_at    TEXT,
  created_at    TEXT    NOT NULL
);

-- "this card's / this phase's decisions, oldest first" — the outcome back-fill
-- and the per-subject view. id in the index serves the ordering.
CREATE INDEX IF NOT EXISTS idx_route_decisions_subject ON route_decisions(subject, id);
-- time-window reports and any future retention prune.
CREATE INDEX IF NOT EXISTS idx_route_decisions_created ON route_decisions(created_at);
