package api

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/store"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/wtcleanup"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/wtjanitor"
)

// recordingCleanupForcer removes nothing and records every path it is handed.
type recordingCleanupForcer struct{ handed []string }

func (r *recordingCleanupForcer) ForceUnmerged(_ string, paths []string) []wtjanitor.ForceResult {
	r.handed = append(r.handed, paths...)
	out := make([]wtjanitor.ForceResult, len(paths))
	for i, p := range paths {
		out[i] = wtjanitor.ForceResult{Path: p, Outcome: wtjanitor.ForceRemoved, SalvageBranch: "salvage/swarm-phase-1-x"}
	}
	return out
}

// stubCleanupGit reports every proposed path as still on its proposed branch.
type stubCleanupGit struct{ branches map[string]string }

func (g stubCleanupGit) Worktrees(string) ([]wtjanitor.Worktree, error) {
	var out []wtjanitor.Worktree
	for p, b := range g.branches {
		out = append(out, wtjanitor.Worktree{Path: p, Branch: b})
	}
	return out, nil
}
func (stubCleanupGit) ContentInMain(string, string) (bool, string, error) { return false, "", nil }
func (stubCleanupGit) Dirty(string) (int, error)                          { return 0, nil }

// cleanupServer: two projects, a plan with a phase, a board card, and the
// proposals `wtcleanup propose` would write for them.
func cleanupServer(t *testing.T) (*httptest.Server, *sql.DB, *recordingCleanupForcer) {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "cleanup.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	exec := func(q string, args ...any) int64 {
		t.Helper()
		res, err := db.Exec(q, args...)
		if err != nil {
			t.Fatalf("exec: %v\n%s", err, q)
		}
		id, _ := res.LastInsertId()
		return id
	}
	exec(`INSERT INTO projects (id, path, slug, name, first_seen) VALUES
		(1, '/work/alpha', '-work-alpha', 'Alpha', '2026-10-01T00:00:00Z'),
		(2, '/work/beta', '-work-beta', 'Beta', '2026-10-01T00:00:00Z')`)
	plan := exec(`INSERT INTO tasks (project_id, title, prompt, status, created_at, source, external_id)
		VALUES (1, 'Cleanup plan', 'p', 'running', '2026-10-01T00:00:00Z', 'workspace', '2026-10-01-cleanup')`)
	phase := exec(`INSERT INTO epic_phases (workspace_task_id, seq, name, doc_path, doc_status, landing_state)
		VALUES (?, 4, 'Landing', '/ws/p4.md', 'done', 'merged')`, plan)
	card := exec(`INSERT INTO tasks (project_id, title, prompt, status, created_at, source, external_id, board_column)
		VALUES (1, 'Fix the thing', 'p', 'done', '2026-10-01T00:00:00Z', 'queue', 'T-abc', 'done')`)

	scan := wtcleanup.Scan{Candidates: []wtcleanup.Candidate{
		{RepoRoot: "/work/alpha", ProjectID: 1, ProjectSlug: "-work-alpha", Path: "/wt/phase-4", Branch: "swarm/phase-4",
			PhaseID: phase, TaskID: plan, Proof: wtcleanup.Proof{Kind: wtcleanup.ProofLanding, Detail: "phase landing merged"}},
		{RepoRoot: "/work/alpha", ProjectID: 1, ProjectSlug: "-work-alpha", Path: "/wt/T-abc", Branch: "swarm/T-abc",
			TaskID: card, Proof: wtcleanup.Proof{Kind: wtcleanup.ProofContentDiff, Detail: "merging into origin/main changes nothing"}},
		{RepoRoot: "/work/alpha", ProjectID: 1, ProjectSlug: "-work-alpha", Path: "/wt/phase-9", Branch: "swarm/phase-9",
			Proof: wtcleanup.Proof{Kind: wtcleanup.ProofNone, Detail: "conflicts with origin/main"}},
		// beta has only a proof-less worktree: counted, never an item.
		{RepoRoot: "/work/beta", ProjectID: 2, ProjectSlug: "-work-beta", Path: "/wt/beta-1", Branch: "swarm/phase-77",
			Proof: wtcleanup.Proof{Kind: wtcleanup.ProofNone}},
	}}
	if _, err := wtcleanup.Propose(db, scan, time.Now().Add(-2*time.Hour)); err != nil {
		t.Fatalf("Propose: %v", err)
	}

	rec := &recordingCleanupForcer{}
	h := &Handler{DB: db, wtCleanupForce: rec, wtCleanupGit: stubCleanupGit{branches: map[string]string{
		"/wt/phase-4": "swarm/phase-4", "/wt/T-abc": "swarm/T-abc", "/wt/phase-9": "swarm/phase-9",
	}}}
	mux := http.NewServeMux()
	Routes(mux, h)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv, db, rec
}

func cleanupItems(items []needsYouItem) []needsYouItem {
	var out []needsYouItem
	for _, it := range items {
		if it.Kind == needsYouWorktreeCleanup {
			out = append(out, it)
		}
	}
	return out
}

