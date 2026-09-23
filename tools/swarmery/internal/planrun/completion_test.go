package planrun

import (
	"database/sql"
	"path/filepath"
	"strings"
	"testing"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/runcore"
)

// ── the completion loop (phase 3) ──
//
// planrun's criteria live across the plan's phase docs, which never enter the
// worktree, so its loop re-reads them from disk. Each test scripts both inputs
// the loop weighs: what the docs say, and what the transcript says.

func seedTranscript(t *testing.T, db *sql.DB, uuid, text string) {
	t.Helper()
	var sid int64
	err := db.QueryRow(`SELECT id FROM sessions WHERE session_uuid=?`, uuid).Scan(&sid)
	if err == sql.ErrNoRows {
		res, ierr := db.Exec(`INSERT INTO sessions (project_id, session_uuid, started_at)
			VALUES (1, ?, '2026-07-28T12:00:00Z')`, uuid)
		if ierr != nil {
			t.Fatalf("seed session: %v", ierr)
		}
		sid, _ = res.LastInsertId()
	} else if err != nil {
		t.Fatalf("seed session: %v", err)
	}
	var next int
	if err := db.QueryRow(`SELECT COALESCE(MAX(seq),0)+1 FROM turns WHERE session_id=?`, sid).Scan(&next); err != nil {
		t.Fatalf("seed turn seq: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO turns (session_id, seq, role, text, started_at)
		VALUES (?, ?, 'assistant', ?, '2026-07-28T12:00:00Z')`, sid, next, text); err != nil {
		t.Fatalf("seed turn: %v", err)
	}
}

// finishAllPhases writes every phase doc with its criteria ticked.
func finishAllPhases(t *testing.T, planDir string) {
	t.Helper()
	mustWrite(t, filepath.Join(planDir, "phase-1-schema.md"), "# Phase 1 — Schema\n\n- [x] a\n- [x] b\n")
	mustWrite(t, filepath.Join(planDir, "phase-2-ui.md"), "# Phase 2 — UI\n\n- [x] c\n")
}

func planRunState(t *testing.T, db *sql.DB, taskID int64) (string, sql.NullString) {
	t.Helper()
	var state string
	var runErr sql.NullString
	if err := db.QueryRow(`SELECT run_state, run_error FROM plan_runs WHERE workspace_task_id=?`,
		taskID).Scan(&state, &runErr); err != nil {
		t.Fatalf("read plan_runs: %v", err)
	}
	return state, runErr
}

func runEventKinds(t *testing.T, db *sql.DB, taskID int64) []string {
	t.Helper()
	var kinds []string
	for _, e := range runcore.RunEvents(db, Engine, taskID) {
		kinds = append(kinds, e.Kind)
	}
	return kinds
}

func TestPlanSettle_Done(t *testing.T) {
	db, taskID, planDir := fixture(t)
	r := &stubRunner{}
	r.runFn = func(spec RunSpec) (*Run, error) {
		finishAllPhases(t, planDir)
		seedTranscript(t, db, spec.SessionUUID, "All phases landed.\n\nPLAN DONE")
		return &Run{SessionUUID: spec.SessionUUID, ExitCode: 0}, nil
	}
	s := newTestService(db, r, &stubWt{})
	if _, err := s.Start(taskID, "", ""); err != nil {
		t.Fatalf("Start: %v", err)
	}

	state, runErr := planRunState(t, db, taskID)
	if state != "done" {
		t.Errorf("run_state = %q, want done", state)
	}
	if runErr.Valid {
		t.Errorf("run_error = %q, want NULL", runErr.String)
	}
	if n := r.specCount(); n != 1 {
		t.Errorf("spawned %d times, want 1", n)
	}
}

func TestPlanSettle_Blocked(t *testing.T) {
	db, taskID, _ := fixture(t)
	r := &stubRunner{}
	r.runFn = func(spec RunSpec) (*Run, error) {
		seedTranscript(t, db, spec.SessionUUID,
			"Phase 1 landed.\n\nPLAN BLOCKED at phase 2: the design needs a human decision")
		return &Run{SessionUUID: spec.SessionUUID, ExitCode: 0}, nil
	}
	s := newTestService(db, r, &stubWt{})
	if _, err := s.Start(taskID, "", ""); err != nil {
		t.Fatalf("Start: %v", err)
	}

	state, runErr := planRunState(t, db, taskID)
	if state != "blocked" {
		t.Errorf("run_state = %q, want blocked", state)
	}
	if runErr.String != "the design needs a human decision" {
		t.Errorf("run_error = %q, want the blocked reason", runErr.String)
	}
	if n := r.specCount(); n != 1 {
		t.Errorf("spawned %d times, want 1 — a blocked plan must not be continued", n)
	}
}

func TestPlanSettle_ContinuedThenDone(t *testing.T) {
	db, taskID, planDir := fixture(t)
	r := &stubRunner{}
	r.runFn = func(spec RunSpec) (*Run, error) {
		if spec.Resume {
			finishAllPhases(t, planDir)
			seedTranscript(t, db, spec.SessionUUID, "Phase 2 done.\n\nPLAN DONE")
			return &Run{SessionUUID: spec.SessionUUID, ExitCode: 0}, nil
		}
		mustWrite(t, filepath.Join(planDir, "phase-1-schema.md"), "# Phase 1 — Schema\n\n- [x] a\n- [x] b\n")
		seedTranscript(t, db, spec.SessionUUID, "Phase 1 is landed. Shall I carry on with phase 2?")
		return &Run{SessionUUID: spec.SessionUUID, ExitCode: 0}, nil
	}
	s := newTestService(db, r, &stubWt{})
	if _, err := s.Start(taskID, "", ""); err != nil {
		t.Fatalf("Start: %v", err)
	}

	if state, _ := planRunState(t, db, taskID); state != "done" {
		t.Errorf("run_state = %q, want done", state)
	}
	if n := r.specCount(); n != 2 {
		t.Fatalf("spawned %d times, want 2", n)
	}

	cont, first := r.lastSpec(), r.firstSpec()
	if !cont.Resume || cont.SessionUUID != first.SessionUUID {
		t.Error("the continuation must resume the SAME session")
	}
	if cont.Agent != first.Agent || cont.SettingsFile != first.SettingsFile ||
		cont.Cwd != first.Cwd || cont.ProjectPath != first.ProjectPath {
		t.Error("the continuation must keep the original run's agent, settings, cwd and project")
	}
	// The unticked criterion is named WITH its phase — ten phase docs word their
	// gates identically, so a bare label would be ambiguous.
	if !strings.Contains(cont.Prompt, "phase 2 — c") {
		t.Errorf("continuation prompt does not locate the unticked criterion:\n%s", cont.Prompt)
	}
	if !strings.Contains(cont.Prompt, "PLAN BLOCKED at phase <n>: <reason>") {
		t.Errorf("continuation prompt does not offer this engine's blocked ending:\n%s", cont.Prompt)
	}

	k := runEventKinds(t, db, taskID)
	if len(k) != 2 || k[0] != runcore.EventContinuation || k[1] != runcore.EventDone {
		t.Errorf("run events = %v, want [continuation done]", k)
	}
}

func TestPlanSettle_ContinuedTwiceThenPartial(t *testing.T) {
	db, taskID, _ := fixture(t)
	r := &stubRunner{}
	r.runFn = func(spec RunSpec) (*Run, error) {
		seedTranscript(t, db, spec.SessionUUID, "Here is where I got to.")
		return &Run{SessionUUID: spec.SessionUUID, ExitCode: 0}, nil
	}
	s := newTestService(db, r, &stubWt{})
	if _, err := s.Start(taskID, "", ""); err != nil {
		t.Fatalf("Start: %v", err)
	}

	state, runErr := planRunState(t, db, taskID)
	if state != "partial" {
		t.Errorf("run_state = %q, want partial", state)
	}
	if !strings.Contains(runErr.String, "0 of 3 criteria ticked after 2 continuations") {
		t.Errorf("run_error = %q, want the honest count", runErr.String)
	}
	if n := r.specCount(); n != 1+runcore.MaxContinuations {
		t.Errorf("spawned %d times, want %d — the cap is hard", n, 1+runcore.MaxContinuations)
	}
	want := []string{runcore.EventContinuation, runcore.EventContinuation, runcore.EventPartial}
	if got := runEventKinds(t, db, taskID); strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("run events = %v, want %v", got, want)
	}
}

// TestPlanPrompt_CarriesTheStandingInstructionAndBudgetExactlyOnce is step
// 3.4/3.5's acceptance criterion on the rendered plan-run prompt.
func TestPlanPrompt_CarriesTheStandingInstructionAndBudgetExactlyOnce(t *testing.T) {
	db, taskID, _ := fixture(t)
	r := &stubRunner{}
	s := newTestService(db, r, &stubWt{})
	if _, err := s.Start(taskID, "", ""); err != nil {
		t.Fatalf("Start: %v", err)
	}

	got := r.firstSpec().Prompt
	if n := strings.Count(got, "HOW YOUR TURN ENDS"); n != 1 {
		t.Errorf("standing instruction appears %d times, want exactly 1:\n%s", n, got)
	}
	if n := strings.Count(got, "Budget: "); n != 1 {
		t.Errorf("budget line appears %d times, want exactly 1:\n%s", n, got)
	}
	for _, want := range []string{"PLAN DONE", "PLAN BLOCKED at phase", "run-plan", "PHASE MANIFEST"} {
		if !strings.Contains(got, want) {
			t.Errorf("prompt lost %q:\n%s", want, got)
		}
	}
}
