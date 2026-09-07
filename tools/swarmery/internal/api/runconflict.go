package api

// Run-conflict vocabulary shared by the phase-run and plan-run endpoints.
//
// POST …/run and DELETE …/branch answer 409 for a dozen different reasons, and
// they used to arrive as three different body shapes ({error}, {error,unmetDeps},
// {error,branch,commitsAhead}) that the client had to tell apart by sniffing
// which fields were present. That works until a new case adds a field, at which
// point every existing client silently mis-classifies it. A stable `code`
// discriminator makes the case explicit; the pre-existing fields all stay, so
// nothing that reads them breaks.
//
// The mapping from worktree sentinels to codes lives here too (worktreeConflict)
// rather than being spelled out in each switch: the phase and plan surfaces must
// answer the same condition with the same code, and four hand-maintained copies
// is exactly how they would drift apart.

import (
	"errors"
	"net/http"
	"time"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/runcore"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/worktree"
)

// 409 discriminators. Stable wire values — the web client switches on these, so
// they are part of the API contract and must not be reworded casually.
const (
	// Run admission (both surfaces).
	codeAlreadyRunning = "already-running"
	codeDepsUnmet      = "deps-unmet"
	codeDocUnreadable  = "doc-unreadable"
	codeNoProjectPath  = "no-project-path"
	// codeNoRepoRoot: the project HAS a path, it is simply not a git checkout, and
	// nothing the plan declares resolved to one either. Distinct from
	// codeNoProjectPath ("no path at all") because the fix is different: name the
	// repo in the phase doc's `Repo` header, or set mainApp in project.json. The
	// message carries the candidates that were checked — before this code existed,
	// a multi-repo project answered with git's raw "fatal: not a git repository",
	// which named nothing the user could act on.
	codeNoRepoRoot = "no-repo-root"
	// codeRepoOutsideProject: the phase doc's `Repo` header names a path that IS a
	// git checkout, but it lies outside the project and is not a registered
	// project. Distinct from codeNoRepoRoot because the fix is different again:
	// register that checkout as a project (the allow-list), or move the phase into
	// the plan of the project that owns it. Refused at admission — the old
	// behaviour dropped the declaration silently and ran the phase in the wrong
	// repository, once per retry.
	codeRepoOutsideProject = "repo-outside-project"
	// codePlanSpansRepos: a plan run executes in ONE worktree, and this plan's
	// unfinished phases name several repos. Plan-run-only; the phase surface has no
	// such condition (a phase is one repo by construction).
	codePlanSpansRepos = "plan-spans-repos"

	// Branch lifecycle — the phase surface's own gate, then the worktree sentinels
	// (see worktreeConflict). codeNoRunBranch is not a worktree condition: the phase
	// row simply has no branch STAMPED on it (migration 0043), so there is nothing to
	// delete and re-deriving a name is exactly the bug the stamp exists to prevent.
	codeNoRunBranch = "no-run-branch"

	// Branch lifecycle — the worktree sentinels (see worktreeConflict).
	codeBranchDirty      = "branch-dirty"
	codeBranchCheckedOut = "branch-checked-out"
	codeBranchIsHead     = "branch-is-head"
	codeBranchRefused    = "branch-refused"
	codeBranchBusy       = "branch-busy"
	codeDetachedHead     = "detached-head"
	codeBranchExists     = "branch-exists"
	codePathOccupied     = "path-occupied"

	// Board review loop (§3.1/§3.2) — conditions the phase surfaces never had.
	//
	// codeNoStartPoint: the card carries a run branch but no pinned base (0051
	// added start_point; rows dispatched before it have NULL). Distinct from
	// codeNoRunBranch — that card was never dispatched at all, this one was, and
	// the remedy is different: re-run it so admission pins a base, rather than
	// dispatch it for the first time.
	codeNoStartPoint = "no-start-point"
	// codeBaseUnreachable: the start point IS recorded but git can no longer
	// resolve it in this repo (a force-push or a gc dropped it). Nothing about the
	// card is wrong; the repo moved out from under the recorded base.
	codeBaseUnreachable = "base-unreachable"
	// codeBadColumn: the card is not in a column this action accepts (rerun wants
	// in_review or done). A guard on the request, not on the repo — which is why it
	// is not one of the branch codes above.
	codeBadColumn = "bad-column"

	// codeBranchLivePhase: the orphan-cleanup route was handed a branch that IS a
	// live phase row's run branch. That route exists to delete work stranded under
	// an id generation that no longer has a row; pointing it at a live phase would
	// destroy a run's branch behind that run's own back, which the phase-scoped
	// route refuses by construction (it derives the name, it cannot be told one).
	codeBranchLivePhase = "branch-live-phase"

	// codePlanRunning is codePhaseRunning's mirror, on the PHASE surface: a
	// whole-plan run is already driving these docs. The pair is what makes the
	// plan↔phase exclusion independent of which button the operator presses first.
	codePlanRunning = "plan-running"

	// Plan-run-only admission gates.
	codePhaseRunning = "phase-running"
	codePlanInactive = "plan-not-active"
	codeNoPhases     = "no-phases"
	codePlanComplete = "plan-complete"
	// codeSpecUncovered: plan/spec.md declares acceptance criteria that no phase
	// doc covers, so a whole-plan run would execute a plan that provably misses
	// promised outcomes. The message names the uncovered ids; the fix is a
	// **Covers:** line in a phase doc (or trimming the spec). Underscore, not
	// hyphen — the spec (SC-5) froze this wire value before the code existed.
	codeSpecUncovered = "spec_uncovered"

	// codeNoRunSlot: the daemon-wide run budget (SWARMERY_MAX_RUNS) is full. This
	// is the only 409 in this list that is purely TRANSIENT — nothing about the
	// plan, the phase or the project is wrong, and the same request will succeed
	// once a slot frees. The body carries `holders` (the runs in flight) and `max`,
	// because "no free run slot" on its own leaves an operator with nowhere to look.
	codeNoRunSlot = "no-free-run-slot"
)

