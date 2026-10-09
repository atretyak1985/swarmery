package api

// Tests for the board review exits (tasks_review.go): rerun, discard, land.
//
// Unlike the diff endpoint (tasks_diff_test.go, real temp repos), these fake the
// exec boundary: `git push` and `gh pr create` reach a network and a GitHub
// account, and a test suite that touches either is a test suite that fails on a
// plane. The fake (repoprovider.FakeExec behind useFakeLand) is scripted per
// command so each failure mode — no origin, no gh, gh erroring — is driven
// deliberately rather than hoped for.

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

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/dispatch"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/repoprovider"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/repoprovider/credstore"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/taskdir"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/worktree"
)

// noGitLabProbe answers "not GitLab" for every host, so no land test can ever
// reach the network through repoprovider's HTTP probe of an unknown host.
type noGitLabProbe struct{}

func (noGitLabProbe) IsGitLab(context.Context, string) bool { return false }

// useFakeLand swaps the land path's provider factory for the production
// composition (Detect → github.New over credstore.Env) driven by a scripted
// repoprovider.FakeExec, restoring it on cleanup. Commands are keyed by binary +
// first argument ("git remote", "git push", "gh pr"), which is granular enough
// to distinguish every step of the land sequence without the tests hard-coding
// whole argv lines. SWARMERY_SECRETS_DIR points at an empty temp dir, so
// credstore.Env finds no token and never reads the operator's real store.
func useFakeLand(t *testing.T, f *repoprovider.FakeExec) *repoprovider.FakeExec {
	t.Helper()
	t.Setenv("SWARMERY_SECRETS_DIR", t.TempDir())
	prev := landProvider
	landProvider = newLandProvider(f, noGitLabProbe{}, credstore.Env)
	t.Cleanup(func() { landProvider = prev })
	return f
}

// landOK is the fake for a land that succeeds end to end.
func landOK(prURL string) *repoprovider.FakeExec {
	return &repoprovider.FakeExec{Out: map[string]string{
		"git remote": "git@github.com:acme/widgets.git\n",
		"gh pr":      "Creating pull request…\n" + prURL + "\n",
	}}
}

// reviewStubWt is a recording WorktreeManager: discard's whole contract is which
// branch it deletes and whether it reclaimed first, so both are observable.
type reviewStubWt struct {
	deletedBranches []string
	removes         int
	existed         bool
	deleteErr       error
}

func (w *reviewStubWt) Acquire(repoRoot, projectSlug, taskID string) (worktree.Acquired, error) {
	return worktree.Acquired{}, nil
}

func (w *reviewStubWt) Remove(repoRoot string, a worktree.Acquired, keepBranch bool) error {
	w.removes++
	return nil
}

func (w *reviewStubWt) Path(projectSlug, taskID string) (string, error) {
	return "/wt/" + projectSlug + "/" + taskID, nil
}
func (w *reviewStubWt) ReclaimEmptyBranch(repoRoot, branch string) (int, error) { return 0, nil }

func (w *reviewStubWt) DeleteBranch(repoRoot, branch string) (bool, error) {
	w.deletedBranches = append(w.deletedBranches, branch)
	return w.existed, w.deleteErr
}

func (w *reviewStubWt) CommitsForTask(repoRoot, taskID string) ([]string, error) { return nil, nil }

// attachReviewDispatch wires a dispatcher whose admission is OFF: these tests
// assert board state and worktree calls, and a scheduling pass that spawned a
// real headless run would be both slow and wrong.
func attachReviewDispatch(t *testing.T, db *sql.DB, wt dispatch.WorktreeManager) {
	t.Helper()
	svc := dispatch.NewService(db, dispatch.Config{
		MaxConcurrent: 1, MaxWorktrees: 1,
		PollInterval: time.Hour, RunTimeout: time.Minute, Enabled: false,
	}, dispatch.ClaudeRunner{}, wt)
	prev := dispatchSvc
	AttachDispatch(svc)
	t.Cleanup(func() { dispatchSvc = prev })
}

// postReview posts a review action and returns the response with its body.
func postReview(t *testing.T, srvURL string, id int64, action, body string) (*http.Response, map[string]any) {
	t.Helper()
	resp, err := http.Post(
		fmt.Sprintf("%s/api/board/tasks/%d/%s", srvURL, id, action),
		"application/json", bytes.NewReader([]byte(body)))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var out map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decode %s body: %v", action, err)
	}
	return resp, out
}

// taskRow reads one column off a task row.
func taskRow(t *testing.T, db *sql.DB, id int64, col string) string {
	t.Helper()
	var v sql.NullString
	if err := db.QueryRow(`SELECT `+col+` FROM tasks WHERE id=?`, id).Scan(&v); err != nil {
		t.Fatalf("read %s: %v", col, err)
	}
	return v.String
}

