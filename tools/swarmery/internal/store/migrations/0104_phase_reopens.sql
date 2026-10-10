-- 0104: phase_reopens — a finished phase sent back because a defect slipped past
-- its gates.
--
-- Phase-run outcomes plan, phase 1 (decision D1): the first real "a defect got
-- through" signal. The Reopen action on a Done phase writes ONE row here AND
-- unticks the named criteria in the phase doc — the ledger is what the baseline
-- report counts (by caught_by), the untick is what puts the phase back to work.
--
-- caught_by records WHICH gate should have caught it and did, or none did:
--   verifier | review | operator | none
-- criteria_json is the JSON array of the criterion labels that were unticked.
--
-- workspace_task_id + doc_path are kept beside phase_id because epic_phases rows
-- are deleted and re-inserted on a plan rescan (a renamed doc is a delete +
-- insert): the pair is what still finds the phase after its id changed.
--
-- NO FOREIGN KEY on phase_id, for the reasons 0081 gives: wsingest re-inserts
-- epic_phases, the daemon runs with PRAGMA foreign_keys=ON, and a reopen is a
-- historical fact that must outlive the row it was about.

CREATE TABLE IF NOT EXISTS phase_reopens (
  id            INTEGER PRIMARY KEY,
  phase_id      INTEGER NOT NULL,
  workspace_task_id TEXT NOT NULL,     -- epics.workspace_task_id: survives a phase re-insert
  doc_path      TEXT NOT NULL,
  reason        TEXT NOT NULL,
  fix_url       TEXT NOT NULL DEFAULT '',
  caught_by     TEXT NOT NULL CHECK (caught_by IN ('verifier','review','operator','none')),
  criteria_json TEXT NOT NULL DEFAULT '[]',  -- the untick'd criterion labels
  created_at    TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_phase_reopens_phase ON phase_reopens(phase_id, id);
CREATE INDEX IF NOT EXISTS idx_phase_reopens_created ON phase_reopens(created_at);
