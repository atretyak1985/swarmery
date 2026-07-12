-- 0004: data fix for an ingest defect (fixed in code alongside this
-- migration) so historical rows heal without a re-ingest: background
-- (run_in_background) Agent calls get an immediate "async_launched"
-- tool_result, which closed subagent_start/stop with the launch roundtrip
-- (~0.1s) as duration_ms and status='error', although the sidechain ran for
-- minutes. Recompute the duration as subagent_start.ts → latest stored child
-- event ts (best available approximation; a re-ingest refines it to the
-- sidechain's true last record) and clear the bogus error status.

-- Async subagent_stop rows.
UPDATE events SET
    duration_ms = COALESCE((
        SELECT CAST(ROUND((julianday(MAX(c.ts)) - julianday(
                   (SELECT p.ts FROM events p WHERE p.id = events.parent_event_id)
               )) * 86400000) AS INTEGER)
        FROM events c WHERE c.parent_event_id = events.parent_event_id
    ), duration_ms),
    status = 'ok'
WHERE type = 'subagent_stop'
  AND json_extract(payload, '$.status') = 'async_launched';

-- Mirror onto the paired subagent_start rows.
UPDATE events SET
    duration_ms = COALESCE((
        SELECT st.duration_ms FROM events st
        WHERE st.parent_event_id = events.id AND st.type = 'subagent_stop'
          AND json_extract(st.payload, '$.status') = 'async_launched'
    ), duration_ms),
    status = 'ok'
WHERE type = 'subagent_start'
  AND EXISTS (
    SELECT 1 FROM events st
    WHERE st.parent_event_id = events.id AND st.type = 'subagent_stop'
      AND json_extract(st.payload, '$.status') = 'async_launched'
  );
