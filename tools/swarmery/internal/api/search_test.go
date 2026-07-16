package api

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/store"
)

// searchServer plants a fixture for /api/search and /api/files/sessions:
// project alpha (visible), beta (visible), old (ARCHIVED); session 1 matches
// 'tokamak' by title AND branch, session 3 is HIDDEN, session 4 belongs to
// the archived project. Turn text is written INSERT-then-UPDATE — the exact
// ingester write path — so the 0012 triggers are exercised end-to-end.
func searchServer(t *testing.T) *httptest.Server {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "search.db"))
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

	const ts = "2026-07-15T10:00:00.000Z"
	mustExec(`INSERT INTO projects (id, path, slug, name, first_seen, archived) VALUES
		(1, '/work/alpha', '-work-alpha', 'Alpha', ?, 0),
		(2, '/work/beta',  '-work-beta',  'Beta',  ?, 0),
		(3, '/work/old',   '-work-old',   'Tokamak Legacy', ?, 1)`, ts, ts, ts)

	mustExec(`INSERT INTO sessions (id, project_id, session_uuid, git_branch, status, started_at, title, hidden) VALUES
		(1, 1, 'u1', 'fix/tokamak-cooling', 'completed', ?, 'Tokamak cooling loop',      0),
		(2, 1, 'u2', 'main',                'completed', ?, 'Unrelated work',            0),
		(3, 2, 'u3', 'main',                'completed', ?, 'Hidden tokamak session',    1),
		(4, 3, 'u4', 'main',                'completed', ?, 'Archived tokamak session',  0)`,
		ts, ts, ts, ts)

	mustExec(`INSERT INTO turns (session_id, seq, role, started_at, agent_name) VALUES
		(1, 0, 'user',      ?, NULL),
		(1, 1, 'assistant', ?, 'tech-lead'),
		(2, 0, 'assistant', ?, NULL),
		(3, 0, 'user',      ?, NULL)`, ts, ts, ts, ts)
	mustExec(`UPDATE turns SET text = 'tokamak tokamak tokamak' WHERE session_id = 1 AND seq = 0`)
	mustExec(`UPDATE turns SET text = 'a long reply that mentions the tokamak once among many other words about plasma containment and magnet cooling loops' WHERE session_id = 1 AND seq = 1`)
	mustExec(`UPDATE turns SET text = 'nothing relevant here at all' WHERE session_id = 2 AND seq = 0`)
	mustExec(`UPDATE turns SET text = 'hidden tokamak prose' WHERE session_id = 3 AND seq = 0`)

	// Files: sessions 1 and 2 both touched reactor.go (2 changes in s2);
	// s2's later event drives the recency ordering of the reverse lookup.
	mustExec(`INSERT INTO events (id, session_id, ts, type, dedup_key) VALUES
		(1, 1, '2026-07-15T10:05:00.000Z', 'file_change', 'ev1'),
		(2, 2, '2026-07-15T11:00:00.000Z', 'file_change', 'ev2'),
		(3, 2, '2026-07-15T11:30:00.000Z', 'file_change', 'ev3')`)
	mustExec(`INSERT INTO file_changes (event_id, session_id, file_path, change_type, additions, deletions) VALUES
		(1, 1, 'internal/reactor/reactor.go', 'edit',   10, 2),
		(2, 2, 'internal/reactor/reactor.go', 'edit',    5, 1),
		(3, 2, 'internal/reactor/reactor.go', 'edit',    3, 0)`)

	h, err := NewServer(db, false)
	if err != nil {
		t.Fatalf("new server: %v", err)
	}
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return srv
}

type searchResp struct {
	Query    string `json:"query"`
	Sessions []struct {
		ID          int64   `json:"id"`
		Title       *string `json:"title"`
		GitBranch   *string `json:"gitBranch"`
		ProjectSlug string  `json:"projectSlug"`
	} `json:"sessions"`
	Turns []struct {
		TurnID       int64   `json:"turnId"`
		SessionID    int64   `json:"sessionId"`
		SessionTitle *string `json:"sessionTitle"`
		ProjectSlug  string  `json:"projectSlug"`
		StartedAt    string  `json:"startedAt"`
		Role         string  `json:"role"`
		AgentName    *string `json:"agentName"`
		Snippet      string  `json:"snippet"`
	} `json:"turns"`
	Files []struct {
		Path        string `json:"path"`
		Sessions    int64  `json:"sessions"`
		LastTouched string `json:"lastTouched"`
	} `json:"files"`
	Projects []struct {
		ID   int64   `json:"id"`
		Slug string  `json:"slug"`
		Name *string `json:"name"`
	} `json:"projects"`
}