// ── rerun ────────────────────────────────────────────────────────────────────

func TestRerunAppendsFeedbackAndRequeues(t *testing.T) {
	repo, base := reviewRepo(t, "swarm/T-rerun1")
	srv, db := reviewServer(t, repo)
	id := seedReviewCard(t, db, "T-rerun1", reviewCard{
		Branch: "swarm/T-rerun1", StartPoint: base, Prompt: "original instructions",
		Verdict: "fail", Detail: "tests red",
	})

	resp, _ := postReview(t, srv.URL, id, "rerun", `{"feedback":"the retry loop is still unbounded"}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}

	d, err := (&Handler{DB: db}).boardTaskByID(id)
	if err != nil || d == nil {
		t.Fatalf("reload: %v", err)
	}
	if d.BoardColumn != "todo" || d.Status != "queued" {
		t.Errorf("column/status = %s/%s, want todo/queued", d.BoardColumn, d.Status)
	}
	// The verdict describes the run being re-done; carrying it forward would grade
	// the new attempt with the old attempt's result.
	if d.VerifyVerdict != nil || d.VerifyDetail != nil {
		t.Errorf("verdict not cleared: %v / %v", d.VerifyVerdict, d.VerifyDetail)
	}
	if !strings.Contains(d.Prompt, "original instructions") {
		t.Error("rerun dropped the original prompt")
	}
	if !strings.Contains(d.Prompt, "## Reviewer feedback (") {
		t.Errorf("prompt has no feedback heading:\n%s", d.Prompt)
	}
	if !strings.Contains(d.Prompt, "the retry loop is still unbounded") {
		t.Errorf("prompt has no feedback body:\n%s", d.Prompt)
	}
	// The heading is timestamped so a card re-run twice reads as two rounds, not
	// one undated blob.
	if !strings.Contains(d.Prompt, time.Now().UTC().Format("2006-01-02")) {
		t.Errorf("feedback heading is not timestamped:\n%s", d.Prompt)
	}
	if d.ColumnMovedAt == nil {
		t.Error("columnMovedAt not stamped by the requeue")
	}
}

func TestRerunFromDoneIsAllowed(t *testing.T) {
	// The point of the endpoint: before it, done→in_progress was refused outright
	// and done→todo was an unobvious drag, so a shipped-but-wrong card had no
	// legal way back into the queue.
	repo, base := reviewRepo(t, "swarm/T-redone")
	srv, db := reviewServer(t, repo)
	id := seedReviewCard(t, db, "T-redone", reviewCard{
		Branch: "swarm/T-redone", StartPoint: base, BoardColumn: "done", Status: "done",
	})

	resp, _ := postReview(t, srv.URL, id, "rerun", `{"feedback":"reopen: the fix regressed"}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if col := taskRow(t, db, id, "board_column"); col != "todo" {
		t.Errorf("column = %s, want todo", col)
	}
}

func TestRerunOutsideReviewOrDoneIs409(t *testing.T) {
	repo, _ := reviewRepo(t, "swarm/T-early")
	srv, db := reviewServer(t, repo)
	for _, col := range []string{"triage", "todo", "in_progress", "archived"} {
		id := seedReviewCard(t, db, "T-e"+col[:3], reviewCard{BoardColumn: col})
		resp, body := postReview(t, srv.URL, id, "rerun", `{"feedback":"nope"}`)
		if resp.StatusCode != http.StatusConflict {
			t.Errorf("rerun from %s = %d, want 409", col, resp.StatusCode)
			continue
		}
		if body["code"] != codeBadColumn {
			t.Errorf("rerun from %s: code = %v, want %q", col, body["code"], codeBadColumn)
		}
	}
}

func TestRerunRequiresFeedback(t *testing.T) {
	repo, _ := reviewRepo(t, "swarm/T-nofb")
	srv, db := reviewServer(t, repo)
	id := seedReviewCard(t, db, "T-nofb", reviewCard{Branch: "swarm/T-nofb"})
	for _, body := range []string{`{}`, `{"feedback":""}`, `{"feedback":"   \n "}`} {
		resp, _ := postReview(t, srv.URL, id, "rerun", body)
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("rerun %s = %d, want 400", body, resp.StatusCode)
		}
	}
	// A blank re-run must not have moved the card.
	if col := taskRow(t, db, id, "board_column"); col != "in_review" {
		t.Errorf("column = %s, want in_review (unchanged)", col)
	}
}

func TestRerunUnknownTaskIs404(t *testing.T) {
	repo, _ := reviewRepo(t, "swarm/T-none")
	srv, _ := reviewServer(t, repo)
	resp, _ := postReview(t, srv.URL, 4242, "rerun", `{"feedback":"x"}`)
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("status = %d, want 404", resp.StatusCode)
	}
}

