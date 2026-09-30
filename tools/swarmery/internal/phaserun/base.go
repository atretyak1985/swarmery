package phaserun

// Base resolution — which commit a phase run starts on.
//
// worktree.Acquire pins every worktree to the repo's current branch tip, while the
// dependency gate (depSatisfied) accepts TICKED CRITERIA. The two disagree whenever
// a dependency finished on its run branch and nobody merged it: the gate opens, and
// the phase starts on a tree that does not contain the work it depends on. The
// blocked reasons of 2026-09-27 are that gap, in the executors' own words —
// "contracts exist only on the unmerged swarm/phase-26498", "this worktree is based
// on a commit from before PR #108".
//
// resolveBase closes it without the daemon ever merging anything: it looks at the
// run branches of the phase's DIRECT dependencies and answers one of three things —
// start where you always did (everything is merged), start on top of this one
// dependency branch (it contains all the others), or do not start (the unmerged
// branches have diverged, and only the operator can say how they combine).

import (
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/worktree"
)

// ErrDepsUnmerged: the phase's dependencies are complete, but their work sits on
// run branches that are neither merged into the repo's base branch nor contained
// in one another, so there is no single commit to start from (409). Returned as a
// *DepsUnmergedError, which errors.Is-matches this sentinel.
var ErrDepsUnmerged = errors.New("phase dependencies are on unmerged, diverged run branches")

// ErrCannotStack: base resolution chose a dependency branch to start on, but the
// wired worktree manager cannot pin a worktree anywhere except the repo's current
// branch tip. Refused rather than started on the wrong tree. Unreachable with
// *worktree.Manager, which is what the daemon wires.
var ErrCannotStack = errors.New("worktree manager cannot start a run on a dependency branch")

// DepsUnmergedError names the branches the operator has to merge and the branch
// they were measured against, so the api's 409 body and the UI say exactly what to
// do instead of "dependencies unmerged".
type DepsUnmergedError struct {
	// Branches are the unmerged dependency run branches, sorted.
	Branches []string
	// Base is the repo's checked-out branch — what "merged" was measured against.
	// Empty on a detached HEAD, where there is a tip but no name for it.
	Base string
}

func (e *DepsUnmergedError) Error() string {
	base := e.Base
	if base == "" {
		base = "the repo's current HEAD"
	}
	return fmt.Sprintf("this phase depends on run branches that are not merged into %s and have diverged from one another: %s — "+
		"merge them into %s (or one into the other), then run the phase again",
		base, strings.Join(e.Branches, ", "), base)
}

func (e *DepsUnmergedError) Is(target error) bool { return target == ErrDepsUnmerged }

// baseResolution is what resolveBase found out about where a run would start.
type baseResolution struct {
	// StartRef is the SHA the worktree must be pinned to, or "" for the repo's
	// current branch tip — today's start point, and the answer whenever every
	// dependency is merged. A SHA rather than the branch name, so the commit
	// resolved here is the commit acquired: a dependency branch that moves between
	// the two cannot change what the run starts on.
	StartRef string
	// StackedOn is the dependency run branch StartRef is the tip of; "" when the
	// run is not stacked. It is what the prompt tells the executor.
	StackedOn string
	// BaseTip is the SHA of the repo's current branch tip; BaseBranch its name
	// ("" on a detached HEAD). Both "" when git could not be asked.
	BaseTip    string
	BaseBranch string
	// DepTips are the tips of the dependency run branches that still exist, merged
	// or not, sorted. The blocked fingerprint hashes them: a dependency branch
	// that gained a commit is a changed situation even when it changes nothing
	// about where the run starts.
	DepTips []string
}

// fingerprintBase is the commit the next run would start from: the dependency tip
// when stacked, the repo's branch tip otherwise.
func (b baseResolution) fingerprintBase() string {
	if b.StartRef != "" {
		return b.StartRef
	}
	return b.BaseTip
}

// depBranch is one dependency run branch that still exists, with its tip.
type depBranch struct {
	name string
	tip  string
}

