package advisor

// memory-engineering phase 3 — R14 tests. The two thresholds ARE the rule, so
// the cases are: two corrections of one key from two refs fire; two from the
// same ref stay silent; one stays silent. The firing case also pins identity,
// title, detail, evidence shape, the window bound, and the persistence path
// (Run upserts a skill-kind row, which is what 0072's CHECK must accept).

import (
	"database/sql"
	"strings"
	"testing"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/corrections"
)

// seedCorrection records one ledger row daysAgo days before testNow.
func seedCorrection(t *testing.T, db *sql.DB, source, ref, reason, after string, daysAgo int) {
	t.Helper()
	at := testNow.AddDate(0, 0, -daysAgo)
	if err := corrections.Record(db, corrections.Correction{
		Source: source, Ref: ref, Before: "old", After: after, Reason: reason,
	}, at); err != nil {
		t.Fatalf("Record: %v", err)
	}
}

func TestR14FiresOnTwoRefs(t *testing.T) {
	db := testDB(t)
	seedCorrection(t, db, corrections.SourceLessonEdit, "lesson:1", "", "Pin the model", 3)
	seedCorrection(t, db, corrections.SourceLessonDismiss, "lesson:2", "pin the model!", "", 1)
	// A third row outside the window must not count.
	seedCorrection(t, db, corrections.SourceLessonRetire, "lesson:3", "Pin the model", "", WindowDays+2)

	fs, err := r14RepeatedCorrection(db, evalWindow())
	if err != nil {
		t.Fatalf("r14RepeatedCorrection: %v", err)
	}
	if len(fs) != 1 {
		t.Fatalf("findings = %d, want 1: %+v", len(fs), fs)
	}
	f := fs[0]
	if f.rule != "R14" || f.targetKind != "skill" || f.target != "pin model" {
		t.Fatalf("finding identity = %s/%s/%s, want R14/skill/pin model", f.rule, f.targetKind, f.target)
	}
	if f.title != "Repeated operator correction: pin the model!" {
		t.Errorf("title = %q", f.title)
	}
	if !strings.HasPrefix(f.detail, `"pin the model!" was corrected 2 times across lesson:2, lesson:1 (lesson_dismiss, lesson_edit).`) {
		t.Errorf("detail = %q", f.detail)
	}
	counts, _ := f.evidence["counts"].(map[string]int)
	if counts["corrections"] != 2 || counts["refs"] != 2 {
		t.Errorf("evidence counts = %v", f.evidence["counts"])
	}
	if refs, _ := f.evidence["refs"].([]string); len(refs) != 2 || refs[0] != "lesson:2" {
		t.Errorf("evidence refs = %v", f.evidence["refs"])
	}
	if srcs, _ := f.evidence["sources"].([]string); len(srcs) != 2 {
		t.Errorf("evidence sources = %v", f.evidence["sources"])
	}
	if f.evidence["norm_key"] != "pin model" {
		t.Errorf("evidence norm_key = %v", f.evidence["norm_key"])
	}

	// Persistence: Run stores it as a skill-kind recommendation, and the
	// metric reads the windowed count.
	if _, err := Run(db, testNow); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if n := count(t, db, `SELECT COUNT(*) FROM recommendations WHERE rule = 'R14' AND target_kind = 'skill'
		AND target = 'pin model' AND status = 'proposed' AND dedup_key = 'R14:pin model'`); n != 1 {
		t.Fatalf("stored R14 rows = %d, want 1", n)
	}
	name, v, ok, err := metricValue(db, "R14", "pin model", evalWindow())
	if err != nil || !ok || name != "correction_count" || v != 2 {
		t.Errorf("metric = %s/%v/%v/%v, want correction_count/2/true/nil", name, v, ok, err)
	}
	if _, _, ok, _ := metricValue(db, "R14", "never corrected", evalWindow()); ok {
		t.Error("a key with no rows must be ok=false, not a measured zero")
	}
}

func TestR14SilentOnOneRef(t *testing.T) {
	db := testDB(t)
	seedCorrection(t, db, corrections.SourceLessonEdit, "lesson:1", "", "Pin the model", 3)
	seedCorrection(t, db, corrections.SourceLessonEdit, "lesson:1", "", "Pin the model", 1)

	fs, err := r14RepeatedCorrection(db, evalWindow())
	if err != nil {
		t.Fatalf("r14RepeatedCorrection: %v", err)
	}
	if len(fs) != 0 {
		t.Fatalf("findings = %d, want 0 (two corrections of ONE ref): %+v", len(fs), fs)
	}
}

func TestR14SilentOnOneCorrection(t *testing.T) {
	db := testDB(t)
	seedCorrection(t, db, corrections.SourceTriageUndo, "verdict:8", "undo friction/noise", "", 1)

	fs, err := r14RepeatedCorrection(db, evalWindow())
	if err != nil {
		t.Fatalf("r14RepeatedCorrection: %v", err)
	}
	if len(fs) != 0 {
		t.Fatalf("findings = %d, want 0: %+v", len(fs), fs)
	}
}

// A malformed window is an error, not an empty evaluation (so the sweep never
// treats "could not evaluate" as "the condition is gone").
func TestR14RejectsAMalformedWindow(t *testing.T) {
	db := testDB(t)
	if _, err := r14RepeatedCorrection(db, window{From: "yesterday", To: fmtTS(testNow)}); err == nil {
		t.Fatal("malformed window: err = nil")
	}
}
