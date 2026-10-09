package api

import (
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Regenerate the golden with
// `go test ./internal/api -run TestPhasePRBodyGolden -update-phase-pr` and commit
// it together with the change that moved it. A distinct flag name (not -update)
// so it cannot collide with another golden test in this package.
var updatePhasePRGolden = flag.Bool("update-phase-pr", false, "rewrite testdata/phase-landing/phase-doc.pr.md")

const phasePRPlanRel = "working/2026/10/09/order-line-items/plan/phase-2-line-item-crud.md"

func TestPhasePRTitle(t *testing.T) {
	cases := []struct {
		plan  string
		seq   int
		name  string
		want  string
		label string
	}{
		{"Order line items", 2, "Line-item CRUD", "Order line items: Phase 2 — Line-item CRUD", "basic"},
		{"  Order line items \n", 10, " Totals ", "Order line items: Phase 10 — Totals", "trims both parts"},
	}
	for _, c := range cases {
		t.Run(c.label, func(t *testing.T) {
			if got := phasePRTitle(c.plan, c.seq, c.name); got != c.want {
				t.Errorf("phasePRTitle = %q, want %q", got, c.want)
			}
		})
	}
}

func TestPhasePRBodyGolden(t *testing.T) {
	doc, err := os.ReadFile(filepath.Join("testdata", "phase-landing", "phase-doc.md"))
	if err != nil {
		t.Fatal(err)
	}
	got := phasePRBody(string(doc), 41, 207, phasePRPlanRel)

	goldenPath := filepath.Join("testdata", "phase-landing", "phase-doc.pr.md")
	if *updatePhasePRGolden {
		if err := os.WriteFile(goldenPath, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(goldenPath)
	if err != nil {
		t.Fatalf("read golden: %v — regenerate with -update-phase-pr", err)
	}
	if got != string(want) {
		t.Errorf("phasePRBody drifted from %s.\n--- got ---\n%s\n--- want ---\n%s", goldenPath, got, want)
	}

	// Belt and braces on the properties the golden encodes, so a careless
	// -update-phase-pr cannot quietly bless a regression.
	for _, leak := range []string{"quoted template goal", "quoted criterion", "DELETE` returns 404"} {
		if strings.Contains(got, leak) {
			t.Errorf("body must not contain %q (fenced quote or unticked criterion)", leak)
		}
	}
	if !strings.HasSuffix(got, "Swarm-Phase: 41/207\nPlan: "+phasePRPlanRel+"\n") {
		t.Errorf("body must end with the Swarm-Phase / Plan trailer, got tail %q", got[max(0, len(got)-120):])
	}
}

func TestPhasePRBodyPlaceholders(t *testing.T) {
	doc := "# Phase 1 — Scaffold\n\n" +
		"## Goal\nStand up the module.\n\n" +
		"## Acceptance Criteria\n- [ ] builds\n- [ ] tests pass\n\n" +
		"## Completion Report\n"
	got := phasePRBody(doc, 3, 9, "plan/phase-1-scaffold.md")

	want := "## Goal\n\nStand up the module.\n\n" +
		"## Completion Report\n\n" + phasePRNoReport + "\n\n" +
		"### How to verify\n\n" + phasePRNoCriteria + "\n\n" +
		"---\n\nSwarm-Phase: 3/9\nPlan: plan/phase-1-scaffold.md\n"
	if got != want {
		t.Errorf("placeholders:\n--- got ---\n%s\n--- want ---\n%s", got, want)
	}
	if phasePRNoReport != "_Completion report not written yet._" {
		t.Errorf("report placeholder wording changed: %q", phasePRNoReport)
	}
}

func TestPhasePRBodyNoReportSectionNoGoal(t *testing.T) {
	doc := "# Phase 4 — Docs\n\n## Acceptance Criteria\n- [x] README updated\n"
	got := phasePRBody(doc, 1, 2, "p.md")
	for _, want := range []string{
		"## Goal\n\n" + phasePRNoGoal + "\n",
		"## Completion Report\n\n" + phasePRNoReport + "\n",
		"### How to verify\n\n- [x] README updated\n",
		"Swarm-Phase: 1/2\nPlan: p.md\n",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("body missing %q\n--- got ---\n%s", want, got)
		}
	}
}
