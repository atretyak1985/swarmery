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

	project := readJSON(t, filepath.Join(cfg.ProjectDir, ".claude", "project.json"))
	if project["name"] != "acme-app" {
		t.Errorf("project name = %v, want acme-app", project["name"])
	}
	packs, ok := project["enabledPacks"].([]any)
	if !ok || len(packs) != 1 || packs[0] != "web-pack" {
		t.Errorf("enabledPacks = %#v, want [web-pack]", project["enabledPacks"])
	}

	// Workspace namespace tree.
	for _, sub := range []string{"wiki", "workspace/plans", "workspace/sessions", "workspace/metrics"} {
		p := filepath.Join(cfg.WorkspaceRoot, cfg.Slug, sub)
		if info, err := os.Stat(p); err != nil || !info.IsDir() {
			t.Errorf("expected workspace dir %s", p)
		}
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

	res, err := Run(cfg)
	if err != nil {
		t.Fatalf("second Run: %v", err)
	}
	got, _ := os.ReadFile(settingsPath)
	if string(got) != string(sentinel) {
		t.Errorf("second Run overwrote existing settings.json: %s", got)
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
