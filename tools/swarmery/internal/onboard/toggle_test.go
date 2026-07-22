package onboard

import (
	"errors"
	"os"
	"testing"
)

// toggleSettings is a realistic settings.json with foreign keys TogglePlugin
// must preserve: a permissions object with a deny array, an env object, and a
// foreign enabledPlugins entry.
const toggleSettings = `{
  "enabledPlugins": {"core@swarmery": true, "other@elsewhere": true},
  "env": {"MY_OWN": "keep-me"},
  "permissions": {"deny": ["Read(./.env)", "Read(./secrets/**)"]}
}`

func TestTogglePluginEnableAddsKeyPreservesForeign(t *testing.T) {
	dir := t.TempDir()
	path := writeTestSettings(t, dir, toggleSettings)

	res, err := TogglePlugin(dir, "lsp-pack", true)
	if err != nil {
		t.Fatalf("TogglePlugin: %v", err)
	}
	if !res.Changed {
		t.Error("Changed = false, want true")
	}
	if res.Backup != ".claude/settings.json.bak" {
		t.Errorf("Backup = %q, want .claude/settings.json.bak", res.Backup)
	}

	s := readSettings(t, path)
	ep := s["enabledPlugins"].(map[string]any)
	if on, _ := ep["lsp-pack@swarmery"].(bool); !on {
		t.Errorf("enabledPlugins[lsp-pack@swarmery] = %v, want true", ep["lsp-pack@swarmery"])
	}
	for _, key := range []string{"core@swarmery", "other@elsewhere"} {
		if on, _ := ep[key].(bool); !on {
			t.Errorf("enabledPlugins[%s] = %v, want true (foreign/existing keys survive)", key, ep[key])
		}
	}
	env := s["env"].(map[string]any)
	if env["MY_OWN"] != "keep-me" {
		t.Errorf("env.MY_OWN = %v, want keep-me", env["MY_OWN"])
	}
	perms := s["permissions"].(map[string]any)
	deny, ok := perms["deny"].([]any)
	if !ok || len(deny) != 2 || deny[0] != "Read(./.env)" || deny[1] != "Read(./secrets/**)" {
		t.Errorf("permissions.deny = %v, want the two seeded entries", perms["deny"])
	}

	// The backup preserves the pre-edit bytes verbatim.
	bak, err := os.ReadFile(path + ".bak")
	if err != nil {
		t.Fatalf("backup not written: %v", err)
	}
	if string(bak) != toggleSettings {
		t.Errorf("backup = %q, want the original file bytes", bak)
	}
}

func TestTogglePluginEnableCreatesSection(t *testing.T) {
	dir := t.TempDir()
	path := writeTestSettings(t, dir, `{"env": {"MY_OWN": "keep-me"}}`)

	res, err := TogglePlugin(dir, "lsp-pack", true)
	if err != nil {
		t.Fatalf("TogglePlugin: %v", err)
	}
	if !res.Changed {
		t.Error("Changed = false, want true")
	}
	s := readSettings(t, path)
	ep, ok := s["enabledPlugins"].(map[string]any)
	if !ok {
		t.Fatalf("enabledPlugins section not created: %v", s["enabledPlugins"])
	}
	if on, _ := ep["lsp-pack@swarmery"].(bool); !on {
		t.Errorf("enabledPlugins[lsp-pack@swarmery] = %v, want true", ep["lsp-pack@swarmery"])
	}
}

func TestTogglePluginEnableAlreadyOn(t *testing.T) {
	dir := t.TempDir()
	path := writeTestSettings(t, dir, `{"enabledPlugins": {"lsp-pack@swarmery": true}}`)

	res, err := TogglePlugin(dir, "lsp-pack", true)
	if err != nil {
		t.Fatalf("TogglePlugin: %v", err)
	}
	if res.Changed {
		t.Error("Changed = true, want false (already enabled)")
	}
	if res.Backup != "" {
		t.Errorf("Backup = %q, want empty on a no-op", res.Backup)
	}
	if _, err := os.Stat(path + ".bak"); !os.IsNotExist(err) {
		t.Error("no-op enable must not write a .bak")
	}
}

