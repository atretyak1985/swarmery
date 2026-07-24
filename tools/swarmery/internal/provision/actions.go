package provision

import (
	"encoding/json"
	"os"
	"path/filepath"
	"time"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/githead"
)

// GenerateAction is a pack's optional post-install artifact step.
type GenerateAction struct {
	Prompt  string                // fed to `claude -p`
	Timeout time.Duration         // generate cap
	Fresh   func(dir string) bool // true → artifact already current, skip generate
}

// archMapPrompt is the exact wording confirmed by the Phase 0 spike.
const archMapPrompt = "Use the architecture-map skill to generate or refresh this repository's architecture map (architecture-out/architecture-map.json and .html). Run non-interactively and do not ask for confirmation."

// defaultActions is the pack→action policy. MVP: architecture-pack only; every
// other pack is install-only.
func defaultActions() map[string]GenerateAction {
	return map[string]GenerateAction{
		"architecture-pack": {
			Prompt:  archMapPrompt,
			Timeout: 40 * time.Minute,
			Fresh:   architectureFresh,
		},
	}
}

// architectureFresh reports HEAD == analyzedAtCommit for the repo at dir.
// Any missing/unreadable/mismatch → not fresh (regenerate).
func architectureFresh(dir string) bool {
	head, ok := githead.Resolve(dir)
	if !ok {
		return false
	}
	raw, err := os.ReadFile(filepath.Join(dir, "architecture-out", "architecture-map.json"))
	if err != nil {
		return false
	}
	var m struct {
		AnalyzedAtCommit string `json:"analyzedAtCommit"`
	}
	if json.Unmarshal(raw, &m) != nil {
		return false
	}
	return m.AnalyzedAtCommit != "" && m.AnalyzedAtCommit == head
}
