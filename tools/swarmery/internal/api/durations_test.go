package api

import (
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/store"
)

// durationsServer seeds completed sessions of 10/20/40 minutes (plus one
// active and one out-of-range), and permission_requests resolved after
// 30 s / 90 s plus one still pending.
func durationsServer(t *testing.T) *httptest.Server {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "durations.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	const tsFmt = "2006-01-02T15:04:05.000Z"
	now := time.Now()
	todayStart := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	base := todayStart.Add(10 * time.Hour)
	at := func(d time.Duration) string { return base.Add(d).UTC().Format(tsFmt) }
	day20 := todayStart.AddDate(0, 0, -20).Add(12 * time.Hour).UTC().Format(tsFmt)

	mustExec := func(q string, args ...any) {
		t.Helper()
		if _, err := db.Exec(q, args...); err != nil {
			t.Fatalf("exec: %v\n%s", err, q)
		}
	}

	mustExec(`INSERT INTO projects (id, path, slug, name, first_seen) VALUES
		(1, '/work/alpha', '-work-alpha', 'Alpha', ?),
		(2, '/work/beta',  '-work-beta',  NULL,    ?)`, day20, day20)

	mustExec(`INSERT INTO sessions (id, project_id, session_uuid, model, status, started_at, ended_at) VALUES
		(1, 1, 'u1', 'claude-fable-5', 'completed', ?, ?),
		(2, 1, 'u2', 'claude-fable-5', 'completed', ?, ?),
		(3, 2, 'u3', 'claude-fable-5', 'completed', ?, ?),
		(4, 1, 'u4', 'claude-fable-5', 'active',    ?, NULL),
		(5, 1, 'u5', 'claude-fable-5', 'completed', ?, ?)`,
		at(0), at(10*time.Minute), // 600 s
		at(0), at(20*time.Minute), // 1200 s
		at(0), at(40*time.Minute), // 2400 s
		at(0),
		day20, day20) // out of the default 14-day range

	mustExec(`INSERT INTO permission_requests (session_id, tool_name, request_json, status, requested_at, resolved_at) VALUES
		(1, 'Bash', '{}', 'approved', ?, ?),
		(3, 'Bash', '{}', 'denied',   ?, ?),
		(1, 'Bash', '{}', 'pending',  ?, NULL)`,
		at(time.Hour), at(time.Hour+30*time.Second),
		at(time.Hour), at(time.Hour+90*time.Second),
		at(time.Hour))

	h, err := NewServer(db, false)
	if err != nil {
		t.Fatalf("new server: %v", err)
	}
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return srv
}

func TestStatsDurations(t *testing.T) {
	srv := durationsServer(t)

	var out durationsDTO
	getJSON(t, srv.URL+"/api/stats/durations", &out)
	if out.SessionCount != 3 { // active + out-of-range excluded
		t.Fatalf("session_count = %d, want 3", out.SessionCount)
	}
	if out.AvgSessionSec == nil || *out.AvgSessionSec != 1400 { // (600+1200+2400)/3
		t.Errorf("avg session = %v, want 1400", out.AvgSessionSec)
	}
	if out.MedianSessionSec == nil || *out.MedianSessionSec != 1200 {
		t.Errorf("median session = %v, want 1200", out.MedianSessionSec)
	}
	if out.ApprovalsResolved != 2 { // pending row excluded
		t.Errorf("approvals_resolved = %d, want 2", out.ApprovalsResolved)
	}
	if out.AvgResolveSec == nil || *out.AvgResolveSec != 60 { // (30+90)/2
		t.Errorf("avg resolve = %v, want 60", out.AvgResolveSec)
	}
	if out.WaitTotalMin != 2 { // 120 s total
		t.Errorf("wait_total_min = %v, want 2", out.WaitTotalMin)
	}

	var alpha durationsDTO
	getJSON(t, srv.URL+"/api/stats/durations?project=-work-alpha", &alpha)
	if alpha.SessionCount != 2 || alpha.AvgSessionSec == nil || *alpha.AvgSessionSec != 900 {
		t.Errorf("filtered = %+v, want 2 sessions avg 900", alpha)
	}
	if alpha.ApprovalsResolved != 1 { // beta's 90 s approval filtered out
		t.Errorf("filtered approvals = %d, want 1", alpha.ApprovalsResolved)
	}
}
