package retrodigest

import (
	"strings"
	"testing"
)

// TestBuild_LabelsOnlyWhenPresent: classifier labels add a section when there
// are any, and a report without them renders byte for byte as before.
func TestBuild_LabelsOnlyWhenPresent(t *testing.T) {
	base := Report{From: "2026-09-01", To: "2026-09-14"}
	without, _ := Build(base, 1<<20)
	if strings.Contains(without, "Session labels") {
		t.Fatal("an unlabelled window must not grow a labels section")
	}
	with := base
	with.Labels = []LabelCount{{Field: "outcome", Value: "failed", Count: 4}}
	got, _ := Build(with, 1<<20)
	if !strings.Contains(got, "## Session labels (local classifier, advisory)") ||
		!strings.Contains(got, "- outcome = failed: 4 sessions") {
		t.Fatalf("labels section missing:\n%s", got)
	}
	if !strings.HasPrefix(got, without) {
		t.Error("the labels section must be appended after the existing sections")
	}
}