func TestTogglePluginDisableDeletesKey(t *testing.T) {
	dir := t.TempDir()
	path := writeTestSettings(t, dir, `{
		"enabledPlugins": {"lsp-pack@swarmery": true, "core@swarmery": true, "other@elsewhere": true}
	}`)

	res, err := TogglePlugin(dir, "lsp-pack", false)
	if err != nil {
		t.Fatalf("TogglePlugin: %v", err)
	}
	if !res.Changed {
		t.Error("Changed = false, want true")
	}
	s := readSettings(t, path)
	ep := s["enabledPlugins"].(map[string]any)
	if _, present := ep["lsp-pack@swarmery"]; present {
		t.Error("lsp-pack@swarmery must be DELETED on disable, not set to false")
	}
	for _, key := range []string{"core@swarmery", "other@elsewhere"} {
		if on, _ := ep[key].(bool); !on {
			t.Errorf("enabledPlugins[%s] = %v, want true (foreign entries survive)", key, ep[key])
		}
	}
}

func TestTogglePluginDisableAlreadyOff(t *testing.T) {
	dir := t.TempDir()
	seed := `{"enabledPlugins": {"core@swarmery": true}}`
	path := writeTestSettings(t, dir, seed)

	res, err := TogglePlugin(dir, "lsp-pack", false)
	if err != nil {
		t.Fatalf("TogglePlugin: %v", err)
	}
	if res.Changed {
		t.Error("Changed = true, want false (key absent)")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != seed {
		t.Errorf("file bytes changed on a no-op disable: %q", raw)
	}
	if _, err := os.Stat(path + ".bak"); !os.IsNotExist(err) {
		t.Error("no-op disable must not write a .bak")
	}
}

func TestTogglePluginCoreLocked(t *testing.T) {
	dir := t.TempDir()
	writeTestSettings(t, dir, toggleSettings)

	if _, err := TogglePlugin(dir, "core", true); !errors.Is(err, ErrCoreLocked) {
		t.Errorf("enable core err = %v, want ErrCoreLocked", err)
	}
	if _, err := TogglePlugin(dir, "core", false); !errors.Is(err, ErrCoreLocked) {
		t.Errorf("disable core err = %v, want ErrCoreLocked", err)
	}
}

func TestTogglePluginNoSettings(t *testing.T) {
	dir := t.TempDir() // no .claude/settings.json at all

	if _, err := TogglePlugin(dir, "lsp-pack", true); !errors.Is(err, ErrNoSettings) {
		t.Errorf("err = %v, want ErrNoSettings", err)
	}
}

func TestTogglePluginMalformedSettings(t *testing.T) {
	dir := t.TempDir()
	path := writeTestSettings(t, dir, `{oops`)

	if _, err := TogglePlugin(dir, "lsp-pack", true); !errors.Is(err, ErrBadSettings) {
		t.Errorf("err = %v, want ErrBadSettings", err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != `{oops` {
		t.Errorf("malformed file must never be overwritten, got %q", raw)
	}
	if _, err := os.Stat(path + ".bak"); !os.IsNotExist(err) {
		t.Error("malformed settings must not produce a .bak")
	}
}

func TestTogglePluginBadEnabledPluginsShape(t *testing.T) {
	dir := t.TempDir()
	seed := `{"enabledPlugins": []}`
	path := writeTestSettings(t, dir, seed)

	if _, err := TogglePlugin(dir, "lsp-pack", true); !errors.Is(err, ErrBadSettings) {
		t.Errorf("err = %v, want ErrBadSettings for a non-object enabledPlugins", err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != seed {
		t.Errorf("file bytes changed: %q", raw)
	}
}
