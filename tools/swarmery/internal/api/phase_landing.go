package api

// Plan-phase landing (phase-landing plan, phase 3): how a finished phase run's
// branch reaches the code host.
//
//	GET  /api/epics/{taskId}/phases/{phaseId}/review → 200 phaseReviewDTO
//	POST /api/epics/{taskId}/phases/{phaseId}/land   {action:"push"|"pr", draft?} → 200 {branch, base, action, landing}
//	POST /api/epics/{taskId}/phases/{phaseId}/land   {action:"return", feedback} → 202 {status, sessionUuid, action, landing}
//
// The review is the evidence the operator lands on — the commits the run branch
// carries, the files and the patch (collectBranchDiff, the same reader the board
// card diff uses), the verification verdict, the landing state and the
// provider's vocabulary. Land pushes the branch through internal/repoprovider
// (the landProvider factory board land uses, so tests script a FakeExec) and,
// for action "pr", opens the change request. Action "return" sends the phase
// back to its agent instead: the operator's feedback is written into the phase
// doc (wsingest.AppendOperatorFeedback), the phase is marked `returned`, and its
// run restarts with phaserun.StartOptions.Returned.
//
// The landing lifecycle lives on epic_phases (migration 0103) and is
// daemon-owned: wsingest's upsert never lists those columns, so a re-scan of the
// plan dir cannot reset a pushed phase. `ready` is never stored — it is derived
// at read time (landing_state='none' AND run_state='done'), so a re-scan that
// flips run_state never needs a landing write.
//
// Refusals come before any network call: a running phase (409 phase-running), a
// phase with no run branch (409 no-run-branch), the reserved fork workflow (409
// fork-workflow-unsupported), and a run branch that IS the base branch (409
// push-to-base-refused, lifted by swarmery.vcs.allowPushToBase). Every machine
// problem after that is a 422 {error, code, hint, detail}: `hint` carries the
// exact commands that finish the job by hand, `detail` the tool's own output
// through credstore.Redact. A push is never forced and never retried.

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/phaserun"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/repoprovider"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/repoprovider/credstore"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/repoprovider/providers"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/wsingest"
)

// landExec is the process boundary of the landing path's own local git reads
// (origin's HEAD, the checked-out branch, the remote lookup behind the review's
// provider terms). Push and change-request calls go through landProvider. A
// package var for the same reason landProvider is one: tests swap both for one
// repoprovider.FakeExec, with a restore in t.Cleanup.
var landExec repoprovider.Exec = repoprovider.OSExec{}

// Landing states as stored in epic_phases.landing_state, plus the derived
// landingReady. Stable wire values (web/src/api/types.ts PhaseLandingState).
const (
	landingNone     = "none"
	landingReady    = "ready" // derived, never stored
	landingPushed   = "pushed"
	landingPROpen   = "pr_open"
	landingMerged   = "merged"
	landingReturned = "returned"
)

// Land actions (web/src/api/types.ts PhaseLandAction).
const (
	landActionPush   = "push"
	landActionPR     = "pr"
	landActionReturn = "return" // send the phase back to its agent with feedback
)

// landingDTO is a phase's landing lifecycle (camelCase, mirrored in
// web/src/api/types.ts as PhaseLanding). Every nullable column stays null until
// the step that writes it has happened.
type landingDTO struct {
	// none | ready | pushed | pr_open | merged | returned. `ready` is derived:
	// nothing landed yet and the phase's run finished.
	State      string  `json:"state"`
	PrURL      *string `json:"prUrl"`
	PrNumber   *int    `json:"prNumber"`
	PrProvider *string `json:"prProvider"`
	// The change request's last polled status (repoprovider.ChangeStatus as JSON),
	// null until polled. Passed through as stored.
	PrStatus json.RawMessage `json:"prStatus"`
	LandedAt *string         `json:"landedAt"`
	// The last landing failure the operator has to act on (today: the host
	// rejected the daemon's credentials). Cleared by the next successful push.
	Error *string `json:"error"`
}

// landingColumns is the SELECT fragment landingRow scans, aliased on `e`
// (epic_phases). Shared by the epic list and the landing handlers so the DTO
// cannot be built from two different column lists.
const landingColumns = `e.landing_state, e.pr_url, e.pr_number, e.pr_provider, e.pr_status, e.landed_at, e.landing_error`

// landingRow is the raw scan of landingColumns.
type landingRow struct {
	state                                     string
	prURL, prProvider, prStatus, landedAt, le sql.NullString
	prNumber                                  sql.NullInt64
}

