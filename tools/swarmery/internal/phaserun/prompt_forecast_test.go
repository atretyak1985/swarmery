package phaserun

import (
	"strings"
	"testing"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/runcore"
)

// notALimit is the whole mitigation for the one risk a forecast contract
// carries: an agent that has just predicted "S, 30-90m" and then bends the work
// to match its own prediction. The sentence is pinned verbatim, and pinned at
// EXACTLY ONE occurrence — repeating it would read as emphasis on the forecast
// rather than on the freedom, which is the opposite of what it is for.
const notALimit = "It is a prediction, not a limit: do whatever the phase actually needs."

func TestPromptForecastContract(t *testing.T) {
	got := BuildPrompt("plan/phase-2.md", "plan/phase-2.md", "# Phase 2\n\n## Forecast\n")

	if n := strings.Count(got, notALimit); n != 1 {
		t.Errorf("%q appears %d times, want exactly 1", notALimit, n)
	}
	if !strings.Contains(got, "kind: posterior") {
		t.Error("the prompt never names the `kind: posterior` block the executor has to write")
	}
	if !strings.Contains(got, "before your first edit") {
		t.Error("the prompt does not say WHEN to write the forecast — its timing is the whole point")
	}
	if !strings.Contains(got, "Where reality diverged") {
		t.Error("the prompt does not ask for the divergence paragraph in the Completion Report")
	}

	// A forecast is data, never a fence: no wording anywhere may suggest the run
	// is bounded by its own prediction.
	for _, banned := range []string{
		"stay within", "within your forecast", "do not exceed", "must not exceed",
		"limited to your forecast", "budget for this phase is",
	} {
		if strings.Contains(strings.ToLower(got), strings.ToLower(banned)) {
			t.Errorf("prompt contains scope-fence wording %q — a forecast must never gate the work", banned)
		}
	}

	// The forecast bullet must not have disturbed the exactly-once turn contract
	// the whole unattended-run fleet depends on.
	if n := strings.Count(got, "HOW YOUR TURN ENDS"); n != 1 {
		t.Errorf("HOW YOUR TURN ENDS appears %d times, want exactly 1", n)
	}
	if n := strings.Count(BuildPromptIn("d", "d", "x", "", "", runcore.Budget{}), notALimit); n != 1 {
		t.Errorf("BuildPromptIn: forecast sentence appears %d times, want exactly 1", n)
	}
}
