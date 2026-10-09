-- 0103: a plan phase's landing lifecycle — how a finished phase run's branch
-- reaches the remote (pushed) and, optionally, a change request (pr_open → merged,
-- or returned to the agent).
--
-- Daemon-owned, like run_branch (0043): the doc authors nothing here; wsingest's
-- upsert must never list these columns. wsingest.PhaseUpsertSQL deliberately leaves
-- them out of its ON CONFLICT … DO UPDATE SET list, so a re-scan of the plan dir (the
-- executor ticking its own checkboxes, a plan regeneration) cannot reset a pushed or
-- opened phase back to 'none'.
--
-- landing_state: none | pushed | pr_open | merged | returned. 'ready' is DERIVED at
-- read time (landing_state = 'none' AND run_state = 'done') and is never stored, so a
-- re-scan that flips run_state never needs a landing write.
ALTER TABLE epic_phases ADD COLUMN landing_state TEXT NOT NULL DEFAULT 'none';
ALTER TABLE epic_phases ADD COLUMN pr_url TEXT;
ALTER TABLE epic_phases ADD COLUMN pr_provider TEXT;
ALTER TABLE epic_phases ADD COLUMN pr_number INTEGER;
ALTER TABLE epic_phases ADD COLUMN pr_status TEXT;
ALTER TABLE epic_phases ADD COLUMN pr_checked_at TEXT;
ALTER TABLE epic_phases ADD COLUMN landed_at TEXT;
ALTER TABLE epic_phases ADD COLUMN landing_error TEXT;
