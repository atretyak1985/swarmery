package wsingest

import (
	"database/sql"
	"testing"
)

// retroDoc renders a minimal phases/09-retrospective.md carrying one lesson —
// enough for applyRetro, and deliberately built from the same heading shapes the
// real template emits so the parser is exercised, not bypassed.
func normTitleRetroDoc(lessonTitle, action string) string {
	return "# Retrospective\n\n" +
		"## Lessons Learned\n\n" +
		"### Lesson 1: " + lessonTitle + "\n" +
		"Some description of what happened.\n" +
		"**Action**: " + action + "\n"
}

// seedNormTitleTask inserts one project-scoped task and returns its id.
func seedNormTitleTask(t *testing.T, db *sql.DB, id int64, externalID, startedAt string) int64 {
	t.Helper()
	mustExec(t, db, `INSERT INTO tasks
		(id, project_id, title, prompt, status, created_at, started_at, source, external_id)
		VALUES (?, 1, ?, 'goal', 'done', ?, ?, 'workspace', ?)`,
		id, externalID, startedAt, startedAt, externalID)
	return id
}

// applyRetroFor runs the real ingest write path for one task, in its own tx.
func applyRetroFor(t *testing.T, db *sql.DB, taskID int64, doc string) {
	t.Helper()
	s := New(db, Config{WorkspaceRoot: t.TempDir()})
	tx, err := db.Begin()
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	if err := s.applyRetro(tx, taskID, doc); err != nil {
		tx.Rollback()
		t.Fatalf("applyRetro: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit: %v", err)
	}
}

// TestNormTitleInsertPathSharesOneIdentity is the phase's central claim: two
// DIFFERENT tasks whose retros word the same lesson differently (case,
// punctuation, a stop-word, a different "Lesson N" ordinal) end up carrying ONE
// norm_title — which is what makes "the same lesson, ten times" countable at
// all.
func TestNormTitleInsertPathSharesOneIdentity(t *testing.T) {
	db := testDB(t)
	mustExec(t, db, `INSERT INTO projects (id, path, slug, name, first_seen)
		VALUES (1, '/work/alpha', 'alpha', 'Alpha', '2026-06-01T00:00:00Z')`)

	a := seedNormTitleTask(t, db, 1, "2026-09-01-task-a", "2026-09-01T00:00:00Z")
	b := seedNormTitleTask(t, db, 2, "2026-09-02-task-b", "2026-09-02T00:00:00Z")

	applyRetroFor(t, db, a, normTitleRetroDoc("Sync-Cache Before Build!", "run sync-cache.sh"))
	applyRetroFor(t, db, b, normTitleRetroDoc("sync the cache before the build", "run sync-cache.sh"))

	// Both titles survive verbatim — the fold is an ADDITIONAL key, never a
	// rewrite of what the author wrote.
	if got := count(t, db, `SELECT COUNT(*) FROM retro_lessons`); got != 2 {
		t.Fatalf("lessons = %d, want 2", got)
	}
	if got := count(t, db, `SELECT COUNT(DISTINCT title) FROM retro_lessons`); got != 2 {
		t.Errorf("distinct titles = %d, want 2 (titles must not be rewritten)", got)
	}

	var norm string
	var distinct, tasks int
	if err := db.QueryRow(`
		SELECT COUNT(DISTINCT l.norm_title), MIN(l.norm_title), COUNT(DISTINCT tr.task_id)
		  FROM retro_lessons l JOIN task_retros tr ON tr.id = l.retro_id`).
		Scan(&distinct, &norm, &tasks); err != nil {
		t.Fatalf("group query: %v", err)
	}
	if distinct != 1 {
		t.Fatalf("distinct norm_title = %d, want 1", distinct)
	}
	if norm != "sync cache before build" {
		t.Errorf("norm_title = %q, want %q", norm, "sync cache before build")
	}
	if tasks != 2 {
		t.Errorf("tasks sharing the lesson = %d, want 2", tasks)
	}
}

