-- 0019: advisor recommendations (retro improvement loop, phase 3).
--
-- internal/advisor is a deterministic rule engine (R1..R6, no LLM) that folds
-- the aggregates already in SQLite into evidenced improvement recommendations
-- with the lifecycle proposed → accepted|dismissed → adopted → verified:
--   * proposed   — a rule fired; Run() keeps evidence/detail fresh in place
--   * accepted   — user intent (PATCH); baseline holds the metric snapshot
--                  (+ accepted_at) that verification compares against
--   * dismissed  — suppressed from re-proposal for 30 days
--   * adopted    — auto-detected, per target_kind:
--                    agent (R2/R4)  — the target agent's registry version
--                                     changed after acceptance
--                                     (agents.current_version_id → agent_versions)
--                    tool (R1)      — an enabled approval_rules row covering
--                                     the tool was created after acceptance
--                    process (R5)   — the referenced retro_improvements row's
--                                     status flipped to done/closed/виконано
--                    error_group (R3) / config (R6) — NO detectable adoption
--                                     signal; these never reach adopted
--   * verified   — terminal (a re-fire inserts a fresh row with a numeric
--                  dedup_key suffix), reached per target_kind:
--                    agent/tool     — metric ≥ 20% better than the baseline
--                                     ≥ 7 days after adoption
--                    process        — improvement still done ≥ 7 days after
--                                     adoption (no metric math)
--                    error_group/config — metric ≥ 20% better ≥ 7 days after
--                                     ACCEPTANCE (verifies straight from
--                                     accepted, skipping adopted)
--                  a post window under the metric's activity floor never
--                  verifies (absence of data is not improvement); count
--                  metrics are per-day rates so unequal windows compare fairly

CREATE TABLE recommendations (
  id          INTEGER PRIMARY KEY,
  rule        TEXT NOT NULL,      -- 'R1'..'R6'
  target_kind TEXT NOT NULL CHECK (target_kind IN ('tool','agent','error_group','process','config')),
  target      TEXT NOT NULL,
  title       TEXT NOT NULL,
  detail      TEXT NOT NULL,      -- human-readable rationale with numbers baked in
  evidence    TEXT NOT NULL,      -- JSON: {window:{from,to}, counts, session_ids[], source_rows[]}
  status      TEXT NOT NULL DEFAULT 'proposed'
              CHECK (status IN ('proposed','accepted','dismissed','adopted','verified')),
  dedup_key   TEXT NOT NULL UNIQUE,
  baseline    TEXT,               -- JSON metric snapshot, written when status -> accepted
  created_at  TEXT NOT NULL,
  updated_at  TEXT NOT NULL
);
CREATE INDEX idx_recommendations_status ON recommendations(status);
