package api

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/improve"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/store"
)

// seedProposal inserts one proposal row with the given status, plus a
// registry agent whose current content hashes to the proposal's base_sha256 —
// so the apply pipeline's sha re-read check passes and an approved proposal
// reaches the noopExec happy path (applied).
func seedProposal(t *testing.T, db *sql.DB, id int64, agent, status string) {
	t.Helper()
	const content = "agent body"
	sum := sha256.Sum256([]byte(content))
	base := hex.EncodeToString(sum[:])
	// agent_path is absolute under the pipeline's Repo (/repo) and, made
	// repo-relative, matches the single path noopExec's numstat reports — so the
	// apply-scope gate (path scope) sees only the target agent file.
	agentPath := "/repo/" + noopChangedPath
	improveExec(t, db, `INSERT INTO agents (id, name, scope, file_path, origin)
		VALUES (?, ?, 'global', ?, 'local')`, id, agent, agentPath)
	improveExec(t, db, `INSERT INTO agent_versions (id, agent_id, content_hash, content, created_at)
		VALUES (?, ?, ?, ?, '2026-07-20T00:00:00Z')`, id, id, "h"+agent, content)
	improveExec(t, db, `UPDATE agents SET current_version_id = ? WHERE id = ?`, id, id)
	improveExec(t, db, `INSERT INTO agent_change_proposals
		(id, agent, agent_path, base_sha256, diff, rationale, status, created_at)
		VALUES (?, ?, ?, ?, 'd', 'r', ?, '2026-07-20T00:00:00.000Z')`,
		id, agent, agentPath, base, status)
}

// patchProposalReq fires PATCH /api/retro/proposals/{id} with the status body.
func patchProposalReq(t *testing.T, url string, status string, wantCode int) {
	t.Helper()
	body := bytes.NewBufferString(`{"status":"` + status + `"}`)
	req, _ := http.NewRequest(http.MethodPatch, url, body)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("PATCH %s: %v", url, err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != wantCode {
		t.Fatalf("PATCH %s status=%s = %d (%s), want %d", url, status, resp.StatusCode, raw, wantCode)
	}
}

// noopChangedPath is the single repo-relative path noopExec's numstat reports;
// seedProposal stores the matching /repo/<noopChangedPath> as agent_path so the
// apply-scope gate passes. Non-core → no semver bump on the happy path.
const noopChangedPath = "plugins/uav-pack/agents/x.md"

// noopExec is an Exec whose apply pipeline never fails: git/gh succeed, scan is
// clean, numstat is tiny, gh returns a PR url. It lets an approve→apply chain
// run inline in the httptest without shelling out.
type noopExec struct{ tmp string }

func (e *noopExec) Run(_ context.Context, _ string, name string, args ...string) (string, error) {
	if name == "bash" {
		return "✓ clean\n", nil
	}
	if name == "gh" {
		return "https://github.com/x/y/pull/1\n", nil
	}
	if name == "git" && len(args) > 0 && args[0] == "diff" {
		return "1\t0\t" + noopChangedPath + "\n", nil // non-core → no semver bump
	}
	return "", nil
}
func (e *noopExec) ReadFile(string) ([]byte, error) {
	return []byte("---\nname: x\ndescription: y\n---\n"), nil
}
func (e *noopExec) WriteFile(string, []byte) error { return nil }
func (e *noopExec) MkdirTemp() (string, error)     { return e.tmp, nil }
func (e *noopExec) RemoveAll(string) error         { return nil }

func decisionServer(t *testing.T) (string, *sql.DB) {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "decide.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	h := &Handler{
		DB: db,
		Improve: &improve.Service{DB: db, Runner: &improveMockRunner{out: improveValidOut},
			Repo: "/repo", Exec: &noopExec{tmp: filepath.Join(t.TempDir(), "wt")}},
		improveGo: func(fn func()) { fn() },
	}
	mux := http.NewServeMux()
	Routes(mux, h)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv.URL, db
}

func proposalStatus(t *testing.T, db *sql.DB, id int64) string {
	t.Helper()
	var s string
	if err := db.QueryRow(`SELECT status FROM agent_change_proposals WHERE id = ?`, id).Scan(&s); err != nil {
		t.Fatal(err)
	}
	return s
}

func TestPatchProposalMatrix(t *testing.T) {
	srv, db := decisionServer(t)
	url := func(id int64) string {
		return srv + "/api/retro/proposals/" + strconv.FormatInt(id, 10)
	}

	// proposed → approved: 200, decided_at set, apply fired inline.
	seedProposal(t, db, 1, "a1", "proposed")
	patchProposalReq(t, url(1), "approved", http.StatusOK)
	// The inline apply took it to applied (noopExec happy path).
	if s := proposalStatus(t, db, 1); s != "applied" {
		t.Errorf("proposal 1 = %q, want applied after inline apply", s)
	}

	// proposed → rejected: 200.
	seedProposal(t, db, 2, "a2", "proposed")
	patchProposalReq(t, url(2), "rejected", http.StatusOK)
	if s := proposalStatus(t, db, 2); s != "rejected" {
		t.Errorf("proposal 2 = %q, want rejected", s)
	}

	// approved → rejected: 422 (only proposed is decidable).
	seedProposal(t, db, 3, "a3", "approved")
	patchProposalReq(t, url(3), "rejected", http.StatusUnprocessableEntity)

	// applied → anything: 422.
	seedProposal(t, db, 4, "a4", "applied")
	patchProposalReq(t, url(4), "approved", http.StatusUnprocessableEntity)
	patchProposalReq(t, url(4), "rejected", http.StatusUnprocessableEntity)

	// bad status value: 422.
	seedProposal(t, db, 5, "a5", "proposed")
	patchProposalReq(t, url(5), "applied", http.StatusUnprocessableEntity)

	// unknown id: 404.
	patchProposalReq(t, url(999), "approved", http.StatusNotFound)
}

// A panic inside the async pipeline must be recovered, not propagate out of
// spawnImprove and crash the daemon.
func TestSpawnImproveRecoversPanic(t *testing.T) {
	h := &Handler{improveGo: func(fn func()) { fn() }}
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("spawnImprove let a panic escape: %v", r)
		}
	}()
	h.spawnImprove("generate agent test", func() { panic("boom in generate") })
}

func TestApplyProposalManualRerun(t *testing.T) {
	srv, db := decisionServer(t)
	url := func(id int64) string {
		return srv + "/api/retro/proposals/" + strconv.FormatInt(id, 10) + "/apply"
	}

	// approved: 202, inline apply drives it to applied.
	seedProposal(t, db, 1, "a1", "approved")
	postJSON(t, url(1), http.StatusAccepted)
	if s := proposalStatus(t, db, 1); s != "applied" {
		t.Errorf("manual apply left proposal 1 = %q, want applied", s)
	}

	// proposed: 422 (not yet approved).
	seedProposal(t, db, 2, "a2", "proposed")
	postJSON(t, url(2), http.StatusUnprocessableEntity)

	// applied: 422 (already done).
	seedProposal(t, db, 3, "a3", "applied")
	postJSON(t, url(3), http.StatusUnprocessableEntity)

	// unknown id: 404.
	postJSON(t, url(999), http.StatusNotFound)
}
