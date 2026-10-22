-- 0120: the stalled-phase detector (verify-not-trust phase 11, D15/D34/D35/D41).
--
-- A phase that says it is running while nobody works on it is DERIVED on the
-- daemon's 60 s tick (internal/stall.Detect) from epic_phases, sessions, turns,
-- permission_requests, phase_reviews and verification_runs. Nothing here is a
-- second status: the run's own state stays where it is, and the detector only
-- notifies (no restart, no kill).
--
-- stall_notices is the dedupe ledger of those derivations: one row per
-- (phase, reason) for as long as the condition holds. The phase_stalled webhook
-- fires on INSERT only, the row is DELETEd when the condition clears, so a later
-- recurrence notifies again. The context columns (stage, session_uuid,
-- request_id, since) are refreshed on every tick and are what the epic payload's
-- `stalled` field and the Needs-you `stalled_phase` item read — the ledger is the
-- one place the tick's derivation is visible, so the badge, the queue and the
-- webhook can never disagree.
--
-- No foreign key, like run_events and phase_actuals: wsingest may replace an
-- epic_phases row (a renamed doc mints a new id), and the next tick clears a row
-- whose phase no longer derives a stall.
--
-- Rollback:
--   DROP TABLE IF EXISTS stall_notices;
--   ALTER TABLE epic_phases DROP COLUMN review_started_at;
--   ALTER TABLE epic_phases DROP COLUMN review_session_uuid;
CREATE TABLE IF NOT EXISTS stall_notices (
  phase_id     INTEGER NOT NULL,             -- epic_phases.id
  reason       TEXT    NOT NULL,             -- no-turns | ended | permission-pending
  stage        TEXT    NOT NULL,             -- executor | reviewer | verifier
  session_uuid TEXT    NOT NULL DEFAULT '',  -- the stage's session ('' while not yet known)
  request_id   INTEGER,                      -- permission_requests.id (permission-pending only)
  since        TEXT    NOT NULL,             -- when the stalled condition began (RFC3339)
  first_seen   TEXT    NOT NULL,             -- the tick that first derived it
  notified_at  TEXT,                         -- when phase_stalled was emitted; NULL without a notifier
  PRIMARY KEY (phase_id, reason)
);

-- The reviewer stage was invisible while it ran: phase_reviews is written only
-- AFTER the reviewer's `claude -p` exits (internal/phaserun/review.go). The
-- detector needs the reviewer's session while it is live, so the review stage
-- parks the uuid it minted here at spawn, like run_session_uuid. DAEMON-owned:
-- these two must never appear in wsingest.PhaseUpsertSQL's DO UPDATE SET list.
-- A review is in flight while no phase_reviews row carries this session_uuid.
ALTER TABLE epic_phases ADD COLUMN review_session_uuid TEXT;
ALTER TABLE epic_phases ADD COLUMN review_started_at TEXT;