// dest is the Scan destination list matching landingColumns, in order.
func (l *landingRow) dest() []any {
	return []any{&l.state, &l.prURL, &l.prNumber, &l.prProvider, &l.prStatus, &l.landedAt, &l.le}
}

// dto renders the row, deriving `ready` from runState.
func (l landingRow) dto(runState string) landingDTO {
	d := landingDTO{State: l.state}
	if d.State == "" {
		d.State = landingNone
	}
	if d.State == landingNone && runState == "done" {
		d.State = landingReady
	}
	d.PrURL = nullStrPtr(l.prURL)
	d.PrProvider = nullStrPtr(l.prProvider)
	d.LandedAt = nullStrPtr(l.landedAt)
	d.Error = nullStrPtr(l.le)
	if l.prNumber.Valid {
		n := int(l.prNumber.Int64)
		d.PrNumber = &n
	}
	if l.prStatus.Valid && json.Valid([]byte(l.prStatus.String)) {
		d.PrStatus = json.RawMessage(l.prStatus.String)
	}
	return d
}

func nullStrPtr(v sql.NullString) *string {
	if !v.Valid {
		return nil
	}
	s := v.String
	return &s
}

// phaseReviewDTO is the GET …/review body (mirrored in web/src/api/types.ts as
// PhaseReview): the run branch's diff against its base, the verification
// verdict, the landing lifecycle and the provider's vocabulary.
type phaseReviewDTO struct {
	Base           string             `json:"base"`
	Branch         string             `json:"branch"`
	Commits        []reviewCommitDTO  `json:"commits"`
	Files          []reviewFileDTO    `json:"files"`
	Patch          string             `json:"patch"`
	PatchTruncated bool               `json:"patchTruncated"`
	VerifyVerdict  *string            `json:"verifyVerdict"`
	VerifyDetail   *string            `json:"verifyDetail"`
	Landing        landingDTO         `json:"landing"`
	Terms          repoprovider.Terms `json:"terms"`
}

// landingTarget is the row state the landing handlers read.
type landingTarget struct {
	TaskID        int64
	PhaseID       int64
	Seq           int
	Name          string
	DocPath       string
	RunState      string
	RunBranch     string
	RunStartPoint string
	VerifyVerdict *string
	VerifyDetail  *string
	ProjectPath   string
	PlanTitle     string
	PlanDir       string
	WorkspaceRoot string
	Landing       landingDTO
}

// parseLandingParams parses {taskId} and {phaseId}. Writes the 400 itself.
func parseLandingParams(w http.ResponseWriter, r *http.Request) (taskID, phaseID int64, ok bool) {
	taskID, err := strconv.ParseInt(r.PathValue("taskId"), 10, 64)
	if err != nil {
		writeClientErr(w, http.StatusBadRequest, "invalid task id")
		return 0, 0, false
	}
	phaseID, err = strconv.ParseInt(r.PathValue("phaseId"), 10, 64)
	if err != nil {
		writeClientErr(w, http.StatusBadRequest, "invalid phase id")
		return 0, 0, false
	}
	return taskID, phaseID, true
}

// loadLandingTarget reads the phase a landing route addresses, with its plan
// (the workspace task: title, plan dir) and project path — the same
// epic_phases → tasks → projects join runPhase's admission uses. ok=false means
// it already wrote the response: 404 when the phase is unknown or belongs to a
// different workspace task (the route addresses a phase THROUGH its epic).
func (h *Handler) loadLandingTarget(w http.ResponseWriter, taskID, phaseID int64) (landingTarget, bool) {
	var (
		t                                       landingTarget
		wsTaskID                                int64
		runBranch, startPoint, verdict, vDetail sql.NullString
		projectPath, wsRoot, planDir            sql.NullString
		lr                                      landingRow
	)
	dest := []any{
		&wsTaskID, &t.Seq, &t.Name, &t.DocPath, &t.RunState, &runBranch, &startPoint,
		&verdict, &vDetail, &t.PlanTitle, &projectPath, &wsRoot, &planDir,
	}
	dest = append(dest, lr.dest()...)
	err := h.DB.QueryRow(`
		SELECT e.workspace_task_id, e.seq, e.name, e.doc_path, e.run_state, e.run_branch,
		       e.run_start_point, e.verify_verdict, e.verify_detail,
		       t.title, p.path, w.root_path,
		       (SELECT path FROM task_artifacts WHERE task_id = t.id AND kind = 'plan'),
		       `+landingColumns+`
		  FROM epic_phases e
		  JOIN tasks t ON t.id = e.workspace_task_id
		  JOIN projects p ON p.id = t.project_id
		  LEFT JOIN workspaces w ON w.project_id = p.id
		 WHERE e.id = ?`, phaseID).Scan(dest...)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && wsTaskID != taskID) {
		writeClientErr(w, http.StatusNotFound, "phase not found")
		return landingTarget{}, false
	}
	if err != nil {
		writeErr(w, err)
		return landingTarget{}, false
	}
	t.TaskID, t.PhaseID = taskID, phaseID
	t.RunBranch = strings.TrimSpace(runBranch.String)
	t.RunStartPoint = strings.TrimSpace(startPoint.String)
	t.VerifyVerdict = nullStrPtr(verdict)
	t.VerifyDetail = nullStrPtr(vDetail)
	t.ProjectPath = strings.TrimSpace(projectPath.String)
	t.WorkspaceRoot = wsRoot.String
	t.PlanDir = planDir.String
	t.Landing = lr.dto(t.RunState)
	return t, true
}

