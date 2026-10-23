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
	got := phasePRBody(string(doc), 41, 207, phasePRPlanRel, phaseEvidence{})

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
	got := phasePRBody(doc, 3, 9, "plan/phase-1-scaffold.md", phaseEvidence{})

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
	got := phasePRBody(doc, 1, 2, "p.md", phaseEvidence{})
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

// The evidence sections: a phase claiming SC-3 (cited by its report) and SC-4
// (cited nowhere) lands SC-3 under Decisions honoured and SC-4 under Remaining;
// a not-implemented line is honoured with its reason, never remaining; an id
// evidenced elsewhere in the plan is honoured; an id the plan does not declare
// is left out; a plan with no declared ids gets no sections at all.
func TestPhasePRBodyEvidenceSections(t *testing.T) {
	doc := "# Phase 3 — Import\n**Covers:** SC-3, SC-4, SC-9\n**Depends on decisions:** D-5, D-6, D-7\n\n" +
		"## Goal\nImport.\n\n## Completion Report\nCloses SC-3.\n\n- not implemented: D-5 — the mirror waits on a policy\n"
	ev := phaseEvidence{
		Declared: map[string]bool{"SC-3": true, "SC-4": true, "D-5": true, "D-6": true, "D-7": true},
		Held:     map[string]bool{"D-6": true},
	}
	got := phasePRBody(doc, 1, 2, "p.md", ev)
	for _, want := range []string{
		"## Decisions honoured\n\n- D-5 — not implemented: the mirror waits on a policy\n- D-6\n- SC-3\n\n",
		"## Remaining\n\n- D-7\n- SC-4\n\n---\n",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("body missing %q\n--- got ---\n%s", want, got)
		}
	}
	if strings.Contains(got, "SC-9") {
		t.Errorf("an undeclared id leaked into the body:\n%s", got)
	}

	none := phasePRBody(doc, 1, 2, "p.md", phaseEvidence{Declared: map[string]bool{}})
	if strings.Contains(none, "## Remaining") || strings.Contains(none, "## Decisions honoured") {
		t.Errorf("a plan with no declared ids must get no evidence sections:\n%s", none)
	}

	all := phasePRBody("# P\n**Covers:** SC-1\n\n## Completion Report\nCloses SC-1.\n", 1, 2, "p.md", phaseEvidence{})
	if !strings.Contains(all, "## Remaining\n\n"+phasePRNoRemains+"\n") {
		t.Errorf("remaining placeholder missing:\n%s", all)
	}
}
