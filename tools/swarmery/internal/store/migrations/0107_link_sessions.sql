-- 0107: link_sessions — the swarmery link mod's live view of a session.
--
-- mods-swarmery-link phase 1. A Claude Code mod POSTs session events to
-- /api/link/events (internal/link). They are keyed by the Claude session id,
-- which IS sessions.session_uuid (docs/mods-format.md §M1), so no new identity
-- column is needed. sessions.source gains the value 'mod' (a row the link
-- minted before any transcript was tailed); the JSONL ingest promotes it to
-- 'both' exactly as it already does for 'hook'. sessions.source has no CHECK
-- constraint (0001), so the new value needs no table rebuild.
--
-- The five columns below are the latest state the mod reported. All nullable:
-- NULL means "no mod is attached to this session", which is every session
-- before this feature and every session run without the mod.
--   link_status       working | waiting | done (turn.start / turn.complete / session.end)
--   link_seen_at      at of the newest link event (fixed-width UTC, ms)
--   link_context_pct  session.measure context.percent (0–100)
--   link_rate_limits  session.measure rateLimits[], JSON verbatim
--   link_cost_usd     session.measure cost.usd — PER PROCESS, not lifetime (§M2)
ALTER TABLE sessions ADD COLUMN link_status TEXT;
ALTER TABLE sessions ADD COLUMN link_seen_at TEXT;
ALTER TABLE sessions ADD COLUMN link_context_pct REAL;
ALTER TABLE sessions ADD COLUMN link_rate_limits TEXT;
ALTER TABLE sessions ADD COLUMN link_cost_usd REAL;

-- link_events: every accepted mod event, one row each. The UNIQUE key is the
-- dedup contract: a mod that resends a batch (retry after a timeout) gets the
-- duplicates counted, not stored twice.
--
-- NO FOREIGN KEY on session_id, for the reason 0077/0079 spell out: the daemon
-- runs with PRAGMA foreign_keys=ON, sessions rows are re-pointed and pruned by
-- other writers, and an FK child column is what wedged the first retention
-- prune for 11.5 hours (0073). Retention is the prune pass's job instead.
CREATE TABLE IF NOT EXISTS link_events (
  id         INTEGER PRIMARY KEY,
  session_id INTEGER NOT NULL,   -- sessions.id (no FK, see above)
  kind       TEXT    NOT NULL,   -- turn.start | turn.complete | tool.call | session.measure | session.start | session.end
  at         TEXT    NOT NULL,   -- fixed-width UTC, ms
  payload    TEXT,               -- the event's payload object, JSON verbatim
  UNIQUE(session_id, kind, at)
);

CREATE INDEX IF NOT EXISTS idx_link_events_session_at
  ON link_events(session_id, at DESC);
