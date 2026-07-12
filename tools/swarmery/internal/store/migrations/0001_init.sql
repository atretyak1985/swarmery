-- 0001_init: full schema from swarmery-design.md §2 + file_offsets service table.
-- Order matters: agents and skills are created BEFORE events (events has FKs to them).

-- ============ Project & session registry ============

CREATE TABLE projects (
    id            INTEGER PRIMARY KEY,
    path          TEXT NOT NULL UNIQUE,
    slug          TEXT NOT NULL,
    name          TEXT,
    first_seen    TEXT NOT NULL,
    last_activity TEXT,
    archived      INTEGER NOT NULL DEFAULT 0
);

CREATE TABLE sessions (
    id           INTEGER PRIMARY KEY,
    project_id   INTEGER NOT NULL REFERENCES projects(id),
    session_uuid TEXT NOT NULL UNIQUE,
    parent_uuid  TEXT,               -- no observed JSONL source (C4) — NULL in MVP
    model        TEXT,
    git_branch   TEXT,
    cwd          TEXT,
    status       TEXT NOT NULL DEFAULT 'active',
                 -- active | waiting_approval | idle | completed | killed
                 -- MVP emits only active|idle|completed (C5);
                 -- waiting_approval|killed reserved for hooks (Phase 2)
    started_at   TEXT NOT NULL,
    ended_at     TEXT,
    title        TEXT,
    source       TEXT NOT NULL DEFAULT 'jsonl'  -- jsonl | hook | both
);
CREATE INDEX idx_sessions_project ON sessions(project_id, started_at DESC);
CREATE INDEX idx_sessions_status  ON sessions(status);

-- ============ Agents & skills (layer 3) — BEFORE events (FK targets) ============

CREATE TABLE agents (
    id          INTEGER PRIMARY KEY,
    name        TEXT NOT NULL,
    scope       TEXT NOT NULL,               -- global | project
    project_id  INTEGER REFERENCES projects(id),
    file_path   TEXT NOT NULL,
    model       TEXT,
    tools_json  TEXT,
    description TEXT,
    current_version_id INTEGER,              -- FK to agent_versions (deferred)
    deleted     INTEGER NOT NULL DEFAULT 0,
    UNIQUE(name, scope, project_id)
);

CREATE TABLE agent_versions (
    id           INTEGER PRIMARY KEY,
    agent_id     INTEGER NOT NULL REFERENCES agents(id),
    content_hash TEXT NOT NULL,
    git_sha      TEXT,
    content      TEXT NOT NULL,
    created_at   TEXT NOT NULL,
    change_note  TEXT,
    UNIQUE(agent_id, content_hash)
);

CREATE TABLE skills (
    id          INTEGER PRIMARY KEY,
    name        TEXT NOT NULL,
    scope       TEXT NOT NULL,
    project_id  INTEGER REFERENCES projects(id),
    dir_path    TEXT NOT NULL,
    description TEXT,
    current_version_id INTEGER,
    deleted     INTEGER NOT NULL DEFAULT 0,
    UNIQUE(name, scope, project_id)
);

CREATE TABLE skill_versions (
    id           INTEGER PRIMARY KEY,
    skill_id     INTEGER NOT NULL REFERENCES skills(id),
    content_hash TEXT NOT NULL,
    git_sha      TEXT,
    content      TEXT NOT NULL,
    created_at   TEXT NOT NULL,
    change_note  TEXT,
    UNIQUE(skill_id, content_hash)
);

CREATE TABLE config_lint_findings (
    id          INTEGER PRIMARY KEY,
    target      TEXT NOT NULL,               -- agent:12 | skill:3 | claude_md:...
    rule        TEXT NOT NULL,
    severity    TEXT NOT NULL,               -- info | warn | error
    message     TEXT NOT NULL,
    detected_at TEXT NOT NULL,
    resolved_at TEXT
);

-- ============ Turns & events (core) ============

CREATE TABLE turns (
    id          INTEGER PRIMARY KEY,
    session_id  INTEGER NOT NULL REFERENCES sessions(id),
    seq         INTEGER NOT NULL,            -- file-order turn-opener counter (C2)
    role        TEXT NOT NULL,               -- user | assistant
    message_id  TEXT,                        -- API message.id (assistant turns);
                -- usage dedup / idempotency key; NULL for user turns
    started_at  TEXT NOT NULL,
    ended_at    TEXT,
    -- usage is duplicated verbatim on each of the N split lines of one API
    -- response (C1): tokens MUST be deduplicated by message.id before storing
    tokens_in   INTEGER,
    tokens_out  INTEGER,
    tokens_cache_read  INTEGER,
    tokens_cache_write INTEGER,
    cost_usd    REAL,
    UNIQUE(session_id, seq)
);

