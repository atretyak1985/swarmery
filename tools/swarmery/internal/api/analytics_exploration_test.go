package api

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/store"
)

// explorationDTO mirrors the wire shape by hand rather than reusing
// exploration.Result: the response is a frozen contract for
// web/src/api/types.ts, so a renamed json tag must fail here, not silently
// decode into the Go struct it came from.
type explorationDTO struct {
	Days []struct {
		Day     string  `json:"day"`
		Explore int     `json:"explore"`
		Edit    int     `json:"edit"`
		Run     int     `json:"run"`
		Other   int     `json:"other"`
		Calls   int     `json:"calls"`
		Share   float64 `json:"share"`
	} `json:"days"`
	Totals struct {
		Explore int     `json:"explore"`
		Edit    int     `json:"edit"`
		Run     int     `json:"run"`
		Other   int     `json:"other"`
		Calls   int     `json:"calls"`
		Share   float64 `json:"share"`
	} `json:"totals"`
	Top []struct {
		Tool string `json:"tool"`
		N    int    `json:"n"`
	} `json:"top"`
}

// explorationServer plants tool_call events on two projects: alpha gets 2
// explore + 1 run + 1 edit today (share 0.5), beta gets 2 runs today, and one
// alpha explore call sits 20 days back — outside the default 14-day window.
func explorationServer(t *testing.T) (*httptest.Server, string) {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "exploration-api.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	const tsFmt = "2006-01-02T15:04:05.000Z"
	now := time.Now()
	todayStart := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	at := func(d time.Time) string { return d.UTC().Format(tsFmt) }
	today := at(todayStart.Add(12 * time.Hour))
	day20 := at(todayStart.AddDate(0, 0, -20).Add(12 * time.Hour))

	mustExec := func(q string, args ...any) {
		t.Helper()
		if _, err := db.Exec(q, args...); err != nil {
			t.Fatalf("exec: %v\n%s", err, q)
		}
	}

	mustExec(`INSERT INTO projects (id, path, slug, name, first_seen) VALUES
		(1, '/work/alpha', '-work-alpha', 'Alpha', ?),
		(2, '/work/beta',  '-work-beta',  'Beta',  ?)`, day20, day20)
	mustExec(`INSERT INTO sessions (id, project_id, session_uuid, status, started_at) VALUES
		(1, 1, 'x1', 'active',    ?),
		(2, 2, 'x2', 'completed', ?)`, day20, day20)

	mustExec(`INSERT INTO events (session_id, ts, type, tool_name, payload, dedup_key) VALUES
		(1, ?, 'tool_call', 'Read', '{"input":{"file_path":"a.go"}}',            'e1'),
		(1, ?, 'tool_call', 'Bash', '{"input":{"command":"rg -n handler ./"}}',  'e2'),
		(1, ?, 'tool_call', 'Bash', '{"input":{"command":"go build ./..."}}',    'e3'),
		(1, ?, 'tool_call', 'Edit', '{"input":{"file_path":"a.go"}}',            'e4'),
		(2, ?, 'tool_call', 'Bash', '{"input":{"command":"npm run build"}}',     'e5'),
		(2, ?, 'tool_call', 'Bash', '{"input":{"command":"git status"}}',        'e6'),
		(1, ?, 'tool_call', 'Grep', '{"input":{"pattern":"stale"}}',             'e7')`,
		today, today, today, today, today, today, day20)

	h, err := NewServer(db, false)
	if err != nil {
		t.Fatalf("new server: %v", err)
	}
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return srv, todayStart.Format(dayFmt)
}

