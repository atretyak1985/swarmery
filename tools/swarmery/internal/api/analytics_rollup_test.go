package api

import (
	"database/sql"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/store"
)

// rollupAnalyticsServer plants one project with a LIVE priced turn today plus
// a daily_rollups row 5 days ago (as `swarmery prune` would have written it:
// per-project, agent_id NULL) and the pruned session's bare header. The db is
// returned so scope tests can seed extra projects.
func rollupAnalyticsServer(t *testing.T) (*httptest.Server, string, *sql.DB) {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "rollup.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	mustExec := func(q string, args ...any) {
		t.Helper()
		if _, err := db.Exec(q, args...); err != nil {
			t.Fatalf("exec: %v\n%s", err, q)
		}
	}
	now := time.Now()
	todayStart := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	today := todayStart.Add(12 * time.Hour).UTC().Format("2006-01-02T15:04:05.000Z")
	prunedDay := todayStart.AddDate(0, 0, -5).Format("2006-01-02") // local-day key

	mustExec(`INSERT INTO projects (id, path, slug, name, first_seen) VALUES
		(1, '/work/alpha', '-work-alpha', 'Alpha', ?)`, today)
	mustExec(`INSERT INTO sessions (id, project_id, session_uuid, status, started_at, pruned) VALUES
		(1, 1, 'live-1',   'active',    ?, 0),
		(2, 1, 'pruned-1', 'completed', ?, 1)`, today, today)
	mustExec(`INSERT INTO turns (session_id, seq, role, started_at, tokens_in, tokens_out, cost_usd)
		VALUES (1, 0, 'assistant', ?, 100, 50, 0.5)`, today)
	mustExec(`INSERT INTO daily_rollups
		(day, project_id, agent_id, sessions, tasks_done, tasks_reverted,
		 tool_calls, errors, tokens_in, tokens_out, cost_usd, wait_minutes)
		VALUES (?, 1, NULL, 3, 0, 0, 40, 2, 1000, 400, 2.0, 0)`, prunedDay)

	h, err := NewServer(db, false)
	if err != nil {
		t.Fatalf("new server: %v", err)
	}
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return srv, prunedDay, db
}

func TestTimeseriesUnionsRollups(t *testing.T) {
	srv, prunedDay, _ := rollupAnalyticsServer(t)
	var ts timeseriesDTO
	getJSON(t, srv.URL+"/api/stats/timeseries?metric=cost&group=project", &ts)
	if ts.Approx {
		t.Error("approx = true, want false (project grouping is exact over rollups)")
	}
	if len(ts.Series) != 1 || ts.Series[0].Key != "-work-alpha" {
		t.Fatalf("series = %+v, want one -work-alpha series", ts.Series)
	}
	if ts.Series[0].Total != 2.5 { // 0.5 live + 2.0 rolled up
		t.Errorf("total = %v, want 2.5", ts.Series[0].Total)
	}
	idx := -1
	for i, d := range ts.Buckets {
		if d == prunedDay {
			idx = i
		}
	}
	if idx == -1 {
		t.Fatalf("pruned day %s not in buckets %v", prunedDay, ts.Buckets)
	}
	if ts.Series[0].Values[idx] != 2.0 {
		t.Errorf("pruned-day value = %v, want 2.0", ts.Series[0].Values[idx])
	}
}

func TestTimeseriesApproxForNonProjectGroups(t *testing.T) {
	srv, _, _ := rollupAnalyticsServer(t)
	var ts timeseriesDTO
	getJSON(t, srv.URL+"/api/stats/timeseries?metric=cost&group=model", &ts)
	if !ts.Approx {
		t.Error("approx = false, want true (rollups carry no model grain)")
	}
	getJSON(t, srv.URL+"/api/stats/timeseries?metric=runs&group=agent", &ts)
	if !ts.Approx {
		t.Error("runs approx = false, want true (pruned days lost their events)")
	}
}

func TestBreakdownUnionsRollups(t *testing.T) {
	srv, _, _ := rollupAnalyticsServer(t)
	var rows []breakdownRow
	getJSON(t, srv.URL+"/api/stats/breakdown?by=project", &rows)
	if len(rows) != 1 || rows[0].Key != "-work-alpha" {
		t.Fatalf("rows = %+v, want one -work-alpha row", rows)
	}
	r := rows[0]
	if r.CostUSD == nil || *r.CostUSD != 2.5 {
		t.Errorf("cost = %v, want 2.5", r.CostUSD)
	}
	if r.TokensIn == nil || *r.TokensIn != 1100 || r.TokensOut == nil || *r.TokensOut != 450 {
		t.Errorf("tokens = %v/%v, want 1100/450", r.TokensIn, r.TokensOut)
	}
	if r.Sessions != 4 { // 1 live (has turns in range) + 3 rolled up
		t.Errorf("sessions = %d, want 4", r.Sessions)
	}
}