// ── discard ──────────────────────────────────────────────────────────────────

func TestDiscardDeletesBranchAndArchives(t *testing.T) {
	const branch = "swarm/T-drop11"
	repo, base := reviewRepo(t, branch)
	srv, db := reviewServer(t, repo)
	wt := &reviewStubWt{existed: true}
	attachReviewDispatch(t, db, wt)
	id := seedReviewCard(t, db, "T-drop11", reviewCard{
		Branch: branch, StartPoint: base, Worktree: "/tmp/wt/T-drop11",
	})

	resp, body := postReview(t, srv.URL, id, "discard", `{}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %v)", resp.StatusCode, body)
	}
	if body["deleted"] != true || body["branch"] != branch {
		t.Errorf("body = %v, want deleted=true branch=%s", body, branch)
	}
	if len(wt.deletedBranches) != 1 || wt.deletedBranches[0] != branch {
		t.Errorf("deleted branches = %v, want [%s]", wt.deletedBranches, branch)
	}
	// The worktree must be reclaimed BEFORE the delete — a checked-out branch
	// cannot be deleted at all.
	if wt.removes == 0 {
		t.Error("worktree was not reclaimed")
	}
	if col := taskRow(t, db, id, "board_column"); col != "archived" {
		t.Errorf("column = %s, want archived", col)
	}
	if st := taskRow(t, db, id, "status"); st != "cancelled" {
		t.Errorf("status = %s, want cancelled", st)
	}
	if wtp := taskRow(t, db, id, "worktree_path"); wtp != "" {
		t.Errorf("worktree_path = %q, want cleared", wtp)
	}
}

func TestDiscardIsIdempotentWhenBranchAlreadyGone(t *testing.T) {
	const branch = "swarm/T-drop22"
	repo, base := reviewRepo(t, branch)
	srv, db := reviewServer(t, repo)
	wt := &reviewStubWt{existed: false} // nothing was there to delete
	attachReviewDispatch(t, db, wt)
	id := seedReviewCard(t, db, "T-drop22", reviewCard{Branch: branch, StartPoint: base})

	resp, body := postReview(t, srv.URL, id, "discard", `{}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	// Still a success — but honest about having deleted nothing.
	if body["deleted"] != false {
		t.Errorf("deleted = %v, want false", body["deleted"])
	}
	if col := taskRow(t, db, id, "board_column"); col != "archived" {
		t.Errorf("column = %s, want archived", col)
	}
}

func TestDiscardRefusesRunningCard(t *testing.T) {
	const branch = "swarm/T-live33"
	repo, base := reviewRepo(t, branch)
	srv, db := reviewServer(t, repo)
	wt := &reviewStubWt{existed: true}
	attachReviewDispatch(t, db, wt)
	id := seedReviewCard(t, db, "T-live33", reviewCard{
		Branch: branch, StartPoint: base, BoardColumn: "in_progress", Status: "running",
	})

	resp, body := postReview(t, srv.URL, id, "discard", `{}`)
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("status = %d, want 409", resp.StatusCode)
	}
	if body["code"] != codeAlreadyRunning {
		t.Errorf("code = %v, want %q", body["code"], codeAlreadyRunning)
	}
	if len(wt.deletedBranches) != 0 {
		t.Errorf("a running card's branch was deleted: %v", wt.deletedBranches)
	}
	if col := taskRow(t, db, id, "board_column"); col != "in_progress" {
		t.Errorf("column = %s, want in_progress (unchanged)", col)
	}
}

func TestDiscardSurfacesWorktreeConflict(t *testing.T) {
	const branch = "swarm/T-held44"
	repo, base := reviewRepo(t, branch)
	srv, db := reviewServer(t, repo)
	wt := &reviewStubWt{deleteErr: worktree.ErrBranchCheckedOut}
	attachReviewDispatch(t, db, wt)
	id := seedReviewCard(t, db, "T-held44", reviewCard{Branch: branch, StartPoint: base})

	resp, body := postReview(t, srv.URL, id, "discard", `{}`)
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("status = %d, want 409", resp.StatusCode)
	}
	if body["code"] != codeBranchCheckedOut {
		t.Errorf("code = %v, want %q", body["code"], codeBranchCheckedOut)
	}
	// A refused delete must NOT archive the card — otherwise the user loses the
	// handle to a branch that quietly survived.
	if col := taskRow(t, db, id, "board_column"); col != "in_review" {
		t.Errorf("column = %s, want in_review (unchanged)", col)
	}
}

// ── land ─────────────────────────────────────────────────────────────────────

