package api

import (
	"strings"
	"testing"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/claudeflags"
)

func clearResumeKnobs(t *testing.T) {
	t.Helper()
	t.Setenv(resumePermEnv, "")
	t.Setenv(claudeflags.ModeEnv, "")
	t.Setenv(claudeflags.AgentDenyEnv, "")
	t.Setenv(resumeEffortEnv, "")
	t.Setenv(claudeflags.EffortEnv, "")
}

// A session swarmery did not spawn — an operator's own terminal session — is
// resumed with NO permission flag: the CLI's own default, never bypass. It used
// to get bypassPermissions like everything else.
func TestResume_ForeignSessionGetsNoBypass(t *testing.T) {
	clearResumeKnobs(t)
	db, _, _, _ := originFixture(t)
	session(t, db, "u-terminal", t.TempDir())

	got := strings.Join(resumeArgs("u-terminal", "hi", lookupResumeOrigin(db, "u-terminal")), " ")
	if strings.Contains(got, "--permission-mode") || strings.Contains(got, "bypassPermissions") {
		t.Errorf("foreign session resume argv %q carries a permission mode", got)
	}
}

// Review I2, direction 1: the planning PROCEED turn is a resume, so the
// PLANNING knob must govern it — pinning SWARMERY_PLANNING_PERMISSION_MODE used
// to be ignored there in favour of the resume knob.
func TestResume_PlanningKnobGovernsTheProceedTurn(t *testing.T) {
	clearResumeKnobs(t)
	t.Setenv("SWARMERY_PLANNING_PERMISSION_MODE", "plan")
	t.Setenv(resumePermEnv, "acceptEdits")
	o := withPlannerWritable(resumeOrigin{}, []string{"/ws/root"})
	if got := strings.Join(resumeArgs("u-wiz", "PROCEED", o), " "); !strings.Contains(got, "--permission-mode plan") {
		t.Errorf("planning resume argv %q ignores SWARMERY_PLANNING_PERMISSION_MODE", got)
	}
	// With the origin knob unset the resume knob still applies.
	t.Setenv("SWARMERY_PLANNING_PERMISSION_MODE", "")
	if got := strings.Join(resumeArgs("u-wiz", "PROCEED", o), " "); !strings.Contains(got, "--permission-mode acceptEdits") {
		t.Errorf("planning resume argv %q ignores the resume knob fallback", got)
	}
}

// Review I2, direction 2: no knob can hand a session swarmery did not spawn
// bypassPermissions — not the resume knob, not the global one.
func TestResume_NoKnobWidensAForeignSession(t *testing.T) {
	clearResumeKnobs(t)
	for _, knob := range []string{resumePermEnv, claudeflags.ModeEnv} {
		t.Setenv(resumePermEnv, "")
		t.Setenv(claudeflags.ModeEnv, "")
		t.Setenv(knob, "bypassPermissions")
		if got := strings.Join(resumeArgs("u-terminal", "hi", resumeOrigin{}), " "); strings.Contains(got, "bypassPermissions") || strings.Contains(got, "--permission-mode") {
			t.Errorf("%s=bypassPermissions widened a foreign resume: %q", knob, got)
		}
	}
	t.Setenv(claudeflags.ModeEnv, "")
	t.Setenv(resumePermEnv, "dontAsk")
	if got := strings.Join(resumeArgs("u-terminal", "hi", resumeOrigin{}), " "); !strings.Contains(got, "--permission-mode dontAsk") {
		t.Errorf("dontAsk on a foreign resume was dropped: %q", got)
	}
}

// Review round 2 (M1): a verifier resume carries the verifier's own deny list —
// the edit tools and the daemon-API rules — even when a knob pins bypass.
func TestResume_VerifierSessionKeepsItsDenyListUnderBypass(t *testing.T) {
	clearResumeKnobs(t)
	t.Setenv("SWARMERY_VERIFY_PERMISSION_MODE", "bypassPermissions")
	db, _, _, taskID := originFixture(t)
	mustExecT(t, db, `INSERT INTO verification_runs (target_key, task_id, verify_session_uuid, status, started_at)
		VALUES ('task:'||?, ?, 'u-verify-deny', 'running', '2026-09-20T10:05:00Z')`, taskID, taskID)
	session(t, db, "u-verify-deny", t.TempDir())

	got := strings.Join(resumeArgs("u-verify-deny", "the verdict line?", lookupResumeOrigin(db, "u-verify-deny")), " ")
	for _, want := range []string{"--permission-mode bypassPermissions", "--disallowedTools Edit,Write,MultiEdit,NotebookEdit,", "Bash(curl *127.0.0.1:7777*)"} {
		if !strings.Contains(got, want) {
			t.Errorf("verifier resume argv %q lacks %q", got, want)
		}
	}
}

// An executor session (here a phase run) resumes the way it ran: bypass plus
// the executor deny list.
func TestResume_ExecutorSessionInheritsBypassAndDenyList(t *testing.T) {
	clearResumeKnobs(t)
	db, _, _, taskID := originFixture(t)
	mustExecT(t, db, `INSERT INTO epic_phases
		(workspace_task_id, seq, name, doc_path, depends_on, repo, run_state, run_session_uuid, checkboxes_done, checkboxes_total)
		VALUES (?, 1, 'Phase 1', '/ws/plan/phase-1.md', '[]', 'app', 'running', 'u-phase', 0, 3)`, taskID)
	session(t, db, "u-phase", t.TempDir())

	got := strings.Join(resumeArgs("u-phase", "carry on", lookupResumeOrigin(db, "u-phase")), " ")
	if !strings.Contains(got, "--permission-mode bypassPermissions") || !strings.Contains(got, "Bash(git push*)") {
		t.Errorf("phase-run resume argv %q, want bypass + the deny list", got)
	}
}

// A planning session's resume — the PROCEED turn writes the plan — inherits the
// planner's least-privilege shape: dontAsk, read tools, an Edit rule only under
// the wizard's writable dir.
func TestResume_PlanningSessionInheritsThePlannerAllowList(t *testing.T) {
	clearResumeKnobs(t)
	db, _, _, _ := originFixture(t)
	mustExecT(t, db, `INSERT INTO planning_sessions(project_id, session_uuid, status, idea, mode, created_at, updated_at)
		VALUES (1, 'u-plan-wiz', 'awaiting_answer', 'idea', 'plan', '2026-09-20T10:00:00Z', '2026-09-20T10:00:00Z')`)
	session(t, db, "u-plan-wiz", t.TempDir())

	o := lookupResumeOrigin(db, "u-plan-wiz")
	o = withPlannerWritable(o, []string{"/ws/root"})
	got := strings.Join(resumeArgs("u-plan-wiz", "PROCEED", o), " ")
	for _, want := range []string{"--permission-mode dontAsk", "Edit(//ws/root/**)", "Read,Grep,Glob"} {
		if !strings.Contains(got, want) {
			t.Errorf("planning resume argv %q lacks %q", got, want)
		}
	}
	if strings.Contains(got, "bypassPermissions") {
		t.Errorf("planning resume argv %q carries bypassPermissions", got)
	}
}
