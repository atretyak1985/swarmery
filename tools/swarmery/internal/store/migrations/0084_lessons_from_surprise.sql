-- 0084: lessons from surprise — area-scoped, reviewable lesson candidates.
--
-- Opus 5.5 / learning-loop phase 14. A phase run whose surprise index
-- (phase_surprise, 0082) crossed the attention threshold AND whose Completion
-- Report explains the gap ("Where reality diverged") is handed to a cheap
-- headless model, which may return 0–2 small lessons: one imperative sentence of
-- guidance, the area globs it applies to, and evidence ids copied from its input.
-- internal/lessons writes them; nothing reaches a future run until an OPERATOR
-- accepts one on the dashboard (POST /api/lessons/{id}/accept — the only code
-- path that sets status = 'active').
--
-- 14.1 — the retro lessons store grows the same shape. retro_lessons rows are
-- written by a human in a retrospective doc, so they are ALREADY accepted
-- knowledge: they default to area '*' (everywhere) and status 'active'. The
-- ALTERs are instant on the live table (constant defaults, no rebuild), which
-- is the same reason 0070 gave for norm_title.
--
-- WHY A SIBLING TABLE for the surprise-born lessons instead of more rows in
-- retro_lessons: retro_lessons.retro_id is NOT NULL REFERENCES task_retros, and
-- wsingest DELETEs + reinserts a retro's rows on every rescan (0070's note).
-- A candidate has no retro, and an operator decision stored on a row that the
-- next rescan deletes would be lost. surprise_lessons carries the same lesson
-- columns plus the review state; norm_title is the shared identity between the
-- two (wsingest.NormalizeLessonTitle), which is what 14.5's dedup and the
-- review queue's merge suggestions key on.
--
-- WHY 0084: migrate.go applies unapplied files in FILENAME order; 0083
-- (decisions) is the highest existing one.
--
-- NO FOREIGN KEYS to hot tables (epic_phases, sessions, phase_surprise), for the
-- reasons 0073/0077/0082 give: PRAGMA foreign_keys=ON plus an unindexed child
-- column wedged the first retention prune for 11.5 hours.

ALTER TABLE retro_lessons ADD COLUMN area_globs       TEXT NOT NULL DEFAULT '*';
ALTER TABLE retro_lessons ADD COLUMN guidance         TEXT NOT NULL DEFAULT '';
ALTER TABLE retro_lessons ADD COLUMN evidence_json    TEXT NOT NULL DEFAULT '[]';
ALTER TABLE retro_lessons ADD COLUMN source_phase_run TEXT NOT NULL DEFAULT '';
ALTER TABLE retro_lessons ADD COLUMN status           TEXT NOT NULL DEFAULT 'active';
ALTER TABLE retro_lessons ADD COLUMN activated_at     TEXT;
ALTER TABLE retro_lessons ADD COLUMN retired_at       TEXT;
ALTER TABLE retro_lessons ADD COLUMN retire_reason    TEXT;

CREATE TABLE IF NOT EXISTS surprise_lessons (
  id                   INTEGER PRIMARY KEY,
  source_phase_run     TEXT    NOT NULL,             -- the run's session uuid (phase_surprise.session_uuid)
  phase_id             INTEGER NOT NULL,             -- epic_phases.id (no FK, see above)
  seq                  INTEGER NOT NULL,             -- 1..2 within the generation
  title                TEXT    NOT NULL,
  norm_title           TEXT    NOT NULL DEFAULT '',  -- wsingest.NormalizeLessonTitle(title)
  guidance             TEXT    NOT NULL,             -- one imperative sentence
  area_globs           TEXT    NOT NULL DEFAULT '*', -- comma-separated globs; '*' = everywhere
  evidence_json        TEXT    NOT NULL DEFAULT '[]',-- JSON ["kind:id", …], every id present in the input
  cause                TEXT    NOT NULL DEFAULT '',  -- the cited cause the lesson draws on
  source_paragraph     TEXT    NOT NULL DEFAULT '',  -- the "Where reality diverged" paragraph
  surprise_index       REAL,
  status               TEXT    NOT NULL DEFAULT 'candidate'
                       CHECK (status IN ('candidate','active','retired','dismissed','merged')),
  -- 14.5: an existing lesson with the same identity. For a retro lesson this is
  -- its norm_title; for a surprise lesson merged by the operator, merged_into_id.
  linked_norm_title    TEXT    NOT NULL DEFAULT '',
  merged_into_id       INTEGER,
  recurrences          INTEGER NOT NULL DEFAULT 1,   -- runs that produced this identity
  recurrence_runs_json TEXT    NOT NULL DEFAULT '[]',-- their session uuids
  model                TEXT    NOT NULL DEFAULT '',
  created_at           TEXT    NOT NULL,
  updated_at           TEXT    NOT NULL,
  activated_at         TEXT,
  retired_at           TEXT,
  retire_reason        TEXT,
  UNIQUE (source_phase_run, seq)
);

-- the review queue: "candidates, newest first"
CREATE INDEX IF NOT EXISTS idx_surprise_lessons_status ON surprise_lessons(status, id);
-- 14.5 dedup and the merge suggestions
CREATE INDEX IF NOT EXISTS idx_surprise_lessons_norm ON surprise_lessons(norm_title);
-- the orphan sweep / per-phase reads
CREATE INDEX IF NOT EXISTS idx_surprise_lessons_phase ON surprise_lessons(phase_id);

-- One row per run that was ELIGIBLE (surprising + explained) and therefore
-- claimed for generation. The primary key is the idempotency: the run-end and
-- the settled pass both score a run, and only the first claim spends tokens.
-- A failed generation stays failed — no retry loop, no repeated spend.
CREATE TABLE IF NOT EXISTS lesson_generations (
  source_phase_run TEXT    PRIMARY KEY,
  phase_id         INTEGER NOT NULL,
  state            TEXT    NOT NULL CHECK (state IN ('running','done','failed')),
  inserted         INTEGER NOT NULL DEFAULT 0,   -- new candidate rows
  linked           INTEGER NOT NULL DEFAULT 0,   -- lessons folded into an existing identity
  rejected_json    TEXT    NOT NULL DEFAULT '[]',-- [{title, reason}] — lessons that failed validation
  error            TEXT    NOT NULL DEFAULT '',
  model            TEXT    NOT NULL DEFAULT '',
  created_at       TEXT    NOT NULL,
  finished_at      TEXT
);

CREATE INDEX IF NOT EXISTS idx_lesson_generations_phase ON lesson_generations(phase_id);
