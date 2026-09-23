-- 0082: phase_surprise — how far a phase run landed from what was predicted.
--
-- Opus 5.5 / learning-loop phase 13, the comparison half of the predictive
-- loop: phase_forecasts (0079/0080) holds what the planner and the executor
-- EXPECTED, phase_actuals (0081) what the run measurably DID, and this table the
-- deterministic difference — a surprise vector (unexpected_areas, missed_areas,
-- size_miss, duration_miss, outcome_miss, test_surprise, overconfidence), the
-- weighted headline index 0..1, and the component that contributed most.
-- internal/surprise writes it; no LLM is involved.
--
-- ONE ROW PER PHASE RUN, keyed like phase_actuals on the run's session uuid, so
-- the settled re-measure and a backfill recompute REPLACE the row instead of
-- appending one. A run with no forecast to score against, or no actuals, has NO
-- ROW: "not scored" must never read as "scored zero" (a phase that went exactly
-- as predicted). The writer deletes a stale row when a recompute finds the run
-- no longer scorable.
--
-- ADVISORY, like the forecast and the actuals it compares. Nothing gates on these
-- rows: no merge, no run, no completion state consults them. They route
-- attention (a Plans chip, an optional notification, an opt-in verification) and
-- feed the retro digest — nothing more.
--
-- notified_at / auto_verify_at make the two side effects fire at most once per
-- run: the settled pass recomputes the score, and an upsert that re-sent the
-- notification every time would teach the operator to ignore it. They survive
-- the upsert (the writer never overwrites them).
--
-- WHY 0082. migrate.go applies unapplied files in FILENAME order, so a new
-- migration takes the next number above the highest existing one (0081,
-- phase_actuals) — see 0074's note.
--
-- NO FOREIGN KEY on phase_id, for the reasons 0077/0079/0081 give: epic_phases
-- rows are deleted when a phase leaves its plan, and the daemon runs with
-- PRAGMA foreign_keys=ON, where an unindexed FK child column wedged the first
-- retention prune for 11.5 hours (0073). wsingest's phase prune sweeps orphans
-- here beside phase_forecasts and phase_actuals.

CREATE TABLE IF NOT EXISTS phase_surprise (
  id                INTEGER PRIMARY KEY,
  phase_id          INTEGER NOT NULL,             -- epic_phases.id (no FK, see above)
  session_uuid      TEXT    NOT NULL,             -- the run's identity (phase_actuals.session_uuid)
  -- Which forecast was scored: the first non-post-hoc POSTERIOR, else the first
  -- PRIOR. forecast_post_hoc copies that forecast's flag so calibration (phase 16)
  -- can exclude a score built on a prediction that cannot have been one.
  forecast_kind     TEXT    NOT NULL DEFAULT '',
  forecast_post_hoc INTEGER NOT NULL DEFAULT 0,
  forecast_doc_hash TEXT    NOT NULL DEFAULT '',  -- phase_forecasts.doc_hash of the scored block
  surprise_index    REAL    NOT NULL,             -- weighted sum of the components, clipped to 0..1
  top_component     TEXT    NOT NULL DEFAULT '',  -- largest weighted contribution; '' when the index is 0
  components_json   TEXT    NOT NULL DEFAULT '{}',-- {component: 0..1 | null} — null = not measurable
  weights_json      TEXT    NOT NULL DEFAULT '{}',-- the weights the index was computed with
  detail_json       TEXT    NOT NULL DEFAULT '{}',-- areas diff, bands, outcomes, confidence (the UI's source)
  -- Step 13.2: how much reading the code changed the expectation — the prior
  -- compared with the posterior on the same scale. NULL unless the doc carries
  -- both a prior and a non-post-hoc posterior.
  revision_index    REAL,
  revision_json     TEXT,
  summary           TEXT    NOT NULL DEFAULT '',  -- one operator sentence (notification body, verify focus hint)
  actuals_source    TEXT    NOT NULL DEFAULT '',  -- phase_actuals.source the score was computed from
  notified_at       TEXT,                         -- RFC3339; set once, when the notify threshold fired
  auto_verify_at    TEXT,                         -- RFC3339; set once, when an auto-verification was requested
  computed_at       TEXT    NOT NULL,             -- RFC3339
  UNIQUE (session_uuid)
);

-- "this phase's scored runs" — the Plans read path and the orphan sweep.
CREATE INDEX IF NOT EXISTS idx_phase_surprise_phase
  ON phase_surprise(phase_id, id);

-- "the top surprises of a retro window" — the retro digest's range scan.
CREATE INDEX IF NOT EXISTS idx_phase_surprise_computed
  ON phase_surprise(computed_at);
