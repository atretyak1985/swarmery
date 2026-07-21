-- 0017: retro artifact ingestion (retro improvement loop, phase 2).
--
-- wsingest parses three per-task workspace artifacts into structured rows:
--   phases/09-retrospective.md → task_retros + retro_lessons + retro_improvements
--   ORCHESTRATION.md           → task_loops (quality-gate re-dispatch journal)
--   logs/agents.md             → task_delegations (one row per delegation)
-- task_artifacts is the content-hash gate: one row per (task, kind) recording
-- the SHA-256 of the last parsed file — unchanged files are skipped on rescan;
-- a changed file deletes + reinserts its child rows in one tx.

CREATE TABLE task_artifacts (
  task_id      INTEGER NOT NULL REFERENCES tasks(id) ON DELETE CASCADE,
  kind         TEXT    NOT NULL CHECK (kind IN ('retro','orchestration','agents_log')),
  path         TEXT    NOT NULL,
  content_hash TEXT    NOT NULL,
  parsed_at    TEXT    NOT NULL,
  PRIMARY KEY (task_id, kind)
);

CREATE TABLE task_retros (
  id              INTEGER PRIMARY KEY,
  task_id         INTEGER NOT NULL UNIQUE REFERENCES tasks(id) ON DELETE CASCADE,
  estimated_hours REAL,
  actual_hours    REAL,
  variance_pct    REAL,
  ingested_at     TEXT NOT NULL
);

CREATE TABLE retro_lessons (
  id       INTEGER PRIMARY KEY,
  retro_id INTEGER NOT NULL REFERENCES task_retros(id) ON DELETE CASCADE,
  seq      INTEGER NOT NULL,
  title    TEXT NOT NULL,
  body     TEXT,
  action   TEXT
);

CREATE TABLE retro_improvements (
  id       INTEGER PRIMARY KEY,
  retro_id INTEGER NOT NULL REFERENCES task_retros(id) ON DELETE CASCADE,
  text     TEXT NOT NULL,
  priority TEXT,
  owner    TEXT,
  status   TEXT
);

CREATE TABLE task_loops (
  id          INTEGER PRIMARY KEY,
  task_id     INTEGER NOT NULL REFERENCES tasks(id) ON DELETE CASCADE,
  loop_n      INTEGER NOT NULL,
  failed      TEXT,
  brief_delta TEXT,
  UNIQUE (task_id, loop_n)
);

CREATE TABLE task_delegations (
  id       INTEGER PRIMARY KEY,
  task_id  INTEGER NOT NULL REFERENCES tasks(id) ON DELETE CASCADE,
  seq      INTEGER NOT NULL,
  agent    TEXT NOT NULL,
  phase    TEXT,
  verdict  TEXT,
  artifact TEXT,
  UNIQUE (task_id, seq)
);
CREATE INDEX idx_task_delegations_agent ON task_delegations(agent);
