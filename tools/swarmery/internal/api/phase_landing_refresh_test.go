package api

// Handler tests for POST …/landing/refresh (phase_landing_refresh.go). The
// phase fixture is the land tests' (a real temp repo for RunRoot); every call
// that would reach a code host — the remote lookup and `gh pr view` — goes
// through one scripted repoprovider.FakeExec swapped into landStatusExec.

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/repoprovider"
)

// ghPRView is a `gh pr view --json` body.
func ghPRView(state, mergedAt, review, checks string) string {
	return fmt.Sprintf(`{"state":%q,"isDraft":false,"mergedAt":%q,"reviewDecision":%q,"statusCheckRollup":[%s],"url":%q}`,
		state, mergedAt, review, checks, landTestPRURL)
}

const ghCheckOK = `{"__typename":"CheckRun","status":"COMPLETED","conclusion":"SUCCESS"}`

// useFakeLandStatus drives the refresh's process boundary from f (restored on
// cleanup).
func useFakeLandStatus(t *testing.T, f *repoprovider.FakeExec) *repoprovider.FakeExec {
	t.Helper()
	prev := landStatusExec
	landStatusExec = f
	t.Cleanup(func() { landStatusExec = prev })
	return f
}

// prOpenFixture is the land fixture with phase 207's PR #77 open on GitHub.
func prOpenFixture(t *testing.T) *phaseLandingFixture {
	t.Helper()
	f := newPhaseLandingFixture(t, "done")
	if _, err := f.db.Exec(`UPDATE epic_phases SET landing_state = 'pr_open', pr_url = ?, pr_number = 77,
		pr_provider = 'github' WHERE id = ?`, landTestPRURL, f.phaseID); err != nil {
		t.Fatal(err)
	}
	return f
}

func (f *phaseLandingFixture) refresh(t *testing.T, phaseID int64) (*http.Response, map[string]any) {
	t.Helper()
	return landingJSON(t, http.MethodPost, f.url(phaseID, "landing/refresh"), "")
}

func TestLandingRefreshStoresStatus(t *testing.T) {
	f := prOpenFixture(t)
	fake := useFakeLandStatus(t, &repoprovider.FakeExec{Out: map[string]string{
		"git remote": "git@github.com:acme/widgets.git\n",
		"gh pr":      ghPRView("OPEN", "", "REVIEW_REQUIRED", ghCheckOK),
	}})

	before := time.Now().UTC().Add(-time.Second)
	resp, body := f.refresh(t, f.phaseID)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %v)", resp.StatusCode, body)
	}
	if body["state"] != landingPROpen || body["landedAt"] != nil {
		t.Errorf("landing = %v, want pr_open with landedAt null", body)
	}
	st, _ := body["prStatus"].(map[string]any)
	if st["state"] != "open" || st["ci"] != "success" || st["review"] != "review_required" {
		t.Errorf("prStatus = %v, want open / success / review_required", st)
	}
	checked, err := time.Parse(time.RFC3339Nano, fmt.Sprint(st["checkedAt"]))
	if err != nil || checked.Before(before) {
		t.Errorf("prStatus.checkedAt = %v (%v), want the refresh time", st["checkedAt"], err)
	}
	// Stored, not just answered.
	var stored repoprovider.ChangeStatus
	if err := json.Unmarshal([]byte(phaseCol(t, f.db, f.phaseID, "pr_status")), &stored); err != nil {
		t.Fatalf("pr_status: %v", err)
	}
	if stored.CI != repoprovider.CISuccess || stored.State != repoprovider.StateOpen {
		t.Errorf("stored pr_status = %+v", stored)
	}
	if phaseCol(t, f.db, f.phaseID, "pr_checked_at") == "" {
		t.Error("pr_checked_at not stamped")
	}
	if !fake.Ran("gh pr view 77") {
		t.Errorf("the PR was not read by number; calls = %v", fake.Calls)
	}
	for i, d := range fake.Dirs {
		if d != f.repo && d != "" {
			t.Errorf("call %q ran in %q, want the phase's repo %q", fake.Calls[i], d, f.repo)
		}
	}
}

