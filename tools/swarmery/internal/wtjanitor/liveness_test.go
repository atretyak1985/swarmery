package wtjanitor

import (
	"path/filepath"
	"testing"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/store"
)

// TestProcLivenessBusyByStatus: every live session status vetoes a sweep of
// its cwd — awaiting_reply included (an interactive session waiting on the
// operator still owns its checkout) — and the terminal ones do not.
func TestProcLivenessBusyByStatus(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if _, err := db.Exec(`INSERT INTO projects (id, path, slug, first_seen) VALUES (1, '/tmp/p', 'p', '2026-10-06T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	for status, want := range map[string]bool{
		"active": true, "idle": true, "awaiting_reply": true,
		"completed": false, "killed": false,
	} {
		cwd := "/tmp/wt-" + status
		if _, err := db.Exec(`INSERT INTO sessions (project_id, session_uuid, status, started_at, source, cwd)
			VALUES (1, ?, ?, '2026-10-06T00:00:00Z', 'jsonl', ?)`, "u-"+status, status, cwd); err != nil {
			t.Fatal(err)
		}
		got, err := ProcLiveness{DB: db}.Busy(cwd)
		if err != nil {
			t.Fatalf("Busy(%s): %v", cwd, err)
		}
		if got != want {
			t.Errorf("Busy for a %s session = %v, want %v", status, got, want)
		}
	}
}
