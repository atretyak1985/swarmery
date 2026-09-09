package api

// Step 02 tests — GET /api/tools sidebar feed + fenced serena start/stop.
// A stub bash script (prints serena's dashboard line, then sleeps) stands in
// for the real binary; lookPathFn is overridden per test to control
// serena.available and the missing-binary 503.

import (
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/toolproc"
)

// attachStubToolManager wires a toolproc.Manager whose command is a stub bash
// script, restoring the global and killing any children on cleanup.
func attachStubToolManager(t *testing.T) *toolproc.Manager {
	t.Helper()
	stub := filepath.Join(t.TempDir(), "stub.sh")
	body := "#!/bin/bash\necho 'Serena web dashboard started at http://127.0.0.1:19999/dashboard/index.html'\nsleep 60\n"
	if err := os.WriteFile(stub, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	m := toolproc.NewManager(toolproc.Config{Command: func(projectDir string, mcpPort int) (string, []string) {
		return stub, nil
	}})
	AttachToolManager(m)
	t.Cleanup(func() {
		AttachToolManager(nil)
		m.StopAll()
	})
	return m
}

// stubLookPath forces serena availability on (err == nil) or off, restoring
// the real exec.LookPath on cleanup.
func stubLookPath(t *testing.T, err error) {
	t.Helper()
	prev := lookPathFn
	lookPathFn = func(name string) (string, error) {
		if err != nil {
			return "", err
		}
		return "/usr/local/bin/" + name, nil
	}
	t.Cleanup(func() { lookPathFn = prev })
}

func getToolsResponse(t *testing.T, srvURL string) toolsResponse {
	t.Helper()
	var resp toolsResponse
	getJSON(t, srvURL+"/api/tools", &resp)
	return resp
}

func TestToolsDashLists(t *testing.T) {
	srv, _ := projectsTestServer(t)
	attachStubToolManager(t)
	stubLookPath(t, nil)

	// Project 1 enables both packs; graph.json exists but graph.html does not.
	path := projectPath(t, srv.URL, "1")
	writeProjectSettings(t, path, `{
		"enabledPlugins": {"core@swarmery": true, "lsp-pack@swarmery": true, "graphify-pack@swarmery": true}
	}`)
	out := filepath.Join(path, "graphify-out")
	if err := os.MkdirAll(out, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(out, "graph.json"), []byte(`{"nodes":[]}`), 0o644); err != nil {
		t.Fatal(err)
	}

	resp := getToolsResponse(t, srv.URL)
	if !resp.Serena.Available {
		t.Error("serena.available = false with lookPath stubbed to succeed, want true")
	}

	// Project 2 (telemetry-only, no packs) and project 3 (archived) must appear
	// in neither list.
	if len(resp.Serena.Projects) != 1 {
		t.Fatalf("serena.projects len = %d, want 1 (%+v)", len(resp.Serena.Projects), resp.Serena.Projects)
	}
	sp := resp.Serena.Projects[0]
	if sp.ID != 1 || sp.Slug != "managed" {
		t.Errorf("serena project = id %d slug %q, want id 1 slug managed", sp.ID, sp.Slug)
	}
	if sp.State != "stopped" {
		t.Errorf("state = %q, want stopped (never started)", sp.State)
	}
	if sp.DashboardPath != "/api/projects/1/serena/" {
		t.Errorf("dashboardPath = %q, want /api/projects/1/serena/", sp.DashboardPath)
	}
	if sp.StartedAt != nil {
		t.Errorf("startedAt = %v, want null for a stopped project", *sp.StartedAt)
	}
	if sp.LogTail == nil || len(sp.LogTail) != 0 {
		t.Errorf("logTail = %v, want []", sp.LogTail)
	}
	if sp.Error != "" {
		t.Errorf("error = %q, want empty", sp.Error)
	}

	if len(resp.Graphify.Projects) != 1 {
		t.Fatalf("graphify.projects len = %d, want 1 (%+v)", len(resp.Graphify.Projects), resp.Graphify.Projects)
	}
	gp := resp.Graphify.Projects[0]
	if gp.ID != 1 || gp.Slug != "managed" {
		t.Errorf("graphify project = id %d slug %q, want id 1 slug managed", gp.ID, gp.Slug)
	}
	if !gp.HasGraph || gp.HasViz {
		t.Errorf("hasGraph=%v hasViz=%v, want hasGraph=true hasViz=false", gp.HasGraph, gp.HasViz)
	}
	if gp.BuiltAt == nil {
		t.Error("builtAt = null with graph.json present, want its mtime")
	} else if _, err := time.Parse(time.RFC3339, *gp.BuiltAt); err != nil {
		t.Errorf("builtAt = %q is not RFC3339: %v", *gp.BuiltAt, err)
	}
	if gp.VizPath != "/api/projects/1/graphify/graph.html" {
		t.Errorf("vizPath = %q, want /api/projects/1/graphify/graph.html", gp.VizPath)
	}
}

func TestToolsDashSerenaUnavailable(t *testing.T) {
	srv, _ := projectsTestServer(t)
	attachStubToolManager(t)
	stubLookPath(t, errors.New("exec: \"serena\": executable file not found in $PATH"))

	// Raw body: available=false, and all empty lists render [] — never null.
	// Four sections carry "projects": serena, graphify, graft, architecture. The
	// count is asserted rather than a per-section check so that adding a fifth
	// section fails here until it is confirmed to render [] too.
	res, err := http.Get(srv.URL + "/api/tools")
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("GET /api/tools = %d, want 200", res.StatusCode)
	}
	raw, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatal(err)
	}
	body := string(raw)
	if !strings.Contains(body, `"available":false`) {
		t.Errorf("body missing \"available\":false:\n%s", body)
	}
	if strings.Count(body, `"projects":[]`) != 4 {
		t.Errorf("empty lists must serialize as [] for all four tools (serena, graphify, graft, architecture):\n%s", body)
	}
	if strings.Contains(body, "null") {
		t.Errorf("empty response must not contain null:\n%s", body)
	}
}

