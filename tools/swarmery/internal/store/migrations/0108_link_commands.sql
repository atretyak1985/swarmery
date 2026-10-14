-- 0108: link_commands — the dashboard → mod command queue.
--
-- mods-swarmery-link phase 4 (control channel, docs/mods-link-protocol.md
-- "Commands"). The SPA enqueues a `stop` or a `prompt` for a session; the
-- swarmery link mod long-polls GET /api/link/commands, executes the command
-- with $.turn.abort / $.prompt.submit and acks it. internal/link.Commands owns
-- every write.
--
-- One row per command. The row is the source of truth, so a daemon restart
-- never strands a command: a delivered-but-unacked row is handed out again on
-- the next poll once RedeliverAfter (30 s) has passed.
--   kind        stop | prompt
--   payload     JSON verbatim: {} for stop, {"text":…,"asUser":…} for prompt
--   state       queued | delivered | done | failed | expired
--   attempts    "turn in progress" bounces of a prompt (max 3, then final failed)
--   created_at  fixed-width UTC, ms (orders lexically like time)
--   delivered_at the latest delivery; NULL while queued
--   acked_at    when the final ack (done / failed) arrived
--   result      done | failed — the mod's ack verbatim
--   message     the ack message (≤ 500 chars), e.g. "turn in progress"
--   wait_turn   1 = a bounced prompt held back until the next turn.complete
--
-- NO FOREIGN KEY on session_id, for the reason 0107 spells out (an FK child of
-- sessions wedged the first retention prune, 0073). internal/prune ages the
-- rows by created_at, like link_events.
CREATE TABLE IF NOT EXISTS link_commands (
  id           INTEGER PRIMARY KEY,
  session_id   INTEGER NOT NULL,   -- sessions.id (no FK, see above)
  kind         TEXT    NOT NULL,
  payload      TEXT    NOT NULL,
  state        TEXT    NOT NULL DEFAULT 'queued',
  attempts     INTEGER NOT NULL DEFAULT 0,
  created_at   TEXT    NOT NULL,
  delivered_at TEXT,
  acked_at     TEXT,
  result       TEXT,
  message      TEXT,
  wait_turn    INTEGER NOT NULL DEFAULT 0
);

CREATE INDEX IF NOT EXISTS idx_link_commands_session_state
  ON link_commands(session_id, state, created_at);
