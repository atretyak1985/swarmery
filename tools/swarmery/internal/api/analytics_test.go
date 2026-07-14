package api

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/store"
)

// analyticsServer plants two projects with turns (priced today on alpha,
// unpriced on day3 for beta, and a day20 row OUTSIDE the default 14-day range)
// plus subagent_start / skill_use events whose payload names exercise the
// normAgentType fold ("core:tech-lead" and "tech-lead" must share totals).
func analyticsServer(t *testing.T) *httptest.Server {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "analytics.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	const tsFmt = "2006-01-02T15:04:05.000Z"
	now := time.Now()
	todayStart := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	at := func(d time.Time) string { return d.UTC().Format(tsFmt) }
	today := at(todayStart.Add(12 * time.Hour))
	day3 := at(todayStart.AddDate(0, 0, -3).Add(12 * time.Hour))
	day20 := at(todayStart.AddDate(0, 0, -20).Add(12 * time.Hour))

	mustExec := func(q string, args ...any) {
		t.Helper()
		if _, err := db.Exec(q, args...); err != nil {
			t.Fatalf("exec: %v\n%s", err, q)
		}
	}

	mustExec(`INSERT INTO projects (id, path, slug, name, first_seen) VALUES
		(1, '/work/alpha', '-work-alpha', 'Alpha', ?),
		(2, '/work/beta',  '-work-beta',  NULL,    ?)`, day20, day20)

	mustExec(`INSERT INTO sessions (id, project_id, session_uuid, model, status, started_at) VALUES
		(1, 1, 'u1', 'claude-fable-5', 'active',    ?),
		(2, 1, 'u2', 'cheap-model',    'completed', ?),
		(3, 2, 'u3', 'mystery-model',  'completed', ?),
		(4, 2, 'u4', 'claude-fable-5', 'completed', ?)`, today, today, day3, day20)

	mustExec(`INSERT INTO turns (session_id, seq, role, model, started_at, tokens_in, tokens_out, cost_usd) VALUES
		(1, 0, 'assistant', 'claude-fable-5', ?, 100, 50, 0.5),
		(2, 0, 'assistant', 'cheap-model',    ?, 10,  5,  0.25),
		(3, 0, 'assistant', 'mystery-model',  ?, 30,  20, NULL),
		(4, 0, 'assistant', 'claude-fable-5', ?, 7,   7,  1.0)`,
		today, today, day3, day20)

	// subagent_start: two notations of tech-lead on alpha today (fold → 2),
	// debugger on alpha today, tech-lead on beta day3, tech-lead on beta day20
	// (outside the default range).
	mustExec(`INSERT INTO events (session_id, ts, type, payload, dedup_key) VALUES
		(1, ?, 'subagent_start', '{"subagent_type":"core:tech-lead"}', 'a1'),
		(1, ?, 'subagent_start', '{"subagent_type":"tech-lead"}',      'a2'),
		(2, ?, 'subagent_start', '{"subagent_type":"debugger"}',       'a3'),
		(3, ?, 'subagent_start', '{"subagent_type":"tech-lead"}',      'a4'),
		(4, ?, 'subagent_start', '{"subagent_type":"tech-lead"}',      'a5')`,
		today, today, today, day3, day20)

	// skill_use: code-review on alpha today.
	mustExec(`INSERT INTO events (session_id, ts, type, payload, dedup_key) VALUES
		(1, ?, 'skill_use', '{"input":{"skill":"code-review"}}', 's1')`, today)

	h, err := NewServer(db, false)
	if err != nil {
		t.Fatalf("new server: %v", err)
	}
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return srv
}

