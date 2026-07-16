package api

// ?project=<slug|id> scope tests for /api/stats/overview and the analytics
// endpoints, over the existing analyticsServer fixture (analytics_test.go):
// alpha ('-work-alpha', id 1) has today's priced turns + events; beta
// ('-work-beta', id 2) has an unpriced day3 turn and day3/day20 events.

import "testing"

func TestStatsOverviewProjectScope(t *testing.T) {
	srv := analyticsServer(t)

	var o statsOverviewDTO
	getJSON(t, srv.URL+"/api/stats/overview?project=-work-alpha", &o)
	if o.Sessions != 2 {
		t.Errorf("alpha sessions today = %d, want 2", o.Sessions)
	}
	if o.TokensIn != 110 || o.TokensOut != 55 {
		t.Errorf("alpha tokens = %d/%d, want 110/55", o.TokensIn, o.TokensOut)
	}
	if len(o.Projects) != 1 || o.Projects[0].Slug != "-work-alpha" {
		t.Errorf("projects = %+v, want alpha only", o.Projects)
	}

	getJSON(t, srv.URL+"/api/stats/overview?project=-work-beta", &o)
	if o.Sessions != 0 || o.TokensIn != 0 {
		t.Errorf("beta today = %d sessions / %d tokens_in, want 0/0", o.Sessions, o.TokensIn)
	}
}

func TestAnalyticsProjectScope(t *testing.T) {
	srv := analyticsServer(t)

	t.Run("timeseries cost scoped to beta is empty", func(t *testing.T) {
		var ts timeseriesDTO
		getJSON(t, srv.URL+"/api/stats/timeseries?metric=cost&group=project&project=-work-beta", &ts)
		if len(ts.Series) != 0 {
			t.Errorf("series = %+v, want none (beta has no priced turns)", ts.Series)
		}
	})

	t.Run("timeseries tokens scoped to alpha excludes beta", func(t *testing.T) {
		var ts timeseriesDTO
		getJSON(t, srv.URL+"/api/stats/timeseries?metric=tokens&group=project&project=-work-alpha", &ts)
		if len(ts.Series) != 1 || ts.Series[0].Key != "-work-alpha" {
			t.Fatalf("series = %+v, want alpha only", ts.Series)
		}
		if ts.Series[0].Total != 165 {
			t.Errorf("alpha tokens = %v, want 165", ts.Series[0].Total)
		}
	})

	t.Run("scope accepts the numeric project id too", func(t *testing.T) {
		var ts timeseriesDTO
		getJSON(t, srv.URL+"/api/stats/timeseries?metric=tokens&group=project&project=1", &ts)
		if len(ts.Series) != 1 || ts.Series[0].Key != "-work-alpha" {
			t.Fatalf("series = %+v, want alpha only (scoped by id)", ts.Series)
		}
	})

	t.Run("breakdown by agent scoped to alpha drops beta runs", func(t *testing.T) {
		var rows []breakdownRow
		getJSON(t, srv.URL+"/api/stats/breakdown?by=agent&project=-work-alpha", &rows)
		byKey := map[string]breakdownRow{}
		for _, r := range rows {
			byKey[r.Key] = r
		}
		// Unscoped tech-lead has 3 runs (2 alpha today + 1 beta day3); scoped → 2.
		if tl := byKey["tech-lead"]; tl.Runs == nil || *tl.Runs != 2 {
			t.Errorf("tech-lead = %+v, want runs 2", tl)
		}
	})

	t.Run("matrix scoped to alpha has no beta column", func(t *testing.T) {
		var m matrixDTO
		getJSON(t, srv.URL+"/api/stats/matrix?rows=agent&cols=project&project=-work-alpha", &m)
		for _, c := range m.Cols {
			if c.Key == "-work-beta" {
				t.Errorf("cols = %+v, must not contain beta", m.Cols)
			}
		}
	})
}

