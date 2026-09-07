package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/dispatch"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/planrun"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/worktree"
)

// planrunStubRunner implements planrun.Runner without spawning a process. block,
// when set, holds the run in flight (a Cancel unblocks it via ctx).
type planrunStubRunner struct {
	mu    sync.Mutex
	block chan struct{}
	specs []planrun.RunSpec
}

func (r *planrunStubRunner) Start(ctx context.Context, spec planrun.RunSpec) (*planrun.Run, error) {
	r.mu.Lock()
	r.specs = append(r.specs, spec)
	block := r.block
	r.mu.Unlock()
	if block != nil {
		select {
		case <-block:
		case <-ctx.Done():
			return &planrun.Run{SessionUUID: spec.SessionUUID, ExitCode: -1}, nil
		}
	}
	return &planrun.Run{SessionUUID: spec.SessionUUID, ExitCode: 0}, nil
}

func (r *planrunStubRunner) lastSpec() planrun.RunSpec {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.specs) == 0 {
		return planrun.RunSpec{}
	}
	return r.specs[len(r.specs)-1]
}

// attachPlanRun wires a stub-backed planrun service (package var, reset on
// cleanup). sync=true runs the spawn inline so a POST response implies the run
// finished (deterministic end-state assertions).
func attachPlanRun(t *testing.T, db *sql.DB, r planrun.Runner, sync bool) *planrun.Service {
	t.Helper()
	return attachPlanRunWt(t, db, r, sync, phaseStubWt{})
}

// attachPlanRunWt is attachPlanRun with an explicit worktree manager, for the
// branch-lifecycle paths (dirty reclaim, DeleteRunBranch). It also wires the
// optional Git seam, because that is what names BranchDirtyError.Base and what
// lets DeleteRunBranch claim the branch actually existed.
func attachPlanRunWt(t *testing.T, db *sql.DB, r planrun.Runner, sync bool,
	wt dispatch.WorktreeManager) *planrun.Service {
	t.Helper()
	svc := planrun.NewService(db, r, wt)
	svc.UUID = func() string { return "plan-uuid-1" }
	// Identity resolver: see attachPhaseRunWt — these fixtures assert the HTTP
	// contract, not repo resolution.
	svc.RepoRoot = func(p string, _ ...string) (string, error) { return p, nil }
	svc.Git = planGitStub{}
	if sync {
		svc.Go = func(fn func()) { fn() }
	}
	AttachPlanRun(svc)
	t.Cleanup(func() { AttachPlanRun(nil) })
	return svc
}

// planGitStub answers every read the planrun service makes off its optional Git
// seam: `symbolic-ref --short HEAD` (→ the base a commits-ahead count was measured
// against) and `rev-parse --verify` (→ the branch is there). One canned answer is
// enough — the service only ever reads these two.
type planGitStub struct{}

func (planGitStub) Run(dir string, args ...string) (string, error) { return "dev\n", nil }

func planBranchURL(srv *httptest.Server, taskID int64) string {
	return srv.URL + "/api/epics/" + i64(taskID) + "/branch"
}

func deletePlanBranch(t *testing.T, url string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(http.MethodDelete, url, nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { resp.Body.Close() })
	return resp
}

func planRunURL(srv *httptest.Server, taskID int64) string {
	return srv.URL + "/api/epics/" + i64(taskID) + "/run"
}

// waitForAPI polls until cond() or 2s — for the async (real-goroutine) run.
func waitForAPI(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("condition not met within 2s")
}

// postJSON posts a body (nil ⇒ no body) and returns the response.
func postPlanRun(t *testing.T, url, body string) *http.Response {
	t.Helper()
	var r *http.Response
	var err error
	if body == "" {
		r, err = http.Post(url, "application/json", nil)
	} else {
		r, err = http.Post(url, "application/json", strings.NewReader(body))
	}
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { r.Body.Close() })
	return r
}

func TestRunPlan503WhenUnattached(t *testing.T) {
	srv, _, taskID, _ := epicFixture(t)
	detachPlanRun(t)
	resp := postPlanRun(t, planRunURL(srv, taskID), "")
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503 when the service is not attached", resp.StatusCode)
	}
}

