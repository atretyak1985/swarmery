package api

// Handler tests for the plan-phase landing endpoints (phase_landing.go).
//
// The diff half runs against a REAL temp git repo (reviewRepo, as the board diff
// tests do): the review endpoint's job is to translate git's output. Everything
// that would talk to a code host — `git remote`, `git push`, `gh pr create`, the
// origin-HEAD lookup — goes through one scripted repoprovider.FakeExec, swapped
// into both landProvider and landExec. SWARMERY_SECRETS_DIR is a temp dir, so
// credstore never reads the operator's real store, and no test reaches a network.

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/phaserun"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/repoprovider"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/repoprovider/credstore"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/runcore"
)

const (
	landTestPhaseID = 207
	landTestBranch  = "swarm/phase-207"
	landTestPRURL   = "https://github.com/acme/widgets/pull/77"
	// A token shape credstore.Redact masks; planted in tool stderr so every 422
	// test proves the body never carries it.
	landTestToken = "ghp_ABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"
)

const landTestDoc = `# Phase 2 — Line-item CRUD

## Goal
Add, edit and remove line items on an order.

## Acceptance Criteria
- [x] POST creates a line item
- [ ] DELETE removes it

## Completion Report
Shipped the CRUD handlers.
`

// phaseLandingFixture is one plan with one phase whose run left a branch in a
// real repo.
type phaseLandingFixture struct {
	srv     string
	db      *sql.DB
	repo    string // symlink-resolved — what RunRoot hands back
	base    string
	taskID  int64
	phaseID int64
	docPath string
}

