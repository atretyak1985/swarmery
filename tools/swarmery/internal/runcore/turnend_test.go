package runcore

import (
	"strings"
	"testing"
	"time"
)

func TestBudgetLine(t *testing.T) {
	started := time.Date(2026, 9, 23, 10, 4, 11, 0, time.UTC)
	b := Budget{Timeout: 4 * time.Hour, Started: started}
	if got, want := b.Line(), "Budget: 4h0m0s; started 2026-09-23T10:04:11Z"; got != want {
		t.Errorf("Line() = %q, want %q", got, want)
	}
	if got := (Budget{}).Line(); got != "" {
		t.Errorf("an unknown budget must render nothing, got %q", got)
	}
	if got := (Budget{Timeout: time.Hour}).Line(); got != "Budget: 1h0m0s" {
		t.Errorf("a half-known budget must print the half it knows, got %q", got)
	}
}

// TestBudgetLineIsUTC: run_started_at is RFC3339-Z everywhere in this daemon, and
// a prompt that quoted a local-zone instant would disagree with the row beside it.
func TestBudgetLineIsUTC(t *testing.T) {
	zone := time.FixedZone("UTC+5", 5*60*60)
	b := Budget{Timeout: time.Hour, Started: time.Date(2026, 9, 23, 15, 0, 0, 0, zone)}
	if !strings.HasSuffix(b.Line(), "started 2026-09-23T10:00:00Z") {
		t.Errorf("Line() = %q, want the instant normalised to UTC", b.Line())
	}
}

func TestBudgetElapsed(t *testing.T) {
	started := time.Date(2026, 9, 23, 10, 0, 0, 0, time.UTC)
	b := Budget{Timeout: time.Hour, Started: started}
	if got := b.Elapsed(started.Add(7 * time.Minute)); got != 7*time.Minute {
		t.Errorf("Elapsed = %v, want 7m", got)
	}
	if got := (Budget{}).Elapsed(started); got != 0 {
		t.Errorf("Elapsed with no start = %v, want 0", got)
	}
}

// TestTurnContractAppearsExactlyOnce is the acceptance criterion for step 3.4/3.5
// at the primitive level; each engine's prompt test re-asserts it on the rendered
// prompt.
func TestTurnContractAppearsExactlyOnce(t *testing.T) {
	b := Budget{Timeout: 4 * time.Hour, Started: time.Date(2026, 9, 23, 10, 0, 0, 0, time.UTC)}
	got := TurnContract(b)

	if n := strings.Count(got, "HOW YOUR TURN ENDS"); n != 1 {
		t.Errorf("standing instruction appears %d times, want 1", n)
	}
	if n := strings.Count(got, "Budget: "); n != 1 {
		t.Errorf("budget line appears %d times, want 1", n)
	}
	// The instruction must not be read as permission to push/merge/install.
	for _, want := range []string{"no push", "no PR", "no merge", "no package install"} {
		if !strings.Contains(got, want) {
			t.Errorf("standing instruction dropped the %q fence:\n%s", want, got)
		}
	}
	// And it must state the harness's own behaviour, so the model knows a
	// text-only stop will be resumed rather than accepted.
	if !strings.Contains(got, "resumed automatically, at most twice") {
		t.Errorf("standing instruction does not state the continuation cap:\n%s", got)
	}
}

func TestTurnContractWithoutABudgetOmitsTheLine(t *testing.T) {
	got := TurnContract(Budget{})
	if strings.Contains(got, "Budget:") {
		t.Errorf("a zero budget must render no budget line:\n%s", got)
	}
	if !strings.Contains(got, "HOW YOUR TURN ENDS") {
		t.Error("the standing instruction is unconditional and must still be present")
	}
}
