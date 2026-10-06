-- 0098: decisions.ground_truth_source records WHO wrote a ground-truth label.
--
-- Until now a label carried no author: the D1 recorder wrote what a run did,
-- and an operator answered the rest in the dashboard. A triage agent is about
-- to start answering too, and an agent's label must never feed a threshold
-- (eval agreement, Summary.Agreed, the D2 taxonomy gate) — those measure the
-- classifier AGAINST an independent judge, and an agent is not one.
--
-- Values: 'operator' (a person in the dashboard), 'agent' (a triage run),
-- 'observed' (the daemon recorded what a run did — D1), '' (no label yet).
--
-- Backfill: every existing label predates the agent, so a d1.run_end label is
-- 'observed' and every other label is 'operator'. ground_truth and
-- ground_truth_at are not touched.
ALTER TABLE decisions ADD COLUMN ground_truth_source TEXT NOT NULL DEFAULT '';

UPDATE decisions
   SET ground_truth_source = CASE WHEN question_id = 'd1.run_end' THEN 'observed' ELSE 'operator' END
 WHERE ground_truth IS NOT NULL;