func TestLandPushesCreatesPRAndFinishes(t *testing.T) {
	const branch = "swarm/T-land55"
	const prURL = "https://github.com/acme/widgets/pull/912"
	repo, base := reviewRepo(t, branch)
	srv, db := reviewServer(t, repo)
	wt := &reviewStubWt{existed: true}
	attachReviewDispatch(t, db, wt)
	fake := useFakeLand(t, landOK(prURL))
	id := seedReviewCard(t, db, "T-land55", reviewCard{
		Branch: branch, StartPoint: base, Worktree: "/tmp/wt/T-land55", Verdict: "pass",
	})

	resp, body := postReview(t, srv.URL, id, "land", `{"draft":false}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %v)", resp.StatusCode, body)
	}
	if body["prUrl"] != prURL {
		t.Errorf("prUrl = %v, want %s", body["prUrl"], prURL)
	}
	if !fake.Ran("git push -u origin " + branch) {
		t.Errorf("branch was not pushed; calls = %v", fake.Calls)
	}
	if !fake.Ran("gh pr create --head " + branch) {
		t.Errorf("PR was not created; calls = %v", fake.Calls)
	}
	if fake.Ran("--draft") {
		t.Error("draft flag sent for a non-draft land")
	}
	// Git must run in the project repo root, not the (reclaimed) worktree.
	for _, d := range fake.Dirs {
		if d != repo {
			t.Errorf("command ran in %q, want the project repo root %q", d, repo)
		}
	}
	if col := taskRow(t, db, id, "board_column"); col != "done" {
		t.Errorf("column = %s, want done", col)
	}
	if note := taskRow(t, db, id, "result_note"); note != prURL {
		t.Errorf("result_note = %q, want the PR URL", note)
	}
	// The →done side effects the PATCH path performs must also run here.
	if wt.removes == 0 {
		t.Error("landing did not reclaim the worktree")
	}
	if len(wt.deletedBranches) != 0 {
		t.Errorf("landing deleted the branch: %v", wt.deletedBranches)
	}
	// resultNote is exposed on the DTO so the card can link the PR it opened.
	d, _ := (&Handler{DB: db}).boardTaskByID(id)
	if d == nil || d.ResultNote == nil || *d.ResultNote != prURL {
		t.Errorf("DTO resultNote = %v, want %s", d.ResultNote, prURL)
	}
}

func TestLandDraftPassesTheFlag(t *testing.T) {
	const branch = "swarm/T-draft6"
	repo, base := reviewRepo(t, branch)
	srv, db := reviewServer(t, repo)
	attachReviewDispatch(t, db, &reviewStubWt{})
	fake := useFakeLand(t, landOK("https://github.com/acme/widgets/pull/1"))
	id := seedReviewCard(t, db, "T-draft6", reviewCard{Branch: branch, StartPoint: base})

	resp, _ := postReview(t, srv.URL, id, "land", `{"draft":true}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if !fake.Ran("--draft") {
		t.Errorf("draft flag not sent; calls = %v", fake.Calls)
	}
}

func TestLandWithoutOriginIs422(t *testing.T) {
	const branch = "swarm/T-noorg7"
	repo, base := reviewRepo(t, branch)
	srv, db := reviewServer(t, repo)
	fake := useFakeLand(t, &repoprovider.FakeExec{Errs: map[string]string{
		"git remote": "error: No such remote 'origin'\n",
	}})
	id := seedReviewCard(t, db, "T-noorg7", reviewCard{Branch: branch, StartPoint: base})

	resp, body := postReview(t, srv.URL, id, "land", `{}`)
	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422", resp.StatusCode)
	}
	if body["error"] != "no origin remote" {
		t.Errorf("error = %v, want %q", body["error"], "no origin remote")
	}
	hint, _ := body["hint"].(string)
	if !strings.Contains(hint, "push") || !strings.Contains(hint, branch) {
		t.Errorf("hint %q does not carry the manual push command", hint)
	}
	// Nothing was pushed, so the card must stay where it was.
	if fake.Ran("git push") {
		t.Error("pushed despite having no origin")
	}
	if col := taskRow(t, db, id, "board_column"); col != "in_review" {
		t.Errorf("column = %s, want in_review (unchanged)", col)
	}
}

