package planrun

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/runcore"
)

// An orchestrated run is the one that exists FOR plans spanning repositories: it
// has no worktree, so the one-worktree refusal must not apply to it.
func TestStartOrchestrate_PlanSpansRepos_Admits(t *testing.T) {
	db, taskID, _ := fixture(t)
	projectRoot, _ := multiRepoFixture(t, db, taskID, "`app`")
	mkRepo(t, filepath.Join(projectRoot, "infra"))
	mustExec(t, db, `UPDATE epic_phases SET repo='`+"`infra`"+`' WHERE workspace_task_id=? AND seq=2`, taskID)

	r := &stubRunner{}
	wt := &stubWt{}
	s := newTestService(db, r, wt)
	s.RepoRoot = nil
	s.SwarmeryBin = "/opt/bin/swarmery"

	if _, err := s.Start(taskID, "tech-lead", "orchestrate"); err != nil {
		t.Fatalf("Start: %v", err)
	}
	spec := r.firstSpec()
	if spec.Cwd != projectRoot {
		t.Errorf("spec.Cwd = %q, want the project root %q", spec.Cwd, projectRoot)
	}
	if spec.SettingsFile != "" {
		t.Errorf("spec.SettingsFile = %q, want none (cwd is the project root)", spec.SettingsFile)
	}
	// No worktree, no run branch — so nothing to acquire, reclaim or remove.
	if wt.acquiredCount() != 0 || wt.removedCount() != 0 || len(wt.reclaimedList()) != 0 {
		t.Errorf("acquire/remove/reclaim = %d/%d/%d, want 0/0/0",
			wt.acquiredCount(), wt.removedCount(), len(wt.reclaimedList()))
	}
	var mode string
	if err := db.QueryRow(`SELECT mode FROM plan_runs WHERE workspace_task_id=?`, taskID).Scan(&mode); err != nil {
		t.Fatal(err)
	}
	if mode != "orchestrate" {
		t.Errorf("plan_runs.mode = %q, want orchestrate", mode)
	}
}

// The orchestrator resumes a plan whose phase runs it already started; those
// runs are its own work, not a competing mechanism.
func TestStartOrchestrate_AdmitsWhilePhaseRunActive(t *testing.T) {
	db, taskID, _ := fixture(t)
	mustExec(t, db, `UPDATE epic_phases SET run_state='running' WHERE workspace_task_id=? AND seq=1`, taskID)
	s := newTestService(db, &stubRunner{}, &stubWt{})

	if _, err := s.Start(taskID, "", "orchestrate"); err != nil {
		t.Fatalf("orchestrate Start = %v, want admitted", err)
	}
	// Every other mode still refuses: two mechanisms on the same docs.
	mustExec(t, db, `DELETE FROM plan_runs`)
	if _, err := s.Start(taskID, "", "auto"); err != ErrPhaseRunning {
		t.Errorf("auto Start = %v, want ErrPhaseRunning", err)
	}
}

func TestBuildOrchestratePrompt(t *testing.T) {
	phases := []Phase{
		{ID: 41, Seq: 1, Name: "Contract", DocPath: "/ws/plan/phase-1.md", Done: 2, Total: 2},
		{ID: 42, Seq: 2, Name: "Edge", DocPath: "/ws/plan/phase-2.md", Total: 3, DependsOn: []int{1}},
	}
	p := BuildOrchestratePrompt(7, "/ws/plan", "# Readme body", phases, "/opt/bin/swarmery", runcore.Budget{})
	for _, want := range []string{
		"run-plan", "ROUTE D", "daemon-orchestration.md",
		"PLAN TASK ID (the {taskId} of every API path): 7",
		"/opt/bin/swarmery api",
		"Phase 1 (phaseId 41) [DONE]",
		"Phase 2 (phaseId 42) [TODO] Edge — 0/3 criteria, depends on: 1",
		"NEVER merge",
		`land {"action":"pr"}`,
		"# Readme body",
	} {
		if !strings.Contains(p, want) {
			t.Errorf("orchestrate prompt missing %q", want)
		}
	}
}

func TestValidModeOrchestrate(t *testing.T) {
	if got := ValidMode(" orchestrate "); got != ModeOrchestrate {
		t.Errorf("ValidMode(orchestrate) = %q", got)
	}
	// The worktree prompt never carries the orchestrate directive.
	if d := modeDirective(ModeOrchestrate); d != "" {
		t.Errorf("modeDirective(orchestrate) = %q, want empty", d)
	}
}
