package api

// fusion phase 12: per-project scoping of the advisor recommendations feed.
// GET /api/retro/recommendations?projectId= filters the global recs down to
// those whose evidence session_ids resolve to the project's sessions.

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/store"
)

// recScopeServer seeds two projects, each with its own session, plus four
// recommendations: two attributable to project A (evidence names A's session),
// one to project B, one fleet-level (no session_ids — an R5-style row), and two
// that name a project as their TARGET with no session evidence (R10 memory for
// A, R7 architecture map for B) — attributed by target, not by evidence.
func recScopeServer(t *testing.T) *httptest.Server {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "recscope.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	today := retroDay(t, 0)
	mustExec := func(q string, args ...any) {
		t.Helper()
		if _, err := db.Exec(q, args...); err != nil {
			t.Fatalf("exec: %v\n%s", err, q)
		}
	}
	mustExec(`INSERT INTO projects (id, path, slug, name, first_seen) VALUES
		(1, '/work/alpha', '-work-alpha', 'Alpha', ?),
		(2, '/work/beta',  '-work-beta',  'Beta',  ?)`, today, today)
	mustExec(`INSERT INTO sessions (id, project_id, session_uuid, status, started_at) VALUES
		(1, 1, 'sess-alpha', 'completed', ?),
		(2, 2, 'sess-beta',  'completed', ?)`, today, today)

	// Two recs for A, one for B, one fleet-level (R5, no session_ids), and one
	// project-targeted rec each for A (R10) and B (R7).
	rows := []struct {
		id       int
		rule     string
		kind     string
		target   string
		evidence string
	}{
		{1, "R2", "agent", "agent-a1", `{"session_ids":["sess-alpha"]}`},
		{2, "R1", "tool", "Bash", `{"session_ids":["sess-alpha","other-x"]}`},
		{3, "R2", "agent", "agent-b1", `{"session_ids":["sess-beta"]}`},
		{4, "R5", "process", "stale-improvement", `{"counts":{"stale":2}}`},
		{5, "R10", "memory", "-work-alpha", `{"counts":{"index_lines":400}}`},
		{6, "R7", "project", "-work-beta", `{"counts":{"age_days":40}}`},
	}
	for _, r := range rows {
		mustExec(`INSERT INTO recommendations
			(id, rule, target_kind, target, title, detail, evidence, status, dedup_key, created_at, updated_at)
			VALUES (?, ?, ?, ?, 'T', 'D', ?, 'proposed', ?, ?, ?)`,
			r.id, r.rule, r.kind, r.target, r.evidence, r.rule+":"+r.target, today, today)
	}

	h, err := NewServer(db, false)
	if err != nil {
		t.Fatalf("new server: %v", err)
	}
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return srv
}

func recTargets(t *testing.T, srv *httptest.Server, path string) map[string]bool {
	t.Helper()
	var out recsResp
	getJSON(t, srv.URL+path, &out)
	got := map[string]bool{}
	for _, r := range out.Recommendations {
		got[r.Target] = true
	}
	return got
}

func TestRecommendationsProjectScope(t *testing.T) {
	srv := recScopeServer(t)

	t.Run("scoped to project A keeps only A-attributed recs", func(t *testing.T) {
		got := recTargets(t, srv, "/api/retro/recommendations?projectId=-work-alpha")
		if len(got) != 3 || !got["agent-a1"] || !got["Bash"] || !got["-work-alpha"] {
			t.Errorf("targets = %v, want exactly {agent-a1, Bash, -work-alpha}", got)
		}
		if got["agent-b1"] || got["stale-improvement"] || got["-work-beta"] {
			t.Errorf("targets = %v leaked B or fleet-level recs into project A", got)
		}
	})

	t.Run("scoped by numeric id resolves the same set", func(t *testing.T) {
		got := recTargets(t, srv, "/api/retro/recommendations?projectId=1")
		if len(got) != 3 || !got["agent-a1"] || !got["Bash"] || !got["-work-alpha"] {
			t.Errorf("targets = %v, want {agent-a1, Bash, -work-alpha} by id", got)
		}
	})

	t.Run("scoped by pretty name resolves the project-targeted rec too", func(t *testing.T) {
		got := recTargets(t, srv, "/api/retro/recommendations?projectId=alpha")
		if !got["-work-alpha"] {
			t.Errorf("targets = %v, want the R10 rec targeting -work-alpha by name", got)
		}
	})

	t.Run("project B sees only its own rec", func(t *testing.T) {
		got := recTargets(t, srv, "/api/retro/recommendations?projectId=-work-beta")
		if len(got) != 2 || !got["agent-b1"] || !got["-work-beta"] {
			t.Errorf("targets = %v, want exactly {agent-b1, -work-beta}", got)
		}
	})

	t.Run("unscoped returns all six", func(t *testing.T) {
		got := recTargets(t, srv, "/api/retro/recommendations")
		if len(got) != 6 {
			t.Errorf("unscoped targets = %v, want all 6", got)
		}
	})

	t.Run("unknown project yields no recs", func(t *testing.T) {
		var out recsResp
		getJSON(t, srv.URL+"/api/retro/recommendations?projectId=-work-ghost", &out)
		if len(out.Recommendations) != 0 {
			t.Errorf("unknown project returned %d recs, want 0", len(out.Recommendations))
		}
	})
}

// The advise endpoint accepts ?projectId= (API symmetry) but always runs
// fleet-wide — it must still 200 with a Stats body when scoped.
func TestRetroAdviseAcceptsProjectID(t *testing.T) {
	srv := recScopeServer(t)
	res, err := http.Post(srv.URL+"/api/retro/advise?projectId=-work-alpha", "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("advise?projectId status = %d, want 200", res.StatusCode)
	}
	var stats struct {
		Proposed int `json:"proposed"`
		Updated  int `json:"updated"`
	}
	if err := json.NewDecoder(res.Body).Decode(&stats); err != nil {
		t.Fatalf("decode advise stats: %v", err)
	}
}