func TestLandWithoutGhIs422WithHint(t *testing.T) {
	const branch = "swarm/T-nogh88"
	repo, base := reviewRepo(t, branch)
	srv, db := reviewServer(t, repo)
	fake := useFakeLand(t, &repoprovider.FakeExec{
		Out:     map[string]string{"git remote": "git@github.com:acme/widgets.git\n"},
		Missing: map[string]bool{"gh": true},
	})
	id := seedReviewCard(t, db, "T-nogh88", reviewCard{
		Branch: branch, StartPoint: base, Prompt: "some work",
	})

	resp, body := postReview(t, srv.URL, id, "land", `{}`)
	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422", resp.StatusCode)
	}
	hint, _ := body["hint"].(string)
	if !strings.Contains(hint, "gh pr create") || !strings.Contains(hint, branch) {
		t.Errorf("hint %q does not carry the exact gh command", hint)
	}
	// The push DID happen — the hint has to describe only the step that is left.
	if !fake.Ran("git push") {
		t.Error("expected the push to have run before the gh check")
	}
	if !strings.Contains(hint, "pushed") {
		t.Errorf("hint %q does not tell the user the branch is already pushed", hint)
	}
	// No PR ⇒ not done. A card marked done with no PR is the outcome this
	// endpoint must never produce.
	if col := taskRow(t, db, id, "board_column"); col != "in_review" {
		t.Errorf("column = %s, want in_review", col)
	}
	if note := taskRow(t, db, id, "result_note"); note != "" {
		t.Errorf("result_note = %q, want empty", note)
	}
}

func TestLandWhenGhFailsIs422(t *testing.T) {
	const branch = "swarm/T-ghbad9"
	repo, base := reviewRepo(t, branch)
	srv, db := reviewServer(t, repo)
	// The remote must be a real URL: Detect parses `git remote get-url origin`,
	// and a bare "origin" reads as no remote at all.
	useFakeLand(t, &repoprovider.FakeExec{
		Out:  map[string]string{"git remote": "https://github.com/o/r.git\n"},
		Errs: map[string]string{"gh pr": "pull request already exists for branch\n"},
	})
	id := seedReviewCard(t, db, "T-ghbad9", reviewCard{Branch: branch, StartPoint: base})

	resp, body := postReview(t, srv.URL, id, "land", `{}`)
	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422", resp.StatusCode)
	}
	detail, _ := body["detail"].(string)
	if !strings.Contains(detail, "already exists") {
		t.Errorf("detail %q does not carry gh's stderr", detail)
	}
	if col := taskRow(t, db, id, "board_column"); col != "in_review" {
		t.Errorf("column = %s, want in_review", col)
	}
}

func TestLandWithoutBranchIs409(t *testing.T) {
	repo, base := reviewRepo(t, "swarm/T-unused2")
	srv, db := reviewServer(t, repo)
	useFakeLand(t, landOK("https://example.invalid/pr/1"))
	id := seedReviewCard(t, db, "T-nobr10", reviewCard{StartPoint: base, BoardColumn: "triage"})

	resp, body := postReview(t, srv.URL, id, "land", `{}`)
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("status = %d, want 409", resp.StatusCode)
	}
	if body["code"] != codeNoRunBranch {
		t.Errorf("code = %v, want %q", body["code"], codeNoRunBranch)
	}
}

func TestLandRefusesRunningCard(t *testing.T) {
	const branch = "swarm/T-busy11"
	repo, base := reviewRepo(t, branch)
	srv, db := reviewServer(t, repo)
	fake := useFakeLand(t, landOK("https://example.invalid/pr/2"))
	id := seedReviewCard(t, db, "T-busy11", reviewCard{
		Branch: branch, StartPoint: base, BoardColumn: "in_progress", Status: "running",
	})

	resp, body := postReview(t, srv.URL, id, "land", `{}`)
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("status = %d, want 409", resp.StatusCode)
	}
	if body["code"] != codeAlreadyRunning {
		t.Errorf("code = %v, want %q", body["code"], codeAlreadyRunning)
	}
	if len(fake.Calls) != 0 {
		t.Errorf("commands ran for a running card: %v", fake.Calls)
	}
}

// TestLandRedactsTokenInDetail: a CLI that echoes a credential (a push URL with
// an embedded token, a verbose auth error) must never get it into the 422 body.
// `detail` is the raw tool output by contract, so it is the field that would
// carry it — and the operator pastes these bodies into bug reports.
func TestLandRedactsTokenInDetail(t *testing.T) {
	const branch = "swarm/T-redact1"
	const token = "ghp_ABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"
	repo, base := reviewRepo(t, branch)
	srv, db := reviewServer(t, repo)
	useFakeLand(t, &repoprovider.FakeExec{
		Out: map[string]string{"git remote": "https://github.com/acme/widgets.git\n"},
		Errs: map[string]string{"git push": "remote: Invalid username or token " + token + "\n" +
			"fatal: unable to access 'https://x-access-token:" + token + "@github.com/acme/widgets.git/'\n"},
	})
	id := seedReviewCard(t, db, "T-redact1", reviewCard{Branch: branch, StartPoint: base})

	resp, body := postReview(t, srv.URL, id, "land", `{}`)
	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422", resp.StatusCode)
	}
	if body["error"] != "push failed" {
		t.Errorf("error = %v, want %q", body["error"], "push failed")
	}
	detail, _ := body["detail"].(string)
	if !strings.Contains(detail, "***") {
		t.Errorf("detail %q carries no redaction mark", detail)
	}
	raw, _ := json.Marshal(body)
	if strings.Contains(string(raw), token) || strings.Contains(string(raw), token[:20]) {
		t.Errorf("the token reached the 422 body: %s", raw)
	}
	if col := taskRow(t, db, id, "board_column"); col != "in_review" {
		t.Errorf("column = %s, want in_review", col)
	}
}

