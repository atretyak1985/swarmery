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
	"strings"
)

// DefaultMarketplaceRepo is the GitHub marketplace source baked into a new
// project's settings.json (mirrors scripts/init.sh MARKETPLACE_REPO).
const DefaultMarketplaceRepo = "atretyak1985/swarmery"

// KnownPacks is the allow-list of opt-in domain packs a project may enable
// (core is always on). Mirrors the case guard in scripts/init.sh.
var KnownPacks = []string{"uav-pack", "iot-pack", "web-pack", "lsp-pack", "infra-pack"}

// marketplaceSuffix tags every swarmery plugin key in enabledPlugins
// (e.g. "core@swarmery") — the marketplace manifest name is "swarmery".
const marketplaceSuffix = "@swarmery"

// statuslineMarker identifies the swarmery-deployed statusLine.command so detach
// removes only our entry, never a user's own statusline.
const statuslineMarker = "statusline/statusline.sh"

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
	// plugins/core/statusline source whose *.sh are copied into the project, and
	// it also wires settings.json statusLine at the deployed copy. Empty (the
	// default — scripts/init.sh never passes it) skips both: the statusline is
	// strictly opt-in via --statusline-src / SWARMERY_STATUSLINE_SRC.
	StatuslineSrc string
}

// Result reports what a run did, one human-readable line per step, so callers
// (CLI stdout, API response body) render an identical trace.
type Result struct {
	Steps []string
	// Detached is set by Detach: true when it removed at least one swarmery-owned
	// entry (and thus wrote the file, unless DryRun). Zero for Run.
	Detached bool
	// Attached is set by Attach: true when it added/restored at least one
	// onboarding artifact (and thus wrote, unless DryRun). Zero for Run/Detach.
	Attached bool
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
		"permissions": map[string]any{
			"deny": []string{
				"Read(./.env)", "Read(./.env.*)", "Read(./secrets/**)",
				"Edit(./.env)", "Edit(./.env.*)",
				"Write(./.env)", "Write(./.env.*)",
			},
			"additionalDirectories": []string{cfg.WorkspaceRoot},
		},
	}
	// statusLine wiring rides with the deployed scripts: only an explicit
	// --statusline-src opts in, so a default onboard never points settings at a
	// script that was not installed.
	if statuslineSrcDir(cfg.StatuslineSrc) {
		settings["statusLine"] = map[string]any{
			"type":    "command",
			"command": "bash $CLAUDE_PROJECT_DIR/.claude/statusline/statusline.sh",
		}
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

// deployStatusline copies statusline.sh + fetch-fable-usage.sh from src into
// the project's .claude/statusline/. Strictly opt-in (scripts/init.sh no longer
// requests it): skipped when src is unset/absent or the destination script
// already exists.
func deployStatusline(claudeDir, src string, res *Result) error {
	if !statuslineSrcDir(src) {
		return nil // absent source is not an error — statusline is opt-in
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
	res.step("✓ .claude/statusline/ (opt-in Fable usage: export SWARMERY_STATUSLINE_FABLE=1; header = account email: export SWARMERY_STATUSLINE_USER=1)")
	return nil
}

// statuslineSrcDir reports whether src names a readable statusline source dir —
// the opt-in signal shared by writeSettings, deployStatusline, and Attach.
func statuslineSrcDir(src string) bool {
	if src == "" {
		return false
	}
	info, err := os.Stat(src)
	return err == nil && info.IsDir()
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

// DetachConfig drives a single detach (offboard) run — the safe inverse of
// Config/Run. It removes ONLY swarmery-owned entries from a project's
// .claude/settings.json; the file is never deleted and unrelated keys are never
// touched. Full additionally removes the other onboarding artifacts
// (.claude/project.json and the deployed statusline scripts).
type DetachConfig struct {
	// ProjectDir is the project root holding .claude/settings.json. Required.
	ProjectDir string
	// Slug guards env pruning: env.AGENT_PROJECT is removed only when it equals
	// Slug, the project.json onboarding slug, or Slug is empty. Prevents
	// clobbering a same-named var a user set.
	Slug string
	// WorkspaceRoot is the fallback additionalDirectories entry to drop when the
	// settings carry no env.AGENT_WORKSPACE_ROOT of their own.
	WorkspaceRoot string
	// Full removes every onboarding artifact, not just the settings entries:
	// .claude/project.json (backed up to project.json.bak) and the swarmery
	// statusline scripts under .claude/statusline/. Project-local components
	// (agents/, skills/, commands/, …) are never touched — they are user-owned.
	Full bool
	// DryRun reports the plan (Steps) without touching the filesystem.
	DryRun bool
}

// Detach removes the swarmery-owned entries from a project's
// .claude/settings.json — the inverse of writeSettings. It is conservative by
// construction: it prunes only the specific keys/values onboarding writes
// (enabledPlugins "<name>@swarmery", extraKnownMarketplaces.swarmery, the two
// AGENT_* env vars, the swarmery statusLine, and the workspace additionalDirectory),
// leaves every other key intact, backs the file up to settings.json.bak before
// writing, and never deletes the file. It is idempotent: a second run finds
// nothing to remove and reports Detached=false. A missing settings.json is a
// no-op, not an error.
//
// Note: the file is re-serialised via encoding/json, which sorts object keys —
// a cosmetic reordering, which is why the original is preserved in .bak.
func Detach(cfg DetachConfig) (*Result, error) {
	res := &Result{}
	path := filepath.Join(cfg.ProjectDir, ".claude", "settings.json")
	orig, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			res.step("• .claude/settings.json not found — nothing to detach")
			return res, nil
		}
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	var settings map[string]any
	if err := json.Unmarshal(orig, &settings); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}

	// The onboarding slug as project.json records it. The API layer only knows
	// the registry slug (path-derived, e.g. "-Volumes-Work-app"), which never
	// matches the AGENT_PROJECT value onboarding wrote — without this the env
	// entry would survive every dashboard-initiated detach.
	projPath := filepath.Join(cfg.ProjectDir, ".claude", "project.json")
	onboardSlug := ""
	if raw, err := os.ReadFile(projPath); err == nil {
		var pj struct {
			Name string `json:"name"`
		}
		if json.Unmarshal(raw, &pj) == nil {
			onboardSlug = pj.Name
		}
	}

	// The workspace root this project actually uses (env is the source of truth,
	// falling back to the caller's default) — used to target the one
	// additionalDirectories entry onboarding added.
	wsRoot := cfg.WorkspaceRoot
	if env, ok := settings["env"].(map[string]any); ok {
		if v, ok := env["AGENT_WORKSPACE_ROOT"].(string); ok && v != "" {
			wsRoot = v
		}
	}

	changed := false
	settingsChanged := false
	mark := func(step string) {
		res.step("- " + step)
		changed = true
		settingsChanged = true
	}

	// 1) enabledPlugins: drop every "<name>@swarmery" key (core + packs).
	if ep, ok := settings["enabledPlugins"].(map[string]any); ok {
		for key := range ep {
			if strings.HasSuffix(key, marketplaceSuffix) {
				delete(ep, key)
				mark("enabledPlugins." + key)
			}
		}
	}
	// 2) extraKnownMarketplaces.swarmery — only that one marketplace entry.
	if mk, ok := settings["extraKnownMarketplaces"].(map[string]any); ok {
		if _, ok := mk["swarmery"]; ok {
			delete(mk, "swarmery")
			mark("extraKnownMarketplaces.swarmery")
		}
	}
	// 3) env: the two swarmery vars only. AGENT_PROJECT is guarded by Slug so a
	// user's unrelated same-named var survives.
	if env, ok := settings["env"].(map[string]any); ok {
		if v, ok := env["AGENT_PROJECT"].(string); ok &&
			(cfg.Slug == "" || v == cfg.Slug || (onboardSlug != "" && v == onboardSlug)) {
			delete(env, "AGENT_PROJECT")
			mark("env.AGENT_PROJECT")
		}
		if _, ok := env["AGENT_WORKSPACE_ROOT"]; ok {
			delete(env, "AGENT_WORKSPACE_ROOT")
			mark("env.AGENT_WORKSPACE_ROOT")
		}
	}
	// 4) statusLine — only when it points at the swarmery statusline script.
	if sl, ok := settings["statusLine"].(map[string]any); ok {
		if cmd, ok := sl["command"].(string); ok && strings.Contains(cmd, statuslineMarker) {
			delete(settings, "statusLine")
			mark("statusLine")
		}
	}
	// 5) permissions.additionalDirectories — drop the workspace-root entry, keep
	// the rest (and the deny list, which is generic .env protection, untouched).
	if perms, ok := settings["permissions"].(map[string]any); ok {
		if dirs, ok := perms["additionalDirectories"].([]any); ok && wsRoot != "" {
			kept := make([]any, 0, len(dirs))
			for _, d := range dirs {
				if s, ok := d.(string); ok && s == wsRoot {
					continue
				}
				kept = append(kept, d)
			}
			if len(kept) != len(dirs) {
				perms["additionalDirectories"] = kept
				mark("permissions.additionalDirectories[" + wsRoot + "]")
			}
		}
	}

	// 6) full offboard: the remaining onboarding artifacts. Planned here (so the
	// dry run lists them), executed after the DryRun gate below. Project-local
	// components (agents/, skills/, commands/, …) are user-owned and never touched.
	var fullOps []func() error
	if cfg.Full {
		if raw, err := os.ReadFile(projPath); err == nil {
			res.step("- .claude/project.json (backup: project.json.bak)")
			changed = true
			fullOps = append(fullOps, func() error {
				if err := os.WriteFile(projPath+".bak", raw, 0o644); err != nil {
					return fmt.Errorf("write backup %s.bak: %w", projPath, err)
				}
				return os.Remove(projPath)
			})
		}
		slDir := filepath.Join(cfg.ProjectDir, ".claude", "statusline")
		for _, name := range []string{"statusline.sh", "fetch-fable-usage.sh"} {
			script := filepath.Join(slDir, name)
			if _, err := os.Stat(script); err == nil {
				res.step("- .claude/statusline/" + name)
				changed = true
				fullOps = append(fullOps, func() error { return os.Remove(script) })
			}
		}
	}

	res.Detached = changed
	if !changed {
		res.step("• no swarmery-owned entries found — nothing to detach")
		return res, nil
	}
	if cfg.DryRun {
		res.step("• dry run — no files written")
		return res, nil
	}

	if settingsChanged {
		// Back up the pre-change file verbatim, then write the pruned settings.
		if err := os.WriteFile(path+".bak", orig, 0o644); err != nil {
			return nil, fmt.Errorf("write backup %s.bak: %w", path, err)
		}
		if err := writeJSON(path, settings); err != nil {
			return nil, err
		}
		res.step("✓ .claude/settings.json rewritten (backup: .claude/settings.json.bak)")
	}
	for _, op := range fullOps {
		if err := op(); err != nil {
			return nil, err
		}
	}
	if cfg.Full && len(fullOps) > 0 {
		// Drop the statusline dir when the removed scripts were its last content
		// (best-effort — a non-empty dir means user files live there, keep it).
		_ = os.Remove(filepath.Join(cfg.ProjectDir, ".claude", "statusline"))
		res.step("✓ onboarding artifacts removed")
	}
	return res, nil
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
