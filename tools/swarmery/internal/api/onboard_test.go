package api

// POST /api/projects/onboard — the dashboard onboarding surface. Fenced by an
// explicit root allow-list; empty allow-list disables the endpoint. Real
// tmpdirs throughout — never the machine's filesystem outside t.TempDir().

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/store"
)

// onboardServer builds a test server with the onboarding endpoint attached to
// cfg. Returns the server and the tmp root the allow-list is scoped to.
func onboardServer(t *testing.T, cfg OnboardConfig) (*httptest.Server, string) {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "onboard.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	AttachOnboard(cfg)
	t.Cleanup(func() { AttachOnboard(OnboardConfig{}) })

	h, err := NewServer(db, false)
	if err != nil {
		t.Fatalf("new server: %v", err)
	}
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return srv, ""
}

func TestOnboardDisabledWithoutRoots(t *testing.T) {
	srv, _ := onboardServer(t, OnboardConfig{}) // no roots → disabled
	doJSON(t, http.MethodPost, srv.URL+"/api/projects/onboard",
		map[string]any{"slug": "x", "path": "/tmp/x"}, http.StatusForbidden)
}

func TestOnboardHappyPath(t *testing.T) {
	root := t.TempDir()
	ws := t.TempDir()
	proj := filepath.Join(root, "my-project")
	if err := os.MkdirAll(proj, 0o755); err != nil {
		t.Fatal(err)
	}
	srv, _ := onboardServer(t, OnboardConfig{Roots: []string{root}, WorkspaceRoot: ws})

	out := doJSON(t, http.MethodPost, srv.URL+"/api/projects/onboard",
		map[string]any{"slug": "my-project", "path": proj, "packs": []string{"web-pack"}},
		http.StatusCreated)

	if out["slug"] != "my-project" {
		t.Errorf("slug = %v", out["slug"])
	}
	if _, err := os.Stat(filepath.Join(proj, ".claude", "settings.json")); err != nil {
		t.Errorf("settings.json not written: %v", err)
	}
	if _, err := os.Stat(filepath.Join(ws, "my-project", "workspace", "working")); err != nil {
		t.Errorf("workspace namespace not carved: %v", err)
	}
}

func TestOnboardConfigReportsDefaults(t *testing.T) {
	ws := t.TempDir()
	root := t.TempDir()
	srv, _ := onboardServer(t, OnboardConfig{Roots: []string{root}, WorkspaceRoot: ws})

	out := doJSON(t, http.MethodGet, srv.URL+"/api/projects/onboard/config", nil, http.StatusOK)
	if out["enabled"] != true {
		t.Errorf("enabled = %v, want true", out["enabled"])
	}
	if out["workspaceRoot"] != ws {
		t.Errorf("workspaceRoot = %v, want %v", out["workspaceRoot"], ws)
	}
}

func TestOnboardConfigDisabledWhenNoRoots(t *testing.T) {
	srv, _ := onboardServer(t, OnboardConfig{})
	out := doJSON(t, http.MethodGet, srv.URL+"/api/projects/onboard/config", nil, http.StatusOK)
	if out["enabled"] != false {
		t.Errorf("enabled = %v, want false", out["enabled"])
	}
}

