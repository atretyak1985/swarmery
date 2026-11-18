-- 0136: proposal_probes (harness² phase 4 — D9–D11).
--
-- A probe runs an agent change proposal against the agent's promptfoo suite
-- (evals/agents/<agent>.yaml) twice — once on the base agent, once on the
-- patched copy — in a throwaway temp tree, and stores one row per test case.
-- ADVISORY: no code path reads this table to change a proposal's status.
--
--   proposal_id   agent_change_proposals.id. No FK, on purpose (notes 0082 /
--                 0084): a probe row must never block a proposal delete or a
--                 table rebuild, and an orphaned row is harmless history.
--   job_id        the background job that wrote the row (one POST = one job),
--                 so the card can show the latest run as one table.
--   source        'promptfoo' today; 'bench' is reserved for phase 6.
--   task_id       the suite case's description (or "case-N"); '' on the one
--                 no_suite row.
--   base_pass     1/0 — every HARD assertion of the case passed on the base
--   variant_pass  agent / on the proposal. llm-rubric assertions are recorded
--                 in reason but never decide these. NULL when the side did not
--                 run (capped, no_suite).
--   blind_choice  base | variant | tie — the blind judge's pick on a case where
--                 base and variant agree; skipped — they disagree (the
--                 deterministic result already decides) or the judge failed;
--                 capped — SWARMERY_PROBE_MAX_USD reached before this case;
--                 no_suite — the target has no promptfoo suite (SKILL.md).
--   reason        judge reason (≤6 lines), or why the case was skipped/capped.
--   usd           cost of this row: both promptfoo runs plus the judge.
--
-- WHY 0136: migrate.go applies unapplied files in FILENAME order;
-- 0135_proposal_outcome_lens.sql is the highest existing one.
--
-- Rollback: DROP INDEX idx_proposal_probes_proposal; DROP TABLE proposal_probes;
CREATE TABLE proposal_probes (
  id           INTEGER PRIMARY KEY AUTOINCREMENT,
  proposal_id  INTEGER NOT NULL,
  job_id       TEXT    NOT NULL DEFAULT '',
  source       TEXT    NOT NULL DEFAULT 'promptfoo' CHECK (source IN ('promptfoo','bench')),
  task_id      TEXT    NOT NULL DEFAULT '',
  base_pass    INTEGER,
  variant_pass INTEGER,
  blind_choice TEXT    NOT NULL CHECK (blind_choice IN ('base','variant','tie','skipped','capped','no_suite')),
  reason       TEXT    NOT NULL DEFAULT '',
  usd          REAL    NOT NULL DEFAULT 0,
  created_at   TEXT    NOT NULL
);
CREATE INDEX idx_proposal_probes_proposal ON proposal_probes(proposal_id, id);
