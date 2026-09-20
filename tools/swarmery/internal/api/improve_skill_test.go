package api

// Phase-5 API surface for SKILL proposals: an accepted R11 (skill-kind)
// recommendation routes through POST /api/retro/recommendations/{id}/improve,
// GET /api/retro/proposals carries target_kind/target_path, and a needs_target
// row can be dismissed but never approved.

import (
	"context"
	"database/sql"
	"encoding/json"
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

const apiSkillRel = "plugins/core/skills/docker-build/SKILL.md"

const apiSkillBody = "---\nname: docker-build\ndescription: \"Build images.\"\n---\n\n# Rules\n\n1. Build clean.\n"

// repoSkillsExec fakes an origin/main that ships one skill and one agent.
type repoSkillsExec struct{}

func (e *repoSkillsExec) Run(_ context.Context, _ string, name string, args ...string) (string, error) {
	if name != "git" || len(args) == 0 {
		return "", nil
	}
	if hasArg(args, "ls-tree") {
		return apiSkillRel + "\nplugins/core/agents/tech-lead.md\n", nil
	}
	if hasArg(args, "show") {
		if strings.HasSuffix(args[len(args)-1], apiSkillRel) {
			return apiSkillBody, nil
		}
		return "agent body", nil
	}
	return "", nil
}

func (e *repoSkillsExec) ReadFile(string) ([]byte, error) { return nil, nil }
func (e *repoSkillsExec) WriteFile(string, []byte) error  { return nil }
func (e *repoSkillsExec) MkdirTemp() (string, error)      { return "", nil }
func (e *repoSkillsExec) RemoveAll(string) error          { return nil }

func skillServer(t *testing.T) (*httptest.Server, *sql.DB) {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "skill-api.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	h := &Handler{
		DB: db,
		Improve: &improve.Service{DB: db, Runner: &improveMockRunner{out: improveValidOut},
			Repo: "/repo", Exec: &repoSkillsExec{}},
		improveGo: func(fn func()) { fn() },
	}
	mux := http.NewServeMux()
	Routes(mux, h)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv, db
}

// seedAPILesson writes one task + retro + lesson so RouteSkill has an action to
// resolve (or fail to resolve) a target from.
func seedAPILesson(t *testing.T, db *sql.DB, id int, norm, action string) {
	t.Helper()
	improveExec(t, db, `INSERT OR IGNORE INTO projects (id, path, slug, first_seen)
		VALUES (1, '/p', 'p', ?)`, retroDay(t, 20))
	ext := "2026-09-0" + strconv.Itoa(id) + "-task"
	improveExec(t, db, `INSERT INTO tasks
		(id, project_id, title, prompt, status, created_at, started_at, source, external_id)
		VALUES (?, 1, 'x', 'goal', 'done', ?, ?, 'workspace', ?)`,
		id, retroDay(t, id), retroDay(t, id), ext)
	improveExec(t, db, `INSERT INTO task_retros (id, task_id, ingested_at) VALUES (?, ?, ?)`,
		id, id, retroDay(t, id))
	improveExec(t, db, `INSERT INTO retro_lessons (retro_id, seq, title, action, norm_title)
		VALUES (?, 1, 'Sync the cache', ?, ?)`, id, action, norm)
}

// setLatestAction puts R11's own `latest_action` into a recommendation's
// evidence JSON. Routing reads the action from there (improve.lessonAction) so
// the skill it edits is the one the card's "Latest action:" line named; the
// generic seedRecommendation fixture writes `{}`, which means "the card showed
// no action" and would legitimately route to needs_target.
func setLatestAction(t *testing.T, db *sql.DB, id int64, action string) {
	t.Helper()
	raw, err := json.Marshal(map[string]any{"latest_action": action})
	if err != nil {
		t.Fatalf("marshal evidence: %v", err)
	}
	improveExec(t, db, `UPDATE recommendations SET evidence = ? WHERE id = ?`, string(raw), id)
}

