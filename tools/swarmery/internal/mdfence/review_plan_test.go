package mdfence

import (
	"reflect"
	"testing"
)

// TestParseReviewPlanDriftAliases: the plan round's drift entries name a
// decision — {id, where, how} — and read into the same Drift fields as a phase
// round's {criterion, file, note}; ids entries carry a D-n/SC-n id.
func TestParseReviewPlanDriftAliases(t *testing.T) {
	doc := "## Review\n\n```yaml\nkind: review\nround: plan\nverdict: FAIL\n" +
		"ids:\n  - {id: D-2, evidence: \"phase 3 @ abc1234\"}\n  - {id: SC-4, evidence: missing}\n" +
		"drift:\n  - {id: D-4, where: \"internal/x/y.go:42\", how: \"a second table instead of the column\"}\n" +
		"  - {criterion: \"c\", file: \"f.go\", note: \"n\"}\n```\n"
	rs, errs := ParseReviews(doc)
	if len(errs) != 0 || len(rs) != 1 {
		t.Fatalf("parse = %+v, %+v", rs, errs)
	}
	r := rs[0]
	if !reflect.DeepEqual(r.IDs, []IDEvidence{{ID: "D-2", Evidence: "phase 3 @ abc1234"}, {ID: "SC-4", Evidence: "missing"}}) {
		t.Errorf("ids = %+v", r.IDs)
	}
	want := []Drift{
		{Criterion: "D-4", File: "internal/x/y.go:42", Note: "a second table instead of the column"},
		{Criterion: "c", File: "f.go", Note: "n"},
	}
	if !reflect.DeepEqual(r.Drift, want) {
		t.Errorf("drift = %+v, want %+v", r.Drift, want)
	}
}
