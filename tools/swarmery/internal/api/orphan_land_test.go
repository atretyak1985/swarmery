package api

import (
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/repoprovider"
)

// The orphan land rides the phase-landing fixture: phase 207 is a live row, and
// swarm/phase-999 names no row at all — the shape of work stranded under an
// older id generation.
const orphanTestBranch = "swarm/phase-999"

func (f *phaseLandingFixture) landOrphan(t *testing.T, branch, body string) (*http.Response, map[string]any) {
	t.Helper()
	url := fmt.Sprintf("%s/api/epics/%d/orphan-branch/land?branch=%s", f.srv, f.taskID, branch)
	return landingJSON(t, http.MethodPost, url, body)
}

func TestLandOrphanBranchPushesAndOpensChangeRequest(t *testing.T) {
	f := newPhaseLandingFixture(t, "done")
	fake := useFakePhaseLand(t, phaseLandOK())

	resp, body := f.landOrphan(t, orphanTestBranch, `{"draft":true}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %v)", resp.StatusCode, body)
	}
	if body["url"] != landTestPRURL || body["number"] != float64(77) || body["branch"] != orphanTestBranch {
		t.Errorf("body = %v, want %s #77 for %s", body, landTestPRURL, orphanTestBranch)
	}
	if !fake.Ran("git push -u origin " + orphanTestBranch) {
		t.Errorf("orphan branch not pushed; calls = %v", fake.Calls)
	}
	if !fake.Ran("gh pr create --head "+orphanTestBranch) || !fake.Ran("--draft") {
		t.Errorf("draft PR not requested for the orphan; calls = %v", fake.Calls)
	}
	if !fake.Ran("--title Order line items: recover stranded work from " + orphanTestBranch) {
		t.Errorf("title not rendered by orphanPRTitle; calls = %v", fake.Calls)
	}
	// No phase row owns the branch, so nothing in the landing lifecycle moves.
	if got := phaseCol(t, f.db, f.phaseID, "landing_state"); got != landingNone {
		t.Errorf("phase %d landing_state = %q, want none — an orphan land must not touch a live phase", f.phaseID, got)
	}
}

func TestLandOrphanBranchRefusesALivePhasesBranch(t *testing.T) {
	f := newPhaseLandingFixture(t, "done")
	fake := useFakePhaseLand(t, phaseLandOK())

	resp, body := f.landOrphan(t, fmt.Sprintf("swarm/phase-%d", f.phaseID), `{}`)
	if resp.StatusCode != http.StatusConflict || body["code"] != codeBranchLivePhase {
		t.Fatalf("status = %d code = %v, want 409 %s", resp.StatusCode, body["code"], codeBranchLivePhase)
	}
	if len(fake.Calls) != 0 {
		t.Errorf("a refused orphan land ran %v — the gate must come before any git call", fake.Calls)
	}
}

func TestLandOrphanBranchRefusesANonRunBranch(t *testing.T) {
	f := newPhaseLandingFixture(t, "done")
	useFakePhaseLand(t, phaseLandOK())

	resp, body := f.landOrphan(t, "main", `{}`)
	if resp.StatusCode != http.StatusConflict || body["code"] != codeBranchRefused {
		t.Fatalf("status = %d code = %v, want 409 %s", resp.StatusCode, body["code"], codeBranchRefused)
	}
}

func TestLandOrphanBranchNotAuthenticatedStampsNoPhase(t *testing.T) {
	f := newPhaseLandingFixture(t, "done")
	useFakePhaseLand(t, &repoprovider.FakeExec{
		Out: map[string]string{"git remote": "https://github.com/acme/widgets.git\n"},
		Errs: map[string]string{"git push": "remote: Invalid username or token " + landTestToken + "\n" +
			"fatal: Authentication failed for 'https://github.com/acme/widgets.git/'\n"},
	})

	resp, body := f.landOrphan(t, orphanTestBranch, `{}`)
	assert422(t, resp, body, codeNotAuthenticated, "gh auth login --hostname github.com")
	assertRedactedDetail(t, body)
	if got := phaseCol(t, f.db, f.phaseID, "landing_error"); got != "" {
		t.Errorf("landing_error = %q on phase %d, want empty — the orphan has no phase row to stamp", got, f.phaseID)
	}
	if strings.Contains(fmt.Sprint(body), landTestToken) {
		t.Errorf("response carries the raw token")
	}
}
