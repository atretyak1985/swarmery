-- dispatch_state (fusion phase 3 — dispatcher): durable pause flags for the
-- task dispatcher. One row per pause scope: the singleton 'global' scope parks
-- ALL admissions; a 'project:<id>' scope parks only that project's tasks. Rows
-- are created lazily by the pause endpoint (upsert) — absence means "not
-- paused". Kept in the DB (not just env) so a pause survives a daemon restart,
-- mirroring the durable-job discipline of provision_jobs (0023).
--
-- This is the ONLY table the dispatcher owns; the dispatcher-owned COLUMNS
-- (branch, worktree_path, dispatch_error, retry_count, verify_*) already exist
-- on tasks from 0024_task_board and are written via inline SQL in the api
-- package (single-writer discipline), never here.
CREATE TABLE dispatch_pause (
  scope      TEXT PRIMARY KEY,   -- 'global' | 'project:<project_id>'
  paused     INTEGER NOT NULL DEFAULT 0,
  updated_at TEXT NOT NULL
);

-- The explicit task↔session link. The phase-3 spike confirmed `claude -p
-- --session-id <uuid>` is honored on this platform, so the dispatcher
-- pre-generates the UUID and passes it to the headless run. tasks.session_id is
-- an INTEGER FK that can only be set AFTER the sessions row is ingested, so the
-- pre-generated UUID is parked here at spawn time; the dispatcher reconciles it
-- into tasks.session_id + task_sessions(link_source='explicit') as soon as the
-- transcript is ingested. Also lets a restart/verification re-link a run.
ALTER TABLE tasks ADD COLUMN dispatch_session_uuid TEXT;
