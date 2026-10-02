package wtjanitor

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// pluginsFixture writes an installed_plugins.json under a fresh config dir and
// returns the dir, the file and a daemon worktree root holding one live
// worktree (live) and one removed one (gone).
func pluginsFixture(t *testing.T) (cfgDir, file, daemonRoot, live, gone string) {
	t.Helper()
	cfgDir = t.TempDir()
	daemonRoot = t.TempDir()
	live = filepath.Join(daemonRoot, "-repo", "phase-1")
	gone = filepath.Join(daemonRoot, "-repo", "phase-2")
	if err := os.MkdirAll(live, 0o755); err != nil {
		t.Fatal(err)
	}
	foreignGone := filepath.Join(t.TempDir(), "someone-elses-checkout")
	body := map[string]any{
		"version":   2,
		"futureKey": "kept",
		"plugins": map[string]any{
			"core@swarmery": []map[string]any{
				{"scope": "user", "version": "3.9.7", "installPath": "/c/core/3.9.7"},
				{"scope": "project", "projectPath": live, "version": "3.9.6", "extra": map[string]any{"a": 1}},
				{"scope": "project", "projectPath": gone, "version": "3.9.6"},
				{"scope": "project", "projectPath": foreignGone, "version": "3.9.6"},
			},
			"web-pack@swarmery": []map[string]any{
				{"scope": "project", "projectPath": gone, "version": "1.3.0"},
			},
		},
	}
	raw, err := json.MarshalIndent(body, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	file = filepath.Join(cfgDir, "plugins", "installed_plugins.json")
	if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	return cfgDir, file, daemonRoot, live, gone
}

func ownedUnder(daemonRoot string) func(string) bool {
	return func(p string) bool { return agentOwned("/repo", daemonRoot, p) }
}

func readPlugins(t *testing.T, file string) (map[string]json.RawMessage, map[string][]map[string]any) {
	t.Helper()
	raw, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	var top map[string]json.RawMessage
	if err := json.Unmarshal(raw, &top); err != nil {
		t.Fatalf("result is not valid JSON: %v", err)
	}
	var plugins map[string][]map[string]any
	if err := json.Unmarshal(top["plugins"], &plugins); err != nil {
		t.Fatal(err)
	}
	return top, plugins
}

func TestPluginRecordsPrunesOnlyGoneAgentWorktrees(t *testing.T) {
	cfgDir, file, daemonRoot, live, _ := pluginsFixture(t)
	p := PluginRecords{ConfigDirs: func() []string { return []string{cfgDir} }}

	n, err := p.Prune(ownedUnder(daemonRoot), false)
	if err != nil || n != 2 {
		t.Fatalf("Prune = (%d, %v), want (2, nil): the two records of the removed worktree", n, err)
	}

	top, plugins := readPlugins(t, file)
	if string(top["futureKey"]) != `"kept"` || string(top["version"]) != "2" {
		t.Errorf("top-level keys not preserved: %v", top)
	}
	if _, ok := plugins["web-pack@swarmery"]; ok {
		t.Error("a plugin whose only record was pruned must be dropped from the map")
	}
	core := plugins["core@swarmery"]
	if len(core) != 3 {
		t.Fatalf("core records = %d, want 3 (user, live worktree, foreign checkout): %v", len(core), core)
	}
	var sawUser, sawLive, sawForeign bool
	for _, e := range core {
		switch pp, _ := e["projectPath"].(string); {
		case pp == "":
			sawUser = true
		case pp == live:
			sawLive = true
			if e["extra"] == nil {
				t.Error("unknown entry fields must survive the rewrite")
			}
		case !strings.HasPrefix(pp, daemonRoot):
			sawForeign = true
		}
	}
	if !sawUser || !sawLive || !sawForeign {
		t.Errorf("kept user=%v live=%v foreign=%v, want all true", sawUser, sawLive, sawForeign)
	}
	if fi, err := os.Stat(file); err != nil || fi.Mode().Perm() != 0o600 {
		t.Errorf("file mode not preserved: %v %v", fi.Mode().Perm(), err)
	}
}

func TestPluginRecordsDryRunWritesNothing(t *testing.T) {
	cfgDir, file, daemonRoot, _, _ := pluginsFixture(t)
	before, _ := os.ReadFile(file)
	p := PluginRecords{ConfigDirs: func() []string { return []string{cfgDir} }}

	n, err := p.Prune(ownedUnder(daemonRoot), true)
	if err != nil || n != 2 {
		t.Fatalf("dry-run Prune = (%d, %v), want (2, nil)", n, err)
	}
	if after, _ := os.ReadFile(file); string(after) != string(before) {
		t.Error("a dry run rewrote installed_plugins.json")
	}
}

func TestPluginRecordsMissingAndMalformedFiles(t *testing.T) {
	empty := t.TempDir() // no plugins/installed_plugins.json at all
	bad := t.TempDir()
	badFile := filepath.Join(bad, "plugins", "installed_plugins.json")
	if err := os.MkdirAll(filepath.Dir(badFile), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(badFile, []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	p := PluginRecords{ConfigDirs: func() []string { return []string{empty, bad} }}

	n, err := p.Prune(func(string) bool { return true }, false)
	if n != 0 || err == nil {
		t.Fatalf("Prune = (%d, %v), want 0 and the parse error for the malformed file", n, err)
	}
	if got, _ := os.ReadFile(badFile); string(got) != "{not json" {
		t.Error("a malformed file must be left exactly as it was")
	}
}

// Claude Code rewrites installed_plugins.json on every plugin install. A
// rewrite that lands between the pruner's read and its rename must win.
func TestReplaceIfUnchangedRefusesAConcurrentWrite(t *testing.T) {
	_, file, _, _, _ := pluginsFixture(t)
	before, err := os.Stat(file)
	if err != nil {
		t.Fatal(err)
	}
	read, _ := os.ReadFile(file)
	concurrent := []byte(`{"version":2,"plugins":{"new@swarmery":[{"scope":"user"}]}}`)
	if err := os.WriteFile(file, concurrent, 0o600); err != nil {
		t.Fatal(err)
	}

	err = replaceIfUnchanged(file, before, read, []byte(`{"pruned":true}`))
	if !errors.Is(err, errChanged) {
		t.Fatalf("err = %v, want errChanged", err)
	}
	if got, _ := os.ReadFile(file); string(got) != string(concurrent) {
		t.Errorf("the concurrent write was lost: %s", got)
	}
	if left, _ := filepath.Glob(filepath.Join(filepath.Dir(file), ".installed_plugins-*")); len(left) != 0 {
		t.Errorf("temp files left behind: %v", left)
	}
}

// Wiring: a sweep with Plugins set prunes after the worktree pass and counts
// the records; without it the file is never touched.
func TestSweepPrunesPluginRecords(t *testing.T) {
	cfgDir, file, daemonRoot, _, _ := pluginsFixture(t)
	db := testDB(t)
	s := svc(t, db, &stubInspector{}, &recordingRemover{}, idleLive{})
	s.DaemonRoot = daemonRoot

	before, _ := os.ReadFile(file)
	if _, err := s.Sweep(false); err != nil {
		t.Fatal(err)
	}
	if after, _ := os.ReadFile(file); string(after) != string(before) {
		t.Fatal("a sweep without Plugins touched installed_plugins.json")
	}

	s.Plugins = &PluginRecords{ConfigDirs: func() []string { return []string{cfgDir} }}
	res, err := s.Sweep(false)
	if err != nil {
		t.Fatal(err)
	}
	if res.PluginRecords != 2 {
		t.Errorf("Result.PluginRecords = %d, want 2", res.PluginRecords)
	}
}
