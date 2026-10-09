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
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/worktree"
)

// ErrDepsUnmerged: the phase's dependencies are complete, but there is no single
// commit a run could start from that holds all of their work — and producing one
// takes a merge only the operator can perform (409). Returned as a
// *DepsUnmergedError, which errors.Is-matches this sentinel and says which of the
// three ways it happened (its Cause).
var ErrDepsUnmerged = errors.New("phase dependencies are on run branches the operator has to merge first")

// ErrCannotStack: base resolution chose a dependency branch to start on, and the
// run cannot be given a worktree that contains it. Two ways to get here: the wired
// worktree manager cannot pin a worktree anywhere except the repo's current branch
// tip (unreachable with *worktree.Manager, which is what the daemon wires), or a
// leftover worktree of this phase was warm-reused and its branch was cut BEFORE
// the dependency's current tip. Refused either way rather than started on a tree
// without the dependency's work.
var ErrCannotStack = errors.New("the run cannot be started on its dependency branch")

// The three ways dependencies can be complete and still leave nothing to start on.
const (
	// DepsDiverged: two or more unmerged dependency branches, none containing the
	// others.
	DepsDiverged = "diverged"
	// DepsConflict: a dependency branch does not merge cleanly into the base. The
	// daemon cannot tell "its work is not on the base" from "its work was
	// squash-merged and the base has since edited the same lines" — and stacking on
	// it in the second case would start the run on a tree older than the base.
	DepsConflict = "conflict"
	// DepsStale: there IS one unmerged branch to stack on, but it lacks the work of
	// another dependency that has already merged into the base — it was cut before
	// that merge landed.
	DepsStale = "stale"
)

// DepsUnmergedError names the branches the operator has to act on and the branch
// they were measured against, so the api's 409 body and the UI say exactly what to
// do instead of "dependencies unmerged".
type DepsUnmergedError struct {
	// Branches are the dependency run branches to act on, sorted: the diverged
	// ones, the conflicting ones, or the one stale branch the run would have been
	// stacked on.
	Branches []string
	// Base is the repo's checked-out branch — what "merged" was measured against.
	// Empty on a detached HEAD, where there is a tip but no name for it.
	Base string
	// Cause is DepsDiverged, DepsConflict or DepsStale. It picks the remedy the
	// message gives; the zero value reads as DepsDiverged.
	Cause string
	// The three reasons a DepsStale refusal can give for not stacking on
	// Branches[0]; at least one is non-empty, and each changes what the message
	// says and asks for.
	//
	// Missing are already-merged dependency branches whose work Branches[0]
	// provably lacks: merging them in applies cleanly and changes the tree.
	Missing []string
	// Conflicting are already-merged dependency branches that do not merge
	// cleanly into Branches[0]. That is NOT "their work is missing": a dependency
	// squash-merged into the base, with a stack branch cut after the squash that
	// edited the same lines, conflicts while the stack branch holds all of it.
	// It cannot be told either way, so the message says that, and offers the
	// remedy that fits the squash case — delete the merged branch.
	Conflicting []string
	// Absent are dependency run branches that no longer exist. Their work can
	// only be on the base, there is no tip left to test, and Branches[0] does not
	// contain the base as it stands.
	Absent []string
}

