package api

import (
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/phasediag"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/worktree"
)

// phaseWtStub is a configurable WorktreeManager for the branch-lifecycle paths:
// reclaimAhead>0 makes Start report a *BranchDirtyError, deleteErr is what
// DeleteRunBranch's DeleteBranch reports, and branchMissing drives the idempotent
// "already gone" answer (false, nil) the honest `deleted` field is derived from.
type phaseWtStub struct {
	reclaimAhead  int
	reclaimErr    error
	deleteErr     error
	deleted       string
	branchMissing bool
}

func (s *phaseWtStub) Acquire(repoRoot, projectSlug, taskID string) (worktree.Acquired, error) {
	return worktree.Acquired{Path: "/wt/" + projectSlug + "/" + taskID, Branch: "swarm/" + taskID}, nil
}

func (s *phaseWtStub) Path(projectSlug, taskID string) (string, error) {
	return "/wt/" + projectSlug + "/" + taskID, nil
}

func (s *phaseWtStub) Remove(repoRoot string, a worktree.Acquired, keepBranch bool) error { return nil }

func (s *phaseWtStub) ReclaimEmptyBranch(repoRoot, branch string) (int, error) {
	return s.reclaimAhead, s.reclaimErr
}

func (s *phaseWtStub) DeleteBranch(repoRoot, branch string) (bool, error) {
	if s.deleteErr != nil {
		return false, s.deleteErr
	}
	if s.branchMissing {
		return false, nil // already gone — deleting is idempotent
	}
	s.deleted = branch
	return true, nil
}

// CommitsForTask satisfies dispatch.WorktreeManager. This package's tests do not
// exercise the dispatcher's progress high-water, so an empty history is the honest
// stub: no commits observed, no error.
func (s *phaseWtStub) CommitsForTask(repoRoot, taskID string) ([]string, error) { return nil, nil }

func diagURL(srv *httptest.Server, taskID, phaseID int64) string {
	return srv.URL + "/api/epics/" + i64(taskID) + "/phases/" + i64(phaseID) + "/diagnosis"
}

func branchURL(srv *httptest.Server, taskID, phaseID int64) string {
	return srv.URL + "/api/epics/" + i64(taskID) + "/phases/" + i64(phaseID) + "/branch"
}

