package api

// POST /api/epics/{taskId}/phases/{phaseId}/landing/refresh — read a landed
// phase's change request NOW (phase-landing plan, phase 7, SC-13).
//
// The daemon's status poller (internal/repoprovider/landpoll, started in
// cmd/swarmery) reads every open PR/MR on a ticker; this endpoint is the
// impatient click beside it. Both go through the same landpoll.Poller, so a
// refresh stores exactly what a tick would: the normalized status and its
// time, the pr_open → merged flip (with landed_at, the merge time), and a
// cleared — or, on failure, a stamped — landing_error. A not-authenticated
// read also marks the project's VCS auth expired (MarkVcsAuthExpired), so the
// project banner says so without waiting for its cache TTL.
//
//	200 landingDTO                     the phase's landing after the read
//	404                                unknown phase, or a phase of another epic
//	409 {code:"no-change-request"}     landing_state is not pr_open|merged
//	422 {error, code, hint, detail}    the host could not be asked; `detail` is
//	                                   redacted, `code` is the landing_error code
//	                                   (not-authenticated, binary-missing,
//	                                   no-remote, provider-unknown, status-failed)

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"net/url"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/phaserun"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/repoprovider"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/repoprovider/credstore"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/repoprovider/landpoll"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/repoprovider/providers"
)

// landStatusExec is the process boundary of a landing refresh: the remote
// lookup behind Detect and the provider's `gh pr view` / `glab mr view`. A
// package var for the same reason landExec is one: tests swap it for a
// repoprovider.FakeExec, with a restore in t.Cleanup.
var landStatusExec repoprovider.Exec = repoprovider.OSExec{}

// PublishPlanUpdated announces that a workspace task's plan changed (the
// plan_updated WS event the Plans page refetches on). Exported for the daemon's
// landing status poller (cmd/swarmery), which lives outside this package; a
// no-op while no bus is attached.
func PublishPlanUpdated(taskID int64) { publishPlanUpdated(taskID) }

// newLandPoller builds the poller a refresh runs, over the same seams the land
// handler uses: the attached phase-run service's RunRoot (a DB-only Service
// without one, as landingRepoDir does), providers.Factory over the daemon's
// credential env, the plan_updated publisher, the auth-expired hook, and the
// Handler's plan review hook (a merge seen by a refresh may close the plan).
func (h *Handler) newLandPoller() *landpoll.Poller {
	ex := landStatusExec
	svc := phaserunSvc
	if svc == nil {
		svc = &phaserun.Service{DB: h.DB}
	}
	return &landpoll.Poller{
		DB: h.DB,
		Factory: func(k repoprovider.Kind) (repoprovider.Provider, error) {
			return providers.Factory(k, ex, credstore.Env)
		},
		Exec:          ex,
		RepoDir:       svc.RunRoot,
		Publish:       publishPlanUpdated,
		OnAuthExpired: MarkVcsAuthExpired,
		PlanReview:    h.PlanReview,
	}
}

// refreshPhaseLanding — POST /api/epics/{taskId}/phases/{phaseId}/landing/refresh.
// requireLocalOrigin. See the file comment for the answers.
func (h *Handler) refreshPhaseLanding(w http.ResponseWriter, r *http.Request) {
	taskID, phaseID, ok := parseLandingParams(w, r)
	if !ok {
		return
	}
	var (
		wsTaskID        int64
		provider, prURL sql.NullString
	)
	err := h.DB.QueryRow(`SELECT workspace_task_id, pr_provider, pr_url FROM epic_phases WHERE id = ?`,
		phaseID).Scan(&wsTaskID, &provider, &prURL)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && wsTaskID != taskID) {
		writeClientErr(w, http.StatusNotFound, "phase not found")
		return
	}
	if err != nil {
		writeErr(w, err)
		return
	}

	// The read outlives the request (as land's tool calls do): once `gh` has
	// answered, the poller's write must not be lost to a closed tab. The
	// provider bounds the call itself (repoprovider.NetTimeout).
	ctx := context.WithoutCancel(r.Context())
	_, _, err = h.newLandPoller().RefreshOne(ctx, phaseID)
	switch {
	case err == nil:
	case errors.Is(err, landpoll.ErrPhaseNotFound):
		writeClientErr(w, http.StatusNotFound, "phase not found")
		return
	case errors.Is(err, landpoll.ErrNoChangeRequest):
		writeConflict(w, codeNoChangeRequest,
			"this phase has no change request to refresh — open one from the Review tab first")
		return
	default:
		// The poller already stamped landing_error and, for a rejected
		// credential, marked the project's auth expired.
		writeRefreshFailure(w, err, repoprovider.Kind(provider.String), hostOf(prURL.String), prURL.String)
		return
	}

	landing, err := h.phaseLanding(phaseID)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, landing, nil)
}

// writeRefreshFailure maps a failed read onto its 422. code and detail come
// from landpoll.Classify — the same text the poller stamped as landing_error,
// redacted there.
func writeRefreshFailure(w http.ResponseWriter, err error, kind repoprovider.Kind, host, prURL string) {
	code, detail := landpoll.Classify(err)
	cli := landCLIFor(kind)
	byHand := ""
	if prURL != "" {
		byHand = "\nOr check it on the host: " + prURL
	}
	switch code {
	case landpoll.CodeNotAuthenticated:
		hint := "the code host rejected the credentials. Log in, then refresh again."
		if login := vcsCliLogin(kind, host); login != "" {
			hint = "the code host (" + host + ") rejected the credentials. Log in, then refresh again:\n" + login
		}
		writeLandingUnprocessable(w, code, "not authenticated", hint, detail)
	case landpoll.CodeBinaryMissing:
		writeLandingUnprocessable(w, code, "required CLI not found",
			"the "+cli.Name+" (`"+cli.Bin+"`) or `git` is not on the daemon's PATH. Install it ("+cli.Install+
				"), restart the daemon, then refresh again."+byHand,
			detail)
	case landpoll.CodeNoRemote:
		writeLandingUnprocessable(w, code, "no origin remote",
			"the phase's repo has no `origin`, so its code host cannot be asked. Add one, then refresh again."+byHand,
			detail)
	case landpoll.CodeProviderUnknown:
		writeLandingUnprocessable(w, code, "repository provider unknown",
			"the host of this repo's `origin` is not a recognised code host. Set `vcs.provider` "+
				"(\"github\" or \"gitlab\") in .claude/project.json, then refresh again."+byHand,
			detail)
	default:
		writeLandingUnprocessable(w, code, "status read failed",
			"the change request's status could not be read. Try again in a moment."+byHand,
			detail)
	}
}

// hostOf is the host of a change-request URL, "" when it has none.
func hostOf(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	return u.Hostname()
}