func TestStatsTimeseries(t *testing.T) {
	srv := analyticsServer(t)

	t.Run("cost by project skips unpriced turns", func(t *testing.T) {
		var ts timeseriesDTO
		getJSON(t, srv.URL+"/api/stats/timeseries?metric=cost&group=project", &ts)
		if len(ts.Buckets) != 14 {
			t.Fatalf("buckets = %d, want 14", len(ts.Buckets))
		}
		// Only alpha has priced turns; beta's day3 turn is unpriced → no series.
		if len(ts.Series) != 1 || ts.Series[0].Key != "-work-alpha" {
			t.Fatalf("series = %+v, want single alpha", ts.Series)
		}
		if ts.Series[0].Name != "Alpha" {
			t.Errorf("name = %q, want Alpha", ts.Series[0].Name)
		}
		if ts.Series[0].Total != 0.75 {
			t.Errorf("alpha total = %v, want 0.75", ts.Series[0].Total)
		}
		if got := ts.Series[0].Values[len(ts.Series[0].Values)-1]; got != 0.75 {
			t.Errorf("today value = %v, want 0.75", got)
		}
		if ts.Approx {
			t.Errorf("approx = true, want false in phase 1")
		}
	})

	t.Run("tokens by project includes unpriced usage", func(t *testing.T) {
		var ts timeseriesDTO
		getJSON(t, srv.URL+"/api/stats/timeseries?metric=tokens&group=project", &ts)
		byKey := map[string]seriesDTO{}
		for _, s := range ts.Series {
			byKey[s.Key] = s
		}
		if byKey["-work-alpha"].Total != 165 { // 150 + 15
			t.Errorf("alpha tokens = %v, want 165", byKey["-work-alpha"].Total)
		}
		if byKey["-work-beta"].Total != 50 { // 30 + 20
			t.Errorf("beta tokens = %v, want 50", byKey["-work-beta"].Total)
		}
		// Sorted total desc: alpha first.
		if ts.Series[0].Key != "-work-alpha" {
			t.Errorf("first series = %q, want alpha", ts.Series[0].Key)
		}
	})

	t.Run("runs by agent folds notations", func(t *testing.T) {
		var ts timeseriesDTO
		getJSON(t, srv.URL+"/api/stats/timeseries?metric=runs&group=agent", &ts)
		byKey := map[string]seriesDTO{}
		for _, s := range ts.Series {
			byKey[s.Key] = s
		}
		if byKey["tech-lead"].Total != 3 { // core:tech-lead + tech-lead today + beta day3
			t.Errorf("tech-lead runs = %v, want 3", byKey["tech-lead"].Total)
		}
		if byKey["debugger"].Total != 1 {
			t.Errorf("debugger runs = %v, want 1", byKey["debugger"].Total)
		}
	})

	t.Run("invalid combo is 400", func(t *testing.T) {
		// runs is an events metric (agent|skill only) — project is invalid.
		res, err := http.Get(srv.URL + "/api/stats/timeseries?metric=runs&group=project")
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		if res.StatusCode != http.StatusBadRequest {
			t.Errorf("status = %d, want 400", res.StatusCode)
		}
	})
}

func TestStatsBreakdown(t *testing.T) {
	srv := analyticsServer(t)

	t.Run("by project", func(t *testing.T) {
		var rows []breakdownRow
		getJSON(t, srv.URL+"/api/stats/breakdown?by=project", &rows)
		if len(rows) != 2 {
			t.Fatalf("rows = %d, want 2", len(rows))
		}
		alpha := rows[0] // cost desc → alpha first
		if alpha.Key != "-work-alpha" || alpha.CostUSD == nil || *alpha.CostUSD != 0.75 {
			t.Errorf("alpha = %+v, want cost 0.75", alpha)
		}
		if alpha.Sessions != 2 {
			t.Errorf("alpha sessions = %d, want 2", alpha.Sessions)
		}
		beta := rows[1]
		if beta.CostUSD != nil { // only unpriced turns → no $ figure
			t.Errorf("beta cost = %v, want nil", *beta.CostUSD)
		}
		if beta.TokensIn == nil || *beta.TokensIn != 30 {
			t.Errorf("beta tokens_in = %v, want 30", beta.TokensIn)
		}
	})

	t.Run("by agent — runs from events; no agent turns here so cost is nil", func(t *testing.T) {
		// This fixture has only main (NULL agent_name) turns, so subagents carry
		// runs but no cost; the orchestrator surfaces as a "main" row with cost.
		var rows []breakdownRow
		getJSON(t, srv.URL+"/api/stats/breakdown?by=agent", &rows)
		byKey := map[string]breakdownRow{}
		for _, r := range rows {
			byKey[r.Key] = r
		}
		tl, ok := byKey["tech-lead"]
		if !ok || tl.Runs == nil || *tl.Runs != 3 {
			t.Errorf("tech-lead = %+v, want runs 3", tl)
		}
		if tl.Sessions != 2 { // s1, s3
			t.Errorf("tech-lead sessions = %d, want 2", tl.Sessions)
		}
		if tl.CostUSD != nil { // no tech-lead turns in this fixture
			t.Errorf("tech-lead cost = %v, want nil", *tl.CostUSD)
		}
		if tl.LastUsed == nil {
			t.Errorf("tech-lead last_used = nil, want set")
		}
		// The orchestrator's main turns surface as a "main" row with cost.
		main, ok := byKey["main"]
		if !ok || main.CostUSD == nil || *main.CostUSD != 0.75 {
			t.Errorf("main = %+v, want cost 0.75", main)
		}
	})
}