func TestLandingRefreshMergedFlipsState(t *testing.T) {
	f := prOpenFixture(t)
	useFakeLandStatus(t, &repoprovider.FakeExec{Out: map[string]string{
		"git remote": "git@github.com:acme/widgets.git\n",
		"gh pr":      ghPRView("MERGED", "2026-10-09T11:00:00Z", "APPROVED", ghCheckOK),
	}})

	resp, body := f.refresh(t, f.phaseID)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %v)", resp.StatusCode, body)
	}
	if body["state"] != landingMerged || body["landedAt"] == nil {
		t.Errorf("landing = %v, want merged with landedAt", body)
	}
	if got := phaseCol(t, f.db, f.phaseID, "landing_state"); got != landingMerged {
		t.Errorf("landing_state = %q, want merged", got)
	}
	if phaseCol(t, f.db, f.phaseID, "landed_at") == "" {
		t.Error("landed_at not stamped on the merge")
	}
	if st, _ := body["prStatus"].(map[string]any); st["state"] != "merged" || st["review"] != "approved" {
		t.Errorf("prStatus = %v, want merged / approved", st)
	}
}

func TestLandingRefreshNoChangeRequestIs409(t *testing.T) {
	f := newPhaseLandingFixture(t, "done")
	fake := useFakeLandStatus(t, &repoprovider.FakeExec{})
	for _, state := range []string{landingNone, landingPushed, landingReturned} {
		if _, err := f.db.Exec(`UPDATE epic_phases SET landing_state = ? WHERE id = ?`, state, f.phaseID); err != nil {
			t.Fatal(err)
		}
		resp, body := f.refresh(t, f.phaseID)
		if resp.StatusCode != http.StatusConflict || body["code"] != codeNoChangeRequest {
			t.Errorf("%s: status = %d code = %v, want 409 %s", state, resp.StatusCode, body["code"], codeNoChangeRequest)
		}
	}
	if len(fake.Calls) != 0 {
		t.Errorf("a refusal reached the host: %v", fake.Calls)
	}
}

func TestLandingRefreshUnknownPhaseIs404(t *testing.T) {
	f := prOpenFixture(t)
	useFakeLandStatus(t, &repoprovider.FakeExec{})
	if resp, _ := f.refresh(t, 9999); resp.StatusCode != http.StatusNotFound {
		t.Errorf("unknown phase: status = %d, want 404", resp.StatusCode)
	}
	// The right phase addressed through the wrong epic is the same 404.
	wrong := fmt.Sprintf("%s/api/epics/%d/phases/%d/landing/refresh", f.srv, f.taskID+1, f.phaseID)
	if resp, _ := landingJSON(t, http.MethodPost, wrong, ""); resp.StatusCode != http.StatusNotFound {
		t.Errorf("mismatched epic: status = %d, want 404", resp.StatusCode)
	}
}

func TestLandingRefreshNotAuthenticatedIs422AndMarksExpired(t *testing.T) {
	f := prOpenFixture(t)
	useFakeLandStatus(t, &repoprovider.FakeExec{
		Out:  map[string]string{"git remote": "git@github.com:acme/widgets.git\n"},
		Errs: map[string]string{"gh pr": "HTTP 401: Bad credentials " + landTestToken},
	})
	const projectID = 1
	c := projectVcsCache
	c.mu.Lock()
	delete(c.expiredAt, projectID)
	c.mu.Unlock()
	t.Cleanup(func() {
		c.mu.Lock()
		delete(c.expiredAt, projectID)
		c.mu.Unlock()
	})

	resp, body := f.refresh(t, f.phaseID)
	assert422(t, resp, body, codeNotAuthenticated, "gh auth login --hostname github.com")
	assertRedactedDetail(t, body)

	c.mu.Lock()
	_, marked := c.expiredAt[projectID]
	c.mu.Unlock()
	if !marked {
		t.Error("a rejected credential did not mark the project's VCS auth expired")
	}
	if got := phaseCol(t, f.db, f.phaseID, "landing_error"); !strings.HasPrefix(got, codeNotAuthenticated+": ") ||
		strings.Contains(got, landTestToken) {
		t.Errorf("landing_error = %q, want a redacted not-authenticated stamp", got)
	}
	if got := phaseCol(t, f.db, f.phaseID, "landing_state"); got != landingPROpen {
		t.Errorf("landing_state = %q, want pr_open unchanged", got)
	}
}
