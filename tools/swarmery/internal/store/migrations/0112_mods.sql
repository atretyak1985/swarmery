-- 0112: mods — Claude Code plugins whose hooks/hooks.json lists `modules`
-- (mods-swarmery-link phase 9, System → Mods audit).
--
-- internal/sysscan discovers them (internal/modscan.Discover: the plugin cache
-- under <claudeDir>/plugins/cache, plus CLAUDE_CODE_PLUGIN_DIRS from the
-- settings `env` block), runs `claude plugin validate --strict --json` only when
-- module_sha changed (or on a forced rescan), and caches the parsed result here.
-- One row per plugin folder: UNIQUE(dir).
--
-- The table is a CACHE of the last validate, not history: a mod that vanishes
-- from disk (a cache version bump moves its dir) has its row deleted by the
-- next scan. events/uses/env/state/risk/errors/report are JSON text written by
-- the scanner; nothing else writes them.
--
-- WHY 0112 and not 0107: another branch of the same plan reserves 0107 (the
-- link table). migrate.go applies files in sorted filename order and checks
-- version→name, so a gap is harmless and a duplicate number is refused at start.
--
-- No foreign key: a mod row stands on its own (pattern: 0079's note) — the
-- config_lint_findings parse_error row for a mod is keyed by target 'mod:<id>'.
CREATE TABLE IF NOT EXISTS mods (
  id           INTEGER PRIMARY KEY,
  name         TEXT NOT NULL,          -- plugin.json name, else the folder name
  plugin_id    TEXT,                   -- '<name>@<marketplace>' for cache mods; NULL for plugin-dir
  dir          TEXT NOT NULL,          -- absolute plugin root (shown, never its module contents)
  source       TEXT NOT NULL,          -- cache | plugin-dir
  version      TEXT,                   -- plugin.json version, else the cache version dir
  module_sha   TEXT,                   -- modscan.Hash: sha256 over hooks.json + listed modules; NULL = retry next pass
  events       TEXT,                   -- JSON []string: hooked events, verbatim incl. {filters}
  uses         TEXT,                   -- JSON []string: $.* calls without the `$.` prefix
  env          TEXT,                   -- JSON {reads: [], writes: []}
  state        TEXT,                   -- JSON []string
  risk         TEXT,                   -- JSON []Badge (modscan.Risk)
  validated_at TEXT,                   -- RFC3339 of the last validate attempt
  errors       TEXT,                   -- JSON []string (validate errors, or the scanner's own failure)
  report       TEXT,                   -- JSON modscan.Report incl. the raw validate output (detail view)
  UNIQUE(dir)
);
