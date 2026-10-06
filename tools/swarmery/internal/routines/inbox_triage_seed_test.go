package routines

import (
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The nightly inbox-triage routine is a seed file nobody runs by hand: if it
// drifts (a second step, another kind, no cap), the first sign would be a
// night that labelled — or spent — more than it should. It is only LOADED
// here, never seeded into a database.
func TestInboxTriageSeedFile(t *testing.T) {
	sf, err := LoadSeedFile(filepath.Join("..", "..", "config", "routines", "inbox-triage.json"))
	if err != nil {
		t.Fatalf("shipped routine: %v", err)
	}
	if _, ok := NextRun(sf.Cron, time.Now().UTC()); !ok {
		t.Errorf("cron %q does not parse", sf.Cron)
	}
	if sf.CatchUp != "run_one" {
		t.Errorf("catchUp = %q, want run_one: a machine asleep at 03:30 must run once on wake", sf.CatchUp)
	}
	if len(sf.Steps) != 1 {
		t.Fatalf("steps = %d, want exactly 1", len(sf.Steps))
	}
	s := sf.Steps[0]
	if s.Type != StepCommand {
		t.Errorf("step type = %q, want %q", s.Type, StepCommand)
	}
	for _, want := range []string{"swarmery triage run", "--kinds classifier", "--cap", "--wait"} {
		if !strings.Contains(s.Command, want) {
			t.Errorf("command %q lacks %q", s.Command, want)
		}
	}
	if s.TimeoutSec <= 0 || (sf.TimeoutSec > 0 && s.TimeoutSec >= sf.TimeoutSec) {
		t.Errorf("step timeout %ds must be set and below the routine's %ds", s.TimeoutSec, sf.TimeoutSec)
	}
}
