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
	// The fixture covers SC-3 (cited in its report) and SC-4 (not cited).
	if !strings.Contains(got, "\n\n## Remaining\n\n- SC-4\n\n## Decisions honoured\n\n- SC-3\n\n---\n") {
		t.Errorf("body lacks Remaining SC-4 / Decisions honoured SC-3:\n%s", got)
	}
}

// TestPhasePRBodyIDSections: decision deps count as claims; a `not implemented:`
// id is listed with its why and is never remaining; a doc that neither claims
// nor cites an id gets no id sections at all.
func TestPhasePRBodyIDSections(t *testing.T) {
	doc := "# Phase 3 — Totals\n**Covers:** SC-1\n**Depends on decisions:** D-2, D-5\n\n## Goal\nTotals.\n\n" +
		"## Acceptance Criteria\n- [x] totals\n\n## Completion Report\nDone per D-2.\nnot implemented: D-5 — moved to the pricing plan\n"
	got := phasePRBody(doc, 1, 2, "p.md")
	want := "\n\n## Remaining\n\n- SC-1\n\n## Decisions honoured\n\n- D-2\n\n## Not implemented\n\n- D-5 — moved to the pricing plan\n\n---\n"
	if !strings.Contains(got, want) {
		t.Errorf("id sections:\n--- got ---\n%s\n--- want (contained) ---\n%s", got, want)
	}

	all := "# Phase 1\n**Covers:** SC-1\n\n## Completion Report\nSC-1 shipped.\n"
	if got := phasePRBody(all, 1, 2, "p.md"); !strings.Contains(got, "## Remaining\n\n"+phasePRNoRemain+"\n") {
		t.Errorf("fully cited phase:\n%s", got)
	}
	claimOnly := "# Phase 1\n**Covers:** SC-1\n\n## Completion Report\n"
	if got := phasePRBody(claimOnly, 1, 2, "p.md"); !strings.Contains(got, "## Decisions honoured\n\n"+phasePRNoHonoured+"\n") {
		t.Errorf("unwritten report:\n%s", got)
	}
	if got := phasePRBody("# P\n\n## Completion Report\nShipped it.\n", 1, 2, "p.md"); strings.Contains(got, "## Remaining") {
		t.Errorf("a doc without ids grew id sections:\n%s", got)
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
