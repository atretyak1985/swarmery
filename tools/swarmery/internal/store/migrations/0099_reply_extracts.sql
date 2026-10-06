-- 0099: reply_extracts — the cache of Haiku-extracted reply cards for
-- sessions waiting on a plain-text reply (needs-you queue, phase 4).
--
-- One row per (session, assistant turn): the turn whose prose carried the
-- question is the cache key, so a second awaiting_reply transition on the same
-- turn never calls the model again, and a new assistant turn gets a fresh row.
-- status 'error' rows are kept on purpose: they count toward the daily cap and
-- stop the same turn from being retried.
--
-- turn_id carries NO foreign key to turns: the retention prune deletes turns,
-- and a cache row outliving its turn is harmless (nothing joins it any more).
-- session_id cascades with its session; the UNIQUE index below leads with
-- session_id, so the FK check on a session delete is an index seek, never the
-- full-scan shape that wedged the daemon before (see 0073).
CREATE TABLE IF NOT EXISTS reply_extracts (
  id           INTEGER PRIMARY KEY,
  session_id   INTEGER NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
  turn_id      INTEGER NOT NULL,
  status       TEXT NOT NULL CHECK (status IN ('ok', 'error')),
  question     TEXT,
  options_json TEXT,
  recommended  TEXT,
  model        TEXT NOT NULL,
  error        TEXT,
  created_at   TEXT NOT NULL,
  UNIQUE (session_id, turn_id)
);

-- The daily cap counts rows created since UTC midnight.
CREATE INDEX IF NOT EXISTS idx_reply_extracts_created ON reply_extracts(created_at);
