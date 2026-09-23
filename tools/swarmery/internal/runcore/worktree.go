package runcore

import (
	"context"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/worktree"
)

// WorktreeManager is the subset of *worktree.Manager every run engine uses. An
// interface so the services can be unit-tested with a stub (the real Manager is
// itself Git-mockable, but stubbing at this level keeps their tests focused on
// admission logic, not git-list parsing). *worktree.Manager satisfies it.
//
// It lived in internal/dispatch until phase 2 of the execution-engine unification:
// phaserun and planrun imported dispatch for this type and nothing else, which made
// two engines depend on a third for a definition none of them owned.
// keepBranch is always true for dispatched runs — a task's swarm/<id> branch
// carries its Swarm-Task-Id commits, which verification (Phase 6) and the user
// need reachable after the worktree directory is reclaimed.
type WorktreeManager interface {
	Acquire(repoRoot, projectSlug, taskID string) (worktree.Acquired, error)
	// Path is the checkout Acquire WOULD derive for (projectSlug, taskID),
	// without creating or probing anything. The adoption path needs it: a run
	// that outlived the daemon left no worktree.Acquired behind, and the plan doc
	// lent into its checkout is the only copy holding the ticks and the
	// Completion Report the orphan actually wrote.
	Path(projectSlug, taskID string) (string, error)
	Remove(repoRoot string, a worktree.Acquired, keepBranch bool) error
	// ReclaimEmptyBranch deletes branch when it exists and holds no commits ahead
	// of the base, so a re-run can re-acquire the deterministic swarm/<taskID>
	// name instead of dying on ErrBranchExists — the leftover is a name nothing has
	// checked out, not a live conflict (every Remove above keeps the branch, so it
	// always survives). Returns the commits-ahead count when the
	// branch HAS work — the branch is then left untouched and the caller must not
	// destroy it; 0 means deleted or never existed. Errors when the repo has no
	// checked-out branch to measure against (worktree.ErrDetachedHead): a guessed
	// base is the one input a `branch -D` must never run on.
	ReclaimEmptyBranch(repoRoot, branch string) (int, error)
	// DeleteBranch force-deletes branch INCLUDING its commits, refusing while it
	// is checked out or is the repo's HEAD branch. Only for an explicit user
	// decision — never call it to make room for a re-run. The bool reports
	// whether a branch was actually there: deleting is idempotent, so a nil error
	// alone would let a no-op be reported to the user as a deletion.
	DeleteBranch(repoRoot, branch string) (existed bool, err error)
	// CommitsForTask returns the SHAs of commits carrying this task's
	// Swarm-Task-Id trailer. It is the dispatcher's only progress signal: the
	// count is what distinguishes a re-dispatch that advanced something from one
	// that did not. An error must never be read as zero commits — see
	// observedProgress, which keeps the two apart deliberately.
	CommitsForTask(repoRoot, taskID string) ([]string, error)
}

// Verifier is the auto-verification trigger seam (fusion phase 6). Declared here
// rather than in verify, and satisfied by *verify.Service, so `verify` can depend on
// the run engines' data deps (worktree/store) WITHOUT any of them importing verify —
// no import cycle. It moved here with WorktreeManager because dispatch.Verifier
// re-homes alongside the interface it is wired next to. Poke is non-blocking (verify
// spawns its own goroutine). Attached via dispatch's Service.Verifier field; nil ⇒
// auto-verification not wired (guarded).
type Verifier interface {
	Poke(taskID int64)
}

// PhaseVerifyRequest is everything the verifier needs to grade a finished phase run,
// assembled by phaserun at the one moment all of it is true at once: the run has
// ended, its worktree is still on disk, and nothing has reclaimed the branch yet.
//
// It is a struct rather than nine positional strings because the fields are all of
// one type and swapping two of them (Branch for StartPoint, say) compiles cleanly and
// then grades a diff of nothing.
type PhaseVerifyRequest struct {
	// PhaseID identifies the epic_phases row to stamp; WorkspaceTaskID is the epic
	// the Plans page subscribes to, so a stamped verdict can nudge the right plan.
	PhaseID, WorkspaceTaskID int64
	// Mode is the phase doc's own opt-in, off|normal|strict (wsingest.ParseDocVerify).
	// `off` — the default for every plan that does not ask — makes the whole call a
	// no-op, which is where "plans keep today's behaviour" is enforced.
	Mode string
	// WorktreePath is the run's live checkout, Branch the branch it committed to, and
	// StartPoint the SHA it was pinned to: the diff base, without which a verifier
	// compares the branch to itself and grades an empty change set as "nothing done".
	WorktreePath, Branch, StartPoint string
	// Title is the phase name; Prompt is the phase doc, which is where the phase's
	// acceptance criteria live. ProjectPath is the project root the Claude account
	// binding resolves from.
	Title, Prompt, ProjectPath string
}

// PhaseVerifier grades a finished phase run in place. Declared here for the same
// reason Verifier is — so phaserun never imports verify — and satisfied by
// *verify.Service.
//
// Unlike Verifier.Poke this BLOCKS: the caller must still hold the worktree when the
// verdict is reached, because the worktree is the only thing being graded. nil ⇒
// phase verification not wired (guarded), which is also what keeps phaserun's unit
// tests free of a verifier.
type PhaseVerifier interface {
	VerifyPhase(ctx context.Context, req PhaseVerifyRequest) error
}
