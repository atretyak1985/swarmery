package api

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/store"
)

type overviewStats struct {
	Day             string   `json:"day"`
	Sessions        int64    `json:"sessions"`
	Active          int64    `json:"active"`
	WaitingApproval int64    `json:"waiting_approval"`
	TokensIn        int64    `json:"tokens_in"`
	TokensOut       int64    `json:"tokens_out"`
	CostUSD         *float64 `json:"cost_usd"`
	Errors          int64    `json:"errors"`
	Series          []struct {
		Day      string   `json:"day"`
		Sessions int64    `json:"sessions"`
		Tokens   int64    `json:"tokens"`
		CostUSD  *float64 `json:"cost_usd"`
		Errors   int64    `json:"errors"`
	} `json:"series"`
	ErrorsByProject []struct {
		Slug   string  `json:"slug"`
		Name   *string `json:"name"`
		Errors int64   `json:"errors"`
	} `json:"errors_by_project"`
	CostByModel []struct {
		Model   string  `json:"model"`
		CostUSD float64 `json:"cost_usd"`
	} `json:"cost_by_model"`
	Projects []struct {
		Slug     string  `json:"slug"`
		Name     *string `json:"name"`
		Sessions int64   `json:"sessions"`
	} `json:"projects"`
}

// overviewServer plants rows on three local days: today (priced turns, two
// sessions, three errors across two projects), today-3 (one session with
// ONLY unpriced usage → the cost NULL rule must fire for that series day),
// and today-20 (outside the 14-day series when day=today).
//
// Timestamps are planted at local noon of each target day (converted to UTC
// strings) so the local-day windows are DST-proof.
func overviewServer(t *testing.T) *httptest.Server {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "overview.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	const tsFmt = "2006-01-02T15:04:05.000Z"
	now := time.Now()
	todayStart := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	at := func(d time.Time) string { return d.UTC().Format(tsFmt) }
	today := at(now)
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

	// Errors: two today on alpha, one today on beta (via s3 — events join to
	// projects through their session), one on day3 (beta).
	mustExec(`INSERT INTO events (session_id, ts, type, status, dedup_key) VALUES
		(1, ?, 'tool_call', 'error', 'e1'),
		(2, ?, 'tool_call', 'error', 'e2'),
		(3, ?, 'error',     'error', 'e3'),
		(1, ?, 'tool_call', 'ok',    'e4'),
		(3, ?, 'error',     'error', 'e5')`, today, today, today, today, day3)

	h, err := NewServer(db, false)
	if err != nil {
		t.Fatalf("new server: %v", err)
	}
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return srv
}

