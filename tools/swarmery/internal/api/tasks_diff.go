package api

// Board review loop — the diff endpoint (board redesign phase 3, §3.1).
//
// A card sitting in in_review is a claim: "the agent did the work". This
// endpoint is the evidence — the commits its run branch carries, the files they
// touched, and the unified patch — so a human can accept or reject the claim
// without leaving the board.
//
// Git runs in the PROJECT REPO ROOT, never in the task's worktree. The worktree
// is reclaimed the moment a card reaches a terminal column (RemoveWorktreeFor),
// while the swarm/<T-id> branch deliberately outlives it (every Remove passes
// keepBranch=true). Reading the branch from the repo root is therefore the only
// way the diff still resolves for a card that has already been reviewed once.
//
// The exec boundary (reviewExec) lives here rather than reusing worktree.Git
// because that interface returns stdout and stderr COMBINED: spliced into a
// unified patch, git's own progress chatter would land inside the diff text the
// UI renders. A diff endpoint cannot use a combined stream (repoprovider.Exec,
// which the land path uses, keeps the streams apart for the same reason). Everything else about the runner (PATH resolution, a bounded timeout,
// an error carrying the output tail) mirrors worktree.ExecGit.

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/repoprovider"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/repoprovider/credstore"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/repoprovider/github"
)

// reviewMaxPatchBytes caps the unified patch a diff response carries. Past this
// the browser is the wrong tool anyway — the response says so with
// patchTruncated and the UI points at the worktree terminal instead.
const reviewMaxPatchBytes = 200 << 10 // 200 KB

// A local git read that takes longer than gitReviewTimeout is a wedged lock, not
// slow work. (The land path's network budget lives in repoprovider.NetTimeout.)
const (
	gitReviewTimeout  = 10 * time.Second
	reviewOutputTail  = 2048
	reviewGitBinary   = "git"
	reviewPRBodyChars = 500
)

// landProvider resolves the code-host provider the land exit pushes to and
// opens its change request on: the repo's `origin` is read and classified
// (repoprovider.Detect over the project's vcs config), and a GitHub host gets
// the gh-backed provider. Any other kind comes back with a nil Provider and its
// Detection, for landBoardTask to refuse with a hint. An error means there is
// no usable origin.
//
// A package var for the same reason reviewRun is one: tests swap it for a
// factory over a repoprovider.FakeExec, with a restore in t.Cleanup.
var landProvider = newLandProvider(repoprovider.OSExec{}, repoprovider.HTTPProber{}, credstore.Env)

// newLandProvider builds a landProvider over an exec boundary, a GitLab prober
// for unknown hosts, and the per-host credential env (production: credstore.Env,
// which is nil until the daemon holds a token, so the operator's own `gh` login
// is used exactly as before).
func newLandProvider(ex repoprovider.Exec, probe repoprovider.Prober, env func(host string) []string) func(ctx context.Context, repoDir string) (repoprovider.Provider, repoprovider.Detection, error) {
	return func(ctx context.Context, repoDir string) (repoprovider.Provider, repoprovider.Detection, error) {
		det, err := repoprovider.Detect(ctx, ex, repoDir, repoprovider.LoadConfig(repoDir), probe)
		if err != nil {
			return nil, det, err
		}
		if det.Kind == repoprovider.KindGitHub {
			return github.New(ex, env), det, nil
		}
		return nil, det, nil
	}
}

// reviewExec is the process boundary of the diff endpoint: `git` reads of the
// project repo root. (Land goes through landProvider instead.)
type reviewExec interface {
	// Run executes name+args with dir as the working directory, returning stdout
	// and stderr SEPARATELY (see the file header for why that matters).
	Run(dir string, timeout time.Duration, name string, args ...string) (stdout, stderr string, err error)
	// Look reports whether the binary resolves on PATH — the check that turns a
	// missing `gh` into an actionable 422 instead of an opaque exec error.
	Look(name string) error
}

// execReview is the production reviewExec: it shells out to the real binaries.
type execReview struct{}

