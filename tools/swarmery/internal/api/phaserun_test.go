package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/dispatch"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/phasediag"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/phaserun"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/runcore"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/worktree"
)

// phaseStubRunner implements phaserun.Runner without spawning a process.
// block, when set, holds the run in flight (a Cancel unblocks it via ctx).
type phaseStubRunner struct {
	mu    sync.Mutex
	block chan struct{}
	specs []phaserun.RunSpec // every spec the service dispatched, in order
	// tickDB makes the default stub run a SUCCESSFUL one: it ticks every phase
	// doc's checkboxes before exiting 0. attachPhaseRunWt wires it.
	//
	// Exit 0 stopped meaning "finished" (runcore.ClassifyEnd): a run whose doc
	// still shows unticked criteria is resumed up to twice and then stamped
	// `partial`. These api tests assert the HTTP contract and the DTO, not the
	// completion semantics, so the stub is made to leave behind what a finished
	// run leaves behind rather than the new gate being weakened.
	tickDB *sql.DB
}

// tickEveryPhaseDoc rewrites every phase doc in the fixture with all of its
// checkboxes ticked — what a successful executor leaves behind.
func tickEveryPhaseDoc(db *sql.DB) {
	rows, err := db.Query(`SELECT doc_path FROM epic_phases WHERE doc_path IS NOT NULL AND doc_path <> ''`)
	if err != nil {
		return
	}
	var paths []string
	for rows.Next() {
		var p string
		if rows.Scan(&p) == nil {
			paths = append(paths, p)
		}
	}
	rows.Close()
	for _, p := range paths {
		body, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		var out []string
		for _, line := range strings.Split(string(body), "\n") {
			out = append(out, strings.Replace(line, "- [ ] ", "- [x] ", 1))
		}
		_ = os.WriteFile(p, []byte(strings.Join(out, "\n")), 0o644)
	}
}

func (r *phaseStubRunner) Start(ctx context.Context, spec phaserun.RunSpec) (*phaserun.Run, error) {
	r.mu.Lock()
	r.specs = append(r.specs, spec)
	block := r.block
	tickDB := r.tickDB
	r.mu.Unlock()
	if block != nil {
		select {
		case <-block:
		case <-ctx.Done():
			return &phaserun.Run{SessionUUID: spec.SessionUUID, ExitCode: -1}, nil
		}
	}
	if tickDB != nil && !spec.Resume {
		tickEveryPhaseDoc(tickDB)
	}
	return &phaserun.Run{SessionUUID: spec.SessionUUID, ExitCode: 0}, nil
}

// phaseStubWt is a no-op WorktreeManager.
type phaseStubWt struct{}

func (phaseStubWt) Acquire(repoRoot, projectSlug, taskID string) (worktree.Acquired, error) {
	return worktree.Acquired{Path: "/wt/" + projectSlug + "/" + taskID, Branch: "swarm/" + taskID}, nil
}
func (phaseStubWt) Remove(repoRoot string, a worktree.Acquired, keepBranch bool) error { return nil }

// No leftover branch in the api tests: reclaim always reports "nothing to do",
// so Start proceeds straight to Acquire.
func (phaseStubWt) Path(projectSlug, taskID string) (string, error) {
	return "/wt/" + projectSlug + "/" + taskID, nil
}
func (phaseStubWt) ReclaimEmptyBranch(repoRoot, branch string) (int, error) { return 0, nil }
func (phaseStubWt) DeleteBranch(repoRoot, branch string) (bool, error)      { return true, nil }

// CommitsForTask satisfies dispatch.WorktreeManager; phase-run tests do not exercise
// the dispatcher's progress high-water, so an empty history is the honest stub.
func (phaseStubWt) CommitsForTask(repoRoot, taskID string) ([]string, error) { return nil, nil }

// attachPhaseRun wires a stub-backed phaserun service (package var, reset on
// cleanup). sync=true runs the spawn inline so a POST response implies the run
// finished (deterministic end-state assertions).
func attachPhaseRun(t *testing.T, db *sql.DB, r phaserun.Runner, sync bool) *phaserun.Service {
	t.Helper()
	return attachPhaseRunWt(t, db, r, sync, phaseStubWt{})
}

