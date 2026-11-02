-- 0134: lesson discipline (Harness² D6) — one lesson, a stated confidence, and
-- "nothing transferable here" as a recorded answer.
--
-- surprise_lessons.confidence is the generator's own grade of a lesson: 'high',
-- 'medium' or 'low'. A lesson resting on a single run, or on a run where several
-- things changed at once, is at most 'low', and Accepting a 'low' one needs the
-- operator's explicit confirm (POST /api/lessons/{id}/accept {"confirm": true}).
-- confidence_reason is the generator's one-sentence why. Existing rows keep
-- 'medium': they were never graded, and 'medium' neither promotes nor gates them.
--
-- lesson_generations.no_entry_reason is the surprise generator's answer when it
-- returned no lesson ({"lessons": [], "no_entry_reason": "…"}). The review-born
-- path has no run record: its reason goes on the candidate's cause text and the
-- review tick's log line instead (internal/lessons/fromreview.go).
--
-- The ALTERs are instant on the live tables (constant defaults, no rebuild); the
-- CHECK is evaluated against the existing rows, which all take the default.
--
-- WHY 0134: 0133 (lesson_effect_verdict, Harness² phase 1) is the highest on main.
--
-- Rollback: ALTER TABLE lesson_generations DROP COLUMN no_entry_reason;
--           ALTER TABLE surprise_lessons DROP COLUMN confidence_reason;
--           ALTER TABLE surprise_lessons DROP COLUMN confidence;

ALTER TABLE surprise_lessons ADD COLUMN confidence TEXT NOT NULL DEFAULT 'medium'
  CHECK (confidence IN ('high', 'medium', 'low'));
ALTER TABLE surprise_lessons ADD COLUMN confidence_reason TEXT NOT NULL DEFAULT '';
ALTER TABLE lesson_generations ADD COLUMN no_entry_reason TEXT NOT NULL DEFAULT '';
