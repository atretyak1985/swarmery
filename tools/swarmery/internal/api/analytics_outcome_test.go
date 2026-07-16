package api

import (
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/store"
)

// outcomeServer plants 4 sessions today: success / fail / no-outcome /
// abandoned. tech-lead turns appear in all four (one via the "core:" notation
// to exercise the agentKey fold); "main" (NULL agent_name) turns only in the
// success session. Expected rates: tech-lead 1/(1+1)=0.5 (no-outcome and
// abandoned excluded), main 1/1=1.0.
func outcomeServer(t *testing.T) *httptest.Server {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "outcome.db"))
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
	today := time.Date(now.Year(), now.Month(), now.Day(), 12, 0, 0, 0, now.Location()).
		UTC().Format("2006-01-02T15:04:05.000Z")

	mustExec(`INSERT INTO projects (id, path, slug, name, first_seen) VALUES
		(1, '/work/o', '-work-o', 'O', ?)`, today)
	mustExec(`INSERT INTO sessions (id, project_id, session_uuid, status, started_at, outcome) VALUES
		(1, 1, 'o1', 'completed', ?, 'success'),
		(2, 1, 'o2', 'completed', ?, 'fail'),
		(3, 1, 'o3', 'completed', ?, NULL),
		(4, 1, 'o4', 'completed', ?, 'abandoned')`, today, today, today, today)
	mustExec(`INSERT INTO turns (session_id, seq, role, started_at, agent_name, tokens_in, tokens_out, cost_usd) VALUES
		(1, 0, 'assistant', ?, NULL,             100, 50, 0.5),
		(1, 1, 'assistant', ?, 'tech-lead',      10,  5,  0.1),
		(2, 0, 'assistant', ?, 'core:tech-lead', 10,  5,  0.1),
		(3, 0, 'assistant', ?, 'tech-lead',      10,  5,  0.1),
		(4, 0, 'assistant', ?, 'tech-lead',      10,  5,  0.1)`,
		today, today, today, today, today)

	h, err := NewServer(db, false)
	if err != nil {
		t.Fatalf("new server: %v", err)
	}
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return srv
}

func TestBreakdownAgentSuccessRate(t *testing.T) {
	srv := outcomeServer(t)
	var rows []struct {
		Key         string   `json:"key"`
		SuccessRate *float64 `json:"success_rate"`
	}
	getJSON(t, srv.URL+"/api/stats/breakdown?by=agent", &rows)
	byKey := map[string]*float64{}
	for _, r := range rows {
		byKey[r.Key] = r.SuccessRate
	}
	if rate, ok := byKey["tech-lead"]; !ok || rate == nil || *rate != 0.5 {
		t.Errorf("tech-lead success_rate = %v, want 0.5", rate)
	}
	if rate, ok := byKey["main"]; !ok || rate == nil || *rate != 1.0 {
		t.Errorf("main success_rate = %v, want 1.0", rate)
	}
}

func TestBreakdownProjectHasNoSuccessRate(t *testing.T) {
	srv := outcomeServer(t)
	var rows []struct {
		Key         string   `json:"key"`
		SuccessRate *float64 `json:"success_rate"`
	}
	getJSON(t, srv.URL+"/api/stats/breakdown?by=project", &rows)
	for _, r := range rows {
		if r.SuccessRate != nil {
			t.Errorf("project %q success_rate = %v, want null (agent pivot only)", r.Key, *r.SuccessRate)
		}
	}
}
