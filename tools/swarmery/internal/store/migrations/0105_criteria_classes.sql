-- 0105: criteria classes — how many of a phase's UNTICKED acceptance criteria
-- are not the executor's to close.
--
-- Phase-run outcomes plan, phase 3 (decision D3): a criterion whose label starts
-- with `[LAND]` is closed by landing the work (push / PR / merge — the operator or
-- the land action), one starting with `[MANUAL]` only by a human (production, a
-- console, a hand check). wsingest parses the marker (internal/wsingest/classes.go)
-- and stores the two open counts here, so readers that never open the doc (the
-- Needs-you queue, the report) can tell "the run left work" from "the run left
-- what was never its to do".
--
-- WSINGEST-OWNED, like checkboxes_total/checkboxes_done: both columns are in
-- PhaseUpsertSQL's DO UPDATE SET list and are re-derived from the doc on every
-- scan. checkboxes_total still counts every criterion — phasegate is unchanged,
-- so a phase only reads complete once its [LAND]/[MANUAL] criteria are ticked.

ALTER TABLE epic_phases ADD COLUMN criteria_land_open INTEGER NOT NULL DEFAULT 0;
ALTER TABLE epic_phases ADD COLUMN criteria_manual_open INTEGER NOT NULL DEFAULT 0;
