package api

import (
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/store"
)

// rolesFixture lays out a plugin cache (one agents/ dir per pack, each with its
// roles.json) plus a project root, and seeds the registry with one agent per
// pack and a project-local agent. settings / settingsLocal are the project's
// .claude/settings.json and settings.local.json bodies ("" = absent).
type rolesFixture struct {
	srv      *httptest.Server
	cacheDir string
}

func newRolesFixture(t *testing.T, settings, settingsLocal string) rolesFixture {
	t.Helper()
	cache := t.TempDir()
	proj := t.TempDir()

	// Shipped role files: core names tech-lead + code-reviewer; uav-pack's file
	// carries an out-of-vocabulary value; web-pack has NO roles.json.
	writeFile(t, filepath.Join(cache, "core", "agents", "roles.json"),
		`{"tech-lead":"orchestrate","code-reviewer":"review"}`)
	writeFile(t, filepath.Join(cache, "iot-pack", "agents", "roles.json"),
		`{"iot-data-specialist":"domain"}`)
	writeFile(t, filepath.Join(cache, "uav-pack", "agents", "roles.json"),
		`{"mavlink-specialist":"wizard"}`)
	writeFile(t, filepath.Join(proj, ".claude", "agents", "roles.json"), `{"house-style":"review"}`)

	if settings != "" {
		writeFile(t, filepath.Join(proj, ".claude", "settings.json"), settings)
	}
	if settingsLocal != "" {
		writeFile(t, filepath.Join(proj, ".claude", "settings.local.json"), settingsLocal)
	}

	db, err := store.Open(filepath.Join(t.TempDir(), "roles.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := db.Exec(q, args...); err != nil {
			t.Fatalf("seed: %v\n%s", err, q)
		}
	}
	exec(`INSERT INTO projects (id, path, slug, name, first_seen) VALUES (1, ?, 'alpha', 'Alpha', '2026-07-01T00:00:00Z')`, proj)
	agent := func(name, scope string, projectID any, path, origin string, plugin any) {
		exec(`INSERT INTO agents (name, scope, project_id, file_path, origin, plugin_name, deleted) VALUES (?, ?, ?, ?, ?, ?, 0)`,
			name, scope, projectID, path, origin, plugin)
	}
	agent("core:tech-lead", "global", nil, filepath.Join(cache, "core", "agents", "tech-lead.md"), "plugin", "core")
	agent("core:code-reviewer", "global", nil, filepath.Join(cache, "core", "agents", "code-reviewer.md"), "plugin", "core")
	agent("core:debugger", "global", nil, filepath.Join(cache, "core", "agents", "debugger.md"), "plugin", "core")
	agent("web-pack:seo-specialist", "global", nil, filepath.Join(cache, "web-pack", "agents", "seo-specialist.md"), "plugin", "web-pack")
	agent("iot-pack:iot-data-specialist", "global", nil, filepath.Join(cache, "iot-pack", "agents", "iot-data-specialist.md"), "plugin", "iot-pack")
	agent("uav-pack:mavlink-specialist", "global", nil, filepath.Join(cache, "uav-pack", "agents", "mavlink-specialist.md"), "plugin", "uav-pack")
	agent("house-style", "project", 1, filepath.Join(proj, ".claude", "agents", "house-style.md"), "local", nil)

	h, err := NewServer(db, false)
	if err != nil {
		t.Fatalf("new server: %v", err)
	}
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return rolesFixture{srv: srv, cacheDir: cache}
}

func rosterByName(t *testing.T, url string) map[string]agentRosterRow {
	t.Helper()
	var out agentRosterDTO
	getJSON(t, url, &out)
	m := map[string]agentRosterRow{}
	for _, a := range out.Agents {
		m[a.Name] = a
	}
	return m
}

// Fixture 1: a project that enables only core.
func TestAgentsHubRolesOnlyCore(t *testing.T) {
	fx := newRolesFixture(t, `{"enabledPlugins":{"core@swarmery":true}}`, "")
	got := rosterByName(t, fx.srv.URL+"/api/agents/hub?projectId=alpha")

	want := map[string]string{
		"core:tech-lead":     "orchestrate", // named in core's roles.json
		"core:code-reviewer": "review",
		"core:debugger":      "domain", // no entry → domain
		"house-style":        "review", // the project's own .claude/agents/roles.json
	}
	for name, role := range want {
		a, ok := got[name]
		if !ok {
			t.Errorf("%s missing from the core-only roster: %v", name, got)
			continue
		}
		if a.Role != role {
			t.Errorf("%s role = %q, want %q", name, a.Role, role)
		}
		if !a.EnabledInProject {
			t.Errorf("%s enabledInProject = false, want true", name)
		}
	}
	for _, off := range []string{"web-pack:seo-specialist", "iot-pack:iot-data-specialist", "uav-pack:mavlink-specialist"} {
		if a, ok := got[off]; ok && a.EnabledInProject {
			t.Errorf("%s is listed as enabled in a core-only project", off)
		}
	}

	// Fleet mode has no project to gate on: every row is enabled, and the role
	// fallbacks still hold (no roles.json → domain; unknown value → domain).
	fleet := rosterByName(t, fx.srv.URL+"/api/agents/hub")
	if len(fleet) != 7 {
		t.Fatalf("fleet roster = %d rows, want 7", len(fleet))
	}
	for name, a := range fleet {
		if !a.EnabledInProject {
			t.Errorf("fleet %s enabledInProject = false, want true", name)
		}
	}
	if r := fleet["web-pack:seo-specialist"].Role; r != "domain" {
		t.Errorf("web-pack agent without roles.json role = %q, want domain", r)
	}
	if r := fleet["uav-pack:mavlink-specialist"].Role; r != "domain" {
		t.Errorf("unknown role value resolved to %q, want domain", r)
	}
}

