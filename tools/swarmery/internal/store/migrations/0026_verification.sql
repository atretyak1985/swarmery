-- verification (fusion phase 6 — auto-verification): after a dispatched session
-- lands in_review WITHOUT a sentinel, a bounded read-only headless `claude -p`
-- run grades the task's acceptance criteria and stamps tasks.verify_verdict.
-- The verdict columns themselves (verify_verdict, verify_detail, retry_count)
-- already exist on tasks from 0024_task_board and are written via inline SQL in
-- internal/verify (single-writer discipline), never here.
--
-- verification_runs: one durable row per verification attempt (running|pass|fail|
-- inconclusive|error). Durable so the reaper can find zombie `running` rows a
-- crashed/wedged run left behind (>2h → error, task stamped inconclusive), and
-- so a `running` row doubles as the single-flight guard (one in-flight run per
-- task — the partial unique index below closes the SELECT-then-INSERT TOCTOU,
-- mirroring 0023_provision_jobs / 0022_agent_proposals_one_open).
CREATE TABLE verification_runs (
  id                  INTEGER PRIMARY KEY AUTOINCREMENT,
  task_id             INTEGER NOT NULL REFERENCES tasks(id) ON DELETE CASCADE,
  session_id          INTEGER,             -- the VERIFIED dispatched session (FK-shaped; nullable)
  verify_session_uuid TEXT,                -- the verifier's OWN headless session uuid (explicit link)
  status              TEXT NOT NULL         -- running|pass|fail|inconclusive|error
                      CHECK (status IN ('running','pass','fail','inconclusive','error')),
  tree_hash           TEXT,                -- git HEAD^{tree} of the graded worktree
  detail              TEXT,                -- verdict reasons (<=4KB, truncated); 'cache' for a cache-hit row
  started_at          TEXT NOT NULL,
  finished_at         TEXT
);
CREATE INDEX idx_verification_task ON verification_runs(task_id);
-- Single-flight: at most one in-flight (running) verification per task. The
-- second concurrent VerifyTask's INSERT hits this constraint immediately.
CREATE UNIQUE INDEX idx_verification_running
  ON verification_runs(task_id)
  WHERE status = 'running';

-- verification_cache: tree-hash memo so an unchanged worktree is not re-verified.
-- Keyed by (tree_hash, task_id): the same tree graded for a different task is a
-- distinct entry (a task's acceptance criteria differ). Only pass/fail are
-- cached — INCONCLUSIVE is NEVER cached (a transient env failure must not
-- permanently wedge a task at amber; DESIGN.md §4.6).
CREATE TABLE verification_cache (
  tree_hash  TEXT NOT NULL,
  task_id    INTEGER NOT NULL,
  verdict    TEXT NOT NULL CHECK (verdict IN ('pass','fail')),
  created_at TEXT NOT NULL,
  PRIMARY KEY (tree_hash, task_id)
);
