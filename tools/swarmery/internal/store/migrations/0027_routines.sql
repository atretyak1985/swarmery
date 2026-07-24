-- routines: cron/webhook/manual-triggered automation with typed steps
-- (command | ai-prompt | create-task), catch-up policy, and pruned run history
-- (fusion phase 7). The scheduler (internal/routines) ticks every 60s, runs due
-- routines, and recomputes next_run_at; a daemon restart heals in-flight runs to
-- 'failed' (routines.HealStale, provision-heal idiom).
CREATE TABLE routines (
  id            TEXT PRIMARY KEY,            -- "R-" + 6-char base36
  project_id    INTEGER REFERENCES projects(id) ON DELETE CASCADE, -- NULL = global
  name          TEXT NOT NULL,
  cron_expr     TEXT,                        -- NULL/'' = manual/webhook-only
  enabled       INTEGER NOT NULL DEFAULT 1,
  catch_up      TEXT NOT NULL DEFAULT 'skip' -- run_one|skip
                CHECK (catch_up IN ('run_one','skip')),
  steps         TEXT NOT NULL DEFAULT '[]',  -- JSON array of typed steps
  webhook_token TEXT,                        -- non-null/non-empty enables POST trigger
  timeout_sec   INTEGER NOT NULL DEFAULT 900,
  created_at    TEXT NOT NULL,
  updated_at    TEXT NOT NULL,
  last_run_at   TEXT,
  next_run_at   TEXT
);
CREATE INDEX idx_routines_due ON routines(enabled, next_run_at);

-- routine_runs: one row per trigger. detail holds the per-step results JSON
-- (truncated to 8KB by the executor). Pruned to the newest 50 per routine after
-- every insert.
CREATE TABLE routine_runs (
  id          INTEGER PRIMARY KEY AUTOINCREMENT,
  routine_id  TEXT NOT NULL REFERENCES routines(id) ON DELETE CASCADE,
  trigger     TEXT NOT NULL                 -- cron|manual|webhook
              CHECK (trigger IN ('cron','manual','webhook')),
  status      TEXT NOT NULL                 -- running|ok|failed|timeout
              CHECK (status IN ('running','ok','failed','timeout')),
  detail      TEXT,                         -- per-step results JSON (<=8KB)
  started_at  TEXT NOT NULL,
  finished_at TEXT
);
CREATE INDEX idx_routine_runs ON routine_runs(routine_id, started_at DESC);