func deletePhase(t *testing.T, url string) *http.Response {
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

// seedNoopPhase makes phase p look like a run that exited 0 without ticking
// anything: done, 0/7 criteria, NULL baseline (pre-0041 row ⇒ UNMEASURED).
func seedNoopPhase(t *testing.T, db *sql.DB, phaseID int64) {
	t.Helper()
	if _, err := db.Exec(`
		UPDATE epic_phases
		   SET run_state='done', checkboxes_total=7, checkboxes_done=0,
		       run_checkboxes_before=NULL, run_checkboxes_after=NULL,
		       run_started_at='2026-07-28T10:00:00Z', run_ended_at='2026-07-28T10:04:00Z'
		 WHERE id=?`, phaseID); err != nil {
		t.Fatal(err)
	}
}

// firstPhaseDTO fetches the epics list and returns the phase with the given id.
func firstPhaseDTO(t *testing.T, srv *httptest.Server, phaseID int64) epicPhaseDTO {
	t.Helper()
	resp, err := http.Get(srv.URL + "/api/epics?projectId=1")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var epics []epicDTO
	if err := json.NewDecoder(resp.Body).Decode(&epics); err != nil {
		t.Fatal(err)
	}
	for _, e := range epics {
		for _, p := range e.Phases {
			if p.ID == phaseID {
				return p
			}
		}
	}
	t.Fatalf("phase %d not in epics response", phaseID)
	return epicPhaseDTO{}
}

func TestPhaseDiagnosis_NoopPhase_200(t *testing.T) {
	srv, db, taskID, _ := epicFixture(t)
	p1, _ := fixturePhaseIDs(t, db, taskID)
	seedNoopPhase(t, db, p1)

	resp, err := http.Get(diagURL(srv, taskID, p1))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("diagnosis JSON: %s", strings.TrimSpace(string(raw)))

	var d phasediag.Diagnosis
	if err := json.Unmarshal(raw, &d); err != nil {
		t.Fatal(err)
	}
	if d.PhaseID != p1 {
		t.Errorf("phaseId = %d, want %d", d.PhaseID, p1)
	}
	if d.RunOutcome != phasediag.OutcomeNoop {
		t.Errorf("runOutcome = %q, want noop", d.RunOutcome)
	}
	if d.CriteriaTotal != 7 || d.CriteriaAfter != 0 {
		t.Errorf("criteria = %d/%d, want 0/7", d.CriteriaAfter, d.CriteriaTotal)
	}
	if d.CriteriaBefore != nil {
		t.Errorf("criteriaBefore = %v, want nil (unmeasured)", *d.CriteriaBefore)
	}
	if d.Blockers == nil {
		t.Error("blockers = null, want a JSON array")
	}
	if !strings.Contains(string(raw), `"blockers":[`) {
		t.Errorf("blockers not serialised as an array: %s", raw)
	}
}

func TestPhaseDiagnosis_DepIncompleteBlocker(t *testing.T) {
	srv, db, taskID, _ := epicFixture(t)
	_, p2 := fixturePhaseIDs(t, db, taskID)

	resp, err := http.Get(diagURL(srv, taskID, p2))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	var d phasediag.Diagnosis
	if err := json.NewDecoder(resp.Body).Decode(&d); err != nil {
		t.Fatal(err)
	}
	// Fixture phase 1 is 1/2 ticked, so phase 2's dependency is unprovable.
	found := false
	for _, b := range d.Blockers {
		if b.Kind == phasediag.KindDepIncomplete {
			found = true
		}
	}
	if !found {
		t.Errorf("blockers = %+v, want a dep-incomplete blocker", d.Blockers)
	}
}

// diagGitStub scripts the four reads phasediag makes off its Git seam:
// `symbolic-ref --short HEAD` (the base), `show-ref --verify --quiet` (does the
// branch exist), `rev-list --count base..ref` (how far ahead) and `log --format=%s`
// (the subjects the branch-dirty detail lists). Any branch absent from `ahead` is
// reported as non-existent, which is how a repo with no leftover branches looks.
//
// Same shape as the scripted stub internal/phasediag's own tests use — the point
// of duplicating it here is that this one drives the real HTTP handler, so it
// covers the wiring (AttachPhaseDiag → phasediagGit → Diagnose) that a package
// test cannot reach.
type diagGitStub struct {
	base     string
	ahead    map[string]int
	subjects map[string][]string
	// branches is what `git branch --list 'swarm/phase-*'` reports. nil ⇒ the list
	// fails, which the orphan rule degrades to "no orphans" — the shape every test
	// that predates phase 8 relies on.
	branches []string
	calls    []string
}

func (g *diagGitStub) Run(dir string, args ...string) (string, error) {
	g.calls = append(g.calls, strings.Join(args, " "))
	branchOf := func(spec string) string {
		// "dev..refs/heads/swarm/phase-7" → "swarm/phase-7"
		_, ref, _ := strings.Cut(spec, "..")
		return strings.TrimPrefix(ref, "refs/heads/")
	}
	switch args[0] {
	case "symbolic-ref":
		if g.base == "" {
			return "", errors.New("detached HEAD")
		}
		return g.base + "\n", nil
	case "show-ref":
		br := strings.TrimPrefix(args[len(args)-1], "refs/heads/")
		if _, ok := g.ahead[br]; ok {
			return "", nil
		}
		return "", errors.New("no such ref")
	case "rev-list":
		return strconv.Itoa(g.ahead[branchOf(args[len(args)-1])]) + "\n", nil
	case "log":
		return strings.Join(g.subjects[branchOf(args[len(args)-1])], "\n") + "\n", nil
	case "branch":
		if g.branches == nil {
			return "", errors.New("no branch list scripted")
		}
		return "  " + strings.Join(g.branches, "\n  ") + "\n", nil
	}
	return "", nil
}

// The branch-derived half of the diagnosis, end-to-end through HTTP. Everything
// below the handler was covered by internal/phasediag's own tests, but the wiring
// — AttachPhaseDiag's package var reaching Diagnose, and Blocker's Branch /
// CommitsAhead surviving JSON — had no coverage at the API layer, which is exactly
// where the UI reads it from.
func TestPhaseDiagnosis_BranchDirtyBlocker_EndToEnd(t *testing.T) {
	srv, db, taskID, _ := epicFixture(t)
	p1, _ := fixturePhaseIDs(t, db, taskID)
	seedNoopPhase(t, db, p1)

	branch := "swarm/phase-" + i64(p1)
	g := &diagGitStub{
		base:     "dev",
		ahead:    map[string]int{branch: 3},
		subjects: map[string][]string{branch: {"feat: third", "feat: second", "feat: first"}},
	}
	attachPhaseDiag(t, g)

	resp, err := http.Get(diagURL(srv, taskID, p1))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	var d phasediag.Diagnosis
	if err := json.NewDecoder(resp.Body).Decode(&d); err != nil {
		t.Fatal(err)
	}

	var dirty *phasediag.Blocker
	for i, b := range d.Blockers {
		if b.Kind == phasediag.KindBranchDirty {
			dirty = &d.Blockers[i]
		}
	}
	if dirty == nil {
		t.Fatalf("blockers = %+v, want a branch-dirty blocker", d.Blockers)
	}
	// The two facts the delete confirmation names must arrive as DATA, not only
	// inside the prose — a UI that had to parse Summary would go on naming a
	// branch after the server's naming rule moved.
	if dirty.Branch != branch {
		t.Errorf("blocker.branch = %q, want %q", dirty.Branch, branch)
	}
	if dirty.CommitsAhead != 3 {
		t.Errorf("blocker.commitsAhead = %d, want 3", dirty.CommitsAhead)
	}
	if !strings.Contains(dirty.Detail, "feat: third") {
		t.Errorf("detail = %q, want the commit subjects", dirty.Detail)
	}
	// Every branch-derived blocker says which branch it measured against, so base
	// skew is recognisable instead of looking like a real problem.
	if !strings.Contains(dirty.Detail, "dev") {
		t.Errorf("detail = %q, want it to name the base it measured against", dirty.Detail)
	}
	if !g.called("symbolic-ref") {
		t.Errorf("the handler never resolved a base through the attached seam (calls: %v)", g.calls)
	}
}

// The same wiring with NO git seam attached: branch-derived blockers are omitted
// rather than guessed, and the endpoint still answers 200 with the criteria facts.
// This is the contract that keeps a diagnosis from failing because git was unhappy.
func TestPhaseDiagnosis_NoGitSeam_OmitsBranchBlockers(t *testing.T) {
	srv, db, taskID, _ := epicFixture(t)
	p1, _ := fixturePhaseIDs(t, db, taskID)
	seedNoopPhase(t, db, p1)
	attachPhaseDiag(t, nil)

	resp, err := http.Get(diagURL(srv, taskID, p1))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	var d phasediag.Diagnosis
	if err := json.NewDecoder(resp.Body).Decode(&d); err != nil {
		t.Fatal(err)
	}
	for _, b := range d.Blockers {
		if b.Kind == phasediag.KindBranchDirty || b.Kind == phasediag.KindBranchBlocksRetry ||
			b.Kind == phasediag.KindDepUnmerged {
			t.Errorf("blocker %q emitted with no git seam — it can only have been guessed", b.Kind)
		}
	}
	if d.RunOutcome != phasediag.OutcomeNoop {
		t.Errorf("runOutcome = %q, want noop — criteria facts survive a missing seam", d.RunOutcome)
	}
}

func (g *diagGitStub) called(substr string) bool {
	for _, c := range g.calls {
		if strings.Contains(c, substr) {
			return true
		}
	}
	return false
}

func TestPhaseDiagnosis_TaskMismatch_404(t *testing.T) {
	srv, db, taskID, _ := epicFixture(t)
	p1, _ := fixturePhaseIDs(t, db, taskID)

	resp, err := http.Get(diagURL(srv, taskID+999, p1))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", resp.StatusCode)
	}
}

