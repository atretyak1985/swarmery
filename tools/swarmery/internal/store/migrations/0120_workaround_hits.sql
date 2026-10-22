-- 0120: where an agent said it changed course (verify-not-trust phase 5, D23).
--
-- internal/workaround scans the assistant prose of every session tied to a
-- phase — a daemon phase run (epic_phases.run_session_uuid) or a session
-- task_sessions links to a plan — for guards.json's workaroundWords and keeps
-- one row per (turn, word, sentence). The phase card lists them as "places
-- where the agent changed the plan" and the reviewer reads them first.
--
-- phase_id is NULL when the session is linked but the chain is not
-- attributable to one phase (an interactive orchestrator's own prose).
-- agent_name is the subagent that said it (NULL = the session's main chain).
--
-- At most 50 rows per session carry their text. Beyond that a row keeps only
-- its key (sentence = '', contexts = ''), so the count stays exact and a
-- replayed transcript still dedupes, but a runaway session cannot fill the
-- database with prose. sentence_hash is the dedup key for both kinds.
--
-- The two link indexes make the per-batch "is this session phase-linked?"
-- lookup in the ingest tail an index probe instead of a table scan.
--
-- Rollback: DROP TABLE IF EXISTS workaround_hits;
--           DROP INDEX IF EXISTS idx_epic_phases_run_session;
--           DROP INDEX IF EXISTS idx_task_sessions_session;
CREATE TABLE IF NOT EXISTS workaround_hits (
  id             INTEGER PRIMARY KEY AUTOINCREMENT,
  session_uuid   TEXT    NOT NULL,
  turn_id        INTEGER NOT NULL,
  phase_id       INTEGER,
  agent_name     TEXT,
  word           TEXT    NOT NULL,
  sentence       TEXT    NOT NULL DEFAULT '',
  sentence_hash  TEXT    NOT NULL,
  context_before TEXT    NOT NULL DEFAULT '',
  context_after  TEXT    NOT NULL DEFAULT '',
  created_at     TEXT    NOT NULL,
  UNIQUE (turn_id, word, sentence_hash)
);
CREATE INDEX IF NOT EXISTS idx_workaround_hits_phase   ON workaround_hits(phase_id);
CREATE INDEX IF NOT EXISTS idx_workaround_hits_session ON workaround_hits(session_uuid);

CREATE INDEX IF NOT EXISTS idx_epic_phases_run_session
  ON epic_phases(run_session_uuid) WHERE run_session_uuid IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_task_sessions_session ON task_sessions(session_id);