// TestLandGitLabNotSupportedYet pins the TEMPORARY refusal for a GitLab origin:
// the GitLab provider arrives in a later phase, which flips this test. Until
// then nothing may be pushed — a pushed branch with no way to open its merge
// request is a worse state than an honest 422.
func TestLandGitLabNotSupportedYet(t *testing.T) {
	const branch = "swarm/T-gitlab1"
	repo, base := reviewRepo(t, branch)
	srv, db := reviewServer(t, repo)
	fake := useFakeLand(t, &repoprovider.FakeExec{
		Out: map[string]string{"git remote": "git@gitlab.com:acme/widgets.git\n"},
	})
	id := seedReviewCard(t, db, "T-gitlab1", reviewCard{Branch: branch, StartPoint: base})

	resp, body := postReview(t, srv.URL, id, "land", `{}`)
	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422", resp.StatusCode)
	}
	if body["error"] != "gitlab not supported yet" {
		t.Errorf("error = %v, want %q", body["error"], "gitlab not supported yet")
	}
	hint, _ := body["hint"].(string)
	if !strings.Contains(hint, "push") || !strings.Contains(hint, branch) {
		t.Errorf("hint %q does not carry the manual push command", hint)
	}
	if fake.Ran("git push") || fake.Ran("gh ") || fake.Ran("glab ") {
		t.Errorf("a GitLab land ran a push or a CLI; calls = %v", fake.Calls)
	}
	if col := taskRow(t, db, id, "board_column"); col != "in_review" {
		t.Errorf("column = %s, want in_review", col)
	}
}

// TestLandUnknownProviderIs422: an origin on a host that is neither a public
// GitHub/GitLab host nor answers the GitLab probe cannot be landed to until the
// operator names its provider — the hint must say how, and nothing is pushed.
func TestLandUnknownProviderIs422(t *testing.T) {
	const branch = "swarm/T-unknown1"
	repo, base := reviewRepo(t, branch)
	srv, db := reviewServer(t, repo)
	fake := useFakeLand(t, &repoprovider.FakeExec{
		Out: map[string]string{"git remote": "https://git.example.invalid/acme/widgets.git\n"},
	})
	id := seedReviewCard(t, db, "T-unknown1", reviewCard{Branch: branch, StartPoint: base})

	resp, body := postReview(t, srv.URL, id, "land", `{}`)
	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422", resp.StatusCode)
	}
	if body["error"] != "repository provider unknown" {
		t.Errorf("error = %v, want %q", body["error"], "repository provider unknown")
	}
	hint, _ := body["hint"].(string)
	if !strings.Contains(hint, "vcs.provider") || !strings.Contains(hint, branch) {
		t.Errorf("hint %q does not name vcs.provider and the manual commands", hint)
	}
	if fake.Ran("git push") {
		t.Errorf("pushed to an unknown provider; calls = %v", fake.Calls)
	}
}

// ghCalls are the recorded `gh …` invocations.
func ghCalls(f *repoprovider.FakeExec) []string {
	var out []string
	for _, c := range f.Calls {
		if strings.HasPrefix(c, "gh ") {
			out = append(out, c)
		}
	}
	return out
}