func TestRunPlanHappyPath(t *testing.T) {
	srv, db, taskID, planDir := epicFixture(t)
	if err := os.WriteFile(filepath.Join(planDir, "README.md"),
		[]byte("# My Epic\n\nObjective: ship it.\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	r := &planrunStubRunner{}
	attachPlanRun(t, db, r, true)

	resp := postPlanRun(t, planRunURL(srv, taskID), `{"agent":"tech-lead","mode":"subagents"}`)
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("status = %d, want 202", resp.StatusCode)
	}
	var body map[string]string
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if body["sessionUuid"] != "plan-uuid-1" || body["agent"] != "tech-lead" || body["mode"] != "subagents" {
		t.Errorf("body = %v, want the uuid, agent and mode echoed back", body)
	}
	if got := r.lastSpec().Agent; got != "tech-lead" {
		t.Errorf("spec.Agent = %q, want tech-lead", got)
	}

	// The run is visible in the epic DTO the page reads.
	var state, mode string
	if err := db.QueryRow(`SELECT run_state, mode FROM plan_runs WHERE workspace_task_id=?`,
		taskID).Scan(&state, &mode); err != nil {
		t.Fatal(err)
	}
	if state != "done" || mode != "subagents" {
		t.Errorf("plan_runs = (%q, %q), want (done, subagents)", state, mode)
	}
}

func TestRunPlanBadTaskID(t *testing.T) {
	srv, db, _, _ := epicFixture(t)
	attachPlanRun(t, db, &planrunStubRunner{}, true)
	resp := postPlanRun(t, srv.URL+"/api/epics/not-a-number/run", "")
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", resp.StatusCode)
	}
}

func TestRunPlanUnknownPlan404(t *testing.T) {
	srv, db, _, _ := epicFixture(t)
	attachPlanRun(t, db, &planrunStubRunner{}, true)
	resp := postPlanRun(t, planRunURL(srv, 9999), "")
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("status = %d, want 404", resp.StatusCode)
	}
}

func TestRunPlan409WhenPhaseRunActive(t *testing.T) {
	srv, db, taskID, planDir := epicFixture(t)
	os.WriteFile(filepath.Join(planDir, "README.md"), []byte("# Epic\n"), 0o644)
	if _, err := db.Exec(`UPDATE epic_phases SET run_state='running' WHERE workspace_task_id=?`, taskID); err != nil {
		t.Fatal(err)
	}
	attachPlanRun(t, db, &planrunStubRunner{}, true)

	resp := postPlanRun(t, planRunURL(srv, taskID), "")
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("status = %d, want 409 while a phase run holds the docs", resp.StatusCode)
	}
	var body map[string]string
	json.NewDecoder(resp.Body).Decode(&body)
	if !strings.Contains(body["error"], "phase run") {
		t.Errorf("error = %q, want it to name the conflicting phase run", body["error"])
	}
}

func TestCancelPlanRun(t *testing.T) {
	srv, db, taskID, planDir := epicFixture(t)
	os.WriteFile(filepath.Join(planDir, "README.md"), []byte("# Epic\n"), 0o644)
	r := &planrunStubRunner{block: make(chan struct{})}
	attachPlanRun(t, db, r, false) // real goroutine — the run must stay in flight

	if got := postPlanRun(t, planRunURL(srv, taskID), "").StatusCode; got != http.StatusAccepted {
		t.Fatalf("start status = %d, want 202", got)
	}
	waitForAPI(t, func() bool {
		var n int
		db.QueryRow(`SELECT COUNT(*) FROM plan_runs WHERE workspace_task_id=? AND run_state='running'`,
			taskID).Scan(&n)
		return n == 1
	})

	if got := postPlanRun(t, planRunURL(srv, taskID)+"/cancel", "").StatusCode; got != http.StatusAccepted {
		t.Errorf("cancel status = %d, want 202", got)
	}
	waitForAPI(t, func() bool {
		var state string
		db.QueryRow(`SELECT run_state FROM plan_runs WHERE workspace_task_id=?`, taskID).Scan(&state)
		return state == "failed"
	})

	// Cancelling an idle plan is a 409, not a silent success.
	if got := postPlanRun(t, planRunURL(srv, taskID)+"/cancel", "").StatusCode; got != http.StatusConflict {
		t.Errorf("idle cancel status = %d, want 409", got)
	}
}

