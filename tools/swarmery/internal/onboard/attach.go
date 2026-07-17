package onboard

// Attach is the inverse of Detach (including Full): it brings a detached — or
// never-onboarded — project back to managed state. Unlike Run, which refuses to
// touch an existing settings.json, Attach MERGES the swarmery-owned entries
// into it, adding only what is missing and never overwriting a foreign value
// (a conflicting env.AGENT_PROJECT — or a custom statusLine where swarmery's
// would go — is kept and flagged with a "!" warning step). The opt-in
// statusline is touched only when its scripts are already deployed or
// StatuslineSrc is passed. project.json is restored from project.json.bak when
// the file itself is gone; an existing file always wins over the backup. Both
// the CLI (`swarmery attach`) and the control-plane API reuse this.

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// AttachConfig drives a single attach run. Slug and packs are intentionally
// not inputs: they come from the project's (restored) .claude/project.json —
// the record onboarding left behind — falling back to the directory name.
type AttachConfig struct {
	// ProjectDir is the project root holding .claude/. Required.
	ProjectDir string
	// WorkspaceRoot is the shared workspace repo root (env.AGENT_WORKSPACE_ROOT,
	// permissions.additionalDirectories, namespace carving). Required.
	WorkspaceRoot string
	// MarketplaceRepo overrides DefaultMarketplaceRepo when non-empty.
	MarketplaceRepo string
	// StatuslineSrc, when non-empty, is the statusline source dir to deploy
	// from — the explicit opt-in. Without it attach only rewires a statusline
	// whose scripts are already deployed; it never installs one from scratch
	// (the statusline is opt-in, see deployStatusline).
	StatuslineSrc string
	// DryRun reports the plan (Steps) without touching the filesystem.
	DryRun bool
}

// slugSanitizeRe collapses everything a slug may not contain into dashes.
var slugSanitizeRe = regexp.MustCompile(`[^a-z0-9-]+`)

// deriveSlug turns a directory name into a valid onboarding slug
// ("Skygor" → "skygor", "My App" → "my-app"). Empty when nothing survives.
func deriveSlug(name string) string {
	s := slugSanitizeRe.ReplaceAllString(strings.ToLower(name), "-")
	return strings.Trim(s, "-")
}

// canonicalDeny is the generic .env protection onboarding writes; attach adds
// any entry that has gone missing.
var canonicalDeny = []string{
	"Read(./.env)", "Read(./.env.*)", "Read(./secrets/**)",
	"Edit(./.env)", "Edit(./.env.*)",
	"Write(./.env)", "Write(./.env.*)",
}

