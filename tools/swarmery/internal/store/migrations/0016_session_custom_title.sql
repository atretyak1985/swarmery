-- 0016: user-set session title override.
--
-- The ingested `title` is authoritative from the transcript (ai-title
-- checkpoints overwrite it on every tail — see ingest.go), so a manual rename
-- written into `title` would be clobbered on the next batch. `custom_title`
-- is a separate, ingest-untouched override: readers serve
-- COALESCE(custom_title, title), and clearing it reverts to the ingested name.
ALTER TABLE sessions ADD COLUMN custom_title TEXT;
