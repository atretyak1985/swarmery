package actuals

import (
	"testing"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/gitstat"
)

// A later pass that cannot read the diff (branch deleted or merged within the
// settle window) must keep the diff an earlier pass measured, not erase it.
func TestStoreKeepsMeasuredDiffWhenALaterPassCannotReadIt(t *testing.T) {
	db := openDB(t)
	r := newRecorder(db)
	added, removed, band := 30, 2, "S"
	first := Actuals{
		PhaseID: 1, SessionUUID: "u-keep", RunState: "done",
		Files:      []gitstat.FileStat{{Path: "internal/store/a.go", Added: 30, Removed: 2}},
		Areas:      []string{"internal/store"},
		LinesAdded: &added, LinesRemoved: &removed, SizeBand: &band,
		Source: SourceRunEnd,
	}
	if err := r.Store(first); err != nil {
		t.Fatal(err)
	}
	settled := Actuals{PhaseID: 1, SessionUUID: "u-keep", RunState: "done", Source: SourceRunEndSettled}
	if err := r.Store(settled); err != nil {
		t.Fatal(err)
	}
	row := readRow(t, db, "u-keep")
	if row["lines_added"] != int64(30) || row["size_band"] != "S" || row["files_json"] == nil || row["areas_json"] == nil {
		t.Fatalf("settled pass erased the measured diff: %v", row)
	}
	if row["source"] != SourceRunEndSettled {
		t.Fatalf("source = %v, want the settled pass", row["source"])
	}
}

// A runner printing module-relative paths must match an area the forecast wrote
// from the repo root.
func TestExpectsModuleRelativeLocusAgainstRepoRootArea(t *testing.T) {
	s := &forecastScope{Areas: []string{"tools/app/internal/store", "tools/app/web"}}
	for locus, want := range map[string]bool{
		"internal/store":            true,
		"internal/store/a_test.go":  true,
		"web/src/x.test.tsx":        true,
		"internal/restore":          false,
		"internal/ingest/a_test.go": false,
	} {
		if got := s.expects([]string{locus}); got != want {
			t.Errorf("expects(%q) = %v, want %v", locus, got, want)
		}
	}
}
