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

// ShippedSeed registers one routine definition shipped under config/routines/.
// The daemon never seeds them by itself; `swarmery routine seed --from
// config/routines/<File>` does, once per machine.
type ShippedSeed struct {
	File string // file name under config/routines/
	Name string // the routine's name: seeding's identity
	// Model is the model every ai-prompt step of the routine must pin; "" = no
	// ai-prompt step, or the runner's DefaultModel.
	Model string
}

// ShippedSeeds is the registry of shipped routines. A file in config/routines/
// that is not listed here — or a listed one that is missing, renamed, or pins a
// different model — fails TestShippedSeedsAreRegistered.
var ShippedSeeds = []ShippedSeed{
	{File: "inbox-triage.json", Name: "inbox-triage-nightly"},
	{File: "memory-review.json", Name: "memory-review-weekly"},
	{File: "model-upgrade.json", Name: "model-upgrade"},
	// Daily: narrows the janitor's keep-unmerged pile to closed phases/cards
	// with a proof and posts the Needs-you card; a haiku step summarises the
	// scan. Never removes anything — removal is the operator's click.
	{File: "worktree-cleanup.json", Name: "worktree-cleanup-daily", Model: "haiku"},
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
