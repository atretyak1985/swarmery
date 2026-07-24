package api

// Phase 3 tests — the auto-provision toggle hook: enabling a pack enqueues +
// runs a provision job (inline via the provisionGo seam), while disabling it or
// the SWARMERY_AUTOPROVISION kill-switch create no job. A stub Runner stands in
// for the real claude binary — no process is ever spawned.

import (
	"context"
	"database/sql"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/provision"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/store"
)

// provisionStubRunner records the claude invocations and returns canned output;
// it never touches a real process.
type provisionStubRunner struct{ calls int }

func (r *provisionStubRunner) Claude(context.Context, string, string, ...string) (string, error) {
	r.calls++
	return "ok", nil
}

// archManifest is a catalog fixture that knows architecture-pack (so enabling it
// passes the putProjectPlugin known-plugin check).
const archManifest = `{
	"name": "swarmery",
	"metadata": {"version": "1.13.0"},
	"plugins": [
		{"name": "core", "source": "./plugins/core", "description": "the core plugin"},
		{"name": "architecture-pack", "source": "./plugins/architecture-pack", "description": "architecture pack"}
	]
}`

// provisionTestServer builds an httptest server whose provision pipeline runs
// INLINE (provisionGo seam) against a stub Runner, seeds one managed project
// (id 1) under an onboard root with a real settings.json, and points the plugin
// catalog at a clone that knows architecture-pack. Returns the server, db, and
// the stub Runner so tests can assert invocation counts.
func provisionTestServer(t *testing.T) (*httptest.Server, *sql.DB, *provisionStubRunner) {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "provision-api.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	root := t.TempDir()
	managedPath := filepath.Join(root, "managed")
	writeProjectSettings(t, managedPath, `{
		"extraKnownMarketplaces": {"swarmery": {"source": {"source": "github", "repo": "atretyak1985/swarmery"}}},
		"enabledPlugins": {"core@swarmery": true}
	}`)
	execSQL(t, db, `INSERT INTO projects (id, path, slug, name, first_seen, last_activity, archived)
		VALUES (1, ?, 'managed', 'Managed', '2026-07-10T00:00:00Z', '2026-07-14T00:00:00Z', 0)`, managedPath)

	prev := onboardCfg
	AttachOnboard(OnboardConfig{Roots: []string{root}})
	t.Cleanup(func() { onboardCfg = prev })

	seedPluginCatalog(t, archManifest)

	runner := &provisionStubRunner{}
	h := &Handler{
		DB:          db,
		Provision:   provision.NewService(db, runner),
		provisionGo: func(fn func()) { fn() },
	}
	mux := http.NewServeMux()
	Routes(mux, h)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv, db, runner
}

// latestProvisionJob returns (status, ok) for the newest provision_jobs row of
// (projectID, pack); ok=false when no row exists.
func latestProvisionJob(t *testing.T, db *sql.DB, projectID int64, pack string) (string, bool) {
	t.Helper()
	var status string
	err := db.QueryRow(
		`SELECT status FROM provision_jobs WHERE project_id=? AND pack=? ORDER BY id DESC LIMIT 1`,
		projectID, pack).Scan(&status)
	if err == sql.ErrNoRows {
		return "", false
	}
	if err != nil {
		t.Fatalf("query provision job: %v", err)
	}
	return status, true
}

func TestPutPluginProvisionEnqueuesOnEnable(t *testing.T) {
	srv, db, runner := provisionTestServer(t)

	out := doJSON(t, "PUT", srv.URL+"/api/projects/1/plugins/architecture-pack",
		map[string]any{"enabled": true}, 200)
	if out["changed"] != true || out["enabled"] != true {
		t.Fatalf("enable body = %v, want changed=true enabled=true", out)
	}

	status, ok := latestProvisionJob(t, db, 1, "architecture-pack")
	if !ok {
		t.Fatal("no provision_jobs row after a successful enable, want one")
	}
	// Inline runner → the pipeline has already reached a terminal status. The
	// temp project is not a git repo so architectureFresh is false; the stub
	// Runner succeeds through generate → 'done'.
	if status != "done" {
		t.Errorf("provision status = %q, want terminal 'done'", status)
	}
	if runner.calls == 0 {
		t.Error("stub Runner never invoked — the pipeline did not run")
	}
}

func TestPutPluginProvisionSkipsOnDisable(t *testing.T) {
	srv, db, _ := provisionTestServer(t)

	// Enable then disable — the disable must not enqueue a fresh job. Enable
	// first drives one job to terminal so a later in-flight check is moot.
	doJSON(t, "PUT", srv.URL+"/api/projects/1/plugins/architecture-pack",
		map[string]any{"enabled": true}, 200)
	if _, ok := latestProvisionJob(t, db, 1, "architecture-pack"); !ok {
		t.Fatal("expected a job after enable (harness precondition)")
	}

	// Count rows, disable, re-count — disable adds nothing.
	before := provisionJobCount(t, db)
	out := doJSON(t, "PUT", srv.URL+"/api/projects/1/plugins/architecture-pack",
		map[string]any{"enabled": false}, 200)
	if out["enabled"] != false || out["changed"] != true {
		t.Fatalf("disable body = %v, want enabled=false changed=true", out)
	}
	if after := provisionJobCount(t, db); after != before {
		t.Errorf("disable created %d provision job(s), want 0", after-before)
	}
}

func TestPutPluginProvisionKillSwitch(t *testing.T) {
	srv, db, runner := provisionTestServer(t)
	t.Setenv("SWARMERY_AUTOPROVISION", "0")

	out := doJSON(t, "PUT", srv.URL+"/api/projects/1/plugins/architecture-pack",
		map[string]any{"enabled": true}, 200)
	if out["changed"] != true {
		t.Fatalf("enable body = %v, want changed=true (toggle still works)", out)
	}
	if _, ok := latestProvisionJob(t, db, 1, "architecture-pack"); ok {
		t.Error("provision job created with SWARMERY_AUTOPROVISION=0, want none")
	}
	if runner.calls != 0 {
		t.Errorf("stub Runner invoked %d time(s) with kill-switch on, want 0", runner.calls)
	}
}

func provisionJobCount(t *testing.T, db *sql.DB) int {
	t.Helper()
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM provision_jobs`).Scan(&n); err != nil {
		t.Fatalf("count provision jobs: %v", err)
	}
	return n
}