func TestPhaseDiagnosis_UnknownPhase_404(t *testing.T) {
	srv, _, taskID, _ := epicFixture(t)
	resp, err := http.Get(diagURL(srv, taskID, 99999))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", resp.StatusCode)
	}
}

func TestEpicPhaseDTO_Outcome_Noop(t *testing.T) {
	srv, db, taskID, _ := epicFixture(t)
	p1, _ := fixturePhaseIDs(t, db, taskID)
	seedNoopPhase(t, db, p1)

	ph := firstPhaseDTO(t, srv, p1)
	if ph.RunOutcome != phasediag.OutcomeNoop {
		t.Errorf("runOutcome = %q, want noop", ph.RunOutcome)
	}
	if ph.RunEndedAt == nil || *ph.RunEndedAt != "2026-07-28T10:04:00Z" {
		t.Errorf("runEndedAt = %v", ph.RunEndedAt)
	}
	if ph.RunCheckboxesBefore != nil {
		t.Errorf("runCheckboxesBefore = %v, want nil", *ph.RunCheckboxesBefore)
	}
}

func TestEpicPhaseDTO_Outcome_Completed(t *testing.T) {
	srv, db, taskID, _ := epicFixture(t)
	p1, _ := fixturePhaseIDs(t, db, taskID)
	if _, err := db.Exec(`
		UPDATE epic_phases
		   SET run_state='done', checkboxes_total=7, checkboxes_done=7,
		       run_checkboxes_before=0, run_checkboxes_after=7
		 WHERE id=?`, p1); err != nil {
		t.Fatal(err)
	}
	ph := firstPhaseDTO(t, srv, p1)
	if ph.RunOutcome != phasediag.OutcomeCompleted {
		t.Errorf("runOutcome = %q, want completed", ph.RunOutcome)
	}
	if ph.RunCheckboxesBefore == nil || *ph.RunCheckboxesBefore != 0 {
		t.Errorf("runCheckboxesBefore = %v, want 0", ph.RunCheckboxesBefore)
	}
}