func TestStatsMatrix(t *testing.T) {
	srv := analyticsServer(t)
	var m matrixDTO
	getJSON(t, srv.URL+"/api/stats/matrix?rows=agent&cols=project", &m)

	cell := map[[2]string]int64{}
	for _, c := range m.Cells {
		cell[[2]string{c.Row, c.Col}] = c.Runs
	}
	if cell[[2]string{"tech-lead", "-work-alpha"}] != 2 {
		t.Errorf("tech-lead×alpha = %d, want 2", cell[[2]string{"tech-lead", "-work-alpha"}])
	}
	if cell[[2]string{"debugger", "-work-alpha"}] != 1 {
		t.Errorf("debugger×alpha = %d, want 1", cell[[2]string{"debugger", "-work-alpha"}])
	}
	if cell[[2]string{"tech-lead", "-work-beta"}] != 1 {
		t.Errorf("tech-lead×beta = %d, want 1", cell[[2]string{"tech-lead", "-work-beta"}])
	}
	if len(m.Rows) != 2 || m.Rows[0].Key != "tech-lead" {
		t.Errorf("rows = %+v, want tech-lead first", m.Rows)
	}
	// beta project name is NULL → label falls back to slug.
	var betaCol *keyName
	for i := range m.Cols {
		if m.Cols[i].Key == "-work-beta" {
			betaCol = &m.Cols[i]
		}
	}
	if betaCol == nil || betaCol.Name != "-work-beta" {
		t.Errorf("beta col = %+v, want name fallback to slug", betaCol)
	}
}

// agentTurnsServer plants phase-2 subagent turns (agent_name set) alongside a
// "main" (NULL) turn, plus matching subagent_start events, to exercise exact
// per-agent $: tech-lead 3.5 on alpha today (core:tech-lead + tech-lead fold),
// debugger 0.4 on beta day3, main 0.5 on alpha today.
func agentTurnsServer(t *testing.T) *httptest.Server {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "agentturns.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	const tsFmt = "2006-01-02T15:04:05.000Z"
	now := time.Now()
	todayStart := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	at := func(d time.Time) string { return d.UTC().Format(tsFmt) }
	today := at(todayStart.Add(12 * time.Hour))
	day3 := at(todayStart.AddDate(0, 0, -3).Add(12 * time.Hour))

	mustExec := func(q string, args ...any) {
		t.Helper()
		if _, err := db.Exec(q, args...); err != nil {
			t.Fatalf("exec: %v\n%s", err, q)
		}
	}
	mustExec(`INSERT INTO projects (id, path, slug, name, first_seen) VALUES
		(1, '/work/alpha', '-work-alpha', 'Alpha', ?),
		(2, '/work/beta',  '-work-beta',  NULL,    ?)`, day3, day3)
	mustExec(`INSERT INTO sessions (id, project_id, session_uuid, status, started_at) VALUES
		(1, 1, 'u1', 'completed', ?),
		(3, 2, 'u3', 'completed', ?)`, today, day3)
	mustExec(`INSERT INTO turns (session_id, seq, role, started_at, tokens_in, tokens_out, cost_usd, agent_name) VALUES
		(1, 0, 'assistant', ?, 100, 50,  0.5, NULL),
		(1, 1, 'assistant', ?, 200, 100, 2.0, 'core:tech-lead'),
		(1, 2, 'assistant', ?, 150, 80,  1.5, 'tech-lead'),
		(3, 0, 'assistant', ?, 40,  20,  0.4, 'debugger')`, today, today, today, day3)
	mustExec(`INSERT INTO events (session_id, ts, type, payload, dedup_key) VALUES
		(1, ?, 'subagent_start', '{"subagent_type":"core:tech-lead"}', 'a1'),
		(1, ?, 'subagent_start', '{"subagent_type":"tech-lead"}',      'a2'),
		(3, ?, 'subagent_start', '{"subagent_type":"debugger"}',       'a3')`, today, today, day3)

	h, err := NewServer(db, false)
	if err != nil {
		t.Fatalf("new server: %v", err)
	}
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return srv
}