// resolveBase decides where a phase run starts, from the run branches of its
// direct dependencies:
//
//  1. baseTip is the repo's current branch tip — where every run started until now.
//  2. A dependency branch is MERGED when its tip is an ancestor of baseTip, or when
//     merging it into baseTip would change nothing (a squash merge leaves no
//     ancestry, only the content). A branch that no longer exists counts as merged:
//     a deleted run branch is the operator saying its work is accounted for.
//  3. Nothing unmerged ⇒ StartRef "" (unchanged behaviour).
//  4. Exactly one unmerged tip that every other unmerged tip is an ancestor of ⇒
//     start there: a linear chain, where the last branch already carries the rest.
//  5. Otherwise ⇒ *DepsUnmergedError naming the branches.
//
// The resolution is returned even alongside a *DepsUnmergedError, with BaseTip and
// DepTips filled: the blocked fingerprint needs them whether or not a start point
// exists.
//
// git nil ⇒ the zero resolution and no error: without a git seam nothing can be
// asked, and the run starts where it always did. That is the unit tests' state;
// the daemon always wires the seam.
//
// Read-only with one exception the design accepts: `merge-tree --write-tree`
// writes the merged tree into the object store. It moves no ref and touches no
// working tree, and the objects are unreachable garbage the next gc collects.
func resolveBase(git worktree.Git, repoRoot string, depBranches []string) (baseResolution, error) {
	var res baseResolution
	if git == nil || repoRoot == "" {
		return res, nil
	}
	baseTip, baseBranch, err := currentTip(git, repoRoot)
	if err != nil {
		if len(depBranches) == 0 {
			// Nothing to stack on and nothing to measure: leave the failure to
			// Acquire, which reports a non-repo as itself (worktree.ErrNotARepo).
			return res, nil
		}
		return res, fmt.Errorf("resolve run base: %w", err)
	}
	res.BaseTip, res.BaseBranch = baseTip, baseBranch

	var unmerged []depBranch
	for _, name := range uniqueSorted(depBranches) {
		tip, exists, err := branchTip(git, repoRoot, name)
		if err != nil {
			return res, fmt.Errorf("resolve run base: %w", err)
		}
		if !exists {
			continue // a deleted run branch counts as merged
		}
		res.DepTips = append(res.DepTips, tip)
		merged, err := depMerged(git, repoRoot, baseTip, tip)
		if err != nil {
			return res, fmt.Errorf("resolve run base: %s: %w", name, err)
		}
		if !merged {
			unmerged = append(unmerged, depBranch{name: name, tip: tip})
		}
	}
	sort.Strings(res.DepTips)
	if len(unmerged) == 0 {
		return res, nil
	}

	top, ok, err := containingTip(git, repoRoot, unmerged)
	if err != nil {
		return res, fmt.Errorf("resolve run base: %w", err)
	}
	if !ok {
		names := make([]string, 0, len(unmerged))
		for _, d := range unmerged {
			names = append(names, d.name)
		}
		return res, &DepsUnmergedError{Branches: names, Base: baseBranch}
	}
	res.StartRef, res.StackedOn = top.tip, top.name
	return res, nil
}

// containingTip finds the one unmerged branch whose tip every other unmerged tip
// is an ancestor of. ok=false when there is none — the branches diverged.
//
// Two branches on the SAME commit are one tip, not a divergence: either name
// describes it, and the first in sorted order is the one reported.
func containingTip(git worktree.Git, repoRoot string, unmerged []depBranch) (depBranch, bool, error) {
	for _, candidate := range unmerged {
		containsAll := true
		for _, other := range unmerged {
			if other.tip == candidate.tip {
				continue
			}
			anc, err := isAncestor(git, repoRoot, other.tip, candidate.tip)
			if err != nil {
				return depBranch{}, false, err
			}
			if !anc {
				containsAll = false
				break
			}
		}
		if containsAll {
			return candidate, true, nil
		}
	}
	return depBranch{}, false, nil
}

