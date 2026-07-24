-- epic_phases (fusion phase 10 — epic rollups): a workspace plan IS an epic and
-- its README phase-sequencing table rows ARE the phases. The wsingest scanner
-- parses plan/README.md + each plan/phase-*.md|step-*.md into rows here, behind
-- the same content-hash gate as the other artifacts (task_artifacts kind
-- 'plan'); the API rolls them up per task and activates a phase into a board
-- task.
--
-- workspace_task_id references the workspace-sourced tasks row (tasks.id, the
-- INTEGER PK — source='workspace'); the row is deleted+reinserted per rescan of
-- a changed plan, so no FK cascade machinery is needed (SQLite FKs are off by
-- default here and the scanner owns the lifecycle). UNIQUE(workspace_task_id,
-- doc_path) makes the upsert idempotent and lets a phase carry a stable
-- identity across rescans. activated_board_task_id records the board task an
-- activation minted (NULL until a human activates the phase), so a re-activate
-- is a cheap 409 with the existing id and progress can link to the created card.
CREATE TABLE epic_phases (
    id                      INTEGER PRIMARY KEY AUTOINCREMENT,
    workspace_task_id       INTEGER NOT NULL,       -- FK tasks.id (source='workspace')
    seq                     INTEGER NOT NULL,        -- 1-based order within the epic
    name                    TEXT NOT NULL,
    doc_path                TEXT NOT NULL,           -- absolute path to the phase/step doc
    depends_on              TEXT NOT NULL DEFAULT '[]', -- JSON array of seq numbers
    checkboxes_total        INTEGER NOT NULL DEFAULT 0,
    checkboxes_done         INTEGER NOT NULL DEFAULT 0,
    activated_at            TEXT,                    -- RFC3339 when a board task was minted
    activated_board_task_id INTEGER,                 -- FK tasks.id of the created board task
    UNIQUE(workspace_task_id, doc_path)
);

CREATE INDEX idx_epic_phases_task ON epic_phases(workspace_task_id, seq);

-- Widen task_artifacts.kind to admit the 'plan' gate row. SQLite cannot ALTER a
-- CHECK constraint, so rebuild the table with the standard create→copy→swap
-- dance (data preserved). The gate keys the epic parser's content-hash the same
-- way it keys retro/orchestration/agents_log — one row per (task, 'plan').
CREATE TABLE task_artifacts_new (
  task_id      INTEGER NOT NULL REFERENCES tasks(id) ON DELETE CASCADE,
  kind         TEXT    NOT NULL CHECK (kind IN ('retro','orchestration','agents_log','plan')),
  path         TEXT    NOT NULL,
  content_hash TEXT    NOT NULL,
  parsed_at    TEXT    NOT NULL,
  PRIMARY KEY (task_id, kind)
);
INSERT INTO task_artifacts_new (task_id, kind, path, content_hash, parsed_at)
  SELECT task_id, kind, path, content_hash, parsed_at FROM task_artifacts;
DROP TABLE task_artifacts;
ALTER TABLE task_artifacts_new RENAME TO task_artifacts;