// The stamped right edge (run_checkboxes_after) wins over the live count, which
// keeps moving after the run ends. Live 8/8 would read "completed"; the run only
// ever ticked 3, so the DTO must report "partial" — proof it derives through
// phasediag.OutcomeFromRow and not the live columns.
func TestEpicPhaseDTO_Outcome_StampedAfterBeatsLive(t *testing.T) {
	srv, db, taskID, _ := epicFixture(t)
	p1, _ := fixturePhaseIDs(t, db, taskID)
	if _, err := db.Exec(`
		UPDATE epic_phases
		   SET run_state='done', checkboxes_total=8, checkboxes_done=8,
		       run_checkboxes_before=0, run_checkboxes_after=3
		 WHERE id=?`, p1); err != nil {
		t.Fatal(err)
	}
	ph := firstPhaseDTO(t, srv, p1)
	if ph.RunOutcome != phasediag.OutcomePartial {
		t.Errorf("runOutcome = %q, want partial (stamped 3, live 8/8)", ph.RunOutcome)
	}
	// The diagnosis modal must agree with the list chip.
	resp, err := http.Get(diagURL(srv, taskID, p1))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var d phasediag.Diagnosis
	if err := json.NewDecoder(resp.Body).Decode(&d); err != nil {
		t.Fatal(err)
	}
	if d.RunOutcome != ph.RunOutcome {
		t.Errorf("diagnosis runOutcome = %q, list chip = %q — they must never disagree",
			d.RunOutcome, ph.RunOutcome)
	}
}

// A NULL baseline serialises as JSON null, never 0 — a 0 would let the UI render a
// "0 → N" delta nobody measured.
func TestEpicPhaseDTO_RunCheckboxesBefore_SerialisesNull(t *testing.T) {
	srv, db, taskID, _ := epicFixture(t)
	p1, _ := fixturePhaseIDs(t, db, taskID)
	seedNoopPhase(t, db, p1)

	resp, err := http.Get(srv.URL + "/api/epics?projectId=1")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"runCheckboxesBefore":null`) {
		t.Errorf("want a null runCheckboxesBefore in %s", raw)
	}
	if strings.Contains(string(raw), `"runCheckboxesBefore":0`) {
		t.Errorf("NULL baseline fabricated as 0: %s", raw)
	}
}

func TestPhaseRun_BranchDirty_409(t *testing.T) {
	srv, db, taskID, _ := epicFixture(t)
	p1, _ := fixturePhaseIDs(t, db, taskID)
	attachPhaseRunWt(t, db, &phaseStubRunner{}, true, &phaseWtStub{reclaimAhead: 3})

	resp := postPhase(t, phaseRunURL(srv, taskID, p1))
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("status = %d, want 409", resp.StatusCode)
	}
	var body struct {
		Error        string `json:"error"`
		Branch       string `json:"branch"`
		CommitsAhead int    `json:"commitsAhead"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if body.Branch != "swarm/phase-"+i64(p1) {
		t.Errorf("branch = %q", body.Branch)
	}
	if body.CommitsAhead != 3 {
		t.Errorf("commitsAhead = %d, want 3", body.CommitsAhead)
	}
	if body.Error == "" {
		t.Error("error message missing")
	}
}