// phaseLanding re-reads one phase's landing DTO after a write.
func (h *Handler) phaseLanding(phaseID int64) (landingDTO, error) {
	var (
		runState string
		lr       landingRow
	)
	err := h.DB.QueryRow(`SELECT e.run_state, `+landingColumns+` FROM epic_phases e WHERE e.id = ?`,
		phaseID).Scan(append([]any{&runState}, lr.dest()...)...)
	if err != nil {
		return landingDTO{}, err
	}
	return lr.dto(runState), nil
}

// landingRepoDir resolves the repository the phase's runs commit in — the same
// rules phaserun applies at Start (phaserun.Service.RunRoot), so a multi-repo
// plan's phase is reviewed and pushed in the repo it ran in, not the project
// root. Works without an attached phase-run service: RunRoot reads only the DB.
// ok=false means it wrote the response.
func (h *Handler) landingRepoDir(w http.ResponseWriter, phaseID int64) (string, bool) {
	svc := phaserunSvc
	if svc == nil {
		svc = &phaserun.Service{DB: h.DB}
	}
	dir, err := svc.RunRoot(phaseID)
	switch {
	case err == nil:
		return dir, true
	case errors.Is(err, phaserun.ErrPhaseNotFound):
		writeClientErr(w, http.StatusNotFound, "phase not found")
	case errors.Is(err, phaserun.ErrNoPath):
		writeConflict(w, codeNoProjectPath, "project has no known path")
	// Checked BEFORE ErrNoRepoRoot, which it also wraps.
	case errors.Is(err, phaserun.ErrRepoOutsideProject):
		writeConflict(w, codeRepoOutsideProject, err.Error())
	case errors.Is(err, phaserun.ErrNoRepoRoot):
		writeConflict(w, codeNoRepoRoot, err.Error())
	default:
		writeErr(w, err)
	}
	return "", false
}

// getPhaseReview — GET /api/epics/{taskId}/phases/{phaseId}/review.
// 200 phaseReviewDTO; 404 unknown phase / branch gone; 409 no-run-branch, no
// base to measure against, base unreachable, or no repo root.
//
// No requireLocalOrigin: it mutates nothing (the other phase GETs match). It
// makes no network call either — provider terms come from a Detect without the
// GitLab probe, so an unknown host reads with the neutral vocabulary.
func (h *Handler) getPhaseReview(w http.ResponseWriter, r *http.Request) {
	taskID, phaseID, ok := parseLandingParams(w, r)
	if !ok {
		return
	}
	t, ok := h.loadLandingTarget(w, taskID, phaseID)
	if !ok {
		return
	}
	if t.RunBranch == "" {
		writeConflict(w, codeNoRunBranch,
			"this phase has no run branch — it never ran, so there is nothing to review")
		return
	}
	repoDir, ok := h.landingRepoDir(w, phaseID)
	if !ok {
		return
	}
	// Base = the commit the run's worktree was pinned to; a phase stamped before
	// run_start_point existed is measured against the repo's checked-out branch.
	base := t.RunStartPoint
	if base == "" {
		if out, _, err := gitReview(repoDir, "rev-parse", "--abbrev-ref", "HEAD"); err == nil {
			if b := strings.TrimSpace(out); b != "HEAD" {
				base = b
			}
		}
	}
	if base == "" {
		writeConflict(w, codeNoStartPoint,
			"this phase has no recorded start point and the repo has no checked-out branch, "+
				"so there is no base to measure its run branch against")
		return
	}
	diff, err := collectBranchDiff(repoDir, base, t.RunBranch)
	if err != nil {
		writeBranchDiffErr(w, err)
		return
	}
	// Terms come from the provider Factory builds for the detected kind (the
	// same one land uses), so a GitLab project reads "Merge Request"; an
	// unknown host keeps the neutral vocabulary. Building a provider runs nothing.
	terms := repoprovider.TermsFor(repoprovider.KindUnknown)
	if det, err := repoprovider.Detect(r.Context(), landExec, repoDir,
		repoprovider.LoadConfig(t.ProjectPath), nil); err == nil {
		if p, err := providers.Factory(det.Kind, landExec, nil); err == nil {
			terms = p.Terms()
		}
	}
	writeJSON(w, phaseReviewDTO{
		Base:           diff.Base,
		Branch:         diff.Branch,
		Commits:        diff.Commits,
		Files:          diff.Files,
		Patch:          diff.Patch,
		PatchTruncated: diff.PatchTruncated,
		VerifyVerdict:  t.VerifyVerdict,
		VerifyDetail:   t.VerifyDetail,
		Landing:        t.Landing,
		Terms:          terms,
	}, nil)
}

