// Package onboard is the single source of truth for bootstrapping a new
// swarmery consumer project: it writes the project's .claude/settings.json and
// .claude/project.json skeleton, optionally deploys the statusline scripts, and
// carves the per-project workspace namespace. Both the CLI (`swarmery onboard`)
// and the control-plane API reuse Run so the logic lives in exactly one place —
// scripts/init.sh delegates here when the binary is installed.
//
// It is the Go port of scripts/init.sh and is behaviourally equivalent:
// existing settings.json / project.json are never overwritten (idempotent
// re-runs), the same kebab-case slug rule and known-pack allow-list apply, and
// the same workspace namespace tree is created.
package onboard

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
)

// DefaultMarketplaceRepo is the GitHub marketplace source baked into a new
// project's settings.json (mirrors scripts/init.sh MARKETPLACE_REPO).
const DefaultMarketplaceRepo = "atretyak1985/swarmery"

// KnownPacks is the allow-list of opt-in domain packs a project may enable
// (core is always on). Mirrors the case guard in scripts/init.sh.
var KnownPacks = []string{"uav-pack", "iot-pack", "web-pack", "lsp-pack", "infra-pack"}

var slugRe = regexp.MustCompile(`^[a-z0-9-]+$`)

// Config drives a single onboarding run.
type Config struct {
	// Slug is the kebab-case project identifier (AGENT_PROJECT). Required.
	Slug string
	// ProjectDir is the project root under which .claude/ is written. Required
	// (the CLI defaults it to the working directory).
	ProjectDir string
	// Packs are opt-in domain packs to enable alongside core. Each must be in
	// KnownPacks; empty entries are ignored.
	Packs []string
	// WorkspaceRoot is the shared workspace repo root; the project's namespace
	// is carved under <WorkspaceRoot>/<Slug>/. Required.
	WorkspaceRoot string
	// MarketplaceRepo overrides DefaultMarketplaceRepo when non-empty.
	MarketplaceRepo string
	// StatuslineSrc, when non-empty and pointing at a readable directory, is the
	// plugins/core/statusline source whose *.sh are copied into the project.
	// Empty skips the statusline step (it is opt-in).
	StatuslineSrc string
}

// Result reports what a run did, one human-readable line per step, so callers
// (CLI stdout, API response body) render an identical trace.
type Result struct {
	Steps []string
}

// Validate checks the slug and packs without touching the filesystem, so the
// API layer can reject bad input before any write.
func Validate(cfg Config) error {
	if cfg.Slug == "" {
		return fmt.Errorf("slug is required")
	}
	if !slugRe.MatchString(cfg.Slug) {
		return fmt.Errorf("slug must be kebab-case ([a-z0-9-]): %q", cfg.Slug)
	}
	for _, p := range cfg.Packs {
		if p == "" {
			continue
		}
		if !knownPack(p) {
			return fmt.Errorf("unknown pack: %q (known: %v)", p, KnownPacks)
		}
	}
	if cfg.ProjectDir == "" {
		return fmt.Errorf("project dir is required")
	}
	if cfg.WorkspaceRoot == "" {
		return fmt.Errorf("workspace root is required")
	}
	return nil
}

// Run performs onboarding: settings.json + project.json (both skipped if they
// already exist), optional statusline deploy, and the workspace namespace. It
// is idempotent — re-running only fills what is missing.
func Run(cfg Config) (*Result, error) {
	if err := Validate(cfg); err != nil {
		return nil, err
	}
	packs := cleanPacks(cfg.Packs)
	repo := cfg.MarketplaceRepo
	if repo == "" {
		repo = DefaultMarketplaceRepo
	}
	res := &Result{}

	claudeDir := filepath.Join(cfg.ProjectDir, ".claude")
	if err := os.MkdirAll(claudeDir, 0o755); err != nil {
		return nil, fmt.Errorf("mkdir %s: %w", claudeDir, err)
	}

	if err := writeSettings(claudeDir, cfg, repo, packs, res); err != nil {
		return nil, err
	}
	if err := writeProject(claudeDir, cfg, packs, res); err != nil {
		return nil, err
	}
	if err := deployStatusline(claudeDir, cfg.StatuslineSrc, res); err != nil {
		return nil, err
	}
	if err := carveWorkspace(cfg.WorkspaceRoot, cfg.Slug, res); err != nil {
		return nil, err
	}
	return res, nil
}

