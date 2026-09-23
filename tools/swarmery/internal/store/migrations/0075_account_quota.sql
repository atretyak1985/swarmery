-- 0075: the per-account QUOTA signal and a durable record of every usage-limit
-- hit — the trigger for every account switch, which until now the system did
-- not store anywhere (account_runnable, 0054, answers only "can the CLI log in").
--
-- account_quota holds the latest headroom reading per (account, window), written
-- by the daemon's quota poller (internal/quota) from the same usage endpoint the
-- dashboard reads. LATEST READING WINS: a poll replaces that account's whole
-- window set. ABSENCE OF A ROW MEANS UNKNOWN — never fetched, or the last fetch
-- failed — and never "empty" or "no headroom". That is 0054's rule verbatim, and
-- it is why a failed fetch writes nothing instead of zeros: `swarmery account
-- switch` refuses an account whose headroom it cannot vouch for, and a zero row
-- would make "no answer" indistinguishable from "a bad answer".
--
-- account_limit_hits is APPEND-ONLY history: one row per observed limit hit,
-- from a transcript record flagged isApiErrorMessage (source 'transcript',
-- record_uuid set — the partial UNIQUE index makes a re-tail insert nothing) or
-- from a run's classified exit (source 'run', record_uuid ''). "How many days
-- did we hit a limit" is a history question, so no row is ever replaced.
--
-- NO ROW HERE EVER CARRIES MESSAGE TEXT OR CREDENTIAL MATERIAL. scope is a fixed
-- vocabulary ('session' | 'weekly' | 'model' | ''), label is the usage
-- endpoint's own window label, and source/engine are fixed tags.
--
-- WHY 0075. The number was reserved for this change up front: migrate.go applies
-- unapplied files in FILENAME order, so a slot two changes both take yields two
-- different schemas with no error (see 0074's header). Tests match this file by
-- NAME (%_account_quota.sql), never by the number.
--
-- Purely additive: two CREATE TABLEs and two CREATE INDEXes. No ALTER, no
-- rebuild, no foreign key, no backfill — no existing row is touched.
CREATE TABLE account_quota (
  account      TEXT NOT NULL,
  window_key   TEXT NOT NULL,              -- usage.Window.Key: 'five_hour', 'seven_day', …
  label        TEXT NOT NULL DEFAULT '',
  percent_used REAL NOT NULL,
  percent_left REAL NOT NULL,
  resets_at    TEXT NOT NULL DEFAULT '',   -- RFC 3339, '' when the window reports none
  window_ms    INTEGER NOT NULL DEFAULT 0,
  source       TEXT NOT NULL,              -- 'poller'
  fetched_at   INTEGER NOT NULL,           -- unix seconds
  PRIMARY KEY (account, window_key)
);

CREATE TABLE account_limit_hits (
  id           INTEGER PRIMARY KEY,
  account      TEXT NOT NULL,
  observed_at  TEXT NOT NULL,              -- RFC 3339 (the transcript record's timestamp)
  scope        TEXT NOT NULL DEFAULT '',   -- 'session' | 'weekly' | 'model' | ''
  source       TEXT NOT NULL,              -- 'transcript' | 'run'
  engine       TEXT NOT NULL DEFAULT '',
  session_uuid TEXT NOT NULL DEFAULT '',
  record_uuid  TEXT NOT NULL DEFAULT ''
);

CREATE UNIQUE INDEX idx_account_limit_hits_record
  ON account_limit_hits(record_uuid) WHERE record_uuid != '';

CREATE INDEX idx_account_limit_hits_account_time
  ON account_limit_hits(account, observed_at DESC);
