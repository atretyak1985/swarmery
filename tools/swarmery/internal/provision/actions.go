package provision

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/githead"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/plugindrift"
)

// GenerateAction is a pack's optional post-install artifact step.
type GenerateAction struct {
	Prompt  string                // fed to `claude -p`
	Timeout time.Duration         // generate cap
	Fresh   func(dir string) bool // true → artifact already current, skip generate
	// Refresh re-derives the cheap, deterministic part of the artifact from
	// what is already on disk, using the pack version that was JUST installed.
	// It runs after install and BEFORE the freshness gate, so a project whose
	// map is "already current" still gets a viewer rendered by the current
	// template. Errors are logged, never fatal: a stale viewer is a nuisance,
	// a failed job over one is a regression. nil for packs with nothing cheap
	// to refresh.
	Refresh func(ctx context.Context, s *Service, projectPath string) error
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
			Refresh: architectureRefresh,
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

// architectureRefreshTimeout bounds the template-only re-render. The renderer
// is a bash + node script over one JSON file; a second is typical.
const architectureRefreshTimeout = 60 * time.Second

// architectureRefresh re-renders architecture-out/architecture-map.html from
// the existing architecture-map.json with the build script of the
// architecture-pack version installed FOR THIS PROJECT (resolved through the
// account-bound `claude plugin list`, so a project on another Claude account
// uses that account's copy). No LLM, no network.
//
// Why it exists: the viewer HTML is rendered once, by whichever pack version
// built it, and nothing re-rendered it when the pack moved on. A viewer built
// by 1.2.0 carried no highlight listener, so the dashboard's blast-radius
// chips silently did nothing on every project until its next LLM rebuild —
// which the freshness gate skips whenever the map's commit still matches HEAD.
//
// No map JSON → nothing to render, nil. Pack not resolvable, no build script,
// or a render failure → an error the caller logs and moves past.
func architectureRefresh(ctx context.Context, s *Service, projectPath string) error {
	out := filepath.Join(projectPath, "architecture-out")
	jsonPath := filepath.Join(out, "architecture-map.json")
	if fi, err := os.Stat(jsonPath); err != nil || fi.IsDir() {
		return nil
	}
	in, ok, err := plugindrift.ResolveInstalled(ctx, accountRunner{s.Runner, projectPath}, "architecture-pack@swarmery", projectPath)
	if err != nil {
		return fmt.Errorf("resolve installed architecture-pack: %w", err)
	}
	if !ok || in.InstallPath == "" {
		return errors.New("architecture-pack is not installed for this project; viewer not re-rendered")
	}
	build := filepath.Join(in.InstallPath, "skills", "architecture-map", "scripts", "build.sh")
	if _, err := os.Stat(build); err != nil {
		return fmt.Errorf("installed architecture-pack %s has no build script at %s", in.Version, build)
	}
	rctx, cancel := context.WithTimeout(ctx, architectureRefreshTimeout)
	defer cancel()
	cmd := exec.CommandContext(rctx, "bash", build, "--json", jsonPath, "--out", filepath.Join(out, "architecture-map.html"))
	cmd.Dir = projectPath
	if b, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("re-render viewer with architecture-pack %s: %w: %s", in.Version, err, strings.TrimSpace(string(b)))
	}
	return nil
}

// accountRunner adapts the provisioning Runner to plugindrift.Runner while
// pinning every call to projectPath: plugindrift passes "" as the dir, and in
// this package "" means "no account env" — the exact blindness that put a pack
// into ~/.claude for a project whose sessions read ~/.claude-<account>.
type accountRunner struct {
	r           Runner
	projectPath string
}

func (a accountRunner) Run(ctx context.Context, _ string, args ...string) ([]byte, error) {
	out, err := a.r.Claude(ctx, a.projectPath, "", args...)
	return []byte(out), err
}
