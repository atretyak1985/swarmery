package api

// Board review loop — the exits (board redesign phase 3, §3.2).
//
// in_review is where a dispatched card waits for a human verdict. Before this
// file the only way out was dragging it, which made `done` a statement about the
// board rather than about the work: the branch was still unmerged, the feedback
// had nowhere to go, and a rejected card could only be re-dispatched by an
// unobvious drag to todo (done→in_progress is refused outright, see
// legalTransition). The three exits here make the verdict do something:
//
//	land    — push the branch, open a PR, move the card to done
//	rerun   — append the reviewer's feedback to the prompt and re-queue it
//	discard — reclaim the worktree, delete the branch, archive the card
//
// Re-verify is deliberately NOT re-implemented here: POST /api/tasks/{id}/verify
// (verify.go) already exists with the exact preflight this surface would need
// (404 unknown / 422 no worktree / 409 already running / 503 not attached). The
// review UI calls that route.
//
// Every handler is requireLocalOrigin (they all mutate, two of them destroy) and
// board-scoped (source='queue'). Land talks to the code host only through
// internal/repoprovider, resolved by the landProvider factory in tasks_diff.go,
// so `git push` and `gh pr create` are fakeable in tests (repoprovider.FakeExec).

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/repoprovider"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/repoprovider/credstore"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/repoprovider/github"
)

// reviewFeedbackMax bounds the feedback a rerun appends. Generous — it is a
// review note, not a document — but the prompt column is what a headless agent
// is handed, so it cannot be unbounded.
const reviewFeedbackMax = 20 << 10 // 20 KB

// activeInDispatch reports whether the dispatcher currently holds a live run for
// the card. The board columns are the durable truth, but they lag the in-memory
// set by the width of one exit path — and re-queueing or archiving a card the
// dispatcher still owns races its own state write.
func activeInDispatch(id int64) bool {
	return dispatchSvc != nil && dispatchSvc.IsActive(id)
}

