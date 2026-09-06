-- Which model a planning wizard runs on. The first turn always carried --model
-- (the planner pins Opus so the run does not inherit the account default), but
-- every RESUME turn — answer, refine, proceed — spawned without it and fell back
-- to the CLI default, so an interview started on Opus finished on whatever the
-- account defaults to. The full model ID is stamped here at Start and read back
-- by every resume, so one wizard runs on one model end to end. NULL on
-- historical rows means "the planner default".
ALTER TABLE planning_sessions ADD COLUMN model TEXT;
