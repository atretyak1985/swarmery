package api

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
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
// apply-scope gate passes. Non-core → the pack manifest bumps, the marketplace
// one does not.
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
	// The sha re-check reads the CURRENT origin/main content via `git show
	// origin/main:<agent_path>`; seedProposal's base_sha256 is over "agent body".
	if name == "git" && hasArg(args, "show") {
		return "agent body", nil
	}
	// The numstat gate now runs as `git -c core.quotepath=false diff --cached
	// --numstat --no-renames HEAD`; match on the presence of a "diff" arg so the
	// config-flag prefix doesn't dodge the stub.
	if name == "git" && hasArg(args, "diff") {
		// Single in-scope path, inside one pack → the pack's plugin.json is
		// bumped (phase 5 generalized the bump from core-only to pack-aware);
		// not core, so the marketplace manifest is NOT mirrored.
		return "1\t0\t" + noopChangedPath + "\n", nil
	}
	return "", nil
}
func hasArg(args []string, want string) bool {
	for _, a := range args {
		if a == want {
			return true
		}
	}
	return false
}

func (e *noopExec) ReadFile(path string) ([]byte, error) {
	// The semver bump reads the owning pack's manifest; everything else the
	// pipeline reads is a definition file the frontmatter gate checks.
	if strings.HasSuffix(path, "plugin.json") || strings.HasSuffix(path, "marketplace.json") {
		return []byte("{\n  \"name\": \"uav-pack\",\n  \"version\": \"1.4.2\"\n}\n"), nil
	}
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

// --- pack-aware semver bump (phase 5) ------------------------------------
//
// noopExec discards WriteFile, so the pre-phase-5 tests could only prove the
// pipeline REACHED the bump, never WHICH manifests it wrote. The bump changed
// behaviour in two ways that nothing asserted: a non-core pack now bumps at all,
// and a pack manifest without a version field now hard-fails the apply.

const bumpPackSkillRel = "plugins/uav-pack/skills/mavlink-integration/SKILL.md"

const bumpPackSkillBody = "---\nname: mavlink-integration\ndescription: \"Talk MAVLink.\"\n---\n\n# Rules\n\n1. Parse carefully.\n"

const bumpPackManifestRel = "plugins/uav-pack/.claude-plugin/plugin.json"

const marketplaceRel = ".claude-plugin/marketplace.json"

// recordingExec is noopExec with a real write log: manifest reads come from a
// table keyed by repo-relative path, and every WriteFile is REMEMBERED so a test
// can assert exactly which manifests the post-gate semver bump touched.
type recordingExec struct {
	tmp         string
	changedPath string
	body        string
	manifests   map[string]string
	writes      map[string]string
	reads       []string
}

func (e *recordingExec) rel(path string) string {
	return strings.TrimPrefix(strings.TrimPrefix(path, e.tmp), "/")
}

func (e *recordingExec) Run(_ context.Context, _ string, name string, args ...string) (string, error) {
	switch {
	case name == "bash":
		return "✓ clean\n", nil
	case name == "gh":
		return "https://github.com/x/y/pull/1\n", nil
	case name == "git" && hasArg(args, "show"):
		return e.body, nil
	case name == "git" && hasArg(args, "diff"):
		return "1\t0\t" + e.changedPath + "\n", nil
	}
	return "", nil
}

func (e *recordingExec) ReadFile(path string) ([]byte, error) {
	rel := e.rel(path)
	e.reads = append(e.reads, rel)
	if raw, ok := e.manifests[rel]; ok {
		return []byte(raw), nil
	}
	if strings.HasSuffix(rel, ".json") {
		return nil, errors.New("no such manifest: " + rel)
	}
	return []byte(e.body), nil
}

func (e *recordingExec) WriteFile(path string, b []byte) error {
	if e.writes == nil {
		e.writes = map[string]string{}
	}
	e.writes[e.rel(path)] = string(b)
	return nil
}
func (e *recordingExec) MkdirTemp() (string, error) { return e.tmp, nil }
func (e *recordingExec) RemoveAll(string) error     { return nil }

func recordingServer(t *testing.T, ex *recordingExec) (string, *sql.DB) {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "bump.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	ex.tmp = filepath.Join(t.TempDir(), "wt")
	h := &Handler{
		DB: db,
		Improve: &improve.Service{DB: db, Runner: &improveMockRunner{out: improveValidOut},
			Repo: "/repo", Exec: ex},
		improveGo: func(fn func()) { fn() },
	}
	mux := http.NewServeMux()
	Routes(mux, h)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv.URL, db
}

// seedSkillProposal inserts one approved SKILL.md proposal whose base_sha256
// matches the content the exec's `git show origin/main:<path>` returns.
func seedSkillProposal(t *testing.T, db *sql.DB, id int64, rel, body string) {
	t.Helper()
	sum := sha256.Sum256([]byte(body))
	improveExec(t, db, `INSERT INTO agent_change_proposals
		(id, agent, agent_path, target_kind, target_path, base_sha256, diff, rationale, status, created_at)
		VALUES (?, 'sync the cache before building', '', 'skill', ?, ?, 'd', 'r', 'approved', '2026-09-20T00:00:00.000Z')`,
		id, rel, hex.EncodeToString(sum[:]))
}

func proposalError(t *testing.T, db *sql.DB, id int64) string {
	t.Helper()
	var e sql.NullString
	if err := db.QueryRow(`SELECT error FROM agent_change_proposals WHERE id = ?`, id).Scan(&e); err != nil {
		t.Fatal(err)
	}
	return e.String
}

// TestApplyBumpsOwningPackNotMarketplace: a SKILL.md inside a NON-core pack
// bumps that pack's own plugin.json and leaves the marketplace manifest alone —
// the marketplace version tracks core by convention, so mirroring a domain
// pack's patch bump into it would be wrong.
func TestApplyBumpsOwningPackNotMarketplace(t *testing.T) {
	ex := &recordingExec{
		changedPath: bumpPackSkillRel,
		body:        bumpPackSkillBody,
		manifests: map[string]string{
			bumpPackManifestRel: "{\n  \"name\": \"uav-pack\",\n  \"version\": \"1.4.2\"\n}\n",
			marketplaceRel:      "{\n  \"name\": \"swarmery\",\n  \"version\": \"3.5.0\"\n}\n",
		},
	}
	srv, db := recordingServer(t, ex)
	seedSkillProposal(t, db, 1, bumpPackSkillRel, bumpPackSkillBody)

	postJSON(t, srv+"/api/retro/proposals/1/apply", http.StatusAccepted)
	if s := proposalStatus(t, db, 1); s != "applied" {
		t.Fatalf("proposal = %q (error %q), want applied", s, proposalError(t, db, 1))
	}

	// (a) the owning pack's manifest was patch-bumped.
	got, ok := ex.writes[bumpPackManifestRel]
	if !ok {
		t.Fatalf("%s was never written; writes = %v", bumpPackManifestRel, ex.writes)
	}
	if !strings.Contains(got, `"version": "1.4.3"`) {
		t.Errorf("%s = %q, want version 1.4.3", bumpPackManifestRel, got)
	}

	// (b) the marketplace manifest was neither read nor written for a non-core pack.
	if _, hit := ex.writes[marketplaceRel]; hit {
		t.Errorf("marketplace.json was written for non-core pack uav-pack: %q", ex.writes[marketplaceRel])
	}
	for _, r := range ex.reads {
		if r == marketplaceRel {
			t.Errorf("marketplace.json was read for non-core pack uav-pack")
		}
	}
}

// TestApplyFailsWhenPackManifestHasNoVersion: the bump is a hard step, not a
// best-effort one — a pack whose plugin.json carries no version field fails the
// apply with that reason on the row instead of shipping an unversioned PR.
func TestApplyFailsWhenPackManifestHasNoVersion(t *testing.T) {
	ex := &recordingExec{
		changedPath: bumpPackSkillRel,
		body:        bumpPackSkillBody,
		manifests: map[string]string{
			bumpPackManifestRel: "{\n  \"name\": \"uav-pack\"\n}\n",
		},
	}
	srv, db := recordingServer(t, ex)
	seedSkillProposal(t, db, 1, bumpPackSkillRel, bumpPackSkillBody)

	postJSON(t, srv+"/api/retro/proposals/1/apply", http.StatusAccepted)
	if s := proposalStatus(t, db, 1); s != "failed" {
		t.Fatalf("proposal = %q, want failed", s)
	}
	if e := proposalError(t, db, 1); !strings.Contains(e, "no version field") {
		t.Errorf("error = %q, want it to name the missing version field", e)
	}
	if _, hit := ex.writes[bumpPackManifestRel]; hit {
		t.Errorf("a manifest with no version field must not be rewritten: %q", ex.writes[bumpPackManifestRel])
	}
}