func (e *DepsUnmergedError) Error() string {
	base := e.Base
	if base == "" {
		base = "the repo's current HEAD"
	}
	branches := strings.Join(e.Branches, ", ")
	switch e.Cause {
	case DepsConflict:
		return fmt.Sprintf("this phase depends on %s, which does not merge cleanly into %s, so it cannot be told whether %s already holds that work — "+
			"merge it into %s, or delete the branch if its work was already squash-merged, then run the phase again",
			branches, base, base, base)
	case DepsStale:
		var why []string
		if len(e.Missing) > 0 {
			why = append(why, fmt.Sprintf("that branch does not contain the work of %s, which %s already holds",
				strings.Join(e.Missing, ", "), base))
		}
		if len(e.Conflicting) > 0 {
			why = append(why, fmt.Sprintf("%s, which %s already holds, does not merge cleanly into that branch, so it cannot be told whether the branch contains that work",
				strings.Join(e.Conflicting, ", "), base))
		}
		if len(e.Absent) > 0 {
			why = append(why, fmt.Sprintf("the run branch of another dependency (%s) no longer exists, so that work can only be on %s, and the branch does not contain %s as it stands",
				strings.Join(e.Absent, ", "), base, base))
		}
		remedy := fmt.Sprintf("bring %s up to date with %s (or merge it into %s)", branches, base, base)
		if len(e.Conflicting) > 0 {
			remedy += fmt.Sprintf(", or delete %s if its work was already squash-merged", strings.Join(e.Conflicting, ", "))
		}
		return fmt.Sprintf("this phase would be stacked on %s, but %s — %s, then run the phase again",
			branches, strings.Join(why, "; and "), remedy)
	}
	return fmt.Sprintf("this phase depends on run branches that are not merged into %s and have diverged from one another: %s — "+
		"merge them into %s (or one into the other), then run the phase again",
		base, branches, base)
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
//     A branch whose merge into baseTip CONFLICTS is neither: see step 2a.
//  3. Nothing unmerged ⇒ StartRef "" (unchanged behaviour).
//  4. Exactly one unmerged tip that every other unmerged tip is an ancestor of ⇒
//     start there: a linear chain, where the last branch already carries the rest.
//     See step 4a for the condition that tip still has to meet.
//  5. Otherwise ⇒ *DepsUnmergedError naming the branches.
//
// Two refusals guard the phase's one promise — the run starts on a tree that
// contains its dependencies' work, or is told which branches to merge first:
//
//	2a. A dependency branch that conflicts with baseTip ⇒ *DepsUnmergedError
//	    (DepsConflict). "Unmerged, stack on it" would be right for a branch that
//	    really is unmerged and wrong for one that was squash-merged before the base
//	    edited the same lines again: stacking there starts the run on a tree older
//	    than the base, without everything merged since. The two cannot be told
//	    apart from here, so neither is guessed.
//	4a. The tip chosen in step 4 must hold every MERGED dependency's work too — its
//	    tip is an ancestor of the chosen tip, or merging it in changes nothing.
//	    Otherwise ⇒ *DepsUnmergedError (DepsStale): the branch was cut before
//	    another dependency merged, and starting on it would silently drop that
//	    dependency while the prompt says "stacked".
//	4b. A dependency whose branch is GONE has no tip to run 4a against — and a
//	    merged branch is exactly the one that gets deleted (`gh pr merge -d`).
//	    Its work can only be on the base, so when any dependency branch is absent
//	    the chosen tip must hold the BASE tip: baseTip is an ancestor of it, or
//	    merging baseTip in changes nothing. Otherwise ⇒ DepsStale again. This is
//	    stricter than 4a (a base that moved on with unrelated work refuses too);
//	    with nothing left to test, that is the only claim that can be proven.
//
// resolveBase asks git whether it can run the merge probe on every call;
// resolveBaseWith takes the cached answer a Service keeps.
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
	return resolveBaseWith(git, &gitCapability{}, repoRoot, depBranches)
}

