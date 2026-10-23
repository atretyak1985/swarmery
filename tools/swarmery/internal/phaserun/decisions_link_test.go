package phaserun

import (
	"errors"
	"strings"
	"testing"
)

// A skipped EXPENSIVE question holds the dependent phase, and the refusal names
// the question and the planning turn that asked it.
func TestStart_DecisionHoldLinksThePlanningTurn(t *testing.T) {
	db, taskID, p1, _ := fixture(t)
	mustExec(t, db, `UPDATE epic_phases SET decision_deps = '["D-3"]' WHERE id = ?`, p1)
	mustExec(t, db, `INSERT INTO phase_decisions (workspace_task_id, planning_session_id, decision_id, seq, question, cost, status, created_at)
		VALUES (?, 7, 'D-3', 3, 'Which store?', 'expensive', 'undecided', 'x')`, taskID)
	r := &stubRunner{}
	s := newTestService(db, r, &stubWt{})
	_, err := s.StartWith(p1, StartOptions{})
	var hold *DecisionUndecidedError
	if !errors.As(err, &hold) {
		t.Fatalf("StartWith err = %v, want *DecisionUndecidedError", err)
	}
	const link = "/p/p/plans?tab=new&planningSession=7&turn=3#planning-turn-3"
	if len(hold.Refs) != 1 || hold.Refs[0].DecisionID != "D-3" || hold.Refs[0].Question != "Which store?" ||
		hold.Refs[0].PlanningTurn != link {
		t.Fatalf("refs = %+v", hold.Refs)
	}
	if hold.Detail != "decision-undecided: D-3 (Which store?)" {
		t.Errorf("detail = %q", hold.Detail)
	}
	if msg := hold.Error(); !strings.HasPrefix(msg, hold.Detail) || !strings.Contains(msg, link) {
		t.Errorf("message = %q, want the detail and the planning-turn link", msg)
	}
	if r.specCount() != 0 {
		t.Error("the held phase was started")
	}
}

// A skipped CHEAP question does not hold: the phase starts, and its prompt names
// the recommended option it builds on.
func TestStart_CheapSkippedDecisionStartsWithTheRecommendation(t *testing.T) {
	db, taskID, p1, _ := fixture(t)
	mustExec(t, db, `UPDATE epic_phases SET decision_deps = '["D-2"]' WHERE id = ?`, p1)
	mustExec(t, db, `INSERT INTO phase_decisions (workspace_task_id, planning_session_id, decision_id, seq, question, cost, status, answer, created_at)
		VALUES (?, 7, 'D-2', 2, 'TTL?', 'cheap', 'recommended', '1h (без відповіді, рекомендований)', 'x'),
		       (?, 7, 'D-4', 4, 'Unrelated?', 'cheap', 'recommended', 'yes (без відповіді, рекомендований)', 'x')`, taskID, taskID)
	r := &stubRunner{}
	s := newTestService(db, r, &stubWt{})
	if _, err := s.StartWith(p1, StartOptions{}); err != nil {
		t.Fatalf("StartWith: %v", err)
	}
	r.mu.Lock()
	prompt := r.specs[0].Prompt
	r.mu.Unlock()
	if !strings.Contains(prompt, "- D-2 (recommended, no operator answer): TTL? → 1h\n") {
		t.Errorf("prompt lacks the recommended decision:\n%s", prompt)
	}
	if strings.Contains(prompt, "D-4") {
		t.Error("the prompt lists a decision the phase does not depend on")
	}
}
