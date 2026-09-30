package main

import (
	"encoding/json"
	"testing"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/decide"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/store"
)

// SWARMERY_DECIDE_R5 switches rule R5 (a phase or plan run is task type
// feature) on for the replay WITHOUT --llm too: the switch changes what the
// rules answer, and a rules-only replay is where a rule's precision is read.
// Off — the default — the rule answers nothing. Delete this file with R5.
func TestDecideEval_R5SwitchAppliesRulesOnly(t *testing.T) {
	path, _ := evalDB(t)
	db, err := store.OpenNoMigrate(path)
	if err != nil {
		t.Fatal(err)
	}
	// Make the labelled session a phase run (its task_type label is bugfix).
	if _, err := db.Exec(`INSERT INTO epic_phases (workspace_task_id, seq, name, doc_path, checkboxes_total, run_session_uuid, run_state)
		VALUES (1, 1, 'Phase 1', '/ws/plan/phase-1-x.md', 3, 's-1', 'blocked')`); err != nil {
		t.Fatal(err)
	}
	db.Close()

	taskRules := func(env map[string]string) []decide.EvalRule {
		t.Helper()
		code, stdout, stderr := runDecideEval(t, func(k string) string { return env[k] }, "--db", path, "--json", "--questions", "task_type")
		if code != 0 {
			t.Fatalf("exit = %d, want 0\nstderr: %s", code, stderr)
		}
		if stderr != "" {
			t.Errorf("rules-only wrote to stderr: %q", stderr)
		}
		var rep decide.EvalReport
		if err := json.Unmarshal([]byte(stdout), &rep); err != nil {
			t.Fatalf("stdout is not the JSON report: %v\n%s", err, stdout)
		}
		if rep.LLM || len(rep.Questions) != 1 {
			t.Fatalf("report = %+v, want a rules-only task_type report", rep)
		}
		return rep.Questions[0].Rules
	}

	if rules := taskRules(nil); len(rules) != 0 {
		t.Errorf("R5 answered with the switch off: %+v", rules)
	}
	rules := taskRules(map[string]string{"SWARMERY_DECIDE_R5": "on"})
	if len(rules) != 1 || rules[0].Rule != decide.RulePhaseRunFeature || rules[0].N != 1 || rules[0].Agree != 0 {
		t.Errorf("rules = %+v, want R5 answering the one phase run (feature, against a bugfix label)", rules)
	}
}