// A checked-out run branch is a 409 with an actionable message, not a raw 500:
// Start wraps the reclaim failure, so the api arm has to match through the wrap.
func TestPhaseRun_BranchCheckedOut_409(t *testing.T) {
	srv, db, taskID, _ := epicFixture(t)
	p1, _ := fixturePhaseIDs(t, db, taskID)
	attachPhaseRunWt(t, db, &phaseStubRunner{}, true,
		&phaseWtStub{reclaimErr: worktree.ErrBranchCheckedOut})

	resp := postPhase(t, phaseRunURL(srv, taskID, p1))
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
		t.Errorf("error = %q, want it to name the checked-out branch", body.Error)
	}
}

// A detached HEAD leaves reclaim with no base to measure the leftover run branch
// against, so it refuses rather than guess one a `branch -D` would run on. That
// refusal is the user's to resolve (check out a branch), so it must arrive as an
// actionable 409 and not as the raw 500 an unmatched error produces.
func TestPhaseRun_DetachedHead_409(t *testing.T) {
	srv, db, taskID, _ := epicFixture(t)
	p1, _ := fixturePhaseIDs(t, db, taskID)
	attachPhaseRunWt(t, db, &phaseStubRunner{}, true,
		&phaseWtStub{reclaimErr: worktree.ErrDetachedHead})

	resp := postPhase(t, phaseRunURL(srv, taskID, p1))
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

func TestDeletePhaseRunBranch_200(t *testing.T) {
	srv, db, taskID, _ := epicFixture(t)
	p1, _ := fixturePhaseIDs(t, db, taskID)
	wt := &phaseWtStub{}
	attachPhaseRunWt(t, db, &phaseStubRunner{}, true, wt)
	stampRunBranch(t, db, p1, "swarm/phase-"+i64(p1))

	resp := deletePhase(t, branchURL(srv, taskID, p1))
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
	want := "swarm/phase-" + i64(p1)
	if !body.Deleted || body.Branch != want {
		t.Errorf("body = %+v, want {true %s}", body, want)
	}
	if wt.deleted != want {
		t.Errorf("deleted branch = %q, want %q", wt.deleted, want)
	}
}

// The regression this whole change exists for: after a phase doc is renamed the row is
// replaced and its id moves, so "swarm/phase-<current id>" names a branch that does not
// exist while the one holding the run's commits survives. Deleting must follow the
// STAMPED branch, or it reports success and destroys nothing.
func TestDeletePhaseRunBranch_UsesStampedBranchNotRowID(t *testing.T) {
	srv, db, taskID, _ := epicFixture(t)
	p1, _ := fixturePhaseIDs(t, db, taskID)
	wt := &phaseWtStub{}
	attachPhaseRunWt(t, db, &phaseStubRunner{}, true, wt)
	const stamped = "swarm/phase-1279" // the id this phase ran under, before a rename
	stampRunBranch(t, db, p1, stamped)

	resp := deletePhase(t, branchURL(srv, taskID, p1))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if wt.deleted != stamped {
		t.Errorf("deleted branch = %q, want the stamped %q (not the row-id-derived name)", wt.deleted, stamped)
	}
	if derived := "swarm/phase-" + i64(p1); wt.deleted == derived {
		t.Errorf("deleted the derived name %q — the row id is not a branch name", derived)
	}
}

// A phase with no recorded run branch has no branch this service is willing to name.
// It must say so rather than guess one and delete whatever answers to it.
func TestDeletePhaseRunBranch_NoRecordedBranch_409(t *testing.T) {
	srv, db, taskID, _ := epicFixture(t)
	p1, _ := fixturePhaseIDs(t, db, taskID)
	wt := &phaseWtStub{}
	attachPhaseRunWt(t, db, &phaseStubRunner{}, true, wt)

	resp := deletePhase(t, branchURL(srv, taskID, p1))
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("status = %d, want 409", resp.StatusCode)
	}
	if wt.deleted != "" {
		t.Errorf("deleted = %q, want no delete attempted at all", wt.deleted)
	}
}

func TestDeletePhaseRunBranch_NotAttached_503(t *testing.T) {
	srv, db, taskID, _ := epicFixture(t)
	p1, _ := fixturePhaseIDs(t, db, taskID)
	detachPhaseRun(t)
	resp := deletePhase(t, branchURL(srv, taskID, p1))
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", resp.StatusCode)
	}
}