// newPhaseLandingFixture seeds project 1 → a temp repo carrying landTestBranch,
// a workspace task "Order line items" with its plan dir, and phase 207 in the
// given run state, pinned to the repo's base commit.
func newPhaseLandingFixture(t *testing.T, runState string) *phaseLandingFixture {
	t.Helper()
	repo, base := reviewRepo(t, landTestBranch)
	srv, db := reviewServer(t, repo)
	prev := phaserunSvc
	phaserunSvc = nil // landingRepoDir must work without an attached service
	t.Cleanup(func() { phaserunSvc = prev })

	planDir := filepath.Join(t.TempDir(), "order-line-items", "plan")
	if err := os.MkdirAll(planDir, 0o755); err != nil {
		t.Fatal(err)
	}
	docPath := filepath.Join(planDir, "phase-2-line-item-crud.md")
	if err := os.WriteFile(docPath, []byte(landTestDoc), 0o644); err != nil {
		t.Fatal(err)
	}
	res, err := db.Exec(`INSERT INTO tasks (project_id, title, prompt, status, created_at,
		started_at, source, external_id) VALUES (1, 'Order line items', 'goal', 'running',
		'2026-10-09T00:00:00Z', '2026-10-09T00:00:00Z', 'workspace', '2026-10-09-order-line-items')`)
	if err != nil {
		t.Fatal(err)
	}
	taskID, _ := res.LastInsertId()
	if _, err := db.Exec(`INSERT INTO task_artifacts (task_id, kind, path, content_hash, parsed_at)
		VALUES (?, 'plan', ?, 'hash', '2026-10-09T00:00:00Z')`, taskID, planDir); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO epic_phases
		(id, workspace_task_id, seq, name, doc_path, depends_on, checkboxes_total, checkboxes_done,
		 run_state, run_branch, run_start_point, verify_mode, verify_verdict, verify_detail)
		VALUES (?, ?, 2, 'Phase 2 — Line-item CRUD', ?, '[]', 2, 1, ?, ?, ?, 'normal', 'pass', 'all green')`,
		landTestPhaseID, taskID, docPath, runState, landTestBranch, base); err != nil {
		t.Fatal(err)
	}
	real, err := filepath.EvalSymlinks(repo)
	if err != nil {
		t.Fatal(err)
	}
	return &phaseLandingFixture{
		srv: srv.URL, db: db, repo: real, base: base,
		taskID: taskID, phaseID: landTestPhaseID, docPath: docPath,
	}
}

// useFakePhaseLand drives the landing path's provider factory AND its local git
// reads from one scripted fake (restored on cleanup).
func useFakePhaseLand(t *testing.T, f *repoprovider.FakeExec) *repoprovider.FakeExec {
	t.Helper()
	useFakeLand(t, f)
	prev := landExec
	landExec = f
	t.Cleanup(func() { landExec = prev })
	return f
}

// phaseLandOK is the fake for a land that succeeds end to end.
func phaseLandOK() *repoprovider.FakeExec {
	return &repoprovider.FakeExec{Out: map[string]string{
		"git remote": "git@github.com:acme/widgets.git\n",
		"gh pr":      "Creating pull request…\n" + landTestPRURL + "\n",
	}}
}

func (f *phaseLandingFixture) url(phaseID int64, tail string) string {
	return fmt.Sprintf("%s/api/epics/%d/phases/%d/%s", f.srv, f.taskID, phaseID, tail)
}

func (f *phaseLandingFixture) review(t *testing.T) (*http.Response, map[string]any) {
	t.Helper()
	return landingJSON(t, http.MethodGet, f.url(f.phaseID, "review"), "")
}

func (f *phaseLandingFixture) land(t *testing.T, body string) (*http.Response, map[string]any) {
	t.Helper()
	return landingJSON(t, http.MethodPost, f.url(f.phaseID, "land"), body)
}

func landingJSON(t *testing.T, method, url, body string) (*http.Response, map[string]any) {
	t.Helper()
	req, err := http.NewRequest(method, url, bytes.NewReader([]byte(body)))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var out map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decode %s %s: %v", method, url, err)
	}
	return resp, out
}

// phaseCol reads one epic_phases column.
func phaseCol(t *testing.T, db *sql.DB, id int64, col string) string {
	t.Helper()
	var v sql.NullString
	if err := db.QueryRow(`SELECT `+col+` FROM epic_phases WHERE id = ?`, id).Scan(&v); err != nil {
		t.Fatalf("read %s: %v", col, err)
	}
	return v.String
}

func landingOf(t *testing.T, body map[string]any) map[string]any {
	t.Helper()
	l, ok := body["landing"].(map[string]any)
	if !ok {
		t.Fatalf("body has no landing object: %v", body)
	}
	return l
}

// assert422 checks the {error, code, hint, detail} shape, the code, that the
// hint carries every wanted command, and that the planted token never leaks.
func assert422(t *testing.T, resp *http.Response, body map[string]any, code string, hintWants ...string) {
	t.Helper()
	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422 (body %v)", resp.StatusCode, body)
	}
	if body["code"] != code {
		t.Errorf("code = %v, want %q", body["code"], code)
	}
	if e, _ := body["error"].(string); e == "" {
		t.Error("422 body has no error message")
	}
	hint, _ := body["hint"].(string)
	for _, w := range hintWants {
		if !strings.Contains(hint, w) {
			t.Errorf("hint %q does not carry %q", hint, w)
		}
	}
	if d, _ := body["detail"].(string); d == "" {
		t.Errorf("422 body has no detail: %v", body)
	}
	raw, _ := json.Marshal(body)
	if strings.Contains(string(raw), landTestToken) {
		t.Errorf("token leaked into the 422 body: %s", raw)
	}
}

// assertRedactedDetail checks that the tool output planted with landTestToken
// reached `detail` — masked, not dropped.
func assertRedactedDetail(t *testing.T, body map[string]any) {
	t.Helper()
	if d, _ := body["detail"].(string); !strings.Contains(d, "***") {
		t.Errorf("detail %q does not carry the masked tool output", d)
	}
}

// ── review ───────────────────────────────────────────────────────────────────

func TestPhaseReviewReturnsDiffVerdictLandingTerms(t *testing.T) {
	f := newPhaseLandingFixture(t, "done")
	useFakePhaseLand(t, phaseLandOK())

	resp, body := f.review(t)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %v)", resp.StatusCode, body)
	}
	if body["base"] != f.base || body["branch"] != landTestBranch {
		t.Errorf("base/branch = %v/%v, want %s/%s", body["base"], body["branch"], f.base, landTestBranch)
	}
	if commits, _ := body["commits"].([]any); len(commits) != 2 {
		t.Errorf("commits = %v, want 2", body["commits"])
	}
	if files, _ := body["files"].([]any); len(files) != 2 {
		t.Errorf("files = %v, want 2", body["files"])
	}
	if patch, _ := body["patch"].(string); !strings.Contains(patch, "beta.txt") {
		t.Errorf("patch does not carry the branch's change: %q", patch)
	}
	if body["verifyVerdict"] != "pass" || body["verifyDetail"] != "all green" {
		t.Errorf("verdict = %v/%v, want pass/all green", body["verifyVerdict"], body["verifyDetail"])
	}
	if l := landingOf(t, body); l["state"] != landingReady {
		t.Errorf("landing.state = %v, want ready (derived for a done run)", l["state"])
	}
	terms, _ := body["terms"].(map[string]any)
	if terms["provider"] != "GitHub" || terms["changeShort"] != "PR" {
		t.Errorf("terms = %v, want GitHub/PR", terms)
	}
}

func TestPhaseReviewUnknownPhaseIs404(t *testing.T) {
	f := newPhaseLandingFixture(t, "done")
	useFakePhaseLand(t, phaseLandOK())

	resp, _ := landingJSON(t, http.MethodGet, f.url(9999, "review"), "")
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("unknown phase: status = %d, want 404", resp.StatusCode)
	}
	// The right phase addressed through the wrong epic is the same 404.
	wrong := fmt.Sprintf("%s/api/epics/%d/phases/%d/review", f.srv, f.taskID+1, f.phaseID)
	if resp, _ := landingJSON(t, http.MethodGet, wrong, ""); resp.StatusCode != http.StatusNotFound {
		t.Errorf("mismatched epic: status = %d, want 404", resp.StatusCode)
	}
}

func TestPhaseReviewWithoutRunBranchIs409(t *testing.T) {
	f := newPhaseLandingFixture(t, "idle")
	if _, err := f.db.Exec(`UPDATE epic_phases SET run_branch = NULL WHERE id = ?`, f.phaseID); err != nil {
		t.Fatal(err)
	}
	resp, body := f.review(t)
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("status = %d, want 409", resp.StatusCode)
	}
	if body["code"] != codeNoRunBranch {
		t.Errorf("code = %v, want %q", body["code"], codeNoRunBranch)
	}
}

// ── land: success ────────────────────────────────────────────────────────────

func TestLandPhasePushMarksPushed(t *testing.T) {
	f := newPhaseLandingFixture(t, "done")
	fake := useFakePhaseLand(t, phaseLandOK())

	resp, body := f.land(t, `{"action":"push"}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %v)", resp.StatusCode, body)
	}
	if l := landingOf(t, body); l["state"] != landingPushed || l["landedAt"] == nil {
		t.Errorf("landing = %v, want pushed with landedAt", l)
	}
	if got := phaseCol(t, f.db, f.phaseID, "landing_state"); got != landingPushed {
		t.Errorf("landing_state = %q, want pushed", got)
	}
	if !fake.Ran("git push -u origin " + landTestBranch) {
		t.Errorf("branch was not pushed; calls = %v", fake.Calls)
	}
	if fake.Ran("gh pr") {
		t.Errorf("action push must not open a change request; calls = %v", fake.Calls)
	}
	// Every tool call runs in the phase's resolved repo root.
	for i, d := range fake.Dirs {
		if d != f.repo && d != "" {
			t.Errorf("call %q ran in %q, want %q", fake.Calls[i], d, f.repo)
		}
	}
}

