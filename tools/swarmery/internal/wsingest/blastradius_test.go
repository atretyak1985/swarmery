package wsingest

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func blastFixture(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", "testdata", "fixtures", "blast-radius", name))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// lintBlast runs the doc through the same two parsers the scan and the Plans API
// chain: ParseForecasts for the files, ParseBlastRadius for the line.
func lintBlast(doc string) []ForecastLint {
	return LintBlastRadius(ParseForecasts(doc, false), ParseBlastRadius(doc))
}

func TestBlastRadiusLintFixtures(t *testing.T) {
	t.Run("forecast lists files, no line: fires", func(t *testing.T) {
		doc := blastFixture(t, "phase-with-risk.md")
		// The fixture quotes the line in a fence, cites it inline in Risks and
		// opens a Notes bullet with it as inline code — none of them is the line.
		if got := ParseBlastRadius(doc); got != "" {
			t.Fatalf("ParseBlastRadius = %q, want none (fenced / inline-code quotes are not the doc's line)", got)
		}
		ls := lintBlast(doc)
		if len(ls) != 1 || ls[0].Code != LintBlastRadiusMissing {
			t.Fatalf("lints = %+v, want exactly one %s", ls, LintBlastRadiusMissing)
		}
		if !strings.Contains(ls[0].Message, "2 file(s)") {
			t.Errorf("message %q does not count the forecast's 2 files", ls[0].Message)
		}
	})
	t.Run("doc carries the line: quiet", func(t *testing.T) {
		doc := blastFixture(t, "phase-with-blast-radius.md")
		want := "Blast radius: 3 callers in internal/orders, internal/api — internal/orders/service.go:41, " +
			"internal/api/line_items.go:88, internal/orders/totals_test.go:17 (sources: rg, serena)"
		if got := ParseBlastRadius(doc); got != want {
			t.Fatalf("ParseBlastRadius =\n  %q\nwant\n  %q", got, want)
		}
		if ls := lintBlast(doc); len(ls) != 0 {
			t.Fatalf("lints = %+v, want none", ls)
		}
	})
}

func TestParseBlastRadiusShapes(t *testing.T) {
	cases := []struct{ name, doc, want string }{
		{"bullet", "## Risks\n\n- Blast radius: 0 callers — new files only (sources: rg)\n", "Blast radius: 0 callers — new files only (sources: rg)"},
		{"bare line", "## Risks\nBlast radius: not computed — rg unavailable\n", "Blast radius: not computed — rg unavailable"},
		{"under Notes", "## Notes\n\n* blast radius: 2 callers in a — a/b.go:1, a/c.go:2\n", "blast radius: 2 callers in a — a/b.go:1, a/c.go:2"},
		{"bold label", "## Risks\n- **Blast radius:** 1 caller in a — a/b.go:3\n", "Blast radius:** 1 caller in a — a/b.go:3"},
		{"first wins", "- Blast radius: 1 caller in a — a/b.go:3\n- Blast radius: 9 callers\n", "Blast radius: 1 caller in a — a/b.go:3"},
		{"only fenced", "```\n- Blast radius: 1 caller in a — a/b.go:3\n```\n", ""},
		{"mid-sentence", "- the Blast radius: line goes under Risks\n", ""},
		{"none", "## Risks\n- the cache may be stale\n", ""},
	}
	for _, c := range cases {
		if got := ParseBlastRadius(c.doc); got != c.want {
			t.Errorf("%s: ParseBlastRadius = %q, want %q", c.name, got, c.want)
		}
	}
}

func TestLintBlastRadiusNeedsFiles(t *testing.T) {
	// A forecast naming only areas gives the planner nothing to look callers up for.
	areasOnly := []Forecast{{Kind: ForecastPrior, Areas: []string{"internal/orders"}}}
	if ls := LintBlastRadius(areasOnly, ""); ls != nil {
		t.Errorf("areas-only forecast linted: %+v", ls)
	}
	if ls := LintBlastRadius(nil, ""); ls != nil {
		t.Errorf("no forecast linted: %+v", ls)
	}
	// Files are counted once across the prior and the posterior.
	both := []Forecast{
		{Kind: ForecastPrior, Files: []string{"a.go", "b.go"}},
		{Kind: ForecastPosterior, Files: []string{"b.go", "c.go"}},
	}
	ls := LintBlastRadius(both, "  ")
	if len(ls) != 1 || !strings.Contains(ls[0].Message, "3 file(s)") || ls[0].Kind != "" {
		t.Errorf("lints = %+v, want one doc-level lint counting 3 distinct files", ls)
	}
	if ls := LintBlastRadius(both, "Blast radius: not computed — no rg"); ls != nil {
		t.Errorf("a not-computed line is still the line; linted: %+v", ls)
	}
}

// The scan half: parsePlan reads the line from a real plan dir and applyEpics
// stores it on the phase row, where the Plans API lints its absence.
func TestApplyEpicsStoresBlastRadius(t *testing.T) {
	db := carryFixture(t)
	warn, _ := collectWarn(t)
	phases := parsePlan(writePlan(t, map[string]string{
		"README.md": "# Epic\n\n| # | Phase | Doc | Depends on |\n|---|---|---|---|\n" +
			"| 1 | Without | `phase-1.md` | — |\n" +
			"| 2 | With | `phase-2.md` | 1 |\n",
		"phase-1.md": blastFixture(t, "phase-with-risk.md"),
		"phase-2.md": blastFixture(t, "phase-with-blast-radius.md"),
	}), warn)
	applyPhases(t, db, phases)

	stored := map[string]string{}
	rows, err := db.Query(`SELECT doc_path, blast_radius FROM epic_phases WHERE workspace_task_id = ?`, carryTaskID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var doc, line string
		if err := rows.Scan(&doc, &line); err != nil {
			t.Fatal(err)
		}
		stored[filepath.Base(doc)] = line
	}
	if got := stored["phase-1.md"]; got != "" {
		t.Errorf("phase-1 blast_radius = %q, want '' (no line of its own)", got)
	}
	if got := stored["phase-2.md"]; !strings.HasPrefix(got, "Blast radius: 3 callers in internal/orders") {
		t.Errorf("phase-2 blast_radius = %q, want the doc's line", got)
	}
}
