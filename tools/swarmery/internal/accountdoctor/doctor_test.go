package accountdoctor

// Hermetic: every test points HOME, the secrets dir and the launch marker at
// temp state, so no operator file is ever read. Values planted in the
// environment are asserted ABSENT from output — never printed.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// fixture is a hermetic HOME with the default account's config dir.
type fixture struct {
	home, cfg, secrets string
}

func newFixture(t *testing.T) fixture {
	t.Helper()
	root := t.TempDir()
	f := fixture{
		home:    filepath.Join(root, "home"),
		cfg:     filepath.Join(root, "home", ".claude"),
		secrets: filepath.Join(root, "secrets"),
	}
	mustMkdir(t, filepath.Join(f.cfg, "projects"), 0o755)
	mustMkdir(t, f.secrets, 0o700)
	t.Setenv("HOME", f.home)
	t.Setenv("SWARMERY_SECRETS_DIR", f.secrets)
	t.Setenv(LaunchPathEnv, "")
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	return f
}

func mustMkdir(t *testing.T, dir string, mode os.FileMode) {
	t.Helper()
	if err := os.MkdirAll(dir, mode); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, mode); err != nil {
		t.Fatal(err)
	}
}

func mustWrite(t *testing.T, path, body string, mode os.FileMode) {
	t.Helper()
	mustMkdir(t, filepath.Dir(path), 0o755)
	if err := os.WriteFile(path, []byte(body), mode); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, mode); err != nil {
		t.Fatal(err)
	}
}

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// plugin installs one plugin (id) under the fixture's config dir with the given
// .mcp.json body, records it in installed_plugins.json and returns its dir.
func (f fixture) plugin(t *testing.T, installed map[string][]installRecord, id, mcp string) string {
	t.Helper()
	dir := filepath.Join(f.cfg, "plugins", "cache", strings.ReplaceAll(id, "@", "-"))
	if mcp != "" {
		mustWrite(t, filepath.Join(dir, ".mcp.json"), mcp, 0o644)
	} else {
		mustMkdir(t, dir, 0o755)
	}
	installed[id] = append(installed[id], installRecord{Scope: "user", InstallPath: dir})
	return dir
}

func (f fixture) writeInstalled(t *testing.T, installed map[string][]installRecord) {
	t.Helper()
	mustWrite(t, filepath.Join(f.cfg, "plugins", "installed_plugins.json"),
		mustJSON(t, map[string]any{"version": 2, "plugins": installed}), 0o644)
}

func (f fixture) enable(t *testing.T, enabled map[string]bool) {
	t.Helper()
	mustWrite(t, filepath.Join(f.cfg, "settings.json"), mustJSON(t, map[string]any{"enabledPlugins": enabled}), 0o644)
}

func TestFastRequiresPath(t *testing.T) {
	newFixture(t)
	if _, err := Fast(Options{}); err == nil {
		t.Fatal("Fast(Options{}) = nil error, want ErrNoPath")
	}
	if _, err := Fast(Options{Path: "   "}); err == nil {
		t.Fatal("Fast(Options{Path: blank}) = nil error, want ErrNoPath")
	}
}

// TestFastZeroConfigNeverNull: Path alone, every other Options field zero —
// no error, every list a non-nil empty slice, and the raw JSON carries [] and
// never null for each of them.
func TestFastZeroConfigNeverNull(t *testing.T) {
	newFixture(t)
	dir := t.TempDir()

	rep, err := Fast(Options{Path: dir})
	if err != nil {
		t.Fatalf("Fast: %v", err)
	}
	for name, v := range map[string]any{
		"VarsExpected": rep.VarsExpected, "VarsPresent": rep.VarsPresent, "VarsMissing": rep.VarsMissing,
		"Findings": rep.Findings, "StaleDuplicates": rep.StaleDuplicates,
	} {
		rv := reflect.ValueOf(v)
		if rv.IsNil() || rv.Len() != 0 {
			t.Errorf("%s = %#v, want a non-nil empty slice", name, v)
		}
	}
	if rep.Schema != 1 || rep.Account != "default" || rep.Credentials != 0 || rep.CredentialStore != "" {
		t.Errorf("zero report = %+v", rep)
	}
	raw := mustJSON(t, rep)
	for _, want := range []string{`"staleDuplicates":[]`, `"varsMissing":[]`, `"varsExpected":[]`, `"varsPresent":[]`, `"findings":[]`} {
		if !strings.Contains(raw, want) {
			t.Errorf("JSON lacks %s: %s", want, raw)
		}
	}
	if strings.Contains(raw, "null") {
		t.Errorf("JSON carries a null: %s", raw)
	}
}