// landingBase is the branch a phase lands against.
type landingBase struct {
	// Name is the base the push-to-base refusal compares the run branch with
	// (a SHA when only the run's start point is known).
	Name string
	// PRBase is the change request's --base; "" = the host's default branch.
	PRBase string
	// OriginHead is origin's default branch as this clone knows it ("" unknown).
	OriginHead string
	// CheckedOut is the repo's checked-out branch, read only when Name fell back
	// to the run's start point (a SHA no branch name can equal): in that case the
	// checked-out branch is the best evidence of what the base is, so a run
	// branch equal to it is refused like any other push onto the base.
	CheckedOut string
}

// resolveLandingBase picks the base: vcs.baseBranch, else origin's HEAD, else
// the run's start point (a commit, so the change request falls back to the
// host default — the checked-out branch is still read, for the push-to-base
// refusal), else the repo's checked-out branch. Local reads only.
func resolveLandingBase(ctx context.Context, ex repoprovider.Exec, repoDir string, cfg repoprovider.Config, startPoint string) landingBase {
	var b landingBase
	if out, _, err := ex.Run(ctx, repoDir, nil, "git", "symbolic-ref", "--short", "refs/remotes/origin/HEAD"); err == nil {
		b.OriginHead = strings.TrimPrefix(strings.TrimSpace(out), repoprovider.DefaultRemote+"/")
	}
	switch {
	case cfg.BaseBranch != "":
		b.Name, b.PRBase = cfg.BaseBranch, cfg.BaseBranch
	case b.OriginHead != "":
		b.Name, b.PRBase = b.OriginHead, b.OriginHead
	case startPoint != "":
		b.Name = startPoint
		b.CheckedOut = checkedOutBranch(ctx, ex, repoDir)
	default:
		if cur := checkedOutBranch(ctx, ex, repoDir); cur != "" {
			b.Name, b.PRBase = cur, cur
		}
	}
	return b
}

// checkedOutBranch is the repo's checked-out branch, "" when detached or unreadable.
func checkedOutBranch(ctx context.Context, ex repoprovider.Exec, repoDir string) string {
	out, _, err := ex.Run(ctx, repoDir, nil, "git", "rev-parse", "--abbrev-ref", "HEAD")
	if err != nil {
		return ""
	}
	if cur := strings.TrimSpace(out); cur != "HEAD" {
		return cur
	}
	return ""
}

// isBase reports whether branch is the base branch by any of the names the
// base could go by.
func (b landingBase) isBase(branch string, cfg repoprovider.Config) bool {
	if branch == "" {
		return false
	}
	return branch == b.Name || branch == cfg.BaseBranch || branch == b.OriginHead || branch == b.CheckedOut
}

// phaseNamePrefixRe matches a "Phase N — " / "Phase N: " lead a phase doc's H1
// usually carries, which phasePRTitle adds itself.
var phaseNamePrefixRe = regexp.MustCompile(`^(?i:phase)\s+\d+\s*[—–:\-.]\s*`)

// phaseDisplayName strips the doc's own "Phase N — " lead from a phase name so
// the change-request title does not read "Phase 3 — Phase 3 — …". Pure.
func phaseDisplayName(name string) string {
	name = strings.TrimSpace(name)
	if stripped := strings.TrimSpace(phaseNamePrefixRe.ReplaceAllString(name, "")); stripped != "" {
		return stripped
	}
	return name
}