func (execReview) Run(dir string, timeout time.Duration, name string, args ...string) (string, string, error) {
	if timeout <= 0 {
		timeout = gitReviewTimeout
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = dir
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	if err != nil && ctx.Err() == context.DeadlineExceeded {
		err = fmt.Errorf("%s %s timed out after %s", name, strings.Join(args, " "), timeout)
	}
	return stdout.String(), stderr.String(), err
}

func (execReview) Look(name string) error {
	_, err := lookPathFn(name)
	return err
}

// reviewRun is the exec boundary the review handlers use. A package var for the
// same reason dispatchSvc is one: the handlers are methods on Handler, which the
// daemon builds without any injection point, and tests substitute a scripted
// fake with a restore in t.Cleanup.
var reviewRun reviewExec = execReview{}

// gitReview runs a git subcommand in the project repo root with the local-read
// budget.
func gitReview(dir string, args ...string) (stdout, stderr string, err error) {
	return reviewRun.Run(dir, gitReviewTimeout, reviewGitBinary, args...)
}

// gitRevExists reports whether rev resolves to a commit in dir. It is what
// separates "the branch was deleted out of band" (404) from "the base SHA is
// unreachable" (409) after a range command fails — the two conditions produce
// the same git error text and need opposite answers.
func gitRevExists(dir, rev string) bool {
	_, _, err := gitReview(dir, "rev-parse", "--verify", "--quiet", rev+"^{commit}")
	return err == nil
}

// reviewCommitDTO is one commit on the run branch.
type reviewCommitDTO struct {
	SHA     string `json:"sha"`
	Subject string `json:"subject"`
}

// reviewFileDTO is one changed path with its line deltas. A binary file reports
// 0/0 (git prints "-" for both) — the path is still the useful part.
type reviewFileDTO struct {
	Path      string `json:"path"`
	Additions int    `json:"additions"`
	Deletions int    `json:"deletions"`
}

// taskDiffDTO is the GET /api/board/tasks/{id}/diff body. Mirrored in
// web/src/api/types.ts as TaskDiff.
type taskDiffDTO struct {
	Base           string            `json:"base"`
	Branch         string            `json:"branch"`
	Commits        []reviewCommitDTO `json:"commits"`
	Files          []reviewFileDTO   `json:"files"`
	Patch          string            `json:"patch"`
	PatchTruncated bool              `json:"patchTruncated"`
}

// reviewTarget is the row state a review action reads: the card's identity,
// where its work lives, and whether anything still holds it.
type reviewTarget struct {
	ID            int64
	ExternalID    string
	Title         string
	Prompt        string
	Branch        string
	StartPoint    string
	WorktreePath  string
	BoardColumn   string
	Status        string
	ProjectPath   string
	VerifyVerdict string
	VerifyDetail  string
}

// loadReviewTarget reads the board row a review action addresses. ok=false means
// it already wrote the response: 404 when the row is unknown OR is not a board
// card (source='queue') — from this surface the two are the same thing, exactly
// as deleteBoardTask treats them.
func (h *Handler) loadReviewTarget(w http.ResponseWriter, id int64) (reviewTarget, bool) {
	var (
		t                                        reviewTarget
		source                                   string
		extID, branch, startPoint, wtPath        sql.NullString
		projectPath, verifyVerdict, verifyDetail sql.NullString
	)
	err := h.DB.QueryRow(`
		SELECT t.external_id, t.title, t.prompt, t.source, t.board_column, t.status,
		       t.branch, t.start_point, t.worktree_path,
		       t.verify_verdict, t.verify_detail, p.path
		  FROM tasks t JOIN projects p ON p.id = t.project_id
		 WHERE t.id = ?`, id).Scan(
		&extID, &t.Title, &t.Prompt, &source, &t.BoardColumn, &t.Status,
		&branch, &startPoint, &wtPath,
		&verifyVerdict, &verifyDetail, &projectPath)
	if errors.Is(err, sql.ErrNoRows) {
		writeClientErr(w, http.StatusNotFound, "task not found")
		return reviewTarget{}, false
	}
	if err != nil {
		writeErr(w, err)
		return reviewTarget{}, false
	}
	if source != "queue" {
		writeClientErr(w, http.StatusNotFound, "task not found")
		return reviewTarget{}, false
	}
	t.ID = id
	t.ExternalID = extID.String
	t.Branch = strings.TrimSpace(branch.String)
	t.StartPoint = strings.TrimSpace(startPoint.String)
	t.WorktreePath = strings.TrimSpace(wtPath.String)
	t.ProjectPath = strings.TrimSpace(projectPath.String)
	t.VerifyVerdict = verifyVerdict.String
	t.VerifyDetail = verifyDetail.String
	return t, true
}

// GET /api/board/tasks/{id}/diff — what the agent committed on this card's run
// branch, measured from the start point admission pinned it to.
//
// No requireLocalOrigin: it mutates nothing, matching every other board GET (and
// GET /api/retro/agents/{agent}/evidence, which spells the same reasoning out).
func (h *Handler) boardTaskDiff(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeClientErr(w, http.StatusBadRequest, "invalid task id")
		return
	}
	tgt, ok := h.loadReviewTarget(w, id)
	if !ok {
		return
	}
	// Branch is checked before start point on purpose: a card that was never
	// dispatched has neither, and "never dispatched" is the more useful sentence
	// than "no recorded base".
	if tgt.Branch == "" {
		writeConflict(w, codeNoRunBranch,
			"this card has no run branch — it was never dispatched, so there is nothing to diff")
		return
	}
	if tgt.StartPoint == "" {
		writeConflict(w, codeNoStartPoint,
			"this card has no recorded start point (it was dispatched before the base was pinned), "+
				"so there is no commit to measure its branch against")
		return
	}
	if tgt.ProjectPath == "" {
		writeConflict(w, codeNoProjectPath, "project has no known path")
		return
	}

	diff, err := collectBranchDiff(tgt.ProjectPath, tgt.StartPoint, tgt.Branch)
	if err != nil {
		writeBranchDiffErr(w, err)
		return
	}
	writeJSON(w, diff, nil)
}