func resolveBaseWith(git worktree.Git, caps *gitCapability, repoRoot string, depBranches []string) (baseResolution, error) {
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

	var merged, unmerged, conflicting []depBranch
	var absent []string
	for _, name := range uniqueSorted(depBranches) {
		tip, exists, err := branchTip(git, repoRoot, name)
		if err != nil {
			return res, fmt.Errorf("resolve run base: %w", err)
		}
		if !exists {
			// A deleted run branch counts as merged — and is remembered, because
			// "merged" with no tip left is a claim step 4b has to cover.
			absent = append(absent, name)
			continue
		}
		res.DepTips = append(res.DepTips, tip)
		dep := depBranch{name: name, tip: tip}
		state, err := depStateOf(git, caps, repoRoot, baseTip, tip)
		if err != nil {
			return res, fmt.Errorf("resolve run base: %s: %w", name, err)
		}
		switch state {
		case depMerged:
			merged = append(merged, dep)
		case depConflicts:
			conflicting = append(conflicting, dep)
		default:
			unmerged = append(unmerged, dep)
		}
	}
	sort.Strings(res.DepTips)
	// Step 2a, ahead of everything else: while a conflicting branch is there, no
	// start point this function could pick is known to hold its work.
	if len(conflicting) > 0 {
		return res, &DepsUnmergedError{Branches: branchNames(conflicting), Base: baseBranch, Cause: DepsConflict}
	}
	if len(unmerged) == 0 {
		return res, nil
	}

	top, ok, err := containingTip(git, repoRoot, unmerged)
	if err != nil {
		return res, fmt.Errorf("resolve run base: %w", err)
	}
	if !ok {
		return res, &DepsUnmergedError{Branches: branchNames(unmerged), Base: baseBranch, Cause: DepsDiverged}
	}
	// Step 4a: the unmerged set agreed on a tip — now the merged dependencies have
	// to be in it as well.
	missing, unclear, err := missingFrom(git, caps, repoRoot, top, merged)
	if err != nil {
		return res, fmt.Errorf("resolve run base: %w", err)
	}
	stale := &DepsUnmergedError{
		Branches: []string{top.name}, Base: baseBranch, Cause: DepsStale,
		Missing: branchNames(missing), Conflicting: branchNames(unclear),
	}
	// Step 4b: a dependency branch that is gone leaves only the base to test.
	if len(absent) > 0 {
		holds, err := holdsCommit(git, caps, repoRoot, top.tip, baseTip)
		if err != nil {
			return res, fmt.Errorf("resolve run base: %s against %s: %w", top.name, baseTip, err)
		}
		if !holds {
			stale.Absent = absent
		}
	}
	if len(stale.Missing)+len(stale.Conflicting)+len(stale.Absent) > 0 {
		return res, stale
	}
	res.StartRef, res.StackedOn = top.tip, top.name
	return res, nil
}

// holdsCommit reports whether `tip` provably contains everything `commit` has:
// commit is an ancestor of tip, or merging it into tip changes nothing. A
// conflict, a merge that changes the tree, and a git that cannot run the probe
// are all "not provably" — false.
func holdsCommit(git worktree.Git, caps *gitCapability, repoRoot, tip, commit string) (bool, error) {
	if tip == commit {
		return true, nil
	}
	anc, err := isAncestor(git, repoRoot, commit, tip)
	if err != nil || anc {
		return anc, err
	}
	outcome, err := probeMerge(git, caps, repoRoot, tip, commit)
	if err != nil {
		return false, err
	}
	return outcome == mergeNoOp, nil
}

func branchNames(deps []depBranch) []string {
	names := make([]string, 0, len(deps))
	for _, d := range deps {
		names = append(names, d.name)
	}
	return names
}

