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
	// Three sections carry "projects": serena, graphify, architecture.
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
	if strings.Count(body, `"projects":[]`) != 3 {
		t.Errorf("empty lists must serialize as [] for all three tools (serena, graphify, architecture):\n%s", body)
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
