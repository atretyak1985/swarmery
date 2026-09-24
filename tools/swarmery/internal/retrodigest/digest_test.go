package retrodigest

import (
	"fmt"
	"regexp"
	"strings"
	"testing"
	"unicode/utf8"
)

func f64(v float64) *float64 { return &v }
func i64(v int64) *int64     { return &v }

// sample is a report exercising every section and every citation kind.
func sample() Report {
	return Report{
		From: "2026-08-10", To: "2026-08-24", Scope: "swarmery", Approx: true,
		Main: Main{CostUSD: 12.5, TokensOut: 91000, Errors: 7},
		Agents: []Agent{
			{Name: "tech-lead", Runs: 40, Sessions: 12, CostUSD: 4.2, TokensOut: 30000,
				Errors: 9, ErrorRate: 0.25, P95Ms: i64(92000), SuccessRate: f64(0.8),
				ReDispatchRate: f64(0.1), PrevErrorRate: 0.1, PrevRuns: 30, Improvable: true},
			{Name: "Explore", Runs: 90, Sessions: 20, CostUSD: 1.1, TokensOut: 8000,
				Errors: 1, ErrorRate: 0.01, PrevErrorRate: 0, PrevRuns: 10},
		},
		Friction: Friction{
			DeniedTools: []DeniedTool{
				{Tool: "Bash", Calls: 300, Denied: 12, HasRule: false},
				{Tool: "Write", Calls: 90, Denied: 3, HasRule: true},
			},
			ErrorGroups: []ErrorGroup{
				{Key: "file-not-found", Example: "no such file\nor directory", Count: 14,
					LastTs: "2026-08-23T10:00:00Z", Sessions: []string{"s-b", "s-a", "s-a"}},
				{Key: "timeout", Example: "context deadline exceeded", Count: 3,
					LastTs: "2026-08-22T10:00:00Z"},
			},
			Approvals: Approvals{Resolved: 12, Pending: 2, AvgResolveSec: f64(45), WaitTotalMin: 9},
		},
		Lessons: []Lesson{
			{TaskExternalID: "2026-08-20-thing", TaskTitle: "Thing", Date: "2026-08-20",
				Seq: 1, Title: "Stage explicit paths", Action: "never git add -A"},
			{TaskExternalID: "2026-08-21-other", TaskTitle: "Other", Date: "2026-08-21", Seq: 2,
				Title: "Read the migration tree first"},
		},
		Tasks: []Task{
			{ExternalID: "2026-08-20-thing", Title: "Thing", EstimatedHours: f64(4),
				ActualHours: f64(6), VariancePct: f64(50), Loops: 2, Delegations: 3,
				VerdictOK: 2, VerdictRedisp: 1},
			{ExternalID: "2026-08-21-other", Title: "Other", Loops: 0, Delegations: 1, VerdictOK: 1},
		},
		Recommendations: []Recommendation{
			{ID: 7, Rule: "R2", TargetKind: "agent", Target: "tech-lead", DedupKey: "R2:tech-lead",
				Title: "High behaviour-failure rate", Detail: "25% of runs\nfailed", Status: "proposed",
				Sessions: []string{"s-a"}},
			{ID: 3, Rule: "R1", TargetKind: "tool", Target: "Bash", DedupKey: "R1:Bash",
				Title: "Denied repeatedly", Detail: "", Status: "accepted"},
		},
		Surprises: []Surprise{
			{PhaseID: 12, Plan: "Learning loop", Phase: "Surprise scoring", Index: 0.4,
				Top: "outcome_miss", Summary: "surprise 0.40 — top: outcome_miss"},
			{PhaseID: 31, Plan: "Other plan", Phase: "Areas\nphase", Index: 0.8,
				Top: "unexpected_areas", Summary: "surprise 0.80 — top: unexpected_areas"},
		},
	}
}

func TestBuildIsDeterministic(t *testing.T) {
	r := sample()
	first, tr1 := Build(r, 30720)
	second, tr2 := Build(r, 30720)
	if first != second {
		t.Fatalf("digest is not byte-identical across two runs")
	}
	if tr1 || tr2 {
		t.Fatalf("sample report should fit in 30720 bytes, got truncated=%v/%v (%d bytes)", tr1, tr2, len(first))
	}
	// Reordering the inputs must not reorder the output: the sort is total.
	shuffled := sample()
	shuffled.Agents[0], shuffled.Agents[1] = shuffled.Agents[1], shuffled.Agents[0]
	shuffled.Recommendations[0], shuffled.Recommendations[1] = shuffled.Recommendations[1], shuffled.Recommendations[0]
	shuffled.Lessons[0], shuffled.Lessons[1] = shuffled.Lessons[1], shuffled.Lessons[0]
	shuffled.Tasks[0], shuffled.Tasks[1] = shuffled.Tasks[1], shuffled.Tasks[0]
	shuffled.Friction.DeniedTools[0], shuffled.Friction.DeniedTools[1] = shuffled.Friction.DeniedTools[1], shuffled.Friction.DeniedTools[0]
	shuffled.Friction.ErrorGroups[0], shuffled.Friction.ErrorGroups[1] = shuffled.Friction.ErrorGroups[1], shuffled.Friction.ErrorGroups[0]
	if got, _ := Build(shuffled, 30720); got != first {
		t.Fatalf("input order changed the digest; sorting is not total")
	}
}