// missingFrom sorts the MERGED dependencies the chosen stack tip cannot be shown
// to hold into two lists. A merged dependency is present in the tip when its own
// tip is an ancestor of it (the branch was cut after the merge landed), or when
// merging it into the tip would change nothing (the tip picked the same content up
// another way — through a squash on the base it was cut from, say). Otherwise:
//
//   - missing: the merge applies cleanly and changes the tree — the tip lacks that
//     work, provably. A git too old to run the probe lands here as well: not
//     provably there is treated as missing, because the alternative is a run that
//     starts without a dependency and says nothing.
//   - unclear: the merge conflicts. The tip may hold the work (a squash-merged
//     dependency, and a tip cut after the squash that edited the same lines) or
//     may not; saying "it does not contain it" would be a guess, and the remedy
//     for the squash case is a different one.
//
// Both lists become a refusal; they differ in what it says.
func missingFrom(git worktree.Git, caps *gitCapability, repoRoot string, top depBranch, merged []depBranch) (missing, unclear []depBranch, err error) {
	for _, dep := range merged {
		if dep.tip == top.tip {
			continue
		}
		anc, err := isAncestor(git, repoRoot, dep.tip, top.tip)
		if err != nil {
			return nil, nil, err
		}
		if anc {
			continue
		}
		outcome, err := probeMerge(git, caps, repoRoot, top.tip, dep.tip)
		if err != nil {
			return nil, nil, fmt.Errorf("%s into %s: %w", dep.name, top.name, err)
		}
		switch outcome {
		case mergeNoOp:
		case mergeConflicts:
			unclear = append(unclear, dep)
		default:
			missing = append(missing, dep)
		}
	}
	return missing, unclear, nil
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

// exitStatus is the exit status git itself reported, when the failure WAS git
// exiting: worktree.ExecGit wraps the *exec.ExitError, and a status is what that
// carries. ok=false for everything else — a timeout (ExecGit reports it without an
// exit status), a git binary that could not be started, a killed process.
//
// The distinction is the whole point of asking. Git answers several of the
// questions below WITH its exit status — 1 is "no" from `rev-parse --verify
// --quiet`, from `merge-base --is-ancestor`, and "it conflicts" from `merge-tree`
// — and a probe that never produced a status has not answered at all. Reading a
// silent failure as "no" is how a wedged filesystem would turn into "the branch is
// gone, so it counts as merged".
func exitStatus(err error) (status int, ok bool) {
	var exited interface{ ExitCode() int }
	if !errors.As(err, &exited) {
		return 0, false
	}
	status = exited.ExitCode()
	return status, status > 0 // -1 is "did not exit": killed by a signal
}

// branchTip resolves refs/heads/<branch>, telling "the branch is not there" apart
// from "git could not answer". `rev-parse --verify --quiet` exits 1 with NO output
// for an absent ref; that, and only that, is "absent" — which resolveBase counts
// as merged. Any other failure is an error that refuses the start: a diagnostic on
// the output, another exit status, or no exit status at all (a timeout, a git that
// did not run) must never read as "merged", because merged means "start on the
// base without this dependency's branch".
func branchTip(git worktree.Git, repoRoot, branch string) (tip string, exists bool, err error) {
	out, runErr := git.Run(repoRoot, "rev-parse", "--verify", "--quiet", "refs/heads/"+branch)
	if runErr == nil {
		tip = strings.TrimSpace(out)
		if tip == "" {
			return "", false, fmt.Errorf("probe branch %s: git answered with nothing", branch)
		}
		return tip, true, nil
	}
	if status, ok := exitStatus(runErr); ok && status == 1 && strings.TrimSpace(out) == "" {
		return "", false, nil
	}
	return "", false, fmt.Errorf("probe branch %s: %w", branch, runErr)
}

// depState is what one dependency branch is, relative to the base tip.
type depState int

const (
	// depUnmerged: its work is not on the base, and merging it would apply cleanly
	// — a branch a run may be stacked on.
	depUnmerged depState = iota
	// depMerged: an ancestor of the base, or a merge that changes nothing.
	depMerged
	// depConflicts: merging it into the base conflicts. Neither of the above can
	// be claimed; resolveBase refuses (step 2a).
	depConflicts
)

// depStateOf classifies the dependency tip against baseTip.
func depStateOf(git worktree.Git, caps *gitCapability, repoRoot, baseTip, depTip string) (depState, error) {
	if depTip == baseTip {
		return depMerged, nil
	}
	anc, err := isAncestor(git, repoRoot, depTip, baseTip)
	if err != nil {
		return depUnmerged, err
	}
	if anc {
		return depMerged, nil
	}
	outcome, err := probeMerge(git, caps, repoRoot, baseTip, depTip)
	if err != nil {
		return depUnmerged, err
	}
	switch outcome {
	case mergeNoOp:
		return depMerged, nil
	case mergeConflicts:
		return depConflicts, nil
	}
	// mergeChanges, and mergeUnsupported: on a git without the probe the ancestor
	// test above is the only judge — the documented fallback.
	return depUnmerged, nil
}

// isAncestor runs `merge-base --is-ancestor a b`. Exit status 1 is the plain "no";
// any other failure — another status, a diagnostic, no status at all — is git
// failing, and is returned as an error rather than read as "not an ancestor": that
// reading would turn a broken probe into a stacking decision.
func isAncestor(git worktree.Git, repoRoot, a, b string) (bool, error) {
	_, err := git.Run(repoRoot, "merge-base", "--is-ancestor", a, b)
	if err == nil {
		return true, nil
	}
	if status, ok := exitStatus(err); ok && status == 1 {
		return false, nil
	}
	return false, fmt.Errorf("ancestry of %s in %s: %w", a, b, err)
}

// mergeOutcome is what merging one commit into another would do to the tree.
type mergeOutcome int

const (
	// mergeNoOp: the result is the target's own tree. The signature of a SQUASH
	// merge, which puts a branch's content on the target and none of its commits.
	mergeNoOp mergeOutcome = iota
	// mergeChanges: the merge applies cleanly and the tree changes.
	mergeChanges
	// mergeConflicts: the merge does not apply cleanly.
	mergeConflicts
	// mergeUnsupported: this git cannot run the probe (`merge-tree --write-tree`
	// needs git ≥ 2.38). Nothing was asked and nothing was learned.
	mergeUnsupported
)

// mergeTreeMinMajor/Minor: the first git whose `merge-tree` takes `--write-tree`.
const (
	mergeTreeMinMajor = 2
	mergeTreeMinMinor = 38
)

// gitCapability remembers whether the git behind a seam can run the merge probe.
// The zero value is ready to use and knows nothing yet.
//
// It exists so that "this git is too old" is DECIDED — once, from `git version` —
// rather than inferred from a failing probe. Inferring it made every exit status
// other than 0 and 1 mean "unsupported, fall back to the ancestor test", which on
// a modern git turns a real failure (a corrupt object, exit 128) into "unmerged,
// stack on it". Only an answer is cached: a version probe that failed is asked
// again next time, so one timeout cannot pin a Service to a wrong verdict.
type gitCapability struct {
	mu        sync.Mutex
	known     bool
	mergeTree bool
}

// canMergeTree reports whether `git merge-tree --write-tree` exists here. A
// version that cannot be obtained or read is an error: whether the probe may be
// trusted is not something to guess in either direction.
func (c *gitCapability) canMergeTree(git worktree.Git, repoRoot string) (bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.known {
		return c.mergeTree, nil
	}
	out, err := git.Run(repoRoot, "version")
	if err != nil {
		return false, fmt.Errorf("git version: %w", err)
	}
	major, minor, ok := parseGitVersion(out)
	if !ok {
		return false, fmt.Errorf("git version: cannot read a version out of %q", strings.TrimSpace(out))
	}
	c.known = true
	c.mergeTree = major > mergeTreeMinMajor || (major == mergeTreeMinMajor && minor >= mergeTreeMinMinor)
	return c.mergeTree, nil
}

// parseGitVersion reads major and minor out of `git version`'s line — "git version
// 2.50.1 (Apple Git-155)", "git version 2.43.0.windows.1".
func parseGitVersion(out string) (major, minor int, ok bool) {
	const prefix = "git version "
	line := firstLine(out)
	if !strings.HasPrefix(line, prefix) {
		return 0, 0, false
	}
	fields := strings.Fields(strings.TrimPrefix(line, prefix))
	if len(fields) == 0 {
		return 0, 0, false
	}
	parts := strings.Split(fields[0], ".")
	if len(parts) < 2 {
		return 0, 0, false
	}
	major, errMajor := strconv.Atoi(parts[0])
	minor, errMinor := strconv.Atoi(parts[1])
	if errMajor != nil || errMinor != nil || major < 0 || minor < 0 {
		return 0, 0, false
	}
	return major, minor, true
}

// probeMerge asks what merging `from` into `into` would do, without touching a
// ref or a working tree: `git merge-tree --write-tree into from`.
//
// On a git that cannot run it (caps says so) nothing is executed and the answer
// is mergeUnsupported — the callers each have a documented answer for "no probe".
// On a git that can, its exit status IS the answer: 0 and the merged tree's id on
// the first line for a clean merge, 1 for a conflicted one. Everything else is an
// error that refuses the start — any other status (git could not perform the
// merge), no status at all (a timeout, a git that did not start), and a clean
// merge whose answer cannot be read. None of those is a decision, and the
// fallback for old gits is not a place to hide them.
func probeMerge(git worktree.Git, caps *gitCapability, repoRoot, into, from string) (mergeOutcome, error) {
	supported, err := caps.canMergeTree(git, repoRoot)
	if err != nil {
		return mergeUnsupported, fmt.Errorf("merge probe of %s into %s: %w", from, into, err)
	}
	if !supported {
		return mergeUnsupported, nil
	}
	out, err := git.Run(repoRoot, "merge-tree", "--write-tree", into, from)
	if err != nil {
		if status, ok := exitStatus(err); ok && status == 1 {
			return mergeConflicts, nil
		}
		return mergeUnsupported, fmt.Errorf("merge probe of %s into %s: %w", from, into, err)
	}
	if strings.TrimSpace(out) == "" {
		return mergeUnsupported, fmt.Errorf("merge probe of %s into %s: git answered with nothing", from, into)
	}
	tree, err := git.Run(repoRoot, "rev-parse", into+"^{tree}")
	if err != nil {
		return mergeUnsupported, fmt.Errorf("resolve tree of %s: %w", into, err)
	}
	// The answer is searched for, not read off the first line: worktree.ExecGit
	// returns stderr in the same buffer, so a warning printed during the merge
	// comes ahead of the tree id. No warning line is ever a tree id.
	want := strings.TrimSpace(tree)
	for _, line := range strings.Split(out, "\n") {
		if strings.TrimSpace(line) == want {
			return mergeNoOp, nil
		}
	}
	return mergeChanges, nil
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
//
// Only dependencies that run in THIS phase's repository (info.RepoRoot) count. In
// a multi-repo plan a dependency declaring another repo has its run branch there:
// probed here it would read as a deleted — merged — branch and trip step 4b with a
// refusal no merge in this repo can clear. Its root is resolved by the same rules
// as this phase's (runRoot); a dependency whose root cannot be resolved is kept,
// which is how every dependency was treated before.
func (s *Service) depRunBranches(info phaseInfo) ([]string, error) {
	type dep struct{ branch, repo string }
	var deps []dep
	for _, seq := range info.DependsOn {
		rows, err := s.DB.Query(`
			SELECT COALESCE(run_branch, ''), COALESCE(repo, '') FROM epic_phases
			 WHERE workspace_task_id = ? AND seq = ?`, info.WorkspaceTaskID, seq)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var d dep
			if err := rows.Scan(&d.branch, &d.repo); err != nil {
				rows.Close()
				return nil, err
			}
			if d.branch != "" {
				deps = append(deps, d)
			}
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return nil, err
		}
		rows.Close()
	}
	// Filtered only once every cursor is closed: resolving a repository reads the
	// project registry, and the store runs one connection.
	var out []string
	for _, d := range deps {
		if s.sameRepo(info, d.repo) {
			out = append(out, d.branch)
		}
	}
	return out, nil
}

// sameRepo reports whether a dependency of info declaring depRepo (its raw Repo
// cell) runs in info.RepoRoot. Same project, same workspace — so the same cell
// resolves to the same repository and needs no lookup; a different cell may still
// land on the same checkout (a single-repo project falls back to its path), so it
// is resolved. An unresolvable root is "same": see depRunBranches.
func (s *Service) sameRepo(info phaseInfo, depRepo string) bool {
	if strings.TrimSpace(depRepo) == strings.TrimSpace(info.Repo) {
		return true
	}
	root, err := s.runRoot(phaseInfo{ProjectPath: info.ProjectPath, WorkspaceRoot: info.WorkspaceRoot, Repo: depRepo})
	if err != nil {
		return true
	}
	return filepath.Clean(root) == filepath.Clean(info.RepoRoot)
}

// resolveRunBase is resolveBase for a loaded phase: its dependencies' stamped run
// branches, measured in the repository the run executes in (info.RepoRoot, which
// the caller has already resolved).
func (s *Service) resolveRunBase(info phaseInfo) (baseResolution, error) {
	branches, err := s.depRunBranches(info)
	if err != nil {
		return baseResolution{}, err
	}
	return resolveBaseWith(s.Git, &s.gitCaps, info.RepoRoot, branches)
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

// ExistingBranchWorktrees is the optional extension a RETURNED run continues
// through (continueOwnBranch): check the phase's existing run branch out as it
// stands instead of cutting a new one. *worktree.Manager satisfies it.
type ExistingBranchWorktrees interface {
	AcquireExisting(repoRoot, projectSlug, taskID string) (worktree.Acquired, error)
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
		return worktree.Acquired{}, fmt.Errorf("%w: the wired worktree manager can only start a run on the repo's current branch tip", ErrCannotStack)
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