// TestLandThroughSSHAliasResolvesHost: an origin on an ssh_config Host alias
// (git@github-work:…) landed before the provider layer — git resolved the alias
// and gh resolved the repo from the remote. It must keep landing: the alias is
// resolved with `ssh -G` for classification and gh's --repo, while the push
// keeps going to the remote NAME so git resolves the alias itself.
func TestLandThroughSSHAliasResolvesHost(t *testing.T) {
	const branch = "swarm/T-alias1"
	const prURL = "https://github.com/acme/widgets/pull/77"
	repo, base := reviewRepo(t, branch)
	srv, db := reviewServer(t, repo)
	attachReviewDispatch(t, db, &reviewStubWt{})
	fake := useFakeLand(t, &repoprovider.FakeExec{Out: map[string]string{
		"git remote": "git@github-work:acme/widgets.git\n",
		"ssh -G":     "user git\nhostname github.com\nport 22\n",
		"gh pr":      prURL + "\n",
	}})
	id := seedReviewCard(t, db, "T-alias1", reviewCard{Branch: branch, StartPoint: base})

	resp, body := postReview(t, srv.URL, id, "land", `{}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %v)", resp.StatusCode, body)
	}
	if body["prUrl"] != prURL {
		t.Errorf("prUrl = %v, want %s", body["prUrl"], prURL)
	}
	if !fake.Ran("ssh -G github-work") {
		t.Errorf("alias was not resolved; calls = %v", fake.Calls)
	}
	if !fake.Ran("git push -u origin " + branch) {
		t.Errorf("push did not go to the remote name; calls = %v", fake.Calls)
	}
	gh := ghCalls(fake)
	if len(gh) == 0 {
		t.Fatalf("gh never ran; calls = %v", fake.Calls)
	}
	for _, c := range gh {
		if strings.Contains(c, "github-work") {
			t.Errorf("gh got the unresolved alias: %q", c)
		}
		if !strings.Contains(c, "--repo acme/widgets") {
			t.Errorf("gh call %q does not target acme/widgets on github.com", c)
		}
	}
}

// TestLandThroughGitHubSSH443: GitHub's port-443 SSH endpoint is GitHub — no
// alias lookup, no probe, a normal land.
func TestLandThroughGitHubSSH443(t *testing.T) {
	const branch = "swarm/T-ssh443"
	const prURL = "https://github.com/acme/widgets/pull/78"
	repo, base := reviewRepo(t, branch)
	srv, db := reviewServer(t, repo)
	attachReviewDispatch(t, db, &reviewStubWt{})
	fake := useFakeLand(t, landOK(prURL))
	fake.Out["git remote"] = "ssh://git@ssh.github.com:443/acme/widgets.git\n"
	id := seedReviewCard(t, db, "T-ssh443", reviewCard{Branch: branch, StartPoint: base})

	resp, body := postReview(t, srv.URL, id, "land", `{}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %v)", resp.StatusCode, body)
	}
	if fake.Ran("ssh -G") {
		t.Errorf("a known endpoint was looked up; calls = %v", fake.Calls)
	}
	for _, c := range ghCalls(fake) {
		if strings.Contains(c, "ssh.github.com") {
			t.Errorf("gh got the SSH endpoint as its host: %q", c)
		}
	}
}

// TestLandSSHAliasUnresolvedIsUnknown: when `ssh -G` cannot resolve the alias,
// the old outcome stands — an unknown provider, an honest 422, nothing pushed.
func TestLandSSHAliasUnresolvedIsUnknown(t *testing.T) {
	const branch = "swarm/T-alias2"
	repo, base := reviewRepo(t, branch)
	srv, db := reviewServer(t, repo)
	fake := useFakeLand(t, &repoprovider.FakeExec{
		Out:  map[string]string{"git remote": "git@github-work:acme/widgets.git\n"},
		Errs: map[string]string{"ssh -G": "ssh: something went wrong\n"},
	})
	id := seedReviewCard(t, db, "T-alias2", reviewCard{Branch: branch, StartPoint: base})

	resp, body := postReview(t, srv.URL, id, "land", `{}`)
	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422", resp.StatusCode)
	}
	if body["error"] != "repository provider unknown" {
		t.Errorf("error = %v, want %q", body["error"], "repository provider unknown")
	}
	hint, _ := body["hint"].(string)
	if !strings.Contains(hint, "vcs.provider") {
		t.Errorf("hint %q does not name vcs.provider", hint)
	}
	if fake.Ran("git push") {
		t.Errorf("pushed to an unresolved alias; calls = %v", fake.Calls)
	}
}

// ── pure helpers ─────────────────────────────────────────────────────────────

func TestLandPRBody(t *testing.T) {
	got := landPRBody(reviewTarget{
		ExternalID: "T-abc123", Prompt: "  make the widget foldable  ",
		VerifyVerdict: "pass", VerifyDetail: "build + tests green",
	})
	for _, want := range []string{
		"make the widget foldable",
		"Verification: pass — build + tests green",
		"Swarm-Task-Id: T-abc123",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("body missing %q:\n%s", want, got)
		}
	}
	// A long prompt is summarised, not pasted whole.
	long := landPRBody(reviewTarget{ExternalID: "T-x", Prompt: strings.Repeat("y", 2000)})
	if !strings.Contains(long, "…") {
		t.Error("long prompt was not truncated")
	}
	if len(long) > reviewPRBodyChars+200 {
		t.Errorf("body = %d chars, want ~%d + trailer", len(long), reviewPRBodyChars)
	}
	// No verdict recorded ⇒ no verdict line invented.
	bare := landPRBody(reviewTarget{ExternalID: "T-y", Prompt: "p"})
	if strings.Contains(bare, "Verification:") {
		t.Errorf("ungraded card claims a verdict:\n%s", bare)
	}
}

