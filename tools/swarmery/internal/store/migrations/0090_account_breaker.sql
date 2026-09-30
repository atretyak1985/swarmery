-- 0090: the per-account CIRCUIT BREAKER — the daemon's memory that an account
-- cannot run right now, so it stops spending runs on it.
--
-- account_runnable (0054) answers "can the CLI log in" and account_quota (0089)
-- answers "how much headroom is left"; neither one STOPS anything. A run that
-- dies because the login expired, the organisation switched subscription access
-- off, or the account hit its usage limit used to be followed by the next run
-- on the same account, and the next. One row here per account is what every
-- engine's admission reads (internal/runcore CheckAccount) before it takes a
-- slot or a worktree.
--
-- One row per account, the LATEST state wins; a closed row is kept as the
-- record of the last opening and how it ended. ABSENCE OF A ROW MEANS THE
-- BREAKER NEVER OPENED — the same "absence is not a verdict" rule as 0054/0089.
--
--   state      'open' | 'closed'
--   kind       'auth' | 'quota'   — the only two failures that trip it; an
--              API error (overloaded, connection reset) never does
--   reason     a FIXED phrase from internal/claudeprobe (Reason* constants),
--              never CLI output — NO ROW HERE EVER CARRIES MESSAGE TEXT OR
--              CREDENTIAL MATERIAL
--   opened_at  RFC 3339
--   resets_at  RFC 3339, NULL when nothing says when (every auth opening). A
--              quota opening past its resets_at closes itself on the next
--              admission; an auth opening closes only on a ready probe
--   source     'run' (a run's classified exit) | 'probe' (the pre-flight or an
--              operator probe) | 'transcript' (a fresh API-error record)
--   closed_at  RFC 3339, NULL while open
--   closed_by  'reset' | 'probe' | 'login' | 'operator', NULL while open
--
-- WHY 0090: migrate.go applies unapplied files in FILENAME order and 0089
-- (account_quota) is the highest existing one. Tests match this file by NAME
-- (%_account_breaker.sql), never by the number, so a merge-time renumber costs
-- nothing (see 0089's header for why that happens).
--
-- NO FOREIGN KEYS, for the reasons 0083 gives: the daemon runs with PRAGMA
-- foreign_keys=ON and an unindexed FK child column wedged a retention prune.
-- Purely additive: one CREATE TABLE. No ALTER, no rebuild, no backfill.
CREATE TABLE account_breaker (
  account   TEXT PRIMARY KEY,
  state     TEXT NOT NULL CHECK (state IN ('open','closed')),
  kind      TEXT NOT NULL DEFAULT '',
  reason    TEXT NOT NULL DEFAULT '',
  opened_at TEXT NOT NULL DEFAULT '',
  resets_at TEXT,
  source    TEXT NOT NULL DEFAULT '',
  closed_at TEXT,
  closed_by TEXT
);
