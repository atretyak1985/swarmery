package dispatch

// The dispatch twin of phaserun.ResumeSettingsFile / planrun.ResumeSettingsFile
// — see phaserun's for why this recovery lives in the engine rather than the
// api layer that resumes.
//
// It matters here for the same reason it matters for planrun: a dispatched
// card's --agent is only resolvable when the plugin that ships it is enabled,
// and for a multi-repo project's worktree (a checkout of one sub-repo, not the
// project root) that means the project's settings.json must be lent via
// --settings — see repopath.InheritedSettings, wired into RunSpec.SettingsFile
// by the run-root work above. Before that, dispatch never lent a settings
// file, so its resume origin could safely assume there was none to recover; it
// now can, and a resume that keeps assuming "none" resolves --agent against a
// worktree with nothing to resolve it against.

import (
	"database/sql"
	"strings"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/repopath"
)

// ResumeSettingsFile returns the --settings file the dispatched run that owns
// sessionUUID was spawned with, and whether the answer is TRUSTWORTHY ("" with
// ok=true means the run genuinely needed none — its worktree IS a checkout of
// the project root).
//
// worktree is the session's recorded cwd — the worktree the run was acquired
// into, the same acq.Path the original spawn passed.
func ResumeSettingsFile(db *sql.DB, sessionUUID, worktree string) (file string, ok bool) {
	if db == nil || strings.TrimSpace(sessionUUID) == "" {
		return "", false
	}
	var projectPath string
	var wsRoot sql.NullString
	err := db.QueryRow(`
		SELECT p.path, w.root_path
		  FROM tasks t JOIN projects p ON p.id = t.project_id
		  LEFT JOIN `+workspaceOnePerProject+` w ON w.project_id = p.id
		 WHERE t.dispatch_session_uuid = ?`, sessionUUID).Scan(&projectPath, &wsRoot)
	if err != nil || projectPath == "" {
		return "", false
	}
	svc := &Service{DB: db}
	root, err := svc.runRoot(projectPath, wsRoot.String)
	if err != nil {
		return "", false
	}
	return repopath.InheritedSettings(projectPath, root, worktree), true
}
