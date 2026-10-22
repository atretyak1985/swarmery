-- 0124: the guard check of a phase run (verify-not-trust phase 4,
-- internal/phaserun/guardcheck.go). After a run settles, the daemon audits it
-- against the guard block of the doc as lent — protected files the run changed,
-- the lent copy compared with that doc, the hook's .swarmery/guard.log — and
-- records the result as JSON (phaserun.GuardResult):
--
--   phase_reviews.guard_json    — on every review row, so the Review tab shows
--                                 the guard badge next to the reviewer's verdict;
--                                 '{}' on rows written before this migration.
--   phase_decisions.guard_json  — on the source='stop' decision a flagged run
--                                 raises, so the needs-you card and the answer
--                                 endpoint know which paths the decision is about.
--
-- Rollback: ALTER TABLE phase_reviews DROP COLUMN guard_json;
--           ALTER TABLE phase_decisions DROP COLUMN guard_json;
ALTER TABLE phase_reviews ADD COLUMN guard_json TEXT NOT NULL DEFAULT '{}';
ALTER TABLE phase_decisions ADD COLUMN guard_json TEXT NOT NULL DEFAULT '{}';
