package wsingest

import (
	"database/sql"
	"testing"
)

// Renaming a phase doc replaces its row — epic_phases identity is doc_path. These tests
// pin the hand-over that keeps a run alive across that replacement, and the guard that
// refuses to delete a running row when there is nothing to hand it to.

const carryTaskID int64 = 10

// carryFixture seeds a project + workspace task and returns an open DB.
func carryFixture(t *testing.T) *sql.DB {
	t.Helper()
	db := testDB(t)
	mustExec(t, db, `INSERT INTO projects (id, path, slug, first_seen)
		VALUES (1, '/repo/p', 'p', '2026-01-01T00:00:00Z')`)
	mustExec(t, db, `INSERT INTO tasks (id, project_id, title, prompt, status, created_at, source, external_id)
		VALUES (?, 1, 'Epic', 'goal', 'running', '2026-07-29T00:00:00Z', 'workspace', 'ws-epic')`, carryTaskID)
	return db
}

// seedPhase inserts a phase row, optionally already carrying run state.
func seedPhase(t *testing.T, db *sql.DB, seq int, name, docPath, runState string, sessionUUID, runBranch string) {
	t.Helper()
	var sess, branch any
	if sessionUUID != "" {
		sess = sessionUUID
	}
	if runBranch != "" {
		branch = runBranch
	}
	mustExec(t, db, `INSERT INTO epic_phases
		(workspace_task_id, seq, name, doc_path, depends_on, checkboxes_total, checkboxes_done,
		 run_state, run_session_uuid, run_started_at, run_branch, run_checkboxes_before)
		VALUES (?, ?, ?, ?, '[]', 12, 5, ?, ?, '2026-07-29T18:00:00Z', ?, 4)`,
		carryTaskID, seq, name, docPath, runState, sess, branch)
}