// writeNoRunSlot renders a full run budget: a retriable 409 that names what is
// holding the pool. Shared by the phase-run and plan-run surfaces so both answer
// a busy machine with one shape the web client can switch on once.
func writeNoRunSlot(w http.ResponseWriter, err *runcore.NoSlotError) {
	holders := make([]map[string]any, 0, len(err.Holders))
	for _, h := range err.Holders {
		holders = append(holders, map[string]any{
			"engine": h.Engine,
			"id":     h.ID,
			"since":  h.Since.UTC().Format(time.RFC3339),
		})
	}
	writeConflictFields(w,
		codeNoRunSlot,
		"the daemon is already running as many agents as it is allowed to "+
			"(SWARMERY_MAX_RUNS). Nothing was started — retry when one finishes.",
		map[string]any{"holders": holders, "max": err.Max})
}

// writeConflict replies 409 {"error": msg, "code": code}.
func writeConflict(w http.ResponseWriter, code, msg string) {
	writeConflictFields(w, code, msg, nil)
}

// writeConflictFields is writeConflict plus the case's structured escape-hatch
// data (unmetDeps; branch/commitsAhead/base). `error` and `code` are written
// last so a stray key in extra can never shadow the discriminator.
func writeConflictFields(w http.ResponseWriter, code, msg string, extra map[string]any) {
	body := make(map[string]any, len(extra)+2)
	for k, v := range extra {
		body[k] = v
	}
	body["error"] = msg
	body["code"] = code
	writeJSONStatus(w, http.StatusConflict, body)
}

// worktreeConflict maps a worktree sentinel to its 409 code and an actionable
// message, or ok=false when err is not one of them.
//
// Every sentinel the branch lifecycle can raise is mapped HERE, including the
// ones no arm used to name: ErrBranchIsHead, ErrRefusedBranch and ErrBranchBusy
// all reached the generic `case err != nil` and surfaced as opaque 500s. Two of
// them are genuinely reachable — a user whose repo has swarm/phase-N checked out
// as HEAD hits ErrBranchIsHead on delete, and a leftover worktree under a
// different project slug (slug churn is real here) makes reclaim answer (0, nil)
// so Acquire then refuses with ErrBranchBusy.
//
// Callers must place this ABOVE their generic `case err != nil` arm: below it,
// it is unreachable code that no body assertion would ever catch — only a status
// assertion would.
func worktreeConflict(err error) (code, msg string, ok bool) {
	switch {
	case errors.Is(err, worktree.ErrBranchCheckedOut):
		return codeBranchCheckedOut, "the run branch is checked out in another worktree", true
	case errors.Is(err, worktree.ErrBranchIsHead):
		return codeBranchIsHead,
			"the run branch is the repo's currently checked-out branch — check out another branch first", true
	case errors.Is(err, worktree.ErrRefusedBranch):
		return codeBranchRefused,
			"refusing to operate on a branch outside the swarm/ namespace", true
	case errors.Is(err, worktree.ErrBranchBusy):
		return codeBranchBusy,
			"the run branch is busy in another worktree — remove it or finish the run that holds it", true
	case errors.Is(err, worktree.ErrDetachedHead):
		return codeDetachedHead,
			"the repo is on a detached HEAD, so the run branch cannot be measured against a base — check out a branch first", true
	// ErrBranchExists and ErrPathOccupied were BOTH unmapped, so an acquire that hit
	// either surfaced as an opaque 500 carrying git's raw sentence — which is how a
	// plan spent four retries acting on a diagnosis that named the wrong blocker
	// (2026-07-30). They are separated here for the same reason the sentinels are:
	// one is resolved by merging or deleting a branch, the other by freeing a path.
	case errors.Is(err, worktree.ErrBranchExists):
		return codeBranchExists,
			"the run branch already exists and holds commits — merge them or delete the branch, then retry", true
	case errors.Is(err, worktree.ErrPathOccupied):
		return codePathOccupied,
			"the run's worktree path is taken by a directory git does not track — free that path, then retry", true
	}
	return "", "", false
}