// currentTip resolves the repo's current branch tip and its name. It mirrors
// worktree.Manager's own start-point resolution — the symbolic HEAD's branch, or
// HEAD's SHA with no name when the checkout is detached — because "the base" here
// has to be the very commit Acquire would pin to.
func currentTip(git worktree.Git, repoRoot string) (sha, branch string, err error) {
	name, symErr := git.Run(repoRoot, "symbolic-ref", "--short", "HEAD")
	if symErr != nil {
		out, headErr := git.Run(repoRoot, "rev-parse", "HEAD")
		if headErr != nil {
			return "", "", fmt.Errorf("resolve HEAD of %s: %w", repoRoot, headErr)
		}
		return strings.TrimSpace(out), "", nil
	}
	branch = strings.TrimSpace(name)
	out, err := git.Run(repoRoot, "rev-parse", "refs/heads/"+branch)
	if err != nil {
		return "", "", fmt.Errorf("resolve tip of %s: %w", branch, err)
	}
	return strings.TrimSpace(out), branch, nil
}

// branchTip resolves refs/heads/<branch>, telling "the branch is not there" apart
// from "git could not answer" the way worktree.Manager does: `rev-parse --verify
// --quiet` exits non-zero with NO output for an absent ref and prints a diagnostic
// when git itself is unhappy. The second must never read as "merged".
func branchTip(git worktree.Git, repoRoot, branch string) (tip string, exists bool, err error) {
	out, runErr := git.Run(repoRoot, "rev-parse", "--verify", "--quiet", "refs/heads/"+branch)
	if runErr == nil {
		tip = strings.TrimSpace(out)
		if tip == "" {
			return "", false, fmt.Errorf("probe branch %s: git answered with nothing", branch)
		}
		return tip, true, nil
	}
	if strings.TrimSpace(out) == "" {
		return "", false, nil
	}
	return "", false, fmt.Errorf("probe branch %s: %w", branch, runErr)
}

// depMerged reports whether the dependency tip is already contained in baseTip.
func depMerged(git worktree.Git, repoRoot, baseTip, depTip string) (bool, error) {
	if depTip == baseTip {
		return true, nil
	}
	anc, err := isAncestor(git, repoRoot, depTip, baseTip)
	if err != nil || anc {
		return anc, err
	}
	return mergeChangesNothing(git, repoRoot, baseTip, depTip), nil
}

// isAncestor runs `merge-base --is-ancestor a b`. Exit 1 with no output is the
// plain "no"; anything that prints is git failing, and is returned as an error
// rather than read as "not an ancestor" — that reading would turn a broken probe
// into a stacking decision.
func isAncestor(git worktree.Git, repoRoot, a, b string) (bool, error) {
	out, err := git.Run(repoRoot, "merge-base", "--is-ancestor", a, b)
	if err == nil {
		return true, nil
	}
	if strings.TrimSpace(out) == "" {
		return false, nil
	}
	return false, fmt.Errorf("ancestry of %s in %s: %w", a, b, err)
}

// mergeChangesNothing reports whether merging depTip into baseTip would leave
// baseTip's tree exactly as it is — the signature of a SQUASH merge, which puts
// the dependency's content on the base branch and none of its commits.
//
// `git merge-tree --write-tree` needs git ≥ 2.38. On an older git the option is
// rejected and this answers false, which leaves the ancestor test as the only
// judge — the documented fallback: a squash-merged dependency then reads as
// unmerged and the run is stacked on (or refused for) a branch whose work the base
// already holds, which is conservative and never wrong about what the tree
// contains. A real conflict answers false for the honest reason: a merge that
// conflicts is not a no-op.
func mergeChangesNothing(git worktree.Git, repoRoot, baseTip, depTip string) bool {
	out, err := git.Run(repoRoot, "merge-tree", "--write-tree", baseTip, depTip)
	if err != nil {
		return false
	}
	merged := firstLine(out)
	if merged == "" {
		return false
	}
	baseTree, err := git.Run(repoRoot, "rev-parse", baseTip+"^{tree}")
	if err != nil {
		return false
	}
	return merged == strings.TrimSpace(baseTree)
}

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	return strings.TrimSpace(s)
}

