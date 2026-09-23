package api

import (
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/planning"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/store"
)

// A planning wizard's DEPTH must survive its resume turns exactly the way its
// model does (0067 → 0076). These tests assert what a resume WOULD be spawned
// with — wizardResumeOrigin, the production ladder — rather than spawning one.
//
// The load-bearing claim is the negative one in the second test. `--effort` is
// not like `--model`: omitting it does not mean "cheap" or "the account
// default", it means the CLI's own xhigh, the DEEPEST and most expensive
// setting. So a wizard row with no stored effort must land on the planner's
// ladder (SWARMERY_PLANNING_EFFORT → planning.DefaultEffort), and an empty
// Effort — which reaches the spawn as no flag at all — is a failure, not a
// tolerable default.

// wizardEffortFixture opens a migrated temp DB with one project and one
// planning_sessions row, and returns a Service over it plus the wizard's uuid.
// effort is written as SQL NULL when empty, which is what every row created
// before migration 0076 carries.
func wizardEffortFixture(t *testing.T, effort string) (*planning.Service, string) {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "wizard.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	const uuid = "wizard-uuid"
	mustExecT(t, db, `INSERT INTO projects (id, path, slug, name, first_seen)
		VALUES (1, '/p', 'proj', 'Proj', '2026-09-23T00:00:00Z')`)
	// The ingested transcript row a resume reads its model off. Present so the
	// test exercises the real override, not a nil-db shortcut.
	mustExecT(t, db, `INSERT INTO sessions (project_id, session_uuid, model, status, started_at, cwd)
		VALUES (1, ?, 'claude-opus-5-5', 'completed', '2026-09-23T00:00:00Z', '/p')`, uuid)

	var stored any
	if effort != "" {
		stored = effort
	}
	mustExecT(t, db, `INSERT INTO planning_sessions
		(project_id, session_uuid, status, idea, mode, model, effort, created_at, updated_at)
		VALUES (1, ?, 'awaiting_answer', 'an idea', 'plan', 'claude-opus-5-5', ?,
		        '2026-09-23T00:00:00Z', '2026-09-23T00:00:00Z')`, uuid, stored)

	return &planning.Service{DB: db}, uuid
}

// The stored rung is what the resume carries — not the resume site's own
// default, which is what every wizard turn silently used before 0076.
func TestWizardResumeCarriesStoredEffort(t *testing.T) {
	// `low` is deliberately NOT ResumeEffort ("high"), so a regression that drops
	// the wizard pin and falls back to the generic resume default is visible.
	svc, uuid := wizardEffortFixture(t, "low")

	got := wizardResumeOrigin(svc.DB, svc, uuid)
	if got.Effort != "low" {
		t.Errorf("resume effort = %q, want low (the depth the interview was started at)", got.Effort)
	}
	if got.Effort == ResumeEffort {
		t.Errorf("resume effort fell back to the generic resume default %q — the wizard's pin was dropped", ResumeEffort)
	}
	// The model pin must keep working alongside it; effort is an addition, not a
	// replacement.
	if got.Model != "claude-opus-5-5" {
		t.Errorf("resume model = %q, want claude-opus-5-5", got.Model)
	}
}

// A row that predates 0076 carries NULL. It must fall through the planner's
// ladder to the engine default — never to "", which is omission, which is xhigh.
func TestWizardResumeFallsThroughToEngineDefault(t *testing.T) {
	svc, uuid := wizardEffortFixture(t, "")

	got := wizardResumeOrigin(svc.DB, svc, uuid)
	if got.Effort == "" {
		t.Fatal("resume effort = \"\" — that omits --effort, which runs the turn at the CLI's xhigh, the most expensive setting")
	}
	if got.Effort != planning.DefaultEffort {
		t.Errorf("resume effort = %q, want %q (the planner's engine default)", got.Effort, planning.DefaultEffort)
	}
	if planning.DefaultEffort != "high" {
		t.Errorf("planning.DefaultEffort = %q, want high — phase 7 owns changing this, not step 2.7",
			planning.DefaultEffort)
	}
}

// An effort a newer (or corrupted) build wrote must not reach the CLI verbatim:
// an unknown --effort value kills the spawn before the run starts. It degrades
// to the ladder instead, the same way an env typo does.
func TestWizardResumeRejectsUnknownStoredEffort(t *testing.T) {
	svc, uuid := wizardEffortFixture(t, "ludicrous")

	got := wizardResumeOrigin(svc.DB, svc, uuid)
	if got.Effort != planning.DefaultEffort {
		t.Errorf("resume effort = %q, want %q (an unknown stored rung degrades, it does not pass through)",
			got.Effort, planning.DefaultEffort)
	}
}

// Sanity: the fixture's NULL really is NULL, so the fall-through test above is
// testing the legacy shape and not an empty string the fixture invented.
func TestWizardEffortFixtureWritesNull(t *testing.T) {
	svc, uuid := wizardEffortFixture(t, "")
	var effort *string
	if err := svc.DB.QueryRow(
		`SELECT effort FROM planning_sessions WHERE session_uuid = ?`, uuid,
	).Scan(&effort); err != nil && err != sql.ErrNoRows {
		t.Fatalf("read effort: %v", err)
	}
	if effort != nil {
		t.Errorf("fixture wrote %q, want NULL", *effort)
	}
}
