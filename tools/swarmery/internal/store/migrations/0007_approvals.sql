-- 0007: approvals (phase 2, frozen at gate 2.2 — docs/hooks-protocol.md).
-- Numbered 0007: 0006 is reserved by the parallel workspaces branch; the runner
-- (migrate.go) applies by filename order and tracks versions individually, so
-- the gap is harmless.
-- Additive only — permission_requests itself ships in 0001 (design §2 verbatim);
-- the permission_request/permission_resolved event types already exist in the
-- events enum. New columns are NULL for any pre-phase-2 rows.
--
-- dedup_hash: hex(SHA-256(session_id + "\n" + tool_name + "\n" +
-- canonical_json(tool_input))) over the raw hook stdin — identical concurrent
-- requests (parallel subagents share the parent session_id, spike E11) attach
-- to one pending row; both callers receive the one decision.
ALTER TABLE permission_requests ADD COLUMN dedup_hash TEXT;
-- expires_at: requested_at + approval_timeout (default 120 s); the expiry
-- sweeper resolves overdue pending rows as 'expired'.
ALTER TABLE permission_requests ADD COLUMN expires_at TEXT;
-- reason: human-entered approve/deny reason; delivered to Claude verbatim on
-- deny via hookSpecificOutput.decision.message (spike E3).
ALTER TABLE permission_requests ADD COLUMN reason TEXT;

-- Dedup lookups only ever race against pending rows → partial index.
CREATE INDEX idx_pr_dedup ON permission_requests(dedup_hash) WHERE status = 'pending';
