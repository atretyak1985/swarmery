-- 0029: permission presets (fusion phase 11 — DESIGN.md §2 item 11).
--
-- A human-readable POLICY layer over the low-level approval_rules (0013). One
-- row per project selects a preset that COMPILES into managed auto-approve
-- rules (source='preset'), leaving hand-written manual rules untouched.
--
-- SECURITY — fail CLOSED (least privilege):
--   * preset DEFAULT 'approval-required' and a project with NO row is treated
--     identically: no managed auto-approve rules ⇒ every tool call queues for a
--     human. An absent / unknown / misconfigured preset can therefore NEVER
--     silently auto-approve — the safe state is the default state.
--   * overrides is a JSON object {category: "allow"|"ask"}; an absent category
--     falls back to the preset's baseline (see internal/approvals/presets.go).
--     "block" is deliberately NOT a value — our hook protocol has no auto-deny
--     (honesty over parity, same rationale as approval_rules.action).
--
-- preset values are CHECKed so a bad write is a hard error, not a fail-open:
--   unrestricted      — auto-approve every category whose effective policy is
--                       'allow' (default allow-set = all categories EXCEPT
--                       git_push; command_exec compiled last as the broadest).
--   approval-required — no managed rules; every request waits for a human.
--   locked-down       — no managed rules AND the dispatcher refuses to admit
--                       this project's Todo tasks (dispatch_error='project
--                       locked down'); pending requests expire = deny normally.
CREATE TABLE project_permission_presets (
    project_id  INTEGER PRIMARY KEY REFERENCES projects(id),
    preset      TEXT NOT NULL DEFAULT 'approval-required'
                CHECK (preset IN ('unrestricted', 'approval-required', 'locked-down')),
    overrides   TEXT NOT NULL DEFAULT '{}',   -- JSON {category: "allow"|"ask"}
    updated_at  TEXT NOT NULL
);

-- source distinguishes hand-written rules ('manual', the default for every
-- existing row and every POST /api/approval-rules) from compiled preset rules
-- ('preset'). Compile ONLY ever deletes+inserts source='preset' rows for its
-- project — manual rules are invisible to it. Backfills existing rows to
-- 'manual' via the column default.
ALTER TABLE approval_rules ADD COLUMN source TEXT NOT NULL DEFAULT 'manual';

-- Compile's replace transaction filters by (project_id, source='preset'); index
-- that access path.
CREATE INDEX idx_approval_rules_source ON approval_rules(project_id, source);
