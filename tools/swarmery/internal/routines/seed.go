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

// ShippedSeedFiles is the registry of routine definitions under
// config/routines, by file name. Each one is installed with `swarmery routine
// seed --from config/routines/<file>`; TestShippedSeedFiles loads every entry
// and fails on a file in that directory that is missing here, or an entry whose
// file is gone — a definition nobody registered is one nobody seeds.
var ShippedSeedFiles = []string{
	"github-inbox.json",
	"inbox-triage.json",
	"memory-review.json",
	"model-upgrade.json",
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
