-- 0128: lesson candidates from repeated review findings (verify-not-trust phase 8,
-- D3/D38). A finding type that recurs in one code area across two or more phases
-- becomes ONE surprise_lessons candidate (internal/lessons/fromreview.go) — never
-- a rule: Accept stays the only path to 'active' (activeguard_test.go).
--
-- surprise_lessons.source tells the two producers apart: 'surprise' (a run that
-- landed far from its forecast, 0084) and 'review' (a repeated finding).
-- review_type / review_area are a review candidate's group identity — the finding
-- type and the area its files lie in — so a later tick extends the same row's
-- evidence_json instead of proposing it again, and a dismissal knows what to mute.
-- Both stay '' on a surprise lesson.
--
-- lesson_mutes holds the operator's "not this one": dismissing a review candidate
-- mutes its (type, area) for 30 days (lessons.Dismiss). A mute expires by time;
-- the proposer reads muted_until, and muted_at so that an expired mute is lifted
-- only by a finding newer than the dismissal.
--
-- The ALTERs are instant on the live table (constant defaults, no rebuild); the
-- CHECK is evaluated against the existing rows, which all take the default.
--
-- WHY 0128: 0127 (turns_cold_restart) is the highest on main and 0125 is held by
-- an open branch (stall_notices).
--
-- NO FOREIGN KEYS to hot tables, for the reasons 0073/0077/0082 give.
--
-- Rollback: DROP INDEX IF EXISTS idx_review_findings_type;
--           DROP INDEX IF EXISTS idx_surprise_lessons_review;
--           DROP TABLE IF EXISTS lesson_mutes;
--           ALTER TABLE surprise_lessons DROP COLUMN review_area;
--           ALTER TABLE surprise_lessons DROP COLUMN review_type;
--           ALTER TABLE surprise_lessons DROP COLUMN source;

ALTER TABLE surprise_lessons ADD COLUMN source TEXT NOT NULL DEFAULT 'surprise'
  CHECK (source IN ('surprise', 'review'));
ALTER TABLE surprise_lessons ADD COLUMN review_type TEXT NOT NULL DEFAULT '';
ALTER TABLE surprise_lessons ADD COLUMN review_area TEXT NOT NULL DEFAULT '';

-- the proposer's "is this group already a lesson?" lookup
CREATE INDEX IF NOT EXISTS idx_surprise_lessons_review ON surprise_lessons(source, review_type);

CREATE TABLE IF NOT EXISTS lesson_mutes (
  id          INTEGER PRIMARY KEY,
  type        TEXT    NOT NULL,           -- review_findings.type
  area        TEXT    NOT NULL,           -- the group's area (surprise.AreaOf of its files)
  muted_at    TEXT    NOT NULL,           -- when the operator dismissed the candidate
  muted_until TEXT    NOT NULL,           -- muted_at + 30 days
  lesson_id   INTEGER NOT NULL DEFAULT 0, -- the dismissed surprise_lessons.id (no FK)
  UNIQUE (type, area)
);

-- GroupFindings reads review_findings by type
CREATE INDEX IF NOT EXISTS idx_review_findings_type ON review_findings(type);
