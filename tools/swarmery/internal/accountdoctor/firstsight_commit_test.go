package accountdoctor

import (
	"os"
	"path/filepath"
	"testing"
)

// The first-sight ledger is written only once the run completes: while any
// later arm is still running (the point a hook watchdog could kill the
// process) the path is not yet recorded, so a killed run shows the warning
// again next time instead of never.
func TestFirstSightLedgerRecordedAfterTheRun(t *testing.T) {
	f := newFixture(t)
	root := t.TempDir()
	f.anchoredEstate(t, root, "estate", "")
	fresh := filepath.Join(root, "fresh")
	mustMkdir(t, fresh, 0o755)
	state := filepath.Join(t.TempDir(), "doctor")
	ledger := filepath.Join(state, LedgerFile)

	seenAfter := map[string]bool{}
	afterArm = func(name string) {
		_, err := os.Stat(ledger)
		seenAfter[name] = err == nil
	}
	t.Cleanup(func() { afterArm = nil })

	rep, err := Fast(Options{Path: fresh, StateDir: state, Record: true})
	if err != nil {
		t.Fatal(err)
	}
	if countFindings(rep, "first-sight", "") != 1 {
		t.Fatalf("no first-sight warning: %+v", rep.Findings)
	}
	for name, exists := range seenAfter {
		if exists {
			t.Errorf("the ledger existed after the %s arm — recorded before the run completed", name)
		}
	}
	if !seenAfter["first-sight"] && len(seenAfter) < 2 {
		t.Fatalf("afterArm saw too few arms: %v", seenAfter)
	}
	if _, err := os.Stat(ledger); err != nil {
		t.Errorf("the ledger was not written once the run completed: %v", err)
	}
	// Full records it the same way.
	os.Remove(ledger)
	if _, err := Full(Options{Path: fresh, StateDir: state, Record: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(ledger); err != nil {
		t.Errorf("Full did not record: %v", err)
	}
}
