-- 0003: data fix for an ingest defect (fixed in code alongside this
-- migration) so historical rows heal without a re-ingest: sidechain events
-- ingested before their parent subagent_start existed (live-tail race — a
-- sidechain batch can be flushed and picked up before the main transcript's
-- Agent tool_use line) kept parent_event_id NULL, and the session-detail
-- timeline rendered them as a dangling "unassigned events" bucket.
--
-- Adopt them: the dedup_key prefix before ':' is the sidechain agentId; join
-- it to the agentId recorded in the matching subagent_stop payload of the
-- same session. (Orphans whose stop record never arrived have no join key in
-- the DB and stay unassigned until their transcript is re-ingested.)
-- Must run BEFORE 0004: the async-duration recomputation there derives spans
-- from child events and has to see the adopted rows.
UPDATE events SET parent_event_id = (
    SELECT st.parent_event_id FROM events st
    WHERE st.session_id = events.session_id
      AND st.type = 'subagent_stop'
      AND st.parent_event_id IS NOT NULL
      AND json_extract(st.payload, '$.agentId') =
          substr(events.dedup_key, 1, instr(events.dedup_key, ':') - 1)
)
WHERE parent_event_id IS NULL
  AND turn_id IS NULL
  AND instr(dedup_key, ':') > 1
  AND EXISTS (
    SELECT 1 FROM events st
    WHERE st.session_id = events.session_id
      AND st.type = 'subagent_stop'
      AND st.parent_event_id IS NOT NULL
      AND json_extract(st.payload, '$.agentId') =
          substr(events.dedup_key, 1, instr(events.dedup_key, ':') - 1)
  );
