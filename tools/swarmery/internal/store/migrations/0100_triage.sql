-- inbox triage agent (phase 2): recorded, single-flight background triage runs
-- and the per-part verdicts they produce. Every write a run makes passes the Go
-- policy check in internal/triage first; these tables are the audit trail and
-- the undo source.
CREATE TABLE triage_runs (
  id INTEGER PRIMARY KEY,
  trigger TEXT NOT NULL CHECK (trigger IN ('operator','schedule')),
  scope_project_id INTEGER,                      -- NULL = whole fleet
  kinds TEXT NOT NULL DEFAULT '[]',
  status TEXT NOT NULL DEFAULT 'running' CHECK (status IN ('running','ok','failed')),
  total INTEGER NOT NULL DEFAULT 0, done INTEGER NOT NULL DEFAULT 0,
  applied INTEGER NOT NULL DEFAULT 0, suggested INTEGER NOT NULL DEFAULT 0,
  skipped INTEGER NOT NULL DEFAULT 0, failed INTEGER NOT NULL DEFAULT 0, rejected INTEGER NOT NULL DEFAULT 0,
  cost_usd REAL NOT NULL DEFAULT 0, session_uuids TEXT NOT NULL DEFAULT '[]',
  error TEXT NOT NULL DEFAULT '', started_at TEXT NOT NULL, finished_at TEXT
);
CREATE TABLE triage_verdicts (
  id INTEGER PRIMARY KEY,
  run_id INTEGER NOT NULL REFERENCES triage_runs(id),
  kind TEXT NOT NULL, class TEXT NOT NULL DEFAULT '',
  ref TEXT NOT NULL, item_key TEXT NOT NULL DEFAULT '', title TEXT NOT NULL DEFAULT '',
  value TEXT NOT NULL, reason TEXT NOT NULL DEFAULT '',
  payload TEXT NOT NULL DEFAULT '{}', prior TEXT NOT NULL DEFAULT '{}',
  state TEXT NOT NULL CHECK (state IN
    ('applied','suggested','sample','accepted','audited','stale','undoing','undone','rejected','failed','skipped')),
  created_at TEXT NOT NULL, decided_at TEXT,
  project_id INTEGER                             -- the item's project; NULL = none
);
CREATE INDEX idx_triage_verdicts_ref ON triage_verdicts(kind, ref, state);
CREATE INDEX idx_triage_verdicts_run ON triage_verdicts(run_id);
CREATE INDEX idx_triage_verdicts_state ON triage_verdicts(state, created_at);
CREATE INDEX idx_triage_verdicts_project ON triage_verdicts(project_id, state);
