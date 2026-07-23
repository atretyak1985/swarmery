package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/improve"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/store"
)

// improveMockRunner returns a fixed model output (or error) — no real
// claude anywhere in the API tests.
type improveMockRunner struct {
	out string
	err error
}

func (m *improveMockRunner) Run(context.Context, string) (string, error) { return m.out, m.err }

// improveValidOut satisfies the splitDiffRationale contract.
const improveValidOut = "## Diff\n```diff\n--- a/x.md\n+++ b/x.md\n@@ -1 +1 @@\n-a\n+b\n```\n## Rationale\nEvidence-backed fix.\n"

// improveServer builds an httptest server whose improve pipeline runs INLINE
// (improveGo seam), so a 202 means the row already landed when the request
// returns.
func improveServer(t *testing.T, runner improve.Runner) (*httptest.Server, *sql.DB) {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "improve-api.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	h := &Handler{
		DB:        db,
		Improve:   &improve.Service{DB: db, Runner: runner},
		improveGo: func(fn func()) { fn() },
	}
	mux := http.NewServeMux()
	Routes(mux, h)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv, db
}

func improveExec(t *testing.T, db *sql.DB, q string, args ...any) {
	t.Helper()
	if _, err := db.Exec(q, args...); err != nil {
		t.Fatalf("exec: %v\n%s", err, q)
	}
}

// seedRegistryAgent registers one live agent + current version.
func seedRegistryAgent(t *testing.T, db *sql.DB, id int64, name string) {
	t.Helper()
	improveExec(t, db, `INSERT INTO agents (id, name, scope, file_path, origin)
		VALUES (?, ?, 'global', ?, 'local')`, id, name, "/x/"+name+".md")
	improveExec(t, db, `INSERT INTO agent_versions (id, agent_id, content_hash, content, created_at)
		VALUES (?, ?, ?, 'agent body', '2026-07-20T00:00:00Z')`, id, id, "h"+name)
	improveExec(t, db, `UPDATE agents SET current_version_id = ? WHERE id = ?`, id, id)
}

func seedRecommendation(t *testing.T, db *sql.DB, id int64, kind, target, status string) {
	t.Helper()
	improveExec(t, db, `INSERT INTO recommendations
		(id, rule, target_kind, target, title, detail, evidence, status, dedup_key, created_at, updated_at)
		VALUES (?, 'R2', ?, ?, 't', 'd', '{}', ?, ?, ?, ?)`,
		id, kind, target, status, "R2:"+target+":"+status, retroDay(t, 1), retroDay(t, 1))
}

// postJSON fires a body-less POST and decodes the JSON reply.
func postJSON(t *testing.T, url string, wantStatus int) map[string]any {
	t.Helper()
	resp, err := http.Post(url, "application/json", nil)
	if err != nil {
		t.Fatalf("POST %s: %v", url, err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != wantStatus {
		t.Fatalf("POST %s = %d (%s), want %d", url, resp.StatusCode, raw, wantStatus)
	}
	var out map[string]any
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &out); err != nil {
			t.Fatalf("decode %s: %v\n%s", url, err, raw)
		}
	}
	return out
}

