package corrections

import (
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/store"
)

var now = time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC)

func openDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "corrections.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func mustRecord(t *testing.T, db *sql.DB, c Correction, at time.Time) {
	t.Helper()
	if err := Record(db, c, at); err != nil {
		t.Fatalf("Record(%+v): %v", c, err)
	}
}

// The identity is the reason folded like a lesson title; without a reason it
// is the after text; with neither it is ” (no identity).
func TestCorrectionNormKeyFoldsReasonThenAfter(t *testing.T) {
	for _, tc := range []struct {
		c    Correction
		want string
	}{
		{Correction{Reason: "Pin the model in every run!", After: "ignored"}, "pin model every run"},
		{Correction{Reason: "", After: "Pin-the-model, in every run"}, "pin model every run"},
		{Correction{Reason: "the a an", After: "Pin the model"}, "pin model"},
		{Correction{}, ""},
	} {
		if got := NormKey(tc.c); got != tc.want {
			t.Errorf("NormKey(%+v) = %q, want %q", tc.c, got, tc.want)
		}
	}
}

// Record stores every field, folds the key, keeps a nil project NULL, and
// refuses a source outside the CHECK vocabulary or an empty ref.
func TestCorrectionRecordAndList(t *testing.T) {
	db := openDB(t)
	pid := int64(7)
	mustRecord(t, db, Correction{Source: SourceLessonEdit, Ref: "lesson:12", ProjectID: &pid,
		Before: "Old title", After: "New title", Reason: ""}, now.Add(-time.Hour))
	mustRecord(t, db, Correction{Source: SourceTriageUndo, Ref: "verdict:88",
		Before: "noise", After: `{"state":"open"}`, Reason: " undo friction/noise "}, now)

	rows, err := List(db, now.Add(-24*time.Hour), 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 {
		t.Fatalf("rows = %d, want 2: %+v", len(rows), rows)
	}
	// Newest first.
	if rows[0].Ref != "verdict:88" || rows[1].Ref != "lesson:12" {
		t.Fatalf("order = %s, %s; want verdict:88 then lesson:12", rows[0].Ref, rows[1].Ref)
	}
	if rows[0].Reason != "undo friction/noise" || rows[0].NormKey != "undo friction noise" {
		t.Errorf("reason/key = %q/%q", rows[0].Reason, rows[0].NormKey)
	}
	if rows[0].ProjectID != nil {
		t.Errorf("project of a project-less correction = %v, want nil", *rows[0].ProjectID)
	}
	if rows[1].NormKey != "new title" {
		t.Errorf("reason-less key = %q, want the after text folded", rows[1].NormKey)
	}
	if rows[1].ProjectID == nil || *rows[1].ProjectID != 7 {
		t.Errorf("project = %v, want 7", rows[1].ProjectID)
	}
	if rows[1].CreatedAt != "2026-10-07T11:00:00.000Z" {
		t.Errorf("created_at = %q, want the ingest timestamp shape", rows[1].CreatedAt)
	}

	// since bounds the list; limit caps it.
	if got, _ := List(db, now.Add(-time.Minute), 0); len(got) != 1 {
		t.Errorf("since-bounded = %d, want 1", len(got))
	}
	if got, _ := List(db, now.Add(-24*time.Hour), 1); len(got) != 1 {
		t.Errorf("limit 1 = %d", len(got))
	}

	if err := Record(db, Correction{Source: "lesson_poke", Ref: "lesson:1"}, now); !errors.Is(err, ErrInvalid) {
		t.Errorf("unknown source: err = %v, want ErrInvalid", err)
	}
	if err := Record(db, Correction{Source: SourceLessonRetire, Ref: "  "}, now); !errors.Is(err, ErrInvalid) {
		t.Errorf("empty ref: err = %v, want ErrInvalid", err)
	}
	if err := Record(nil, Correction{Source: SourceLessonRetire, Ref: "lesson:1"}, now); !errors.Is(err, ErrInvalid) {
		t.Errorf("nil db: err = %v, want ErrInvalid", err)
	}
	if got, _ := List(db, now.Add(-24*time.Hour), 0); len(got) != 2 {
		t.Errorf("rows after refused writes = %d, want 2", len(got))
	}
}

// Groups folds by norm_key: count is rows, refs/sources are distinct and
// newest first, the sample is the newest row's reason (or after text), the
// biggest group leads, ” keys are left out, and since bounds the window.
func TestCorrectionGroups(t *testing.T) {
	db := openDB(t)
	mustRecord(t, db, Correction{Source: SourceLessonEdit, Ref: "lesson:1", After: "Pin the model"}, now.Add(-3*time.Hour))
	mustRecord(t, db, Correction{Source: SourceLessonDismiss, Ref: "lesson:2", Reason: "pin the model"}, now.Add(-2*time.Hour))
	mustRecord(t, db, Correction{Source: SourceLessonDismiss, Ref: "lesson:2", Reason: "Pin the model!"}, now.Add(-time.Hour))
	mustRecord(t, db, Correction{Source: SourceRevisionReject, Ref: "revision:5", Reason: "wrong direction"}, now)
	// No identity: never grouped.
	mustRecord(t, db, Correction{Source: SourceLessonRetire, Ref: "lesson:3"}, now)
	// Out of the window.
	mustRecord(t, db, Correction{Source: SourceLessonEdit, Ref: "lesson:9", After: "Pin the model"}, now.Add(-48*time.Hour))

	gs, err := Groups(db, now.Add(-24*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if len(gs) != 2 {
		t.Fatalf("groups = %d, want 2: %+v", len(gs), gs)
	}
	g := gs[0]
	if g.NormKey != "pin model" || g.Count != 3 {
		t.Fatalf("lead group = %+v, want pin model x3", g)
	}
	if len(g.Refs) != 2 || g.Refs[0] != "lesson:2" || g.Refs[1] != "lesson:1" {
		t.Errorf("refs = %v, want [lesson:2 lesson:1]", g.Refs)
	}
	if len(g.Sources) != 2 || g.Sources[0] != SourceLessonDismiss || g.Sources[1] != SourceLessonEdit {
		t.Errorf("sources = %v", g.Sources)
	}
	if g.Sample != "Pin the model!" || g.Latest != "2026-10-07T11:00:00.000Z" {
		t.Errorf("sample/latest = %q/%q", g.Sample, g.Latest)
	}
	if gs[1].NormKey != "wrong direction" || gs[1].Count != 1 || gs[1].Sample != "wrong direction" {
		t.Errorf("second group = %+v", gs[1])
	}

	if got, _ := Groups(db, now.Add(time.Minute)); len(got) != 0 {
		t.Errorf("future since: groups = %d, want 0", len(got))
	}
}