func TestOnboardWorkspaceRootOverride(t *testing.T) {
	root := t.TempDir()
	serverWS := t.TempDir()
	// An override is fenced like the target path, so it must sit under a root.
	overrideWS := filepath.Join(root, "ws-override")
	if err := os.MkdirAll(overrideWS, 0o755); err != nil {
		t.Fatal(err)
	}
	proj := filepath.Join(root, "ovr-project")
	if err := os.MkdirAll(proj, 0o755); err != nil {
		t.Fatal(err)
	}
	srv, _ := onboardServer(t, OnboardConfig{Roots: []string{root}, WorkspaceRoot: serverWS})

	out := doJSON(t, http.MethodPost, srv.URL+"/api/projects/onboard",
		map[string]any{"slug": "ovr-project", "path": proj, "workspaceRoot": overrideWS},
		http.StatusCreated)

	// The fence resolves symlinks (macOS tmp is /var → /private/var), so the
	// echo is the resolved directory, exactly as for the target path.
	wantWS, err := filepath.EvalSymlinks(overrideWS)
	if err != nil {
		t.Fatal(err)
	}
	if out["workspaceRoot"] != wantWS {
		t.Errorf("workspaceRoot echo = %v, want %v", out["workspaceRoot"], wantWS)
	}
	// Namespace carved under the OVERRIDE, not the server default.
	if _, err := os.Stat(filepath.Join(overrideWS, "ovr-project", "workspace", "working")); err != nil {
		t.Errorf("namespace not carved under override: %v", err)
	}
	if _, err := os.Stat(filepath.Join(serverWS, "ovr-project")); !os.IsNotExist(err) {
		t.Errorf("server-default workspace should be untouched")
	}
}

// A workspaceRoot override outside every onboarding root is refused before
// anything is written: onboarding carves directories and writes
// overlay/project.json under it, so an unfenced override would let a caller
// create files anywhere the daemon can write.
func TestOnboardRejectsWorkspaceRootOutsideRoots(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir() // a sibling tmp dir, NOT under root
	proj := filepath.Join(root, "ws-evil")
	if err := os.MkdirAll(proj, 0o755); err != nil {
		t.Fatal(err)
	}
	srv, _ := onboardServer(t, OnboardConfig{Roots: []string{root}, WorkspaceRoot: t.TempDir()})

	doJSON(t, http.MethodPost, srv.URL+"/api/projects/onboard",
		map[string]any{"slug": "ws-evil", "path": proj, "workspaceRoot": outside}, http.StatusForbidden)

	if _, err := os.Stat(filepath.Join(outside, "ws-evil")); !os.IsNotExist(err) {
		t.Errorf("nothing may be carved under a refused workspace root (stat err = %v)", err)
	}
	if _, err := os.Stat(filepath.Join(proj, ".claude")); !os.IsNotExist(err) {
		t.Errorf("the target must stay untouched when the workspace root is refused (stat err = %v)", err)
	}
}

func TestOnboardRejectsPathOutsideRoots(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir() // a sibling tmp dir, NOT under root
	srv, _ := onboardServer(t, OnboardConfig{Roots: []string{root}, WorkspaceRoot: t.TempDir()})

	doJSON(t, http.MethodPost, srv.URL+"/api/projects/onboard",
		map[string]any{"slug": "evil", "path": outside}, http.StatusForbidden)
}

func TestOnboardRejectsTraversalEscape(t *testing.T) {
	root := t.TempDir()
	// root/../<sibling> resolves outside root even though it starts with root.
	escape := filepath.Join(root, "..", filepath.Base(t.TempDir()))
	srv, _ := onboardServer(t, OnboardConfig{Roots: []string{root}, WorkspaceRoot: t.TempDir()})

	doJSON(t, http.MethodPost, srv.URL+"/api/projects/onboard",
		map[string]any{"slug": "evil", "path": escape}, http.StatusForbidden)
}

func TestOnboardRejectsMissingDir(t *testing.T) {
	root := t.TempDir()
	srv, _ := onboardServer(t, OnboardConfig{Roots: []string{root}, WorkspaceRoot: t.TempDir()})

	doJSON(t, http.MethodPost, srv.URL+"/api/projects/onboard",
		map[string]any{"slug": "ghost", "path": filepath.Join(root, "does-not-exist")},
		http.StatusBadRequest)
}

func TestOnboardRejectsBadSlug(t *testing.T) {
	root := t.TempDir()
	srv, _ := onboardServer(t, OnboardConfig{Roots: []string{root}, WorkspaceRoot: t.TempDir()})

	doJSON(t, http.MethodPost, srv.URL+"/api/projects/onboard",
		map[string]any{"slug": "Bad_Slug", "path": root}, http.StatusBadRequest)
}
