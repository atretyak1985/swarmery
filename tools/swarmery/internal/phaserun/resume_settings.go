package phaserun

// Recovering the --settings file of a phase run that is already over.
//
// A resume of a phase-run session is the SAME run, one turn later, and it is
// spawned from internal/api with the flags the original carried. Model and
// effort are readable off the transcript; the settings file is not — it is
// DERIVED (repopath.InheritedSettings over the project path, the resolved run
// root and the worktree), and the derivation is the run root ladder that lives
// in this package. So the recovery lives here too, on top of the very same
// loadPhase + runRoot the original spawn used, rather than being re-implemented
// against the same three tables in the api layer, where it would drift the first
// time a hint source is added.

import (
	"database/sql"
	"strings"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/repopath"
)

// ResumeSettingsFile returns the --settings file the phase run that owns
// sessionUUID was spawned with, and whether the answer is TRUSTWORTHY.
//
// The two are separate because "" is a legitimate answer: a run whose worktree
// is cut from the project repo itself discovers .claude/settings.json on its own
// and is lent nothing (see repopath.InheritedSettings). The caller must be able
// to tell that from "this could not be worked out at all" — an unknown uuid, a
// project with no path, a run root that no longer resolves — because the flags a
// resume may safely carry depend on it.
//
// worktree is the session's recorded cwd, which is the worktree the run was
// acquired into: the same third argument Start passed.
func ResumeSettingsFile(db *sql.DB, sessionUUID, worktree string) (file string, ok bool) {
	if db == nil || strings.TrimSpace(sessionUUID) == "" {
		return "", false
	}
	var phaseID int64
	if err := db.QueryRow(
		`SELECT id FROM epic_phases WHERE run_session_uuid = ?`, sessionUUID,
	).Scan(&phaseID); err != nil {
		return "", false
	}
	svc := &Service{DB: db}
	info, err := svc.loadPhase(phaseID)
	if err != nil || info.ProjectPath == "" {
		return "", false
	}
	root, err := svc.runRoot(info)
	if err != nil {
		return "", false
	}
	return repopath.InheritedSettings(info.ProjectPath, root, worktree), true
}
