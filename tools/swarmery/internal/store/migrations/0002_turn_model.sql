-- 0002: per-turn model (contract change accepted at step 10, see
-- web/CONTRACT-REQUESTS.md). The API message model lives per message, not per
-- session; persisting it on the turn makes `swarmery recost` exact when a
-- session switches models mid-file. Additive only — NULL for user turns and
-- for rows ingested before this migration (recost falls back to sessions.model).
ALTER TABLE turns ADD COLUMN model TEXT;