// POST /api/board/tasks/{id}/rerun {"feedback": "…"} — send the card back around
// with the reviewer's notes appended to its prompt.
//
// The amendment and the re-queue are ONE statement so a crash between them
// cannot leave a card queued with un-amended instructions — it would then re-run
// the identical prompt and produce the identical work. The WHERE clause carries
// the column guard, which is also what gives a `done` card its first legal
// re-dispatch path.
func (h *Handler) rerunBoardTask(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeClientErr(w, http.StatusBadRequest, "invalid task id")
		return
	}
	var body struct {
		Feedback string `json:"feedback"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&body); err != nil {
		writeClientErr(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	feedback := strings.TrimSpace(body.Feedback)
	if feedback == "" {
		writeClientErr(w, http.StatusBadRequest,
			"feedback is required — a re-run with no notes would repeat the same work")
		return
	}
	if len(feedback) > reviewFeedbackMax {
		writeClientErr(w, http.StatusBadRequest,
			fmt.Sprintf("feedback exceeds %d bytes", reviewFeedbackMax))
		return
	}
	// 404 before 409: an unknown id and a card in the wrong column are different
	// mistakes, and the single UPDATE below cannot tell them apart from 0 rows.
	if _, ok := h.loadReviewTarget(w, id); !ok {
		return
	}
	if activeInDispatch(id) {
		writeConflict(w, codeAlreadyRunning,
			"the dispatcher is still running this card — wait for it to land in review first")
		return
	}

	now := time.Now().UTC().Format(boardTSFormat)
	res, err := h.DB.Exec(`
		UPDATE tasks
		   SET prompt = prompt || char(10) || char(10) ||
		                '## Reviewer feedback (' || ? || ')' || char(10) || ?,
		       board_column='todo', status='queued',
		       verify_verdict=NULL, verify_detail=NULL,
		       dispatch_error=NULL, column_moved_at=?
		 WHERE id=? AND source='queue' AND board_column IN ('in_review','done')`,
		now, feedback, now, id)
	if err != nil {
		writeErr(w, err)
		return
	}
	if n, _ := res.RowsAffected(); n == 0 {
		writeConflict(w, codeBadColumn,
			"only a card in review or done can be re-run with feedback")
		return
	}
	d, err := h.boardTaskByID(id)
	if err != nil {
		writeErr(w, err)
		return
	}
	publishTaskUpdated(id)
	pokeDispatch()
	writeJSON(w, d, nil)
}

// POST /api/board/tasks/{id}/discard — throw the work away: reclaim the
// worktree, delete the run branch AND its commits, archive the card.
//
// Order is load-bearing. The worktree must go first: a branch checked out
// somewhere cannot be deleted (worktree.ErrBranchCheckedOut), so reclaiming is
// what makes the delete possible rather than a 409. The archive comes last so a
// failed delete leaves a card the user can retry, not an archived card whose
// branch quietly survived.
//
// Deleting is idempotent — a branch already gone is still a success — mirroring
// the `existed` semantics of the phase-run branch route (phaserun.go). The
// swarm/ namespace guard lives inside DeleteBranch, so this route cannot be
// pointed at anything it did not create.
func (h *Handler) discardBoardTask(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeClientErr(w, http.StatusBadRequest, "invalid task id")
		return
	}
	tgt, ok := h.loadReviewTarget(w, id)
	if !ok {
		return
	}
	if tgt.BoardColumn == "in_progress" || tgt.Status == "running" || activeInDispatch(id) {
		writeConflict(w, codeAlreadyRunning,
			"this card is running — stop it first (move it to done or archived), then discard")
		return
	}

	// Reclaim before delete. Best-effort inside the dispatcher: a failure is
	// logged there, and the DeleteBranch below will refuse loudly if the checkout
	// is genuinely still holding the branch.
	if dispatchSvc != nil {
		dispatchSvc.RemoveWorktreeFor(id)
	}

	deleted := false
	if tgt.Branch != "" && tgt.ProjectPath != "" && dispatchSvc != nil && dispatchSvc.Wt != nil {
		existed, err := dispatchSvc.Wt.DeleteBranch(tgt.ProjectPath, tgt.Branch)
		if code, msg, isConflict := worktreeConflict(err); isConflict {
			writeConflict(w, code, msg)
			return
		}
		if err != nil {
			writeErr(w, err)
			return
		}
		deleted = existed
	}

	// Re-guarded at the write: the "not running" check above is a read, and the
	// dispatcher's admit() can move a todo card to in_progress in the gap. admit
	// only ever promotes FROM 'todo', so refusing to archive an in_progress row
	// here closes the race — a 0-row update means the card was claimed mid-flight
	// and the discard must not clobber the live run.
	now := time.Now().UTC().Format(boardTSFormat)
	res, err := h.DB.Exec(`
		UPDATE tasks SET board_column='archived', status='cancelled', column_moved_at=?
		 WHERE id=? AND source='queue' AND board_column != 'in_progress'`, now, id)
	if err != nil {
		writeErr(w, err)
		return
	}
	if n, _ := res.RowsAffected(); n == 0 {
		writeConflict(w, codeAlreadyRunning,
			"this card was picked up by the dispatcher while discarding — stop it first, then discard")
		return
	}
	// applyTerminalColumnEffects is NOT called here: its 'archived' arm is exactly
	// the RemoveWorktreeFor above, which had to run BEFORE the branch delete. A
	// second call would be a no-op (worktree_path is already NULL) but would
	// misstate the ordering this handler depends on.
	// A discarded card must not leave its micro-plan sitting in working/ as an
	// active plan. Files only, and best-effort: the card is already archived, so a
	// failure here must not turn a completed discard into an error the user has to
	// retry — and wsingest re-derives the plan's rows from the zone on its next pass,
	// which is what makes moving the dir sufficient.
	h.archiveMicroPlanDir(id)

	d, err := h.boardTaskByID(id)
	if err != nil {
		writeErr(w, err)
		return
	}
	publishTaskUpdated(id)
	pokeDispatch()
	writeJSON(w, map[string]any{"deleted": deleted, "branch": tgt.Branch, "task": d}, nil)
}

// archiveMicroPlanDir moves a discarded card's micro-plan from working/ to
// archive/, stamping its README the way the plan lifecycle's own archive action
// does (status done + completion date), so the Plans page stops showing it as
// active work nobody is doing.
//
// Deliberately file-level only: wsingest owns workspace task ROWS and re-derives
// status and archived_at from the zone the dir sits in, so moving the dir IS the
// state change. Writing the rows here as well would make two writers of the same
// projection.
func (h *Handler) archiveMicroPlanDir(cardID int64) {
	var dir sql.NullString
	if err := h.DB.QueryRow(`SELECT workspace_dir FROM tasks WHERE id = ?`, cardID).Scan(&dir); err != nil {
		log.Printf("warning: discard: read workspace_dir (card %d): %v", cardID, err)
		return
	}
	if !dir.Valid || dir.String == "" {
		return // no micro-plan (dispatched before the feature, or minting disabled)
	}
	if fi, err := os.Stat(dir.String); err != nil || !fi.IsDir() {
		return // already moved, or never made it to disk
	}

	today := time.Now().UTC().Format("2006-01-02")
	if err := rewriteCardReadme(dir.String, func(text string) string {
		text = upsertCardStatus(text, "done")
		return doneDateEmptyRe.ReplaceAllString(text, "${1}"+today)
	}); err != nil {
		log.Printf("warning: discard: stamp micro-plan README (card %d): %v", cardID, err)
	}
	moved, err := moveTaskZone(dir.String, "working", "archive")
	if err != nil {
		log.Printf("warning: discard: archive micro-plan dir (card %d): %v", cardID, err)
		return
	}
	// Keep the join pointing at where the dir actually is now.
	if _, err := h.DB.Exec(`UPDATE tasks SET workspace_dir = ? WHERE id = ?`, moved, cardID); err != nil {
		log.Printf("warning: discard: repoint workspace_dir (card %d): %v", cardID, err)
	}
}

// landBoardTask handles POST /api/board/tasks/{id}/land {"draft": false}: push
// the run branch and open a PR for it, then move the card to done.
//
// Both failure modes answer 422 with a `hint` carrying the exact commands to run
// by hand. That is the difference between "the daemon could not finish" and "you
// are stuck": a missing origin remote or a missing `gh` is a machine-setup fact
// the daemon must not pretend to fix, and the work is already committed on the
// branch either way.
//
// The card only reaches done AFTER the PR exists. A card marked done whose
// branch was never pushed is the one outcome this endpoint must never produce.
func (h *Handler) landBoardTask(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeClientErr(w, http.StatusBadRequest, "invalid task id")
		return
	}
	var body struct {
		Draft bool `json:"draft"`
	}
	// An empty body is the common case (land with defaults), so a decode failure
	// on EOF is not an error.
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&body); err != nil && err != io.EOF {
		writeClientErr(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	tgt, ok := h.loadReviewTarget(w, id)
	if !ok {
		return
	}
	if tgt.Branch == "" {
		writeConflict(w, codeNoRunBranch,
			"this card has no run branch — there is nothing to push")
		return
	}
	if tgt.ProjectPath == "" {
		writeConflict(w, codeNoProjectPath, "project has no known path")
		return
	}
	if tgt.BoardColumn == "in_progress" || tgt.Status == "running" || activeInDispatch(id) {
		writeConflict(w, codeAlreadyRunning,
			"this card is still running — let it reach review before landing it")
		return
	}

	pushCmd := "git -C " + tgt.ProjectPath + " push -u origin " + tgt.Branch
	prCmd := "gh pr create --head " + tgt.Branch + " --title " + strconv.Quote(tgt.Title)

	// The tool calls outlive the HTTP request on purpose: a browser that
	// navigates away mid-push must not kill a `gh pr create` that may already
	// have opened the PR, leaving a card that is not done over a PR that exists.
	// Each call carries its own budget inside the provider (repoprovider.NetTimeout).
	ctx := context.WithoutCancel(r.Context())

	// 1. Origin must exist, and name a host the daemon can land to, before
	//    anything is attempted. Detected separately so the "no remote configured"
	//    case gets its own sentence rather than arriving as a generic push failure
	//    the user has to decode — and so a host this phase cannot open a change
	//    request on is refused BEFORE the branch is pushed.
	provider, det, err := landProvider(ctx, tgt.ProjectPath)
	if err != nil {
		writeUnprocessable(w, "no origin remote",
			"this repo has no `origin` to push to. Add one, then land again — or push and open the PR by hand:\n"+
				pushCmd+"\n"+prCmd,
			landDetail(err))
		return
	}
	switch det.Kind {
	case repoprovider.KindGitHub:
	case repoprovider.KindGitLab:
		// Temporary: the GitLab provider lands in a later phase, which replaces
		// this arm (TestLandGitLabNotSupportedYet pins it until then).
		writeUnprocessable(w, "gitlab not supported yet",
			"this repo's `origin` is a GitLab host ("+det.Remote.Host+"), and landing to GitLab is not supported yet. "+
				"Push and open the merge request by hand:\n"+pushCmd+"\n"+
				"glab mr create --source-branch "+tgt.Branch+" --title "+strconv.Quote(tgt.Title),
			credstore.Redact(det.Remote.URL))
		return
	default:
		writeUnprocessable(w, "repository provider unknown",
			"the host of this repo's `origin` ("+det.Remote.Host+") is not a recognised code host. "+
				"Set `vcs.provider` (\"github\" or \"gitlab\") in .claude/project.json, then land again — "+
				"or push and open the PR by hand:\n"+pushCmd+"\n"+prCmd,
			credstore.Redact(det.Remote.URL))
		return
	}
	target := repoprovider.Target{
		RepoDir: tgt.ProjectPath, RemoteName: repoprovider.DefaultRemote, Remote: det.Remote,
	}
	if err := provider.Push(ctx, target, tgt.Branch); err != nil {
		writeUnprocessable(w, "push failed",
			"the branch could not be pushed. Resolve it and land again, or push by hand:\n"+pushCmd,
			landDetail(err))
		return
	}

	// 2. The branch is already pushed at this point, so every hint below has to
	//    tell the user the ONE step left. The provider checks `gh` on PATH before
	//    invoking it, which is what turns a missing CLI into its own sentence.
	ref, err := provider.OpenChangeRequest(ctx, target, repoprovider.ChangeRequest{
		Head: tgt.Branch, Title: tgt.Title, Body: landPRBody(tgt), Draft: body.Draft,
	})
	switch {
	case errors.Is(err, repoprovider.ErrBinaryMissing):
		writeUnprocessable(w, "gh not found",
			"the branch is pushed, but the GitHub CLI is not on PATH so the PR was not opened. "+
				"Install `gh` and land again, or open it by hand:\n"+prCmd,
			landDetail(err))
		return
	case errors.Is(err, github.ErrNoURL):
		// gh exited 0 but printed no URL on either stream: something changed
		// under us, and claiming a PR exists would be worse than saying so.
		writeUnprocessable(w, "gh pr create returned no URL",
			"the branch is pushed and `gh` exited 0, but printed no pull-request URL. Check it by hand:\n"+prCmd,
			landDetail(err))
		return
	case err != nil:
		writeUnprocessable(w, "gh pr create failed",
			"the branch is pushed, but the PR was not created. Open it by hand:\n"+prCmd,
			landDetail(err))
		return
	}
	prURL := ref.URL

	// 3. Only now is the card done — and it goes through the SAME side effects the
	//    →done PATCH performs (worktree reclaimed, branch kept, plan phase ticked),
	//    via the helper both paths share.
	now := time.Now().UTC().Format(boardTSFormat)
	if _, err := h.DB.Exec(`
		UPDATE tasks SET board_column='done', status='done', result_note=?,
		                 dispatch_error=NULL, finished_at=?, column_moved_at=?
		 WHERE id=? AND source='queue'`, prURL, now, now, id); err != nil {
		writeErr(w, err)
		return
	}
	h.applyTerminalColumnEffects(id, "done")
	d, err := h.boardTaskByID(id)
	if err != nil {
		writeErr(w, err)
		return
	}
	publishTaskUpdated(id)
	pokeDispatch()
	writeJSON(w, map[string]any{"prUrl": prURL, "branch": tgt.Branch, "task": d}, nil)
}

// landDetail is the `detail` of a land 422: the tool's own output (stderr, or
// the process error when it said nothing), bounded, and passed through
// credstore.Redact so a token a CLI echoed never reaches the HTTP body. A
// classified *repoprovider.Error already carries exactly that text; anything
// else (ErrNoURL's captured output, a refused ref name) is reduced to its message.
func landDetail(err error) string {
	var pe *repoprovider.Error
	if errors.As(err, &pe) {
		return credstore.Redact(pe.Detail)
	}
	msg := err.Error()
	if errors.Is(err, github.ErrNoURL) {
		msg = strings.TrimPrefix(msg, github.ErrNoURL.Error()+": ")
	}
	return credstore.RedactedTail("", errors.New(msg), repoprovider.OutputTail)
}

// writeUnprocessable replies 422 {error, hint, detail}. The shape is specific to
// the land path and deliberately not folded into writeConflictFields: a 409 says
// "the server refuses given its state", while these say "your machine is missing
// something and here is the command that finishes the job by hand". `detail`
// carries the (redacted) tool output; `hint` is the part meant to be pasted.
func writeUnprocessable(w http.ResponseWriter, msg, hint, detail string) {
	writeJSONStatus(w, http.StatusUnprocessableEntity, map[string]any{
		"error": msg, "hint": hint, "detail": detail,
	})
}

// landPRBody renders the PR description: what the card asked for, the id that
// ties commits back to it (the same trailer key the dispatcher stamps), and the
// verification verdict if one was recorded. Pure; unit-tested.
func landPRBody(t reviewTarget) string {
	var b strings.Builder
	summary := strings.TrimSpace(t.Prompt)
	if len(summary) > reviewPRBodyChars {
		summary = strings.TrimSpace(summary[:reviewPRBodyChars]) + "…"
	}
	if summary != "" {
		b.WriteString(summary)
		b.WriteString("\n\n")
	}
	if t.VerifyVerdict != "" {
		b.WriteString("Verification: ")
		b.WriteString(t.VerifyVerdict)
		if d := strings.TrimSpace(t.VerifyDetail); d != "" {
			b.WriteString(" — ")
			b.WriteString(d)
		}
		b.WriteString("\n\n")
	}
	b.WriteString("Swarm-Task-Id: ")
	b.WriteString(t.ExternalID)
	b.WriteString("\n")
	return b.String()
}
