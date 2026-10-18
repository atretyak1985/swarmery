-- 0114: the delivering channel of a permission request (mods-swarmery-link
-- phase 5, the mod's approvals channel).
--
-- A permission request now reaches the daemon one of two ways:
--   'shim' — the PermissionRequest hook shim, POST /api/hooks/permission-request
--            (docs/hooks-protocol.md, FROZEN — unchanged by this migration);
--   'mod'  — the swarmery link mod's tool.check hook, POST /api/link/permission
--            (docs/mods-link-protocol.md, Permission).
-- Both write the SAME permission_requests rows: same dedup_hash (D6), same
-- risk class, same rules, same dashboard decisions. `channel` says which
-- channel delivered the row; `resolved_via` keeps meaning WHO decided it
-- ('dashboard', 'rule', 'terminal', 'local-only', 'restart') and never takes
-- the value 'mod'.
--
-- tool_use_id is the engine's `toolu_…` id of the call (tool.check's
-- e.tool_use_id). The mod sends it; the shim's stdin has none (NULL). It makes
-- a re-POST of the same call idempotent: (session_id, tool_use_id) finds the
-- row the first POST created.
--
-- Additive: channel is NOT NULL with a constant default, so existing rows read
-- 'shim'. Rollback:
--   DROP INDEX IF EXISTS idx_pr_mod_hash;
--   DROP INDEX IF EXISTS idx_pr_tool_use;
--   ALTER TABLE permission_requests DROP COLUMN tool_use_id;
--   ALTER TABLE permission_requests DROP COLUMN channel;
--
-- WHY 0114: 0109–0111 were first reserved for phases 5–7 of the same plan,
-- but 0112/0113 landed first; numbering after the highest applied version
-- keeps /api/health's MAX(version) a truthful "this schema is current" signal.
ALTER TABLE permission_requests ADD COLUMN channel TEXT NOT NULL DEFAULT 'shim';
ALTER TABLE permission_requests ADD COLUMN tool_use_id TEXT;

-- Idempotent mod re-POST: the row of (session, tool_use_id).
CREATE INDEX IF NOT EXISTS idx_pr_tool_use
  ON permission_requests(session_id, tool_use_id) WHERE tool_use_id IS NOT NULL;

-- The shim-after-mod window: "a mod row with this dedup_hash resolved
-- expired/resolved_elsewhere in the last 5 s" is asked on every shim request.
-- Partial on channel = 'mod' so it stays as small as the mod's own rows.
CREATE INDEX IF NOT EXISTS idx_pr_mod_hash
  ON permission_requests(dedup_hash, resolved_at) WHERE channel = 'mod';