// branchGoneError: the run branch a diff was asked for no longer resolves in
// the repo (deleted out of band). Answered 404 — the thing addressed is gone.
type branchGoneError struct{ Branch, Reason string }

func (e *branchGoneError) Error() string {
	return "run branch " + e.Branch + " no longer exists: " + e.Reason
}

// baseUnreachableError: the base a diff is measured from is recorded, but git
// can no longer resolve it (a force-push or a gc dropped it). Answered 409
// base-unreachable — the repo moved out from under the recorded base.
type baseUnreachableError struct{ Base, Reason string }

func (e *baseUnreachableError) Error() string {
	return "the start point " + e.Base + " is no longer reachable in this repo, " +
		"so the branch cannot be measured against it: " + e.Reason
}

// collectBranchDiff reads what branch carries on top of base in repoDir: the
// commits unique to it, the files they touched, and the unified patch (capped at
// reviewMaxPatchBytes). Shared by the board card diff and the plan-phase review
// (phase_landing.go), so both screens read a branch the same way.
//
// `..` for the commit list, `...` for the diffs: the log wants the commits unique
// to the branch, the diff wants the change against the merge base. The trailing
// `--` pins both ranges into the revision slot so a name can never be re-read as
// a flag.
//
// A *branchGoneError or *baseUnreachableError says which end of the range is
// gone; anything else is an unexpected git failure. writeBranchDiffErr maps all
// three onto the HTTP answer.
func collectBranchDiff(repoDir, base, branch string) (taskDiffDTO, error) {
	logRange := base + ".." + branch
	diffRange := base + "..." + branch

	out, stderr, err := gitReview(repoDir, "log", "--format=%H%x00%s", logRange, "--")
	if err != nil {
		// Both a deleted branch and an unreachable base fail here with the same
		// "unknown revision" shape, and they need opposite answers — probe which
		// one is actually gone rather than guessing from the message.
		switch {
		case !gitRevExists(repoDir, branch):
			return taskDiffDTO{}, &branchGoneError{Branch: branch, Reason: gitReason(stderr, err)}
		case !gitRevExists(repoDir, base):
			return taskDiffDTO{}, &baseUnreachableError{Base: base, Reason: gitReason(stderr, err)}
		default:
			return taskDiffDTO{}, fmt.Errorf("git log %s: %w: %s", logRange, err, gitReason(stderr, err))
		}
	}
	commits := parseReviewCommits(out)

	numstat, nsErr, err := gitReview(repoDir, "diff", "--numstat", diffRange, "--")
	if err != nil {
		return taskDiffDTO{}, fmt.Errorf("git diff --numstat %s: %w: %s", diffRange, err, gitReason(nsErr, err))
	}
	files := parseNumstat(numstat)

	// The whole patch is buffered before it is capped. Acceptable for a
	// single-user local daemon reviewing one branch; the cap is about what a
	// browser can usefully render, not about bounding this process's memory.
	raw, pErr, err := gitReview(repoDir, "diff", diffRange, "--")
	if err != nil {
		return taskDiffDTO{}, fmt.Errorf("git diff %s: %w: %s", diffRange, err, gitReason(pErr, err))
	}
	patch, truncated := truncatePatch(raw, reviewMaxPatchBytes)

	return taskDiffDTO{
		Base:           base,
		Branch:         branch,
		Commits:        commits,
		Files:          files,
		Patch:          patch,
		PatchTruncated: truncated,
	}, nil
}

