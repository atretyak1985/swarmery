package planning

import (
	"strings"
	"testing"
)

// The planner's half of the forecast contract: every phase doc it writes gets a
// `kind: prior` block, and the same one sentence that stops the forecast turning
// into a scope fence — once, in each of the two prompts that are contracts in
// their own right (plan mode and revise mode are never rendered together).
const notALimit = "It is a prediction, not a limit: do whatever the phase actually needs."

func TestPlanPromptForecastContract(t *testing.T) {
	got := BuildPrompt("add line items to orders", "/ws")

	if n := strings.Count(got, notALimit); n != 1 {
		t.Errorf("%q appears %d times, want exactly 1", notALimit, n)
	}
	if !strings.Contains(got, "kind: prior") {
		t.Error("the plan prompt never names the `kind: prior` block")
	}
	// Point at the format, do not restate it: plan-format.md is the source of
	// truth and a second copy in the prompt is a second thing to keep in sync.
	if !strings.Contains(got, "plan-format.md") {
		t.Error("the plan prompt does not point at plan-format.md for the forecast shape")
	}
	if strings.Contains(got, "size_band:") {
		t.Error("the plan prompt restates the forecast yaml instead of citing plan-format.md")
	}
}

func TestRevisePromptForecastContract(t *testing.T) {
	got := BuildRevisePrompt(ReviseInput{
		Reason: "scope changed", PlanDir: "/p/plan", ScratchDir: "/p/scratch", PlanTitle: "Epic",
	})
	if n := strings.Count(got, notALimit); n != 1 {
		t.Errorf("%q appears %d times, want exactly 1", notALimit, n)
	}
	if !strings.Contains(got, "kind: prior") {
		t.Error("the revise prompt never names the `kind: prior` block a NEW phase doc needs")
	}
}

// The planner's half of the `**Effort:**` contract: both prompts require the
// header on every phase doc they write, and name the lint that catches a doc
// without it.
func TestPromptsRequireEffortHeader(t *testing.T) {
	for name, got := range map[string]string{
		"plan": BuildPrompt("add line items to orders", "/ws"),
		"revise": BuildRevisePrompt(ReviseInput{
			Reason: "scope changed", PlanDir: "/p/plan", ScratchDir: "/p/scratch", PlanTitle: "Epic",
		}),
	} {
		if !strings.Contains(got, "`**Effort:**` line (low | medium | high | xhigh)") {
			t.Errorf("%s prompt does not require the **Effort:** header", name)
		}
		if !strings.Contains(got, "phase_effort_missing") {
			t.Errorf("%s prompt does not name the phase_effort_missing lint", name)
		}
	}
}