// attachPhaseRunWt is attachPhaseRun with an explicit worktree manager, for the
// branch-lifecycle paths (dirty reclaim, DeleteRunBranch).
func attachPhaseRunWt(t *testing.T, db *sql.DB, r phaserun.Runner, sync bool, wt dispatch.WorktreeManager) *phaserun.Service {
	t.Helper()
	// The default stub run finishes its work — see phaseStubRunner.tickDB.
	if sr, ok := r.(*phaseStubRunner); ok && sr.tickDB == nil {
		sr.tickDB = db
	}
	svc := phaserun.NewService(db, r, wt)
	svc.UUID = func() string { return "phase-uuid-1" }
	// Identity resolver: the api fixtures use project paths that are not checkouts,
	// and what they assert is the HTTP contract, not repo resolution (which has its
	// own tests in internal/repopath and the run services).
	svc.RepoRoot = func(p string, _ ...string) (string, error) { return p, nil }
	if sync {
		svc.Go = func(fn func()) { fn() }
	}
	AttachPhaseRun(svc)
	t.Cleanup(func() { AttachPhaseRun(nil) })
	return svc
}

// detachPhaseRun clears the phaserun package var for the 503 tests and RESTORES
// whatever was there on cleanup. A bare AttachPhaseRun(nil) is order-dependent:
// it is safe only because every other test attaches what it needs before acting,
// and it would break outright under t.Parallel().
func detachPhaseRun(t *testing.T) {
	t.Helper()
	prev := phaserunSvc
	phaserunSvc = nil
	t.Cleanup(func() { phaserunSvc = prev })
}

// detachPlanRun is detachPhaseRun for the plan-run package var.
func detachPlanRun(t *testing.T) {
	t.Helper()
	prev := planrunSvc
	planrunSvc = nil
	t.Cleanup(func() { planrunSvc = prev })
}

// attachPhaseDiag wires the diagnosis endpoint's git seam and resets it on
// cleanup — the same idiom attachPhaseRunWt uses for phaserunSvc. AttachPhaseDiag
// is a bare package-var setter, so the first test to wire a git stub without this
// would leak it into every later test in the package.
func attachPhaseDiag(t *testing.T, g worktree.Git) {
	t.Helper()
	attachPhaseDiagOwn(t, g, nil)
}

// attachPhaseDiagOwn is attachPhaseDiag plus the ownership seam, for the tests
// that exercise the own-worktree / branch-dirty split.
func attachPhaseDiagOwn(t *testing.T, g worktree.Git, own phasediag.OwnCheckout) {
	t.Helper()
	prevGit, prevOwn := phasediagGit, phasediagOwn
	AttachPhaseDiag(g, own)
	t.Cleanup(func() { AttachPhaseDiag(prevGit, prevOwn) })
}

// fixturePhaseIDs reads the two phase ids the epicFixture inserted (seq order).
func fixturePhaseIDs(t *testing.T, db *sql.DB, taskID int64) (int64, int64) {
	t.Helper()
	rows, err := db.Query(`SELECT id FROM epic_phases WHERE workspace_task_id=? ORDER BY seq`, taskID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
	}
	if len(ids) != 2 {
		t.Fatalf("fixture phases = %d, want 2", len(ids))
	}
	return ids[0], ids[1]
}

// stampRunBranch records the branch a run used (migration 0043), which is what
// DeleteRunBranch reads. Fixture phases have never run, so anything exercising the
// branch endpoint has to say which branch it is talking about — the service refuses to
// re-derive one from the row id.
func stampRunBranch(t *testing.T, db *sql.DB, phaseID int64, branch string) {
	t.Helper()
	if _, err := db.Exec(
		`UPDATE epic_phases SET run_state='done', run_branch=? WHERE id=?`, branch, phaseID); err != nil {
		t.Fatal(err)
	}
}

