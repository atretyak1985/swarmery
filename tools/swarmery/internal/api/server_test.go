package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"testing/fstest"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/ingest"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/store"
)

func testServer(t *testing.T) *httptest.Server {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	if _, err := ingest.File(db, filepath.Join("..", "..", "testdata", "fixtures", "subagent-session.jsonl")); err != nil {
		t.Fatalf("ingest fixture: %v", err)
	}
	h, err := NewServer(db)
	if err != nil {
		t.Fatalf("new server: %v", err)
	}
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return srv
}

func getJSON(t *testing.T, url string, out any) {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET %s: status %d", url, resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "application/json" {
		t.Errorf("GET %s: Content-Type = %q, want application/json", url, ct)
	}
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		t.Fatalf("GET %s: decode: %v", url, err)
	}
}

func TestEndpoints(t *testing.T) {
	srv := testServer(t)

	var projects []map[string]any
	getJSON(t, srv.URL+"/api/projects", &projects)
	if len(projects) != 1 {
		t.Fatalf("projects = %d, want 1", len(projects))
	}

	var sessions []map[string]any
	getJSON(t, srv.URL+"/api/sessions", &sessions)
	if len(sessions) != 1 {
		t.Fatalf("sessions = %d, want 1", len(sessions))
	}

	// Filters narrow correctly.
	var filtered []map[string]any
	getJSON(t, srv.URL+"/api/sessions?status=active", &filtered)
	if len(filtered) != 0 {
		t.Errorf("active sessions = %d, want 0 (fixture is completed)", len(filtered))
	}
	getJSON(t, srv.URL+"/api/sessions?status=completed", &filtered)
	if len(filtered) != 1 {
		t.Errorf("completed sessions = %d, want 1", len(filtered))
	}

	// Detail by numeric id and by session UUID, with turns/events/fileChanges.
	id := sessions[0]["id"].(float64)
	uuid := sessions[0]["sessionUuid"].(string)
	for _, key := range []string{
		srv.URL + "/api/sessions/" + jsonNum(id),
		srv.URL + "/api/sessions/" + uuid,
	} {
		var detail struct {
			Turns       []map[string]any `json:"turns"`
			Events      []map[string]any `json:"events"`
			FileChanges []map[string]any `json:"fileChanges"`
		}
		getJSON(t, key, &detail)
		if len(detail.Turns) != 5 {
			t.Errorf("%s: turns = %d, want 5", key, len(detail.Turns))
		}
		if len(detail.Events) != 7 {
			t.Errorf("%s: events = %d, want 7", key, len(detail.Events))
		}
		if detail.FileChanges == nil {
			t.Errorf("%s: fileChanges must be [], not null", key)
		}
	}

	// Unknown session → 404.
	resp, err := http.Get(srv.URL + "/api/sessions/does-not-exist")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("missing session status = %d, want 404", resp.StatusCode)
	}
}

func jsonNum(f float64) string {
	b, _ := json.Marshal(int64(f))
	return string(b)
}

// TestSPACacheHeaders pins the cache contract: index.html (and the SPA
// fallback for client-side routes) must never be cached across daemon
// upgrades, while content-hashed /assets/* may be cached forever.
func TestSPACacheHeaders(t *testing.T) {
	dist := fstest.MapFS{
		"index.html":              {Data: []byte("<!doctype html><title>spa</title>")},
		"assets/index-abc123.js":  {Data: []byte("console.log('bundle')")},
		"assets/index-abc123.css": {Data: []byte("body{}")},
	}
	srv := httptest.NewServer(spaHandler(dist))
	t.Cleanup(srv.Close)

	cases := []struct {
		path string
		want string
	}{
		{"/", "no-cache"},
		{"/index.html", "no-cache"},
		{"/sessions/42", "no-cache"}, // SPA fallback for a client-side route
		{"/assets/index-abc123.js", "public, max-age=31536000, immutable"},
		{"/assets/index-abc123.css", "public, max-age=31536000, immutable"},
	}
	for _, tc := range cases {
		resp, err := http.Get(srv.URL + tc.path)
		if err != nil {
			t.Fatalf("GET %s: %v", tc.path, err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Errorf("GET %s: status = %d, want 200", tc.path, resp.StatusCode)
		}
		if got := resp.Header.Get("Cache-Control"); got != tc.want {
			t.Errorf("GET %s: Cache-Control = %q, want %q", tc.path, got, tc.want)
		}
	}
}
