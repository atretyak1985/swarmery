package api

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/claudeacct"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/claudeacct/accttest"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/ingest"
)

// A resume composes from the session's PROJECT, never from an estate-less
// worktree: Resolve walks a daemon worktree cwd from its source checkout, and
// the file the origin run was lent is the Fallback. Built on a real repository
// and a real `git worktree add` under <home>/.swarmery/worktrees.
func TestResumeComposesFromProjectPath(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("SWARMERY_RUN_DIR", t.TempDir())
	proj := filepath.Join(home, "projects", "p")
	git := func(dir string, args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
		}
	}
	if err := os.MkdirAll(proj, 0o755); err != nil {
		t.Fatal(err)
	}
	git(proj, "init", "-q", "-b", "main")
	git(proj, "config", "user.email", "t@example.com")
	git(proj, "config", "user.name", "T")
	if err := os.WriteFile(filepath.Join(proj, "README.md"), []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git(proj, "add", "README.md")
	git(proj, "commit", "-qm", "init")

	if err := claudeacct.SetEstate(proj, "acme"); err != nil { // untracked binding: Lock 1 honours it
		t.Fatalf("SetEstate: %v", err)
	}
	accttest.AdmitEstate(t, "acme", proj)
	writeJSONFile(t, filepath.Join(proj, ".claude", "settings.json"),
		map[string]any{"pluginConfigs": map[string]any{"a@m": map[string]any{"k": "v"}}, "permissions": map[string]any{}})

	wt := filepath.Join(home, ".swarmery", "worktrees", ingest.SlugForPath(proj), "T-1")
	git(proj, "worktree", "add", "-q", "-b", "t1", wt)

	lent := filepath.Join(t.TempDir(), "lent.json")
	writeJSONFile(t, lent, map[string]any{"hooks": map[string]any{"Stop": []any{}}, "enabledPlugins": map[string]any{"x@m": true}})

	// A worktree session found through its origin run.
	res, o := resumeSettings(wt, resumeOrigin{SettingsFile: lent, Agent: "a"})
	wantRoot, _ := filepath.EvalSymlinks(proj)
	gotRoot, _ := filepath.EvalSymlinks(res.EstateRoot)
	if gotRoot != wantRoot || !res.EstateAdmitted {
		t.Fatalf("resolution = root %q admitted %v, want the source project %q, admitted", res.EstateRoot, res.EstateAdmitted, proj)
	}
	if o.SettingsFile == "" || o.SettingsFile == lent || o.Agent != "a" {
		t.Fatalf("origin = %+v, want a composed file over the lent one and the rest untouched", o)
	}
	got := readJSONFile(t, o.SettingsFile)
	if got["hooks"] == nil || got["enabledPlugins"] == nil {
		t.Errorf("the lent file's own keys must travel verbatim: %v", got)
	}
	if got["pluginConfigs"] == nil || got["permissions"] != nil {
		t.Errorf("the estate adds its EstateKeys only: %v", got)
	}

	// A session with no origin run composes from its cwd with Fallback "".
	_, o = resumeSettings(proj, resumeOrigin{})
	if got := readJSONFile(t, o.SettingsFile); len(got) != 1 || got["pluginConfigs"] == nil {
		t.Errorf("no origin: composed %v, want the estate's pluginConfigs alone", got)
	}

	// No admitted estate: the lent file comes back unchanged — no flag day.
	if _, o = resumeSettings(t.TempDir(), resumeOrigin{SettingsFile: lent}); o.SettingsFile != lent {
		t.Errorf("no estate: SettingsFile = %q, want the lent %q", o.SettingsFile, lent)
	}
}

func writeJSONFile(t *testing.T, path string, v any) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(v)
	if err := os.WriteFile(path, b, 0o600); err != nil {
		t.Fatal(err)
	}
}

func readJSONFile(t *testing.T, path string) map[string]any {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	return m
}
