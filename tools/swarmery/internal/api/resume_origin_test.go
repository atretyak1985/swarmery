package api

import (
	"database/sql"
	"os"
	"path/filepath"
	"testing"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/claudeflags"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/store"
)

// A swarmery-spawned session is entitled to resume with the flags its engine
// spawned it with, and FOUR engines stamp a session uuid of their own. The first
// cut of lookupResumeOrigin knew two of them (dispatch and plan runs), which
// left the most common swarmery session there is — a phase run — and every
// verification resuming as a stranger.
//
// These tests use a real temp SQLite through the store's own migration path, not
// a hand-written schema: the point is that the queries match the LIVE tables
// (epic_phases.run_session_uuid from 0034, verification_runs.verify_session_uuid
// as rebuilt by 0058), and a fake schema would pin the test's own opinion of them.

// originFixture opens a migrated temp DB holding one project whose root is an
// UMBRELLA directory — no .git of its own, one checkout named `app` inside it,
// and the project's .claude/settings.json beside them. That is the multi-repo
// shape repopath.InheritedSettings exists for: a worktree cut from `app`
// discovers no project settings, so the engine lends the file explicitly.
// Returns the db, the project root and one workspace (epic) task.
func originFixture(t *testing.T) (db *sql.DB, projectPath, settingsFile string, taskID int64) {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "origin.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	projectPath = t.TempDir()
	if err := os.MkdirAll(filepath.Join(projectPath, "app", ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(projectPath, ".claude"), 0o755); err != nil {
		t.Fatal(err)
	}
	settingsFile = filepath.Join(projectPath, ".claude", "settings.json")
	if err := os.WriteFile(settingsFile, []byte(`{"enabledPlugins":{}}`), 0o644); err != nil {
		t.Fatal(err)
	}

	mustExecT(t, db, `INSERT INTO projects (id, path, slug, name, first_seen)
		VALUES (1, ?, 'proj', 'Proj', '2026-09-20T10:00:00Z')`, projectPath)
	mustExecT(t, db, `INSERT INTO tasks (project_id, title, prompt, status, created_at, source, external_id)
		VALUES (1, 'My Epic', 'goal', 'running', '2026-09-20T10:00:00Z', 'workspace', '2026-09-20-my-epic')`)
	if err := db.QueryRow(`SELECT id FROM tasks WHERE external_id='2026-09-20-my-epic'`).Scan(&taskID); err != nil {
		t.Fatal(err)
	}
	return db, projectPath, settingsFile, taskID
}

// session records the row ingest would have written for a run: the model it
// spoke as and the worktree it ran in.
func session(t *testing.T, db *sql.DB, uuid, cwd string) {
	t.Helper()
	mustExecT(t, db, `INSERT INTO sessions (project_id, session_uuid, status, started_at, model, cwd)
		VALUES (1, ?, 'completed', '2026-09-20T10:00:00Z', 'claude-opus-5-5', ?)`, uuid, cwd)
}

// A phase run is the most common swarmery session of all. It passes no --agent
// and no --setting-sources (phaserun.RunSpec has neither field), but it DOES lend
// its worktree the project's settings file — so that is exactly what its resume
// must carry, and nothing else.
func TestLookupResumeOrigin_PhaseRun(t *testing.T) {
	t.Setenv(resumeEffortEnv, "")
	t.Setenv(claudeflags.EffortEnv, "")

	db, _, settingsFile, taskID := originFixture(t)
	worktree := t.TempDir() // a worktree under ~/.swarmery: discovers no .claude/
	mustExecT(t, db, `INSERT INTO epic_phases
		(workspace_task_id, seq, name, doc_path, depends_on, repo, run_state, run_session_uuid, checkboxes_done, checkboxes_total)
		VALUES (?, 1, 'Phase 1', '/ws/plan/phase-1.md', '[]', 'app', 'running', 'u-phase', 0, 3)`, taskID)
	session(t, db, "u-phase", worktree)

	o := lookupResumeOrigin(db, "u-phase")
	if o.Model != "claude-opus-5-5" {
		t.Errorf("model = %q, want the session's own", o.Model)
	}
	if o.SettingsFile != settingsFile {
		t.Errorf("settings file = %q, want %q — a phase-run worktree cannot discover the project's settings on its own", o.SettingsFile, settingsFile)
	}
	if o.Agent != "" || o.SettingSources != "" {
		t.Errorf("origin = %+v, want no agent and no setting-sources: a phase run passes neither", o)
	}
	if o.Effort != ResumeEffort {
		t.Errorf("effort = %q, want %q", o.Effort, ResumeEffort)
	}
}

// A verification is the read-only judge: --setting-sources project,local like
// dispatch, but no agent (verify.RunSpec has no Agent field) and no lent
// settings file. Its uuid lives on verification_runs.verify_session_uuid — the
// table as migration 0058 rebuilt it, which is where the live schema ends.
func TestLookupResumeOrigin_Verification(t *testing.T) {
	t.Setenv(resumeEffortEnv, "")
	t.Setenv(claudeflags.EffortEnv, "")

	db, _, _, taskID := originFixture(t)
	mustExecT(t, db, `INSERT INTO verification_runs (target_key, task_id, verify_session_uuid, status, started_at)
		VALUES ('task:'||?, ?, 'u-verify', 'running', '2026-09-20T10:05:00Z')`, taskID, taskID)
	session(t, db, "u-verify", t.TempDir())

	o := lookupResumeOrigin(db, "u-verify")
	if o.SettingSources != swarmerySettingSources {
		t.Errorf("setting sources = %q, want %q", o.SettingSources, swarmerySettingSources)
	}
	if o.Agent != "" || o.SettingsFile != "" {
		t.Errorf("origin = %+v, want no agent and no settings file: verify passes neither", o)
	}
	if o.Model != "claude-opus-5-5" {
		t.Errorf("model = %q, want the session's own", o.Model)
	}
}

// The degradation contract: a session swarmery did not spawn keeps its model and
// nothing else. This must not change as origins are added — an unrecognised uuid
// resuming with someone else's agent would be far worse than resuming bare.
func TestLookupResumeOrigin_UnknownSessionStaysBare(t *testing.T) {
	t.Setenv(resumeEffortEnv, "")
	t.Setenv(claudeflags.EffortEnv, "")

	db, _, _, _ := originFixture(t)
	session(t, db, "u-terminal", t.TempDir())

	o := lookupResumeOrigin(db, "u-terminal")
	if o.Agent != "" || o.SettingSources != "" || o.SettingsFile != "" {
		t.Errorf("origin = %+v, want model+effort only for a session no engine spawned", o)
	}
	if o.Model != "claude-opus-5-5" || o.Effort != ResumeEffort {
		t.Errorf("origin = %+v, want the session's model and the resume effort default", o)
	}
}

// A plan run is the one engine that pairs --agent with a lent --settings, and
// the two are INSEPARABLE: the settings file is what enables the plugin the
// agent ships in, so an --agent sent without it names an agent the session
// cannot resolve ("--agent 'tech-lead' not found"). Recovered: both flags.
func TestLookupResumeOrigin_PlanRunCarriesAgentWithItsSettings(t *testing.T) {
	t.Setenv(resumeEffortEnv, "")
	t.Setenv(claudeflags.EffortEnv, "")

	db, _, settingsFile, taskID := originFixture(t)
	mustExecT(t, db, `INSERT INTO task_artifacts (task_id, kind, path, content_hash, parsed_at)
		VALUES (?, 'plan', '/ws/plan', 'h1', '2026-09-20T10:00:00Z')`, taskID)
	mustExecT(t, db, `INSERT INTO epic_phases
		(workspace_task_id, seq, name, doc_path, depends_on, repo, checkboxes_done, checkboxes_total)
		VALUES (?, 1, 'Phase 1', '/ws/plan/phase-1.md', '[]', 'app', 0, 3)`, taskID)
	mustExecT(t, db, `INSERT INTO plan_runs (workspace_task_id, agent, run_state, run_session_uuid)
		VALUES (?, 'tech-lead', 'running', 'u-plan')`, taskID)
	session(t, db, "u-plan", t.TempDir())

	o := lookupResumeOrigin(db, "u-plan")
	if o.Agent != "tech-lead" || o.SettingsFile != settingsFile {
		t.Errorf("origin = %+v, want agent tech-lead WITH settings %q", o, settingsFile)
	}
	if o.SettingSources != "" {
		t.Errorf("setting sources = %q, want none: a plan run lends a settings file instead", o.SettingSources)
	}
}

// …and when the settings file cannot be recovered — here because the plan's
// project root is no longer a resolvable repository — the agent goes with it.
// Sending --agent alone is the NEW failure this pairing prevents: before origin
// flags existed the reply worked, and a resume that dies on a flag we added is
// worse than one that continues bare.
func TestLookupResumeOrigin_PlanRunDropsAgentWhenSettingsAreUnrecoverable(t *testing.T) {
	t.Setenv(resumeEffortEnv, "")
	t.Setenv(claudeflags.EffortEnv, "")

	db, _, _, taskID := originFixture(t)
	// No plan artifact and no resolvable repo: loadPlan/runRoot cannot answer.
	mustExecT(t, db, `INSERT INTO plan_runs (workspace_task_id, agent, run_state, run_session_uuid)
		VALUES (?, 'tech-lead', 'running', 'u-plan-broken')`, taskID)
	session(t, db, "u-plan-broken", t.TempDir())

	o := lookupResumeOrigin(db, "u-plan-broken")
	if o.Agent != "" || o.SettingsFile != "" {
		t.Errorf("origin = %+v, want NEITHER flag: an agent without its settings stack cannot resolve", o)
	}
	if o.Model != "claude-opus-5-5" {
		t.Errorf("model = %q, want the session's own — the model rung never depends on the settings file", o.Model)
	}
}

// dispatch is the OTHER engine that pairs --agent with a lent --settings, the
// same inseparable pairing as a plan run (see TestLookupResumeOrigin_PlanRun*
// above) — added after dispatch gained a resolved run root and the ability to
// lend a multi-repo card's project settings file, which this branch's resume
// origin had not caught up to (it used to assume dispatch lent no settings
// file at all, which was true before that change).
func TestLookupResumeOrigin_DispatchCarriesAgentWithItsSettings(t *testing.T) {
	t.Setenv(resumeEffortEnv, "")
	t.Setenv(claudeflags.EffortEnv, "")

	db, projectPath, settingsFile, _ := originFixture(t)
	if err := os.WriteFile(filepath.Join(projectPath, ".claude", "project.json"),
		[]byte(`{"mainApp":"app"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	mustExecT(t, db, `INSERT INTO tasks (project_id, title, prompt, status, created_at, source, external_id, agent, dispatch_session_uuid)
		VALUES (1, 'Card', 'do it', 'running', '2026-09-20T10:00:00Z', 'queue', 'T-1', 'tech-lead', 'u-dispatch')`)
	session(t, db, "u-dispatch", t.TempDir())

	o := lookupResumeOrigin(db, "u-dispatch")
	if o.Agent != "tech-lead" || o.SettingsFile != settingsFile {
		t.Errorf("origin = %+v, want agent tech-lead WITH settings %q", o, settingsFile)
	}
	if o.SettingSources != swarmerySettingSources {
		t.Errorf("setting sources = %q, want %q — dispatch pins this unconditionally, unlike planrun", o.SettingSources, swarmerySettingSources)
	}
}

// …and when the settings file cannot be recovered — here because the project
// root declares no mainApp and is not itself a git checkout, so runRoot cannot
// resolve a repo — the agent goes with it, same posture as a plan run's drop
// case. --setting-sources stays: it is unconditional for dispatch, not paired
// with the settings file the way --agent is.
func TestLookupResumeOrigin_DispatchDropsAgentWhenSettingsAreUnrecoverable(t *testing.T) {
	t.Setenv(resumeEffortEnv, "")
	t.Setenv(claudeflags.EffortEnv, "")

	db, _, _, _ := originFixture(t)
	mustExecT(t, db, `INSERT INTO tasks (project_id, title, prompt, status, created_at, source, external_id, agent, dispatch_session_uuid)
		VALUES (1, 'Card', 'do it', 'running', '2026-09-20T10:00:00Z', 'queue', 'T-2', 'tech-lead', 'u-dispatch-broken')`)
	session(t, db, "u-dispatch-broken", t.TempDir())

	o := lookupResumeOrigin(db, "u-dispatch-broken")
	if o.Agent != "" || o.SettingsFile != "" {
		t.Errorf("origin = %+v, want NEITHER flag: an agent without its settings stack cannot resolve", o)
	}
	if o.SettingSources != swarmerySettingSources {
		t.Errorf("setting sources = %q, want %q even with the agent dropped — unconditional for dispatch", o.SettingSources, swarmerySettingSources)
	}
}