func TestLandPhasePROpensDraftChangeRequest(t *testing.T) {
	f := newPhaseLandingFixture(t, "done")
	fake := useFakePhaseLand(t, phaseLandOK())

	resp, body := f.land(t, `{"action":"pr","draft":true}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %v)", resp.StatusCode, body)
	}
	l := landingOf(t, body)
	if l["state"] != landingPROpen || l["prUrl"] != landTestPRURL || l["prNumber"] != float64(77) ||
		l["prProvider"] != "github" {
		t.Errorf("landing = %v, want pr_open %s #77 github", l, landTestPRURL)
	}
	if got := phaseCol(t, f.db, f.phaseID, "pr_url"); got != landTestPRURL {
		t.Errorf("pr_url = %q", got)
	}
	if !fake.Ran("git push -u origin " + landTestBranch) {
		t.Errorf("branch not pushed before the PR; calls = %v", fake.Calls)
	}
	if !fake.Ran("gh pr create --head "+landTestBranch) || !fake.Ran("--draft") {
		t.Errorf("draft PR not requested; calls = %v", fake.Calls)
	}
	// Title de-duplicates the doc's own "Phase 2 — " lead; body carries the trailer.
	if !fake.Ran("--title Order line items: Phase 2 — Line-item CRUD --body") {
		t.Errorf("title not rendered by phasePRTitle; calls = %v", fake.Calls)
	}
	if !fake.Ran(fmt.Sprintf("Swarm-Phase: %d/%d", f.taskID, f.phaseID)) || !fake.Ran("- [x] POST creates a line item") {
		t.Errorf("body not rendered by phasePRBody; calls = %v", fake.Calls)
	}
}

func TestLandPhaseReadyIsDerivedNotStored(t *testing.T) {
	f := newPhaseLandingFixture(t, "done")
	resp, err := http.Get(f.srv + "/api/epics?projectId=1")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var epics []epicDTO
	if err := json.NewDecoder(resp.Body).Decode(&epics); err != nil {
		t.Fatal(err)
	}
	if len(epics) != 1 || len(epics[0].Phases) != 1 {
		t.Fatalf("epics = %+v", epics)
	}
	if got := epics[0].Phases[0].Landing.State; got != landingReady {
		t.Errorf("landing.state = %q, want ready for a done run with nothing landed", got)
	}
	if got := phaseCol(t, f.db, f.phaseID, "landing_state"); got != landingNone {
		t.Errorf("stored landing_state = %q, want none (ready is never written)", got)
	}
	// An idle phase is plain none.
	if _, err := f.db.Exec(`UPDATE epic_phases SET run_state = 'idle' WHERE id = ?`, f.phaseID); err != nil {
		t.Fatal(err)
	}
	ph, _, _, err := (&Handler{DB: f.db}).epicPhases(f.taskID, "")
	if err != nil {
		t.Fatal(err)
	}
	if ph[0].Landing.State != landingNone {
		t.Errorf("idle phase landing.state = %q, want none", ph[0].Landing.State)
	}
}

// ── land: refusals (409 / 400) ───────────────────────────────────────────────

func TestLandPhaseUnknownActionIs400(t *testing.T) {
	f := newPhaseLandingFixture(t, "done")
	fake := useFakePhaseLand(t, phaseLandOK())
	for _, b := range []string{`{"action":"return"}`, `{}`, ``} {
		if resp, _ := f.land(t, b); resp.StatusCode != http.StatusBadRequest {
			t.Errorf("body %q: status = %d, want 400", b, resp.StatusCode)
		}
	}
	if len(fake.Calls) != 0 {
		t.Errorf("a refused request ran tools: %v", fake.Calls)
	}
}

func TestLandPhaseRefusesRunningPhase(t *testing.T) {
	f := newPhaseLandingFixture(t, "running")
	fake := useFakePhaseLand(t, phaseLandOK())

	resp, body := f.land(t, `{"action":"push"}`)
	if resp.StatusCode != http.StatusConflict || body["code"] != codePhaseRunning {
		t.Fatalf("status/code = %d/%v, want 409 %s", resp.StatusCode, body["code"], codePhaseRunning)
	}
	if len(fake.Calls) != 0 {
		t.Errorf("a running phase ran tools: %v", fake.Calls)
	}
}

func TestLandPhasePushToBaseRefused(t *testing.T) {
	f := newPhaseLandingFixture(t, "done")
	if _, err := f.db.Exec(`UPDATE epic_phases SET run_branch = 'main' WHERE id = ?`, f.phaseID); err != nil {
		t.Fatal(err)
	}
	fake := phaseLandOK()
	fake.Out["git symbolic-ref"] = "origin/main\n"
	useFakePhaseLand(t, fake)

	resp, body := f.land(t, `{"action":"push"}`)
	if resp.StatusCode != http.StatusConflict || body["code"] != codePushToBaseRefused {
		t.Fatalf("status/code = %d/%v, want 409 %s (body %v)", resp.StatusCode, body["code"], codePushToBaseRefused, body)
	}
	if fake.Ran("git push") {
		t.Errorf("pushed onto the base branch; calls = %v", fake.Calls)
	}
}

// With no vcs.baseBranch and no origin/HEAD the base falls back to the run's start
// point — a SHA no branch name equals. The checked-out branch must still count as
// the base, or the refusal is bypassed exactly where the base is least known.
func TestLandPhasePushToCheckedOutBranchRefusedOnStartPointFallback(t *testing.T) {
	f := newPhaseLandingFixture(t, "done")
	if _, err := f.db.Exec(`UPDATE epic_phases SET run_branch = 'trunk' WHERE id = ?`, f.phaseID); err != nil {
		t.Fatal(err)
	}
	fake := phaseLandOK()
	fake.Out["git rev-parse"] = "trunk\n" // checked out; no origin/HEAD scripted
	useFakePhaseLand(t, fake)

	resp, body := f.land(t, `{"action":"push"}`)
	if resp.StatusCode != http.StatusConflict || body["code"] != codePushToBaseRefused {
		t.Fatalf("status/code = %d/%v, want 409 %s (body %v)", resp.StatusCode, body["code"], codePushToBaseRefused, body)
	}
	if body["base"] != f.base {
		t.Errorf("base = %v, want the start point %s", body["base"], f.base)
	}
	if fake.Ran("git push") {
		t.Errorf("pushed onto the checked-out branch; calls = %v", fake.Calls)
	}
}

func TestLandPhaseAllowPushToBaseLocalOverride(t *testing.T) {
	f := newPhaseLandingFixture(t, "done")
	if _, err := f.db.Exec(`UPDATE epic_phases SET run_branch = 'main' WHERE id = ?`, f.phaseID); err != nil {
		t.Fatal(err)
	}
	// project.json asks for the refusal explicitly; the machine-local file wins.
	writeClaudeFile(t, f.repo, "project.json", `{"vcs":{"allowPushToBase":false}}`)
	writeClaudeFile(t, f.repo, "settings.local.json", `{"swarmery":{"vcs":{"allowPushToBase":true}}}`)
	fake := phaseLandOK()
	fake.Out["git symbolic-ref"] = "origin/main\n"
	useFakePhaseLand(t, fake)

	resp, body := f.land(t, `{"action":"push"}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %v)", resp.StatusCode, body)
	}
	if !fake.Ran("git push -u origin main") {
		t.Errorf("push not attempted; calls = %v", fake.Calls)
	}
}

