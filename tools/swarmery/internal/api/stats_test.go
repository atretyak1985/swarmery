package api

import (
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/store"
)

// statsServer builds a DB with hand-planted rows: two projects, sessions and
// turns dated today and older, priced and unpriced turns, and error events.
func statsServer(t *testing.T) *httptest.Server {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "stats.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	const tsFmt = "2006-01-02T15:04:05.000Z"
	today := time.Now().UTC().Format(tsFmt)
	old := time.Now().Add(-48 * time.Hour).UTC().Format(tsFmt)

	mustExec := func(q string, args ...any) {
		t.Helper()
		if _, err := db.Exec(q, args...); err != nil {
			t.Fatalf("exec: %v\n%s", err, q)
		}
	}

	mustExec(`INSERT INTO projects (id, path, slug, first_seen) VALUES
		(1, '/work/alpha', '-work-alpha', ?),
		(2, '/work/beta',  '-work-beta',  ?)`, old, old)

	// Project 1: s1 started today (active), s2 started 2 days ago (active),
	// s3 started today (completed). Project 2: s4 started today.
	mustExec(`INSERT INTO sessions (id, project_id, session_uuid, model, status, started_at) VALUES
		(1, 1, 'u1', 'claude-fable-5', 'active',    ?),
		(2, 1, 'u2', 'claude-fable-5', 'active',    ?),
		(3, 1, 'u3', 'claude-fable-5', 'completed', ?),
		(4, 2, 'u4', 'mystery-model',  'completed', ?)`, today, old, today, today)

	// Turns. Project 1 today: one priced assistant turn, one UNPRICED
	// usage-bearing turn (cost NULL), one user turn (no usage); plus an old
	// turn that must not count. Project 2 today: only unpriced usage turns.
	mustExec(`INSERT INTO turns (session_id, seq, role, started_at, tokens_in, tokens_out, tokens_cache_read, tokens_cache_write, cost_usd) VALUES
		(1, 0, 'user',      ?, NULL, NULL, NULL, NULL, NULL),
		(1, 1, 'assistant', ?, 100,  50,   1000, 200,  0.5),
		(3, 0, 'assistant', ?, 30,   20,   NULL, NULL, NULL),
		(2, 0, 'assistant', ?, 9999, 9999, NULL, NULL, 42.0),
		(4, 0, 'assistant', ?, 70,   40,   NULL, NULL, NULL)`,
		today, today, today, old, today)

	// Events: one error today (p1), one ok today (p1), one error 2 days ago (p1).
	mustExec(`INSERT INTO events (session_id, ts, type, status, dedup_key) VALUES
		(1, ?, 'tool_call', 'error', 'e1'),
		(1, ?, 'tool_call', 'ok',    'e2'),
		(2, ?, 'error',     'error', 'e3')`, today, today, old)

	h, err := NewServer(db)
	if err != nil {
		t.Fatalf("new server: %v", err)
	}
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return srv
}

func TestStatsToday(t *testing.T) {
	srv := statsServer(t)

	type stats struct {
		Sessions  int64    `json:"sessions"`
		Active    int64    `json:"active"`
		TokensIn  int64    `json:"tokens_in"`
		TokensOut int64    `json:"tokens_out"`
		CostUSD   *float64 `json:"cost_usd"`
		Errors    int64    `json:"errors"`
	}

	t.Run("all projects", func(t *testing.T) {
		var s stats
		getJSON(t, srv.URL+"/api/stats/today", &s)
		if s.Sessions != 3 { // s1, s3, s4 started today; old s2 excluded
			t.Errorf("sessions = %d, want 3", s.Sessions)
		}
		if s.Active != 2 { // s1 (today) + s2 (old but still active)
			t.Errorf("active = %d, want 2", s.Active)
		}
		if s.TokensIn != 200 { // 100 + 30 + 70 (old 9999 excluded)
			t.Errorf("tokens_in = %d, want 200", s.TokensIn)
		}
		if s.TokensOut != 110 { // 50 + 20 + 40
			t.Errorf("tokens_out = %d, want 110", s.TokensOut)
		}
		if s.CostUSD == nil || *s.CostUSD != 0.5 { // priced subset only; old 42.0 excluded
			t.Errorf("cost_usd = %v, want 0.5", s.CostUSD)
		}
		if s.Errors != 1 { // today's error only
			t.Errorf("errors = %d, want 1", s.Errors)
		}
	})

	t.Run("project filter by slug", func(t *testing.T) {
		var s stats
		getJSON(t, srv.URL+"/api/stats/today?project=-work-beta", &s)
		if s.Sessions != 1 || s.Active != 0 || s.TokensIn != 70 || s.TokensOut != 40 || s.Errors != 0 {
			t.Errorf("beta stats = %+v", s)
		}
		// All of beta's usage-bearing turns are unpriced → cost_usd must be
		// null, NOT 0 (zero would lie in sums).
		if s.CostUSD != nil {
			t.Errorf("cost_usd = %v, want null (all turns unpriced)", *s.CostUSD)
		}
	})

	t.Run("project filter by id", func(t *testing.T) {
		var s stats
		getJSON(t, srv.URL+"/api/stats/today?project=1", &s)
		if s.Sessions != 2 || s.Active != 2 || s.TokensIn != 130 || s.Errors != 1 {
			t.Errorf("alpha stats = %+v", s)
		}
		if s.CostUSD == nil || *s.CostUSD != 0.5 {
			t.Errorf("cost_usd = %v, want 0.5", s.CostUSD)
		}
	})

	t.Run("unknown project → zeros with cost 0", func(t *testing.T) {
		var s stats
		getJSON(t, srv.URL+"/api/stats/today?project=nope", &s)
		if s.Sessions != 0 || s.Active != 0 || s.TokensIn != 0 || s.TokensOut != 0 || s.Errors != 0 {
			t.Errorf("empty stats = %+v", s)
		}
		if s.CostUSD == nil || *s.CostUSD != 0 { // no usage turns at all → $0 spent, honestly
			t.Errorf("cost_usd = %v, want 0", s.CostUSD)
		}
	})
}