// planRelPath is the phase doc's path as the change-request trailer names it:
// relative to the workspace namespace (…/workspace/working/… → working/…), else
// the task dir name + its path under plan/.
func planRelPath(t landingTarget) string {
	if t.WorkspaceRoot != "" {
		if rel, err := filepath.Rel(t.WorkspaceRoot, t.DocPath); err == nil && !strings.HasPrefix(rel, "..") {
			return filepath.ToSlash(strings.TrimPrefix(rel, "workspace"+string(filepath.Separator)))
		}
	}
	if t.PlanDir != "" {
		taskDir := filepath.Dir(t.PlanDir)
		if rel, err := filepath.Rel(taskDir, t.DocPath); err == nil && !strings.HasPrefix(rel, "..") {
			return filepath.ToSlash(filepath.Join(filepath.Base(taskDir), rel))
		}
	}
	return filepath.Base(t.DocPath)
}

// writeLandingUnprocessable replies 422 {error, code, hint, detail}.
func writeLandingUnprocessable(w http.ResponseWriter, code, msg, hint, detail string) {
	writeJSONStatus(w, http.StatusUnprocessableEntity, map[string]any{
		"error": msg, "code": code, "hint": hint, "detail": detail,
	})
}

// landPhase — POST /api/epics/{taskId}/phases/{phaseId}/land {action, draft}.
// requireLocalOrigin. action "push" pushes the run branch (landing_state →
// pushed); action "pr" pushes it and opens a change request (→ pr_open, with its
// URL, number and provider). 200 {branch, base, action, landing}. Action
// "return" {feedback} sends the phase back to its agent (returnPhase): 202, or
// 400 when feedback is empty or over wsingest.ReviewFeedbackMax.
// 400 unknown action / bad body; 404 unknown phase; 409 phase-running,
// no-run-branch, fork-workflow-unsupported, push-to-base-refused, no repo root;
// 422 {error, code, hint, detail} for every machine problem (no-remote,
// not-authenticated, no-push-access, remote-diverged, binary-missing,
// provider-unknown, push-failed, change-request-failed). A 200 carries the
// provider's terms beside the landing, so the caller words the result.
func (h *Handler) landPhase(w http.ResponseWriter, r *http.Request) {
	taskID, phaseID, ok := parseLandingParams(w, r)
	if !ok {
		return
	}
	var body struct {
		Action   string `json:"action"`
		Draft    bool   `json:"draft"`
		Feedback string `json:"feedback"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&body); err != nil && !errors.Is(err, io.EOF) {
		writeClientErr(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	action := strings.TrimSpace(body.Action)
	if action != landActionPush && action != landActionPR && action != landActionReturn {
		writeClientErr(w, http.StatusBadRequest, `unknown action: choose "push", "pr" or "return"`)
		return
	}
	feedback := strings.TrimSpace(body.Feedback)
	if action == landActionReturn {
		if feedback == "" {
			writeClientErr(w, http.StatusBadRequest,
				"feedback is required — returning a phase with no note would repeat the same work")
			return
		}
		if len(feedback) > wsingest.ReviewFeedbackMax {
			writeClientErr(w, http.StatusBadRequest,
				fmt.Sprintf("feedback exceeds %d bytes", wsingest.ReviewFeedbackMax))
			return
		}
	}
	t, ok := h.loadLandingTarget(w, taskID, phaseID)
	if !ok {
		return
	}
	if t.RunState == "running" {
		msg := "this phase is still running — let the run finish before landing it"
		if action == landActionReturn {
			msg = "this phase is still running — let the run finish before returning it"
		}
		writeConflict(w, codePhaseRunning, msg)
		return
	}
	if action == landActionReturn {
		if t.Landing.State == landingMerged {
			writeConflict(w, codePhaseMerged,
				"this phase's change request is already merged — returning it would continue a branch that is already landed. "+
					"Put the follow-up work in a new phase (or a follow-up task) instead")
			return
		}
		h.returnPhase(w, t, feedback)
		return
	}
	if t.RunBranch == "" {
		writeConflict(w, codeNoRunBranch, "this phase has no run branch — there is nothing to push")
		return
	}
	repoDir, ok := h.landingRepoDir(w, phaseID)
	if !ok {
		return
	}
	cfg := repoprovider.LoadConfig(t.ProjectPath)
	if cfg.ForkRemote != "" {
		writeConflict(w, codeForkUnsupported,
			"this project sets vcs.forkRemote ("+cfg.ForkRemote+"), and the fork workflow is not supported yet — "+
				"remove vcs.forkRemote to land to origin, or push and open the change request by hand")
		return
	}

	branch := t.RunBranch
	pushCmd := "git -C " + repoDir + " push -u origin " + branch
	title := phasePRTitle(t.PlanTitle, t.Seq, phaseDisplayName(t.Name))

	// The tool calls outlive the HTTP request on purpose (as in landBoardTask): a
	// browser that navigates away mid-push must not kill a `gh pr create` that may
	// already have opened the change request. Each network call carries its own
	// budget inside the provider (repoprovider.NetTimeout).
	ctx := context.WithoutCancel(r.Context())

	// cfg is the PROJECT's vcs config, never repoDir's: a multi-repo phase runs in a
	// sub-repo whose own .claude/ (if any) is not where vcs.provider is declared.
	provider, det, err := landProvider(ctx, repoDir, cfg)
	if err != nil {
		if errors.Is(err, repoprovider.ErrBinaryMissing) {
			writeLandingUnprocessable(w, codeBinaryMissing, "git not found",
				"`git` is not on the daemon's PATH. Install git, restart the daemon, then land again.",
				landDetail(err))
			return
		}
		writeLandingUnprocessable(w, codeNoRemote, "no origin remote",
			"this repo has no `origin` to push to. Add one, then land again:\n"+
				"git -C "+repoDir+" remote add origin <url>\n"+pushCmd,
			landDetail(err))
		return
	}
	if provider == nil {
		writeLandingUnprocessable(w, codeProviderUnknown, "repository provider unknown",
			"the host of this repo's `origin` ("+det.Remote.Host+") is not a recognised code host. "+
				"Set `vcs.provider` (\"github\" or \"gitlab\") in .claude/project.json, then land again — "+
				"or push by hand:\n"+pushCmd,
			credstore.Redact(det.Remote.URL))
		return
	}

	base := resolveLandingBase(ctx, landExec, repoDir, cfg, t.RunStartPoint)
	if base.isBase(branch, cfg) && !cfg.AllowPushToBase {
		writeConflictFields(w, codePushToBaseRefused,
			"the run branch "+branch+" is the base branch, so landing would push straight onto it. "+
				"Set swarmery.vcs.allowPushToBase=true in .claude/settings.local.json to allow it.",
			map[string]any{"branch": branch, "base": base.Name})
		return
	}

	prCmd := changeRequestCmd(det.Kind, branch, base.PRBase, title, body.Draft)
	cli, terms := landCLIFor(det.Kind), provider.Terms()

	target := repoprovider.Target{RepoDir: repoDir, RemoteName: repoprovider.DefaultRemote, Remote: det.Remote}
	if err := provider.Push(ctx, target, branch); err != nil {
		h.writeLandFailure(w, t, det, err, landFailureHints{
			binary: "`git` is not on the daemon's PATH. Install git, restart the daemon, then land again.",
			other:  "the branch could not be pushed. Resolve it and land again, or push by hand:\n" + pushCmd,
			push:   pushCmd,
			repo:   repoDir,
		})
		return
	}
	now := time.Now().UTC().Format(time.RFC3339)
	// none|ready → pushed; returned → pushed, or back to pr_open when it was
	// returned with a change request already open (the push updates that same
	// change request). A phase already further along (pr_open, merged) keeps its
	// state: a re-push of new commits does not un-open its PR.
	if _, err := h.DB.Exec(`
		UPDATE epic_phases
		   SET landing_state = CASE
		         WHEN landing_state = ? THEN ?
		         WHEN landing_state = ? THEN CASE WHEN pr_url IS NOT NULL AND pr_url <> '' THEN ? ELSE ? END
		         ELSE landing_state END,
		       landed_at = ?, landing_error = NULL
		 WHERE id = ?`, landingNone, landingPushed, landingReturned, landingPROpen, landingPushed, now, phaseID); err != nil {
		writeErr(w, err)
		return
	}
	publishPlanUpdated(taskID)

	if action == landActionPR {
		doc, _ := os.ReadFile(t.DocPath) // unreadable ⇒ the body's placeholders say so
		ref, err := provider.OpenChangeRequest(ctx, target, repoprovider.ChangeRequest{
			Head:  branch,
			Base:  base.PRBase,
			Title: title,
			Body:  phasePRBody(string(doc), taskID, phaseID, planRelPath(t)),
			Draft: body.Draft,
		})
		if err != nil {
			h.writeLandFailure(w, t, det, err, landFailureHints{
				pushed: true,
				binary: "the branch is pushed, but the " + cli.Name + " (`" + cli.Bin + "`) is not on PATH so the " +
					terms.Change + " was not opened. Install it (" + cli.Install + "), then land again — or open it by hand:\n" + prCmd,
				other: "the branch is pushed, but the " + terms.Change + " was not opened. Open it by hand:\n" + prCmd,
				push:  pushCmd,
				repo:  repoDir,
			})
			return
		}
		var prNumber any // NULL when the URL carried no number, never a fake 0
		if ref.Number > 0 {
			prNumber = ref.Number
		}
		if _, err := h.DB.Exec(`
			UPDATE epic_phases
			   SET landing_state = ?, pr_url = ?, pr_number = ?, pr_provider = ?, landing_error = NULL
			 WHERE id = ?`, landingPROpen, ref.URL, prNumber, string(ref.Provider), phaseID); err != nil {
			writeErr(w, err)
			return
		}
		publishPlanUpdated(taskID)
	}

	landing, err := h.phaseLanding(phaseID)
	if err != nil {
		writeErr(w, err)
		return
	}
	baseName := base.PRBase
	if baseName == "" {
		baseName = base.Name
	}
	writeJSON(w, map[string]any{
		"branch": branch, "base": baseName, "action": action, "landing": landing, "terms": terms,
	}, nil)
}

// landFailureHints are the stage-specific sentences of a land 422.
type landFailureHints struct {
	pushed bool   // the branch already reached the remote
	binary string // the hint when the tool is not on PATH
	other  string // the hint for an unclassified failure
	push   string // the manual push command
	repo   string // the repo dir the commands run in
}

// writeLandFailure maps a classified provider failure onto its 422. A
// not-authenticated failure also stamps landing_error, so the project's auth
// banner can say the stored credentials stopped working.
func (h *Handler) writeLandFailure(w http.ResponseWriter, t landingTarget, det repoprovider.Detection, err error, hints landFailureHints) {
	detail := landDetail(err)
	host := det.Remote.Host
	stage := ""
	if hints.pushed {
		stage = "the branch is pushed, but "
	}
	switch {
	case errors.Is(err, repoprovider.ErrNotAuthenticated):
		if _, dbErr := h.DB.Exec(`UPDATE epic_phases SET landing_error = ? WHERE id = ?`,
			codeNotAuthenticated+": "+detail, t.PhaseID); dbErr == nil {
			publishPlanUpdated(t.TaskID)
		}
		writeLandingUnprocessable(w, codeNotAuthenticated, "not authenticated",
			stage+"the code host ("+host+") rejected the credentials. Log in, then land again:\n"+
				vcsCliLogin(det.Kind, host),
			detail)
	case errors.Is(err, repoprovider.ErrNoPushAccess):
		writeLandingUnprocessable(w, codeNoPushAccess, "no push access",
			stage+"the account the daemon pushes as has no write access to "+host+"/"+det.Remote.Slug()+
				" (or the branch is protected). Get write access — or push from an account that has it — then land again:\n"+
				hints.push,
			detail)
	case errors.Is(err, repoprovider.ErrRemoteDiverged):
		// Never retried and never forced: the remote branch has commits this one
		// lacks, and only the operator can decide how they combine.
		writeLandingUnprocessable(w, codeRemoteDiverged, "remote branch has diverged",
			"origin/"+t.RunBranch+" has commits this branch does not. In a checkout of "+t.RunBranch+
				" (its run worktree, or `git -C "+hints.repo+" switch "+t.RunBranch+"`), bring them in, then land again:\n"+
				"git fetch origin && git rebase origin/"+t.RunBranch,
			detail)
	case errors.Is(err, repoprovider.ErrBinaryMissing):
		writeLandingUnprocessable(w, codeBinaryMissing, "required CLI not found", hints.binary, detail)
	case errors.Is(err, repoprovider.ErrNoRemote):
		writeLandingUnprocessable(w, codeNoRemote, "no origin remote",
			"this repo has no `origin` to push to. Add one, then land again:\n"+
				"git -C "+hints.repo+" remote add origin <url>\n"+hints.push,
			detail)
	case isNoURL(err):
		cli := landCLIFor(det.Kind)
		writeLandingUnprocessable(w, codeChangeRequestFailed, cli.Create+" returned no URL",
			"the branch is pushed and `"+cli.Bin+"` exited 0, but printed no "+det.Terms.Change+" URL. Check it by hand.\n"+hints.other,
			detail)
	case hints.pushed:
		writeLandingUnprocessable(w, codeChangeRequestFailed, "change request failed", hints.other, detail)
	default:
		writeLandingUnprocessable(w, codePushFailed, "push failed", hints.other, detail)
	}
}

// returnPhase is landPhase's action "return": send a finished phase back to its
// agent. The caller has validated feedback and refused a running phase.
//
// Order: the operator's note is written into the WORKSPACE doc (epic_phases.doc_path
// — the run has ended and its lent copy was already returned; the next run lends
// the updated doc into its worktree), the phase is stamped `returned`, then its
// run restarts with Returned:true (bypasses the blocked re-run guard only;
// continues on the phase's own run branch). A refused start does NOT undo the
// first two steps: the note is the operator's, and `returned` is what the phase
// is waiting on — the refusal is answered with the same body runPhase gives,
// plus the phase's landing (state returned), so a later plain Run picks it up.
//
// 202 {status:"running", sessionUuid, action:"return", landing}; 503 phase runs
// not attached (nothing written); 409 phase-running while the service still
// holds the phase's slot (nothing written); 409 doc-unreadable when the note
// cannot be written; the run refusals runPhase maps otherwise. A refused start
// leaves the phase `returned`, and phaserun.StartWith admits ANY later start of a
// returned phase as the returned run.
func (h *Handler) returnPhase(w http.ResponseWriter, t landingTarget, feedback string) {
	if phaserunSvc == nil {
		writeClientErr(w, http.StatusServiceUnavailable, "phase runs not attached")
		return
	}
	// run_state turns terminal before the run's teardown is over: until the service
	// lets go of the slot, the run may still copy its lent doc back over the
	// workspace doc (which would drop the note written below) and its worktree is
	// still on the branch the returned run continues. Same answer as a running row.
	if phaserunSvc.InFlight(t.PhaseID) {
		writeConflict(w, codePhaseRunning,
			"this phase's last run is still finishing — wait a moment, then return it")
		return
	}
	if err := wsingest.AppendOperatorFeedback(t.DocPath, feedback, time.Now()); err != nil {
		writeConflict(w, codeDocUnreadable, "could not write the feedback into the phase doc: "+err.Error())
		return
	}
	if _, err := h.DB.Exec(`UPDATE epic_phases SET landing_state = ?, landing_error = NULL WHERE id = ?`,
		landingReturned, t.PhaseID); err != nil {
		writeErr(w, err)
		return
	}
	publishPlanUpdated(t.TaskID)

	uuid, startErr := phaserunSvc.StartWith(t.PhaseID, phaserun.StartOptions{Returned: true})
	landing, err := h.phaseLanding(t.PhaseID)
	if err != nil {
		writeErr(w, err)
		return
	}
	if startErr != nil {
		rec := &jsonCapture{header: http.Header{}}
		writePhaseStartRefusal(rec, startErr)
		rec.replayWith(w, "landing", landing)
		return
	}
	writeJSONStatus(w, http.StatusAccepted, map[string]any{
		"status": "running", "sessionUuid": uuid, "action": landActionReturn, "landing": landing,
	})
}

// jsonCapture buffers one JSON response so a caller can extend the body a shared
// writer produced (writeNoRunSlot and its siblings take no extra fields).
type jsonCapture struct {
	header http.Header
	status int
	body   bytes.Buffer
}

func (c *jsonCapture) Header() http.Header { return c.header }

func (c *jsonCapture) WriteHeader(status int) {
	if c.status == 0 {
		c.status = status
	}
}

func (c *jsonCapture) Write(b []byte) (int, error) {
	if c.status == 0 {
		c.status = http.StatusOK
	}
	return c.body.Write(b)
}

// replayWith writes the captured response to w with key=value added to its JSON
// object. A body that is not a JSON object is replayed unchanged.
func (c *jsonCapture) replayWith(w http.ResponseWriter, key string, value any) {
	status := c.status
	if status == 0 {
		status = http.StatusOK
	}
	var obj map[string]any
	if err := json.Unmarshal(c.body.Bytes(), &obj); err != nil || obj == nil {
		for k, v := range c.header {
			w.Header()[k] = v
		}
		w.WriteHeader(status)
		_, _ = w.Write(c.body.Bytes())
		return
	}
	obj[key] = value
	writeJSONStatus(w, status, obj)
}
