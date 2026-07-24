-- task_board: promote the read-only `tasks` table (workspace-intent bridge,
-- GET-only) into a dispatchable board queue. Adds board state, dispatch
-- metadata and verdict columns; the dispatcher (Phase 3) and auto-verification
-- (Phase 6) fill the dispatcher-owned columns. This migration is additive —
-- existing workspace-imported rows land in board_column='triage' (correct: they
-- are intake until a human moves them).
--
-- Board columns (closed set, validated in Go, Fusion builtin:coding semantics):
--   triage | todo | in_progress | in_review | done | archived
-- Priority reuses the existing INTEGER `priority` column (0001_init.sql, default
-- 5); the write boundary normalizes the string tokens urgent|high|normal|low to
-- that integer scale (urgent<high<normal<low), so idx_tasks_queue is untouched.
ALTER TABLE tasks ADD COLUMN board_column TEXT NOT NULL DEFAULT 'triage';
ALTER TABLE tasks ADD COLUMN paused INTEGER NOT NULL DEFAULT 0;
ALTER TABLE tasks ADD COLUMN user_paused INTEGER NOT NULL DEFAULT 0;
ALTER TABLE tasks ADD COLUMN dependencies TEXT NOT NULL DEFAULT '[]'; -- JSON array of task ids
ALTER TABLE tasks ADD COLUMN model TEXT;                              -- optional --model override
ALTER TABLE tasks ADD COLUMN file_scope TEXT NOT NULL DEFAULT '[]';   -- JSON array of path prefixes/globs
ALTER TABLE tasks ADD COLUMN branch TEXT;
ALTER TABLE tasks ADD COLUMN worktree_path TEXT;
ALTER TABLE tasks ADD COLUMN dispatch_error TEXT;
ALTER TABLE tasks ADD COLUMN retry_count INTEGER NOT NULL DEFAULT 0;
ALTER TABLE tasks ADD COLUMN verify_verdict TEXT;                     -- pass | fail | inconclusive
ALTER TABLE tasks ADD COLUMN verify_detail TEXT;
ALTER TABLE tasks ADD COLUMN column_moved_at TEXT;
CREATE INDEX idx_tasks_board ON tasks(project_id, board_column);
