package economics

import (
	"database/sql"
	"fmt"
	"path/filepath"
	"testing"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/store"
)

func currentModelDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "cm.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	if _, err := db.Exec(`INSERT INTO projects (id, path, slug, first_seen)
		VALUES (1, '/tmp/cm', 'cm', '2026-09-01T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO sessions (id, project_id, session_uuid, started_at)
		VALUES (1, 1, 'cm-1', '2026-09-01T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	return db
}

// addTurns writes n assistant turns on model, agedDays before now.
func addTurns(t *testing.T, db *sql.DB, model string, n, agedDays int) {
	t.Helper()
	for i := 0; i < n; i++ {
		if _, err := db.Exec(fmt.Sprintf(`
			INSERT INTO turns (session_id, seq, role, model, started_at)
			VALUES (1, (SELECT COALESCE(MAX(seq),0)+1 FROM turns), 'assistant', ?,
			        datetime('now', '-%d days'))`, agedDays), model); err != nil {
			t.Fatal(err)
		}
	}
}

// The flag config/routines/model-upgrade.json has been calling since the day it
// was written. It must return the model with the most assistant TURNS in the
// window — and fold the context-window marker in, or a fleet split between
// `claude-opus-5-5` and `claude-opus-5-5[1m]` can lose its own majority to a
// model nobody runs.
func TestCurrentModelCountsTurnsAndFoldsTheContextMarker(t *testing.T) {
	db := currentModelDB(t)
	addTurns(t, db, "claude-opus-5-5", 4, 1)
	addTurns(t, db, "claude-opus-5-5[1m]", 3, 2)
	addTurns(t, db, "claude-sonnet-5", 6, 1)

	model, turns, ok, err := CurrentModel(db)
	if err != nil {
		t.Fatalf("CurrentModel: %v", err)
	}
	if !ok {
		t.Fatal("ok = false, want a model")
	}
	if model != "claude-opus-5-5" {
		t.Errorf("model = %q, want claude-opus-5-5 (4+3 folded turns beat sonnet's 6)", model)
	}
	if turns != 7 {
		t.Errorf("turns = %d, want 7", turns)
	}
}

// Turns outside the window do not vote. A model retired a quarter ago must not
// keep being reported as the one the fleet runs.
func TestCurrentModelIgnoresTurnsOutsideTheWindow(t *testing.T) {
	db := currentModelDB(t)
	addTurns(t, db, "claude-opus-4-1", 50, CurrentModelWindowDays+5)
	addTurns(t, db, "claude-opus-5-5", 2, 1)

	model, turns, ok, err := CurrentModel(db)
	if err != nil {
		t.Fatalf("CurrentModel: %v", err)
	}
	if !ok || model != "claude-opus-5-5" || turns != 2 {
		t.Errorf("CurrentModel = (%q, %d, %v), want (claude-opus-5-5, 2, true)", model, turns, ok)
	}
}

// An empty window reports ok=false rather than an empty string. The caller
// turns that into a nonzero exit, which is the whole point of the change: the
// routine must fail loudly instead of substituting a guess.
func TestCurrentModelEmptyWindow(t *testing.T) {
	db := currentModelDB(t)
	model, _, ok, err := CurrentModel(db)
	if err != nil {
		t.Fatalf("CurrentModel: %v", err)
	}
	if ok || model != "" {
		t.Errorf("CurrentModel on an empty window = (%q, %v), want (\"\", false)", model, ok)
	}
}