// Fixture 2: like this repo — several packs, one of them switched on only in
// settings.local.json (the same effective set Projects → Plugins computes).
func TestAgentsHubRolesSeveralPacks(t *testing.T) {
	fx := newRolesFixture(t,
		`{"enabledPlugins":{"core@swarmery":true,"web-pack@swarmery":true,"uav-pack@swarmery":false}}`,
		`{"enabledPlugins":{"iot-pack@swarmery":true}}`)
	got := rosterByName(t, fx.srv.URL+"/api/agents/hub?projectId=alpha")

	for _, on := range []string{"core:tech-lead", "web-pack:seo-specialist", "iot-pack:iot-data-specialist", "house-style"} {
		a, ok := got[on]
		if !ok {
			t.Errorf("%s missing from the multi-pack roster", on)
			continue
		}
		if !a.EnabledInProject {
			t.Errorf("%s enabledInProject = false, want true", on)
		}
	}
	if a, ok := got["uav-pack:mavlink-specialist"]; ok && a.EnabledInProject {
		t.Errorf("disabled uav-pack agent reported as enabled")
	}
	if r := got["iot-pack:iot-data-specialist"].Role; r != "domain" {
		t.Errorf("iot-pack role = %q, want domain", r)
	}

	// The profile endpoint carries the same two fields under the same scope.
	var prof agentProfileDTO
	getJSON(t, fx.srv.URL+"/api/agents/"+itoa(got["core:tech-lead"].ID)+"/hub?projectId=alpha", &prof)
	if prof.Role != "orchestrate" || !prof.EnabledInProject {
		t.Errorf("profile role/enabled = %q/%v, want orchestrate/true", prof.Role, prof.EnabledInProject)
	}
}

// The roles file is re-read when it changes on disk, not pinned for the
// daemon's lifetime.
func TestAgentRoleCacheRereadsOnChange(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "a.md")
	rp := filepath.Join(dir, "roles.json")
	c := &agentRoleCache{dirs: map[string]rolesFile{}}

	if r := c.roleOf("core:a", path); r != "domain" {
		t.Fatalf("no roles.json: role = %q, want domain", r)
	}
	writeFile(t, rp, `{"a":"review"}`)
	if r := c.roleOf("core:a", path); r != "review" {
		t.Fatalf("after write: role = %q, want review", r)
	}
	writeFile(t, rp, `{"a":"ops"}`)
	later := time.Now().Add(2 * time.Second)
	if err := os.Chtimes(rp, later, later); err != nil {
		t.Fatal(err)
	}
	if r := c.roleOf("core:a", path); r != "ops" {
		t.Fatalf("after rewrite: role = %q, want ops (stale cache)", r)
	}
	writeFile(t, rp, `not json`)
	evenLater := later.Add(2 * time.Second)
	if err := os.Chtimes(rp, evenLater, evenLater); err != nil {
		t.Fatal(err)
	}
	if r := c.roleOf("core:a", path); r != "domain" {
		t.Fatalf("malformed roles.json: role = %q, want domain", r)
	}
	if err := os.Remove(rp); err != nil {
		t.Fatal(err)
	}
	if r := c.roleOf("core:a", path); r != "domain" {
		t.Fatalf("removed roles.json: role = %q, want domain", r)
	}
}

func TestEffectiveScopeEnabledIn(t *testing.T) {
	core, web, none := "core", "web-pack", (*string)(nil)
	esc := &effectiveScope{packs: []string{"core"}}
	cases := []struct {
		name   string
		esc    *effectiveScope
		origin string
		plugin *string
		want   bool
	}{
		{"fleet plugin", nil, "plugin", &web, true},
		{"local always", esc, "local", none, true},
		{"enabled pack", esc, "plugin", &core, true},
		{"disabled pack", esc, "plugin", &web, false},
		{"plugin without a pack name", esc, "plugin", none, false},
	}
	for _, tc := range cases {
		if got := tc.esc.enabledIn(tc.origin, tc.plugin); got != tc.want {
			t.Errorf("%s: enabledIn = %v, want %v", tc.name, got, tc.want)
		}
	}
}
