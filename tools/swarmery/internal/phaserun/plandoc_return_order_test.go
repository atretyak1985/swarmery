package phaserun

import (
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/phasediag"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/worktree"
)

// The executor ticks its criteria in the copy LENT into the worktree, never in the
// workspace document. Returning that copy after stamping therefore closed every
// run's measurement interval on the PRE-run count: run_checkboxes_after came back
// 0 while the doc on disk showed the work, and OutcomeFromRow prefers the stamped
// edge over the live count forever, so the phase was chipped `noop` permanently.
//
// Observed on 2026-09-18 across two plans — including a run that ticked eight of
// its nine open criteria and still reported `noop`.
func TestRunStampsTheCriteriaTheExecutorActuallyTicked(t *testing.T) {
	db, _, p1, _ := fixture(t)

	var docPath string
	if err := db.QueryRow(`SELECT doc_path FROM epic_phases WHERE id=?`, p1).Scan(&docPath); err != nil {
		t.Fatalf("read doc_path: %v", err)
	}
	wtPath := t.TempDir()

	// Stand in for the executor: tick every criterion in the lent copy, which is
	// the only copy it is ever given.
	r := &stubRunner{runFn: func(spec RunSpec) (*Run, error) {
		lent := filepath.Join(wtPath, worktree.PlanDocDir, filepath.Base(docPath))
		b, err := os.ReadFile(lent)
		if err != nil {
			t.Errorf("executor could not read its lent doc: %v", err)
			return &Run{SessionUUID: spec.SessionUUID, ExitCode: 1}, nil
		}
		if err := os.WriteFile(lent, []byte(strings.ReplaceAll(string(b), "- [ ]", "- [x]")), 0o644); err != nil {
			t.Errorf("executor could not write its lent doc: %v", err)
		}
		return &Run{SessionUUID: spec.SessionUUID, ExitCode: 0}, nil
	}}

	s := newTestService(db, r, &stubWt{pathOverride: wtPath})
	if _, err := s.Start(p1, ""); err != nil {
		t.Fatalf("start: %v", err)
	}

	var state string
	var total int
	var before, after sql.NullInt64
	waitFor(t, func() bool {
		db.QueryRow(`SELECT run_state, checkboxes_total, run_checkboxes_before, run_checkboxes_after
		               FROM epic_phases WHERE id=?`, p1).Scan(&state, &total, &before, &after)
		return state == "done"
	})

	// The workspace copy must carry the executor's work at all.
	doc, err := os.ReadFile(docPath)
	if err != nil {
		t.Fatalf("read workspace doc: %v", err)
	}
	if strings.Contains(string(doc), "- [ ]") {
		t.Fatalf("workspace doc still has unticked criteria — the lent copy was never returned:\n%s", doc)
	}
	ticks := strings.Count(string(doc), "- [x]")

	if !after.Valid || int(after.Int64) != ticks {
		t.Errorf("run_checkboxes_after = %v, want %d — the interval closed before the doc came back", after, ticks)
	}
	if got := phasediag.OutcomeFromRow(state, total, ticks, before, after); got == "noop" {
		t.Errorf("outcome = %q for a run that ticked %d criteria", got, ticks)
	}
}
