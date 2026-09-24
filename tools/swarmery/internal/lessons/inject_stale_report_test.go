package lessons

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// A Completion Report that already cited a lesson before this run started
// (written by an earlier run) must not credit the new run with that citation.
func TestStaleReportCitationIsNotCreditedToTheNextRun(t *testing.T) {
	db := openDB(t)
	phaseID := seedRun(t, db, "stale", 0.2, "")
	a := seedActive(t, db, phaseID, 1, "Alpha.", "internal/**", "2026-09-10T00:00:00Z")
	doc := filepath.Join(t.TempDir(), "phase.md")
	if err := os.WriteFile(doc, []byte("# P\n\n## Completion Report\n\nApplied ["+Ref(a)+"].\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	mustExec(t, db, `UPDATE epic_phases SET doc_path = ? WHERE id = ?`, doc, phaseID)
	inj := NewInjector(db, DefaultBudgetTokens)
	inj.Now = func() time.Time { return time.Date(2026, 9, 23, 0, 0, 0, 0, time.UTC) }

	if text := inj.ForPhase(phaseID, "run-B"); text == "" {
		t.Fatal("nothing injected")
	}
	inj.AfterPhaseRun(phaseID, "run-B", doc) // run B never touched the report
	var relied int
	_ = db.QueryRow(`SELECT relied_on FROM lesson_uses WHERE session_uuid='run-B' AND lesson_id=?`, a).Scan(&relied)
	if relied != 0 {
		t.Fatal("run B was credited with run A's report citation")
	}
}