// planFixtureWithReadme is epicFixture plus the README Start insists on reading,
// so a test can reach the branch-reclaim gate instead of dying on ErrNoDoc.
func planFixtureWithReadme(t *testing.T) (*httptest.Server, *sql.DB, int64) {
	t.Helper()
	srv, db, taskID, planDir := epicFixture(t)
	if err := os.WriteFile(filepath.Join(planDir, "README.md"), []byte("# Epic\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return srv, db, taskID
}

// The observed production failure: a plan whose run branch holds commits answered
// 500 with a raw Go string, because runPlan mapped eight sentinels and not this
// one. All four fields are asserted — `base` in particular, since "2 commits
// ahead" is undecidable without knowing ahead of what.
func TestRunPlan_BranchDirty_409(t *testing.T) {
	srv, db, taskID := planFixtureWithReadme(t)
	attachPlanRunWt(t, db, &planrunStubRunner{}, true, &phaseWtStub{reclaimAhead: 2})

	resp := postPlanRun(t, planRunURL(srv, taskID), "")
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("status = %d, want 409", resp.StatusCode)
	}
	var body struct {
		Error        string `json:"error"`
		Branch       string `json:"branch"`
		CommitsAhead int    `json:"commitsAhead"`
		Base         string `json:"base"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if want := "swarm/plan-" + i64(taskID); body.Branch != want {
		t.Errorf("branch = %q, want %q", body.Branch, want)
	}
	if body.CommitsAhead != 2 {
		t.Errorf("commitsAhead = %d, want 2", body.CommitsAhead)
	}
	if body.Base != "dev" {
		t.Errorf("base = %q, want the branch the count was measured against", body.Base)
	}
	if body.Error == "" {
		t.Error("error message missing — the flat {error} key every client reads")
	}
}

// The phase level gained `base` in the same change, so both run surfaces answer
// with one shape and the web client parses one type.
func TestRunPhase_BranchDirty_CarriesBase(t *testing.T) {
	srv, db, taskID, _ := epicFixture(t)
	p1, _ := fixturePhaseIDs(t, db, taskID)
	svc := attachPhaseRunWt(t, db, &phaseStubRunner{}, true, &phaseWtStub{reclaimAhead: 3})
	svc.Git = planGitStub{}

	resp := postPhase(t, phaseRunURL(srv, taskID, p1))
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("status = %d, want 409", resp.StatusCode)
	}
	var body struct {
		Branch       string `json:"branch"`
		CommitsAhead int    `json:"commitsAhead"`
		Base         string `json:"base"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if body.Base != "dev" {
		t.Errorf("base = %q, want dev", body.Base)
	}
	if body.CommitsAhead != 3 || body.Branch != "swarm/phase-"+i64(p1) {
		t.Errorf("body = %+v, want the pre-existing fields unchanged", body)
	}
}

// Start wraps the reclaim failure (fmt.Errorf("reclaim run branch: %w", …)), so
// the arm has to match through the wrap — a type-assertion or an == would put
// this back on the raw-500 path.
func TestRunPlan_BranchCheckedOut_409(t *testing.T) {
	srv, db, taskID := planFixtureWithReadme(t)
	attachPlanRunWt(t, db, &planrunStubRunner{}, true,
		&phaseWtStub{reclaimErr: worktree.ErrBranchCheckedOut})

	resp := postPlanRun(t, planRunURL(srv, taskID), "")
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("status = %d, want 409", resp.StatusCode)
	}
	var body struct {
		Error string `json:"error"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(body.Error, "checked out") {
		t.Errorf("error = %q, want it to say the branch is checked out", body.Error)
	}
}

// A detached HEAD gives reclaim no base to measure against, so it refuses. The
// user resolves it by checking out a branch — which a raw 500 would never say.
func TestRunPlan_DetachedHead_409(t *testing.T) {
	srv, db, taskID := planFixtureWithReadme(t)
	attachPlanRunWt(t, db, &planrunStubRunner{}, true,
		&phaseWtStub{reclaimErr: worktree.ErrDetachedHead})

	resp := postPlanRun(t, planRunURL(srv, taskID), "")
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("status = %d, want 409", resp.StatusCode)
	}
	var body struct {
		Error string `json:"error"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(body.Error, "detached HEAD") {
		t.Errorf("error = %q, want it to name the detached HEAD", body.Error)
	}
}

func TestDeletePlanRunBranch_200(t *testing.T) {
	srv, db, taskID := planFixtureWithReadme(t)
	wt := &phaseWtStub{}
	attachPlanRunWt(t, db, &planrunStubRunner{}, true, wt)

	resp := deletePlanBranch(t, planBranchURL(srv, taskID))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	var body struct {
		Deleted bool   `json:"deleted"`
		Branch  string `json:"branch"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	want := "swarm/plan-" + i64(taskID)
	if !body.Deleted || body.Branch != want {
		t.Errorf("body = %+v, want {true %s}", body, want)
	}
	if wt.deleted != want {
		t.Errorf("deleted branch = %q, want %q", wt.deleted, want)
	}
}

// The refusals DeleteRunBranch surfaces are states the user can fix, so they are
// 409s — not the 500 an unmatched error produces.
func TestDeletePlanRunBranch_Refusals_409(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want string
	}{
		{"checked out", worktree.ErrBranchCheckedOut, "checked out"},
		{"repo HEAD", worktree.ErrBranchIsHead, "checked-out branch"},
		{"outside swarm/", worktree.ErrRefusedBranch, "swarm/"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv, db, taskID := planFixtureWithReadme(t)
			attachPlanRunWt(t, db, &planrunStubRunner{}, true, &phaseWtStub{deleteErr: tc.err})

			resp := deletePlanBranch(t, planBranchURL(srv, taskID))
			if resp.StatusCode != http.StatusConflict {
				t.Fatalf("status = %d, want 409", resp.StatusCode)
			}
			var body struct {
				Error string `json:"error"`
			}
			if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(body.Error, tc.want) {
				t.Errorf("error = %q, want it to contain %q", body.Error, tc.want)
			}
		})
	}
}

func TestDeletePlanRunBranch_NotAttached_503(t *testing.T) {
	srv, _, taskID := planFixtureWithReadme(t)
	AttachPlanRun(nil)
	if got := deletePlanBranch(t, planBranchURL(srv, taskID)).StatusCode; got != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503 when the service is not attached", got)
	}
}

// The route is DELETE-only and origin-hardened. A GET must never reach the
// handler — the status it gets instead is not asserted, because the SPA fallback
// owns unmatched GETs and answers 200 with index.html once web/dist is built;
// what matters is that no branch was destroyed. A foreign Origin must be refused
// outright: this endpoint force-deletes commits, so a drive-by cross-origin call
// must not get through.
func TestDeletePlanRunBranch_MethodAndOrigin(t *testing.T) {
	srv, db, taskID := planFixtureWithReadme(t)
	wt := &phaseWtStub{}
	attachPlanRunWt(t, db, &planrunStubRunner{}, true, wt)

	resp, err := http.Get(planBranchURL(srv, taskID))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if wt.deleted != "" {
		t.Errorf("a GET deleted %q — the route must be DELETE-only", wt.deleted)
	}

	req, err := http.NewRequest(http.MethodDelete, planBranchURL(srv, taskID), nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Origin", "https://evil.example.com")
	xres, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	xres.Body.Close()
	if xres.StatusCode != http.StatusForbidden {
		t.Errorf("cross-origin status = %d, want 403", xres.StatusCode)
	}
}

// The project has a path — it is simply not a checkout, and nothing the plan
// declares resolved to one. Before this code existed the same condition arrived
// as git's raw "fatal: not a git repository", which named nothing actionable.
func TestRunPlan_NoRepoRoot_409(t *testing.T) {
	srv, db, taskID := planFixtureWithReadme(t)
	umbrella := t.TempDir() // exists, has no .git, declares no repo
	if _, err := db.Exec(`UPDATE projects SET path=? WHERE id=1`, umbrella); err != nil {
		t.Fatal(err)
	}
	svc := attachPlanRunWt(t, db, &planrunStubRunner{}, true, &phaseWtStub{})
	svc.RepoRoot = nil // the real resolver is the subject here

	resp := postPlanRun(t, planRunURL(srv, taskID), "")
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("status = %d, want 409", resp.StatusCode)
	}
	var body struct{ Error, Code string }
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if body.Code != "no-repo-root" {
		t.Errorf("code = %q, want no-repo-root", body.Code)
	}
	if !strings.Contains(body.Error, umbrella) {
		t.Errorf("error = %q, want it to name the path that was checked", body.Error)
	}
}

// One worktree, several declared repos: the 409 lists them, so "run the phases
// individually" is an instruction rather than a guess.
func TestRunPlan_PlanSpansRepos_409(t *testing.T) {
	srv, db, taskID := planFixtureWithReadme(t)
	umbrella := t.TempDir()
	for _, name := range []string{"app", "infra"} {
		if err := os.MkdirAll(filepath.Join(umbrella, name, ".git"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Exec(`UPDATE projects SET path=? WHERE id=1`, umbrella); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("UPDATE epic_phases SET repo='`app`' WHERE workspace_task_id=? AND seq=1", taskID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("UPDATE epic_phases SET repo='`infra`' WHERE workspace_task_id=? AND seq=2", taskID); err != nil {
		t.Fatal(err)
	}
	svc := attachPlanRunWt(t, db, &planrunStubRunner{}, true, &phaseWtStub{})
	svc.RepoRoot = nil

	resp := postPlanRun(t, planRunURL(srv, taskID), "")
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("status = %d, want 409", resp.StatusCode)
	}
	var body struct {
		Error string   `json:"error"`
		Code  string   `json:"code"`
		Repos []string `json:"repos"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if body.Code != "plan-spans-repos" {
		t.Errorf("code = %q, want plan-spans-repos", body.Code)
	}
	if len(body.Repos) != 2 || body.Repos[0] != "app" || body.Repos[1] != "infra" {
		t.Errorf("repos = %v, want [app infra]", body.Repos)
	}
}

// The spec-coverage gate's HTTP face: spec.md declares criteria the fixture's
// phase docs (no **Covers:** lines) leave uncovered ⇒ 409 with the frozen
// `spec_uncovered` code, the ids in the message, and the structured list.
func TestRunPlan_SpecUncovered_409(t *testing.T) {
	srv, db, taskID := planFixtureWithReadme(t)
	var planDir string
	if err := db.QueryRow(`SELECT path FROM task_artifacts WHERE task_id=? AND kind='plan'`,
		taskID).Scan(&planDir); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(planDir, "spec.md"),
		[]byte("# Spec\n\n- [ ] **SC-1** — first\n- [ ] **SC-2** — second\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	attachPlanRun(t, db, &planrunStubRunner{}, true)

	resp := postPlanRun(t, planRunURL(srv, taskID), "")
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("status = %d, want 409", resp.StatusCode)
	}
	var body struct {
		Error     string   `json:"error"`
		Code      string   `json:"code"`
		Uncovered []string `json:"uncovered"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if body.Code != "spec_uncovered" {
		t.Errorf("code = %q, want spec_uncovered", body.Code)
	}
	if len(body.Uncovered) != 2 || body.Uncovered[0] != "SC-1" || body.Uncovered[1] != "SC-2" {
		t.Errorf("uncovered = %v, want [SC-1 SC-2]", body.Uncovered)
	}
	if !strings.Contains(body.Error, "SC-1, SC-2") || !strings.Contains(body.Error, "**Covers:**") {
		t.Errorf("error = %q, want the ids and the Covers remedy named", body.Error)
	}
	// A refusal leaves no run state behind.
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM plan_runs`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Errorf("plan_runs rows = %d, want 0 after a spec refusal", n)
	}
}

// The phase surface answers the same condition with the same code — the two run
// surfaces must not disagree about a state the user has to resolve once.
func TestRunPhase_NoRepoRoot_409(t *testing.T) {
	srv, db, taskID, _ := epicFixture(t)
	p1, _ := fixturePhaseIDs(t, db, taskID)
	umbrella := t.TempDir()
	if _, err := db.Exec(`UPDATE projects SET path=? WHERE id=1`, umbrella); err != nil {
		t.Fatal(err)
	}
	svc := attachPhaseRunWt(t, db, &phaseStubRunner{}, true, &phaseWtStub{})
	svc.RepoRoot = nil

	resp := postPhase(t, phaseRunURL(srv, taskID, p1))
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("status = %d, want 409", resp.StatusCode)
	}
	var body struct{ Error, Code string }
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if body.Code != "no-repo-root" {
		t.Errorf("code = %q, want no-repo-root", body.Code)
	}
}

// A declared checkout OUTSIDE the project that is not a registered project is its
// own 409 code on both surfaces: the remedy (register it, or move the phase) is
// different from no-repo-root's, and before this code the declaration was dropped
// silently and the run executed in the wrong repository.
func TestRunPhase_RepoOutsideProject_409(t *testing.T) {
	srv, db, taskID, _ := epicFixture(t)
	p1, _ := fixturePhaseIDs(t, db, taskID)
	base := t.TempDir()
	proj := filepath.Join(base, "proj")
	outside := filepath.Join(base, "outside")
	for _, d := range []string{proj, outside} {
		if err := os.MkdirAll(filepath.Join(d, ".git"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Exec(`UPDATE projects SET path=? WHERE id=1`, proj); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("UPDATE epic_phases SET repo=? WHERE id=?", "`"+outside+"`", p1); err != nil {
		t.Fatal(err)
	}
	svc := attachPhaseRunWt(t, db, &phaseStubRunner{}, true, &phaseWtStub{})
	svc.RepoRoot = nil

	resp := postPhase(t, phaseRunURL(srv, taskID, p1))
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("status = %d, want 409", resp.StatusCode)
	}
	var body struct{ Error, Code string }
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if body.Code != "repo-outside-project" {
		t.Errorf("code = %q, want repo-outside-project", body.Code)
	}
	if !strings.Contains(body.Error, "outside") {
		t.Errorf("error = %q, want it to explain the refusal", body.Error)
	}
}