func TestFirstURL(t *testing.T) {
	cases := map[string]string{
		"https://github.com/a/b/pull/7\n":                   "https://github.com/a/b/pull/7",
		"Creating pull request\nhttps://x.test/pull/1 done": "https://x.test/pull/1",
		"see https://x.test/pull/2.":                        "https://x.test/pull/2",
		"nothing here":                                      "",
		"":                                                  "",
	}
	for in, want := range cases {
		if got := repoprovider.FirstURL(in); got != want {
			t.Errorf("FirstURL(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestDiscardArchivesTheMicroPlanDir: throwing away a card must also retire the
// micro-plan it materialized, or the Plans page keeps showing an active plan for
// work that was explicitly abandoned — a zombie whose phase will never be ticked
// by anyone.
//
// Files only, deliberately: wsingest re-derives a workspace task's status and
// archived_at from the ZONE its dir sits in, so the move IS the state change and
// writing the rows here too would make two writers of one projection.
func TestDiscardArchivesTheMicroPlanDir(t *testing.T) {
	const branch = "swarm/T-micro1"
	repo, base := reviewRepo(t, branch)
	srv, db := reviewServer(t, repo)
	attachReviewDispatch(t, db, &reviewStubWt{existed: true})
	id := seedReviewCard(t, db, "T-micro1", reviewCard{
		Branch: branch, StartPoint: base, Worktree: "/tmp/wt/T-micro1",
	})

	// The micro-plan dispatch would have minted, in a real workspace tree.
	wsRoot := t.TempDir()
	dir, err := taskdir.MintMicroPlan(wsRoot, "p", taskdir.Card{
		ExternalID: "T-micro1", Title: "review me", Prompt: "do the thing",
	}, time.Date(2026, 8, 17, 9, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE tasks SET workspace_dir=? WHERE id=?`, dir, id); err != nil {
		t.Fatal(err)
	}

	resp, body := postReview(t, srv.URL, id, "discard", `{}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %v)", resp.StatusCode, body)
	}

	// working/ is empty of it, archive/ holds it at the mirrored path.
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Errorf("the micro-plan is still in working/: %v", err)
	}
	archived := strings.Replace(dir, string(os.PathSeparator)+"working"+string(os.PathSeparator),
		string(os.PathSeparator)+"archive"+string(os.PathSeparator), 1)
	if _, err := os.Stat(filepath.Join(archived, "plan", taskdir.PhaseDocName)); err != nil {
		t.Fatalf("the micro-plan was not archived to %s: %v", archived, err)
	}
	// The join follows the dir, so a later reader does not point at a path that
	// no longer exists.
	if got := taskRow(t, db, id, "workspace_dir"); got != archived {
		t.Errorf("workspace_dir = %q, want %q", got, archived)
	}
	// The README says done, the way the plan lifecycle's own archive action says it.
	raw, err := os.ReadFile(filepath.Join(archived, "README.md"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "- **Status**: done") {
		t.Errorf("archived README is still active:\n%s", raw)
	}
}

// A card with no micro-plan discards exactly as it always did — the hook must be
// invisible when there is nothing to archive.
func TestDiscardWithoutAMicroPlanIsUnchanged(t *testing.T) {
	const branch = "swarm/T-micro2"
	repo, base := reviewRepo(t, branch)
	srv, db := reviewServer(t, repo)
	attachReviewDispatch(t, db, &reviewStubWt{existed: true})
	id := seedReviewCard(t, db, "T-micro2", reviewCard{Branch: branch, StartPoint: base})

	resp, body := postReview(t, srv.URL, id, "discard", `{}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %v)", resp.StatusCode, body)
	}
	if col := taskRow(t, db, id, "board_column"); col != "archived" {
		t.Errorf("column = %s, want archived", col)
	}
}

// A workspace_dir pointing at a path that is gone (hand-cleaned workspace, restored
// snapshot) must not break the discard: the card is already archived by then, and a
// failed cleanup that 500s would leave the user retrying a completed action.
func TestDiscardToleratesAMissingMicroPlanDir(t *testing.T) {
	const branch = "swarm/T-micro3"
	repo, base := reviewRepo(t, branch)
	srv, db := reviewServer(t, repo)
	attachReviewDispatch(t, db, &reviewStubWt{existed: true})
	id := seedReviewCard(t, db, "T-micro3", reviewCard{Branch: branch, StartPoint: base})
	if _, err := db.Exec(`UPDATE tasks SET workspace_dir=? WHERE id=?`,
		filepath.Join(t.TempDir(), "gone", "working", "2026", "08", "17", "card-t-micro3"), id); err != nil {
		t.Fatal(err)
	}

	resp, body := postReview(t, srv.URL, id, "discard", `{}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %v)", resp.StatusCode, body)
	}
}
