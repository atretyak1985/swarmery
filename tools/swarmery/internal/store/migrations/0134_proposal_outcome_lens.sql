-- 0134: form, lens and outcome on agent_change_proposals (harness² phase 3 —
-- D7, D8).
--
-- The rewriter now answers in a stricter contract (internal/improve/parse.go):
-- a FORM line naming WHERE the change belongs on the form ladder, a lens on
-- every diff hunk, optional follow-ups for forms outside the rewriter's reach,
-- and KEEP_BASE as a real answer. The columns record that answer:
--
--   outcome                   '' | 'kept_base'. 'kept_base' is the model's
--                             KEEP_BASE answer: no diff, one paragraph why. The
--                             row is written status='rejected' (terminal) so
--                             it does NOT hold the one-open slot — the partial
--                             index idx_agent_proposals_one_open (0074) covers
--                             only proposed|approved|needs_target. It is NOT an
--                             operator rejection: internal/corrections records
--                             only operator endpoints, and the card renders it
--                             apart from a human "rejected".
--   lens                      '' | 'procedure' | 'knowledge' | 'mixed' — the
--                             single hunk lens, or 'mixed'. Phase-1's proposal
--                             confounder (internal/lessons/confound.go) quotes
--                             it in its detail. '' on rows written before this
--                             migration and on KEEP_BASE rows (no hunks).
--   form                      '' | 'skill' | 'script' | 'subagent' | 'hook' |
--                             'always-on' | 'keep_base' — the FORM line.
--   suggested_followups_json  JSON array of {"form","text"}: changes in a form
--                             the one-file rewriter cannot make (a script, a
--                             hook, a subagent). '[]' for none.
--   warnings_json             JSON array of strings: non-blocking apply-gate
--                             warnings, today the always-on net-growth check
--                             (an agent .md or CLAUDE.md diff that adds more
--                             lines than it removes). '[]' for none.
--
-- WHY 0134: migrate.go applies unapplied files in FILENAME order;
-- 0133_lesson_effect_verdict.sql is the highest existing one.
--
-- The status CHECK is deliberately untouched (widening it needs the table
-- rebuild 0074 did); no CHECK on the new columns for the same reason — each is
-- written by one Go function from a closed set of constants.
--
-- Rollback: ALTER TABLE agent_change_proposals DROP COLUMN warnings_json;
--           ALTER TABLE agent_change_proposals DROP COLUMN suggested_followups_json;
--           ALTER TABLE agent_change_proposals DROP COLUMN form;
--           ALTER TABLE agent_change_proposals DROP COLUMN lens;
--           ALTER TABLE agent_change_proposals DROP COLUMN outcome;
ALTER TABLE agent_change_proposals ADD COLUMN outcome TEXT NOT NULL DEFAULT '';
ALTER TABLE agent_change_proposals ADD COLUMN lens TEXT NOT NULL DEFAULT '';
ALTER TABLE agent_change_proposals ADD COLUMN form TEXT NOT NULL DEFAULT '';
ALTER TABLE agent_change_proposals ADD COLUMN suggested_followups_json TEXT NOT NULL DEFAULT '[]';
ALTER TABLE agent_change_proposals ADD COLUMN warnings_json TEXT NOT NULL DEFAULT '[]';
