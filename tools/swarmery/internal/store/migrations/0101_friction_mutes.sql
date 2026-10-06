-- inbox triage agent (phase 7): mutes on Health's recurring error groups. A
-- muted group stays listed on the Friction tab with state 'muted' until
-- muted_until passes; an expired row is simply inactive (never deleted here).
-- key is internal/api normalizeErrKey output; verdict_id names the triage
-- verdict that muted the group, NULL when the operator muted it by hand.
CREATE TABLE friction_mutes (
  key         TEXT PRIMARY KEY,
  reason      TEXT NOT NULL,
  verdict_id  INTEGER,
  muted_at    TEXT NOT NULL,
  muted_until TEXT NOT NULL
);
