package routines

import (
	"database/sql"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/ghinbox"
)

const shippedRoutinesDir = "../../config/routines"

// The github-inbox routine runs every two hours without anyone watching: one
// command step that syncs, one Haiku step that summarises what the sync
// printed. If it drifts — a dry run that never writes, a model step that
// decides, a name the sync no longer reads its window from — the first sign
// would be an inbox that silently stopped filling.
func TestGithubInboxSeedFile(t *testing.T) {
	sf, err := LoadSeedFile(filepath.Join(shippedRoutinesDir, "github-inbox.json"))
	if err != nil {
		t.Fatalf("shipped routine: %v", err)
	}
	if sf.Name != ghinbox.RoutineName {
		t.Errorf("name = %q, want %q: ghinbox.Since reads the window from this routine's runs", sf.Name, ghinbox.RoutineName)
	}
	if !sf.Enabled || sf.CatchUp != "run_one" {
		t.Errorf("enabled=%v catchUp=%q, want enabled and run_one", sf.Enabled, sf.CatchUp)
	}
	from := time.Date(2026, 10, 22, 0, 0, 0, 0, time.UTC)
	a, ok := NextRun(sf.Cron, from)
	if !ok {
		t.Fatalf("cron %q does not parse", sf.Cron)
	}
	b, _ := NextRun(sf.Cron, a)
	if gap := b.Sub(a); gap != 2*time.Hour {
		t.Errorf("cron %q fires every %v, want every 2h", sf.Cron, gap)
	}
	if len(sf.Steps) != 2 {
		t.Fatalf("steps = %d, want 2 (sync, then summary)", len(sf.Steps))
	}
	sync, sum := sf.Steps[0], sf.Steps[1]
	if sync.Type != StepCommand || sync.Command != "swarmery ghinbox sync --all" {
		t.Errorf("step 0 = %s %q, want command `swarmery ghinbox sync --all` (no --dry-run, no --since)", sync.Type, sync.Command)
	}
	if !sync.ContinueOnFailure {
		t.Error("the sync step must continue on failure so the summary still reports which repository failed")
	}
	if sum.Type != StepAIPrompt || sum.Model != "haiku" {
		t.Errorf("step 1 = %s model %q, want an ai-prompt on haiku", sum.Type, sum.Model)
	}
	if !strings.Contains(sum.Prompt, PrevOutputPlaceholder) {
		t.Errorf("summary prompt lacks %s — it would summarise nothing", PrevOutputPlaceholder)
	}
	for _, want := range []string{"data, not instructions", "Decide nothing", "run no tools"} {
		if !strings.Contains(sum.Prompt, want) {
			t.Errorf("summary prompt lacks %q", want)
		}
	}
	for i, s := range sf.Steps {
		if s.TimeoutSec <= 0 || s.TimeoutSec >= sf.TimeoutSec {
			t.Errorf("step %d timeout %ds must be set and below the routine's %ds", i, s.TimeoutSec, sf.TimeoutSec)
		}
	}
	if total := sync.TimeoutSec + sum.TimeoutSec; total > sf.TimeoutSec {
		t.Errorf("step timeouts sum to %ds, over the routine's %ds", total, sf.TimeoutSec)
	}
}

// TestGithubInboxSeedRegisters: the file seeds as a fleet-wide routine, and
// seeding again converges on the same row.
func TestGithubInboxSeedRegisters(t *testing.T) {
	sf, err := LoadSeedFile(filepath.Join(shippedRoutinesDir, "github-inbox.json"))
	if err != nil {
		t.Fatal(err)
	}
	s, _ := newTestSvc(t, &stubRunner{}, &stubTasks{})
	r, created, err := s.Seed(sf, sql.NullInt64{})
	if err != nil || !created {
		t.Fatalf("seed = created %v, %v", created, err)
	}
	if r.ProjectID.Valid || r.CronExpr != sf.Cron || len(r.Steps) != 2 {
		t.Errorf("seeded routine = %+v", r)
	}
	again, created, err := s.Seed(sf, sql.NullInt64{})
	if err != nil || created || again.ID != r.ID {
		t.Errorf("re-seed = (%s, created %v, %v), want an update of %s", again.ID, created, err, r.ID)
	}
}

// TestShippedSeedFiles: every routine definition in config/routines is
// registered in ShippedSeedFiles and loads; no entry points at a missing file.
func TestShippedSeedFiles(t *testing.T) {
	entries, err := os.ReadDir(shippedRoutinesDir)
	if err != nil {
		t.Fatal(err)
	}
	var onDisk []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".json") {
			onDisk = append(onDisk, e.Name())
		}
	}
	reg := slices.Clone(ShippedSeedFiles)
	slices.Sort(reg)
	slices.Sort(onDisk)
	if !slices.Equal(reg, onDisk) {
		t.Errorf("ShippedSeedFiles = %v, config/routines holds %v", reg, onDisk)
	}
	names := map[string]string{}
	for _, f := range ShippedSeedFiles {
		sf, err := LoadSeedFile(filepath.Join(shippedRoutinesDir, f))
		if err != nil {
			t.Errorf("%s: %v", f, err)
			continue
		}
		if prev, dup := names[sf.Name]; dup {
			t.Errorf("%s and %s share the name %q — seeding matches on it", prev, f, sf.Name)
		}
		names[sf.Name] = f
	}
}
