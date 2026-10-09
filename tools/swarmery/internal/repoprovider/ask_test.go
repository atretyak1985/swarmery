package repoprovider

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func readSettingsLocal(t *testing.T, project string) map[string]any {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(project, ".claude", "settings.local.json"))
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("settings.local.json is not JSON: %v\n%s", err, raw)
	}
	return doc
}

func TestPersistProviderAnswerCreatesFile(t *testing.T) {
	project := t.TempDir()
	if err := PersistProviderAnswer(project, KindGitLab); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(project, ".claude", "settings.local.json")
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o644 {
		t.Fatalf("mode = %04o, want 0644", info.Mode().Perm())
	}
	raw, _ := os.ReadFile(path)
	want := "{\n  \"swarmery\": {\n    \"vcs\": {\n      \"provider\": \"gitlab\"\n    }\n  }\n}\n"
	if string(raw) != want {
		t.Fatalf("file =\n%s\nwant\n%s", raw, want)
	}
	if got := LoadConfig(project).ExplicitKind(); got != KindGitLab {
		t.Fatalf("LoadConfig provider = %q, want gitlab", got)
	}
	// No temp file left behind.
	entries, _ := os.ReadDir(filepath.Join(project, ".claude"))
	if len(entries) != 1 {
		t.Fatalf(".claude entries = %v", entries)
	}
}

func TestPersistProviderAnswerPreservesOtherKeys(t *testing.T) {
	project := t.TempDir()
	if err := os.MkdirAll(filepath.Join(project, ".claude"), 0o755); err != nil {
		t.Fatal(err)
	}
	existing := `{
  "permissions": {"allow": ["Bash(make test && make build)", "Read(<repo>/**)"]},
  "enabledPlugins": {"core@swarmery": true},
  "swarmery": {
    "estate": {"repos": ["api", "web"], "weight": 12345678901234567},
    "vcs": {"baseBranch": "develop", "provider": "github"}
  }
}`
	path := filepath.Join(project, ".claude", "settings.local.json")
	if err := os.WriteFile(path, []byte(existing), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := PersistProviderAnswer(project, KindGitLab); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(path)
	// Byte-level: HTML-significant characters and big integers round-trip.
	for _, want := range []string{"Bash(make test && make build)", "Read(<repo>/**)", "12345678901234567"} {
		if !strings.Contains(string(raw), want) {
			t.Errorf("lost %q:\n%s", want, raw)
		}
	}
	doc := readSettingsLocal(t, project)
	if doc["enabledPlugins"].(map[string]any)["core@swarmery"] != true {
		t.Errorf("top-level key lost: %v", doc)
	}
	sw := doc["swarmery"].(map[string]any)
	estate, ok := sw["estate"].(map[string]any)
	if !ok || len(estate["repos"].([]any)) != 2 {
		t.Errorf("swarmery.estate lost: %v", sw)
	}
	vcs := sw["vcs"].(map[string]any)
	if vcs["provider"] != "gitlab" || vcs["baseBranch"] != "develop" {
		t.Errorf("swarmery.vcs = %v", vcs)
	}
	info, _ := os.Stat(path)
	if info.Mode().Perm() != 0o644 {
		t.Errorf("mode = %04o, want 0644", info.Mode().Perm())
	}
	c := LoadConfig(project)
	if c.ExplicitKind() != KindGitLab || c.BaseBranch != "develop" {
		t.Fatalf("LoadConfig = %+v", c)
	}
	// Answering again switches the provider and still keeps the rest.
	if err := PersistProviderAnswer(project, KindGitHub); err != nil {
		t.Fatal(err)
	}
	if got := LoadConfig(project).ExplicitKind(); got != KindGitHub {
		t.Fatalf("second answer: %q", got)
	}
	if _, ok := readSettingsLocal(t, project)["permissions"]; !ok {
		t.Fatal("permissions lost on the second answer")
	}
}

func TestPersistProviderAnswerLocalOverridesProjectJSON(t *testing.T) {
	project := t.TempDir()
	if err := os.MkdirAll(filepath.Join(project, ".claude"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(project, ".claude", "project.json"),
		[]byte(`{"vcs":{"provider":"github","baseBranch":"main"}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := PersistProviderAnswer(project, KindGitLab); err != nil {
		t.Fatal(err)
	}
	c := LoadConfig(project)
	if c.ExplicitKind() != KindGitLab || c.BaseBranch != "main" {
		t.Fatalf("LoadConfig = %+v (local answer must win per key)", c)
	}
}

func TestPersistProviderAnswerRefusals(t *testing.T) {
	project := t.TempDir()
	for _, k := range []Kind{KindUnknown, "", "bitbucket", "GitLab"} {
		if err := PersistProviderAnswer(project, k); !errors.Is(err, ErrUnknownProvider) {
			t.Errorf("kind %q: err = %v, want ErrUnknownProvider", k, err)
		}
	}
	if _, err := os.Stat(filepath.Join(project, ".claude")); !os.IsNotExist(err) {
		t.Fatal("a refused kind created .claude/")
	}
	if err := PersistProviderAnswer("", KindGitHub); err == nil {
		t.Fatal("empty project path accepted")
	}

	// A file that is not a JSON object is never overwritten.
	path := filepath.Join(project, ".claude", "settings.local.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, body := range []string{`{"broken":`, `[1,2]`, `null`, `{"swarmery":"text"}`, `{"swarmery":{"vcs":[]}}`} {
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := PersistProviderAnswer(project, KindGitHub); err == nil {
			t.Errorf("body %q: accepted", body)
		}
		if raw, _ := os.ReadFile(path); string(raw) != body {
			t.Errorf("body %q overwritten with %q", body, raw)
		}
	}

	// An empty file and an explicit null swarmery block are treated as empty.
	for _, body := range []string{"", "  \n", `{"swarmery":null}`} {
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := PersistProviderAnswer(project, KindGitHub); err != nil {
			t.Errorf("body %q: %v", body, err)
		}
		if got := LoadConfig(project).ExplicitKind(); got != KindGitHub {
			t.Errorf("body %q: provider %q", body, got)
		}
	}
}
