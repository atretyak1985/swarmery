package onboard

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func baseConfig(t *testing.T) Config {
	t.Helper()
	root := t.TempDir()
	return Config{
		Slug:          "acme-app",
		ProjectDir:    filepath.Join(root, "project"),
		WorkspaceRoot: filepath.Join(root, "workspace"),
	}
}

func readJSON(t *testing.T, path string) map[string]any {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	var out map[string]any
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatalf("invalid JSON in %s: %v", path, err)
	}
	return out
}

func TestRunCreatesSettingsProjectAndWorkspace(t *testing.T) {
	cfg := baseConfig(t)
	cfg.Packs = []string{"web-pack"}

	res, err := Run(cfg)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(res.Steps) == 0 {
		t.Fatal("expected step log, got none")
	}

	settings := readJSON(t, filepath.Join(cfg.ProjectDir, ".claude", "settings.json"))
	enabled, ok := settings["enabledPlugins"].(map[string]any)
	if !ok {
		t.Fatalf("enabledPlugins missing/wrong type: %#v", settings["enabledPlugins"])
	}
	if enabled["core@swarmery"] != true {
		t.Error("core@swarmery not enabled")
	}
	if enabled["web-pack@swarmery"] != true {
		t.Error("web-pack@swarmery not enabled")
	}
	env := settings["env"].(map[string]any)
	if env["AGENT_PROJECT"] != "acme-app" {
		t.Errorf("AGENT_PROJECT = %v, want acme-app", env["AGENT_PROJECT"])
	}
	if env["AGENT_WORKSPACE_ROOT"] != cfg.WorkspaceRoot {
		t.Errorf("AGENT_WORKSPACE_ROOT = %v, want %v", env["AGENT_WORKSPACE_ROOT"], cfg.WorkspaceRoot)
	}

	// The statusline is opt-in: a default onboard must neither wire the
	// settings key nor deploy the scripts.
	if _, ok := settings["statusLine"]; ok {
		t.Error("statusLine wired without --statusline-src (must be opt-in)")
	}
	if _, err := os.Stat(filepath.Join(cfg.ProjectDir, ".claude", "statusline")); !os.IsNotExist(err) {
		t.Error(".claude/statusline deployed without --statusline-src (must be opt-in)")
	}

	project := readJSON(t, filepath.Join(cfg.ProjectDir, ".claude", "project.json"))
	if project["name"] != "acme-app" {
		t.Errorf("project name = %v, want acme-app", project["name"])
	}
	packs, ok := project["enabledPacks"].([]any)
	if !ok || len(packs) != 1 || packs[0] != "web-pack" {
		t.Errorf("enabledPacks = %#v, want [web-pack]", project["enabledPacks"])
	}

	// Workspace namespace tree.
	for _, sub := range []string{"wiki", "workspace/working", "workspace/archive", "workspace/sessions", "workspace/metrics"} {
		p := filepath.Join(cfg.WorkspaceRoot, cfg.Slug, sub)
		if info, err := os.Stat(p); err != nil || !info.IsDir() {
			t.Errorf("expected workspace dir %s", p)
		}
	}
	// The frozen workspace/plans/ tree must NOT be scaffolded — its presence
	// invited planners to save where the epic scanner never looks (issue #188).
	if _, err := os.Stat(filepath.Join(cfg.WorkspaceRoot, cfg.Slug, "workspace", "plans")); !os.IsNotExist(err) {
		t.Error("workspace/plans must not be scaffolded (frozen tree, issue #188)")
	}

	// overlay/project.json pins the workspace scanner's project binding to the
	// real project directory, so wsingest never falls through to a phantom
	// project row keyed by the workspace path itself (project swarmery,
	// 2026-09-24).
	overlay := readJSON(t, filepath.Join(cfg.WorkspaceRoot, cfg.Slug, "overlay", "project.json"))
	if overlay["codePath"] != cfg.ProjectDir {
		t.Errorf("overlay codePath = %v, want %v", overlay["codePath"], cfg.ProjectDir)
	}
	if overlay["name"] != cfg.Slug {
		t.Errorf("overlay name = %v, want %v", overlay["name"], cfg.Slug)
	}
}

func TestRunIsIdempotentAndNeverOverwrites(t *testing.T) {
	cfg := baseConfig(t)
	if _, err := Run(cfg); err != nil {
		t.Fatalf("first Run: %v", err)
	}

	// Mutate settings.json — a second run must NOT clobber it.
	settingsPath := filepath.Join(cfg.ProjectDir, ".claude", "settings.json")
	sentinel := []byte(`{"hand":"edited"}`)
	if err := os.WriteFile(settingsPath, sentinel, 0o644); err != nil {
		t.Fatalf("write sentinel: %v", err)
	}

	// Same for a hand-tuned overlay/project.json — a re-run must not clobber an
	// operator's deliberate codePath redirect either. A realistic redirect (a
	// different codePath, not just unparseable JSON) is what exercises the
	// actual claim: the redirect survives, not just "the bytes are unrelated".
	overlayPath := filepath.Join(cfg.WorkspaceRoot, cfg.Slug, "overlay", "project.json")
	redirectedPath := filepath.Join(t.TempDir(), "elsewhere")
	overlaySentinel := []byte(`{"name":"acme-app","codePath":"` + redirectedPath + `"}`)
	if err := os.WriteFile(overlayPath, overlaySentinel, 0o644); err != nil {
		t.Fatalf("write overlay sentinel: %v", err)
	}

	res, err := Run(cfg)
	if err != nil {
		t.Fatalf("second Run: %v", err)
	}
	got, _ := os.ReadFile(settingsPath)
	if string(got) != string(sentinel) {
		t.Errorf("second Run overwrote existing settings.json: %s", got)
	}
	gotOverlay, _ := os.ReadFile(overlayPath)
	if string(gotOverlay) != string(overlaySentinel) {
		t.Errorf("second Run overwrote existing overlay/project.json: %s", gotOverlay)
	}
	// A stale pin must be flagged, not silently kept — an operator watching the
	// step log needs to know the workspace is still bound to a dead path.
	var flaggedStale bool
	for _, s := range res.Steps {
		if contains(s, "pins codePath="+redirectedPath) {
			flaggedStale = true
		}
	}
	if !flaggedStale {
		t.Errorf("expected a stale-pin warning step, got %v", res.Steps)
	}
	// The step log should acknowledge the skip.
	var skipped bool
	for _, s := range res.Steps {
		if contains(s, "settings.json exists") {
			skipped = true
		}
	}
	if !skipped {
		t.Errorf("expected skip note in steps, got %v", res.Steps)
	}
}

