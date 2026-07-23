-- 7-cell assessment ledger (tech-lead ≥ core 2.2.0):
--   agent | phase | verdict | loops | quality | mistakes | artifact
ALTER TABLE task_delegations ADD COLUMN loops    INTEGER;
ALTER TABLE task_delegations ADD COLUMN quality  INTEGER;  -- 1..5, NULL = legacy row
ALTER TABLE task_delegations ADD COLUMN mistakes TEXT;
