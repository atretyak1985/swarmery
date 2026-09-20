-- 0073: indexes on the foreign-key CHILD columns that point at events and turns.
--
-- The store opens every connection with PRAGMA foreign_keys=ON (store.go). Under
-- that pragma, deleting a parent row makes SQLite verify that no child row still
-- references it — and when the child column has no index, that verification is
-- a FULL SCAN of the child table, once per deleted parent row.
--
-- Four such columns existed with no index at all:
--
--   events.parent_event_id      → events   (self-reference)
--   events.turn_id              → turns
--   file_changes.event_id       → events
--   permission_requests.event_id → events
--
-- The retention prune (internal/prune) deletes tens of thousands of events and
-- turns per pass. Each deleted event therefore scanned all of events and all of
-- file_changes; each deleted turn scanned all of events. On the first live pass
-- (2026-09-19, ~30k events over a ~200k-row table) that was billions of row
-- visits inside ONE transaction on the store's single connection: the daemon
-- sat at 100 % CPU in `DELETE FROM events` for 11+ hours with every HTTP
-- handler and ingest write queued behind it, and the dashboard was dead. With
-- foreign keys OFF the same statements take well under a second, which is why
-- neither the CLI experiments nor the fixture-sized tests ever showed it.
--
-- With these indexes the per-row check is an index seek and the full pass is
-- about one second on the same store. They also make the ingest-side deletes
-- and the parent/turn lookups the API does cheap for the same reason.
CREATE INDEX IF NOT EXISTS idx_events_parent ON events(parent_event_id);
CREATE INDEX IF NOT EXISTS idx_events_turn   ON events(turn_id);
CREATE INDEX IF NOT EXISTS idx_fc_event      ON file_changes(event_id);
CREATE INDEX IF NOT EXISTS idx_pr_event      ON permission_requests(event_id);