func TestLandPhaseForkRemoteUnsupported(t *testing.T) {
	f := newPhaseLandingFixture(t, "done")
	writeClaudeFile(t, f.repo, "project.json", `{"vcs":{"forkRemote":"fork"}}`)
	fake := useFakePhaseLand(t, phaseLandOK())

	resp, body := f.land(t, `{"action":"pr"}`)
	if resp.StatusCode != http.StatusConflict || body["code"] != codeForkUnsupported {
		t.Fatalf("status/code = %d/%v, want 409 %s", resp.StatusCode, body["code"], codeForkUnsupported)
	}
	if len(fake.Calls) != 0 {
		t.Errorf("fork refusal must come before any tool call; calls = %v", fake.Calls)
	}
}

func writeClaudeFile(t *testing.T, repo, name, content string) {
	t.Helper()
	dir := filepath.Join(repo, ".claude")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// ── land: machine problems (422) ─────────────────────────────────────────────

func TestLandPhaseWithoutRemoteIs422(t *testing.T) {
	f := newPhaseLandingFixture(t, "done")
	fake := useFakePhaseLand(t, &repoprovider.FakeExec{Errs: map[string]string{
		"git remote": "error: No such remote 'origin' " + landTestToken + "\n",
	}})

	resp, body := f.land(t, `{"action":"push"}`)
	assert422(t, resp, body, codeNoRemote, "git -C "+f.repo+" remote add origin <url>", "push -u origin "+landTestBranch)
	assertRedactedDetail(t, body)
	if fake.Ran("git push") {
		t.Error("pushed despite having no origin")
	}
	if got := phaseCol(t, f.db, f.phaseID, "landing_state"); got != landingNone {
		t.Errorf("landing_state = %q, want none (unchanged)", got)
	}
}

func TestLandPhaseNotAuthenticatedIs422AndStampsError(t *testing.T) {
	f := newPhaseLandingFixture(t, "done")
	useFakePhaseLand(t, &repoprovider.FakeExec{
		Out: map[string]string{"git remote": "https://github.com/acme/widgets.git\n"},
		Errs: map[string]string{"git push": "remote: Invalid username or token " + landTestToken + "\n" +
			"fatal: Authentication failed for 'https://github.com/acme/widgets.git/'\n"},
	})

	resp, body := f.land(t, `{"action":"push"}`)
	assert422(t, resp, body, codeNotAuthenticated, "gh auth login --hostname github.com")
	assertRedactedDetail(t, body)
	stamped := phaseCol(t, f.db, f.phaseID, "landing_error")
	if !strings.HasPrefix(stamped, codeNotAuthenticated) {
		t.Errorf("landing_error = %q, want it stamped with %s", stamped, codeNotAuthenticated)
	}
	if strings.Contains(stamped, landTestToken) {
		t.Errorf("landing_error carries the raw token: %q", stamped)
	}
	if got := phaseCol(t, f.db, f.phaseID, "landing_state"); got != landingNone {
		t.Errorf("landing_state = %q, want none", got)
	}
}

func TestLandPhaseNoPushAccessIs422(t *testing.T) {
	f := newPhaseLandingFixture(t, "done")
	useFakePhaseLand(t, &repoprovider.FakeExec{
		Out: map[string]string{"git remote": "https://github.com/acme/widgets.git\n"},
		Errs: map[string]string{"git push": "remote: Permission to acme/widgets.git denied to bot " + landTestToken + ".\n" +
			"fatal: unable to access 'https://github.com/acme/widgets.git/': The requested URL returned error: 403\n"},
	})

	resp, body := f.land(t, `{"action":"push"}`)
	assert422(t, resp, body, codeNoPushAccess, "github.com/acme/widgets", "push -u origin "+landTestBranch)
	assertRedactedDetail(t, body)
}

func TestLandPhaseBinaryMissingIs422(t *testing.T) {
	t.Run("gh missing after the push", func(t *testing.T) {
		f := newPhaseLandingFixture(t, "done")
		fake := useFakePhaseLand(t, &repoprovider.FakeExec{
			Out:     map[string]string{"git remote": "git@github.com:acme/widgets.git\n"},
			Missing: map[string]bool{"gh": true},
		})
		resp, body := f.land(t, `{"action":"pr"}`)
		assert422(t, resp, body, codeBinaryMissing, "gh pr create --head "+landTestBranch, "is pushed", "cli.github.com")
		if !fake.Ran("git push") {
			t.Error("expected the push to have run before the gh check")
		}
		// The push landed, so the state says so even though the PR did not.
		if got := phaseCol(t, f.db, f.phaseID, "landing_state"); got != landingPushed {
			t.Errorf("landing_state = %q, want pushed", got)
		}
	})
	t.Run("git missing", func(t *testing.T) {
		f := newPhaseLandingFixture(t, "done")
		useFakePhaseLand(t, &repoprovider.FakeExec{Missing: map[string]bool{"git": true}})
		resp, body := f.land(t, `{"action":"push"}`)
		assert422(t, resp, body, codeBinaryMissing, "Install git")
	})
}

func TestLandPhaseRemoteDivergedIs422WithoutRetry(t *testing.T) {
	f := newPhaseLandingFixture(t, "done")
	fake := useFakePhaseLand(t, &repoprovider.FakeExec{
		Out: map[string]string{"git remote": "git@github.com:acme/widgets.git\n"},
		Errs: map[string]string{"git push": "To github.com:acme/widgets.git\n" +
			" ! [rejected]        " + landTestBranch + " -> " + landTestBranch + " (non-fast-forward)\n" +
			"error: failed to push some refs " + landTestToken + "\n"},
	})

	resp, body := f.land(t, `{"action":"pr"}`)
	assert422(t, resp, body, codeRemoteDiverged, "git fetch origin && git rebase origin/"+landTestBranch)
	assertRedactedDetail(t, body)
	pushes := 0
	for _, c := range fake.Calls {
		if strings.HasPrefix(c, "git push") {
			pushes++
		}
		if strings.Contains(c, "--force") || strings.Contains(c, " -f ") || strings.Contains(c, "+"+landTestBranch) {
			t.Errorf("forced push attempted: %q", c)
		}
	}
	if pushes != 1 {
		t.Errorf("push attempts = %d, want exactly 1 (no retry); calls = %v", pushes, fake.Calls)
	}
	if fake.Ran("gh pr") {
		t.Error("opened a PR for a branch that was never pushed")
	}
}

// TestLandPhaseGitLabNotSupportedYet pins the temporary refusal for GitLab
// origins (same contract as TestLandGitLabNotSupportedYet on the board); the
// GitLab provider phase replaces it.
func TestLandPhaseGitLabNotSupportedYet(t *testing.T) {
	f := newPhaseLandingFixture(t, "done")
	fake := useFakePhaseLand(t, &repoprovider.FakeExec{Out: map[string]string{
		"git remote": "git@gitlab.com:acme/widgets.git\n",
	}})

	resp, body := f.land(t, `{"action":"pr"}`)
	assert422(t, resp, body, codeGitLabUnsupported, "glab mr create --source-branch "+landTestBranch)
	if body["error"] != "gitlab not supported yet" {
		t.Errorf("error = %v, want %q", body["error"], "gitlab not supported yet")
	}
	if fake.Ran("git push") {
		t.Error("pushed to a host this phase cannot open a change request on")
	}
}

// failingProbe fails the test if the provider factory ever reaches for the
// network GitLab probe.
type failingProbe struct{ t *testing.T }

func (p failingProbe) IsGitLab(_ context.Context, host string) bool {
	p.t.Errorf("GitLab probe called for %q — the project's vcs.provider should have decided", host)
	return false
}

// A multi-repo plan's phase runs in a sub-repo (RunRoot ≠ project path) whose own
// .claude/ does not declare the provider; the PROJECT's .claude/project.json does.
// Land must classify the self-hosted origin from the project's config — never from
// the run repo's — so it lands as GitHub without a network probe.
func TestLandPhaseMultiRepoUsesProjectVcsConfig(t *testing.T) {
	f := newPhaseLandingFixture(t, "done")
	project := t.TempDir()
	writeClaudeFile(t, project, "project.json", `{"vcs":{"provider":"github"}}`)
	if _, err := f.db.Exec(`UPDATE projects SET path = ? WHERE id = 1`, project); err != nil {
		t.Fatal(err)
	}
	prev := phaserunSvc
	phaserunSvc = &phaserun.Service{DB: f.db, RepoRoot: func(projectPath string, _ ...string) (string, error) {
		if projectPath != project {
			t.Errorf("RepoRoot projectPath = %q, want %q", projectPath, project)
		}
		return f.repo, nil
	}}
	t.Cleanup(func() { phaserunSvc = prev })

	const prURL = "https://git.corp.example/acme/widgets/pull/9"
	fake := &repoprovider.FakeExec{Out: map[string]string{
		"git remote": "https://git.corp.example/acme/widgets.git\n",
		"gh pr":      "Creating pull request…\n" + prURL + "\n",
	}}
	useFakePhaseLand(t, fake)
	landProvider = newLandProvider(fake, failingProbe{t}, credstore.Env)

	resp, body := f.land(t, `{"action":"pr"}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %v)", resp.StatusCode, body)
	}
	if l := landingOf(t, body); l["state"] != landingPROpen || l["prProvider"] != "github" || l["prUrl"] != prURL {
		t.Errorf("landing = %v, want pr_open github %s", l, prURL)
	}
	if !fake.Ran("gh pr create --head " + landTestBranch) {
		t.Errorf("GitHub change request not opened; calls = %v", fake.Calls)
	}
	for i, d := range fake.Dirs {
		if d != f.repo && d != "" {
			t.Errorf("call %q ran in %q, want the run repo %q", fake.Calls[i], d, f.repo)
		}
	}

	// The review reads the same project config: GitHub vocabulary, no probe.
	resp, body = f.review(t)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("review status = %d, want 200 (body %v)", resp.StatusCode, body)
	}
	if terms, _ := body["terms"].(map[string]any); terms["provider"] != "GitHub" {
		t.Errorf("review terms = %v, want GitHub", terms)
	}
}

func TestPhaseDisplayNameStripsPhaseLead(t *testing.T) {
	for in, want := range map[string]string{
		"Phase 2 — Line-item CRUD": "Line-item CRUD",
		"phase 10: Totals":         "Totals",
		"Line-item CRUD":           "Line-item CRUD",
		"Phase 3 — ":               "Phase 3 —",
	} {
		if got := phaseDisplayName(in); got != want {
			t.Errorf("phaseDisplayName(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestPlanRelPath(t *testing.T) {
	cases := []struct {
		t    landingTarget
		want string
	}{
		{landingTarget{WorkspaceRoot: "/ws/p", DocPath: "/ws/p/workspace/working/2026/10/09/x/plan/phase-1.md"},
			"working/2026/10/09/x/plan/phase-1.md"},
		{landingTarget{PlanDir: "/tmp/x/plan", DocPath: "/tmp/x/plan/phase-1.md"}, "x/plan/phase-1.md"},
		{landingTarget{DocPath: "/a/b/phase-1.md"}, "phase-1.md"},
	}
	for _, c := range cases {
		if got := planRelPath(c.t); got != c.want {
			t.Errorf("planRelPath(%+v) = %q, want %q", c.t, got, c.want)
		}
	}
}

// ── land: return to agent ────────────────────────────────────────────────────

const landTestFeedback = "DELETE ignores soft-deleted rows — filter them before the 404."

// attachReturnRun wires a stub-backed phase-run service whose runs stay in flight
// until cleanup, so a test can observe the `returned` stamp before the run's end
// resets it. Cleanup releases the run and waits for its slot, so nothing outlives
// the fixture's DB and temp dirs.
func attachReturnRun(t *testing.T, f *phaseLandingFixture) (*phaseStubRunner, *phaserun.Service) {
	t.Helper()
	r := &phaseStubRunner{block: make(chan struct{})}
	svc := attachPhaseRun(t, f.db, r, false)
	t.Cleanup(func() {
		close(r.block)
		deadline := time.Now().Add(5 * time.Second)
		for svc.Slots.IsActive(runcore.SlotKey(phaserun.Engine, f.phaseID)) && time.Now().Before(deadline) {
			time.Sleep(5 * time.Millisecond)
		}
	})
	return r, svc
}

// waitSpecs waits until the stub runner has been handed n specs.
func waitSpecs(t *testing.T, r *phaseStubRunner, n int) []phaserun.RunSpec {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		r.mu.Lock()
		got := append([]phaserun.RunSpec(nil), r.specs...)
		r.mu.Unlock()
		if len(got) >= n {
			return got
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("runner never received %d spec(s)", n)
	return nil
}

func specCount(r *phaseStubRunner) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.specs)
}

func readDoc(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// TestLandPhaseReturnAppendsFeedbackAndRestarts: the note lands in the phase doc
// above its report, the phase is stamped returned (clearing a stale landing
// error), and its run restarts with Returned:true — no code-host tool is touched.
func TestLandPhaseReturnAppendsFeedbackAndRestarts(t *testing.T) {
	f := newPhaseLandingFixture(t, "done")
	fake := useFakePhaseLand(t, phaseLandOK())
	r, _ := attachReturnRun(t, f)
	if _, err := f.db.Exec(`UPDATE epic_phases SET landing_error = 'not-authenticated: stale' WHERE id = ?`, f.phaseID); err != nil {
		t.Fatal(err)
	}

	resp, body := f.land(t, `{"action":"return","feedback":"  `+landTestFeedback+`  "}`)
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("status = %d, want 202 (body %v)", resp.StatusCode, body)
	}
	if body["status"] != "running" || body["action"] != landActionReturn || body["sessionUuid"] != "phase-uuid-1" {
		t.Errorf("body = %v, want status running, action return, the run's session", body)
	}
	if l := landingOf(t, body); l["state"] != landingReturned || l["error"] != nil {
		t.Errorf("landing = %v, want returned with no error", l)
	}

	specs := waitSpecs(t, r, 1)
	if !specs[0].Returned {
		t.Error("the run was started without Returned:true")
	}
	if n := strings.Count(specs[0].Prompt, phaserun.ReturnedNote); n != 1 {
		t.Errorf("the run's prompt carries the returned sentence %d times, want 1", n)
	}

	doc := readDoc(t, f.docPath)
	fb := strings.Index(doc, "## Operator feedback (")
	note := strings.Index(doc, landTestFeedback+"\n")
	report := strings.Index(doc, "## Completion Report")
	if fb < 0 || note < fb || report < note {
		t.Errorf("doc does not carry the feedback section above the report:\n%s", doc)
	}
	// The run's prompt inlines the doc it read — the one carrying the note.
	if !strings.Contains(specs[0].Prompt, landTestFeedback) {
		t.Error("the restarted run's prompt does not carry the feedback")
	}
	if got := phaseCol(t, f.db, f.phaseID, "landing_state"); got != landingReturned {
		t.Errorf("landing_state = %q, want returned", got)
	}
	if got := phaseCol(t, f.db, f.phaseID, "landing_error"); got != "" {
		t.Errorf("landing_error = %q, want cleared", got)
	}
	if len(fake.Calls) != 0 {
		t.Errorf("a return ran code-host tools: %v", fake.Calls)
	}
}

// TestLandPhaseReturnRequiresFeedback: no note (absent, blank, or over the cap) is
// a 400 that writes nothing and starts nothing.
func TestLandPhaseReturnRequiresFeedback(t *testing.T) {
	f := newPhaseLandingFixture(t, "done")
	r, _ := attachReturnRun(t, f)

	big := strings.Repeat("x", 20<<10+1)
	for _, b := range []string{
		`{"action":"return"}`,
		`{"action":"return","feedback":"  \n\t "}`,
		`{"action":"return","feedback":"` + big + `"}`,
	} {
		resp, body := f.land(t, b)
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("body %.60q: status = %d, want 400 (%v)", b, resp.StatusCode, body)
		}
	}
	if got := readDoc(t, f.docPath); got != landTestDoc {
		t.Errorf("a refused return changed the doc:\n%s", got)
	}
	if got := phaseCol(t, f.db, f.phaseID, "landing_state"); got != landingNone {
		t.Errorf("landing_state = %q, want none", got)
	}
	if n := specCount(r); n != 0 {
		t.Errorf("a refused return started %d run(s)", n)
	}
}

// TestLandPhaseReturnWhileRunning409: a running phase cannot be returned.
func TestLandPhaseReturnWhileRunning409(t *testing.T) {
	f := newPhaseLandingFixture(t, "running")
	r, _ := attachReturnRun(t, f)

	resp, body := f.land(t, `{"action":"return","feedback":"`+landTestFeedback+`"}`)
	if resp.StatusCode != http.StatusConflict || body["code"] != codePhaseRunning {
		t.Fatalf("status/code = %d/%v, want 409 %s", resp.StatusCode, body["code"], codePhaseRunning)
	}
	if got := readDoc(t, f.docPath); got != landTestDoc {
		t.Errorf("a refused return changed the doc:\n%s", got)
	}
	if got := phaseCol(t, f.db, f.phaseID, "landing_state"); got != landingNone {
		t.Errorf("landing_state = %q, want none", got)
	}
	if n := specCount(r); n != 0 {
		t.Errorf("a refused return started %d run(s)", n)
	}
}

// TestLandPhaseReturnStartRefusedKeepsFeedback: the run cannot start (the run
// budget is full). The refusal is runPhase's body plus the landing — still
// returned — and the operator's note stays in the doc.
func TestLandPhaseReturnStartRefusedKeepsFeedback(t *testing.T) {
	f := newPhaseLandingFixture(t, "done")
	r, svc := attachReturnRun(t, f)
	svc.Slots = runcore.NewSlots(1)
	if _, err := svc.Slots.TryAcquire(runcore.SlotKey("planrun", 77), "u-plan", nil); err != nil {
		t.Fatal(err)
	}

	resp, body := f.land(t, `{"action":"return","feedback":"`+landTestFeedback+`"}`)
	if resp.StatusCode != http.StatusConflict || body["code"] != codeNoRunSlot {
		t.Fatalf("status/code = %d/%v, want 409 %s (body %v)", resp.StatusCode, body["code"], codeNoRunSlot, body)
	}
	if holders, _ := body["holders"].([]any); len(holders) != 1 {
		t.Errorf("holders = %v, want the plan run named", body["holders"])
	}
	if l := landingOf(t, body); l["state"] != landingReturned {
		t.Errorf("landing = %v, want returned", l)
	}
	if !strings.Contains(readDoc(t, f.docPath), landTestFeedback) {
		t.Error("the operator's note was dropped when the run could not start")
	}
	if got := phaseCol(t, f.db, f.phaseID, "landing_state"); got != landingReturned {
		t.Errorf("landing_state = %q, want returned", got)
	}
	if n := specCount(r); n != 0 {
		t.Errorf("a refused start spawned %d run(s)", n)
	}

	// The budget frees up; the operator presses the plain Run button. That start is
	// the returned run the feedback asked for.
	svc.Slots.Release(runcore.SlotKey("planrun", 77))
	resp, body = landingJSON(t, http.MethodPost, f.url(f.phaseID, "run"), "")
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("plain run of a returned phase: status = %d, want 202 (body %v)", resp.StatusCode, body)
	}
	specs := waitSpecs(t, r, 1)
	if !specs[0].Returned {
		t.Error("the plain run of a returned phase was started without Returned")
	}
	if n := strings.Count(specs[0].Prompt, phaserun.ReturnedNote); n != 1 {
		t.Errorf("its prompt carries the returned sentence %d times, want 1", n)
	}
}

// TestLandPhaseReturnWhileSlotHeld409: run_state already says done, but the
// service still holds the phase's slot (the run's teardown — doc return,
// verification, worktree removal — is not over). A return now would race that
// teardown's doc copy-back, so it is refused 409 phase-running before anything is
// written.
func TestLandPhaseReturnWhileSlotHeld409(t *testing.T) {
	f := newPhaseLandingFixture(t, "done")
	r, svc := attachReturnRun(t, f)
	key := runcore.SlotKey(phaserun.Engine, f.phaseID)
	if _, err := svc.Slots.TryAcquire(key, "u-tearing-down", nil); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { svc.Slots.Release(key) })

	resp, body := f.land(t, `{"action":"return","feedback":"`+landTestFeedback+`"}`)
	if resp.StatusCode != http.StatusConflict || body["code"] != codePhaseRunning {
		t.Fatalf("status/code = %d/%v, want 409 %s (body %v)", resp.StatusCode, body["code"], codePhaseRunning, body)
	}
	if got := readDoc(t, f.docPath); got != landTestDoc {
		t.Errorf("a refused return changed the doc:\n%s", got)
	}
	if got := phaseCol(t, f.db, f.phaseID, "landing_state"); got != landingNone {
		t.Errorf("landing_state = %q, want none", got)
	}
	if n := specCount(r); n != 0 {
		t.Errorf("a refused return started %d run(s)", n)
	}
}

// TestLandPhaseReturnWithoutPhaseRuns503: no phase-run service ⇒ 503 before the
// doc or the row is touched.
func TestLandPhaseReturnWithoutPhaseRuns503(t *testing.T) {
	f := newPhaseLandingFixture(t, "done") // leaves phaserunSvc nil

	resp, _ := f.land(t, `{"action":"return","feedback":"`+landTestFeedback+`"}`)
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", resp.StatusCode)
	}
	if got := readDoc(t, f.docPath); got != landTestDoc {
		t.Errorf("an unattached return changed the doc:\n%s", got)
	}
	if got := phaseCol(t, f.db, f.phaseID, "landing_state"); got != landingNone {
		t.Errorf("landing_state = %q, want none", got)
	}
}

// waitSlotFree waits until the phase-run service has released the phase's slot —
// the run's teardown is over.
func waitSlotFree(t *testing.T, svc *phaserun.Service, phaseID int64) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for svc.Slots.IsActive(runcore.SlotKey(phaserun.Engine, phaseID)) {
		if time.Now().After(deadline) {
			t.Fatal("the phase's run slot was never released")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// TestLandPhaseReturnPROpenRestoredAfterRun: returning a phase whose change
// request is open keeps the PR on the DTO while the phase is returned, and once
// the returned run ends the phase is pr_open again with pr_url intact — so the
// next `land pr` does not try to open a second change request.
func TestLandPhaseReturnPROpenRestoredAfterRun(t *testing.T) {
	f := newPhaseLandingFixture(t, "done")
	useFakePhaseLand(t, phaseLandOK())
	if _, err := f.db.Exec(`UPDATE epic_phases SET landing_state = 'pr_open', pr_url = ?, pr_number = 77,
		pr_provider = 'github' WHERE id = ?`, landTestPRURL, f.phaseID); err != nil {
		t.Fatal(err)
	}
	r := &phaseStubRunner{block: make(chan struct{})}
	svc := attachPhaseRun(t, f.db, r, false)
	released := false
	t.Cleanup(func() {
		if !released {
			close(r.block)
		}
		waitSlotFree(t, svc, f.phaseID)
	})

	resp, body := f.land(t, `{"action":"return","feedback":"`+landTestFeedback+`"}`)
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("status = %d, want 202 (body %v)", resp.StatusCode, body)
	}
	l := landingOf(t, body)
	if l["state"] != landingReturned || l["prUrl"] != landTestPRURL || l["prNumber"] != float64(77) {
		t.Errorf("landing while returned = %v, want returned with prUrl/prNumber kept", l)
	}

	waitSpecs(t, r, 1)
	close(r.block)
	released = true
	waitSlotFree(t, svc, f.phaseID)
	if got := phaseCol(t, f.db, f.phaseID, "landing_state"); got != landingPROpen {
		t.Errorf("landing_state after the returned run = %q, want pr_open", got)
	}
	if got := phaseCol(t, f.db, f.phaseID, "pr_url"); got != landTestPRURL {
		t.Errorf("pr_url = %q, want %q kept", got, landTestPRURL)
	}
}

// TestLandPhaseReturnMergedIs409: a merged phase cannot be returned — its work is
// already in the base branch. 409 phase-merged; the doc, the row and the run are
// untouched.
func TestLandPhaseReturnMergedIs409(t *testing.T) {
	f := newPhaseLandingFixture(t, "done")
	r, _ := attachReturnRun(t, f)
	if _, err := f.db.Exec(`UPDATE epic_phases SET landing_state = 'merged', pr_url = ? WHERE id = ?`,
		landTestPRURL, f.phaseID); err != nil {
		t.Fatal(err)
	}

	resp, body := f.land(t, `{"action":"return","feedback":"`+landTestFeedback+`"}`)
	if resp.StatusCode != http.StatusConflict || body["code"] != codePhaseMerged {
		t.Fatalf("status/code = %d/%v, want 409 %s (body %v)", resp.StatusCode, body["code"], codePhaseMerged, body)
	}
	if got := readDoc(t, f.docPath); got != landTestDoc {
		t.Errorf("a refused return changed the doc:\n%s", got)
	}
	if got := phaseCol(t, f.db, f.phaseID, "landing_state"); got != landingMerged {
		t.Errorf("landing_state = %q, want merged kept", got)
	}
	if n := specCount(r); n != 0 {
		t.Errorf("a refused return started %d run(s)", n)
	}
}

// TestLandPhasePushFromReturnedKeepsOpenPR: a phase returned with its change
// request open and then pushed (its returned run never started) is pr_open after
// the push — the push updated that change request — not `pushed`.
func TestLandPhasePushFromReturnedKeepsOpenPR(t *testing.T) {
	f := newPhaseLandingFixture(t, "done")
	useFakePhaseLand(t, phaseLandOK())
	if _, err := f.db.Exec(`UPDATE epic_phases SET landing_state = 'returned', pr_url = ?, pr_number = 77
		WHERE id = ?`, landTestPRURL, f.phaseID); err != nil {
		t.Fatal(err)
	}
	resp, body := f.land(t, `{"action":"push"}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %v)", resp.StatusCode, body)
	}
	if got := phaseCol(t, f.db, f.phaseID, "landing_state"); got != landingPROpen {
		t.Errorf("landing_state = %q, want pr_open", got)
	}
}