CREATE TABLE events (
    id          INTEGER PRIMARY KEY,
    session_id  INTEGER NOT NULL REFERENCES sessions(id),
    turn_id     INTEGER REFERENCES turns(id),
    ts          TEXT NOT NULL,
    type        TEXT NOT NULL,
        -- tool_call | subagent_start | subagent_stop | skill_use
        -- | file_change | permission_request | permission_resolved
        -- | error | test_run | commit | user_prompt | session_end | unknown
        -- subagent_start/stop are derived from tool_use name="Agent" and its
        -- matching tool_result (C6) — they do not exist as JSONL record types
    tool_name   TEXT,
    agent_id    INTEGER REFERENCES agents(id),
    skill_id    INTEGER REFERENCES skills(id),
    parent_event_id INTEGER REFERENCES events(id),
    status      TEXT,                        -- ok | error | denied | timeout
    duration_ms INTEGER,
    payload     TEXT,                        -- JSON: raw event details
    dedup_key   TEXT UNIQUE                  -- record uuid; sidechain records are
                -- prefixed with agentId (their uuid space restarts per file, C3);
                -- uuid-less lines use SHA-256(file path + raw line)
);
CREATE INDEX idx_events_session ON events(session_id, ts);
CREATE INDEX idx_events_agent   ON events(agent_id, ts);
CREATE INDEX idx_events_type    ON events(type, ts);

-- ============ File changes ============

CREATE TABLE file_changes (
    id           INTEGER PRIMARY KEY,
    event_id     INTEGER NOT NULL REFERENCES events(id),
    session_id   INTEGER NOT NULL REFERENCES sessions(id),
    file_path    TEXT NOT NULL,
    change_type  TEXT NOT NULL,              -- create | edit | delete | rename
    additions    INTEGER,
    deletions    INTEGER,
    diff         TEXT,
    out_of_scope INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX idx_fc_session ON file_changes(session_id);
CREATE INDEX idx_fc_path    ON file_changes(file_path);

-- ============ Approvals (layer 2) ============

CREATE TABLE permission_requests (
    id           INTEGER PRIMARY KEY,
    session_id   INTEGER NOT NULL REFERENCES sessions(id),
    event_id     INTEGER REFERENCES events(id),
    tool_name    TEXT NOT NULL,
    request_json TEXT NOT NULL,
    status       TEXT NOT NULL DEFAULT 'pending',
                 -- pending | approved | denied | expired | resolved_elsewhere
    requested_at TEXT NOT NULL,
    resolved_at  TEXT,
    resolved_via TEXT                        -- dashboard | terminal | mobile
);
CREATE INDEX idx_pr_pending ON permission_requests(status, requested_at);

-- ============ Task queue (layer 2) ============

CREATE TABLE tasks (
    id          INTEGER PRIMARY KEY,
    project_id  INTEGER NOT NULL REFERENCES projects(id),
    title       TEXT NOT NULL,
    prompt      TEXT NOT NULL,
    priority    INTEGER NOT NULL DEFAULT 5,
    status      TEXT NOT NULL DEFAULT 'queued',
                -- queued | running | needs_review | done | failed | cancelled
    session_id  INTEGER REFERENCES sessions(id),
    agent_id    INTEGER REFERENCES agents(id),
    created_at  TEXT NOT NULL,
    started_at  TEXT,
    finished_at TEXT,
    result_note TEXT,
    reverted    INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX idx_tasks_queue ON tasks(status, priority, created_at);

-- ============ Metrics & evals (layer 4) ============

CREATE TABLE daily_rollups (
    day         TEXT NOT NULL,               -- YYYY-MM-DD
    project_id  INTEGER REFERENCES projects(id),
    agent_id    INTEGER REFERENCES agents(id),
    sessions    INTEGER NOT NULL DEFAULT 0,
    tasks_done  INTEGER NOT NULL DEFAULT 0,
    tasks_reverted INTEGER NOT NULL DEFAULT 0,
    tool_calls  INTEGER NOT NULL DEFAULT 0,
    errors      INTEGER NOT NULL DEFAULT 0,
    tokens_in   INTEGER NOT NULL DEFAULT 0,
    tokens_out  INTEGER NOT NULL DEFAULT 0,
    cost_usd    REAL    NOT NULL DEFAULT 0,
    wait_minutes REAL   NOT NULL DEFAULT 0,
    PRIMARY KEY (day, project_id, agent_id)
);

CREATE TABLE eval_suites (
    id         INTEGER PRIMARY KEY,
    agent_id   INTEGER NOT NULL REFERENCES agents(id),
    name       TEXT NOT NULL,
    created_at TEXT NOT NULL
);

CREATE TABLE eval_cases (
    id        INTEGER PRIMARY KEY,
    suite_id  INTEGER NOT NULL REFERENCES eval_suites(id),
    prompt    TEXT NOT NULL,
    check_cmd TEXT,
    expected  TEXT
);

CREATE TABLE eval_runs (
    id               INTEGER PRIMARY KEY,
    suite_id         INTEGER NOT NULL REFERENCES eval_suites(id),
    agent_version_id INTEGER NOT NULL REFERENCES agent_versions(id),
    started_at       TEXT NOT NULL,
    finished_at      TEXT,
    passed           INTEGER,
    failed           INTEGER,
    tokens_total     INTEGER,
    cost_usd         REAL
);

CREATE TABLE eval_results (
    id         INTEGER PRIMARY KEY,
    run_id     INTEGER NOT NULL REFERENCES eval_runs(id),
    case_id    INTEGER NOT NULL REFERENCES eval_cases(id),
    status     TEXT NOT NULL,                -- pass | fail | error
    session_id INTEGER REFERENCES sessions(id),
    notes      TEXT
);

-- ============ Ingest service table ============

CREATE TABLE file_offsets (
    file_path   TEXT PRIMARY KEY,
    byte_offset INTEGER NOT NULL,
    inode       INTEGER
);
