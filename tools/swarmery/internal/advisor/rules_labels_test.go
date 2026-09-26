package advisor

import (
	"path/filepath"
	"testing"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/store"
)

// TestSessionLabels: D2 labels are read when present, and absent/unknown
// labels add nothing — a rule never depends on the classifier having run.
func TestSessionLabels(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "labels.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if got := sessionLabels(db, "nope"); got != nil {
		t.Errorf("unlabelled session = %v, want nil", got)
	}
	if _, err := db.Exec(`INSERT INTO session_labels (session_uuid, task_type, outcome, failure_cause, labeled_at)
		VALUES ('a', 'bugfix', 'failed', 'unknown', '2026-09-23T00:00:00Z'),
		       ('b', 'unknown', 'unknown', 'unknown', '2026-09-23T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	got := sessionLabels(db, "a")
	if len(got) != 2 || got["task_type"] != "bugfix" || got["outcome"] != "failed" {
		t.Errorf("labels = %v, want task_type+outcome without the unknown cause", got)
	}
	if got := sessionLabels(db, "b"); got != nil {
		t.Errorf("all-unknown labels = %v, want nil", got)
	}
}