func proposalCount(t *testing.T, db *sql.DB, where string, args ...any) int {
	t.Helper()
	var n int
	q := `SELECT COUNT(*) FROM agent_change_proposals`
	if where != "" {
		q += " WHERE " + where
	}
	if err := db.QueryRow(q, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestImproveRecommendationMatrix(t *testing.T) {
	srv, db := improveServer(t, &improveMockRunner{out: improveValidOut})
	seedRegistryAgent(t, db, 1, "tech-lead")
	seedRecommendation(t, db, 1, "agent", "core:tech-lead", "accepted")
	seedRecommendation(t, db, 2, "agent", "tech-lead", "proposed")
	seedRecommendation(t, db, 3, "tool", "Bash", "accepted")
	seedRecommendation(t, db, 4, "agent", "ghost-agent", "accepted")

	// 404 unknown recommendation.
	postJSON(t, srv.URL+"/api/retro/recommendations/999/improve", http.StatusNotFound)
	// 422 not accepted.
	postJSON(t, srv.URL+"/api/retro/recommendations/2/improve", http.StatusUnprocessableEntity)
	// 422 not agent-kind.
	postJSON(t, srv.URL+"/api/retro/recommendations/3/improve", http.StatusUnprocessableEntity)
	// 422 target not in the registry.
	postJSON(t, srv.URL+"/api/retro/recommendations/4/improve", http.StatusUnprocessableEntity)
	if n := proposalCount(t, db, ""); n != 0 {
		t.Fatalf("rejected triggers wrote %d proposals, want 0", n)
	}

	// Happy path: 202, inline pipeline landed a proposed row with the "core:"
	// target folded to the registry key and the recommendation linked.
	out := postJSON(t, srv.URL+"/api/retro/recommendations/1/improve", http.StatusAccepted)
	if out["agent"] != "tech-lead" || out["status"] != "generating" {
		t.Errorf("202 body = %v", out)
	}
	if n := proposalCount(t, db,
		`agent = 'tech-lead' AND status = 'proposed' AND recommendation_id = 1`); n != 1 {
		t.Fatalf("proposed rows linked to rec 1 = %d, want 1", n)
	}

	// 409 while that proposal stays open.
	out = postJSON(t, srv.URL+"/api/retro/recommendations/1/improve", http.StatusConflict)
	if _, ok := out["proposal_id"]; !ok {
		t.Errorf("409 body missing proposal_id: %v", out)
	}
}

func TestImproveAgentEndpoint(t *testing.T) {
	srv, db := improveServer(t, &improveMockRunner{out: improveValidOut})
	seedRegistryAgent(t, db, 1, "code-auditor")

	// 404 unknown agent.
	postJSON(t, srv.URL+"/api/retro/agents/ghost/improve", http.StatusNotFound)

	// Happy path folds the "core:" notation and keeps recommendation_id NULL.
	postJSON(t, srv.URL+"/api/retro/agents/core:code-auditor/improve", http.StatusAccepted)
	if n := proposalCount(t, db,
		`agent = 'code-auditor' AND status = 'proposed' AND recommendation_id IS NULL`); n != 1 {
		t.Fatal("ad-hoc trigger did not land an unlinked proposed row")
	}

	// 409 on the second trigger.
	postJSON(t, srv.URL+"/api/retro/agents/code-auditor/improve", http.StatusConflict)
}

// A runner failure surfaces as a failed row (daemon alive, 202 already sent).
func TestImproveRunnerFailureLandsFailedRow(t *testing.T) {
	runner := &improveMockRunner{err: errStderr("boom from claude")}
	srv, db := improveServer(t, runner)
	seedRegistryAgent(t, db, 1, "tech-lead")

	postJSON(t, srv.URL+"/api/retro/agents/tech-lead/improve", http.StatusAccepted)
	if n := proposalCount(t, db, `agent = 'tech-lead' AND status = 'failed' AND error LIKE '%boom from claude%'`); n != 1 {
		t.Fatal("runner failure did not land as a failed row with the stderr text")
	}
}

func TestListProposals(t *testing.T) {
	srv, db := improveServer(t, &improveMockRunner{out: improveValidOut})
	improveExec(t, db, `INSERT INTO agent_change_proposals
		(agent, agent_path, base_sha256, diff, rationale, status, error, created_at) VALUES
		('a1', '/x/a1.md', 's1', 'd1', 'r1', 'proposed', NULL,   '2026-07-20T00:00:00.000Z'),
		('a2', '/x/a2.md', 's2', '',   '',   'failed',   'oops', '2026-07-21T00:00:00.000Z'),
		('a3', '/x/a3.md', 's3', 'd3', 'r3', 'approved', NULL,   '2026-07-22T00:00:00.000Z')`)

	get := func(q string, wantStatus int) proposalsDTO {
		t.Helper()
		resp, err := http.Get(srv.URL + "/api/retro/proposals" + q)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		raw, _ := io.ReadAll(resp.Body)
		if resp.StatusCode != wantStatus {
			t.Fatalf("GET %s = %d (%s), want %d", q, resp.StatusCode, raw, wantStatus)
		}
		var out proposalsDTO
		if wantStatus == http.StatusOK {
			if err := json.Unmarshal(raw, &out); err != nil {
				t.Fatalf("decode: %v\n%s", err, raw)
			}
		}
		return out
	}

	// Unfiltered: everything, newest first.
	all := get("", http.StatusOK)
	if len(all.Proposals) != 3 || all.Proposals[0].Agent != "a3" || all.Proposals[2].Agent != "a1" {
		t.Errorf("unfiltered list wrong: %+v", all.Proposals)
	}
	if all.Proposals[1].Error == nil || *all.Proposals[1].Error != "oops" {
		t.Errorf("failed row error not surfaced: %+v", all.Proposals[1])
	}

	// Filtered by status set.
	open := get("?status=proposed,approved", http.StatusOK)
	if len(open.Proposals) != 2 {
		t.Errorf("status filter returned %d rows, want 2", len(open.Proposals))
	}

	// Unknown status is a 400, not a silent empty list.
	get("?status=draft", http.StatusBadRequest)
}

func TestRetryProposalMatrix(t *testing.T) {
	runner := &improveMockRunner{err: errStderr("still broken")}
	srv, db := improveServer(t, runner)
	seedRegistryAgent(t, db, 1, "tech-lead")

	// Land a failed row through the real pipeline.
	postJSON(t, srv.URL+"/api/retro/agents/tech-lead/improve", http.StatusAccepted)
	var failedID int64
	if err := db.QueryRow(`SELECT id FROM agent_change_proposals WHERE status = 'failed'`).Scan(&failedID); err != nil {
		t.Fatalf("no failed row: %v", err)
	}
	url := func(id int64) string {
		return srv.URL + "/api/retro/proposals/" + strconv.FormatInt(id, 10) + "/retry"
	}

	// 404 unknown proposal.
	postJSON(t, srv.URL+"/api/retro/proposals/999/retry", http.StatusNotFound)

	// Retry with a recovered runner: 202, row flips failed → proposed in place.
	runner.err, runner.out = nil, improveValidOut
	postJSON(t, url(failedID), http.StatusAccepted)
	if n := proposalCount(t, db, `id = ? AND status = 'proposed' AND error IS NULL`, failedID); n != 1 {
		t.Fatal("retry did not flip the failed row to proposed")
	}

	// 422: the row is no longer failed.
	postJSON(t, url(failedID), http.StatusUnprocessableEntity)

	// 409: a NEW failed row for the same agent cannot be retried while the
	// proposed one is open.
	improveExec(t, db, `INSERT INTO agent_change_proposals
		(agent, agent_path, base_sha256, diff, rationale, status, error, created_at)
		VALUES ('tech-lead', '/x/tech-lead.md', 's', '', '', 'failed', 'old', '2026-07-22T00:00:00.000Z')`)
	var secondID int64
	if err := db.QueryRow(`SELECT id FROM agent_change_proposals WHERE status = 'failed'`).Scan(&secondID); err != nil {
		t.Fatal(err)
	}
	postJSON(t, url(secondID), http.StatusConflict)
}

// errStderr builds a runner error shaped like ClaudeRunner's stderr capture.
func errStderr(msg string) error {
	return &stderrErr{msg: msg}
}

type stderrErr struct{ msg string }

func (e *stderrErr) Error() string { return "claude -p: exit status 1; stderr: " + e.msg }
