-- Seed for internal/handoff rejected_test.go (verify-not-trust phase 19): one
-- session working on a plan born in Planning Mode, one session with no plan, and
-- noise that must not leak into either. Loaded after store.Open migrated the
-- schema and the test inserted projects(1, '/work/alpha').
--
-- phase_decisions is verify-not-trust phase 2 (PR #628). Until that migration is
-- on main the table does not exist, so the seed creates it with phase 2's exact
-- schema; once the migration lands, CREATE TABLE IF NOT EXISTS is a no-op and
-- the seed keeps working unchanged.
CREATE TABLE IF NOT EXISTS phase_decisions (
  id                  INTEGER PRIMARY KEY,
  workspace_task_id   INTEGER,
  planning_session_id INTEGER,
  decision_id         TEXT NOT NULL,
  seq                 INTEGER NOT NULL,
  source              TEXT NOT NULL DEFAULT 'interview'
                      CHECK (source IN ('interview','stop','review')),
  question_id         TEXT NOT NULL DEFAULT '',
  question            TEXT NOT NULL DEFAULT '',
  cost                TEXT NOT NULL DEFAULT 'cheap' CHECK (cost IN ('cheap','expensive')),
  cost_reason         TEXT NOT NULL DEFAULT '',
  status              TEXT NOT NULL CHECK (status IN ('decided','undecided','recommended')),
  answer              TEXT NOT NULL DEFAULT '',
  option_ids_json     TEXT NOT NULL DEFAULT '[]',
  phase_id            INTEGER,
  created_at          TEXT NOT NULL,
  decided_at          TEXT,
  UNIQUE (workspace_task_id, decision_id),
  UNIQUE (planning_session_id, decision_id)
);

INSERT INTO sessions (id, project_id, session_uuid, status, cwd, git_branch, title, started_at, hidden, pruned) VALUES
  (1, 1, 'rej-plan-session',   'active', '/work/alpha', 'feat/cache', 'Cache layer work', '2026-10-21T09:00:00Z', 0, 0),
  (2, 1, 'rej-noplan-session', 'active', '/work/alpha', 'dev',        'Loose work',       '2026-10-21T09:00:00Z', 0, 0),
  (3, 1, 'rej-phase-run',      'active', '/work/alpha', 'swarm/phase-100', 'Phase run',   '2026-10-21T09:00:00Z', 0, 0);

-- The plan: a workspace task whose plan artifact has since been ARCHIVED, while
-- the wizard recorded the working-zone dir — the zone must not break the link.
INSERT INTO tasks (id, project_id, title, prompt, created_at, source, external_id) VALUES
  (10, 1, 'Cache layer', '', '2026-10-21T08:00:00Z', 'workspace', '2026-10-21-cache-layer'),
  (11, 1, 'Other plan',  '', '2026-10-21T08:00:00Z', 'workspace', '2026-10-21-other-plan');
INSERT INTO task_artifacts (task_id, kind, path, content_hash, parsed_at) VALUES
  (10, 'plan', '/ws/alpha/workspace/archive/2026/10/21/cache-layer/plan', 'h10', '2026-10-21T08:00:00Z'),
  (11, 'plan', '/ws/alpha/workspace/working/2026/10/21/other-plan/plan',  'h11', '2026-10-21T08:00:00Z');
INSERT INTO epic_phases (id, workspace_task_id, seq, name, doc_path) VALUES
  (100, 10, 1, 'Phase 1 — store', '/ws/alpha/workspace/archive/2026/10/21/cache-layer/plan/phase-1-store.md');

-- Session 1 picked the plan in its terminal (by external id).
INSERT INTO session_plan (session_id, task_id, phase_id, chosen_by, at) VALUES
  ('rej-plan-session', '2026-10-21-cache-layer', NULL, 'terminal', '2026-10-21T09:05:00.000Z');

-- The wizard that wrote the plan (5) and one that wrote another plan (6).
INSERT INTO planning_sessions (id, project_id, session_uuid, status, idea, plan_dir, created_at, updated_at) VALUES
  (5, 1, 'wizard-cache', 'done', 'Add a cache layer', '/ws/alpha/workspace/working/2026/10/21/cache-layer/', '2026-10-21T07:00:00Z', '2026-10-21T07:30:00Z'),
  (6, 1, 'wizard-other', 'done', 'Something else',    '/ws/alpha/workspace/working/2026/10/21/other-plan/plan', '2026-10-21T07:00:00Z', '2026-10-21T07:30:00Z');

-- Three questions, two answered. D-3 was left open in the interview (expensive)
-- and decided later on the dashboard — only phase_decisions knows its answer.
INSERT INTO planning_turns (planning_session_id, seq, question, answer, created_at) VALUES
  (5, 1, '{"id":"q-store","type":"single_select","question":"Which store backs the cache?","options":[{"id":"redis","label":"Redis","description":"Fast shared in-memory store. Needs a new service to run."},{"id":"sqlite","label":"SQLite table","description":"Reuses the daemon DB."},{"id":"other","label":"Other","isOther":true}]}',
         '{"kind":"answer","selectedOptionIds":["sqlite"]}', '2026-10-21T07:05:00Z'),
  (5, 2, '{"id":"q-evict","type":"single_select","question":"How are entries evicted?","options":[{"id":"lru","label":"LRU eviction","description":"Evicts the least recently used entry when full."},{"id":"ttl","label":"TTL only","description":"Entries expire after a fixed time."}]}',
         '{"kind":"answer","selectedOptionIds":["ttl"]}', '2026-10-21T07:10:00Z'),
  (5, 3, '{"id":"q-warm","type":"single_select","question":"When is the cache filled?","options":[{"id":"eager","label":"Eager warm-up","description":"Fills the cache at daemon start."},{"id":"lazy","label":"Lazy fill","description":"Fills on the first miss."}]}',
         NULL, '2026-10-21T07:15:00Z'),
  (6, 1, '{"id":"q-x","type":"single_select","question":"Unrelated?","options":[{"id":"a","label":"Leaked option A"},{"id":"b","label":"Option B"}]}',
         '{"kind":"answer","selectedOptionIds":["b"]}', '2026-10-21T07:05:00Z');

-- D-1 mirrors the interview (adopted onto the task); D-3 decided after the
-- interview; D-4 is a stop decision with no interview question (no variants);
-- D-2 has no row, so its interview answer stands.
INSERT INTO phase_decisions (id, workspace_task_id, planning_session_id, decision_id, seq, source, question_id, question, cost, status, answer, option_ids_json, created_at, decided_at) VALUES
  (1, 10, 5,    'D-1', 1, 'interview', 'q-store', 'Which store backs the cache?', 'cheap',     'decided', 'SQLite table', '["sqlite"]', '2026-10-21T07:30:00Z', '2026-10-21T07:30:00Z'),
  (2, 10, 5,    'D-3', 3, 'interview', 'q-warm',  'When is the cache filled?',    'expensive', 'decided', 'Lazy fill',    '["lazy"]',   '2026-10-21T07:30:00Z', '2026-10-21T10:00:00Z'),
  (3, 10, NULL, 'D-4', 4, 'stop',      '',        'Keep the old cache flag?',     'cheap',     'decided', 'drop it',      '[]',         '2026-10-21T11:00:00Z', '2026-10-21T11:00:00Z');

-- Classifier decisions: about session 1, about a phase run of the plan, about
-- the no-plan session, about a stranger.
INSERT INTO decisions (id, question_id, subject, session_uuid, input_hash, answer, created_at) VALUES
  (900, 'd2.task_type', 'rej-plan-session',   'rej-plan-session',   'h', 'refactor', '2026-10-21T12:00:00Z'),
  (901, 'd1.run_end',   'phaserun:100',       '',                   'h', 'continue', '2026-10-21T12:00:00Z'),
  (902, 'd2.task_type', 'rej-noplan-session', 'rej-noplan-session', 'h', 'feature',  '2026-10-21T12:00:00Z'),
  (903, 'd2.task_type', 'stranger',           'stranger',           'h', 'docs',     '2026-10-21T12:00:00Z');

INSERT INTO plan_revisions (id, workspace_task_id, plan_dir, status, reason, created_at) VALUES
  (7, 10, '/ws/alpha/workspace/working/2026/10/21/cache-layer/plan', 'rejected', 'Split phase 1 into a migration and a backfill', '2026-10-21T13:00:00Z');

INSERT INTO operator_corrections (source, ref, before, after, reason, created_at) VALUES
  ('truth_disagree',  'decision:900', 'refactor', 'bugfix', 'd2.task_type: refactor -> bugfix', '2026-10-21T12:30:00.000Z'),
  ('truth_disagree',  'decision:901', 'continue', 'stop',   'd1.run_end: continue -> stop',     '2026-10-21T12:31:00.000Z'),
  ('revision_reject', 'revision:7',   'Split phase 1 into a migration and a backfill', '', 'the backfill must ship with the migration', '2026-10-21T13:05:00.000Z'),
  ('truth_disagree',  'decision:902', 'feature',  'docs',   'd2.task_type: feature -> docs',    '2026-10-21T12:32:00.000Z'),
  ('truth_disagree',  'decision:903', 'docs',     'chore',  'd2.task_type: docs -> chore',      '2026-10-21T12:33:00.000Z'),
  ('lesson_edit',     'lesson:1',     'old lesson text', 'new lesson text', '', '2026-10-21T12:34:00.000Z');