func TestSearchEndpoint(t *testing.T) {
	srv := searchServer(t)

	t.Run("grouped results, hidden and archived excluded", func(t *testing.T) {
		var r searchResp
		getJSON(t, srv.URL+"/api/search?q=tokamak", &r)

		// Sessions: only s1 (title+branch match); s3 hidden, s4 archived project.
		if len(r.Sessions) != 1 || r.Sessions[0].ID != 1 {
			t.Fatalf("sessions = %+v, want only session 1", r.Sessions)
		}
		// Turns: both alpha turns; the term-dense user turn ranks first (bm25).
		if len(r.Turns) != 2 {
			t.Fatalf("turns = %d, want 2 (hidden session's turn excluded)", len(r.Turns))
		}
		if r.Turns[0].SessionID != 1 || r.Turns[0].Role != "user" {
			t.Errorf("turns[0] = %+v, want the term-dense user turn first", r.Turns[0])
		}
		if !strings.Contains(r.Turns[0].Snippet, "⟦tokamak⟧") {
			t.Errorf("snippet %q: missing ⟦⟧ highlight markers", r.Turns[0].Snippet)
		}
		if r.Turns[1].AgentName == nil || *r.Turns[1].AgentName != "tech-lead" {
			t.Errorf("turns[1].agentName = %v, want tech-lead", r.Turns[1].AgentName)
		}
		// Projects: the only name match is archived → empty group.
		if len(r.Projects) != 0 {
			t.Errorf("projects = %+v, want none (archived excluded)", r.Projects)
		}
		// Files: 'tokamak' matches no path.
		if len(r.Files) != 0 {
			t.Errorf("files = %+v, want none", r.Files)
		}
	})

	t.Run("file group matches path substring", func(t *testing.T) {
		var r searchResp
		getJSON(t, srv.URL+"/api/search?q=reactor.go", &r)
		if len(r.Files) != 1 || r.Files[0].Path != "internal/reactor/reactor.go" ||
			r.Files[0].Sessions != 2 || r.Files[0].LastTouched != "2026-07-15T11:30:00.000Z" {
			t.Errorf("files = %+v, want reactor.go with 2 sessions, last 11:30", r.Files)
		}
	})

	t.Run("prefix match while typing", func(t *testing.T) {
		var r searchResp
		getJSON(t, srv.URL+"/api/search?q=tokam", &r)
		if len(r.Turns) != 2 {
			t.Errorf("prefix turns = %d, want 2 ('tokam' → \"tokam\"*)", len(r.Turns))
		}
	})

	t.Run("project scoping", func(t *testing.T) {
		var r searchResp
		getJSON(t, srv.URL+"/api/search?q=tokamak&project=-work-beta", &r)
		if len(r.Sessions) != 0 || len(r.Turns) != 0 || len(r.Files) != 0 {
			t.Errorf("beta-scoped = %d/%d/%d sessions/turns/files, want 0/0/0",
				len(r.Sessions), len(r.Turns), len(r.Files))
		}
	})

	t.Run("limit caps each group", func(t *testing.T) {
		var r searchResp
		getJSON(t, srv.URL+"/api/search?q=tokamak&limit=1", &r)
		if len(r.Turns) != 1 {
			t.Errorf("turns with limit=1 = %d, want 1", len(r.Turns))
		}
	})

	t.Run("hostile FTS syntax never 500s", func(t *testing.T) {
		for _, q := range []string{`"foo`, `foo OR (`, `col:val NEAR/x`, `*`, `""`} {
			resp, err := http.Get(srv.URL + "/api/search?q=" + urlQueryEscape(q))
			if err != nil {
				t.Fatal(err)
			}
			resp.Body.Close()
			if resp.StatusCode == http.StatusInternalServerError {
				t.Errorf("q=%q → 500, want safe handling", q)
			}
		}
	})

	t.Run("missing q → 400", func(t *testing.T) {
		for _, u := range []string{"/api/search", "/api/search?q=", "/api/search?q=%20"} {
			resp, err := http.Get(srv.URL + u)
			if err != nil {
				t.Fatal(err)
			}
			resp.Body.Close()
			if resp.StatusCode != http.StatusBadRequest {
				t.Errorf("%s status = %d, want 400", u, resp.StatusCode)
			}
		}
	})

	t.Run("invalid limit → 400", func(t *testing.T) {
		resp, err := http.Get(srv.URL + "/api/search?q=x&limit=abc")
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("limit=abc status = %d, want 400", resp.StatusCode)
		}
	})
}

// urlQueryEscape percent-encodes the handful of characters the hostile-input
// cases use, without pulling net/url into the import block.
func urlQueryEscape(s string) string {
	r := strings.NewReplacer(`"`, "%22", " ", "%20", "(", "%28", "*", "%2A", "/", "%2F")
	return r.Replace(s)
}
