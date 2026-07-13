-- 0009: process liveness — PID binding and proc_state tracking on sessions.
-- All columns are additive (NULL by default); existing rows unaffected.
ALTER TABLE sessions ADD COLUMN pid             INTEGER;
ALTER TABLE sessions ADD COLUMN pid_source      TEXT;   -- 'hook' | 'scan'
ALTER TABLE sessions ADD COLUMN proc_started_at TEXT;   -- lstart string, PID-reuse guard
ALTER TABLE sessions ADD COLUMN proc_state      TEXT;   -- 'running' | 'orphaned' | 'dead' | 'unknown'
ALTER TABLE sessions ADD COLUMN proc_checked_at TEXT;   -- ISO timestamp of last procwatch check