// Attach re-enables swarmery for a project. Idempotent: on an already-managed
// project it reports Attached=false and writes nothing.
func Attach(cfg AttachConfig) (*Result, error) {
	if cfg.ProjectDir == "" {
		return nil, fmt.Errorf("project dir is required")
	}
	if cfg.WorkspaceRoot == "" {
		return nil, fmt.Errorf("workspace root is required")
	}
	repo := cfg.MarketplaceRepo
	if repo == "" {
		repo = DefaultMarketplaceRepo
	}
	res := &Result{}
	claudeDir := filepath.Join(cfg.ProjectDir, ".claude")

	// ── project.json: keep > restore from .bak > skeleton ────────────────────
	pjPath := filepath.Join(claudeDir, "project.json")
	bakPath := pjPath + ".bak"
	var pjRaw []byte    // content slug/packs are read from (file or backup)
	var pjWrite []byte  // non-nil → restore this content on a real run
	var pjSkeleton bool // no file, no backup → write the onboard skeleton
	if raw, err := os.ReadFile(pjPath); err == nil {
		pjRaw = raw
		if _, err := os.Stat(bakPath); err == nil {
			res.step("• .claude/project.json exists — left untouched (a project.json.bak is also present; restore it manually if that is the one you want)")
		}
	} else if !os.IsNotExist(err) {
		return nil, fmt.Errorf("read %s: %w", pjPath, err)
	} else if raw, err := os.ReadFile(bakPath); err == nil {
		pjRaw, pjWrite = raw, raw
		res.step("+ .claude/project.json restored from project.json.bak")
		res.Attached = true
	} else {
		pjSkeleton = true
		res.step("+ .claude/project.json skeleton (no backup to restore) — FILL IN the TODO fields")
		res.Attached = true
	}

	// ── slug + packs from project.json, falling back to the directory name ───
	slug := ""
	var packs []string
	if pjRaw != nil {
		var pj struct {
			Name         string   `json:"name"`
			EnabledPacks []string `json:"enabledPacks"`
		}
		if err := json.Unmarshal(pjRaw, &pj); err != nil {
			return nil, fmt.Errorf("parse %s: %w", pjPath, err)
		}
		if slugRe.MatchString(pj.Name) {
			slug = pj.Name
		}
		for _, p := range pj.EnabledPacks {
			if knownPack(p) {
				packs = append(packs, p)
			}
		}
		packs = cleanPacks(packs)
	}
	if slug == "" {
		abs, err := filepath.Abs(cfg.ProjectDir)
		if err != nil {
			return nil, fmt.Errorf("invalid project dir: %v", err)
		}
		slug = deriveSlug(filepath.Base(abs))
		if slug == "" {
			return nil, fmt.Errorf("cannot derive a slug from %q — fill in project.json name first", filepath.Base(abs))
		}
	}

	// ── settings.json: merge only the missing swarmery-owned entries ─────────
	sPath := filepath.Join(claudeDir, "settings.json")
	settings := map[string]any{}
	orig, err := os.ReadFile(sPath)
	existed := err == nil
	if existed {
		if err := json.Unmarshal(orig, &settings); err != nil {
			return nil, fmt.Errorf("parse %s: %w", sPath, err)
		}
	} else if !os.IsNotExist(err) {
		return nil, fmt.Errorf("read %s: %w", sPath, err)
	}

	// Statusline evidence, computed up front because it drives both the
	// settings key and the redeploy: attach touches the statusline only when
	// the project shows a prior install (deployed script) or the caller opted
	// in via StatuslineSrc. A never-opted-in project stays statusline-free.
	slDeployed := false
	if _, err := os.Stat(filepath.Join(claudeDir, "statusline", "statusline.sh")); err == nil {
		slDeployed = true
	}
	statuslinePending := !slDeployed && statuslineSrcDir(cfg.StatuslineSrc)

	settingsChanged := false
	add := func(step string) {
		res.step("+ " + step)
		settingsChanged = true
		res.Attached = true
	}
	// section returns the object at settings[key], creating it when absent;
	// ok=false (with a warning) when the key holds a non-object value.
	section := func(key string) (map[string]any, bool) {
		v, present := settings[key]
		if !present {
			m := map[string]any{}
			settings[key] = m
			return m, true
		}
		m, ok := v.(map[string]any)
		if !ok {
			res.step("! " + key + " has an unexpected shape — left untouched")
		}
		return m, ok
	}

	// 1) enabledPlugins: core + the packs project.json records.
	if ep, ok := section("enabledPlugins"); ok {
		for _, name := range append([]string{"core"}, packs...) {
			key := name + marketplaceSuffix
			if on, _ := ep[key].(bool); !on {
				ep[key] = true
				add("enabledPlugins." + key)
			}
		}
	}
	// 2) extraKnownMarketplaces.swarmery.
	if mk, ok := section("extraKnownMarketplaces"); ok {
		if _, present := mk["swarmery"]; !present {
			mk["swarmery"] = map[string]any{
				"source": map[string]any{"source": "github", "repo": repo},
			}
			add("extraKnownMarketplaces.swarmery")
		}
	}
	// 3) env: the two swarmery vars. A conflicting AGENT_PROJECT is a user
	// value — keep it and warn instead of clobbering.
	if env, ok := section("env"); ok {
		if v, present := env["AGENT_PROJECT"]; present {
			if v != slug {
				res.step(fmt.Sprintf("! env.AGENT_PROJECT=%v differs from %q — left untouched", v, slug))
			}
		} else {
			env["AGENT_PROJECT"] = slug
			add("env.AGENT_PROJECT=" + slug)
		}
		if _, present := env["AGENT_WORKSPACE_ROOT"]; !present {
			env["AGENT_WORKSPACE_ROOT"] = cfg.WorkspaceRoot
			add("env.AGENT_WORKSPACE_ROOT")
		}
	}
	// 4) statusLine — only when the scripts are deployed (or about to be); a
	// foreign statusline is never replaced, and a project that never opted in
	// gets no wiring at all.
	if v, present := settings["statusLine"]; present {
		sl, _ := v.(map[string]any)
		if cmd, _ := sl["command"].(string); (slDeployed || statuslinePending) && !strings.Contains(cmd, statuslineMarker) {
			res.step("! statusLine is not swarmery's — left untouched")
		}
	} else if slDeployed || statuslinePending {
		settings["statusLine"] = map[string]any{
			"type":    "command",
			"command": "bash $CLAUDE_PROJECT_DIR/.claude/statusline/statusline.sh",
		}
		add("statusLine")
	}
	// 5) permissions: missing deny entries + the workspace additionalDirectory.
	if perms, ok := section("permissions"); ok {
		deny, denyOK := sliceOrNil(perms, "deny", res)
		if denyOK {
			have := map[any]bool{}
			for _, d := range deny {
				have[d] = true
			}
			added := false
			for _, d := range canonicalDeny {
				if !have[d] {
					deny = append(deny, d)
					added = true
				}
			}
			if added {
				perms["deny"] = deny
				add("permissions.deny (.env protection)")
			}
		}
		dirs, dirsOK := sliceOrNil(perms, "additionalDirectories", res)
		if dirsOK {
			found := false
			for _, d := range dirs {
				if d == cfg.WorkspaceRoot {
					found = true
				}
			}
			if !found {
				perms["additionalDirectories"] = append(dirs, cfg.WorkspaceRoot)
				add("permissions.additionalDirectories[" + cfg.WorkspaceRoot + "]")
			}
		}
	}

	// ── statusline redeploy (planned; executed below) ─────────────────────────
	if statuslinePending {
		res.Attached = true
		if cfg.DryRun {
			res.step("+ .claude/statusline/ (redeploy)")
		}
	}

	if !res.Attached {
		res.step("• nothing to attach — swarmery is already fully enabled")
		return res, nil
	}
	if cfg.DryRun {
		return res, nil
	}

	// ── execute ───────────────────────────────────────────────────────────────
	if err := os.MkdirAll(claudeDir, 0o755); err != nil {
		return nil, fmt.Errorf("mkdir %s: %w", claudeDir, err)
	}
	if pjWrite != nil {
		if err := os.WriteFile(pjPath, pjWrite, 0o644); err != nil {
			return nil, fmt.Errorf("restore %s: %w", pjPath, err)
		}
	}
	if pjSkeleton {
		// writeProject reports its own step; the plan line above already covers
		// it, so route the duplicate into a throwaway Result.
		if err := writeProject(claudeDir, Config{Slug: slug, ProjectDir: cfg.ProjectDir}, packs, &Result{}); err != nil {
			return nil, err
		}
	}
	if settingsChanged {
		if existed {
			if err := os.WriteFile(sPath+".bak", orig, 0o644); err != nil {
				return nil, fmt.Errorf("write backup %s.bak: %w", sPath, err)
			}
			res.step("✓ .claude/settings.json merged (backup: .claude/settings.json.bak)")
		} else {
			res.step("✓ .claude/settings.json written")
		}
		if err := writeJSON(sPath, settings); err != nil {
			return nil, err
		}
	}
	if statuslinePending {
		if err := deployStatusline(claudeDir, cfg.StatuslineSrc, res); err != nil {
			return nil, err
		}
	}
	if err := carveWorkspace(cfg.WorkspaceRoot, slug, res); err != nil {
		return nil, err
	}
	return res, nil
}

// sliceOrNil returns the slice at m[key] (nil when absent); ok=false with a
// warning step when the key holds a non-slice value.
func sliceOrNil(m map[string]any, key string, res *Result) ([]any, bool) {
	v, present := m[key]
	if !present {
		return nil, true
	}
	s, ok := v.([]any)
	if !ok {
		res.step("! permissions." + key + " has an unexpected shape — left untouched")
	}
	return s, ok
}