func TestStatsAgentCost(t *testing.T) {
	srv := agentTurnsServer(t)

	t.Run("timeseries cost by agent includes main + subagents", func(t *testing.T) {
		var ts timeseriesDTO
		getJSON(t, srv.URL+"/api/stats/timeseries?metric=cost&group=agent", &ts)
		byKey := map[string]seriesDTO{}
		for _, s := range ts.Series {
			byKey[s.Key] = s
		}
		if byKey["tech-lead"].Total != 3.5 { // core:tech-lead + tech-lead
			t.Errorf("tech-lead cost = %v, want 3.5", byKey["tech-lead"].Total)
		}
		if byKey["main"].Total != 0.5 {
			t.Errorf("main cost = %v, want 0.5", byKey["main"].Total)
		}
		if byKey["debugger"].Total != 0.4 {
			t.Errorf("debugger cost = %v, want 0.4", byKey["debugger"].Total)
		}
		if ts.Series[0].Key != "tech-lead" {
			t.Errorf("first series = %q, want tech-lead", ts.Series[0].Key)
		}
	})

	t.Run("breakdown by agent has exact cost + runs", func(t *testing.T) {
		var rows []breakdownRow
		getJSON(t, srv.URL+"/api/stats/breakdown?by=agent", &rows)
		byKey := map[string]breakdownRow{}
		for _, r := range rows {
			byKey[r.Key] = r
		}
		tl := byKey["tech-lead"]
		if tl.CostUSD == nil || *tl.CostUSD != 3.5 || tl.Runs == nil || *tl.Runs != 2 {
			t.Errorf("tech-lead = %+v, want cost 3.5 runs 2", tl)
		}
		main := byKey["main"]
		if main.CostUSD == nil || *main.CostUSD != 0.5 || main.Runs != nil {
			t.Errorf("main = %+v, want cost 0.5 no runs", main)
		}
		if rows[0].Key != "tech-lead" { // cost desc
			t.Errorf("first row = %q, want tech-lead", rows[0].Key)
		}
	})

	t.Run("matrix cost agent x project", func(t *testing.T) {
		var m matrixDTO
		getJSON(t, srv.URL+"/api/stats/matrix?rows=agent&cols=project&metric=cost", &m)
		if m.Metric != "cost" {
			t.Errorf("metric = %q, want cost", m.Metric)
		}
		cell := map[[2]string]float64{}
		for _, c := range m.Cells {
			if c.Cost != nil {
				cell[[2]string{c.Row, c.Col}] = *c.Cost
			}
		}
		if cell[[2]string{"tech-lead", "-work-alpha"}] != 3.5 {
			t.Errorf("tech-lead×alpha = %v, want 3.5", cell[[2]string{"tech-lead", "-work-alpha"}])
		}
		if cell[[2]string{"main", "-work-alpha"}] != 0.5 {
			t.Errorf("main×alpha = %v, want 0.5", cell[[2]string{"main", "-work-alpha"}])
		}
		if cell[[2]string{"debugger", "-work-beta"}] != 0.4 {
			t.Errorf("debugger×beta = %v, want 0.4", cell[[2]string{"debugger", "-work-beta"}])
		}
	})

	t.Run("matrix cost rejects skill rows", func(t *testing.T) {
		res, err := http.Get(srv.URL + "/api/stats/matrix?rows=skill&cols=project&metric=cost")
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		if res.StatusCode != http.StatusBadRequest {
			t.Errorf("status = %d, want 400", res.StatusCode)
		}
	})
}
