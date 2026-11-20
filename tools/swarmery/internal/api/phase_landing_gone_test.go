package api

import (
	"net/http"
	"testing"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/phaserun"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/worktree"
)

// A land whose run branch is gone from the repo (the operator merged the work
// through another PR and deleted the branch) must not try `git push` on it.

// attachBranchProbe attaches a live-shaped phase-run service over the fixture's
// real repo, so RunBranchGone asks real git.
func (f *phaseLandingFixture) attachBranchProbe(t *testing.T) {
	t.Helper()
	prev := phaserunSvc
	phaserunSvc = &phaserun.Service{
		DB:       f.db,
		Wt:       &worktree.Manager{Git: worktree.ExecGit{}, Root: t.TempDir()},
		RepoRoot: func(string, ...string) (string, error) { return f.repo, nil },
	}
	t.Cleanup(func() { phaserunSvc = prev })
}

func TestLandPhaseGoneBranchOfFinishedPhaseSettlesAsMerged(t *testing.T) {
	f := newPhaseLandingFixture(t, "blocked")
	f.attachBranchProbe(t)
	if _, err := f.db.Exec(`UPDATE epic_phases SET checkboxes_done = checkboxes_total WHERE id = ?`, f.phaseID); err != nil {
		t.Fatal(err)
	}
	gitFixture(t, f.repo, "branch", "-D", landTestBranch)
	fake := useFakePhaseLand(t, phaseLandOK())

	resp, body := f.land(t, `{"action":"pr"}`)
	if resp.StatusCode != http.StatusConflict || body["code"] != codeRunBranchGone {
		t.Fatalf("status = %d code = %v, want 409 %s (body %v)", resp.StatusCode, body["code"], codeRunBranchGone, body)
	}
	if l := landingOf(t, body); l["state"] != landingMerged || l["landedAt"] != nil {
		t.Errorf("landing = %v, want merged with no landedAt", l)
	}
	if got := phaseCol(t, f.db, f.phaseID, "landing_state"); got != landingMerged {
		t.Errorf("stored landing_state = %q, want merged", got)
	}
	if fake.Ran("git push") || fake.Ran("gh pr create") {
		t.Errorf("a gone branch was pushed: %v", fake.Calls)
	}
}

func TestLandPhaseGoneBranchOfUnfinishedPhaseIsRefused(t *testing.T) {
	f := newPhaseLandingFixture(t, "done") // one criterion still open
	f.attachBranchProbe(t)
	gitFixture(t, f.repo, "branch", "-D", landTestBranch)
	fake := useFakePhaseLand(t, phaseLandOK())

	resp, body := f.land(t, `{"action":"push"}`)
	if resp.StatusCode != http.StatusConflict || body["code"] != codeRunBranchGone {
		t.Fatalf("status = %d code = %v, want 409 %s (body %v)", resp.StatusCode, body["code"], codeRunBranchGone, body)
	}
	if _, has := body["landing"]; has {
		t.Errorf("an unsettled refusal carries a landing: %v", body)
	}
	if got := phaseCol(t, f.db, f.phaseID, "landing_state"); got == landingMerged {
		t.Error("an unfinished phase was settled as merged")
	}
	if fake.Ran("git push") {
		t.Errorf("a gone branch was pushed: %v", fake.Calls)
	}
}

func TestLandPhasePresentBranchStillPushes(t *testing.T) {
	f := newPhaseLandingFixture(t, "done")
	f.attachBranchProbe(t)
	fake := useFakePhaseLand(t, phaseLandOK())

	resp, body := f.land(t, `{"action":"push"}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %v)", resp.StatusCode, body)
	}
	if !fake.Ran("git push") {
		t.Errorf("present branch not pushed: %v", fake.Calls)
	}
}
