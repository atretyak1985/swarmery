package phaserun

import (
	"errors"
	"testing"
)

// TestStart_DecisionUndecidedRefused: a phase whose doc depends on an undecided
// expensive decision is refused before anything is acquired or stamped; a cheap
// undecided one never holds, and taking the decision lifts the hold.
func TestStart_DecisionUndecidedRefused(t *testing.T) {
	db, taskID, p1, _ := fixture(t)
	mustExec(t, db, `UPDATE epic_phases SET decision_deps = '["D-2","D-3"]' WHERE id = ?`, p1)
	mustExec(t, db, `INSERT INTO phase_decisions (workspace_task_id, decision_id, seq, question, cost, status, created_at)
		VALUES (?, 'D-2', 2, 'TTL?', 'cheap', 'undecided', 'x'),
		       (?, 'D-3', 3, 'Which store?', 'expensive', 'undecided', 'x')`, taskID, taskID)

	r := &stubRunner{}
	wt := &stubWt{}
	s := newTestService(db, r, wt)
	_, err := s.StartWith(p1, StartOptions{Force: true})
	var hold *DecisionUndecidedError
	if !errors.As(err, &hold) {
		t.Fatalf("StartWith err = %v, want *DecisionUndecidedError", err)
	}
	if hold.Detail != "decision-undecided: D-3 (Which store?)" || len(hold.Decisions) != 1 || hold.Decisions[0] != "D-3" {
		t.Errorf("hold = %+v", hold)
	}
	if r.specCount() != 0 || wt.acquiredCount() != 0 {
		t.Errorf("spawned %d / acquired %d, want nothing", r.specCount(), wt.acquiredCount())
	}
	if state, _, _, _ := phaseRow(t, db, p1); state == "running" {
		t.Errorf("run_state = %q, want untouched", state)
	}

	// The operator takes D-3: the hold lifts (D-2 is cheap and never held).
	mustExec(t, db, `UPDATE phase_decisions SET status = 'decided' WHERE decision_id = 'D-3'`)
	if h, err := DecisionHold(db, p1); err != nil || h != nil {
		t.Fatalf("DecisionHold after deciding = %+v, %v — want none", h, err)
	}
	if _, err := s.StartWith(p1, StartOptions{}); errors.As(err, &hold) {
		t.Errorf("decided phase still refused: %v", err)
	}
}

// A phase that declares no decision dependencies is never held, whatever the
// plan's decisions say.
func TestDecisionHold_NoDeclarationNoHold(t *testing.T) {
	db, taskID, p1, _ := fixture(t)
	mustExec(t, db, `INSERT INTO phase_decisions (workspace_task_id, decision_id, seq, question, cost, status, created_at)
		VALUES (?, 'D-1', 1, 'q', 'expensive', 'undecided', 'x')`, taskID)
	if h, err := DecisionHold(db, p1); err != nil || h != nil {
		t.Fatalf("DecisionHold = %+v, %v", h, err)
	}
}