// Propose's rows surface as ONE needs-you item per repository with a proof
// candidate: the proof rows listed, the proof-less ones a count.
func TestNeedsYouWorktreeCleanupItem(t *testing.T) {
	srv, _, _ := cleanupServer(t)

	got := cleanupItems(getNeedsYou(t, srv.URL+"/api/needs-you").Items)
	if len(got) != 1 {
		t.Fatalf("worktree_cleanup items = %d (%+v), want 1 (beta has no proof)", len(got), got)
	}
	it := got[0]
	if it.ProjectSlug != "-work-alpha" || it.SessionName != "alpha" || it.Cleanup == nil || it.BlockingSince == "" {
		t.Fatalf("item = %+v", it)
	}
	c := it.Cleanup
	if c.RepoRoot != "/work/alpha" || c.WithoutProof != 1 || len(c.Candidates) != 2 || c.ID == 0 {
		t.Fatalf("cleanup = %+v, want 2 candidates + 1 without proof", c)
	}
	byPath := map[string]cleanupCandidateDTO{}
	for _, cand := range c.Candidates {
		byPath[cand.Path] = cand
	}
	ph := byPath["/wt/phase-4"]
	if ph.ProofKind != "landing" || ph.Owner != "Cleanup plan · Phase 4: Landing" || ph.PlanExternalID != "2026-10-01-cleanup" || ph.PhaseSeq != 4 {
		t.Errorf("phase candidate = %+v", ph)
	}
	card := byPath["/wt/T-abc"]
	if card.ProofKind != "content-diff" || card.Owner != "T-abc: Fix the thing" || card.PhaseID != nil || card.TaskID == nil {
		t.Errorf("card candidate = %+v", card)
	}
	if _, listed := byPath["/wt/phase-9"]; listed {
		t.Error("a proof-less worktree was listed for deletion")
	}

	scoped := cleanupItems(getNeedsYou(t, srv.URL+"/api/needs-you?project=-work-beta").Items)
	if len(scoped) != 0 {
		t.Errorf("beta scope items = %+v, want none", scoped)
	}
}

func postCleanupApply(t *testing.T, url string, id int64, paths []string) (int, wtcleanup.ApplyResult) {
	t.Helper()
	body, _ := json.Marshal(map[string]any{"paths": paths})
	resp, err := http.Post(url+"/api/worktree-cleanup/"+strconv.FormatInt(id, 10)+"/apply", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("POST apply: %v", err)
	}
	defer resp.Body.Close()
	var res wtcleanup.ApplyResult
	_ = json.NewDecoder(resp.Body).Decode(&res)
	return resp.StatusCode, res
}

// The click removes ONLY the selected path, the item shrinks, and the same
// click again reaches nothing.
func TestApplyWorktreeCleanup_SelectedOnlyAndIdempotent(t *testing.T) {
	srv, db, rec := cleanupServer(t)
	it := cleanupItems(getNeedsYou(t, srv.URL+"/api/needs-you").Items)[0]

	code, res := postCleanupApply(t, srv.URL, it.Cleanup.ID, []string{"/wt/T-abc", "/wt/phase-9"})
	if code != http.StatusOK {
		t.Fatalf("status = %d", code)
	}
	if len(rec.handed) != 1 || rec.handed[0] != "/wt/T-abc" {
		t.Fatalf("janitor was handed %v, want only the selected path with a proof", rec.handed)
	}
	outcomes := map[string]string{}
	for _, p := range res.Paths {
		outcomes[p.Path] = p.Outcome
	}
	if outcomes["/wt/T-abc"] != wtjanitor.ForceRemoved || outcomes["/wt/phase-9"] != wtcleanup.OutcomeNoProof {
		t.Errorf("outcomes = %v", outcomes)
	}
	var decided string
	if err := db.QueryRow(`SELECT decided FROM worktree_cleanup_proposals WHERE path = '/wt/T-abc'`).Scan(&decided); err != nil || decided != "removed" {
		t.Errorf("decided = %q (%v), want removed", decided, err)
	}

	after := cleanupItems(getNeedsYou(t, srv.URL+"/api/needs-you").Items)
	if len(after) != 1 || len(after[0].Cleanup.Candidates) != 1 || after[0].Cleanup.Candidates[0].Path != "/wt/phase-4" {
		t.Errorf("item after apply = %+v, want only the unselected phase-4 left", after)
	}

	code, res = postCleanupApply(t, srv.URL, it.Cleanup.ID, []string{"/wt/T-abc"})
	if code != http.StatusOK || res.Paths[0].Outcome != wtcleanup.OutcomeAlready || len(rec.handed) != 1 {
		t.Errorf("second apply = %d %+v, janitor handed %v; want already-decided and no new call", code, res.Paths, rec.handed)
	}
}

func TestApplyWorktreeCleanup_BadRequests(t *testing.T) {
	srv, _, rec := cleanupServer(t)
	if code, _ := postCleanupApply(t, srv.URL, 99999, []string{"/wt/T-abc"}); code != http.StatusNotFound {
		t.Errorf("unknown id: status = %d, want 404", code)
	}
	if code, _ := postCleanupApply(t, srv.URL, 1, nil); code != http.StatusBadRequest {
		t.Errorf("no paths: status = %d, want 400", code)
	}
	resp, err := http.Post(srv.URL+"/api/worktree-cleanup/abc/apply", "application/json", bytes.NewReader([]byte(`{"paths":["/x"]}`)))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("bad id: status = %d, want 400", resp.StatusCode)
	}
	if len(rec.handed) != 0 {
		t.Errorf("janitor handed %v on bad requests", rec.handed)
	}
}
