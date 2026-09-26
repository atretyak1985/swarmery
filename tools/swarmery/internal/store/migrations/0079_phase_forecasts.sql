-- 0079: phase_forecasts — the `## Forecast` block a phase doc may carry.
--
-- Opus 5.5 / learning-loop phase 10, the first half of a predictive loop: the
-- planner writes a PRIOR (what this phase will touch, how big, how long, how it
-- ends), the executor writes a POSTERIOR after the work, and a later phase scores
-- the difference. internal/wsingest.ParseForecasts parses both out of the doc.
--
-- A FORECAST IS DATA, NEVER A FENCE. Nothing gates on these rows: no run is
-- refused for diverging from a forecast, no phase is incomplete for lacking one,
-- and a block the parser cannot read is a LINT (computed in the read path, like
-- the spec-coverage `unknownRefs` beside it) on a plan that still ingests. That is
-- why every column below is nullable or defaulted — a half-written forecast must
-- store as the half it is rather than fail an INSERT inside the scan transaction
-- that is also writing the plan's real phases.
--
-- VERBATIM VALUES. size_band / duration_band / outcome / kind hold exactly what
-- the author wrote, not a normalized enum, and there is deliberately NO CHECK
-- constraint on any of them. The same reasoning epic_phases.doc_model carries
-- (0069): the scan must never fail over a typo, and the operator has to SEE the
-- offending text to fix it — a value folded to NULL at write time is a typo that
-- looks like an absent key.
--
-- WHY 0079. migrate.go applies unapplied files in FILENAME order, so a new
-- migration always takes the next number above the highest existing one (0078,
-- turns.stop_reason). Re-using a lower slot would order differently on a fresh
-- database than on a migrated one — see 0074's note.
--
-- NO FOREIGN KEY on phase_id, for the reason 0077 spells out for run_events: the
-- parent rows are not stable (wsingest deletes and re-inserts epic_phases on a
-- plan rescan, and a renamed doc is a delete + insert), and the daemon runs with
-- PRAGMA foreign_keys=ON, where an unindexed FK child column is what wedged the
-- first scheduled retention prune for 11.5 hours (0073).
--
-- What bounds growth instead is the writer: applyEpics replaces a phase's whole
-- forecast set on every scan of a changed plan, and sweeps rows whose phase no
-- longer exists whenever the phase prune deletes anything. So the table grows with
-- live phases that carry a forecast, not with plan history.

CREATE TABLE IF NOT EXISTS phase_forecasts (
  id            INTEGER PRIMARY KEY,
  phase_id      INTEGER NOT NULL,             -- epic_phases.id (no FK, see above)
  kind          TEXT    NOT NULL DEFAULT '',  -- 'prior' | 'posterior', verbatim; '' when the block declares none
  written_at    TEXT    NOT NULL DEFAULT '',  -- RFC3339 as the author wrote it
  areas_json    TEXT    NOT NULL DEFAULT '[]',-- JSON array of directories / module names
  files_json    TEXT    NOT NULL DEFAULT '[]',-- JSON array of paths or globs (optional in the format)
  size_band     TEXT    NOT NULL DEFAULT '',  -- XS | S | M | L | XL, verbatim
  duration_band TEXT    NOT NULL DEFAULT '',  -- <30m | 30-90m | 90m-4h | >4h, verbatim
  outcome       TEXT    NOT NULL DEFAULT '',  -- done | partial | blocked, verbatim
  risks_json    TEXT    NOT NULL DEFAULT '[]',-- JSON array of free-text risks
  -- NULL, not 0, when the author said nothing or wrote something that is not a
  -- number: "did not say" and "certain this is wrong" are opposite statements.
  confidence    REAL,
  -- 1 for a PRIOR living in a doc whose `## Completion Report` is already filled —
  -- a prediction that cannot have been one. Derived by the scan, never declared,
  -- because the author backfilling a prior is exactly who would not declare it.
  post_hoc      INTEGER NOT NULL DEFAULT 0,
  -- sha256 of the phase doc at the scan that stored this row, so a later scoring
  -- pass can tell whether the forecast it is grading is still the one in the doc.
  doc_hash      TEXT    NOT NULL DEFAULT ''
);

-- The ONLY access path: "give me this phase's forecasts, in document order".
-- id is in the index so the ordering is served from it rather than from a sort,
-- and it is what makes the per-scan replace (DELETE … WHERE phase_id = ?) and the
-- orphan sweep index seeks rather than table scans — the 0073 lesson applied
-- up front rather than after an 11-hour outage.
CREATE INDEX IF NOT EXISTS idx_phase_forecasts_phase
  ON phase_forecasts(phase_id, id);