func TestDeletePhaseRunBranch_CheckedOut_409(t *testing.T) {
	srv, db, taskID, _ := epicFixture(t)
	p1, _ := fixturePhaseIDs(t, db, taskID)
	attachPhaseRunWt(t, db, &phaseStubRunner{}, true,
		&phaseWtStub{deleteErr: worktree.ErrBranchCheckedOut})
	stampRunBranch(t, db, p1, "swarm/phase-"+i64(p1))

	resp := deletePhase(t, branchURL(srv, taskID, p1))
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
		t.Errorf("error = %q, want it to name the checked-out worktree", body.Error)
	}
}

func TestDeletePhaseRunBranch_UnknownPhase_404(t *testing.T) {
	srv, db, taskID, _ := epicFixture(t)
	attachPhaseRunWt(t, db, &phaseStubRunner{}, true, &phaseWtStub{})
	resp := deletePhase(t, branchURL(srv, taskID, 99999))
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", resp.StatusCode)
	}
}

func TestDeletePhaseRunBranch_Running_409(t *testing.T) {
	srv, db, taskID, _ := epicFixture(t)
	p1, _ := fixturePhaseIDs(t, db, taskID)
	r := &phaseStubRunner{block: make(chan struct{})}
	attachPhaseRunWt(t, db, r, false, &phaseWtStub{}) // real goroutine — run stays in flight

	if resp := postPhase(t, phaseRunURL(srv, taskID, p1)); resp.StatusCode != http.StatusAccepted {
		t.Fatalf("run status = %d, want 202", resp.StatusCode)
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		var st string
		if err := db.QueryRow(`SELECT run_state FROM epic_phases WHERE id=?`, p1).Scan(&st); err == nil && st == "running" {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	resp := deletePhase(t, branchURL(srv, taskID, p1))
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("status = %d, want 409", resp.StatusCode)
	}
	postPhase(t, phaseRunURL(srv, taskID, p1)+"/cancel") // unblock the goroutine
}

// Every worktree sentinel through BOTH phaserun switches, asserting the STATUS
// CODE and the `code` discriminator.
//
// The status assertion is the point: worktreeConflict is matched above the generic
// `case err != nil` arm, and an arm placed BELOW that one is dead code no body
// assertion would ever reveal — the request would simply 500 with the same prose.
// ErrBranchIsHead and ErrRefusedBranch had no test on either phase switch, so a
// future edit reordering the arms would have shipped silently.
func TestPhaseRun_WorktreeSentinels_409(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		code string
	}{
		{"is-head", worktree.ErrBranchIsHead, codeBranchIsHead},
		{"refused", worktree.ErrRefusedBranch, codeBranchRefused},
		{"busy", worktree.ErrBranchBusy, codeBranchBusy},
		{"checked-out", worktree.ErrBranchCheckedOut, codeBranchCheckedOut},
		{"detached", worktree.ErrDetachedHead, codeDetachedHead},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv, db, taskID, _ := epicFixture(t)
			p1, _ := fixturePhaseIDs(t, db, taskID)
			attachPhaseRunWt(t, db, &phaseStubRunner{}, true, &phaseWtStub{reclaimErr: tc.err})

			resp := postPhase(t, phaseRunURL(srv, taskID, p1))
			if resp.StatusCode != http.StatusConflict {
				t.Fatalf("status = %d, want 409 — the sentinel arm must sit above `case err != nil`",
					resp.StatusCode)
			}
			var body struct {
				Code  string `json:"code"`
				Error string `json:"error"`
			}
			if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
				t.Fatal(err)
			}
			if body.Code != tc.code {
				t.Errorf("code = %q, want %q", body.Code, tc.code)
			}
			if body.Error == "" {
				t.Error("error = \"\", want an actionable message")
			}
		})
	}
}

func TestDeletePhaseRunBranch_WorktreeSentinels_409(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		code string
	}{
		{"is-head", worktree.ErrBranchIsHead, codeBranchIsHead},
		{"refused", worktree.ErrRefusedBranch, codeBranchRefused},
		{"busy", worktree.ErrBranchBusy, codeBranchBusy},
		{"detached", worktree.ErrDetachedHead, codeDetachedHead},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv, db, taskID, _ := epicFixture(t)
			p1, _ := fixturePhaseIDs(t, db, taskID)
			attachPhaseRunWt(t, db, &phaseStubRunner{}, true, &phaseWtStub{deleteErr: tc.err})
			stampRunBranch(t, db, p1, "swarm/phase-"+i64(p1))

			resp := deletePhase(t, branchURL(srv, taskID, p1))
			if resp.StatusCode != http.StatusConflict {
				t.Fatalf("status = %d, want 409 — the sentinel arm must sit above `case err != nil`",
					resp.StatusCode)
			}
			var body struct {
				Code string `json:"code"`
			}
			if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
				t.Fatal(err)
			}
			if body.Code != tc.code {
				t.Errorf("code = %q, want %q", body.Code, tc.code)
			}
		})
	}
}

