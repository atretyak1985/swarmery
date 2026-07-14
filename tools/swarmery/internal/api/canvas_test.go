package api

import (
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/store"
)

// canvasServer plants the minimum rows for the canvas-wave additions: a session
// whose first user turn carries prose (→ why), a Bash error later cleared by a
// Bash success (→ recovered), an unrelated un-cleared error, and a test_run
// event with parsed counts (→ stats test aggregation).
func canvasServer(t *testing.T) *httptest.Server {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "canvas.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	const tsFmt = "2006-01-02T15:04:05.000Z"
	today := time.Now().UTC().Format(tsFmt)
	mustExec := func(q string, args ...any) {
		t.Helper()
		if _, err := db.Exec(q, args...); err != nil {
			t.Fatalf("exec: %v\n%s", err, q)
		}
	}

	mustExec(`INSERT INTO projects (id, path, slug, first_seen) VALUES (1, '/work/alpha', '-work-alpha', ?)`, today)
	mustExec(`INSERT INTO sessions (id, project_id, session_uuid, status, started_at) VALUES (1, 1, 'u1', 'active', ?)`, today)
	mustExec(`INSERT INTO turns (session_id, seq, role, started_at, text) VALUES
		(1, 0, 'user',      ?, ?),
		(1, 1, 'assistant', ?, ?)`,
		today, "  Migrate email templates to the provider v2 API.\nKeep back-compat.  ",
		today, "On it.")
	// Bash error → later Bash ok = recovered; the Read error is never cleared.
	mustExec(`INSERT INTO events (session_id, ts, type, tool_name, status, dedup_key) VALUES
		(1, ?, 'tool_call', 'Bash', 'error', 'c1'),
		(1, ?, 'tool_call', 'Bash', 'ok',    'c2'),
		(1, ?, 'tool_call', 'Read', 'error', 'c3')`, today, today, today)
	mustExec(`INSERT INTO events (session_id, ts, type, tool_name, status, payload, dedup_key) VALUES
		(1, ?, 'test_run', 'Bash', 'ok', ?, 'c4')`,
		today, `{"passed":212,"failed":0,"skipped":3,"parsed":true}`)

	h, err := NewServer(db, false)
	if err != nil {
		t.Fatalf("new server: %v", err)
	}
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return srv
}

func TestSessionWhyAndRecovered(t *testing.T) {
	srv := canvasServer(t)

	var detail struct {
		Why       *string `json:"why"`
		Recovered int64   `json:"recovered"`
	}
	getJSON(t, srv.URL+"/api/sessions/1", &detail)

	if detail.Why == nil || *detail.Why != "Migrate email templates to the provider v2 API." {
		t.Errorf("why = %v, want the trimmed first line of the user turn", detail.Why)
	}
	if detail.Recovered != 1 {
		t.Errorf("recovered = %d, want 1 (Bash error cleared by a later Bash ok; Read error is not)", detail.Recovered)
	}
}

func TestStatsTodayTestAggregate(t *testing.T) {
	srv := canvasServer(t)

	var s struct {
		TestsPassed  *int64 `json:"tests_passed"`
		TestsFailed  *int64 `json:"tests_failed"`
		TestsSkipped *int64 `json:"tests_skipped"`
	}
	getJSON(t, srv.URL+"/api/stats/today", &s)

	if s.TestsPassed == nil || *s.TestsPassed != 212 {
		t.Errorf("tests_passed = %v, want 212", s.TestsPassed)
	}
	if s.TestsSkipped == nil || *s.TestsSkipped != 3 {
		t.Errorf("tests_skipped = %v, want 3", s.TestsSkipped)
	}
	if s.TestsFailed == nil || *s.TestsFailed != 0 {
		t.Errorf("tests_failed = %v, want 0", s.TestsFailed)
	}
}

func TestStatsTodayNoTestSignalOmitsQuality(t *testing.T) {
	// The stats server has no test_run events → the test fields are absent.
	srv := statsServer(t)

	var s map[string]any
	getJSON(t, srv.URL+"/api/stats/today", &s)
	for _, k := range []string{"tests_passed", "tests_failed", "tests_skipped"} {
		if _, present := s[k]; present {
			t.Errorf("%s present with no test_run events, want omitted (degrade signal)", k)
		}
	}
}
