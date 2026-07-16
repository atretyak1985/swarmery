-- 0012_fts: global full-text search over turns.text (external-content FTS5).
--
-- Driver note: modernc.org/sqlite (v1.53.0) ships FTS5, snippet() and bm25()
-- compiled in — verified before this migration was written.
--
-- turns_fts shadows turns(text) with content='turns' / content_rowid='id':
-- the index stores tokens only, snippet() reads the prose from turns at query
-- time, and rowid == turns.id. Triggers mirror the ingester's exact write
-- paths (internal/ingest/ingest.go):
--   INSERT INTO turns …                    upsertTurn — text is NULL here
--   UPDATE turns SET text = ? WHERE id = ? flushTurnTexts — the ONLY text writer
--   DELETE FROM turns                      no current caller; future-proofing
-- The UPDATE trigger is scoped `OF text` so the frequent ended_at / cost_usd
-- updates on the same rows never churn the index.
--
-- Session titles / file paths / project names are deliberately NOT
-- FTS-indexed: tiny cardinality, matched with escaped LIKE in
-- internal/api/search.go (substring semantics are wanted for paths).

CREATE VIRTUAL TABLE turns_fts USING fts5(
    text,
    content='turns',
    content_rowid='id',
    tokenize='unicode61'
);

CREATE TRIGGER turns_fts_ai AFTER INSERT ON turns BEGIN
    INSERT INTO turns_fts(rowid, text) VALUES (new.id, new.text);
END;

CREATE TRIGGER turns_fts_ad AFTER DELETE ON turns BEGIN
    INSERT INTO turns_fts(turns_fts, rowid, text) VALUES ('delete', old.id, old.text);
END;

CREATE TRIGGER turns_fts_au AFTER UPDATE OF text ON turns BEGIN
    INSERT INTO turns_fts(turns_fts, rowid, text) VALUES ('delete', old.id, old.text);
    INSERT INTO turns_fts(rowid, text) VALUES (new.id, new.text);
END;

-- One-time backfill of every existing row. NULL text indexes as zero tokens
-- but MUST still be inserted so later 'delete' commands (UPDATE/DELETE
-- triggers) stay consistent with what was indexed. Runs inside the migration
-- runner's transaction — atomic with the table/trigger creation.
INSERT INTO turns_fts(rowid, text) SELECT id, text FROM turns;