func TestStatsOverview(t *testing.T) {
	srv := overviewServer(t)
	now := time.Now()
	todayStart := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	todayStr := todayStart.Format("2006-01-02")
	day3Str := todayStart.AddDate(0, 0, -3).Format("2006-01-02")

	t.Run("default day is today", func(t *testing.T) {
		var o overviewStats
		getJSON(t, srv.URL+"/api/stats/overview", &o)

		if o.Day != todayStr {
			t.Errorf("day = %q, want %q", o.Day, todayStr)
		}
		if o.Sessions != 2 { // s1, s2 started today; s3/s4 excluded
			t.Errorf("sessions = %d, want 2", o.Sessions)
		}
		if o.Active != 1 { // s1
			t.Errorf("active = %d, want 1", o.Active)
		}
		if o.WaitingApproval != 0 {
			t.Errorf("waiting_approval = %d, want 0", o.WaitingApproval)
		}
		if o.TokensIn != 110 || o.TokensOut != 55 {
			t.Errorf("tokens = %d/%d, want 110/55", o.TokensIn, o.TokensOut)
		}
		if o.CostUSD == nil || *o.CostUSD != 0.75 {
			t.Errorf("cost_usd = %v, want 0.75", o.CostUSD)
		}
		if o.Errors != 3 {
			t.Errorf("errors = %d, want 3", o.Errors)
		}

		// Series: exactly 14 days, ascending, ending at `day`, zero days included.
		if len(o.Series) != 14 {
			t.Fatalf("series length = %d, want 14", len(o.Series))
		}
		for i, p := range o.Series {
			wantDay := todayStart.AddDate(0, 0, i-13).Format("2006-01-02")
			if p.Day != wantDay {
				t.Errorf("series[%d].day = %q, want %q (ascending)", i, p.Day, wantDay)
			}
		}
		last := o.Series[13]
		if last.Sessions != 2 || last.Tokens != 165 || last.Errors != 3 ||
			last.CostUSD == nil || *last.CostUSD != 0.75 {
			t.Errorf("series[13] (today) = %+v, want sessions 2, tokens 165, cost 0.75, errors 3", last)
		}
		// day-3: all usage-bearing turns unpriced → cost_usd null, NOT 0.
		d3 := o.Series[10]
		if d3.Day != day3Str || d3.Sessions != 1 || d3.Tokens != 50 || d3.Errors != 1 {
			t.Errorf("series[10] (day-3) = %+v, want sessions 1, tokens 50, errors 1", d3)
		}
		if d3.CostUSD != nil {
			t.Errorf("series[10].cost_usd = %v, want null (all turns unpriced)", *d3.CostUSD)
		}
		// A zero day is present with honest zeros and cost 0 (no usage → $0).
		z := o.Series[0]
		if z.Sessions != 0 || z.Tokens != 0 || z.Errors != 0 {
			t.Errorf("series[0] (zero day) = %+v, want zeros", z)
		}
		if z.CostUSD == nil || *z.CostUSD != 0 {
			t.Errorf("series[0].cost_usd = %v, want 0 (no usage turns)", z.CostUSD)
		}

		// errors_by_project: that day, desc.
		if len(o.ErrorsByProject) != 2 ||
			o.ErrorsByProject[0].Slug != "-work-alpha" || o.ErrorsByProject[0].Errors != 2 ||
			o.ErrorsByProject[1].Slug != "-work-beta" || o.ErrorsByProject[1].Errors != 1 {
			t.Errorf("errors_by_project = %+v", o.ErrorsByProject)
		}
		if o.ErrorsByProject[0].Name == nil || *o.ErrorsByProject[0].Name != "Alpha" {
			t.Errorf("errors_by_project[0].name = %v, want Alpha", o.ErrorsByProject[0].Name)
		}
		if o.ErrorsByProject[1].Name != nil {
			t.Errorf("errors_by_project[1].name = %v, want null", *o.ErrorsByProject[1].Name)
		}

		// cost_by_model: priced turns only, desc — day3's unpriced mystery-model
		// and day20's turn must not appear.
		if len(o.CostByModel) != 2 ||
			o.CostByModel[0].Model != "claude-fable-5" || o.CostByModel[0].CostUSD != 0.5 ||
			o.CostByModel[1].Model != "cheap-model" || o.CostByModel[1].CostUSD != 0.25 {
			t.Errorf("cost_by_model = %+v", o.CostByModel)
		}

		// projects: sessions started that day, desc.
		if len(o.Projects) != 1 || o.Projects[0].Slug != "-work-alpha" || o.Projects[0].Sessions != 2 {
			t.Errorf("projects = %+v", o.Projects)
		}
	})

	t.Run("explicit past day", func(t *testing.T) {
		var o overviewStats
		getJSON(t, srv.URL+"/api/stats/overview?day="+day3Str, &o)

		if o.Day != day3Str {
			t.Errorf("day = %q, want %q", o.Day, day3Str)
		}
		if o.Sessions != 1 || o.TokensIn != 30 || o.TokensOut != 20 || o.Errors != 1 {
			t.Errorf("day-3 scalars = %+v", o)
		}
		if o.Active != 0 { // s1 is active NOW, but day != today → 0
			t.Errorf("active = %d, want 0 for a past day", o.Active)
		}
		if o.CostUSD != nil {
			t.Errorf("cost_usd = %v, want null (all of day-3's turns unpriced)", *o.CostUSD)
		}
		if len(o.Series) != 14 || o.Series[13].Day != day3Str {
			t.Fatalf("series must be 14 days ending at %s", day3Str)
		}
		// day-20 is 17 days before day-3 → outside this series window.
		var total int64
		for _, p := range o.Series {
			total += p.Sessions
		}
		if total != 1 {
			t.Errorf("series sessions total = %d, want 1 (day-20 outside the window)", total)
		}
	})

	t.Run("invalid day → 400", func(t *testing.T) {
		resp, err := http.Get(srv.URL + "/api/stats/overview?day=12-07-2026")
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("invalid day status = %d, want 400", resp.StatusCode)
		}
	})
}
