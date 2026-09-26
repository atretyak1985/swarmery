package api

// Tests for the board review exits (tasks_review.go): rerun, discard, land.
//
// Unlike the diff endpoint (tasks_diff_test.go, real temp repos), these fake the
// exec boundary: `git push` and `gh pr create` reach a network and a GitHub
// account, and a test suite that touches either is a test suite that fails on a
// plane. The fake is scripted per command so each failure mode — no origin, no
// gh, gh erroring — is driven deliberately rather than hoped for.

import (
	"bytes"
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
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/taskdir"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/worktree"
)

// fakeReview is a scripted reviewExec. Commands are keyed by binary + first
// argument ("git push", "gh pr"), which is granular enough to distinguish every
// step of the land sequence without the tests hard-coding whole argv lines.
type fakeReview struct {
	calls   []string
	dirs    []string
	out     map[string]string // key → stdout on success
	errs    map[string]string // key → stderr; the call also fails
	missing map[string]bool   // binaries Look() reports as absent
}

func reviewKey(name string, args []string) string {
	if len(args) == 0 {
		return name
	}
	return name + " " + args[0]
}

func (f *fakeReview) Run(dir string, _ time.Duration, name string, args ...string) (string, string, error) {
	f.calls = append(f.calls, strings.Join(append([]string{name}, args...), " "))
	f.dirs = append(f.dirs, dir)
	k := reviewKey(name, args)
	if msg, bad := f.errs[k]; bad {
		return "", msg, fmt.Errorf("exit status 1")
	}
	return f.out[k], "", nil
}

func (f *fakeReview) Look(name string) error {
	if f.missing[name] {
		return fmt.Errorf("exec: %q: executable file not found in $PATH", name)
	}
	return nil
}

func (f *fakeReview) ran(substr string) bool {
	for _, c := range f.calls {
		if strings.Contains(c, substr) {
			return true
		}
	}
	return false
}

// useFakeReview swaps the package exec boundary, restoring it on cleanup.
func useFakeReview(t *testing.T, f *fakeReview) *fakeReview {
	t.Helper()
	prev := reviewRun
	reviewRun = f
	t.Cleanup(func() { reviewRun = prev })
	return f
}

// landOK is the fake for a land that succeeds end to end.
func landOK(prURL string) *fakeReview {
	return &fakeReview{out: map[string]string{
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
	fake := useFakeReview(t, landOK(prURL))
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
	if !fake.ran("git push -u origin " + branch) {
		t.Errorf("branch was not pushed; calls = %v", fake.calls)
	}
	if !fake.ran("gh pr create --head " + branch) {
		t.Errorf("PR was not created; calls = %v", fake.calls)
	}
	if fake.ran("--draft") {
		t.Error("draft flag sent for a non-draft land")
	}
	// Git must run in the project repo root, not the (reclaimed) worktree.
	for _, d := range fake.dirs {
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
	fake := useFakeReview(t, landOK("https://github.com/acme/widgets/pull/1"))
	id := seedReviewCard(t, db, "T-draft6", reviewCard{Branch: branch, StartPoint: base})

	resp, _ := postReview(t, srv.URL, id, "land", `{"draft":true}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if !fake.ran("--draft") {
		t.Errorf("draft flag not sent; calls = %v", fake.calls)
	}
}

func TestLandWithoutOriginIs422(t *testing.T) {
	const branch = "swarm/T-noorg7"
	repo, base := reviewRepo(t, branch)
	srv, db := reviewServer(t, repo)
	fake := useFakeReview(t, &fakeReview{errs: map[string]string{
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
	if fake.ran("git push") {
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
	fake := useFakeReview(t, &fakeReview{
		out:     map[string]string{"git remote": "git@github.com:acme/widgets.git\n"},
		missing: map[string]bool{"gh": true},
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
	if !fake.ran("git push") {
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
	useFakeReview(t, &fakeReview{
		out:  map[string]string{"git remote": "origin\n"},
		errs: map[string]string{"gh pr": "pull request already exists for branch\n"},
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
	useFakeReview(t, landOK("https://example.invalid/pr/1"))
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
	fake := useFakeReview(t, landOK("https://example.invalid/pr/2"))
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
	if len(fake.calls) != 0 {
		t.Errorf("commands ran for a running card: %v", fake.calls)
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
		if got := firstURL(in); got != want {
			t.Errorf("firstURL(%q) = %q, want %q", in, got, want)
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
