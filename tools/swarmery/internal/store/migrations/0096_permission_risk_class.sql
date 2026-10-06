-- 0096: a risk class on permission requests.
--
-- '' is an ordinary request. 'prod-deploy' marks a tool call that matched the
-- production-deploy pattern list (internal/approvals/prodguard.go): the daemon
-- records it, never auto-approves it, hands it straight back to the session's
-- native permission dialog, and refuses every remote approve/answer on it.
--
-- Additive, NOT NULL with a constant default, so existing rows read ''.
-- Rollback is `ALTER TABLE permission_requests DROP COLUMN risk_class;`.
ALTER TABLE permission_requests ADD COLUMN risk_class TEXT NOT NULL DEFAULT '';