func writeSettings(claudeDir string, cfg Config, repo string, packs []string, res *Result) error {
	path := filepath.Join(claudeDir, "settings.json")
	if _, err := os.Stat(path); err == nil {
		res.step("• .claude/settings.json exists — not touching it (merge manually if needed)")
		return nil
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("stat %s: %w", path, err)
	}

	enabled := map[string]bool{"core@swarmery": true}
	for _, p := range packs {
		enabled[p+"@swarmery"] = true
	}
	settings := map[string]any{
		"extraKnownMarketplaces": map[string]any{
			"swarmery": map[string]any{
				"source": map[string]any{"source": "github", "repo": repo},
			},
		},
		"enabledPlugins": enabled,
		"env": map[string]any{
			"AGENT_PROJECT":        cfg.Slug,
			"AGENT_WORKSPACE_ROOT": cfg.WorkspaceRoot,
		},
		"statusLine": map[string]any{
			"type":    "command",
			"command": "bash $CLAUDE_PROJECT_DIR/.claude/statusline/statusline.sh",
		},
		"permissions": map[string]any{
			"deny": []string{
				"Read(./.env)", "Read(./.env.*)", "Read(./secrets/**)",
				"Edit(./.env)", "Edit(./.env.*)",
				"Write(./.env)", "Write(./.env.*)",
			},
			"additionalDirectories": []string{cfg.WorkspaceRoot},
		},
	}
	if err := writeJSON(path, settings); err != nil {
		return err
	}
	if len(packs) > 0 {
		res.step(fmt.Sprintf("✓ .claude/settings.json (core + %v)", packs))
	} else {
		res.step("✓ .claude/settings.json (core)")
	}
	return nil
}

func writeProject(claudeDir string, cfg Config, packs []string, res *Result) error {
	path := filepath.Join(claudeDir, "project.json")
	if _, err := os.Stat(path); err == nil {
		res.step("• .claude/project.json exists — not touching it")
		return nil
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("stat %s: %w", path, err)
	}

	if packs == nil {
		packs = []string{}
	}
	project := map[string]any{
		"name":         cfg.Slug,
		"displayName":  cfg.Slug,
		"codePath":     cfg.ProjectDir,
		"mainApp":      "TODO-main-app",
		"apps":         []string{},
		"repos":        []string{},
		"domainTerms":  map[string]any{"product": "TODO: one line about the product"},
		"stack":        map[string]any{},
		"commitScopes": []string{},
		"enabledPacks": packs,
	}
	if err := writeJSON(path, project); err != nil {
		return err
	}
	res.step("✓ .claude/project.json — FILL IN the TODO fields (agents read this at runtime)")
	return nil
}

// deployStatusline copies statusline.sh + fetch-fable-usage.sh from src into the
// project's .claude/statusline/, matching scripts/init.sh: opt-in, skipped when
// src is unset/absent or the destination script already exists.
func deployStatusline(claudeDir, src string, res *Result) error {
	if src == "" {
		return nil
	}
	if info, err := os.Stat(src); err != nil || !info.IsDir() {
		return nil // absent source is not an error — statusline is optional
	}
	dstDir := filepath.Join(claudeDir, "statusline")
	if _, err := os.Stat(filepath.Join(dstDir, "statusline.sh")); err == nil {
		return nil // already deployed
	}
	if err := os.MkdirAll(dstDir, 0o755); err != nil {
		return fmt.Errorf("mkdir %s: %w", dstDir, err)
	}
	for _, name := range []string{"statusline.sh", "fetch-fable-usage.sh"} {
		data, err := os.ReadFile(filepath.Join(src, name))
		if err != nil {
			continue // best-effort, like init.sh's `|| true`
		}
		if err := os.WriteFile(filepath.Join(dstDir, name), data, 0o755); err != nil {
			return fmt.Errorf("write %s: %w", name, err)
		}
	}
	res.step("✓ .claude/statusline/ (opt-in Fable usage: export SWARMERY_STATUSLINE_FABLE=1)")
	return nil
}

func carveWorkspace(wsRoot, slug string, res *Result) error {
	base := filepath.Join(wsRoot, slug)
	dirs := []string{filepath.Join(base, "wiki")}
	for _, sub := range []string{"working", "archive", "plans", "specs", "sessions", "logs", "metrics"} {
		dirs = append(dirs, filepath.Join(base, "workspace", sub))
	}
	for _, d := range dirs {
		if err := os.MkdirAll(d, 0o755); err != nil {
			return fmt.Errorf("mkdir %s: %w", d, err)
		}
	}
	res.step(fmt.Sprintf("✓ workspace: %s/", base))
	return nil
}

func writeJSON(path string, v any) error {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal %s: %w", path, err)
	}
	data = append(data, '\n')
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	return nil
}

func knownPack(p string) bool {
	for _, k := range KnownPacks {
		if k == p {
			return true
		}
	}
	return false
}

// cleanPacks drops empties and duplicates and returns a sorted, deterministic
// slice so generated JSON is stable across runs.
func cleanPacks(in []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, p := range in {
		if p == "" || seen[p] {
			continue
		}
		seen[p] = true
		out = append(out, p)
	}
	sort.Strings(out)
	return out
}

func (r *Result) step(s string) { r.Steps = append(r.Steps, s) }
