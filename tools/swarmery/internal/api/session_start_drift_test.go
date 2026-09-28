package api

import (
	"database/sql"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
)

// postRaw POSTs an unparsed body and asserts the status — the session-start
// hook takes non-JSON on its 204 path, which doJSON cannot express.
func postRaw(t *testing.T, url, body string, wantStatus int) {
	t.Helper()
	resp, err := http.Post(url, "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatalf("POST %s: %v", url, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != wantStatus {
		t.Fatalf("POST %s: status %d, want %d", url, resp.StatusCode, wantStatus)
	}
}

// driftHandler returns a Handler over a migrated DB holding one project.
func driftHandler(t *testing.T) (*Handler, *sql.DB, string) {
	t.Helper()
	srv, db := projectsTestServer(t)
	return &Handler{DB: db}, db, projectPath(t, srv.URL, "1")
}

func TestDriftContextRendersActiveErrorFindings(t *testing.T) {
	h, db, path := driftHandler(t)
	seedFinding(t, db, pluginTarget("core@swarmery", path), "plugin_enabled_not_installed",
		"error", "enabled here, but installed only for /Volumes/Work/Skygor/scripts", "")

	got := h.driftContext(path)
	if got == "" {
		t.Fatal("want an injected context for a project with an active error finding")
	}
	if !strings.Contains(got, "core@swarmery") {
		t.Errorf("context must name the plugin, got %q", got)
	}
	if !strings.Contains(got, "/Volumes/Work/Skygor/scripts") {
		t.Errorf("context must carry the finding message, got %q", got)
	}
	if !strings.Contains(got, "NOT loaded") {
		t.Errorf("context must say the plugins are not loaded, got %q", got)
	}
}

func TestDriftContextEmptyWhenClean(t *testing.T) {
	h, _, path := driftHandler(t)
	if got := h.driftContext(path); got != "" {
		t.Errorf("context = %q, want empty for a clean project", got)
	}
}

func TestDriftContextEmptyForUnknownCWD(t *testing.T) {
	h, _, _ := driftHandler(t)
	if got := h.driftContext(""); got != "" {
		t.Errorf("context = %q, want empty for an empty cwd", got)
	}
}

// A finding belonging to another project must never be injected here.
func TestDriftContextIgnoresOtherProjects(t *testing.T) {
	h, db, path := driftHandler(t)
	seedFinding(t, db, pluginTarget("core@swarmery", "/some/other/project"),
		"plugin_enabled_not_installed", "error", "not mine", "")

	if got := h.driftContext(path); got != "" {
		t.Errorf("context = %q, want empty — another project's finding leaked", got)
	}
}

// Only errors inject: a warn costs tokens in every session for a cosmetic issue.
func TestDriftContextIgnoresWarnings(t *testing.T) {
	h, db, path := driftHandler(t)
	seedFinding(t, db, pluginTarget("core@swarmery", path), "plugin_version_behind",
		"warn", "installed 1.2.0, marketplace has 2.7.0", "")

	if got := h.driftContext(path); got != "" {
		t.Errorf("context = %q, want empty — only error findings inject", got)
	}
}

func TestDriftContextIgnoresResolvedFindings(t *testing.T) {
	h, db, path := driftHandler(t)
	seedFinding(t, db, pluginTarget("core@swarmery", path), "plugin_enabled_not_installed",
		"error", "already fixed", "2026-07-28T10:00:00Z")

	if got := h.driftContext(path); got != "" {
		t.Errorf("context = %q, want empty for a resolved finding", got)
	}
}

// A registered ancestor row (live: one session run at cwd=$HOME) must not
// capture the project's own cwd — the lookup would then match findings
// against the ancestor's path and this project's error would go silent.
func TestDriftContextExactProjectWinsOverAncestor(t *testing.T) {
	h, db, path := driftHandler(t)
	execSQL(t, db, `INSERT INTO projects (path, slug, name, first_seen) VALUES (?, 'parent', 'parent', '2026-09-28T00:00:00Z')`,
		filepath.Dir(path))
	seedFinding(t, db, pluginTarget("core@swarmery", path), "plugin_enabled_not_installed",
		"error", "enabled here, but not installed", "")

	if got := h.driftContext(path); !strings.Contains(got, "core@swarmery") {
		t.Errorf("context = %q, want this project's finding — the ancestor row captured its cwd", got)
	}
}

// Machine-wide blindness belongs on the dashboard, not inside every session on
// the box — plugin:detector has no project and must never be injected.
func TestDriftContextNeverInjectsDetectorTarget(t *testing.T) {
	h, db, path := driftHandler(t)
	seedFinding(t, db, "plugin:detector", "plugin_detector_unavailable",
		"error", "claude binary not found", "")

	if got := h.driftContext(path); got != "" {
		t.Errorf("context = %q, want empty — plugin:detector must not be injected", got)
	}
}

func TestDriftContextCapsLineCount(t *testing.T) {
	h, db, path := driftHandler(t)
	for _, name := range []string{"a", "b", "c", "d", "e", "f", "g"} {
		seedFinding(t, db, pluginTarget(name+"@swarmery", path),
			"plugin_enabled_not_installed", "error", "missing", "")
	}

	got := h.driftContext(path)
	if n := strings.Count(got, "\n- "); n != maxDriftContextLines {
		t.Errorf("injected %d finding lines, want the %d-line cap (context: %q)",
			n, maxDriftContextLines, got)
	}
}

func TestDriftContextKicksRefreshOnlyWhenItSpeaks(t *testing.T) {
	h, db, path := driftHandler(t)
	called := make(chan struct{}, 4)
	AttachDriftRefresher(func() { called <- struct{}{} })
	t.Cleanup(func() { driftRefresher = nil })

	if got := h.driftContext(path); got != "" {
		t.Fatalf("setup: want an empty context, got %q", got)
	}
	select {
	case <-called:
		t.Fatal("refresher must not run when there is nothing to say")
	default:
	}

	seedFinding(t, db, pluginTarget("core@swarmery", path), "plugin_enabled_not_installed",
		"error", "missing", "")
	if got := h.driftContext(path); got == "" {
		t.Fatal("want a context once a finding is active")
	}
	<-called // the refresh is kicked asynchronously; a hang here is the failure
}

// ── the endpoint's pre-existing 204 paths must be untouched ──────────────────

func TestSessionStartUndecodableBodyStill204(t *testing.T) {
	srv, db := projectsTestServer(t)
	path := projectPath(t, srv.URL, "1")
	// An active finding exists, so a leaked drift lookup would answer 200.
	seedFinding(t, db, pluginTarget("core@swarmery", path), "plugin_enabled_not_installed",
		"error", "missing", "")

	postRaw(t, srv.URL+"/api/hooks/session-start", "not json", 204)
}

func TestSessionStartNonClaudePIDStill204(t *testing.T) {
	srv, db := projectsTestServer(t)
	path := projectPath(t, srv.URL, "1")
	seedFinding(t, db, pluginTarget("core@swarmery", path), "plugin_enabled_not_installed",
		"error", "missing", "")

	// PID 1 is launchd/init, never a claude process: the identity gate must
	// return before the drift lookup is ever reached.
	postRaw(t, srv.URL+"/api/hooks/session-start",
		`{"session_id":"sid-1","pid":1,"cwd":"`+path+`"}`, 204)
}
