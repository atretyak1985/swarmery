package api

import (
	"net/http"
	"strings"
	"testing"
)

// projectsTestServer (projects_test.go) seeds: project 1 (managed, unarchived),
// project 2 (telemetry-only, unarchived), project 3 (archived).

func projectIDs(list []projectDTO) map[int64]bool {
	ids := map[int64]bool{}
	for _, p := range list {
		ids[p.ID] = true
	}
	return ids
}

func TestArchiveProjectRemovesFromListKeepsById(t *testing.T) {
	srv, _ := projectsTestServer(t)

	if ids := projectIDs(getProjectsList(t, srv.URL+"/api/projects")); !ids[1] {
		t.Fatal("project 1 should be in the default list before archive")
	}

	out := doJSON(t, http.MethodDelete, srv.URL+"/api/projects/1", nil, http.StatusOK)
	if out["archived"] != true {
		t.Errorf("archive body = %v, want {archived:true}", out)
	}

	// Gone from the default list…
	if ids := projectIDs(getProjectsList(t, srv.URL+"/api/projects")); ids[1] {
		t.Error("project 1 should be hidden from the default list after archive")
	}
	// …but present under include=archived…
	if ids := projectIDs(getProjectsList(t, srv.URL+"/api/projects?include=archived")); !ids[1] {
		t.Error("project 1 should reappear under include=archived")
	}
	// …and still reachable by direct id (reversible, not a destroy).
	resp, err := http.Get(srv.URL + "/api/projects/1")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("GET /api/projects/1 after archive = %d, want 200", resp.StatusCode)
	}
}

func TestRestoreProjectReappears(t *testing.T) {
	srv, _ := projectsTestServer(t)

	// Project 3 starts archived → absent from the default list.
	if ids := projectIDs(getProjectsList(t, srv.URL+"/api/projects")); ids[3] {
		t.Fatal("project 3 should start archived (hidden)")
	}

	out := doJSON(t, http.MethodPost, srv.URL+"/api/projects/3/restore", nil, http.StatusOK)
	if out["archived"] != false {
		t.Errorf("restore body = %v, want {archived:false}", out)
	}

	if ids := projectIDs(getProjectsList(t, srv.URL+"/api/projects")); !ids[3] {
		t.Error("project 3 should reappear in the default list after restore")
	}
}

func TestArchiveProjectBadID(t *testing.T) {
	srv, _ := projectsTestServer(t)
	doJSON(t, http.MethodDelete, srv.URL+"/api/projects/not-a-number", nil, http.StatusBadRequest)
}

func TestArchiveProjectNotFound(t *testing.T) {
	srv, _ := projectsTestServer(t)
	doJSON(t, http.MethodDelete, srv.URL+"/api/projects/9999", nil, http.StatusNotFound)
}

func TestArchiveProjectRejectsForeignOrigin(t *testing.T) {
	srv, _ := projectsTestServer(t)

	req, err := http.NewRequest(http.MethodDelete, srv.URL+"/api/projects/1", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Origin", "https://evil.example.com")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("cross-origin archive = %d, want 403", resp.StatusCode)
	}
	// The fence must run before the DB write — project 1 stays in the list.
	if ids := projectIDs(getProjectsList(t, srv.URL+"/api/projects")); !ids[1] {
		t.Error("rejected cross-origin request must not have archived project 1")
	}
}

// A same-origin (localhost) request passes the fence.
func TestArchiveProjectAllowsLocalOrigin(t *testing.T) {
	srv, _ := projectsTestServer(t)
	// srv.URL is http://127.0.0.1:PORT — a local origin.
	origin := srv.URL
	if !strings.HasPrefix(origin, "http://127.0.0.1") {
		t.Skipf("unexpected test server origin %q", origin)
	}
	req, err := http.NewRequest(http.MethodDelete, srv.URL+"/api/projects/1", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header = dashboardHeader(t, origin)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("local-origin archive with a dashboard session = %d, want 200", resp.StatusCode)
	}
}
