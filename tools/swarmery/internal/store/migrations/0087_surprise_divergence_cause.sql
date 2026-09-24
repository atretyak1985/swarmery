-- 0087: phase_surprise.divergence_cause — WHY a scored run diverged.
--
-- Opus 5.5 / learning-loop step 14.3 (optional in the plan, shipped late). The
-- local decision classifier (internal/decide, question d3.divergence_cause)
-- reads a surprising run's "Where reality diverged" paragraph and labels the
-- cause: spec-wrong | code-differs | scope-grew | tooling-env | model-fallback
-- | other, or 'unknown' when the answer fell below the active threshold.
--
-- SHADOW FIRST. In shadow mode (the default) the answer lives only in the
-- decisions table (0083); these columns are written only once the operator
-- switches the question to active on the Decisions page. NULL = never
-- classified, which is different from 'unknown' (asked, not confident).
--
-- ANALYTICS ONLY. Nothing reads these columns to decide anything: no run, no
-- lesson status, no retirement. Phase 17 slices surprise by them.
--
-- The surprise upsert (internal/surprise/store.go) names its SET columns
-- explicitly, so a rescore of the run leaves the label in place — the same
-- survival rule notified_at / auto_verify_at rely on.
--
-- WHY 0087: next number above the highest existing migration (0086), see 0074.
-- Plain ALTER TABLE ADD COLUMN with NULL defaults: no rewrite, no FK, no index
-- (a per-cause histogram over a few hundred rows needs none).

ALTER TABLE phase_surprise ADD COLUMN divergence_cause TEXT;
ALTER TABLE phase_surprise ADD COLUMN divergence_cause_confidence REAL;
ALTER TABLE phase_surprise ADD COLUMN divergence_cause_decision_id INTEGER;
ALTER TABLE phase_surprise ADD COLUMN divergence_cause_at TEXT;