// applyPhases runs applyEpics in its own transaction, the way scanEpics calls it.
func applyPhases(t *testing.T, db *sql.DB, phases []epicPhase) {
	t.Helper()
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if err := applyEpics(tx, carryTaskID, phases, true /* readmePresent */); err != nil {
		tx.Rollback()
		t.Fatalf("applyEpics: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
}

func phase(seq int, name, docPath string) epicPhase {
	return epicPhase{seq: seq, name: name, docPath: docPath, checkboxesTotal: 12, checkboxesDone: 5}
}

// rowState reads the daemon-owned columns of the row at docPath.
func rowState(t *testing.T, db *sql.DB, docPath string) (state string, sess, branch sql.NullString, before sql.NullInt64) {
	t.Helper()
	err := db.QueryRow(`SELECT run_state, run_session_uuid, run_branch, run_checkboxes_before
		 FROM epic_phases WHERE workspace_task_id=? AND doc_path=?`, carryTaskID, docPath).
		Scan(&state, &sess, &branch, &before)
	if err != nil {
		t.Fatalf("row %s: %v", docPath, err)
	}
	return
}

// THE regression: a plan regeneration renames every phase doc while phase 2 is running.
// Before the carry-over this deleted the running row outright — no Cancel, no session
// link, and the branch its commits were on became unreachable.
func TestApplyEpicsCarriesRunStateAcrossFullRename(t *testing.T) {
	db := carryFixture(t)
	seedPhase(t, db, 1, "Phase 1", "/plan/phase-1-old.md", "done", "uuid-1", "swarm/phase-1")
	seedPhase(t, db, 2, "Phase 2", "/plan/phase-2-old.md", "running", "uuid-2", "swarm/phase-1280")
	seedPhase(t, db, 3, "Phase 3", "/plan/phase-3-old.md", "idle", "", "")

	applyPhases(t, db, []epicPhase{
		phase(1, "Phase 1", "/plan/phase-1-new.md"),
		phase(2, "Phase 2", "/plan/phase-2-new.md"),
		phase(3, "Phase 3", "/plan/phase-3-new.md"),
	})

	if n := count(t, db, `SELECT COUNT(*) FROM epic_phases WHERE workspace_task_id=?`, carryTaskID); n != 3 {
		t.Fatalf("rows = %d, want 3 (old rows pruned, new rows kept)", n)
	}
	state, sess, branch, before := rowState(t, db, "/plan/phase-2-new.md")
	if state != "running" {
		t.Errorf("run_state = %q, want running", state)
	}
	if sess.String != "uuid-2" {
		t.Errorf("run_session_uuid = %q, want uuid-2 — without it there is no Cancel and no session link", sess.String)
	}
	if branch.String != "swarm/phase-1280" {
		t.Errorf("run_branch = %q, want swarm/phase-1280 — the branch the run committed to", branch.String)
	}
	if !before.Valid || before.Int64 != 4 {
		t.Errorf("run_checkboxes_before = %v, want 4 — the run's measurement interval must survive", before)
	}
	// A terminal run carries too: its outcome chip is derived from these columns.
	if state, _, branch, _ := rowState(t, db, "/plan/phase-1-new.md"); state != "done" || branch.String != "swarm/phase-1" {
		t.Errorf("phase 1 after rename: state=%q branch=%q, want done / swarm/phase-1", state, branch.String)
	}
	// A phase that never ran has nothing to carry and must stay clean.
	if state, sess, _, _ := rowState(t, db, "/plan/phase-3-new.md"); state != "idle" || sess.Valid {
		t.Errorf("phase 3 after rename: state=%q sess=%v, want idle / NULL", state, sess)
	}
}

// A phase genuinely dropped from the plan README is still pruned — the carry-over must
// not turn the prune into a no-op.
func TestApplyEpicsPrunesRemovedPhase(t *testing.T) {
	db := carryFixture(t)
	seedPhase(t, db, 1, "Phase 1", "/plan/phase-1.md", "done", "uuid-1", "swarm/phase-1")
	seedPhase(t, db, 2, "Phase 2", "/plan/phase-2.md", "done", "uuid-2", "swarm/phase-2")

	applyPhases(t, db, []epicPhase{phase(1, "Phase 1", "/plan/phase-1.md")})

	if n := count(t, db, `SELECT COUNT(*) FROM epic_phases WHERE workspace_task_id=?`, carryTaskID); n != 1 {
		t.Fatalf("rows = %d, want 1 — the removed phase must be pruned", n)
	}
	if n := count(t, db, `SELECT COUNT(*) FROM epic_phases WHERE workspace_task_id=? AND doc_path='/plan/phase-2.md'`,
		carryTaskID); n != 0 {
		t.Error("phase 2 survived the prune")
	}
}

// A phase ADDED to the plan is not a rename: nothing vanished, so nothing is carried and
// the new row starts clean.
func TestApplyEpicsAddedPhaseCarriesNothing(t *testing.T) {
	db := carryFixture(t)
	seedPhase(t, db, 1, "Phase 1", "/plan/phase-1.md", "running", "uuid-1", "swarm/phase-1")

	applyPhases(t, db, []epicPhase{
		phase(1, "Phase 1", "/plan/phase-1.md"),
		phase(2, "Phase 2", "/plan/phase-2.md"),
	})

	if state, _, _, _ := rowState(t, db, "/plan/phase-1.md"); state != "running" {
		t.Errorf("existing phase run_state = %q, want running (untouched)", state)
	}
	if state, sess, branch, _ := rowState(t, db, "/plan/phase-2.md"); state != "idle" || sess.Valid || branch.Valid {
		t.Errorf("added phase = %q/%v/%v, want idle/NULL/NULL", state, sess, branch)
	}
}

// Two stateful rows collapsed onto one seq is not a rename anyone can resolve. Carrying
// either one would attribute a run to a phase that may not have performed it, so the
// carry-over declines — the same reason seq is not the identity key.
func TestApplyEpicsAmbiguousSeqCarriesNothing(t *testing.T) {
	db := carryFixture(t)
	seedPhase(t, db, 5, "Phase 5 — A", "/plan/phase-5-a.md", "done", "uuid-a", "swarm/phase-5a")
	seedPhase(t, db, 5, "Phase 5 — B", "/plan/phase-5-b.md", "done", "uuid-b", "swarm/phase-5b")

	applyPhases(t, db, []epicPhase{phase(5, "Phase 5", "/plan/phase-5-merged.md")})

	state, sess, branch, _ := rowState(t, db, "/plan/phase-5-merged.md")
	if state != "idle" || sess.Valid || branch.Valid {
		t.Errorf("merged row = %q/%v/%v, want idle/NULL/NULL — an ambiguous match must carry nothing",
			state, sess, branch)
	}
}

// A running phase whose doc vanished with no replacement: the row is the only handle on
// that process, so it is kept rather than deleted.
func TestApplyEpicsKeepsRunningOrphan(t *testing.T) {
	db := carryFixture(t)
	seedPhase(t, db, 1, "Phase 1", "/plan/phase-1.md", "done", "uuid-1", "swarm/phase-1")
	seedPhase(t, db, 2, "Phase 2", "/plan/phase-2.md", "running", "uuid-2", "swarm/phase-2")

	applyPhases(t, db, []epicPhase{phase(1, "Phase 1", "/plan/phase-1.md")})

	if n := count(t, db, `SELECT COUNT(*) FROM epic_phases WHERE workspace_task_id=? AND doc_path='/plan/phase-2.md'`,
		carryTaskID); n != 1 {
		t.Fatal("the running orphan was deleted — its process is now unreachable")
	}
	state, sess, _, _ := rowState(t, db, "/plan/phase-2.md")
	if state != "running" || sess.String != "uuid-2" {
		t.Errorf("orphan = %q/%q, want running/uuid-2", state, sess.String)
	}
	// A terminal orphan carries no live process and IS pruned.
	seedPhase(t, db, 3, "Phase 3", "/plan/phase-3.md", "done", "uuid-3", "swarm/phase-3")
	applyPhases(t, db, []epicPhase{phase(1, "Phase 1", "/plan/phase-1.md")})
	if n := count(t, db, `SELECT COUNT(*) FROM epic_phases WHERE workspace_task_id=? AND doc_path='/plan/phase-3.md'`,
		carryTaskID); n != 0 {
		t.Error("a terminal orphan must still be pruned")
	}
}

// A carried-over source row is deleted even though it is 'running': its state now lives
// on the replacement, and two rows claiming the same run is worse than none.
func TestApplyEpicsCarriedSourceIsPrunedWhileRunning(t *testing.T) {
	db := carryFixture(t)
	seedPhase(t, db, 1, "Phase 1", "/plan/phase-1-old.md", "running", "uuid-1", "swarm/phase-1280")

	applyPhases(t, db, []epicPhase{phase(1, "Phase 1", "/plan/phase-1-new.md")})

	if n := count(t, db, `SELECT COUNT(*) FROM epic_phases WHERE workspace_task_id=? AND run_state='running'`,
		carryTaskID); n != 1 {
		t.Fatalf("running rows = %d, want exactly 1", n)
	}
	if n := count(t, db, `SELECT COUNT(*) FROM epic_phases WHERE workspace_task_id=? AND doc_path='/plan/phase-1-old.md'`,
		carryTaskID); n != 0 {
		t.Error("the drained source row survived — two rows now claim the same run")
	}
}

// A rescan that re-mints a phase row must carry the blocked fingerprint (0092) with
// the rest of the run's state. run_state='blocked' IS carried, so a dropped
// fingerprint leaves a row that says "blocked" with nothing to compare against:
// phaserun's re-run guard reads a NULL fingerprint as "never blocked" and admits
// the very re-run it exists to refuse — after nothing more than an archive or a
// doc rename.
func TestRescanCarriesFingerprint(t *testing.T) {
	db := carryFixture(t)
	const (
		oldPath = "/ws/p/workspace/working/2026/09/27/epic/plan/phase-2-contracts.md"
		newPath = "/ws/p/workspace/archive/2026/09/27/epic/plan/phase-2-contracts.md"
		fp      = "9f2c1e0b7a6d5c4b3a291807f6e5d4c3b2a1908f7e6d5c4b3a2918070f1e2d3c"
		reason  = "contracts exist only on the unmerged swarm/phase-26498"
	)
	seedPhase(t, db, 2, "Phase 2", oldPath, "blocked", "uuid-2", "swarm/phase-26499")
	mustExec(t, db, `UPDATE epic_phases
		   SET run_blocked_fingerprint=?, run_error=?, run_ended_at='2026-09-27T09:00:00Z',
		       run_start_point='abc1234'
		 WHERE doc_path=?`, fp, reason, oldPath)
	// A phase that never blocked has no fingerprint, and must not gain one.
	seedPhase(t, db, 1, "Phase 1", "/ws/p/workspace/working/2026/09/27/epic/plan/phase-1-schema.md", "done", "uuid-1", "swarm/phase-26498")

	applyPhases(t, db, []epicPhase{
		phase(1, "Phase 1", "/ws/p/workspace/archive/2026/09/27/epic/plan/phase-1-schema.md"),
		phase(2, "Phase 2", newPath),
	})

	var (
		state                           string
		got, runErr, endedAt, startedOn sql.NullString
	)
	if err := db.QueryRow(`SELECT run_state, run_blocked_fingerprint, run_error, run_ended_at, run_start_point
		  FROM epic_phases WHERE workspace_task_id=? AND doc_path=?`, carryTaskID, newPath).
		Scan(&state, &got, &runErr, &endedAt, &startedOn); err != nil {
		t.Fatalf("re-minted row: %v", err)
	}
	if state != "blocked" {
		t.Fatalf("run_state = %q, want blocked (test premise: the run state is carried)", state)
	}
	if got.String != fp {
		t.Errorf("run_blocked_fingerprint = %v, want %s — without it the guard forgets this block", got, fp)
	}
	// The three columns the guard reads BESIDE the fingerprint travel with it.
	if runErr.String != reason || endedAt.String != "2026-09-27T09:00:00Z" || startedOn.String != "abc1234" {
		t.Errorf("carried run_error=%q run_ended_at=%q run_start_point=%q, want all three intact",
			runErr.String, endedAt.String, startedOn.String)
	}
	// The source rows are gone — exactly one row holds the fingerprint.
	if n := count(t, db, `SELECT COUNT(*) FROM epic_phases WHERE run_blocked_fingerprint=?`, fp); n != 1 {
		t.Errorf("rows holding the fingerprint = %d, want 1", n)
	}
	if n := count(t, db, `SELECT COUNT(*) FROM epic_phases WHERE workspace_task_id=? AND run_state='done'
		AND run_blocked_fingerprint IS NOT NULL`, carryTaskID); n != 0 {
		t.Errorf("a phase that never blocked gained a fingerprint across the rename (%d row(s))", n)
	}

	// A rescan that renames nothing leaves the fingerprint where it is: the upsert
	// of an existing row must not touch a daemon-owned column.
	applyPhases(t, db, []epicPhase{
		phase(1, "Phase 1", "/ws/p/workspace/archive/2026/09/27/epic/plan/phase-1-schema.md"),
		phase(2, "Phase 2 — renamed title", newPath),
	})
	if err := db.QueryRow(`SELECT run_blocked_fingerprint FROM epic_phases WHERE workspace_task_id=? AND doc_path=?`,
		carryTaskID, newPath).Scan(&got); err != nil {
		t.Fatal(err)
	}
	if got.String != fp {
		t.Errorf("run_blocked_fingerprint after a plain rescan = %v, want it untouched", got)
	}
}

// THE archive regression (task 554): `agent-work.sh archive` moves the task dir from
// workspace/working/… to workspace/archive/…, which changes every phase doc_path while
// the workspace task id stays put. The carry handed the run columns to the new rows but
// dropped run_effort (0086), and the run-derived tables keyed on the OLD phase id
// (phase_actuals, phase_surprise, and the lesson tables born from them) were left
// pointing at a deleted row — the first two then removed outright by the orphan sweep.
func TestApplyEpicsCarryKeepsEffortAndActuals(t *testing.T) {
	db := carryFixture(t)
	const (
		oldPath = "/ws/p/workspace/working/2026/09/20/epic/plan/phase-1-build.md"
		newPath = "/ws/p/workspace/archive/2026/09/20/epic/plan/phase-1-build.md"
	)
	seedPhase(t, db, 1, "Phase 1", oldPath, "done", "uuid-1", "swarm/phase-1")
	mustExec(t, db, `UPDATE epic_phases SET run_effort='high' WHERE doc_path=?`, oldPath)
	var oldID int64
	if err := db.QueryRow(`SELECT id FROM epic_phases WHERE doc_path=?`, oldPath).Scan(&oldID); err != nil {
		t.Fatal(err)
	}
	mustExec(t, db, `INSERT INTO phase_actuals (phase_id, session_uuid, computed_at)
		VALUES (?, 'uuid-1', '2026-09-20T10:00:00Z')`, oldID)
	mustExec(t, db, `INSERT INTO phase_surprise (phase_id, session_uuid, surprise_index, computed_at)
		VALUES (?, 'uuid-1', 0.7, '2026-09-20T10:00:00Z')`, oldID)
	mustExec(t, db, `INSERT INTO surprise_lessons (source_phase_run, phase_id, seq, title, guidance, created_at, updated_at)
		VALUES ('uuid-1', ?, 1, 'Lesson', 'Do the thing.', '2026-09-20T10:00:00Z', '2026-09-20T10:00:00Z')`, oldID)
	mustExec(t, db, `INSERT INTO lesson_generations (source_phase_run, phase_id, state, created_at)
		VALUES ('uuid-1', ?, 'done', '2026-09-20T10:00:00Z')`, oldID)
	mustExec(t, db, `INSERT INTO lesson_uses (session_uuid, run_kind, phase_id, task_id, lesson_id, rank, injected_at)
		VALUES ('uuid-1', 'phaserun', ?, ?, 7, 1, '2026-09-20T10:00:00Z')`, oldID, carryTaskID)

	applyPhases(t, db, []epicPhase{phase(1, "Phase 1", newPath)})

	var newID int64
	var effort sql.NullString
	if err := db.QueryRow(`SELECT id, run_effort FROM epic_phases WHERE workspace_task_id=? AND doc_path=?`,
		carryTaskID, newPath).Scan(&newID, &effort); err != nil {
		t.Fatalf("archived row: %v", err)
	}
	if newID == oldID {
		t.Fatalf("new row reused id %d — the fixture no longer exercises a rename", oldID)
	}
	if effort.String != "high" {
		t.Errorf("run_effort = %v, want high — calibration reads the run's effort from this column", effort)
	}
	for _, table := range []string{"phase_actuals", "phase_surprise", "surprise_lessons", "lesson_generations", "lesson_uses"} {
		if n := count(t, db, `SELECT COUNT(*) FROM `+table+` WHERE phase_id=?`, newID); n != 1 {
			t.Errorf("%s rows on the new phase id = %d, want 1", table, n)
		}
		if n := count(t, db, `SELECT COUNT(*) FROM `+table+` WHERE phase_id=?`, oldID); n != 0 {
			t.Errorf("%s rows still on the old phase id = %d, want 0", table, n)
		}
	}
}
