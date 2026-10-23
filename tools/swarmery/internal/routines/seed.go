package routines

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"strings"
)

// SeedFile is a routine definition shipped in the repo rather than typed into
// the dashboard. A routine that exists only in one machine's SQLite is not
// part of the system — it is a local habit, and local habits are precisely
// what fail to catch drift.
type SeedFile struct {
	Name       string `json:"name"`
	Cron       string `json:"cron"`
	Enabled    bool   `json:"enabled"`
	CatchUp    string `json:"catchUp"`
	TimeoutSec int    `json:"timeoutSec"`
	Steps      []Step `json:"steps"`
}

// ShippedSeedFiles are the routine definitions under config/routines/, by file
// name. `swarmery routine seed --from config/routines/<file>` installs one. The
// seed tests load every file named here and fail on a file missing from it, so
// a shipped routine cannot drift out of the validated set unnoticed.
var ShippedSeedFiles = []string{
	"github-inbox.json",  // every 2h: GitHub issues/PR comments into triage cards
	"inbox-triage.json",  // nightly: label the classifier queue
	"memory-review.json", // Friday: memory lint + consolidation dry-run
	"model-upgrade.json", // monthly: changelog, prompt audit, model eval
	"skill-audit.json",   // Monday: skill audit report (haiku) into the workspace retro/ dir
	// Daily: narrows the janitor's keep-unmerged pile to closed phases/cards with
	// a proof and posts the Needs-you card; a haiku step summarises the scan.
	// Never removes anything — removal is the operator's click.
	"worktree-cleanup.json",
}

// LoadSeedFile reads and validates a definition, including its steps.
func LoadSeedFile(path string) (*SeedFile, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var sf SeedFile
	if err := json.Unmarshal(raw, &sf); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	if strings.TrimSpace(sf.Name) == "" {
		return nil, fmt.Errorf("%s: name is required — it is the identity seeding matches on", path)
	}
	steps, err := ValidateStepSlice(sf.Steps)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	sf.Steps = steps
	return &sf, nil
}

// Seed creates the routine, or updates the existing one with the same name.
//
// Idempotent by NAME, not by id: ids are generated per Create, so matching on
// one would make every seed a new row. Re-running must converge — a routine
// duplicated on each deploy would fire its whole step list several times a
// month, which for this particular routine means several API-spending runs.
//
// Returns the routine and whether it was created (false = updated in place).
func (s *Service) Seed(sf *SeedFile, projectID sql.NullInt64) (Routine, bool, error) {
	existing, err := s.List(0)
	if err != nil {
		return Routine{}, false, err
	}
	for _, r := range existing {
		if r.Name != sf.Name {
			continue
		}
		updated, err := s.Update(r.ID, UpdateParams{
			Name:       &sf.Name,
			CronExpr:   &sf.Cron,
			Enabled:    &sf.Enabled,
			CatchUp:    &sf.CatchUp,
			Steps:      &sf.Steps,
			TimeoutSec: &sf.TimeoutSec,
		})
		return updated, false, err
	}

	created, err := s.Create(CreateParams{
		ProjectID:  projectID,
		Name:       sf.Name,
		CronExpr:   sf.Cron,
		Enabled:    sf.Enabled,
		CatchUp:    sf.CatchUp,
		Steps:      sf.Steps,
		TimeoutSec: sf.TimeoutSec,
	})
	return created, true, err
}
