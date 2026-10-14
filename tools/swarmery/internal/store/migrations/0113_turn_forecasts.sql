-- 0113: turn_forecasts — per-turn prediction vs outcome from the swarmery link
-- mod (mods-swarmery-link phase 10, the turn-forecast loop).
--
-- On a main-loop turn.start the mod asks a cheap model what the turn will do
-- (files, tests pass, steps, confidence) and sends it as a `turn.forecast` link
-- event; on turn.complete it sends what its own tool.call journal saw as a
-- `turn.actual`. Both arrive through POST /api/link/events (internal/link) and
-- land here as two HALVES of one row keyed (session_id, turn_seq).
-- internal/turnforecast scores the row — a deterministic surprise vector plus
-- one index 0..1, the same design as phase_surprise (0082) — the moment both
-- halves are present. No LLM is involved in the scoring.
--
-- HALF ROWS ARE LEGAL. A forecast whose turn never completes (or an actual
-- whose forecast was skipped) keeps surprise/vector NULL forever: "not scored"
-- must never read as "scored zero" (a turn that went exactly as predicted).
-- Re-sending a half replaces it and rescores.
--
-- PRIVACY. forecast/actual hold paths and numbers only — never prompt text, tool
-- input or model reply text (docs/mods-link-protocol.md, Forecast).
--
-- started_at is the `at` of the turn.forecast, else of the turn.actual (fixed-
-- width ms UTC, the link's tsFormat) — the retention and window key.
--
-- ADVISORY. Nothing gates on these rows; Insights renders a calibration curve.
--
-- WHY 0113: 0108–0111 are reserved for phases 4–7 of the same plan and 0112 is
-- the mods table; migrate.go applies files in sorted filename order, so a gap is
-- harmless (see 0112's note).
--
-- NO FOREIGN KEY on session_id, for the reason 0107 spells out: the daemon runs
-- with PRAGMA foreign_keys=ON, sessions rows are re-pointed and pruned by other
-- writers, and an FK child column is what wedged the first retention prune for
-- 11.5 hours (0073). Retention is the prune pass's job (by started_at, the same
-- horizon as link_events).
CREATE TABLE IF NOT EXISTS turn_forecasts (
  id              INTEGER PRIMARY KEY,
  session_id      INTEGER NOT NULL,  -- sessions.id (no FK, see above)
  turn_seq        INTEGER NOT NULL,  -- the mod's main-loop turn counter (≥ 1, reset on /clear)
  started_at      TEXT    NOT NULL,  -- at of the turn.forecast, else of the turn.actual
  forecast        TEXT,              -- turn.forecast payload, JSON; NULL until it arrives
  actual          TEXT,              -- turn.actual payload, JSON; NULL until it arrives
  surprise        REAL,              -- index 0..1; NULL until both halves are present
  vector          TEXT,              -- JSON {components, weights, top}; NULL until scored
  model           TEXT,              -- the model the mod asked for the forecast
  forecast_tokens INTEGER,           -- what the forecast call cost, when the reply said
  UNIQUE(session_id, turn_seq)
);

-- "the last N days of turns" — Calibration / TopSurprises and the prune pass.
CREATE INDEX IF NOT EXISTS idx_turn_forecasts_started
  ON turn_forecasts(started_at DESC);
