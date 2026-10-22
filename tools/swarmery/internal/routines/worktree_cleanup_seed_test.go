package routines

import (
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The daily worktree-cleanup routine is a seed file nobody runs by hand. Its
// contract: daily, scan → haiku summary → propose, and NOTHING in it removes a
// worktree — removal is the operator's click on the Needs-you card. It is
// loaded here and seeded into a scratch database, never into a real one.
func TestWorktreeCleanupSeedFile(t *testing.T) {
	path := filepath.Join("..", "..", "config", "routines", "worktree-cleanup.json")
	sf, err := LoadSeedFile(path)
	if err != nil {
		t.Fatalf("shipped routine: %v", err)
	}
	if sf.Name != "worktree-cleanup-daily" || !sf.Enabled {
		t.Errorf("name/enabled = %q/%v", sf.Name, sf.Enabled)
	}
	// Daily: a fixed minute and hour, every day of every month.
	fields := strings.Fields(sf.Cron)
	if len(fields) != 5 || fields[2] != "*" || fields[3] != "*" || fields[4] != "*" || strings.ContainsAny(fields[0]+fields[1], "*/,") {
		t.Errorf("cron %q is not a once-a-day schedule", sf.Cron)
	}
	start := time.Date(2026, 10, 22, 12, 0, 0, 0, time.Local)
	first, ok := NextRun(sf.Cron, start)
	if !ok {
		t.Fatalf("cron %q does not parse", sf.Cron)
	}
	if second, _ := NextRun(sf.Cron, first); second.Sub(first) != 24*time.Hour {
		t.Errorf("runs %s then %s: want exactly one a day", first, second)
	}
	if sf.CatchUp != "run_one" {
		t.Errorf("catchUp = %q, want run_one: a machine asleep at 03:30 must run once on wake", sf.CatchUp)
	}

	if len(sf.Steps) != 3 {
		t.Fatalf("steps = %d, want scan → summary → propose", len(sf.Steps))
	}
	scan, summary, propose := sf.Steps[0], sf.Steps[1], sf.Steps[2]
	if scan.Type != StepCommand || !strings.Contains(scan.Command, "swarmery wtcleanup scan --json") || !strings.Contains(scan.Command, "--out") {
		t.Errorf("step 1 = %+v, want the read-only scan writing its JSON report", scan)
	}
	if summary.Type != StepAIPrompt || summary.Model != "haiku" || !summary.ContinueOnFailure {
		t.Errorf("step 2 = %s model %q continueOnFailure %v; want a haiku ai-prompt that cannot stop the propose step",
			summary.Type, summary.Model, summary.ContinueOnFailure)
	}
	if !strings.Contains(summary.Prompt, "last-scan.json") || !strings.Contains(summary.Prompt, "Make no decisions") {
		t.Errorf("summary prompt must read the scan report and decide nothing: %q", summary.Prompt)
	}
	if propose.Type != StepCommand || strings.TrimSpace(propose.Command) != "swarmery wtcleanup propose" {
		t.Errorf("step 3 = %+v, want `swarmery wtcleanup propose`", propose)
	}
	raw, _ := os.ReadFile(path)
	for _, forbidden := range []string{"wtcleanup apply", "worktree remove", "worktrees clean", "branch -D"} {
		if strings.Contains(string(raw), forbidden) {
			t.Errorf("the routine contains %q: removal must stay the operator's click", forbidden)
		}
	}
	for i, s := range sf.Steps {
		if s.TimeoutSec <= 0 || s.TimeoutSec >= sf.TimeoutSec {
			t.Errorf("step %d timeout %ds must be set and below the routine's %ds", i+1, s.TimeoutSec, sf.TimeoutSec)
		}
	}

	// Seeding keeps the pinned model: the executor passes step.Model to the runner.
	svc := NewService(migratedTestDB(t), nil, nil, true)
	r, created, err := svc.Seed(sf, sql.NullInt64{})
	if err != nil || !created {
		t.Fatalf("seed: created=%v err=%v", created, err)
	}
	if len(r.Steps) != 3 || r.Steps[1].Model != "haiku" || r.CronExpr != sf.Cron {
		t.Errorf("seeded routine = cron %q steps %+v", r.CronExpr, r.Steps)
	}
}

// Every shipped routine is registered in ShippedSeeds, loads, carries the
// registered name, and pins the registered model on each ai-prompt step.
func TestShippedSeedsAreRegistered(t *testing.T) {
	dir := filepath.Join("..", "..", "config", "routines")
	files, err := filepath.Glob(filepath.Join(dir, "*.json"))
	if err != nil {
		t.Fatal(err)
	}
	registered := map[string]ShippedSeed{}
	for _, s := range ShippedSeeds {
		registered[s.File] = s
	}
	if len(files) != len(registered) {
		t.Errorf("config/routines has %d files, ShippedSeeds lists %d", len(files), len(registered))
	}
	for _, f := range files {
		reg, ok := registered[filepath.Base(f)]
		if !ok {
			t.Errorf("%s is shipped but not registered in ShippedSeeds", filepath.Base(f))
			continue
		}
		sf, err := LoadSeedFile(f)
		if err != nil {
			t.Errorf("%s: %v", reg.File, err)
			continue
		}
		if sf.Name != reg.Name {
			t.Errorf("%s: name %q, registered as %q", reg.File, sf.Name, reg.Name)
		}
		for _, s := range sf.Steps {
			if s.Type == StepAIPrompt && s.Model != reg.Model {
				t.Errorf("%s step %q: model %q, registered %q", reg.File, s.Name, s.Model, reg.Model)
			}
		}
	}
}