func TestSerenaStartStopLifecycle(t *testing.T) {
	srv, _ := projectsTestServer(t)
	attachStubToolManager(t)
	stubLookPath(t, nil)
	writeProjectSettings(t, projectPath(t, srv.URL, "1"), `{
		"enabledPlugins": {"core@swarmery": true, "lsp-pack@swarmery": true}
	}`)

	out := doJSON(t, "POST", srv.URL+"/api/projects/1/serena/start", nil, 200)
	if out["state"] != "starting" {
		t.Fatalf("start body = %v, want state=starting", out)
	}

	// The stub prints the dashboard line immediately; GET /api/tools must show
	// running (with startedAt + logTail) well within the deadline — generous to
	// absorb spawn/pipe latency under full-suite parallel load (the loop exits
	// early on success, so the slack costs nothing).
	var sp serenaProjectDTO
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		resp := getToolsResponse(t, srv.URL)
		if len(resp.Serena.Projects) == 1 {
			sp = resp.Serena.Projects[0]
		}
		if sp.State == "running" {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if sp.State != "running" {
		t.Fatalf("state = %q after 2s, want running (logTail: %v)", sp.State, sp.LogTail)
	}
	if sp.StartedAt == nil {
		t.Error("startedAt = null for a running project, want RFC3339")
	}
	if len(sp.LogTail) == 0 {
		t.Error("logTail empty for a running project, want the dashboard line")
	}
	// The iframe targets the raw serena origin (root-absolute ajax in serena's
	// dashboard.js escapes the path-prefix proxy), so a running project must
	// expose a loopback dashboardUrl.
	if sp.DashboardURL == "" {
		t.Error("dashboardUrl empty for a running project, want the raw serena origin")
	} else if !strings.HasPrefix(sp.DashboardURL, "http://127.0.0.1:") &&
		!strings.HasPrefix(sp.DashboardURL, "http://localhost:") {
		t.Errorf("dashboardUrl = %q, want a loopback http origin", sp.DashboardURL)
	}

	out = doJSON(t, "POST", srv.URL+"/api/projects/1/serena/start", nil, 409)
	if msg, _ := out["error"].(string); msg != "serena is already running for this project" {
		t.Errorf("double-start error = %q, want the already-running message", msg)
	}

	out = doJSON(t, "POST", srv.URL+"/api/projects/1/serena/stop", nil, 200)
	if out["state"] != "stopped" {
		t.Errorf("stop body = %v, want state=stopped", out)
	}

	doJSON(t, "POST", srv.URL+"/api/projects/1/serena/stop", nil, 409)

	// Stopped → the raw origin must not leak: dashboardUrl resets to "".
	resp := getToolsResponse(t, srv.URL)
	if len(resp.Serena.Projects) != 1 {
		t.Fatalf("serena.projects len = %d after stop, want 1", len(resp.Serena.Projects))
	}
	if sp = resp.Serena.Projects[0]; sp.DashboardURL != "" {
		t.Errorf("dashboardUrl = %q after stop, want empty", sp.DashboardURL)
	}
}

func TestSerenaStartFences(t *testing.T) {
	srv, _ := projectsTestServer(t)
	attachStubToolManager(t)
	stubLookPath(t, nil)

	// Project 2's path is not under the onboarding root → 403.
	doJSON(t, "POST", srv.URL+"/api/projects/2/serena/start", nil, 403)
	// Unknown project → 404.
	doJSON(t, "POST", srv.URL+"/api/projects/9999/serena/start", nil, 404)
	// Non-numeric id → 400.
	doJSON(t, "POST", srv.URL+"/api/projects/bad/serena/start", nil, 400)

	// Missing binary → 503, before any process is spawned.
	prev := lookPathFn
	lookPathFn = func(string) (string, error) { return "", errors.New("not found") }
	out := doJSON(t, "POST", srv.URL+"/api/projects/1/serena/start", nil, 503)
	lookPathFn = prev
	if msg, _ := out["error"].(string); !strings.Contains(msg, "serena binary not found") {
		t.Errorf("missing-binary error = %q, want the install-serena message", msg)
	}

	// No onboarding roots → 403 with the fence message, for start AND stop.
	AttachOnboard(OnboardConfig{})
	out = doJSON(t, "POST", srv.URL+"/api/projects/1/serena/start", nil, 403)
	if msg, _ := out["error"].(string); !strings.Contains(msg, "SWARMERY_ONBOARD_ROOTS") {
		t.Errorf("no-roots error = %q, want the SWARMERY_ONBOARD_ROOTS fence message", msg)
	}
	doJSON(t, "POST", srv.URL+"/api/projects/1/serena/stop", nil, 403)
}

func TestToolsDashNilManager(t *testing.T) {
	srv, _ := projectsTestServer(t)
	AttachToolManager(nil)

	out := doJSON(t, "GET", srv.URL+"/api/tools", nil, 503)
	if msg, _ := out["error"].(string); msg != "tool manager not attached" {
		t.Errorf("error = %q, want \"tool manager not attached\"", msg)
	}
	doJSON(t, "POST", srv.URL+"/api/projects/1/serena/start", nil, 503)
	doJSON(t, "POST", srv.URL+"/api/projects/1/serena/stop", nil, 503)
}

func TestToolsDashArchitecture(t *testing.T) {
	srv, _ := projectsTestServer(t)
	attachStubToolManager(t)
	stubLookPath(t, nil)

	// Project 1 has architecture-out/architecture-map.html → listed.
	path := projectPath(t, srv.URL, "1")
	archOut := filepath.Join(path, "architecture-out")
	if err := os.MkdirAll(archOut, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(archOut, "architecture-map.html"), []byte("<html>arch</html>"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(archOut, "architecture-map.json"), []byte(`{"nodes":[]}`), 0o644); err != nil {
		t.Fatal(err)
	}

	resp := getToolsResponse(t, srv.URL)
	if len(resp.Architecture.Projects) != 1 {
		t.Fatalf("architecture.projects len = %d, want 1 (%+v)", len(resp.Architecture.Projects), resp.Architecture.Projects)
	}
	ap := resp.Architecture.Projects[0]
	if ap.ID != 1 || ap.Slug != "managed" {
		t.Errorf("architecture project = id %d slug %q, want id 1 slug managed", ap.ID, ap.Slug)
	}
	if !ap.HasMap {
		t.Error("hasMap = false, want true")
	}
	if ap.BuiltAt == nil {
		t.Error("builtAt = null with architecture-map.html present, want its mtime")
	} else if _, err := time.Parse(time.RFC3339, *ap.BuiltAt); err != nil {
		t.Errorf("builtAt = %q is not RFC3339: %v", *ap.BuiltAt, err)
	}
	if ap.MapPath != "/api/projects/1/architecture/architecture-map.html" {
		t.Errorf("mapPath = %q, want /api/projects/1/architecture/architecture-map.html", ap.MapPath)
	}

	// Project 2 (no architecture-out) → not in the list.
	for _, p := range resp.Architecture.Projects {
		if p.ID == 2 {
			t.Error("project 2 (no artifact) appeared in architecture.projects, want absent")
		}
	}

	// A project with unreadable plugin state (settings.json absent) but WITH the
	// artifact still appears. Project 2 has no settings.json in the test fixture;
	// add architecture-out to its dir directly. Pre-clean + register cleanup to
	// avoid leaking state to other tests (project 2's path is a fixed /tmp dir
	// shared across test runs in the same binary).
	path2 := projectPath(t, srv.URL, "2")
	archOut2 := filepath.Join(path2, "architecture-out")
	os.RemoveAll(archOut2) // pre-clean any stale state from prior runs
	t.Cleanup(func() { os.RemoveAll(archOut2) })
	if err := os.MkdirAll(archOut2, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(archOut2, "architecture-map.html"), []byte("<html>arch2</html>"), 0o644); err != nil {
		t.Fatal(err)
	}

	resp2 := getToolsResponse(t, srv.URL)
	found2 := false
	for _, p := range resp2.Architecture.Projects {
		if p.ID == 2 {
			found2 = true
		}
	}
	if !found2 {
		t.Error("project 2 (unreadable settings.json but has artifact) missing from architecture.projects")
	}
}

// TestToolsDashArchitectureUnion covers the pack∪artifact union logic added
// in the v2 iteration: pack-enabled projects appear even without an artifact,
// artifact projects with bad JSON still appear, and commit fields are populated
// from the map JSON + a seeded .git directory.
func TestToolsDashArchitectureUnion(t *testing.T) {
	const fakesha = "aabbccddee112233445566778899001122334455"

	t.Run("pack enabled no artifact — listed hasMap=false", func(t *testing.T) {
		srv, _ := projectsTestServer(t)
		attachStubToolManager(t)
		stubLookPath(t, nil)

		path := projectPath(t, srv.URL, "1")
		// Enable architecture-pack — no artifact on disk.
		writeProjectSettings(t, path, `{
			"enabledPlugins": {"core@swarmery": true, "architecture-pack@swarmery": true}
		}`)

		resp := getToolsResponse(t, srv.URL)
		found := false
		for _, p := range resp.Architecture.Projects {
			if p.ID == 1 {
				found = true
				if p.HasMap {
					t.Error("hasMap = true, want false for pack-enabled project with no artifact")
				}
				if p.MapPath == "" {
					t.Error("mapPath is empty, want the canonical path even without an artifact")
				}
			}
		}
		if !found {
			t.Error("pack-enabled project (id 1) not in architecture.projects, want listed")
		}
	})

	t.Run("telemetry-only artifact regression", func(t *testing.T) {
		// Project 2 has no settings.json (telemetry-only) — but with an artifact
		// it must still appear. This is the regression guard from the original test.
		srv, _ := projectsTestServer(t)
		attachStubToolManager(t)
		stubLookPath(t, nil)

		path2 := projectPath(t, srv.URL, "2")
		archOut2 := filepath.Join(path2, "architecture-out")
		os.RemoveAll(archOut2)
		t.Cleanup(func() { os.RemoveAll(archOut2) })
		if err := os.MkdirAll(archOut2, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(archOut2, "architecture-map.html"), []byte("<html>arch2</html>"), 0o644); err != nil {
			t.Fatal(err)
		}

		resp := getToolsResponse(t, srv.URL)
		found := false
		for _, p := range resp.Architecture.Projects {
			if p.ID == 2 {
				found = true
			}
		}
		if !found {
			t.Error("telemetry-only artifact project (id 2) missing from architecture.projects")
		}
	})

	t.Run("commit fields populated from map JSON and .git", func(t *testing.T) {
		srv, _ := projectsTestServer(t)
		attachStubToolManager(t)
		stubLookPath(t, nil)

		path := projectPath(t, srv.URL, "1")
		archOut := filepath.Join(path, "architecture-out")
		if err := os.MkdirAll(archOut, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(archOut, "architecture-map.html"), []byte("<html>arch</html>"), 0o644); err != nil {
			t.Fatal(err)
		}
		// Seed analyzedAtCommit into the map JSON.
		mapJSON := `{"analyzedAtCommit":"` + fakesha + `","nodes":[]}`
		if err := os.WriteFile(filepath.Join(archOut, "architecture-map.json"), []byte(mapJSON), 0o644); err != nil {
			t.Fatal(err)
		}
		// Seed a fake .git (loose-ref layout) so headCommit is resolvable.
		gitDir := filepath.Join(path, ".git")
		if err := os.MkdirAll(filepath.Join(gitDir, "refs", "heads"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(gitDir, "HEAD"), []byte("ref: refs/heads/main\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(gitDir, "refs", "heads", "main"), []byte(fakesha+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}

		resp := getToolsResponse(t, srv.URL)
		var ap *architectureProjectDTO
		for i := range resp.Architecture.Projects {
			if resp.Architecture.Projects[i].ID == 1 {
				ap = &resp.Architecture.Projects[i]
				break
			}
		}
		if ap == nil {
			t.Fatal("project 1 not found in architecture.projects")
		}
		if ap.AnalyzedAtCommit == nil {
			t.Error("analyzedAtCommit = null, want the sha from the map JSON")
		} else if *ap.AnalyzedAtCommit != fakesha {
			t.Errorf("analyzedAtCommit = %q, want %q", *ap.AnalyzedAtCommit, fakesha)
		}
		if ap.HeadCommit == nil {
			t.Error("headCommit = null, want the sha resolved from .git")
		} else if *ap.HeadCommit != fakesha {
			t.Errorf("headCommit = %q, want %q", *ap.HeadCommit, fakesha)
		}
	})

	t.Run("unparseable map JSON — analyzedAtCommit null but project listed", func(t *testing.T) {
		srv, _ := projectsTestServer(t)
		attachStubToolManager(t)
		stubLookPath(t, nil)

		path := projectPath(t, srv.URL, "1")
		archOut := filepath.Join(path, "architecture-out")
		if err := os.MkdirAll(archOut, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(archOut, "architecture-map.html"), []byte("<html>arch</html>"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(archOut, "architecture-map.json"), []byte(`not valid JSON!!!`), 0o644); err != nil {
			t.Fatal(err)
		}

		resp := getToolsResponse(t, srv.URL)
		var ap *architectureProjectDTO
		for i := range resp.Architecture.Projects {
			if resp.Architecture.Projects[i].ID == 1 {
				ap = &resp.Architecture.Projects[i]
				break
			}
		}
		if ap == nil {
			t.Fatal("project 1 not found — should appear even with bad JSON")
		}
		if ap.AnalyzedAtCommit != nil {
			t.Errorf("analyzedAtCommit = %q, want null for unparseable JSON", *ap.AnalyzedAtCommit)
		}
	})
}

// TestToolsDashArchitectureProvision covers the provision field added by the
// auto-provision hook: the latest provision_jobs row for a pack-enabled project
// surfaces on the architecture DTO; a project with no job carries provision:null.
func TestToolsDashArchitectureProvision(t *testing.T) {
	srv, db := projectsTestServer(t)
	attachStubToolManager(t)
	stubLookPath(t, nil)

	// Project 1: pack-enabled (so it lands in the architecture list) + a seeded
	// in-flight provision job.
	path := projectPath(t, srv.URL, "1")
	writeProjectSettings(t, path, `{
		"enabledPlugins": {"core@swarmery": true, "architecture-pack@swarmery": true}
	}`)
	execSQL(t, db, `INSERT INTO provision_jobs (project_id, pack, status, last_line, error, started_at)
		VALUES (1, 'architecture-pack', 'generating', 'running architecture-map', NULL, '2026-07-24T00:00:00Z')`)

	resp := getToolsResponse(t, srv.URL)
	var ap *architectureProjectDTO
	for i := range resp.Architecture.Projects {
		if resp.Architecture.Projects[i].ID == 1 {
			ap = &resp.Architecture.Projects[i]
			break
		}
	}
	if ap == nil {
		t.Fatal("project 1 not found in architecture.projects")
	}
	if ap.Provision == nil {
		t.Fatal("provision = null, want the seeded job")
	}
	if ap.Provision.State != "generating" {
		t.Errorf("provision.state = %q, want generating", ap.Provision.State)
	}
	if ap.Provision.LastLine != "running architecture-map" {
		t.Errorf("provision.lastLine = %q, want 'running architecture-map'", ap.Provision.LastLine)
	}
	if ap.Provision.Error != "" {
		t.Errorf("provision.error = %q, want empty", ap.Provision.Error)
	}

	// Project 2 (telemetry-only, no job) — appears via artifact, provision:null.
	path2 := projectPath(t, srv.URL, "2")
	archOut2 := filepath.Join(path2, "architecture-out")
	os.RemoveAll(archOut2)
	t.Cleanup(func() { os.RemoveAll(archOut2) })
	if err := os.MkdirAll(archOut2, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(archOut2, "architecture-map.html"), []byte("<html>arch2</html>"), 0o644); err != nil {
		t.Fatal(err)
	}

	resp2 := getToolsResponse(t, srv.URL)
	var ap2 *architectureProjectDTO
	for i := range resp2.Architecture.Projects {
		if resp2.Architecture.Projects[i].ID == 2 {
			ap2 = &resp2.Architecture.Projects[i]
			break
		}
	}
	if ap2 == nil {
		t.Fatal("project 2 not found in architecture.projects")
	}
	if ap2.Provision != nil {
		t.Errorf("provision = %+v for a project with no job, want null", ap2.Provision)
	}
}

// ── graft section (graft-pack) ───────────────────────────────────────────────
//
// The graft DTO is read-only and exec-free: it stats <graphDir>/.graph/wiring.json
// and reads the counts out of that file's `meta` header. These tests pin the
// three things that would silently mislead an operator — a project with the pack
// but no index reading as broken rather than un-built, a relocated index reading
// as absent, and a traversal in graphDir escaping the project directory.

// writeWiring drops a wiring.json with the real 0.16.0 top-level shape (meta
// first, then nodes/edges) under <dir>/.graph/.
func writeWiring(t *testing.T, dir string, nodes, edges int) {
	t.Helper()
	g := filepath.Join(dir, ".graph")
	if err := os.MkdirAll(g, 0o755); err != nil {
		t.Fatal(err)
	}
	body := `{"meta":{"version":1,"nodeCount":` + strconv.Itoa(nodes) +
		`,"edgeCount":` + strconv.Itoa(edges) +
		`,"languages":["typescript"],"scopes":[]},"nodes":[],"edges":[]}`
	if err := os.WriteFile(filepath.Join(g, "wiring.json"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// writeProjectJSON drops a .claude/project.json, the overlay graftDTO reads
// graft.graphDir out of.
func writeProjectJSON(t *testing.T, projectDir, body string) {
	t.Helper()
	dir := filepath.Join(projectDir, ".claude")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "project.json"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestToolsDashGraft(t *testing.T) {
	srv, _ := projectsTestServer(t)
	attachStubToolManager(t)
	stubLookPath(t, nil) // graft on PATH

	path := projectPath(t, srv.URL, "1")
	writeProjectSettings(t, path, `{
		"enabledPlugins": {"core@swarmery": true, "graft-pack@swarmery": true}
	}`)
	writeWiring(t, filepath.Join(path, "graft"), 412, 1180)

	resp := getToolsResponse(t, srv.URL)
	if !resp.Graft.Available {
		t.Error("graft.available = false with lookPath stubbed to succeed, want true")
	}
	if len(resp.Graft.Projects) != 1 {
		t.Fatalf("graft.projects len = %d, want 1 (%+v)", len(resp.Graft.Projects), resp.Graft.Projects)
	}
	p := resp.Graft.Projects[0]
	if p.ID != 1 || p.Slug != "managed" {
		t.Errorf("graft project = id %d slug %q, want id 1 slug managed", p.ID, p.Slug)
	}
	if !p.HasGraph {
		t.Error("hasGraph = false with wiring.json present, want true")
	}
	if p.GraphDir != "graft" {
		t.Errorf("graphDir = %q, want graft (the default)", p.GraphDir)
	}
	if p.Nodes != 412 || p.Edges != 1180 {
		t.Errorf("nodes/edges = %d/%d, want 412/1180 from meta", p.Nodes, p.Edges)
	}
	if p.BuiltAt == nil {
		t.Error("builtAt = null with wiring.json present, want its mtime")
	} else if _, err := time.Parse(time.RFC3339, *p.BuiltAt); err != nil {
		t.Errorf("builtAt = %q is not RFC3339: %v", *p.BuiltAt, err)
	}
}

func TestToolsDashGraftNoIndex(t *testing.T) {
	srv, _ := projectsTestServer(t)
	attachStubToolManager(t)
	stubLookPath(t, nil)

	path := projectPath(t, srv.URL, "1")
	writeProjectSettings(t, path, `{
		"enabledPlugins": {"core@swarmery": true, "graft-pack@swarmery": true}
	}`)

	// Pack on, nothing built yet. The project must still be listed — that is the
	// state the UI needs in order to say "enabled, never built" rather than
	// omitting the project and leaving the operator with no explanation.
	resp := getToolsResponse(t, srv.URL)
	if len(resp.Graft.Projects) != 1 {
		t.Fatalf("graft.projects len = %d, want 1 even with no index", len(resp.Graft.Projects))
	}
	p := resp.Graft.Projects[0]
	if p.HasGraph {
		t.Error("hasGraph = true with no wiring.json, want false")
	}
	if p.BuiltAt != nil {
		t.Errorf("builtAt = %v with no index, want null", *p.BuiltAt)
	}
	if p.Nodes != 0 || p.Edges != 0 {
		t.Errorf("nodes/edges = %d/%d with no index, want 0/0", p.Nodes, p.Edges)
	}
}

func TestToolsDashGraftUnavailable(t *testing.T) {
	srv, _ := projectsTestServer(t)
	attachStubToolManager(t)
	stubLookPath(t, errors.New("not found"))

	path := projectPath(t, srv.URL, "1")
	writeProjectSettings(t, path, `{
		"enabledPlugins": {"core@swarmery": true, "graft-pack@swarmery": true}
	}`)
	writeWiring(t, filepath.Join(path, "graft"), 3, 4)

	// The CLI is gone but the artifacts remain. available=false is the signal the
	// UI reports on — the pack is enabled and the graph on disk is now orphaned.
	resp := getToolsResponse(t, srv.URL)
	if resp.Graft.Available {
		t.Error("graft.available = true with lookPath failing, want false")
	}
	if len(resp.Graft.Projects) != 1 || !resp.Graft.Projects[0].HasGraph {
		t.Errorf("project should still be listed with its graph: %+v", resp.Graft.Projects)
	}
}

func TestToolsDashGraftCustomGraphDir(t *testing.T) {
	srv, _ := projectsTestServer(t)
	attachStubToolManager(t)
	stubLookPath(t, nil)

	path := projectPath(t, srv.URL, "1")
	writeProjectSettings(t, path, `{
		"enabledPlugins": {"core@swarmery": true, "graft-pack@swarmery": true}
	}`)
	writeProjectJSON(t, path, `{"name":"t","codePath":".","enabledPacks":[],"graft":{"graphDir":".ctx"}}`)
	writeWiring(t, filepath.Join(path, ".ctx"), 7, 9)

	resp := getToolsResponse(t, srv.URL)
	if len(resp.Graft.Projects) != 1 {
		t.Fatalf("graft.projects len = %d, want 1", len(resp.Graft.Projects))
	}
	p := resp.Graft.Projects[0]
	if p.GraphDir != ".ctx" {
		t.Errorf("graphDir = %q, want .ctx from project.json", p.GraphDir)
	}
	if !p.HasGraph || p.Nodes != 7 || p.Edges != 9 {
		t.Errorf("relocated index not found: hasGraph=%v nodes=%d edges=%d", p.HasGraph, p.Nodes, p.Edges)
	}
}

func TestToolsDashGraftGraphDirEscape(t *testing.T) {
	srv, _ := projectsTestServer(t)
	attachStubToolManager(t)
	stubLookPath(t, nil)

	path := projectPath(t, srv.URL, "1")
	writeProjectSettings(t, path, `{
		"enabledPlugins": {"core@swarmery": true, "graft-pack@swarmery": true}
	}`)
	// graphDir is operator input; a traversal must not be followed out of the
	// project directory, even to stat a file.
	writeProjectJSON(t, path, `{"name":"t","codePath":".","enabledPacks":[],"graft":{"graphDir":"../../etc"}}`)

	resp := getToolsResponse(t, srv.URL)
	if len(resp.Graft.Projects) != 1 {
		t.Fatalf("graft.projects len = %d, want 1", len(resp.Graft.Projects))
	}
	if resp.Graft.Projects[0].HasGraph {
		t.Error("hasGraph = true for a graphDir that escapes the project, want false")
	}
}

func TestToolsDashGraftPackOffOmitsProject(t *testing.T) {
	srv, _ := projectsTestServer(t)
	attachStubToolManager(t)
	stubLookPath(t, nil)

	path := projectPath(t, srv.URL, "1")
	writeProjectSettings(t, path, `{"enabledPlugins": {"core@swarmery": true}}`)
	writeWiring(t, filepath.Join(path, "graft"), 5, 5)

	// An index on disk is not opt-in. Without the pack enabled the project stays
	// off the list, exactly as it does for serena and graphify.
	resp := getToolsResponse(t, srv.URL)
	if len(resp.Graft.Projects) != 0 {
		t.Errorf("graft.projects = %+v, want empty without graft-pack enabled", resp.Graft.Projects)
	}
}