// uniqueSorted drops blanks and duplicates and sorts, so the resolution (and the
// error that names branches) does not depend on row order.
func uniqueSorted(in []string) []string {
	seen := make(map[string]bool, len(in))
	out := make([]string, 0, len(in))
	for _, s := range in {
		s = strings.TrimSpace(s)
		if s == "" || seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
	}
	sort.Strings(out)
	return out
}

// depRunBranches lists the run branches STAMPED on the phase's direct dependencies
// (epic_phases.run_branch, migration 0043) — the recorded names, never derived
// from row ids, for the reason RunBranch itself is recorded. A dependency that
// never ran has none and contributes nothing: there is no branch its work could be
// stranded on.
func (s *Service) depRunBranches(info phaseInfo) ([]string, error) {
	var out []string
	for _, seq := range info.DependsOn {
		rows, err := s.DB.Query(`
			SELECT COALESCE(run_branch, '') FROM epic_phases
			 WHERE workspace_task_id = ? AND seq = ?`, info.WorkspaceTaskID, seq)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var branch string
			if err := rows.Scan(&branch); err != nil {
				rows.Close()
				return nil, err
			}
			if branch != "" {
				out = append(out, branch)
			}
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return nil, err
		}
		rows.Close()
	}
	return out, nil
}

// resolveRunBase is resolveBase for a loaded phase: its dependencies' stamped run
// branches, measured in the repository the run executes in (info.RepoRoot, which
// the caller has already resolved).
func (s *Service) resolveRunBase(info phaseInfo) (baseResolution, error) {
	branches, err := s.depRunBranches(info)
	if err != nil {
		return baseResolution{}, err
	}
	return resolveBase(s.Git, info.RepoRoot, branches)
}

// StackingWorktrees is the part of *worktree.Manager only phase runs use: pinning
// a worktree to a chosen commit, and reclaiming a leftover branch measured against
// the start point its run recorded.
//
// It is an optional extension of runcore.WorktreeManager rather than two more
// methods on it, because that interface is "the subset every run engine uses" and
// dispatch and planrun use neither: a whole-plan run executes every phase in one
// worktree and has nothing to stack. *worktree.Manager satisfies it.
type StackingWorktrees interface {
	AcquireAt(repoRoot, projectSlug, taskID, startRef string) (worktree.Acquired, error)
	ReclaimEmptyBranchAt(repoRoot, branch, baseRef string) (int, error)
}

// acquire hands the run its worktree, pinned to startRef when base resolution
// chose one. A manager that cannot stack still serves every unstacked run exactly
// as before; asked to stack, it is refused — starting on the repo's branch tip
// instead would be the silent wrong-base run this file exists to prevent.
func (s *Service) acquire(repoRoot, projectSlug, taskName, startRef string) (worktree.Acquired, error) {
	if st, ok := s.Wt.(StackingWorktrees); ok {
		return st.AcquireAt(repoRoot, projectSlug, taskName, startRef)
	}
	if startRef != "" {
		return worktree.Acquired{}, ErrCannotStack
	}
	return s.Wt.Acquire(repoRoot, projectSlug, taskName)
}

// reclaimEmptyBranch frees a leftover run branch, measured against the start point
// the previous run recorded when there is one. A manager that cannot take a start
// point measures against the base branch alone — the larger count, so the
// refusing direction.
func (s *Service) reclaimEmptyBranch(repoRoot, branch, startPoint string) (int, error) {
	if st, ok := s.Wt.(StackingWorktrees); ok {
		return st.ReclaimEmptyBranchAt(repoRoot, branch, startPoint)
	}
	return s.Wt.ReclaimEmptyBranch(repoRoot, branch)
}