func TestBuildCarriesEveryCitationKind(t *testing.T) {
	md, _ := Build(sample(), 30720)
	for _, want := range []string{
		cite(KindAgent, "tech-lead"),
		cite(KindRec, "7"),
		cite(KindErrorGroup, "file-not-found"),
		cite(KindSession, "s-a"),
		cite(KindTask, "2026-08-20-thing"),
		cite(KindLesson, "2026-08-20-thing#1"),
		cite(KindPhase, "12"),
	} {
		if !strings.Contains(md, want) {
			t.Errorf("digest is missing citation %s", want)
		}
	}
}

// markerRe must stay in lockstep with the validator in internal/retroanalysis.
var markerRe = regexp.MustCompile(`\[E:(agent|rec|error_group|session|task|lesson|phase):[^\]\s][^\]]*\]`)

func TestEveryMarkerMatchesTheValidatorGrammar(t *testing.T) {
	md, _ := Build(sample(), 30720)
	// The header carries the marker TEMPLATE ("[E:kind:id]") as instruction
	// text for the model — deliberately not a citable id. Scan the evidence
	// sections only, which is all the validator ever sees quoted back.
	_, body, ok := strings.Cut(md, "\n## ")
	if !ok {
		t.Fatal("digest has no sections")
	}
	if !strings.Contains(md, "[E:kind:id]") {
		t.Error("the header no longer tells the model the marker shape")
	}
	all := regexp.MustCompile(`\[E:[^\]]*\]`).FindAllString(body, -1)
	if len(all) == 0 {
		t.Fatal("no citation markers rendered at all")
	}
	for _, m := range all {
		if !markerRe.MatchString(m) {
			t.Errorf("marker %q does not match the citation grammar", m)
		}
	}
}

// oversized builds a report far too large for any sane budget.
func oversized() Report {
	r := sample()
	for i := 0; i < 400; i++ {
		r.Agents = append(r.Agents, Agent{
			Name: fmt.Sprintf("agent-%03d", i), Runs: int64(i), ErrorRate: 0.5,
		})
		r.Recommendations = append(r.Recommendations, Recommendation{
			ID: int64(100 + i), Rule: "R3", Target: fmt.Sprintf("t-%03d", i),
			DedupKey: fmt.Sprintf("R3:t-%03d", i), Title: strings.Repeat("x", 80),
			Detail: strings.Repeat("y", 200), Status: "proposed",
		})
	}
	return r
}

func TestBuildRespectsThePlanningIdeaLimit(t *testing.T) {
	// The planner's own len() check must never be the thing that catches an
	// overflow.
	md, truncated := Build(oversized(), 8000)
	if !truncated {
		t.Fatal("oversized report reported truncated=false")
	}
	if len(md) > 8000 {
		t.Fatalf("digest is %d bytes, over the 8000 limit", len(md))
	}
	if !strings.Contains(md, "omitted") {
		t.Fatalf("nothing in the digest discloses what was left out:\n%s", md)
	}
}

// The regression that motivated fair shares: on a real fleet the advisor emits
// dozens of recommendations, and rendered in full they consumed the ENTIRE
// budget — friction, lessons and estimation vanished from every digest,
// silently. No section may starve the others.
func TestNoSectionStarvesTheOthers(t *testing.T) {
	r := sample()
	for i := 0; i < 400; i++ {
		r.Recommendations = append(r.Recommendations, Recommendation{
			ID: int64(100 + i), Rule: "R3", Target: fmt.Sprintf("t-%03d", i),
			DedupKey: fmt.Sprintf("R3:t-%03d", i), Title: strings.Repeat("x", 80),
			Detail: strings.Repeat("y", 200), Status: "proposed",
		})
	}
	md, truncated := Build(r, 30720)
	if !truncated {
		t.Fatal("400 recommendations fit in 30KB? the fixture stopped being oversized")
	}
	for _, want := range []string{
		"## Advisor recommendations", "## Agents", "## Friction",
		"## Lessons learned", "## Estimation accuracy",
	} {
		if !strings.Contains(md, want) {
			t.Errorf("section %q was starved out by the recommendations:\n%s", want, headOf(md))
		}
	}
	// And the short sections keep their content, not just their heading.
	for _, want := range []string{
		cite(KindErrorGroup, "file-not-found"),
		cite(KindLesson, "2026-08-20-thing#1"),
		cite(KindTask, "2026-08-20-thing"),
	} {
		if !strings.Contains(md, want) {
			t.Errorf("evidence %s was starved out", want)
		}
	}
	if !strings.Contains(md, "more recommendations omitted") {
		t.Error("the trimmed section does not say how many it dropped")
	}
}

// headOf keeps a failure message readable.
func headOf(md string) string {
	if len(md) > 1200 {
		return md[:1200] + "\n…"
	}
	return md
}