// ── orphan cleanup endpoint (phase 8) ──

func orphanURL(srv *httptest.Server, taskID int64, branch string) string {
	return srv.URL + "/api/epics/" + i64(taskID) + "/orphan-branch?branch=" + url.QueryEscape(branch)
}

// The happy path: a swarm/phase-<id> branch whose id matches no row is deletable
// through the sibling route, and `deleted` reports whether it was actually there.
func TestDeleteOrphanBranch_200(t *testing.T) {
	srv, db, taskID, _ := epicFixture(t)
	wt := &phaseWtStub{}
	attachPhaseRunWt(t, db, &phaseStubRunner{}, true, wt)

	resp := deletePhase(t, orphanURL(srv, taskID, "swarm/phase-999999"))
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
	if !body.Deleted || body.Branch != "swarm/phase-999999" {
		t.Errorf("body = %+v, want {true, swarm/phase-999999}", body)
	}
	if wt.deleted != "swarm/phase-999999" {
		t.Errorf("deleted branch = %q, want the one the client named", wt.deleted)
	}
}

// Guard 1: anything outside swarm/phase-<digits> is refused BEFORE any git call.
// This route takes a branch name from the client, so the namespace class has to be
// closed here as well as at the worktree boundary.
func TestDeleteOrphanBranch_RefusesForeignNamespace(t *testing.T) {
	// The non-canonical ids (leading zeros, bare 0, a dash) are refused for a second
	// reason: the route rebuilds the name it hands git from the parsed id, and only a
	// canonical decimal id makes that rebuild an identity. swarm/phase-007 must not
	// resolve to swarm/phase-7 — that is a different branch.
	for _, branch := range []string{"dev", "main", "feature/x", "swarm/", "swarm/plan-71",
		"swarm/phase-", "swarm/phase-abc", "swarm/phase-1/x",
		"swarm/phase-007", "swarm/phase-0", "swarm/phase--1", "swarm/phase-+1",
		"swarm/phase-1 --force"} {
		t.Run(branch, func(t *testing.T) {
			srv, db, taskID, _ := epicFixture(t)
			wt := &phaseWtStub{}
			attachPhaseRunWt(t, db, &phaseStubRunner{}, true, wt)

			resp := deletePhase(t, orphanURL(srv, taskID, branch))
			if resp.StatusCode != http.StatusConflict {
				t.Fatalf("status = %d, want 409 for %q", resp.StatusCode, branch)
			}
			var body struct {
				Code string `json:"code"`
			}
			if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
				t.Fatal(err)
			}
			if body.Code != codeBranchRefused {
				t.Errorf("code = %q, want %q", body.Code, codeBranchRefused)
			}
			if wt.deleted != "" {
				t.Errorf("deleted %q despite the refusal", wt.deleted)
			}
		})
	}
}