// TestFastTagSpelling pins the camelCase contract both shell consumers select
// on: a "normalise the tags" edit must fail here.
func TestFastTagSpelling(t *testing.T) {
	newFixture(t)
	rep, err := Fast(Options{Path: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal([]byte(mustJSON(t, rep)), &m); err != nil {
		t.Fatal(err)
	}
	want := []string{"schema", "path", "account", "source", "configDir", "estate", "estateRoot", "settingsFile",
		"credentials", "credentialStore", "varsExpected", "varsPresent", "varsMissing", "staleDuplicates",
		"launchedViaSwarmery", "daemon", "findings"}
	if len(m) != len(want) {
		t.Errorf("report has %d keys, want %d: %v", len(m), len(want), m)
	}
	for _, k := range want {
		if _, ok := m[k]; !ok {
			t.Errorf("report JSON lacks %q", k)
		}
	}
	dup := mustJSON(t, Duplicate{})
	for _, k := range []string{`"kind"`, `"paths"`, `"key"`, `"overlap"`, `"count"`} {
		if !strings.Contains(dup, k) {
			t.Errorf("Duplicate JSON lacks %s: %s", k, dup)
		}
	}
	fin := mustJSON(t, Finding{})
	for _, k := range []string{`"id"`, `"severity"`, `"title"`, `"detail"`, `"file"`} {
		if !strings.Contains(fin, k) {
			t.Errorf("Finding JSON lacks %s: %s", k, fin)
		}
	}
}

func TestFastCoverageNamesOnly(t *testing.T) {
	f := newFixture(t)
	installed := map[string][]installRecord{}
	f.plugin(t, installed, "alpha@mk", `{"mcpServers":{"a":{"env":{"X":"${DOCTOR_T_ONE}","Y":"${DOCTOR_T_TWO}",
		"Z":"${DOCTOR_T_DEF:-fallback}","R":"${CLAUDE_PLUGIN_ROOT}/x"}}}}`)
	f.plugin(t, installed, "off@mk", `{"mcpServers":{"b":{"env":{"X":"${DOCTOR_T_OFF}"}}}}`)
	f.plugin(t, installed, "nomcp@mk", "")
	f.writeInstalled(t, installed)
	f.enable(t, map[string]bool{"alpha@mk": true, "off@mk": false, "nomcp@mk": true, "ghost@mk": true})

	const planted = "zzq-doctor-planted-value"
	t.Setenv("DOCTOR_T_ONE", planted)
	t.Setenv("DOCTOR_T_TWO", "")
	t.Setenv("DOCTOR_T_OFF", "")

	rep, err := Fast(Options{Path: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"DOCTOR_T_ONE", "DOCTOR_T_TWO"}; !reflect.DeepEqual(rep.VarsExpected, want) {
		t.Errorf("VarsExpected = %v, want %v", rep.VarsExpected, want)
	}
	if want := []string{"DOCTOR_T_ONE"}; !reflect.DeepEqual(rep.VarsPresent, want) {
		t.Errorf("VarsPresent = %v, want %v", rep.VarsPresent, want)
	}
	if want := []string{"DOCTOR_T_TWO"}; !reflect.DeepEqual(rep.VarsMissing, want) {
		t.Errorf("VarsMissing = %v, want %v", rep.VarsMissing, want)
	}
	if strings.Contains(mustJSON(t, rep), planted) {
		t.Error("the marshalled report carries a planted VALUE")
	}
}

// TestFastNothingPresent: a fully populated varsExpected with nothing set.
func TestFastNothingPresent(t *testing.T) {
	f := newFixture(t)
	installed := map[string][]installRecord{}
	f.plugin(t, installed, "alpha@mk", `{"mcpServers":{"a":{"headers":{"A":"Basic ${DOCTOR_N_A}"},"url":"https://${DOCTOR_N_B}/x"}}}`)
	f.writeInstalled(t, installed)
	f.enable(t, map[string]bool{"alpha@mk": true})
	t.Setenv("DOCTOR_N_A", "")
	t.Setenv("DOCTOR_N_B", "")

	rep, err := Fast(Options{Path: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.VarsExpected) != 2 || len(rep.VarsPresent) != 0 || len(rep.VarsMissing) != 2 {
		t.Errorf("coverage = expected %v present %v missing %v", rep.VarsExpected, rep.VarsPresent, rep.VarsMissing)
	}
	if rep.VarsPresent == nil {
		t.Error("VarsPresent is nil, want []")
	}
}

// TestFastProjectLayersAndManifest: the project's settings layers override the
// account's, a project-scope install covering the path wins, and plugin.json's
// mcpServers (inline and by path) is scraped too.
func TestFastProjectLayersAndManifest(t *testing.T) {
	f := newFixture(t)
	proj := t.TempDir()
	other := t.TempDir()
	installed := map[string][]installRecord{}

	inline := filepath.Join(f.cfg, "plugins", "cache", "inline")
	mustWrite(t, filepath.Join(inline, ".claude-plugin", "plugin.json"),
		`{"name":"inline","mcpServers":{"s":{"env":{"K":"${DOCTOR_P_INLINE}"}}}}`, 0o644)
	byPath := filepath.Join(f.cfg, "plugins", "cache", "bypath")
	mustWrite(t, filepath.Join(byPath, ".claude-plugin", "plugin.json"), `{"name":"bypath","mcpServers":"./servers.json"}`, 0o644)
	mustWrite(t, filepath.Join(byPath, "servers.json"), `{"s":{"env":{"K":"${DOCTOR_P_BYPATH}"}}}`, 0o644)
	userCopy := filepath.Join(f.cfg, "plugins", "cache", "scoped-user")
	mustWrite(t, filepath.Join(userCopy, ".mcp.json"), `{"s":{"env":{"K":"${DOCTOR_P_USERCOPY}"}}}`, 0o644)
	projCopy := filepath.Join(f.cfg, "plugins", "cache", "scoped-proj")
	mustWrite(t, filepath.Join(projCopy, ".mcp.json"), `{"s":{"env":{"K":"${DOCTOR_P_PROJCOPY}"}}}`, 0o644)
	elsewhere := filepath.Join(f.cfg, "plugins", "cache", "scoped-other")
	mustWrite(t, filepath.Join(elsewhere, ".mcp.json"), `{"s":{"env":{"K":"${DOCTOR_P_OTHER}"}}}`, 0o644)

	installed["inline@mk"] = []installRecord{{Scope: "user", InstallPath: inline}}
	installed["bypath@mk"] = []installRecord{{Scope: "user", InstallPath: byPath}}
	installed["scoped@mk"] = []installRecord{
		{Scope: "local", InstallPath: elsewhere, ProjectPath: other},
		{Scope: "user", InstallPath: userCopy},
		{Scope: "project", InstallPath: projCopy, ProjectPath: proj},
	}
	installed["localonly@mk"] = []installRecord{{Scope: "user", InstallPath: userCopy}}
	f.writeInstalled(t, installed)
	f.enable(t, map[string]bool{"inline@mk": true, "bypath@mk": false})
	mustWrite(t, filepath.Join(proj, ".claude", "settings.json"), `{"enabledPlugins":{"bypath@mk":true,"scoped@mk":true}}`, 0o644)
	mustWrite(t, filepath.Join(proj, ".claude", "settings.local.json"), `{"enabledPlugins":{"inline@mk":false,"localonly@mk":"yes"}}`, 0o644)

	rep, err := Fast(Options{Path: proj})
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"DOCTOR_P_BYPATH", "DOCTOR_P_PROJCOPY"}; !reflect.DeepEqual(rep.VarsExpected, want) {
		t.Errorf("VarsExpected = %v, want %v", rep.VarsExpected, want)
	}
}

func TestFastLaunchMarker(t *testing.T) {
	newFixture(t)
	proj := t.TempDir()
	sub := filepath.Join(proj, "sub")
	mustMkdir(t, sub, 0o755)
	alias := filepath.Join(t.TempDir(), "alias")
	if err := os.Symlink(proj, alias); err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name, marker, path string
		want               bool
	}{
		{"unset", "", proj, false},
		{"relative", "rel/dir", proj, false},
		{"same path", proj, proj, true},
		{"same dir through a symlink", alias, proj, true},
		{"ancestor, same resolution", proj, sub, true},
		{"a different tree", t.TempDir(), proj, false},
		{"a descendant is not a cover", sub, proj, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv(LaunchPathEnv, tc.marker)
			rep, err := Fast(Options{Path: tc.path})
			if err != nil {
				t.Fatal(err)
			}
			if rep.LaunchedViaSwarmery != tc.want {
				t.Errorf("LaunchedViaSwarmery = %v, want %v", rep.LaunchedViaSwarmery, tc.want)
			}
		})
	}
}

func TestFastLaunchMarkerAncestorWithDifferentEstate(t *testing.T) {
	newFixture(t)
	proj := t.TempDir()
	sub := filepath.Join(proj, "sub")
	mustWrite(t, filepath.Join(sub, ".claude", "settings.local.json"), `{"swarmery":{"estate":"inner"}}`, 0o644)
	t.Setenv(LaunchPathEnv, proj)
	rep, err := Fast(Options{Path: sub})
	if err != nil {
		t.Fatal(err)
	}
	if rep.LaunchedViaSwarmery {
		t.Error("an ancestor marker resolving to another estate counted as a launch cover")
	}
}

func TestFastDaemonWorktree(t *testing.T) {
	f := newFixture(t)
	wt := filepath.Join(f.home, ".swarmery", "worktrees", "proj", "task")
	mustMkdir(t, wt, 0o755)
	rep, err := Fast(Options{Path: wt})
	if err != nil {
		t.Fatal(err)
	}
	if !rep.Daemon {
		t.Error("Daemon = false for a path under the worktree root")
	}
	rep, _ = Fast(Options{Path: t.TempDir()})
	if rep.Daemon {
		t.Error("Daemon = true outside the worktree root")
	}
}

// TestFastEstateWithoutStoreIsHealthy (D2a): an estate whose store file does
// not exist, with no pack referencing any ${VAR}, is a value — zero
// credentials, no store, empty coverage — and NOT an error.
func TestFastEstateWithoutStoreIsHealthy(t *testing.T) {
	newFixture(t)
	proj := t.TempDir()
	mustWrite(t, filepath.Join(proj, ".claude", "settings.local.json"),
		`{"swarmery":{"claudeAccount":"default","estate":"doctortest"}}`, 0o644)

	rep, err := Fast(Options{Path: proj})
	if err != nil {
		t.Fatalf("Fast: %v, want nil for an estate with no store", err)
	}
	if rep.Credentials != 0 || rep.CredentialStore != "" || len(rep.VarsExpected) != 0 || len(rep.VarsMissing) != 0 {
		t.Errorf("report = %+v", rep)
	}
	if rep.Estate != "doctortest" || rep.EstateRoot == "" || rep.Source != "pin" || rep.Account != "default" {
		t.Errorf("estate/resolution = %q %q %q %q", rep.Estate, rep.EstateRoot, rep.Source, rep.Account)
	}
	if len(rep.Findings) != 0 || rep.Findings == nil {
		t.Errorf("Findings = %#v, want []", rep.Findings)
	}
}

// TestFastEstateStoreCountsNames: a present store is reported by path and by
// the COUNT of names it supplies; its values never reach the report.
func TestFastEstateStoreCountsNames(t *testing.T) {
	f := newFixture(t)
	proj := t.TempDir()
	mustWrite(t, filepath.Join(proj, ".claude", "settings.local.json"), `{"swarmery":{"estate":"doctorstore"}}`, 0o644)
	const planted = "zzq-store-planted-value"
	store := filepath.Join(f.secrets, "doctorstore.env")
	mustWrite(t, store, "DOCTOR_S_ONE="+planted+"\nDOCTOR_S_TWO="+planted+"\n", 0o600)
	if err := os.Chmod(f.secrets, 0o700); err != nil {
		t.Fatal(err)
	}

	rep, err := Fast(Options{Path: proj})
	if err != nil {
		t.Fatal(err)
	}
	if rep.Credentials != 2 || rep.CredentialStore != store {
		t.Errorf("credentials = %d store = %q, want 2 and %q", rep.Credentials, rep.CredentialStore, store)
	}
	if strings.Contains(mustJSON(t, rep), planted) {
		t.Error("the report carries a store VALUE")
	}
}

func TestReadCappedRejectsNonRegularAndOversize(t *testing.T) {
	dir := t.TempDir()
	if _, ok := readCapped(dir); ok {
		t.Error("readCapped accepted a directory")
	}
	big := filepath.Join(dir, "big.json")
	if err := os.WriteFile(big, make([]byte, maxConfigBytes+1), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, ok := readCapped(big); ok {
		t.Error("readCapped accepted an oversize file")
	}
	if pickInstall([]installRecord{{InstallPath: ""}}, dir) != "" {
		t.Error("pickInstall chose an empty install path")
	}
	if got := expectedVars("", dir); got == nil || len(got) != 0 {
		t.Errorf("expectedVars with no config dir = %#v, want []", got)
	}
}
