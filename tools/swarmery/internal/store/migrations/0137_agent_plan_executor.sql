-- 0137: agents.plan_executor — the agent can be handed a WHOLE plan.
--
-- Set by sysscan from the agent's frontmatter (`planExecutor: true`). The Plans
-- page's run-plan picker offers only these agents: a plan run hands the agent a
-- plan directory and expects it to orchestrate phases, which most agents (a
-- reviewer, a test runner, a domain specialist) are not written to do.
-- 0 for every agent until the next scan reads its frontmatter.
ALTER TABLE agents ADD COLUMN plan_executor INTEGER NOT NULL DEFAULT 0;
