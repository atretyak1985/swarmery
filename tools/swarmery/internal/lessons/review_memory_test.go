package lessons

import (
	"context"
	"database/sql"
	"testing"
	"time"
)

func insertLesson(t *testing.T, db *sql.DB, run, title, runsJSON string, rec int) int64 {
	t.Helper()
	return mustExec(t, db, `INSERT INTO surprise_lessons
		(source_phase_run, phase_id, seq, title, norm_title, guidance, recurrences, recurrence_runs_json,
		 created_at, updated_at)
		VALUES (?, 1, 1, ?, ?, 'g', ?, ?, 'now', 'now')`, run, title, wsNorm(title), rec, runsJSON)
}

// A lesson the operator dismissed does not come back as a fresh candidate when
// a later run learns it again; the recurrence lands on the dismissed row.
func TestGenerateRemembersADismissal(t *testing.T) {
	db := openDB(t)
	p1 := seedRun(t, db, runUUID, 0.9, report)
	if _, err := newGen(db, &fakeRunner{out: lessonJSON(p1)}).Generate(context.Background(), p1, runUUID); err != nil {
		t.Fatal(err)
	}
	ls, _ := List(db, StatusCandidate)
	if len(ls) != 1 {
		t.Fatalf("candidates = %d, want 1", len(ls))
	}
	if _, err := Dismiss(db, ls[0].ID, "not useful", time.Now()); err != nil {
		t.Fatal(err)
	}
	p2 := seedRun(t, db, "run-2", 0.9, report)
	if _, err := newGen(db, &fakeRunner{out: lessonJSON(p2)}).Generate(context.Background(), p2, "run-2"); err != nil {
		t.Fatal(err)
	}
	var rows, rec int
	var status string
	db.QueryRow(`SELECT COUNT(*), MAX(recurrences), MAX(status) FROM surprise_lessons`).Scan(&rows, &rec, &status)
	if rows != 1 || rec != 2 || status != StatusDismissed {
		t.Fatalf("rows=%d recurrences=%d status=%s, want the dismissed row seen twice and no new candidate", rows, rec, status)
	}
}

// A lesson merged into another follows the merge: a later recurrence lands on
// the merge target, not on a new candidate.
func TestGenerateFollowsAMerge(t *testing.T) {
	db := openDB(t)
	p1 := seedRun(t, db, runUUID, 0.9, report)
	if _, err := newGen(db, &fakeRunner{out: lessonJSON(p1)}).Generate(context.Background(), p1, runUUID); err != nil {
		t.Fatal(err)
	}
	ls, _ := List(db, StatusCandidate)
	target := insertLesson(t, db, "run-target", "A different lesson entirely", `["run-target"]`, 1)
	if _, err := Merge(db, ls[0].ID, MergeTarget{LessonID: target}, time.Now()); err != nil {
		t.Fatal(err)
	}
	p2 := seedRun(t, db, "run-2", 0.9, report)
	if _, err := newGen(db, &fakeRunner{out: lessonJSON(p2)}).Generate(context.Background(), p2, "run-2"); err != nil {
		t.Fatal(err)
	}
	var rows, rec int
	db.QueryRow(`SELECT COUNT(*) FROM surprise_lessons`).Scan(&rows)
	db.QueryRow(`SELECT recurrences FROM surprise_lessons WHERE id = ?`, target).Scan(&rec)
	if rows != 2 || rec != 3 {
		t.Fatalf("rows=%d target recurrences=%d, want 2 rows and the target seen 3 times", rows, rec)
	}
}

// Merging moves every run the candidate had absorbed, not only its source run.
func TestMergeCarriesEveryRecurrence(t *testing.T) {
	db := openDB(t)
	a := insertLesson(t, db, "R1", "Lesson A", `["R1","R2"]`, 2)
	b := insertLesson(t, db, "RB", "Lesson B", `["RB"]`, 1)
	if _, err := Merge(db, a, MergeTarget{LessonID: b}, time.Now()); err != nil {
		t.Fatal(err)
	}
	got, err := Get(db, b)
	if err != nil {
		t.Fatal(err)
	}
	if got.Recurrences != 3 || len(got.RecurrenceRuns) != 3 {
		t.Fatalf("target = %d recurrences %v, want RB, R1 and R2", got.Recurrences, got.RecurrenceRuns)
	}
}
