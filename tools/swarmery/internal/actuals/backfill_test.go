package actuals

import (
	"bytes"
	"errors"
	"strings"
	"testing"
)

// TestBackfill runs the best-effort backfill over five phase runs, one per
// outcome it has to tell apart: measurable, already recorded, branch gone, still
// running, and never given a start point. Every run lands in exactly one bucket.
func TestBackfill(t *testing.T) {
	repo := newFixtureRepo(t)
	db := openDB(t)
	measurable := seedPhase(t, db, repo.dir, repo.start)
	taskID := int64(1)

	addPhase := func(seq int, uuid, state, branch, start string) int64 {
		return exec1(t, db, `INSERT INTO epic_phases
			(workspace_task_id, seq, name, doc_path, depends_on, checkboxes_total, checkboxes_done,
			 run_state, run_session_uuid, run_started_at, run_ended_at, run_branch, run_start_point)
			VALUES (?, ?, 'P', ?, '[]', 1, 0, ?, ?, ?, ?, NULLIF(?, ''), NULLIF(?, ''))`,
			taskID, seq, "/ws/plan/phase-"+itoa(int64(seq))+".md", state, uuid, runStarted, runEnded, branch, start)
	}
	recorded := addPhase(2, "u-recorded", "done", runBranch, repo.start)
	gone := addPhase(3, "u-gone", "partial", "swarm/phase-deleted", repo.start)
	running := addPhase(4, "u-running", "running", runBranch, repo.start)
	noStart := addPhase(5, "u-nostart", "done", runBranch, "")
	noBranch := addPhase(6, "u-nobranch", "failed", "", repo.start)
	unresolved := addPhase(7, "u-unresolved", "done", runBranch, repo.start)
	// A phase that never ran is not a candidate at all.
	exec1(t, db, `INSERT INTO epic_phases (workspace_task_id, seq, name, doc_path, depends_on) VALUES (?, 8, 'never', '/ws/plan/phase-8.md', '[]')`, taskID)
	exec1(t, db, `INSERT INTO phase_actuals (phase_id, session_uuid, source, computed_at) VALUES (?, 'u-recorded', 'run-end', 'x')`, recorded)
	_ = running
	_ = noStart
	_ = noBranch

	resolve := func(id int64) (string, error) {
		if id == unresolved {
			return "", errors.New("not a git repository")
		}
		return repo.dir, nil
	}
	var logBuf bytes.Buffer
	r := newRecorder(db)

	// Dry run first: measures, stores nothing.
	st, err := r.Backfill(BackfillOptions{RepoRoot: resolve, DryRun: true})
	if err != nil {
		t.Fatalf("dry run: %v", err)
	}
	if st.Recorded != 1 {
		t.Errorf("dry run Recorded = %d, want 1", st.Recorded)
	}
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM phase_actuals`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("dry run stored rows: %d (%v), want only the pre-existing 1", n, err)
	}

	st, err = r.Backfill(BackfillOptions{RepoRoot: resolve, Log: &logBuf})
	if err != nil {
		t.Fatalf("Backfill: %v", err)
	}
	want := BackfillStats{Scanned: 7, Recorded: 1, AlreadyRecorded: 1, InFlight: 1,
		NoBranch: 1, NoStartPoint: 1, NoRepo: 1, BranchGone: 1}
	if st != want {
		t.Errorf("stats = %+v\nwant    %+v", st, want)
	}
	if st.Total() != st.Scanned {
		t.Errorf("buckets sum to %d, scanned %d — a run was double counted or dropped", st.Total(), st.Scanned)
	}
	row := readRow(t, db, runUUID)
	if row["phase_id"] != measurable || row["source"] != SourceBackfill || row["size_band"] != "S" {
		t.Errorf("backfilled row = phase %v source %v band %v", row["phase_id"], row["source"], row["size_band"])
	}
	// Not observed ending, and no run_events left for it: unknown, not 0.
	if row["continuations"] != nil {
		t.Errorf("backfilled continuations = %#v, want NULL", row["continuations"])
	}
	for _, w := range []string{"no longer exists", "no start point", "no run branch", "unresolved"} {
		if !strings.Contains(logBuf.String(), w) {
			t.Errorf("log lacks %q:\n%s", w, logBuf.String())
		}
	}
	_ = gone

	// --force recomputes the already-recorded run (its branch exists).
	st, err = r.Backfill(BackfillOptions{RepoRoot: resolve, Force: true})
	if err != nil {
		t.Fatalf("forced: %v", err)
	}
	if st.Recorded != 2 || st.AlreadyRecorded != 0 {
		t.Errorf("forced stats = %+v, want 2 recorded, 0 already recorded", st)
	}

	// No resolver at all: every measurable run is unresolved, nothing fails.
	st, err = r.Backfill(BackfillOptions{Force: true})
	if err != nil || st.Recorded != 0 || st.NoRepo != 4 {
		t.Errorf("no resolver: %+v (%v), want 0 recorded, 4 unresolved", st, err)
	}
}
