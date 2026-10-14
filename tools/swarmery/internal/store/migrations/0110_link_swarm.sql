-- 0110: link_messages + link_claims — swarm messaging and the file-claims
-- registry (mods-swarmery-link phase 6, docs/mods-link-protocol.md "Swarm").
--
-- Sessions running the swarmery link mod report their $.session.send /
-- session.receive traffic and, per turn, the files they started editing. Both
-- arrive through POST /api/link/events (internal/link, swarm.go) and are kept
-- here in their own tables; the generic link_events row is written too, as for
-- every link kind (it is the dedup ledger).
--
-- link_messages: one row per message seen.
--   kind          send      — the SENDER's mod reported a $.session.send
--                 receive   — the RECEIVER's mod reported a session.receive
--                 dashboard — the dashboard relayed a message through a
--                             sender's mod (link_commands kind 'message')
--   from_session  sender's Claude session id (sessions.session_uuid); NULL on a
--                 receive the daemon could not correlate with a send
--   to_session    recipient's session uuid when the address is one of our
--                 sessions (always set on a receive: the receiver)
--   to_address    the recipient as the engine spelled it (uuid, agent id or
--                 name); NULL on a receive
--   body          the text, at most 4096 characters; truncated = 1 when the
--                 mod or the daemon cut it
--   origin        send: model | plugin; receive: the engine's origin kind
--                 (peer | coordinator | bridge | …); dashboard: 'dashboard'
--   at            fixed-width UTC, ms (orders lexically like time)
--   delivered     NULL unknown, 1 delivered, 0 not delivered (+ reason)
--   command_id    link_commands.id of the dashboard 'message' command
--   matched_id    the row on the other side of a correlated send/receive pair
--                 (a send's receive, a receive's send); NULL until correlated
--
-- link_claims: which session is editing which file right now. A claim is
-- released at turn end; a re-claim of a released path clears released_at and
-- refreshes claimed_at. Two or more sessions holding a live claim on one path
-- is a conflict — advisory, nothing is blocked.
--
-- WHY 0110: 0108–0111 are reserved for phases 4–7 of the same plan; 0109 and
-- 0111 belong to sibling phases. migrate.go applies files in sorted filename
-- order, so the gap until they land is harmless (see 0112's note).
--
-- NO FOREIGN KEYS, for the reason 0107 spells out (an FK child of sessions
-- wedged the first retention prune, 0073). Retention is internal/prune's job:
-- link_messages by at on the link_events horizon, link_claims 24 h after their
-- release (or 24 h after a claim that was never released — a crashed session).
CREATE TABLE IF NOT EXISTS link_messages (
  id           INTEGER PRIMARY KEY AUTOINCREMENT,
  kind         TEXT    NOT NULL,   -- send | receive | dashboard
  from_session TEXT,               -- uuid, NULL when unknown
  to_session   TEXT,               -- uuid when the address is one of our sessions
  to_address   TEXT,               -- as the engine spelled it
  body         TEXT    NOT NULL,   -- ≤ 4096 characters
  truncated    INTEGER NOT NULL DEFAULT 0,
  origin       TEXT,
  at           TEXT    NOT NULL,   -- fixed-width UTC, ms
  delivered    INTEGER,            -- NULL unknown, 1, 0
  reason       TEXT,               -- why not delivered
  command_id   INTEGER,            -- link_commands.id (dashboard relay)
  matched_id   INTEGER             -- link_messages.id of the correlated pair
);

CREATE INDEX IF NOT EXISTS idx_link_messages_at
  ON link_messages(at DESC);
CREATE INDEX IF NOT EXISTS idx_link_messages_from
  ON link_messages(from_session, at);
CREATE INDEX IF NOT EXISTS idx_link_messages_to
  ON link_messages(to_session, at);

CREATE TABLE IF NOT EXISTS link_claims (
  session_id  TEXT NOT NULL,       -- Claude session id (sessions.session_uuid)
  path        TEXT NOT NULL,       -- absolute, cleaned
  claimed_at  TEXT NOT NULL,       -- fixed-width UTC, ms
  released_at TEXT,                -- NULL while live
  PRIMARY KEY (session_id, path)
);

-- "who else holds this path" — Conflicts.
CREATE INDEX IF NOT EXISTS idx_link_claims_path
  ON link_claims(path);
