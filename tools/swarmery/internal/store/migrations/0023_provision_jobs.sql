-- provision_jobs: one row per "enable pack → provision" attempt. Durable so a
-- daemon restart can heal in-flight rows to 'failed' (see provision.HealStale).
-- Phase 2 wires HealStale() at daemon startup; this migration only creates the table.
CREATE TABLE provision_jobs (
  id          INTEGER PRIMARY KEY,
  project_id  INTEGER NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
  pack        TEXT NOT NULL,
  status      TEXT NOT NULL DEFAULT 'pending'
              CHECK (status IN ('pending','installing','generating','installed','done','skipped','failed')),
  last_line   TEXT,
  error       TEXT,
  started_at  TEXT NOT NULL,
  finished_at TEXT
);
CREATE INDEX idx_provision_jobs_project ON provision_jobs(project_id);
-- Single-flight guard: at most one in-flight job per (project, pack). Closes the
-- TOCTOU gap in Enqueue's SELECT-then-INSERT so two concurrent enables can't both
-- pass the code-level check — the second INSERT hits this constraint immediately
-- (same pattern as 0022_agent_proposals_one_open.sql).
CREATE UNIQUE INDEX idx_provision_jobs_inflight
  ON provision_jobs(project_id, pack)
  WHERE status IN ('pending','installing','generating');
