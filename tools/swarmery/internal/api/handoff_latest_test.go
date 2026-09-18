package api

import (
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"testing"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/ingest"
)

// latestHandoffGet issues GET /api/handoffs/latest?cwd=… and returns the
// response; the caller closes the body.
func latestHandoffGet(t *testing.T, base, cwd string) *http.Response {
	t.Helper()
	resp, err := http.Get(base + "/api/handoffs/latest?" + url.Values{"cwd": {cwd}}.Encode())
	if err != nil {
		t.Fatalf("GET latest handoff: %v", err)
	}
	return resp
}

// The project fixture (handoffTestServer) registers project '/tmp/hp' with
// session 1 on it; the cases below only vary the cwd the hook would send.
func TestLatestHandoffResolvesCwdToProject(t *testing.T) {
	srv, db := handoffTestServer(t)

	brief := "# Handoff: finish the context bridge\n## Next step\n- run the suite\n"
	path := filepath.Join(t.TempDir(), "u-ho-1.md")
	if err := os.WriteFile(path, []byte(brief), 0o644); err != nil {
		t.Fatalf("write brief: %v", err)
	}
	// Two rows: the newest must win regardless of insert order.
	if _, err := db.Exec(`INSERT INTO handoffs (session_id, path, context_tokens, created_at)
		VALUES (1, '/nonexistent/older.md', 90000, '2026-09-01T09:00:00Z')`); err != nil {
		t.Fatalf("insert older handoff: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO handoffs (session_id, path, context_tokens, created_at)
		VALUES (1, ?, 164000, '2026-09-17T18:30:00Z')`, path); err != nil {
		t.Fatalf("insert handoff: %v", err)
	}

	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatalf("home dir: %v", err)
	}
	// A dispatcher worktree lives at <home>/.swarmery/worktrees/<projectSlug>/<task>.
	worktreeCwd := filepath.Join(home, ".swarmery", "worktrees", ingest.SlugForPath("/tmp/hp"), "task-1")

	cases := []struct {
		name string
		cwd  string
		want int
	}{
		{"the project cwd itself", "/tmp/hp", http.StatusOK},
		{"a subdirectory of the project", "/tmp/hp/apps/web", http.StatusOK},
		{"a dispatcher worktree resolves to its parent repo", worktreeCwd, http.StatusOK},
		{"an unregistered project has nothing to inject", "/tmp/hp-unrelated", http.StatusNoContent},
		{"a relative cwd is a client error", "not/absolute", http.StatusBadRequest},
		{"an empty cwd is a client error", "", http.StatusBadRequest},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resp := latestHandoffGet(t, srv.URL, tc.cwd)
			defer resp.Body.Close()
			if resp.StatusCode != tc.want {
				t.Fatalf("status = %d, want %d", resp.StatusCode, tc.want)
			}
			if tc.want != http.StatusOK {
				return
			}
			var got struct {
				SessionUUID   string `json:"session_uuid"`
				CreatedAt     string `json:"created_at"`
				ContextTokens int64  `json:"context_tokens"`
				Brief         string `json:"brief"`
			}
			if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
				t.Fatalf("decode: %v", err)
			}
			if got.Brief != brief {
				t.Errorf("brief = %q, want %q", got.Brief, brief)
			}
			if got.SessionUUID != "u-ho-1" {
				t.Errorf("session_uuid = %q, want u-ho-1", got.SessionUUID)
			}
			if got.CreatedAt != "2026-09-17T18:30:00Z" {
				t.Errorf("created_at = %q — the newest row must win", got.CreatedAt)
			}
			if got.ContextTokens != 164000 {
				t.Errorf("context_tokens = %d, want 164000", got.ContextTokens)
			}
		})
	}
}

func TestLatestHandoff204WhenBriefFileGone(t *testing.T) {
	srv, db := handoffTestServer(t)
	if _, err := db.Exec(`INSERT INTO handoffs (session_id, path, context_tokens, created_at)
		VALUES (1, '/nonexistent/gone.md', 160000, '2026-09-17T18:30:00Z')`); err != nil {
		t.Fatalf("insert handoff: %v", err)
	}
	resp := latestHandoffGet(t, srv.URL, "/tmp/hp")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("status = %d, want 204 when the recorded brief is gone", resp.StatusCode)
	}
}

func TestLatestHandoff204WhenProjectHasNoHandoff(t *testing.T) {
	srv, _ := handoffTestServer(t)
	resp := latestHandoffGet(t, srv.URL, "/tmp/hp")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("status = %d, want 204 for a project with no handoff", resp.StatusCode)
	}
}

// A read failure that is NOT ErrNotExist must still be 204: here the row points
// at a directory, so os.ReadFile fails with EISDIR. A 500 would break the
// handler's contract and echo the absolute brief path back into the response.
func TestLatestHandoff204WhenBriefUnreadable(t *testing.T) {
	srv, db := handoffTestServer(t)
	dir := t.TempDir()
	if _, err := db.Exec(`INSERT INTO handoffs (session_id, path, context_tokens, created_at)
		VALUES (1, ?, 160000, '2026-09-17T18:30:00Z')`, dir); err != nil {
		t.Fatalf("insert handoff: %v", err)
	}
	resp := latestHandoffGet(t, srv.URL, "/tmp/hp")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("status = %d, want 204 when the recorded brief cannot be read", resp.StatusCode)
	}
	if body, _ := io.ReadAll(resp.Body); len(body) != 0 {
		t.Errorf("body = %q — a 204 must not leak the brief path", body)
	}
}
