package routines

import (
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The weekly memory-review routine is the dated record of the "read your
// memory every Friday" ritual: a lint over every project's auto-memory and a
// consolidation DRY-RUN over the same set. If it drifts (a third step, an
// apply flag, a daily cron), the first sign would be a Friday that moved the
// operator's memory files instead of reporting on them. It is only LOADED
// here, never seeded into a database.
func TestMemoryReviewSeedFile(t *testing.T) {
	sf, err := LoadSeedFile(filepath.Join("..", "..", "config", "routines", "memory-review.json"))
	if err != nil {
		t.Fatalf("shipped routine: %v", err)
	}
	if sf.Name != "memory-review-weekly" {
		t.Errorf("name = %q, want memory-review-weekly: seeding matches on it", sf.Name)
	}
	if sf.Cron != "0 9 * * 5" {
		t.Errorf("cron = %q, want \"0 9 * * 5\" (Friday 09:00)", sf.Cron)
	}
	if _, ok := NextRun(sf.Cron, time.Now().UTC()); !ok {
		t.Errorf("cron %q does not parse", sf.Cron)
	}
	if sf.CatchUp != "run_one" {
		t.Errorf("catchUp = %q, want run_one: a machine asleep on Friday morning must run once on wake", sf.CatchUp)
	}
	if len(sf.Steps) != 2 {
		t.Fatalf("steps = %d, want exactly 2 (lint, then consolidate dry-run)", len(sf.Steps))
	}
	wantCommands := []string{"swarmery memory lint --all", "swarmery memory consolidate --all"}
	for i, s := range sf.Steps {
		if s.Type != StepCommand {
			t.Errorf("step %d type = %q, want %q", i, s.Type, StepCommand)
		}
		if s.Command != wantCommands[i] {
			t.Errorf("step %d command = %q, want %q", i, s.Command, wantCommands[i])
		}
		if strings.Contains(s.Command, "--yes") {
			t.Errorf("step %d command %q applies: the weekly review must only report", i, s.Command)
		}
		if s.TimeoutSec <= 0 || (sf.TimeoutSec > 0 && s.TimeoutSec >= sf.TimeoutSec) {
			t.Errorf("step %d timeout %ds must be set and below the routine's %ds", i, s.TimeoutSec, sf.TimeoutSec)
		}
	}
	// The lint is a report (exit 0 with findings); continueOnFailure covers a
	// crash so the dry-run block still lands in the same run.
	if !sf.Steps[0].ContinueOnFailure {
		t.Errorf("lint step must set continueOnFailure so a lint crash does not hide the consolidate dry-run")
	}
}
