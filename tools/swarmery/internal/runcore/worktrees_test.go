package runcore

import (
	"database/sql"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/store"
)

func TestBranchMinting(t *testing.T) {
	if got := TaskBranch("T-42"); got != "swarm/T-42" {
		t.Errorf("TaskBranch = %q", got)
	}
	if got := PhaseBranch(7); got != "swarm/phase-7" {
		t.Errorf("PhaseBranch = %q", got)
	}
	if got := PlanBranch(9); got != "swarm/plan-9" {
		t.Errorf("PlanBranch = %q", got)
	}
	// The taskName helpers must agree with the branch helpers, because Acquire is
	// handed the former and derives the latter: a mismatch here is a reclaim that
	// looks at a branch no run ever used.
	if got := "swarm/" + PhaseTaskName(7); got != PhaseBranch(7) {
		t.Errorf("PhaseTaskName/PhaseBranch disagree: %q vs %q", got, PhaseBranch(7))
	}
	if got := "swarm/" + PlanTaskName(9); got != PlanBranch(9) {
		t.Errorf("PlanTaskName/PlanBranch disagree: %q vs %q", got, PlanBranch(9))
	}
}

// TestPhaseTaskNameFromBranch: the inverse of PhaseBranch accepts exactly
// swarm/phase-<digits> and refuses everything else rather than guess.
func TestPhaseTaskNameFromBranch(t *testing.T) {
	for _, tc := range []struct {
		branch, want string
		ok           bool
	}{
		{"swarm/phase-40368", "phase-40368", true},
		{"swarm/phase-", "", false},
		{"swarm/phase-12a", "", false},
		{"feat/x", "", false},
		{"swarm/plan-657", "", false},
		{"", "", false},
	} {
		got, ok := PhaseTaskNameFromBranch(tc.branch)
		if got != tc.want || ok != tc.ok {
			t.Errorf("PhaseTaskNameFromBranch(%q) = (%q, %v), want (%q, %v)", tc.branch, got, ok, tc.want, tc.ok)
		}
	}
	// Round trip: what PhaseBranch mints, the inverse reads back as PhaseTaskName.
	if got, ok := PhaseTaskNameFromBranch(PhaseBranch(7)); !ok || got != PhaseTaskName(7) {
		t.Errorf("round trip of PhaseBranch(7) = (%q, %v), want (%q, true)", got, ok, PhaseTaskName(7))
	}
}

