package api

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/ingest"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/store"
)

func testServer(t *testing.T) *httptest.Server {
	t.Helper()
	srv, _ := testServerWithDB(t)
	return srv
}

// testServerWithDB is testServer plus the underlying DB handle, for tests that
// need to assert against storage directly (e.g. total-cost invariants that span
// turns the API deliberately does not expose one-by-one).
func testServerWithDB(t *testing.T) (*httptest.Server, *sql.DB) {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	if _, err := ingest.File(db, filepath.Join("..", "..", "testdata", "fixtures", "subagent-session.jsonl")); err != nil {
		t.Fatalf("ingest fixture: %v", err)
	}
	h, err := NewServer(db, false)
	if err != nil {
		t.Fatalf("new server: %v", err)
	}
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return srv, db
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

	var page struct {
		Sessions []map[string]any `json:"sessions"`
	}
	getJSON(t, srv.URL+"/api/sessions", &page)
	sessions := page.Sessions
	if len(sessions) != 1 {
		t.Fatalf("sessions = %d, want 1", len(sessions))
	}

	// Filters narrow correctly.
	var filtered struct {
		Sessions []map[string]any `json:"sessions"`
	}
	getJSON(t, srv.URL+"/api/sessions?status=active", &filtered)
	if len(filtered.Sessions) != 0 {
		t.Errorf("active sessions = %d, want 0 (fixture is completed)", len(filtered.Sessions))
	}
	getJSON(t, srv.URL+"/api/sessions?status=completed", &filtered)
	if len(filtered.Sessions) != 1 {
		t.Errorf("completed sessions = %d, want 1", len(filtered.Sessions))
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

// TestSPAContentSecurityPolicy pins the dashboard shell's CSP: every SPA
// response (index, client-side route fallback, static asset) carries spaCSP,
// and that policy keeps the hardening directives it exists for.
func TestSPAContentSecurityPolicy(t *testing.T) {
	dist := fstest.MapFS{
		"index.html":             {Data: []byte("<!doctype html><title>spa</title>")},
		"assets/index-abc123.js": {Data: []byte("console.log('bundle')")},
		"theme-boot.js":          {Data: []byte("void 0")},
	}
	srv := httptest.NewServer(spaHandler(dist))
	t.Cleanup(srv.Close)

	for _, path := range []string{"/", "/sessions/42", "/assets/index-abc123.js", "/theme-boot.js"} {
		resp, err := http.Get(srv.URL + path)
		if err != nil {
			t.Fatalf("GET %s: %v", path, err)
		}
		resp.Body.Close()
		if got := resp.Header.Get("Content-Security-Policy"); got != spaCSP {
			t.Errorf("GET %s: Content-Security-Policy = %q, want spaCSP", path, got)
		}
	}
	for _, directive := range []string{
		"default-src 'self'", "script-src 'self';", "object-src 'none'",
		"base-uri 'self'", "frame-ancestors 'self'",
		// The Serena tab frames serena's own loopback origin, not the daemon.
		"frame-src 'self' http://127.0.0.1:* http://localhost:*;",
	} {
		if !strings.Contains(spaCSP, directive) {
			t.Errorf("spaCSP lacks %q: %s", directive, spaCSP)
		}
	}
	if strings.Contains(spaCSP, "'unsafe-eval'") || strings.Contains(strings.Split(spaCSP, "style-src")[0], "'unsafe-inline'") {
		t.Errorf("spaCSP must not allow eval or inline scripts: %s", spaCSP)
	}
}

// TestSecurityHeadersOnEveryResponse pins the daemon-wide middleware: API
// responses (success and error alike) get nosniff plus the baseline
// frame-ancestors CSP, the SPA shell gets its own policy in place of the
// baseline, and a jailed artifact keeps its sandbox policy — exactly one CSP
// each, never the baseline stacked on top.
func TestSecurityHeadersOnEveryResponse(t *testing.T) {
	srv, _ := projectsTestServer(t)
	seedArchitectureOut(t, srv.URL)

	cases := []struct {
		path    string
		wantCSP string
	}{
		{"/api/projects", baselineCSP},
		{"/api/sessions/does-not-exist", baselineCSP}, // 404 from a handler
		{"/api/projects/bad/architecture/x.html", artifactCSP},
		{"/api/projects/1/architecture/architecture-map.html", artifactCSP},
		{"/", spaCSP},
		{"/sessions/42", spaCSP},
	}
	for _, tc := range cases {
		resp, err := http.Get(srv.URL + tc.path)
		if err != nil {
			t.Fatalf("GET %s: %v", tc.path, err)
		}
		resp.Body.Close()
		if got := resp.Header.Get("X-Content-Type-Options"); got != "nosniff" {
			t.Errorf("GET %s (%d): X-Content-Type-Options = %q, want nosniff", tc.path, resp.StatusCode, got)
		}
		csp := resp.Header.Values("Content-Security-Policy")
		if len(csp) != 1 || csp[0] != tc.wantCSP {
			t.Errorf("GET %s (%d): Content-Security-Policy = %q, want exactly [%q]", tc.path, resp.StatusCode, csp, tc.wantCSP)
		}
	}
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
