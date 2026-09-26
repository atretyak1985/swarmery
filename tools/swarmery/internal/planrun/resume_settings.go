package planrun

// The plan-run twin of phaserun.ResumeSettingsFile — see that file for why the
// recovery belongs to the engine rather than to the api layer that resumes.
//
// It matters more here than it does there. A plan run is the one engine that
// pairs --agent with a lent --settings, and the two are inseparable: the
// settings file is what enables the plugin the agent ships in, so a resume that
// asked for the agent without it would fail with "--agent 'tech-lead' not
// found" — the exact failure repopath.InheritedSettings was written for
// (2026-07-30). Hence the second return value: a caller that cannot recover the
// settings file must drop the agent too.

import (
	"database/sql"
	"strings"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/repopath"
)

// ResumeSettingsFile returns the --settings file the plan run that owns
// sessionUUID was spawned with, and whether the answer is trustworthy ("" with
// ok=true means the run genuinely needed none).
//
// worktree is the session's recorded cwd — the worktree the run was acquired
// into, the same third argument Start passed.
func ResumeSettingsFile(db *sql.DB, sessionUUID, worktree string) (file string, ok bool) {
	if db == nil || strings.TrimSpace(sessionUUID) == "" {
		return "", false
	}
	var taskID int64
	if err := db.QueryRow(
		`SELECT workspace_task_id FROM plan_runs WHERE run_session_uuid = ?`, sessionUUID,
	).Scan(&taskID); err != nil {
		return "", false
	}
	svc := &Service{DB: db}
	info, err := svc.loadPlan(taskID)
	if err != nil || info.ProjectPath == "" {
		return "", false
	}
	root, err := svc.runRoot(info)
	if err != nil {
		return "", false
	}
	return repopath.InheritedSettings(info.ProjectPath, root, worktree), true
}