// writeBranchDiffErr answers a collectBranchDiff failure: 404 when the branch is
// gone, 409 base-unreachable when the base is, 500 otherwise.
func writeBranchDiffErr(w http.ResponseWriter, err error) {
	var gone *branchGoneError
	var unreachable *baseUnreachableError
	switch {
	case errors.As(err, &gone):
		writeClientErr(w, http.StatusNotFound, gone.Error())
	case errors.As(err, &unreachable):
		writeConflict(w, codeBaseUnreachable, unreachable.Error())
	default:
		writeErr(w, err)
	}
}

// gitReason picks the most informative text available for a failed git call:
// git's own stderr when it said something, the process error otherwise. Bounded,
// because it goes into an HTTP body a human reads.
func gitReason(stderr string, err error) string {
	s := strings.TrimSpace(stderr)
	if s == "" && err != nil {
		s = err.Error()
	}
	if len(s) > reviewOutputTail {
		s = s[len(s)-reviewOutputTail:]
	}
	return s
}

// parseReviewCommits parses `git log --format=%H%x00%s` output. NUL separates
// the SHA from the subject so a subject containing anything at all — tabs,
// pipes, the format string itself — still round-trips. Pure; unit-tested.
func parseReviewCommits(out string) []reviewCommitDTO {
	commits := []reviewCommitDTO{}
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimRight(line, "\r")
		if line == "" {
			continue
		}
		sha, subject, found := strings.Cut(line, "\x00")
		if !found {
			continue
		}
		commits = append(commits, reviewCommitDTO{SHA: sha, Subject: subject})
	}
	return commits
}

// parseNumstat parses `git diff --numstat` output ("adds\tdels\tpath"). A binary
// file prints "-" for both counts and lands as 0/0 — the path is what the file
// table is for. Pure; unit-tested.
func parseNumstat(out string) []reviewFileDTO {
	files := []reviewFileDTO{}
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimRight(line, "\r")
		if line == "" {
			continue
		}
		parts := strings.SplitN(line, "\t", 3)
		if len(parts) != 3 {
			continue
		}
		add, _ := strconv.Atoi(parts[0]) // "-" (binary) → 0, which is the honest count
		del, _ := strconv.Atoi(parts[1])
		files = append(files, reviewFileDTO{Path: parts[2], Additions: add, Deletions: del})
	}
	return files
}

// truncatePatch caps s at max bytes, backing off to the last newline inside the
// cap so the client's `diff --git` split never has to reason about a half line.
// Reports whether anything was dropped. Pure; unit-tested.
func truncatePatch(s string, max int) (string, bool) {
	if len(s) <= max {
		return s, false
	}
	cut := s[:max]
	if nl := strings.LastIndexByte(cut, '\n'); nl > 0 {
		cut = cut[:nl+1]
	}
	return cut, true
}