func TestValidateRejectsBadInput(t *testing.T) {
	cases := []struct {
		name string
		cfg  Config
	}{
		{"empty slug", Config{ProjectDir: "/p", WorkspaceRoot: "/w"}},
		{"non-kebab slug", Config{Slug: "Acme_App", ProjectDir: "/p", WorkspaceRoot: "/w"}},
		{"unknown pack", Config{Slug: "acme", Packs: []string{"nope-pack"}, ProjectDir: "/p", WorkspaceRoot: "/w"}},
		{"missing project dir", Config{Slug: "acme", WorkspaceRoot: "/w"}},
		{"missing workspace root", Config{Slug: "acme", ProjectDir: "/p"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := Validate(tc.cfg); err == nil {
				t.Errorf("expected error for %s", tc.name)
			}
		})
	}
}

// Passing StatuslineSrc is the explicit opt-in: the scripts are deployed AND
// settings.json gains the statusLine wiring pointing at the deployed copy.
func TestRunStatuslineOptIn(t *testing.T) {
	cfg := baseConfig(t)
	cfg.StatuslineSrc = statuslineSrc(t)

	if _, err := Run(cfg); err != nil {
		t.Fatalf("Run: %v", err)
	}

	for _, name := range []string{"statusline.sh", "fetch-fable-usage.sh"} {
		if _, err := os.Stat(filepath.Join(cfg.ProjectDir, ".claude", "statusline", name)); err != nil {
			t.Errorf("%s not deployed: %v", name, err)
		}
	}
	settings := readJSON(t, filepath.Join(cfg.ProjectDir, ".claude", "settings.json"))
	sl, ok := settings["statusLine"].(map[string]any)
	if !ok {
		t.Fatalf("statusLine not wired: %#v", settings["statusLine"])
	}
	if cmd, _ := sl["command"].(string); !contains(cmd, "statusline/statusline.sh") {
		t.Errorf("statusLine.command = %v, want the deployed script", sl["command"])
	}
}

func TestRunEmptyPacksProducesEmptyArray(t *testing.T) {
	cfg := baseConfig(t)
	if _, err := Run(cfg); err != nil {
		t.Fatalf("Run: %v", err)
	}
	project := readJSON(t, filepath.Join(cfg.ProjectDir, ".claude", "project.json"))
	packs, ok := project["enabledPacks"].([]any)
	if !ok {
		t.Fatalf("enabledPacks not an array: %#v", project["enabledPacks"])
	}
	if len(packs) != 0 {
		t.Errorf("enabledPacks = %#v, want []", packs)
	}
}

// carveWorkspace is the only onboarding sink that joins the (per-caller) slug
// onto the workspace root, so it re-fences the result even though Validate
// already rejects bad slugs. A slug that tries to escape must never MkdirAll
// outside the workspace root.
func TestCarveWorkspaceRefusesSlugEscape(t *testing.T) {
	wsRoot := t.TempDir()
	if err := carveWorkspace(wsRoot, "../evil", &Result{}); err == nil {
		t.Fatal("expected carveWorkspace to refuse a traversal slug")
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(wsRoot), "evil")); err == nil {
		t.Fatal("traversal slug carved a directory outside the workspace root")
	}
}

// writeCodePathOverlay carries the same per-sink fence carveWorkspace
// documents — a malformed slug must never place a write outside the
// workspace root.
func TestWriteCodePathOverlayRefusesSlugEscape(t *testing.T) {
	wsRoot := t.TempDir()
	if err := writeCodePathOverlay(wsRoot, "../evil", "/some/project", &Result{}); err == nil {
		t.Fatal("expected writeCodePathOverlay to refuse a traversal slug")
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(wsRoot), "evil")); err == nil {
		t.Fatal("traversal slug wrote outside the workspace root")
	}
}

// The overlay dir can already exist (carveWorkspace's sibling namespace, or a
// prior partial run) without project.json in it — that must still be treated
// as "write it", not "skip: exists".
func TestWriteCodePathOverlayFillsMissingFileInExistingDir(t *testing.T) {
	wsRoot := t.TempDir()
	overlayDir := filepath.Join(wsRoot, "acme-app", "overlay")
	if err := os.MkdirAll(overlayDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := writeCodePathOverlay(wsRoot, "acme-app", "/real/project", &Result{}); err != nil {
		t.Fatalf("writeCodePathOverlay: %v", err)
	}
	overlay := readJSON(t, filepath.Join(overlayDir, "project.json"))
	if overlay["codePath"] != "/real/project" {
		t.Errorf("codePath = %v, want /real/project", overlay["codePath"])
	}
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub || indexOf(s, sub) >= 0)
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
