-- 0008: System section Stage 1 (phase 4) — hooks & commands registries, plus
-- origin/plugin_name provenance columns on agents/skills. Additive only:
-- nothing created by 0001–0007 is rewritten.
--
-- Field semantics follow docs/system-config-format.md (step-01 discovery).
-- Deviations vs the original plan sketch, driven by step-01 evidence:
--   * hooks.timeout (INTEGER, seconds) and hooks.status_message (TEXT) added —
--     real settings entries carry optional `timeout` (10, 130 observed) and
--     `statusMessage` (user-tier gitnexus hooks), see step-01 §3.1.
--   * matcher stays TEXT — it is always a string when present (never an
--     object); optional (the swarmery Stop group has none) → NULL.
--   * no `async` column — `async` appears only in plugin hooks/hooks.json,
--     which Stage 1 does not scan (settings files only; plugin-shipped hooks
--     are deferred to Stage 1.5, step-01 §5.4).
--   * settings.json and settings.local.json are scanned as SEPARATE
--     source_file rows; *.bak files are ignored by the scanner (step-01 §3.4).

-- ============ Hooks (settings.json / settings.local.json entries) ============

-- No origin column by design: Stage 1 scans settings files only. No version
-- history either: seq is the array index at scan time, and a rescan performs
-- delete-and-insert per source_file — UNIQUE(source_file, event, seq) is the
-- stable row key, not a durable identity across reorders.
CREATE TABLE hooks (
    id             INTEGER PRIMARY KEY,
    scope          TEXT NOT NULL,              -- global | project
    project_id     INTEGER REFERENCES projects(id),  -- NULL for global
    event          TEXT NOT NULL,              -- PreToolUse | Stop | ...; unknown
                   -- event names are data, not errors (forward-compat, §3.5)
    matcher        TEXT,                       -- NULL when absent in JSON
    command        TEXT NOT NULL,
    timeout        INTEGER,                    -- seconds; NULL when absent
    status_message TEXT,                       -- JSON `statusMessage`; NULL when absent
    source_file    TEXT NOT NULL,              -- full path of settings(.local).json
    seq            INTEGER NOT NULL,           -- position in the event's array
    enabled        INTEGER NOT NULL DEFAULT 1,
    managed        TEXT,                       -- 'swarmery' for installer-owned
                   -- entries (the "swarmery hook" command marker, hookcfg.go),
                   -- else NULL; column is cheap now, enforced in Stage 2
    content_hash   TEXT NOT NULL,
    UNIQUE(source_file, event, seq)
);
CREATE INDEX idx_hooks_project ON hooks(project_id);

-- ============ Commands (commands/*.md) ============

-- Command name = file stem (no `name:` frontmatter key exists, step-01 §4).
-- Plugin-owned commands are stored under the composite "plugin:name" so they
-- coexist with a same-named project-local command under
-- UNIQUE(name, scope, project_id) — confirmed naming rule (step-01 §7).
CREATE TABLE commands (
    id           INTEGER PRIMARY KEY,
    name         TEXT NOT NULL,
    scope        TEXT NOT NULL,               -- global | project
    project_id   INTEGER REFERENCES projects(id),  -- NULL for global
    file_path    TEXT NOT NULL,
    description  TEXT,
    origin       TEXT NOT NULL DEFAULT 'local',    -- local | plugin
    plugin_name  TEXT,                        -- NULL unless origin = 'plugin'
    content_hash TEXT NOT NULL,
    deleted      INTEGER NOT NULL DEFAULT 0,
    UNIQUE(name, scope, project_id)
);
CREATE INDEX idx_commands_project ON commands(project_id);

-- ============ Provenance columns on existing registries ============

-- Same local|plugin split for agents/skills; plugin items are stored under
-- "plugin:name", so UNIQUE(name, scope, project_id) stays untouched. Existing
-- rows are all local scans → backfill via the 'local' default.
ALTER TABLE agents ADD COLUMN origin TEXT NOT NULL DEFAULT 'local';
ALTER TABLE agents ADD COLUMN plugin_name TEXT;
ALTER TABLE skills ADD COLUMN origin TEXT NOT NULL DEFAULT 'local';
ALTER TABLE skills ADD COLUMN plugin_name TEXT;