// listProposalDTOs GETs /api/retro/proposals and returns the decoded rows.
func listProposalDTOs(t *testing.T, srv *httptest.Server) []map[string]any {
	t.Helper()
	resp, err := http.Get(srv.URL + "/api/retro/proposals")
	if err != nil {
		t.Fatalf("GET proposals: %v", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET proposals = %d (%s)", resp.StatusCode, raw)
	}
	var out struct {
		Proposals []map[string]any `json:"proposals"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("decode proposals: %v\n%s", err, raw)
	}
	return out.Proposals
}

// TestImproveSkillRecommendationResolvesTarget: the accepted R11 row routes to
// a proposal against the SKILL.md, and the list endpoint exposes the target so
// the Retro page can show which file is about to change.
func TestImproveSkillRecommendationResolvesTarget(t *testing.T) {
	srv, db := skillServer(t)
	const norm = "sync the cache before building"
	seedAPILesson(t, db, 1, norm, "fold it into skills/docker-build")
	seedRecommendation(t, db, 7, "skill", norm, "accepted")
	setLatestAction(t, db, 7, "fold it into skills/docker-build")

	out := postJSON(t, srv.URL+"/api/retro/recommendations/7/improve", http.StatusAccepted)
	if out["target_kind"] != "skill" || out["lesson"] != norm {
		t.Errorf("202 body = %v", out)
	}

	rows := listProposalDTOs(t, srv)
	if len(rows) != 1 {
		t.Fatalf("proposals = %d, want 1", len(rows))
	}
	p := rows[0]
	if p["target_kind"] != "skill" {
		t.Errorf("target_kind = %v, want skill", p["target_kind"])
	}
	if p["target_path"] != apiSkillRel {
		t.Errorf("target_path = %v, want %s", p["target_path"], apiSkillRel)
	}
	if p["status"] != "proposed" {
		t.Errorf("status = %v (error %v), want proposed", p["status"], p["error"])
	}
	if p["recommendation_id"] != float64(7) {
		t.Errorf("recommendation_id = %v, want 7", p["recommendation_id"])
	}

	// 409 while that target stays open.
	postJSON(t, srv.URL+"/api/retro/recommendations/7/improve", http.StatusConflict)
}

// TestImproveSkillRecommendationNeedsTarget: an action naming no skill still
// lands a visible row — status needs_target, empty target_path — which the
// operator may dismiss but must not be able to approve (there is nothing to
// apply, and an approved row would fire the git pipeline at an empty path).
func TestImproveSkillRecommendationNeedsTarget(t *testing.T) {
	srv, db := skillServer(t)
	const norm = "write it down somewhere"
	seedAPILesson(t, db, 1, norm, "someone should remember this")
	seedRecommendation(t, db, 8, "skill", norm, "accepted")
	setLatestAction(t, db, 8, "someone should remember this")

	postJSON(t, srv.URL+"/api/retro/recommendations/8/improve", http.StatusAccepted)

	rows := listProposalDTOs(t, srv)
	if len(rows) != 1 {
		t.Fatalf("proposals = %d, want 1", len(rows))
	}
	p := rows[0]
	if p["status"] != improve.StatusNeedsTarget {
		t.Fatalf("status = %v, want %s", p["status"], improve.StatusNeedsTarget)
	}
	if p["target_path"] != "" {
		t.Errorf("target_path = %v, want empty", p["target_path"])
	}
	id := int64(p["id"].(float64))
	url := srv.URL + "/api/retro/proposals/" + strconv.FormatInt(id, 10)

	// Approving a needs_target row is refused.
	patchProposalReq(t, url, "approved", http.StatusUnprocessableEntity)
	// Dismissing it is allowed — otherwise it holds the target's open slot forever.
	patchProposalReq(t, url, "rejected", http.StatusOK)
	if s := proposalStatus(t, db, id); s != "rejected" {
		t.Errorf("status after dismiss = %q, want rejected", s)
	}

	// With the slot freed the same lesson can be routed again.
	postJSON(t, srv.URL+"/api/retro/recommendations/8/improve", http.StatusAccepted)
	if n := proposalCount(t, db, `status = ?`, improve.StatusNeedsTarget); n != 1 {
		t.Errorf("needs_target rows after re-route = %d, want 1", n)
	}
}

// TestRetrySkillProposalConflictsOnOpenTarget is the F2 regression. A skill
// row's `agent` column holds the LESSON SENTENCE, so the retry pre-flight's old
// agent-keyed lookup never matched one: the handler answered 202 "generating",
// improve.Retry then hit its own open-target check and returned ErrOpenProposal
// into a log line nobody reads, and the operator was left with a still-failed
// row and no reason. The conflict must reach the HTTP reply, exactly as it does
// for an agent row.
func TestRetrySkillProposalConflictsOnOpenTarget(t *testing.T) {
	srv, db := skillServer(t)
	// A FAILED skill proposal against the SKILL.md…
	improveExec(t, db, `INSERT INTO agent_change_proposals
		(id, agent, agent_path, target_kind, target_path, base_sha256, diff, rationale, status, error, created_at)
		VALUES (10, 'sync the cache before building', '', 'skill', ?, 's', '', '', 'failed', 'boom',
		        '2026-09-20T00:00:00.000Z')`, apiSkillRel)
	// …and another OPEN proposal, routed from a different lesson, already
	// holding that same file's single slot.
	improveExec(t, db, `INSERT INTO agent_change_proposals
		(id, agent, agent_path, target_kind, target_path, base_sha256, diff, rationale, status, created_at)
		VALUES (11, 'a different lesson entirely', '', 'skill', ?, 's', 'd', 'r', 'proposed',
		        '2026-09-20T01:00:00.000Z')`, apiSkillRel)

	out := postJSON(t, srv.URL+"/api/retro/proposals/10/retry", http.StatusConflict)
	if out["proposal_id"] != float64(11) {
		t.Errorf("409 body = %v, want it to name the blocking proposal 11", out)
	}
	if msg, _ := out["error"].(string); !strings.Contains(msg, apiSkillRel) {
		t.Errorf("409 error = %q, want it to name the blocked target file", msg)
	}
	if s := proposalStatus(t, db, 10); s != "failed" {
		t.Errorf("status = %q, want failed (the retry must not have started)", s)
	}
}

// TestRetrySkillProposalRunsWhenTargetIsFree is the control for the test above:
// the tightened pre-flight keys on the FILE, so a failed skill row whose target
// nobody else holds still retries. Without this, "always 409" would pass the
// regression test.
func TestRetrySkillProposalRunsWhenTargetIsFree(t *testing.T) {
	srv, db := skillServer(t)
	improveExec(t, db, `INSERT INTO agent_change_proposals
		(id, agent, agent_path, target_kind, target_path, base_sha256, diff, rationale, status, error, created_at)
		VALUES (10, 'sync the cache before building', '', 'skill', ?, 's', '', '', 'failed', 'boom',
		        '2026-09-20T00:00:00.000Z')`, apiSkillRel)
	// An open proposal for a DIFFERENT file must not block this one.
	improveExec(t, db, `INSERT INTO agent_change_proposals
		(id, agent, agent_path, target_kind, target_path, base_sha256, diff, rationale, status, created_at)
		VALUES (11, 'another lesson', '', 'skill', 'plugins/core/skills/other/SKILL.md', 's', 'd', 'r',
		        'proposed', '2026-09-20T01:00:00.000Z')`)

	postJSON(t, srv.URL+"/api/retro/proposals/10/retry", http.StatusAccepted)
	if s := proposalStatus(t, db, 10); s != "proposed" {
		t.Errorf("status = %q, want proposed after a successful retry", s)
	}
}

// TestListProposalsAcceptsNeedsTargetFilter: the status filter's vocabulary
// grew with the migration; a client asking for the new status must not get a
// 400 telling it the status does not exist.
func TestListProposalsAcceptsNeedsTargetFilter(t *testing.T) {
	srv, db := skillServer(t)
	improveExec(t, db, `INSERT INTO agent_change_proposals
		(agent, agent_path, target_kind, target_path, base_sha256, diff, rationale, status, created_at)
		VALUES ('a lesson', '', 'skill', '', '', '', '', 'needs_target', '2026-09-20T00:00:00.000Z')`)

	resp, err := http.Get(srv.URL + "/api/retro/proposals?status=needs_target")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET ?status=needs_target = %d (%s), want 200", resp.StatusCode, raw)
	}
	if !strings.Contains(string(raw), "needs_target") {
		t.Errorf("filtered list did not return the row: %s", raw)
	}
}
