-- memory-engineering gaps (phase 3): the operator correction ledger. One row
-- per time a person overrode what an agent produced — a lesson rewritten,
-- dismissed or retired by hand, a triage verdict undone, a plan revision
-- rejected, a classifier answer contradicted by ground truth. Rows are written
-- ONLY from the operator endpoints in internal/api (never from the domain
-- functions: lessons.Retire is also the verifier's auto-retire, and a daemon
-- correcting itself is not an operator correction).
-- No FKs to hot tables (0073): ref is a typed text pointer instead —
-- 'lesson:12' | 'verdict:88' | 'revision:5' | 'decision:1947'.
-- norm_key folds the reason (or the after text when there is no reason) with
-- wsingest.NormalizeLessonTitle, the same identity R11 groups lessons by, so
-- advisor R14 can count "the same correction" across sources; '' = no identity.
CREATE TABLE operator_corrections (
  id         INTEGER PRIMARY KEY,
  source     TEXT NOT NULL CHECK (source IN ('lesson_edit','lesson_dismiss','lesson_retire','triage_undo','revision_reject','truth_disagree')),
  ref        TEXT NOT NULL,
  project_id INTEGER,                  -- NULL when unknown
  before     TEXT NOT NULL DEFAULT '',
  after      TEXT NOT NULL DEFAULT '',
  reason     TEXT NOT NULL DEFAULT '',
  norm_key   TEXT NOT NULL DEFAULT '',
  created_at TEXT NOT NULL
);
CREATE INDEX idx_operator_corrections_key ON operator_corrections(norm_key, created_at);