// Drift guard (ops-hygiene union): the daily_rollups union and the approx
// flag must respect ?project= too. Beyond the never-matching scope, a SECOND
// project with its own rollup history is seeded so scoping to alpha proves
// beta's rolled-up series/costs are excluded while alpha's survive.
func TestRollupUnionProjectScope(t *testing.T) {
	srv, prunedDay, db := rollupAnalyticsServer(t)

	// Second project with its own pruned history on the same day: rollup cost
	// 7.0 — distinctive, so any leak into an alpha-scoped sum is unmissable.
	mustExec := func(q string, args ...any) {
		t.Helper()
		if _, err := db.Exec(q, args...); err != nil {
			t.Fatalf("exec: %v\n%s", err, q)
		}
	}
	mustExec(`INSERT INTO projects (id, path, slug, name, first_seen)
		SELECT 2, '/work/beta', '-work-beta', 'Beta', first_seen FROM projects WHERE id = 1`)
	mustExec(`INSERT INTO daily_rollups
		(day, project_id, agent_id, sessions, tasks_done, tasks_reverted,
		 tool_calls, errors, tokens_in, tokens_out, cost_usd, wait_minutes)
		VALUES (?, 2, NULL, 5, 0, 0, 10, 1, 9000, 3000, 7.0, 0)`, prunedDay)

	t.Run("scoped-out project sees no rollup series", func(t *testing.T) {
		var ts timeseriesDTO
		getJSON(t, srv.URL+"/api/stats/timeseries?metric=cost&group=project&project=no-such-project", &ts)
		if len(ts.Series) != 0 {
			t.Errorf("series = %+v, want none (scope excludes the rolled-up project)", ts.Series)
		}
	})

	t.Run("scoped-in project keeps the rollup union", func(t *testing.T) {
		var ts timeseriesDTO
		getJSON(t, srv.URL+"/api/stats/timeseries?metric=cost&group=project&project=-work-alpha", &ts)
		if len(ts.Series) != 1 || ts.Series[0].Key != "-work-alpha" || ts.Series[0].Total != 2.5 {
			t.Fatalf("series = %+v, want alpha only, 0.5 live + 2.0 rollup (beta's 7.0 excluded)", ts.Series)
		}
		idx := -1
		for i, d := range ts.Buckets {
			if d == prunedDay {
				idx = i
			}
		}
		// Both projects have a rollup on prunedDay — the scoped bucket must hold
		// alpha's 2.0 alone, not 9.0.
		if idx == -1 || ts.Series[0].Values[idx] != 2.0 {
			t.Errorf("pruned-day value = %v, want 2.0 from alpha's rollup only", ts.Series)
		}
	})

	t.Run("sibling project's rollups stay scoped to it", func(t *testing.T) {
		var ts timeseriesDTO
		getJSON(t, srv.URL+"/api/stats/timeseries?metric=cost&group=project&project=-work-beta", &ts)
		if len(ts.Series) != 1 || ts.Series[0].Key != "-work-beta" || ts.Series[0].Total != 7.0 {
			t.Fatalf("series = %+v, want beta only with its 7.0 rollup", ts.Series)
		}
	})

	t.Run("approx flag is scope-aware for non-project groupings", func(t *testing.T) {
		var ts timeseriesDTO
		getJSON(t, srv.URL+"/api/stats/timeseries?metric=cost&group=model&project=no-such-project", &ts)
		if ts.Approx {
			t.Error("approx = true, want false (scope excludes the rolled-up project)")
		}
		getJSON(t, srv.URL+"/api/stats/timeseries?metric=cost&group=model&project=-work-alpha", &ts)
		if !ts.Approx {
			t.Error("approx = false, want true (scoped project has rolled-up days)")
		}
	})

	t.Run("breakdown rollup merge respects scope", func(t *testing.T) {
		var rows []breakdownRow
		getJSON(t, srv.URL+"/api/stats/breakdown?by=project&project=no-such-project", &rows)
		if len(rows) != 0 {
			t.Errorf("rows = %+v, want none (scope excludes the rolled-up project)", rows)
		}
		getJSON(t, srv.URL+"/api/stats/breakdown?by=project&project=-work-alpha", &rows)
		if len(rows) != 1 || rows[0].Key != "-work-alpha" {
			t.Fatalf("rows = %+v, want alpha only", rows)
		}
		if rows[0].CostUSD == nil || *rows[0].CostUSD != 2.5 {
			t.Errorf("alpha cost = %v, want 2.5 (beta's 7.0 rollup excluded)", rows[0].CostUSD)
		}
	})
}