func postPhase(t *testing.T, url string) *http.Response {
	t.Helper()
	resp, err := http.Post(url, "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { resp.Body.Close() })
	return resp
}

// postPhaseBody is postPhase with a request body — the optional {model} payload.
func postPhaseBody(t *testing.T, url, body string) *http.Response {
	t.Helper()
	resp, err := http.Post(url, "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { resp.Body.Close() })
	return resp
}

// dispatchedSpecs is the runner-side seam: what the service actually handed the
// spawn, which is where a rejected run must leave nothing.
func (r *phaseStubRunner) dispatchedSpecs() []phaserun.RunSpec {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]phaserun.RunSpec(nil), r.specs...)
}

func phaseRunURL(srv *httptest.Server, taskID, phaseID int64) string {
	return srv.URL + "/api/epics/" + i64(taskID) + "/phases/" + i64(phaseID) + "/run"
}

func i64(n int64) string { return strconv.FormatInt(n, 10) }

func TestPhaseRun_NotAttached_503(t *testing.T) {
	srv, db, taskID, _ := epicFixture(t)
	p1, _ := fixturePhaseIDs(t, db, taskID)
	detachPhaseRun(t)
	resp := postPhase(t, phaseRunURL(srv, taskID, p1))
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", resp.StatusCode)
	}
}

func TestPhaseRun_UnknownPhase_404(t *testing.T) {
	srv, db, taskID, _ := epicFixture(t)
	attachPhaseRun(t, db, &phaseStubRunner{}, true)
	resp := postPhase(t, phaseRunURL(srv, taskID, 99999))
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", resp.StatusCode)
	}
}

func TestPhaseRun_TaskMismatch_404(t *testing.T) {
	srv, db, taskID, _ := epicFixture(t)
	p1, _ := fixturePhaseIDs(t, db, taskID)
	attachPhaseRun(t, db, &phaseStubRunner{}, true)
	resp := postPhase(t, phaseRunURL(srv, taskID+999, p1))
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", resp.StatusCode)
	}
}

func TestPhaseRun_Accepted_AndDTOCarriesRunFields(t *testing.T) {
	srv, db, taskID, _ := epicFixture(t)
	p1, _ := fixturePhaseIDs(t, db, taskID)
	attachPhaseRun(t, db, &phaseStubRunner{}, true)

	resp := postPhase(t, phaseRunURL(srv, taskID, p1))
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("status = %d, want 202", resp.StatusCode)
	}
	var body struct {
		Status      string `json:"status"`
		SessionUUID string `json:"sessionUuid"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if body.Status != "running" || body.SessionUUID != "phase-uuid-1" {
		t.Errorf("body = %+v", body)
	}

	// The epic list DTO carries the run fields (sync spawn ⇒ the run is done).
	listResp, err := http.Get(srv.URL + "/api/epics?projectId=1")
	if err != nil {
		t.Fatal(err)
	}
	defer listResp.Body.Close()
	var epics []epicDTO
	if err := json.NewDecoder(listResp.Body).Decode(&epics); err != nil {
		t.Fatal(err)
	}
	if len(epics) != 1 || len(epics[0].Phases) != 2 {
		t.Fatalf("epics = %+v", epics)
	}
	ph := epics[0].Phases[0]
	if ph.RunState != "done" {
		t.Errorf("runState = %q, want done", ph.RunState)
	}
	if ph.RunSessionUUID == nil || *ph.RunSessionUUID != "phase-uuid-1" {
		t.Errorf("runSessionUuid = %v", ph.RunSessionUUID)
	}
	if ph.RunStartedAt == nil || *ph.RunStartedAt == "" {
		t.Error("runStartedAt missing")
	}
	if ph.RunError != nil {
		t.Errorf("runError = %v, want nil", *ph.RunError)
	}
	// An untouched phase reads idle.
	if epics[0].Phases[1].RunState != "idle" {
		t.Errorf("phase 2 runState = %q, want idle", epics[0].Phases[1].RunState)
	}
}

func TestPhaseRun_DepsUnmet_409(t *testing.T) {
	srv, db, taskID, _ := epicFixture(t)
	_, p2 := fixturePhaseIDs(t, db, taskID)
	attachPhaseRun(t, db, &phaseStubRunner{}, true)

	// epicFixture's phase 1 is 1/2 checkboxes and idle — phase 2's dep is unmet.
	resp := postPhase(t, phaseRunURL(srv, taskID, p2))
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("status = %d, want 409", resp.StatusCode)
	}
	var body struct {
		Error     string `json:"error"`
		UnmetDeps []int  `json:"unmetDeps"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if len(body.UnmetDeps) != 1 || body.UnmetDeps[0] != 1 {
		t.Errorf("unmetDeps = %v, want [1]", body.UnmetDeps)
	}
	if !strings.Contains(body.Error, "unmet") {
		t.Errorf("error = %q, want it to name unmet deps", body.Error)
	}
}

