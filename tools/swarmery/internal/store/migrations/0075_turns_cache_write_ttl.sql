-- Cache writes split by TTL, plus the speed mode of the turn.
--
-- usage.cache_creation_input_tokens is a FLAT total that hides two different
-- SKUs: a 5-minute-TTL write bills at 1.25x input, a 1-hour-TTL write at 2x.
-- Claude Code writes 1h cache (docs/jsonl-format.md §6 shows a real turn with
-- ephemeral_1h_input_tokens=6935, ephemeral_5m_input_tokens=0), so every 1h
-- write was billed at the 5m rate — 37.5% low on Opus 5.5. The split was never
-- stored, so `swarmery recost` could not repair history from the DB alone; it
-- lands here so it can.
--
-- usage.speed ("standard" / "fast") was likewise parsed by nothing. Fast is a
-- separate SKU at 2x standard, and the "<model>-fast" pricing rows only ever
-- matched ids that literally contained "-fast" — which transcript ids do not —
-- so fast turns billed at standard rates.
--
-- The legacy tokens_cache_write column is deliberately KEPT and still written:
-- it is the flat total, read by context-size queries across advisor, handoff,
-- api and economics, and it is what prices rows ingested before this migration.
-- NULL in the new columns means "no split known" and prices exactly as before
-- (whole total at the 5m rate) — no historical cost moves without a re-ingest.
ALTER TABLE turns ADD COLUMN cache_write_5m_tokens INTEGER;
ALTER TABLE turns ADD COLUMN cache_write_1h_tokens INTEGER;
ALTER TABLE turns ADD COLUMN speed TEXT;