func TestASlightOverflowTrimsItemsRatherThanSections(t *testing.T) {
	r := sample()
	full, _ := Build(r, 30720)
	md, truncated := Build(r, len(full)-10)
	if !truncated {
		t.Fatal("expected truncation just under the full size")
	}
	// Dropping a whole section to save ten bytes would be a bad trade.
	for _, want := range []string{
		"## Advisor recommendations", "## Agents", "## Friction",
		"## Lessons learned", "## Estimation accuracy",
	} {
		if !strings.Contains(md, want) {
			t.Errorf("section %q was dropped for a ten-byte overflow", want)
		}
	}
	if !strings.Contains(md, "omitted") {
		t.Error("items were dropped without saying so")
	}
}

// Only when a section cannot even render its heading does it go entirely, and
// then the least important goes first.
func TestATightLimitDropsSectionsLeastImportantFirst(t *testing.T) {
	const tight = 500 // small enough that not every section can render its heading
	md, truncated := Build(oversized(), tight)
	if !truncated {
		t.Fatalf("truncated=false at a %d-byte limit", tight)
	}
	if len(md) > tight {
		t.Fatalf("digest is %d bytes, over the limit", len(md))
	}
	if strings.Contains(md, "## Estimation accuracy") {
		t.Errorf("the lowest-priority section survived a hard squeeze:\n%s", md)
	}
	if !strings.Contains(md, "## Advisor recommendations") {
		t.Errorf("the highest-priority section was dropped first:\n%s", md)
	}
	if !strings.Contains(md, "digest truncated:") {
		t.Errorf("whole sections were dropped without the marker:\n%s", md)
	}
}

func TestBuildNeverExceedsATinyLimit(t *testing.T) {
	for _, limit := range []int{0, 1, 10, 50, 120, 400} {
		md, truncated := Build(sample(), limit)
		if len(md) > limit {
			t.Errorf("limit %d: got %d bytes", limit, len(md))
		}
		if !truncated {
			t.Errorf("limit %d: truncated=false", limit)
		}
		if !utf8.ValidString(md) {
			t.Errorf("limit %d: hard cut split a UTF-8 rune", limit)
		}
	}
}

func TestBuildOnEmptyReport(t *testing.T) {
	md, truncated := Build(Report{From: "2026-08-10", To: "2026-08-24"}, 8000)
	if truncated {
		t.Fatal("an empty report should not truncate")
	}
	if !strings.Contains(md, "whole fleet") {
		t.Error("an unscoped report should say so")
	}
	for _, want := range []string{
		"No agent ran in this window.",
		"The rule engine produced no open recommendation",
		"Nothing in this window was denied and no error fired.",
		"No task in this window recorded a lesson.",
		"No workspace task in this window carried a parsed artifact.",
		"No phase run in this window was scored against a forecast.",
	} {
		if !strings.Contains(md, want) {
			t.Errorf("empty digest is missing the honest %q line:\n%s", want, md)
		}
	}
}

func TestPartialSectionsAreCalledOut(t *testing.T) {
	r := sample()
	r.Partial = []string{"tasks", "lessons"}
	md, _ := Build(r, 30720)
	if !strings.Contains(md, "failed to load and are EMPTY here, not zero: lessons, tasks") {
		t.Errorf("partial sections are not disclosed (or not sorted):\n%s", md)
	}
}

func TestSessionCitesAreCappedAndDeduped(t *testing.T) {
	got := citeSessions([]string{"e", "d", "c", "b", "a", "a", "", "g", "f"})
	want := cite(KindSession, "a") + " " + cite(KindSession, "b") + " " +
		cite(KindSession, "c") + " " + cite(KindSession, "d") + " " +
		cite(KindSession, "e") + " (+2 more)"
	if got != want {
		t.Errorf("citeSessions:\n got %s\nwant %s", got, want)
	}
	if citeSessions(nil) != "" || citeSessions([]string{"", ""}) != "" {
		t.Error("empty session lists should render nothing")
	}
}

func TestOneLineFlattensMultilineProse(t *testing.T) {
	md, _ := Build(sample(), 30720)
	for _, line := range strings.Split(md, "\n") {
		if strings.Count(line, "- rationale:") > 0 && strings.Contains(line, "25% of runs failed") {
			return
		}
	}
	t.Errorf("a multi-line detail was not flattened onto one list line:\n%s", md)
}

// Surprises render most surprising first, one line each, citing the phase.
func TestSurprisesRenderMostSurprisingFirst(t *testing.T) {
	md, _ := Build(sample(), 30720)
	hi := strings.Index(md, cite(KindPhase, "31"))
	lo := strings.Index(md, cite(KindPhase, "12"))
	if hi < 0 || lo < 0 || hi > lo {
		t.Errorf("phase 31 (0.80) should precede phase 12 (0.40):\n%s", md)
	}
	if !strings.Contains(md, "- Other plan / Areas phase — surprise 0.80 (top: unexpected_areas)") {
		t.Errorf("surprise line not rendered as expected:\n%s", md)
	}
}