// Guard 2: a branch whose id IS a live phase row is refused — including a row in
// ANOTHER epic, since ids are global and an epic-scoped check would let one plan
// delete another plan's live run branch. The phase-scoped route is the only way to
// delete a live phase's branch, and it derives the name rather than taking one.
func TestDeleteOrphanBranch_RefusesLivePhaseBranch(t *testing.T) {
	srv, db, taskID, _ := epicFixture(t)
	p1, _ := fixturePhaseIDs(t, db, taskID)
	wt := &phaseWtStub{}
	attachPhaseRunWt(t, db, &phaseStubRunner{}, true, wt)

	resp := deletePhase(t, orphanURL(srv, taskID, "swarm/phase-"+i64(p1)))
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("status = %d, want 409", resp.StatusCode)
	}
	var body struct {
		Code string `json:"code"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if body.Code != codeBranchLivePhase {
		t.Errorf("code = %q, want %q", body.Code, codeBranchLivePhase)
	}
	if wt.deleted != "" {
		t.Errorf("deleted a live phase's branch (%q)", wt.deleted)
	}
}

// The phase-scoped route still cannot be told a branch: it derives swarm/phase-<id>
// from the row and ignores anything the client tries to pass. That is the property
// the sibling route exists to preserve.
func TestDeletePhaseRunBranch_IgnoresClientSuppliedBranch(t *testing.T) {
	srv, db, taskID, _ := epicFixture(t)
	p1, _ := fixturePhaseIDs(t, db, taskID)
	wt := &phaseWtStub{}
	attachPhaseRunWt(t, db, &phaseStubRunner{}, true, wt)
	stampRunBranch(t, db, p1, "swarm/phase-"+i64(p1))

	resp := deletePhase(t, branchURL(srv, taskID, p1)+"?branch=dev")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if wt.deleted != "swarm/phase-"+i64(p1) {
		t.Errorf("deleted = %q, want the STAMPED branch, never the query param", wt.deleted)
	}
}

// End-to-end: a phase in an epic with a stranded branch reports the orphan through
// GET .../diagnosis. This is the whole point of phase 8 — the deterministic
// diagnosis naming the branch that previously appeared only in executor prose.
func TestPhaseDiagnosis_OrphanBranch_EndToEnd(t *testing.T) {
	srv, db, taskID, _ := epicFixture(t)
	p1, _ := fixturePhaseIDs(t, db, taskID)
	seedNoopPhase(t, db, p1)

	own := "swarm/phase-" + i64(p1)
	g := &diagGitStub{
		base:     "dev",
		ahead:    map[string]int{"swarm/phase-697": 2},
		subjects: map[string][]string{"swarm/phase-697": {"feat: plugindrift", "feat: findings"}},
		branches: []string{"swarm/phase-697", own},
	}
	attachPhaseDiag(t, g)

	resp, err := http.Get(diagURL(srv, taskID, p1))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	var d phasediag.Diagnosis
	if err := json.NewDecoder(resp.Body).Decode(&d); err != nil {
		t.Fatal(err)
	}
	var orphan *phasediag.Blocker
	for i, b := range d.Blockers {
		if b.Kind == phasediag.KindOrphanBranch {
			orphan = &d.Blockers[i]
		}
	}
	if orphan == nil {
		t.Fatalf("blockers = %+v, want an orphan-branch blocker", d.Blockers)
	}
	if orphan.Branch != "swarm/phase-697" || orphan.CommitsAhead != 2 {
		t.Errorf("blocker = {%q,%d}, want {swarm/phase-697,2} as DATA for the cleanup action",
			orphan.Branch, orphan.CommitsAhead)
	}
	if !strings.Contains(orphan.Summary, "swarm/phase-697") {
		t.Errorf("summary = %q, want it to name the stranded branch", orphan.Summary)
	}
}

// An already-gone orphan answers deleted:false — the honest reading of an
// idempotent delete. Without it the UI clears its banner on a no-op, the same
// defect the phase-scoped route's `existed` plumbing exists to prevent.
func TestDeleteOrphanBranch_AlreadyGone_DeletedFalse(t *testing.T) {
	srv, db, taskID, _ := epicFixture(t)
	attachPhaseRunWt(t, db, &phaseStubRunner{}, true, &phaseWtStub{branchMissing: true})

	resp := deletePhase(t, orphanURL(srv, taskID, "swarm/phase-999999"))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	var body struct {
		Deleted bool `json:"deleted"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if body.Deleted {
		t.Error("deleted = true for a branch that was not there")
	}
}

// A branch id too large for int64 must be REFUSED, not slide past guard 2. The
// regexp accepts any digit run, and an unparseable id bound as a string would make
// `id = ?` match no row — the guard would fail OPEN and delete the branch.
func TestDeleteOrphanBranch_RefusesOversizedID(t *testing.T) {
	srv, db, taskID, _ := epicFixture(t)
	wt := &phaseWtStub{}
	attachPhaseRunWt(t, db, &phaseStubRunner{}, true, wt)

	resp := deletePhase(t, orphanURL(srv, taskID, "swarm/phase-99999999999999999999999"))
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("status = %d, want 409", resp.StatusCode)
	}
	if wt.deleted != "" {
		t.Errorf("deleted %q for an id that cannot be checked against the table", wt.deleted)
	}
}

// Unattached daemon: the route answers 503 rather than nil-dereferencing
// phaserunSvc.Wt. detachPhaseRun restores the package var, so this cannot leak
// into a later test in the package.
func TestDeleteOrphanBranch_NotAttached_503(t *testing.T) {
	srv, _, taskID, _ := epicFixture(t)
	detachPhaseRun(t)

	resp := deletePhase(t, orphanURL(srv, taskID, "swarm/phase-999999"))
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", resp.StatusCode)
	}
}
