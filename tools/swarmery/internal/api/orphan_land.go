package api

// Push + open a change request for an ORPHAN run branch: a swarm/phase-<id>
// branch whose id matches no phase row (phasediag's orphan-branch blocker) —
// work stranded under a previous id generation. It is the keep-the-work twin of
// DELETE /api/epics/{taskId}/orphan-branch, gated by the same
// resolveOrphanBranch: the branch name is rebuilt from a validated id, a live
// phase's branch is refused, and the repo is the addressed epic's project.
//
// Unlike landPhase there is no phase row to carry a landing lifecycle, so this
// writes nothing to the database: the change request itself is the record, and
// the response carries its link. The refusals and the 422 mapping are
// landPhase's (fork workflow, push to base, provider unknown, auth, diverged…),
// and a push is never forced and never retried.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/repoprovider"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/repoprovider/credstore"
)

// orphanPRTitle names the change request after what the branch is: stranded
// work nobody's phase owns any more.
func orphanPRTitle(planTitle, branch string) string {
	if planTitle == "" {
		return "Recover stranded work from " + branch
	}
	return fmt.Sprintf("%s: recover stranded work from %s", planTitle, branch)
}

func orphanPRBody(branch string, taskID int64) string {
	return fmt.Sprintf("Work found on `%s`, a run branch whose phase id matches no phase of this plan "+
		"any more (stranded before the phase ids were stabilised). Review it before merging: no phase's "+
		"acceptance criteria describe it.\n\nSwarm-Task: %d\n", branch, taskID)
}

// POST /api/epics/{taskId}/orphan-branch/land?branch=swarm/phase-<id> {draft?}
//
// 200 {branch, base, url, number, terms}; 400 bad id / missing branch / bad
// JSON; 404 epic not found; 409 branch-refused / branch-live-phase /
// no-project-path / fork-workflow-unsupported / push-to-base-refused; 422 the
// landing machine failures with their `hint`.
func (h *Handler) landOrphanBranch(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Draft bool `json:"draft"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&body); err != nil && !errors.Is(err, io.EOF) {
		writeClientErr(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	taskID, branch, repoDir, ok := h.resolveOrphanBranch(w, r)
	if !ok {
		return
	}
	cfg := repoprovider.LoadConfig(repoDir)
	if cfg.ForkRemote != "" {
		writeConflict(w, codeForkUnsupported,
			"this project sets vcs.forkRemote ("+cfg.ForkRemote+"), and the fork workflow is not supported yet — "+
				"remove vcs.forkRemote to land to origin, or push and open the change request by hand")
		return
	}

	pushCmd := "git -C " + shellQuote(repoDir) + " push -u origin " + shellQuote(branch)
	var planTitle string
	_ = h.DB.QueryRow(`SELECT title FROM tasks WHERE id = ?`, taskID).Scan(&planTitle)
	title := orphanPRTitle(planTitle, branch)

	// Outlives the request on purpose, as in landPhase: a browser navigating away
	// must not kill a change-request create that may already have succeeded.
	ctx := context.WithoutCancel(r.Context())

	provider, det, err := landProvider(ctx, repoDir, cfg)
	if err != nil {
		if errors.Is(err, repoprovider.ErrBinaryMissing) {
			writeLandingUnprocessable(w, codeBinaryMissing, "git not found",
				"`git` is not on the daemon's PATH. Install git, restart the daemon, then try again.",
				landDetail(err))
			return
		}
		writeLandingUnprocessable(w, codeNoRemote, "no origin remote",
			"this repo has no `origin` to push to. Add one, then try again:\n"+
				"git -C "+shellQuote(repoDir)+" remote add origin <url>\n"+pushCmd,
			landDetail(err))
		return
	}
	if provider == nil {
		writeLandingUnprocessable(w, codeProviderUnknown, "repository provider unknown",
			"the host of this repo's `origin` ("+det.Remote.Host+") is not a recognised code host. "+
				"Set `vcs.provider` (\"github\" or \"gitlab\") in .claude/project.json, then try again — "+
				"or push by hand:\n"+pushCmd,
			credstore.Redact(det.Remote.URL))
		return
	}

	base := resolveLandingBase(ctx, landExec, repoDir, cfg, "")
	if base.isBase(branch, cfg) && !cfg.AllowPushToBase {
		writeConflictFields(w, codePushToBaseRefused,
			"the branch "+branch+" is the base branch, so this would push straight onto it.",
			map[string]any{"branch": branch, "base": base.Name})
		return
	}

	prCmd := changeRequestCmd(det.Kind, branch, base.PRBase, title, body.Draft)
	cli, terms := landCLIFor(det.Kind), provider.Terms()
	// PhaseID 0: no phase row exists, so writeLandFailure stamps no landing_error.
	failTarget := landingTarget{TaskID: taskID, RunBranch: branch}

	target := repoprovider.Target{RepoDir: repoDir, RemoteName: repoprovider.DefaultRemote, Remote: det.Remote}
	if err := provider.Push(ctx, target, branch); err != nil {
		h.writeLandFailure(w, failTarget, det, err, landFailureHints{
			binary: "`git` is not on the daemon's PATH. Install git, restart the daemon, then try again.",
			other:  "the branch could not be pushed. Resolve it and try again, or push by hand:\n" + pushCmd,
			push:   pushCmd,
			repo:   repoDir,
		})
		return
	}
	ref, err := provider.OpenChangeRequest(ctx, target, repoprovider.ChangeRequest{
		Head:  branch,
		Base:  base.PRBase,
		Title: title,
		Body:  orphanPRBody(branch, taskID),
		Draft: body.Draft,
	})
	if err != nil {
		h.writeLandFailure(w, failTarget, det, err, landFailureHints{
			pushed: true,
			binary: "the branch is pushed, but the " + cli.Name + " (`" + cli.Bin + "`) is not on PATH so the " +
				terms.Change + " was not opened. Install it (" + cli.Install + "), then try again — or open it by hand:\n" + prCmd,
			other: "the branch is pushed, but the " + terms.Change + " was not opened. Open it by hand:\n" + prCmd,
			push:  pushCmd,
			repo:  repoDir,
		})
		return
	}
	baseName := base.PRBase
	if baseName == "" {
		baseName = base.Name
	}
	var number any // null when the URL carried no number, never a fake 0
	if ref.Number > 0 {
		number = ref.Number
	}
	writeJSON(w, map[string]any{
		"branch": branch, "base": baseName, "url": ref.URL, "number": number, "terms": terms,
	}, nil)
}
