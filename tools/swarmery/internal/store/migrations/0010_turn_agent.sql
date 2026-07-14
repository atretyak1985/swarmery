-- 0010_turn_agent: per-turn agent attribution (analytics phase 2).
--
-- Phase 1 could not attribute $/tokens to agents because the ingester records
-- NO turns for subagents (sidechain mode). This adds an agent_name column so
-- subagent turns can be tagged with the agent that produced them (the parent
-- subagent_start's subagent_type, folded to the same grain analytics uses for
-- run counts). NULL = the orchestrator ("main") session turns.
ALTER TABLE turns ADD COLUMN agent_name TEXT;

-- Backfill: existing sessions were ingested before subagent turns were
-- recorded, so their sidechain turns are missing entirely. Clearing all file
-- offsets makes the next `serve` backfill re-read every transcript and insert
-- the missing turns. This is safe/idempotent: existing turns dedup by
-- (session_id, message_id) and events by dedup_key (ON CONFLICT DO NOTHING),
-- so only the previously-skipped sidechain turns are added. Best effort —
-- sessions whose sidechain .jsonl files have since been deleted stay
-- counts-only for agents.
DELETE FROM file_offsets;
