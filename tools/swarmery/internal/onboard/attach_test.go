package onboard

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// buildManagedProject lays out a fully onboarded project dir: managed
// settings.json, a real project.json with packs, and deployed statusline
// scripts — the state a full Detach tears down and Attach must rebuild.
func buildManagedProject(t *testing.T) (dir string) {
	t.Helper()
	dir = t.TempDir()
	writeTestSettings(t, dir, managedSettings)
	claude := filepath.Join(dir, ".claude")
	pj := filepath.Join(claude, "project.json")
	if err := os.WriteFile(pj, []byte(`{"name": "demo", "enabledPacks": ["iot-pack"], "mainApp": "web"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	slDir := filepath.Join(claude, "statusline")
	if err := os.MkdirAll(slDir, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"statusline.sh", "fetch-fable-usage.sh"} {
		if err := os.WriteFile(filepath.Join(slDir, name), []byte("#!/bin/bash\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// statuslineSrc builds a fake plugins/core/statusline source dir.
func statuslineSrc(t *testing.T) string {
	t.Helper()
	src := t.TempDir()
	for _, name := range []string{"statusline.sh", "fetch-fable-usage.sh"} {
		if err := os.WriteFile(filepath.Join(src, name), []byte("#!/bin/bash\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return src
}

// The headline scenario (observed live on 2026-07-16): full detach, then
// attach brings the project back — settings merged, project.json restored from
// its backup (packs included), statusline redeployed, foreign keys intact.
func TestAttachRestoresFullDetachedProject(t *testing.T) {
	dir := buildManagedProject(t)
	pj := filepath.Join(dir, ".claude", "project.json")
	wsRoot := t.TempDir()

	if _, err := Detach(DetachConfig{ProjectDir: dir, Slug: "demo", WorkspaceRoot: "/ws", Full: true}); err != nil {
		t.Fatal(err)
	}

	res, err := Attach(AttachConfig{ProjectDir: dir, WorkspaceRoot: wsRoot, StatuslineSrc: statuslineSrc(t)})
	if err != nil {
		t.Fatalf("attach: %v", err)
	}
	if !res.Attached {
		t.Fatal("want Attached=true")
	}

	s := readSettings(t, filepath.Join(dir, ".claude", "settings.json"))

	// enabledPlugins: core + the pack recorded in project.json.bak, foreign kept.
	ep := s["enabledPlugins"].(map[string]any)
	for _, key := range []string{"core@swarmery", "iot-pack@swarmery", "other@elsewhere"} {
		if v, _ := ep[key].(bool); !v {
			t.Errorf("enabledPlugins[%s] = %v, want true", key, ep[key])
		}
	}

	mk := s["extraKnownMarketplaces"].(map[string]any)
	if _, ok := mk["swarmery"]; !ok {
		t.Error("extraKnownMarketplaces.swarmery not restored")
	}
	if _, ok := mk["elsewhere"]; !ok {
		t.Error("foreign marketplace must be preserved")
	}

	env := s["env"].(map[string]any)
	if env["AGENT_PROJECT"] != "demo" {
		t.Errorf("env.AGENT_PROJECT = %v, want demo (from project.json.bak name)", env["AGENT_PROJECT"])
	}
	if env["AGENT_WORKSPACE_ROOT"] != wsRoot {
		t.Errorf("env.AGENT_WORKSPACE_ROOT = %v, want %s", env["AGENT_WORKSPACE_ROOT"], wsRoot)
	}
	if env["MY_OWN"] != "keep-me" {
		t.Error("user env var MY_OWN must be preserved")
	}

	if _, ok := s["statusLine"].(map[string]any); !ok {
		t.Error("statusLine not restored")
	}

	perms := s["permissions"].(map[string]any)
	dirs := perms["additionalDirectories"].([]any)
	found := map[any]bool{}
	for _, d := range dirs {
		found[d] = true
	}
	if !found[wsRoot] || !found["/some/other/dir"] {
		t.Errorf("additionalDirectories = %v, want both %s and /some/other/dir", dirs, wsRoot)
	}

	// project.json restored verbatim from the backup.
	got, err := os.ReadFile(pj)
	if err != nil {
		t.Fatalf("project.json not restored: %v", err)
	}
	bak, _ := os.ReadFile(pj + ".bak")
	if string(got) != string(bak) {
		t.Error("project.json must be restored verbatim from project.json.bak")
	}

	// statusline redeployed.
	if _, err := os.Stat(filepath.Join(dir, ".claude", "statusline", "statusline.sh")); err != nil {
		t.Error("statusline.sh not redeployed")
	}

	// workspace namespace carved.
	if _, err := os.Stat(filepath.Join(wsRoot, "demo", "workspace", "working")); err != nil {
		t.Error("workspace namespace not carved")
	}
}

// A dir that was never onboarded: attach behaves like a fresh onboard —
// skeleton project.json (slug from the dir name) + full settings.json.
func TestAttachFreshDirActsLikeOnboard(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "my-app")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	wsRoot := t.TempDir()

	res, err := Attach(AttachConfig{ProjectDir: dir, WorkspaceRoot: wsRoot})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Attached {
		t.Fatal("want Attached=true")
	}

	s := readSettings(t, filepath.Join(dir, ".claude", "settings.json"))
	if v, _ := s["enabledPlugins"].(map[string]any)["core@swarmery"].(bool); !v {
		t.Error("core@swarmery not enabled")
	}
	if s["env"].(map[string]any)["AGENT_PROJECT"] != "my-app" {
		t.Errorf("AGENT_PROJECT = %v, want my-app (derived from dir name)", s["env"].(map[string]any)["AGENT_PROJECT"])
	}

	pj := readSettings(t, filepath.Join(dir, ".claude", "project.json"))
	if pj["name"] != "my-app" {
		t.Errorf("project.json name = %v, want my-app", pj["name"])
	}
}

func TestAttachIdempotent(t *testing.T) {
	dir := buildManagedProject(t)
	wsRoot := t.TempDir()
	if _, err := Detach(DetachConfig{ProjectDir: dir, Slug: "demo", WorkspaceRoot: "/ws", Full: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := Attach(AttachConfig{ProjectDir: dir, WorkspaceRoot: wsRoot}); err != nil {
		t.Fatal(err)
	}

	path := filepath.Join(dir, ".claude", "settings.json")
	before, _ := os.ReadFile(path)
	if err := os.Remove(path + ".bak"); err != nil {
		t.Fatal(err)
	}

	res, err := Attach(AttachConfig{ProjectDir: dir, WorkspaceRoot: wsRoot})
	if err != nil {
		t.Fatal(err)
	}
	if res.Attached {
		t.Errorf("second attach should find nothing to do (steps: %v)", res.Steps)
	}
	after, _ := os.ReadFile(path)
	if string(before) != string(after) {
		t.Error("second attach must not rewrite settings.json")
	}
	if _, err := os.Stat(path + ".bak"); !os.IsNotExist(err) {
		t.Error("second attach must not write a backup")
	}
}

// Foreign values are never clobbered: a different AGENT_PROJECT survives with a
// "!" warning step, and a custom statusLine survives silently — with no
// swarmery statusline deployed or requested there is no conflict to flag.
func TestAttachPreservesForeignValues(t *testing.T) {
	dir := t.TempDir()
	writeTestSettings(t, dir, `{
	  "env": {"AGENT_PROJECT": "someone-elses"},
	  "statusLine": {"type": "command", "command": "my-own-statusline"}
	}`)
	pj := filepath.Join(dir, ".claude", "project.json")
	if err := os.WriteFile(pj, []byte(`{"name": "demo"}`), 0o644); err != nil {
		t.Fatal(err)
	}

	res, err := Attach(AttachConfig{ProjectDir: dir, WorkspaceRoot: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}

	s := readSettings(t, filepath.Join(dir, ".claude", "settings.json"))
	if s["env"].(map[string]any)["AGENT_PROJECT"] != "someone-elses" {
		t.Error("foreign env.AGENT_PROJECT must be preserved")
	}
	if s["statusLine"].(map[string]any)["command"] != "my-own-statusline" {
		t.Error("foreign statusLine must be preserved")
	}
	envWarned, slWarned := false, false
	for _, st := range res.Steps {
		if strings.HasPrefix(st, "! ") {
			if strings.Contains(st, "AGENT_PROJECT") {
				envWarned = true
			}
			if strings.Contains(st, "statusLine") {
				slWarned = true
			}
		}
	}
	if !envWarned {
		t.Errorf("want a warning step for the AGENT_PROJECT conflict (steps: %v)", res.Steps)
	}
	if slWarned {
		t.Errorf("statusLine warning without a swarmery statusline in play (steps: %v)", res.Steps)
	}
	// The rest is still merged in.
	if v, _ := s["enabledPlugins"].(map[string]any)["core@swarmery"].(bool); !v {
		t.Error("core@swarmery must still be enabled despite conflicts elsewhere")
	}
}

// The statusline stays opt-in through attach: no wiring is added from scratch,
// deployed scripts get their missing key back, and a foreign statusLine that
// blocks a requested deploy is flagged.
func TestAttachStatuslineOptIn(t *testing.T) {
	t.Run("never installed: no wiring added", func(t *testing.T) {
		dir := t.TempDir()
		writeTestSettings(t, dir, `{"env": {"AGENT_PROJECT": "demo"}}`)
		if _, err := Attach(AttachConfig{ProjectDir: dir, WorkspaceRoot: t.TempDir()}); err != nil {
			t.Fatal(err)
		}
		s := readSettings(t, filepath.Join(dir, ".claude", "settings.json"))
		if _, ok := s["statusLine"]; ok {
			t.Error("statusLine wired for a project that never opted in")
		}
		if _, err := os.Stat(filepath.Join(dir, ".claude", "statusline")); !os.IsNotExist(err) {
			t.Error("statusline scripts deployed without opt-in")
		}
	})

	t.Run("scripts deployed: missing key restored", func(t *testing.T) {
		dir := buildManagedProject(t) // deployed scripts + managed settings
		// Simulate a half-detached state: key gone, scripts still there.
		if _, err := Detach(DetachConfig{ProjectDir: dir, Slug: "demo", WorkspaceRoot: "/ws"}); err != nil {
			t.Fatal(err)
		}
		if _, err := Attach(AttachConfig{ProjectDir: dir, WorkspaceRoot: t.TempDir()}); err != nil {
			t.Fatal(err)
		}
		s := readSettings(t, filepath.Join(dir, ".claude", "settings.json"))
		sl, ok := s["statusLine"].(map[string]any)
		if !ok {
			t.Fatal("statusLine key not restored for deployed scripts")
		}
		if cmd, _ := sl["command"].(string); !strings.Contains(cmd, "statusline/statusline.sh") {
			t.Errorf("statusLine.command = %v, want the deployed script", sl["command"])
		}
	})

	t.Run("requested deploy blocked by foreign key: warned", func(t *testing.T) {
		dir := t.TempDir()
		writeTestSettings(t, dir, `{"statusLine": {"type": "command", "command": "my-own-statusline"}}`)
		res, err := Attach(AttachConfig{ProjectDir: dir, WorkspaceRoot: t.TempDir(), StatuslineSrc: statuslineSrc(t)})
		if err != nil {
			t.Fatal(err)
		}
		warned := false
		for _, st := range res.Steps {
			if strings.HasPrefix(st, "! ") && strings.Contains(st, "statusLine") {
				warned = true
			}
		}
		if !warned {
			t.Errorf("want a statusLine conflict warning (steps: %v)", res.Steps)
		}
		s := readSettings(t, filepath.Join(dir, ".claude", "settings.json"))
		if s["statusLine"].(map[string]any)["command"] != "my-own-statusline" {
			t.Error("foreign statusLine must be preserved")
		}
	})
}

// An existing project.json always wins over a stale backup — attach must not
// guess which one the user wants.
func TestAttachKeepsExistingProjectJSON(t *testing.T) {
	dir := t.TempDir()
	claude := filepath.Join(dir, ".claude")
	if err := os.MkdirAll(claude, 0o755); err != nil {
		t.Fatal(err)
	}
	current := `{"name": "demo"}`
	if err := os.WriteFile(filepath.Join(claude, "project.json"), []byte(current), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(claude, "project.json.bak"), []byte(`{"name": "older"}`), 0o644); err != nil {
		t.Fatal(err)
	}

	res, err := Attach(AttachConfig{ProjectDir: dir, WorkspaceRoot: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(filepath.Join(claude, "project.json"))
	if string(got) != current {
		t.Error("existing project.json must be left untouched when a .bak is present")
	}
	noted := false
	for _, st := range res.Steps {
		if strings.Contains(st, "project.json.bak") {
			noted = true
		}
	}
	if !noted {
		t.Errorf("want an informational step about the untouched backup (steps: %v)", res.Steps)
	}
}

func TestAttachDryRunTouchesNothing(t *testing.T) {
	dir := buildManagedProject(t)
	if _, err := Detach(DetachConfig{ProjectDir: dir, Slug: "demo", WorkspaceRoot: "/ws", Full: true}); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, ".claude", "settings.json")
	before, _ := os.ReadFile(path)
	wsRoot := t.TempDir()

	res, err := Attach(AttachConfig{ProjectDir: dir, WorkspaceRoot: wsRoot, DryRun: true})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Attached {
		t.Error("dry run should still report Attached=true (there WAS something to do)")
	}
	after, _ := os.ReadFile(path)
	if string(before) != string(after) {
		t.Error("dry run must not modify settings.json")
	}
	if _, err := os.Stat(filepath.Join(dir, ".claude", "project.json")); !os.IsNotExist(err) {
		t.Error("dry run must not restore project.json")
	}
	if _, err := os.Stat(filepath.Join(wsRoot, "demo")); !os.IsNotExist(err) {
		t.Error("dry run must not carve the workspace")
	}
}

func TestAttachRequiredFields(t *testing.T) {
	if _, err := Attach(AttachConfig{WorkspaceRoot: "/ws"}); err == nil {
		t.Error("missing ProjectDir must error")
	}
	if _, err := Attach(AttachConfig{ProjectDir: t.TempDir()}); err == nil {
		t.Error("missing WorkspaceRoot must error")
	}
}
