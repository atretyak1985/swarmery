-- 0127: the cache-cold fact and the cache-cold prediction (internal/cachecold).
--
-- A prompt cache lives for its TTL — 1 h for the cache Claude Code writes on a
-- subscription (migration 0075), 5 min on an API key. A fat session left idle
-- past it pays for its WHOLE context again on the next message: the request
-- reads nothing from cache and re-writes everything. Two records of that:
--
-- turns.cold_restart — the FACT, set by ingest in the tail transaction (and by
-- `swarmery cachecold-backfill` for history): 1 when this main-thread assistant
-- turn followed a pause longer than the TTL, read ~nothing from cache and wrote
-- at least 0.8× the previous turn's footprint. 0 means "not a cold restart" or
-- "never evaluated" (rows older than the backfill window). ADD COLUMN with a
-- constant default rewrites no rows.
--
-- session_cache_cold — the PREDICTION, upserted by the daemon's 60 s tick for a
-- live session whose footprint is at the handoff threshold and whose last turn
-- ended longer ago than the TTL. One row per session: `since` is when the cache
-- expired (last turn end + TTL), `kind` the billing channel the TTL came from
-- (api | subscription), `footprint` the context the next message re-writes.
-- `cleared_at` is set when the session stops qualifying (a new turn arrived, or
-- it ended); a later cold spell overwrites the row and clears it again.
--
-- The partial index serves the per-session coldRestarts count and the per-day
-- report without touching the warm majority of turns.
--
-- Rollback: DROP INDEX IF EXISTS idx_turns_cold_restart;
--           DROP TABLE IF EXISTS session_cache_cold;
--           ALTER TABLE turns DROP COLUMN cold_restart;
ALTER TABLE turns ADD COLUMN cold_restart INTEGER NOT NULL DEFAULT 0;

CREATE INDEX IF NOT EXISTS idx_turns_cold_restart ON turns(session_id) WHERE cold_restart = 1;

CREATE TABLE IF NOT EXISTS session_cache_cold (
  session_uuid TEXT PRIMARY KEY,
  since        TEXT NOT NULL,
  kind         TEXT NOT NULL CHECK (kind IN ('api', 'subscription')),
  footprint    INTEGER NOT NULL,
  cleared_at   TEXT
);