func TestPhaseRun_AlreadyRunning_409(t *testing.T) {
	srv, db, taskID, _ := epicFixture(t)
	p1, _ := fixturePhaseIDs(t, db, taskID)
	attachPhaseRun(t, db, &phaseStubRunner{}, true)
	if _, err := db.Exec(`UPDATE epic_phases SET run_state='running' WHERE id=?`, p1); err != nil {
		t.Fatal(err)
	}
	resp := postPhase(t, phaseRunURL(srv, taskID, p1))
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("status = %d, want 409", resp.StatusCode)
	}
}

func TestPhaseRun_NoDoc_409(t *testing.T) {
	srv, db, taskID, _ := epicFixture(t)
	p1, _ := fixturePhaseIDs(t, db, taskID)
	attachPhaseRun(t, db, &phaseStubRunner{}, true)
	if _, err := db.Exec(`UPDATE epic_phases SET doc_path='/nope/gone.md' WHERE id=?`, p1); err != nil {
		t.Fatal(err)
	}
	resp := postPhase(t, phaseRunURL(srv, taskID, p1))
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("status = %d, want 409", resp.StatusCode)
	}
}

func TestPhaseRun_NoProjectPath_409(t *testing.T) {
	srv, db, taskID, _ := epicFixture(t)
	p1, _ := fixturePhaseIDs(t, db, taskID)
	attachPhaseRun(t, db, &phaseStubRunner{}, true)
	if _, err := db.Exec(`UPDATE projects SET path='' WHERE id=1`); err != nil {
		t.Fatal(err)
	}
	resp := postPhase(t, phaseRunURL(srv, taskID, p1))
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("status = %d, want 409", resp.StatusCode)
	}
}

func TestPhaseRunCancel_NothingRunning_409(t *testing.T) {
	srv, db, taskID, _ := epicFixture(t)
	p1, _ := fixturePhaseIDs(t, db, taskID)
	attachPhaseRun(t, db, &phaseStubRunner{}, true)
	resp := postPhase(t, phaseRunURL(srv, taskID, p1)+"/cancel")
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("status = %d, want 409", resp.StatusCode)
	}
}

func TestPhaseRunCancel_NotAttached_503(t *testing.T) {
	srv, db, taskID, _ := epicFixture(t)
	p1, _ := fixturePhaseIDs(t, db, taskID)
	detachPhaseRun(t)
	resp := postPhase(t, phaseRunURL(srv, taskID, p1)+"/cancel")
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", resp.StatusCode)
	}
}