func TestAnalyticsExploration(t *testing.T) {
	srv, todayKey := explorationServer(t)

	t.Run("default range is 14 local days", func(t *testing.T) {
		var out explorationDTO
		getJSON(t, srv.URL+"/api/analytics/exploration", &out)
		if len(out.Days) != 14 {
			t.Fatalf("days = %d, want 14", len(out.Days))
		}
		last := out.Days[13]
		if last.Day != todayKey {
			t.Errorf("last bucket = %q, want %q", last.Day, todayKey)
		}
		// alpha's 2 explore + 1 run + 1 edit and beta's 2 runs; the day20 Grep
		// is outside the window.
		if last.Calls != 6 || last.Explore != 2 || last.Edit != 1 || last.Run != 3 {
			t.Errorf("today = %+v, want calls=6 explore=2 edit=1 run=3", last)
		}
		if out.Totals.Calls != 6 || out.Totals.Explore != 2 {
			t.Fatalf("totals = %+v, want calls=6 explore=2", out.Totals)
		}
		if out.Totals.Share != 2.0/6.0 {
			t.Errorf("share = %v, want %v", out.Totals.Share, 2.0/6.0)
		}
	})

	t.Run("explicit range includes the old day", func(t *testing.T) {
		now := time.Now()
		todayStart := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
		from := todayStart.AddDate(0, 0, -20).Format(dayFmt)
		var out explorationDTO
		getJSON(t, srv.URL+"/api/analytics/exploration?from="+from+"&to="+todayKey, &out)
		if len(out.Days) != 21 {
			t.Fatalf("days = %d, want 21", len(out.Days))
		}
		if out.Totals.Calls != 7 || out.Totals.Explore != 3 {
			t.Errorf("totals = %+v, want calls=7 explore=3", out.Totals)
		}
		if out.Days[0].Explore != 1 {
			t.Errorf("oldest bucket = %+v, want explore=1", out.Days[0])
		}
	})

	t.Run("project filter scopes to one project", func(t *testing.T) {
		for _, scope := range []string{"-work-alpha", "Alpha", "1"} {
			var out explorationDTO
			getJSON(t, srv.URL+"/api/analytics/exploration?project="+scope, &out)
			if out.Totals.Calls != 4 || out.Totals.Explore != 2 {
				t.Fatalf("project=%s totals = %+v, want calls=4 explore=2", scope, out.Totals)
			}
			if out.Totals.Share != 0.5 {
				t.Errorf("project=%s share = %v, want 0.5", scope, out.Totals.Share)
			}
		}

		var beta explorationDTO
		getJSON(t, srv.URL+"/api/analytics/exploration?project=-work-beta", &beta)
		if beta.Totals.Calls != 2 || beta.Totals.Explore != 0 || beta.Totals.Share != 0 {
			t.Errorf("beta totals = %+v, want calls=2 explore=0 share=0", beta.Totals)
		}
		if len(beta.Top) != 0 {
			t.Errorf("beta top = %+v, want empty", beta.Top)
		}
	})

	t.Run("top ranks explore tools, bash by its command", func(t *testing.T) {
		var out explorationDTO
		getJSON(t, srv.URL+"/api/analytics/exploration?project=-work-alpha", &out)
		if len(out.Top) != 2 {
			t.Fatalf("top = %+v, want 2 entries", out.Top)
		}
		// Equal counts → alphabetical, and "Read" sorts before "rg".
		if out.Top[0].Tool != "Read" || out.Top[0].N != 1 {
			t.Errorf("top[0] = %+v, want {Read 1}", out.Top[0])
		}
		if out.Top[1].Tool != "rg" || out.Top[1].N != 1 {
			t.Errorf("top[1] = %+v, want {rg 1}", out.Top[1])
		}
	})

	t.Run("empty range serves zeroed buckets, not null", func(t *testing.T) {
		now := time.Now()
		todayStart := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
		from := todayStart.AddDate(0, 0, -10).Format(dayFmt)
		to := todayStart.AddDate(0, 0, -8).Format(dayFmt)
		var out explorationDTO
		getJSON(t, srv.URL+"/api/analytics/exploration?from="+from+"&to="+to, &out)
		if len(out.Days) != 3 {
			t.Fatalf("days = %d, want 3", len(out.Days))
		}
		for _, d := range out.Days {
			if d.Calls != 0 || d.Share != 0 {
				t.Errorf("day %s = %+v, want zeros", d.Day, d)
			}
		}
		if out.Totals.Calls != 0 || out.Totals.Share != 0 {
			t.Errorf("totals = %+v, want zeros", out.Totals)
		}
		if out.Top == nil {
			t.Error("top = null, want []")
		}
	})

	t.Run("bad range is a 400", func(t *testing.T) {
		for _, q := range []string{"?from=yesterday", "?to=13-01-2026", "?from=2026-02-02&to=2026-02-01"} {
			resp, err := http.Get(srv.URL + "/api/analytics/exploration" + q)
			if err != nil {
				t.Fatalf("GET %s: %v", q, err)
			}
			resp.Body.Close()
			if resp.StatusCode != http.StatusBadRequest {
				t.Errorf("GET %s: status %d, want 400", q, resp.StatusCode)
			}
		}
	})
}