// TestNormTitleReinsertKeepsIdentity pins the contract the phase notes call out:
// applyRetro still DELETEs and reinserts a task's lessons on every rescan, and
// that is fine precisely because identity moved into norm_title — the key is
// recomputed to the same value and nothing downstream notices the churn.
func TestNormTitleReinsertKeepsIdentity(t *testing.T) {
	db := testDB(t)
	mustExec(t, db, `INSERT INTO projects (id, path, slug, name, first_seen)
		VALUES (1, '/work/alpha', 'alpha', 'Alpha', '2026-06-01T00:00:00Z')`)
	a := seedNormTitleTask(t, db, 1, "2026-09-01-task-a", "2026-09-01T00:00:00Z")

	applyRetroFor(t, db, a, normTitleRetroDoc("Pin fixture mtimes", "add pinMtime"))

	// Re-ingest the same retro with the lesson reworded: applyRetro deletes the
	// task's lessons and inserts the new set, so the surviving row is the NEW one.
	applyRetroFor(t, db, a, normTitleRetroDoc("PIN the FIXTURE mtimes.", "add pinMtime"))

	if got := count(t, db, `SELECT COUNT(*) FROM retro_lessons`); got != 1 {
		t.Fatalf("lessons after reinsert = %d, want 1 (delete + reinsert, not append)", got)
	}
	var title, norm string
	if err := db.QueryRow(`SELECT title, norm_title FROM retro_lessons`).Scan(&title, &norm); err != nil {
		t.Fatalf("row after reinsert: %v", err)
	}
	if title != "PIN the FIXTURE mtimes." {
		t.Fatalf("title = %q, want the reworded one — the row was not replaced", title)
	}
	if norm != "pin fixture mtimes" {
		t.Errorf("norm_title = %q, want %q — identity must survive the reinsert", norm, "pin fixture mtimes")
	}
}

// TestNormTitleBackfill covers the pre-0070 history path: rows written before
// the column existed carry '', the startup backfill folds them, a second call is
// a no-op, and a title that folds to nothing is left '' rather than being given
// a fake shared identity.
func TestNormTitleBackfill(t *testing.T) {
	db := testDB(t)
	mustExec(t, db, `INSERT INTO projects (id, path, slug, name, first_seen)
		VALUES (1, '/work/alpha', 'alpha', 'Alpha', '2026-06-01T00:00:00Z')`)
	seedNormTitleTask(t, db, 1, "2026-09-01-task-a", "2026-09-01T00:00:00Z")
	mustExec(t, db, `INSERT INTO task_retros (id, task_id, ingested_at)
		VALUES (1, 1, '2026-09-01T00:00:00Z')`)
	// Written the way a pre-0070 daemon wrote them: no norm_title at all.
	mustExec(t, db, `INSERT INTO retro_lessons (id, retro_id, seq, title) VALUES
		(1, 1, 1, 'Sync-Cache Before Build!'),
		(2, 1, 2, 'sync the cache before the build'),
		(3, 1, 3, '--- !!! ---')`)

	if got := count(t, db, `SELECT COUNT(*) FROM retro_lessons WHERE norm_title = ''`); got != 3 {
		t.Fatalf("pre-backfill empty keys = %d, want 3", got)
	}

	s := New(db, Config{WorkspaceRoot: t.TempDir()})
	n, err := s.BackfillNormTitles()
	if err != nil {
		t.Fatalf("backfill: %v", err)
	}
	if n != 2 {
		t.Fatalf("backfilled = %d, want 2 (the unfoldable title is skipped)", n)
	}
	if got := count(t, db,
		`SELECT COUNT(DISTINCT norm_title) FROM retro_lessons WHERE norm_title <> ''`); got != 1 {
		t.Errorf("distinct folded keys = %d, want 1", got)
	}
	if got := count(t, db, `SELECT COUNT(*) FROM retro_lessons WHERE norm_title = ''`); got != 1 {
		t.Errorf("unfoldable rows left empty = %d, want 1", got)
	}

	// Idempotent: the second pass has nothing left to fold.
	again, err := s.BackfillNormTitles()
	if err != nil {
		t.Fatalf("second backfill: %v", err)
	}
	if again != 0 {
		t.Errorf("second backfill wrote %d rows, want 0", again)
	}
}