func TestPhaseRunCancel_InFlight_202(t *testing.T) {
	srv, db, taskID, _ := epicFixture(t)
	p1, _ := fixturePhaseIDs(t, db, taskID)
	r := &phaseStubRunner{block: make(chan struct{})}
	attachPhaseRun(t, db, r, false) // real goroutine — run stays in flight

	if resp := postPhase(t, phaseRunURL(srv, taskID, p1)); resp.StatusCode != http.StatusAccepted {
		t.Fatalf("run status = %d, want 202", resp.StatusCode)
	}
	// Wait for the running stamp, then cancel.
	waitState := func(want string) {
		t.Helper()
		deadline := time.Now().Add(2 * time.Second)
		for time.Now().Before(deadline) {
			var st string
			if err := db.QueryRow(`SELECT run_state FROM epic_phases WHERE id=?`, p1).Scan(&st); err == nil && st == want {
				return
			}
			time.Sleep(5 * time.Millisecond)
		}
		t.Fatalf("run_state never reached %q", want)
	}
	waitState("running")
	resp := postPhase(t, phaseRunURL(srv, taskID, p1)+"/cancel")
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("cancel status = %d, want 202", resp.StatusCode)
	}
	waitState("failed")
	var runErr sql.NullString
	if err := db.QueryRow(`SELECT run_error FROM epic_phases WHERE id=?`, p1).Scan(&runErr); err != nil {
		t.Fatal(err)
	}
	if runErr.String != "cancelled" {
		t.Errorf("run_error = %q, want cancelled", runErr.String)
	}
}

// The daemon-wide run budget is full: 409 with the transient code and the holders
// named. The distinction the body has to carry is "nothing is wrong with this
// phase, the machine is busy" — a plain already-running would send the user
// looking for a run of THIS phase that does not exist.
func TestPhaseRun_NoFreeRunSlot_409(t *testing.T) {
	srv, db, taskID, _ := epicFixture(t)
	p1, _ := fixturePhaseIDs(t, db, taskID)
	svc := attachPhaseRun(t, db, &phaseStubRunner{}, true)
	svc.Slots = runcore.NewSlots(1)
	if _, err := svc.Slots.TryAcquire(runcore.SlotKey("planrun", 77), "u-plan", nil); err != nil {
		t.Fatal(err)
	}

	resp := postPhase(t, phaseRunURL(srv, taskID, p1))
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("status = %d, want 409", resp.StatusCode)
	}
	var body struct {
		Error   string `json:"error"`
		Code    string `json:"code"`
		Max     int    `json:"max"`
		Holders []struct {
			Engine string `json:"engine"`
			ID     int64  `json:"id"`
			Since  string `json:"since"`
		} `json:"holders"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if body.Code != codeNoRunSlot {
		t.Errorf("code = %q, want %q", body.Code, codeNoRunSlot)
	}
	if body.Max != 1 {
		t.Errorf("max = %d, want 1", body.Max)
	}
	if len(body.Holders) != 1 || body.Holders[0].Engine != "planrun" || body.Holders[0].ID != 77 {
		t.Errorf("holders = %+v, want the plan run holding the pool", body.Holders)
	}
	if body.Holders[0].Since == "" {
		t.Error("holder carries no start time — the operator cannot tell a wedged run from a fresh one")
	}
	// Nothing was stamped: the phase is still idle and retriable.
	var state string
	if err := db.QueryRow(`SELECT run_state FROM epic_phases WHERE id=?`, p1).Scan(&state); err != nil {
		t.Fatal(err)
	}
	if state != "idle" {
		t.Errorf("run_state = %q, want idle — a busy pool is not a failed phase", state)
	}
}

// The phase surface's half of the bidirectional exclusion, over HTTP: its own
// code, so the UI can say "cancel the plan run" instead of the plan surface's
// "cancel the phase run".
func TestPhaseRun_PlanRunActive_409(t *testing.T) {
	srv, db, taskID, _ := epicFixture(t)
	p1, _ := fixturePhaseIDs(t, db, taskID)
	attachPhaseRun(t, db, &phaseStubRunner{}, true)
	if _, err := db.Exec(`INSERT INTO plan_runs (workspace_task_id, run_state) VALUES (?, 'running')`, taskID); err != nil {
		t.Fatal(err)
	}

	resp := postPhase(t, phaseRunURL(srv, taskID, p1))
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("status = %d, want 409", resp.StatusCode)
	}
	var body struct {
		Error string `json:"error"`
		Code  string `json:"code"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if body.Code != codePlanRunning {
		t.Errorf("code = %q, want %q", body.Code, codePlanRunning)
	}
	if !strings.Contains(body.Error, "plan run") {
		t.Errorf("error = %q, want it to name the plan run", body.Error)
	}
}
