package accountdoctor

import (
	"os"
	"path/filepath"
	"testing"
)

// The first-sight ledger is written only once the report has gone out: Fast
// and Full leave the record pending and Commit writes it. A run killed before
// its report reached the session — mid-arm, or mid-render under a hook
// watchdog — leaves the path unrecorded, so the warning shows again next time
// instead of never.
func TestFirstSightRecordedOnlyByCommit(t *testing.T) {
	f := newFixture(t)
	root := t.TempDir()
	f.anchoredEstate(t, root, "estate", "")
	fresh := filepath.Join(root, "fresh")
	mustMkdir(t, fresh, 0o755)
	state := filepath.Join(t.TempDir(), "doctor")
	ledger := filepath.Join(state, LedgerFile)

	for name, run := range map[string]func(Options) (Report, error){"Fast": Fast, "Full": Full} {
		os.Remove(ledger)
		rep, err := run(Options{Path: fresh, StateDir: state, Record: true})
		if err != nil {
			t.Fatal(err)
		}
		if countFindings(rep, "first-sight", "") != 1 {
			t.Fatalf("%s: no first-sight warning: %+v", name, rep.Findings)
		}
		if _, err := os.Stat(ledger); !os.IsNotExist(err) {
			t.Errorf("%s recorded the path before its report went out (stat: %v)", name, err)
		}
		if err := Commit(rep); err != nil {
			t.Fatalf("%s: Commit: %v", name, err)
		}
		if _, err := os.Stat(ledger); err != nil {
			t.Errorf("%s: Commit did not record the path: %v", name, err)
		}
	}

	// A read-only run, and a run with nothing to record, commit nothing.
	os.Remove(ledger)
	ro, _ := Fast(Options{Path: fresh, StateDir: state, Record: false})
	quiet, _ := Fast(Options{Path: t.TempDir(), StateDir: state, Record: true})
	for _, rep := range []Report{ro, quiet} {
		if err := Commit(rep); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := os.Stat(ledger); !os.IsNotExist(err) {
		t.Errorf("a run with nothing to record wrote the ledger (stat: %v)", err)
	}
}
