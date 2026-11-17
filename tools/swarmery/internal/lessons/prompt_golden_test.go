package lessons

import (
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/gitstat"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/surprise"
)

// go test ./internal/lessons -run Golden -update rewrites the fixtures.
var updateGolden = flag.Bool("update", false, "rewrite the golden prompt fixtures")

func checkGolden(t *testing.T, name, got string) {
	t.Helper()
	path := filepath.Join("testdata", name)
	if *updateGolden {
		if err := os.MkdirAll("testdata", 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read golden (run with -update to create it): %v", err)
	}
	if got != string(want) {
		t.Errorf("%s drifted from the golden; run go test -run Golden -update and review the diff\n--- got ---\n%s", name, got)
	}
}

// Both prompts carry the lesson discipline (D6): at most one lesson, a graded
// confidence with its reason, and the no_entry_reason answer.
func assertDiscipline(t *testing.T, p string) {
	t.Helper()
	for _, want := range []string{
		"Return 0 or 1 lessons",
		`"confidence": "high", "medium" or "low"`,
		`is at most "low"`,
		`"confidence_reason"`,
		`{"lessons":[],"no_entry_reason":"…"}`,
		`"confidence":"high|medium|low","confidence_reason":"…"`,
	} {
		if !strings.Contains(p, want) {
			t.Errorf("prompt lacks %q", want)
		}
	}
	if strings.Contains(p, "Return 0, 1 or 2") {
		t.Error("prompt still allows two lessons")
	}
}

func TestSurprisePromptGolden(t *testing.T) {
	conf := 0.8
	added, removed, dur := int64(120), int64(4), int64(5400)
	in := Input{
		PhaseID: 12, SessionUUID: "run-1", PlanID: "2026-10-01-cache", PhaseName: "Phase One",
		Surprise: surprise.Stored{PhaseID: 12, SessionUUID: "run-1"},
		Forecasts: []Forecast{{Kind: "prior", Areas: []string{"internal/cost"}, SizeBand: "S",
			DurationBand: "30-90m", Outcome: "done", Confidence: &conf, Risks: []string{"none"}}},
		Actuals: &Actuals{Files: []gitstat.FileStat{{Path: "internal/ingest/record.go", Added: 120, Removed: 4}},
			LinesAdded: &added, LinesRemoved: &removed, SizeBand: "M", DurationS: &dur, Outcome: "completed",
			StartPoint: "abc123"},
		Divergence: "The cache usage fields moved under usage.cache_creation.",
	}
	in.Surprise.Index, in.Surprise.Top, in.Surprise.Summary = 0.82, "unexpected_areas", "touched ingest, not cost"
	p := BuildPrompt(in)
	assertDiscipline(t, p)
	checkGolden(t, "surprise_prompt.golden", p)
}

func TestReviewPromptGolden(t *testing.T) {
	in := ReviewInput{Type: "correctness", Area: "internal/api", AreaGlobs: []string{"internal/api/**"}, Phases: 2,
		Findings: []ReviewFinding{
			{ID: 8, PlanID: "p1", PhaseName: "One", Round: "R1", FindingID: "R1-1", File: "internal/api/a.go", Line: 10,
				Criterion: "errors are mapped", Evidence: "500 on a 404 path"},
			{ID: 9, PlanID: "p2", PhaseName: "Two", Round: "R1", FindingID: "R1-2", File: "internal/api/b.go"},
		}}
	p := BuildReviewPrompt(in)
	assertDiscipline(t, p)
	if !strings.Contains(p, `"area_globs": return exactly the area_globs listed under "The repeat".`) {
		t.Error(`the review prompt lost the "return exactly the area_globs listed" rule`)
	}
	checkGolden(t, "review_prompt.golden", p)
}