// wtFixture builds a store with one project and returns the db.
func wtFixture(t *testing.T) *sql.DB {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "runcore.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	mustExec(t, db, `INSERT INTO projects(id, path, slug, first_seen)
		VALUES(1,'/repo/p','p','2026-01-01T00:00:00Z')`)
	return db
}

func mustExec(t *testing.T, db *sql.DB, q string, args ...any) {
	t.Helper()
	if _, err := db.Exec(q, args...); err != nil {
		t.Fatalf("exec %s: %v", q, err)
	}
}

// insertBoardRun writes an in-progress queue task holding a worktree.
func insertBoardRun(t *testing.T, db *sql.DB, extID string) {
	t.Helper()
	mustExec(t, db, `INSERT INTO tasks (project_id, title, prompt, status, created_at,
		source, external_id, board_column, worktree_path)
		VALUES (1,'board','p','running','2026-08-01T00:00:00Z','queue',?,'in_progress',?)`,
		extID, "/wt/p/"+extID)
}

// insertWorkspaceTask writes a workspace (plan) task and returns its id.
func insertWorkspaceTask(t *testing.T, db *sql.DB, extID string) int64 {
	t.Helper()
	res, err := db.Exec(`INSERT INTO tasks (project_id, title, prompt, status, created_at,
		source, external_id) VALUES (1,'epic','goal','running','2026-08-01T00:00:00Z','workspace',?)`, extID)
	if err != nil {
		t.Fatal(err)
	}
	id, _ := res.LastInsertId()
	return id
}

// TestWorktreeCount_CountsAllThreeSurfaces is the defect from the plan's evidence
// section: MaxWorktrees was enforced against `tasks` rows alone, so a machine full
// of phase and plan runs reported zero worktrees to the cap that exists to bound
// them.
func TestWorktreeCount_CountsAllThreeSurfaces(t *testing.T) {
	db := wtFixture(t)

	if n, err := WorktreeCount(db); err != nil || n != 0 {
		t.Fatalf("WorktreeCount on an empty store = %d, %v; want 0", n, err)
	}

	insertBoardRun(t, db, "T-1")
	if n, _ := WorktreeCount(db); n != 1 {
		t.Errorf("after a board run: %d, want 1", n)
	}

	taskID := insertWorkspaceTask(t, db, "2026-08-01-my-epic")
	mustExec(t, db, `INSERT INTO epic_phases
		(workspace_task_id, seq, name, doc_path, depends_on, run_state)
		VALUES (?, 1, 'P1', '/ws/plan/phase-1.md', '[]', 'running')`, taskID)
	if n, _ := WorktreeCount(db); n != 2 {
		t.Errorf("after a phase run: %d, want 2 — a running phase holds a worktree", n)
	}

	mustExec(t, db, `INSERT INTO plan_runs (workspace_task_id, run_state) VALUES (?, 'running')`, taskID)
	if n, _ := WorktreeCount(db); n != 3 {
		t.Errorf("after a plan run: %d, want 3 — this is the total MaxWorktrees must bound", n)
	}

	// Runs that have ENDED release their checkouts and must stop counting.
	mustExec(t, db, `UPDATE epic_phases SET run_state='done'`)
	mustExec(t, db, `UPDATE plan_runs SET run_state='failed'`)
	mustExec(t, db, `UPDATE tasks SET board_column='in_review' WHERE source='queue'`)
	if n, _ := WorktreeCount(db); n != 0 {
		t.Errorf("after every run ended: %d, want 0", n)
	}
}

// The key is (project, branch) so runs from DIFFERENT engines can be compared at
// all — the old external-id key could not express "this phase run and that board
// task want the same checkout".
func TestWorktreeKeys_AcrossEngines(t *testing.T) {
	db := wtFixture(t)
	insertBoardRun(t, db, "T-1")
	taskID := insertWorkspaceTask(t, db, "2026-08-01-my-epic")
	res, err := db.Exec(`INSERT INTO epic_phases
		(workspace_task_id, seq, name, doc_path, depends_on, run_state)
		VALUES (?, 1, 'P1', '/ws/plan/phase-1.md', '[]', 'running')`, taskID)
	if err != nil {
		t.Fatal(err)
	}
	phaseID, _ := res.LastInsertId()
	mustExec(t, db, `INSERT INTO plan_runs (workspace_task_id, run_state) VALUES (?, 'running')`, taskID)

	held, err := WorktreeKeys(db)
	if err != nil {
		t.Fatal(err)
	}
	want := map[WorktreeKey]bool{
		{ProjectID: 1, Branch: TaskBranch("T-1")}:    true,
		{ProjectID: 1, Branch: PhaseBranch(phaseID)}: true,
		{ProjectID: 1, Branch: PlanBranch(taskID)}:   true,
	}
	if !reflect.DeepEqual(held, want) {
		t.Errorf("keys = %v\nwant %v", held, want)
	}
}

// A phase that ran under a PREVIOUS row id has its real branch in run_branch
// (epic_phases identity is doc_path, so a renamed doc mints a new id). The stamped
// value is authoritative; deriving would name a branch that does not exist while
// the one actually holding the checkout goes unseen.
func TestWorktreeKeys_PrefersTheStampedRunBranch(t *testing.T) {
	db := wtFixture(t)
	taskID := insertWorkspaceTask(t, db, "2026-08-01-my-epic")
	res, err := db.Exec(`INSERT INTO epic_phases
		(workspace_task_id, seq, name, doc_path, depends_on, run_state, run_branch)
		VALUES (?, 1, 'P1', '/ws/plan/phase-1.md', '[]', 'running', 'swarm/phase-3')`, taskID)
	if err != nil {
		t.Fatal(err)
	}
	phaseID, _ := res.LastInsertId()

	held, err := WorktreeKeys(db)
	if err != nil {
		t.Fatal(err)
	}
	if !held[WorktreeKey{ProjectID: 1, Branch: "swarm/phase-3"}] {
		t.Errorf("stamped run_branch was ignored: %v", held)
	}
	if held[WorktreeKey{ProjectID: 1, Branch: PhaseBranch(phaseID)}] {
		t.Errorf("derived the branch although a stamped one exists: %v", held)
	}
}

// A board row with no external id has no deterministic checkout, so it cannot
// collide on a key it does not have.
func TestWorktreeKeys_SkipsRowsWithNoCheckoutIdentity(t *testing.T) {
	db := wtFixture(t)
	mustExec(t, db, `INSERT INTO tasks (project_id, title, prompt, status, created_at,
		source, external_id, board_column, worktree_path)
		VALUES (1,'board','p','running','2026-08-01T00:00:00Z','queue','','in_progress','/wt/p/x')`)
	held, err := WorktreeKeys(db)
	if err != nil {
		t.Fatal(err)
	}
	if len(held) != 0 {
		t.Errorf("keys = %v, want none", held)
	}
}
