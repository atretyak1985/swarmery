-- 0070: cross-task identity for retro lessons (agent-memory phase 4).
--
-- `retro_lessons` rows are DELETE+reinserted on every retro rescan (wsingest
-- artifacts.go applyRetro), so `id` is not an identity a second task can share.
-- Until now the same lesson learned in ten tasks was ten unrelated rows, and the
-- Retro page showed it ten times as ten separate one-off observations.
--
-- norm_title is that missing identity: the lesson title folded by
-- wsingest.NormalizeLessonTitle (lowercase, punctuation stripped, "Lesson N:"
-- prefix and stop-words dropped, whitespace collapsed, 80 runes). Equal
-- norm_title == the same lesson, whatever the wording drifted to. The reinsert
-- semantics are deliberately untouched: identity lives in the column now, and
-- that is enough.
--
-- NOT NULL DEFAULT '' so the ALTER is instant on the live table and existing
-- rows are legal immediately. '' means "not folded yet", not "no lesson": the
-- wsingest startup backfill (BackfillNormTitles) fills those rows once per
-- daemon start, and every rule and endpoint that reads norm_title skips '' so
-- an unbackfilled row can never be grouped with a real one.

ALTER TABLE retro_lessons ADD COLUMN norm_title TEXT NOT NULL DEFAULT '';

CREATE INDEX idx_retro_lessons_norm_title ON retro_lessons(norm_title);
