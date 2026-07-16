-- 0013: auto-approve rules (control-plane v2 — notifications & rules).
-- Numbered 0013: 0012 is reserved by the parallel control-plane-v2 branch;
-- migrate.go applies by filename order and tracks versions individually, so
-- the gap is harmless (precedent: the 0006/0007 workspaces/approvals split).
--
-- One row = "auto-approve this tool pattern" for one project (project_id
-- NULL = every project). tool_pattern is either `Tool` (exact tool_name,
-- any input) or `Tool(argGlob)` where `*` matches ANY run of characters in
-- the tool's argument string (Bash → tool_input.command, Read/Write/Edit →
-- file_path, WebFetch → url, Glob/Grep → pattern). Matching is
-- deny-by-default — no wildcard in the tool part, unknown tools/fields never
-- match; full semantics in internal/approvals/rules.go.
--
-- SECURITY: `Bash(git *)` is a PREFIX match on the command string — it also
-- matches `git status && rm -rf /`. Keep rules narrow; every auto-approval
-- keeps its permission_requests row (resolved_via='rule') as the audit trail.
--
-- action is CHECKed to 'approve' only: auto-DENY rules are a deliberate
-- non-goal (a wrong deny silently breaks agents; a wrong approve stays
-- visible in History).
CREATE TABLE approval_rules (
    id           INTEGER PRIMARY KEY,
    project_id   INTEGER REFERENCES projects(id),  -- NULL = all projects
    tool_pattern TEXT NOT NULL,
    action       TEXT NOT NULL DEFAULT 'approve' CHECK (action IN ('approve')),
    enabled      INTEGER NOT NULL DEFAULT 1,
    created_at   TEXT NOT NULL,
    note         TEXT
);
CREATE INDEX idx_approval_rules_lookup ON approval_rules(enabled, project_id);
