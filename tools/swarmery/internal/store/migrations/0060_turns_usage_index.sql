-- Why the session list and the project overview were reading the whole `turns`
-- table off disk.
--
-- `turns` carries the fat `text` column inline, so at ~107k rows it is by far
-- the widest table a hot query touches: every row visited costs a page read of
-- the prose, even when the query only wants tokens/cost. The two hottest
-- aggregate shapes both visit many rows:
--
--   sessionSelect  — per-session token/cost totals and the newest assistant
--                    turn's context footprint (handlers.go);
--   projectOverview — SUM(cost_usd) over a rolling 7-day window, joined from
--                    sessions by project (project_overview.go).
--
-- Only sqlite_autoindex_turns_1 (session_id, seq) existed, which locates the
-- rows but cannot supply their columns — so both shapes fell through to the
-- table. Cold, that is a ~66MB read; GET /api/projects/{id}/overview was
-- measured at 30s on a cold page cache.
--
-- This index is COVERING for both: session_id leads (matching the join key and
-- the existing lookup order), seq keeps the "newest/first turn" ORDER BY
-- index-resolvable, and every column those aggregates read follows. `text` is
-- deliberately excluded — including it would just duplicate the table.
CREATE INDEX IF NOT EXISTS idx_turns_session_usage
    ON turns(session_id, seq, role, started_at,
             cost_usd, tokens_in, tokens_out,
             tokens_cache_read, tokens_cache_write);
