package phaserun

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/runcore"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/worktree"
)

// Base resolution against REAL git, in temp repositories only. Every answer it
// gives is a claim about git's own behaviour — what `merge-base --is-ancestor`
// says about a squash merge, what `merge-tree --write-tree` prints, how a deleted
// branch probes — and a scripted stub could only assert those by assumption.

// tempRepo is a throwaway repository on `main` with one commit.
type tempRepo struct {
	t   *testing.T
	dir string
	git worktree.ExecGit
	n   int
}

func newTempRepo(t *testing.T) *tempRepo {
	t.Helper()
	if testing.Short() {
		t.Skip("needs a real git binary; skipped in -short")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	r := &tempRepo{t: t, dir: t.TempDir(), git: worktree.ExecGit{}}
	r.run("init", "-q", "-b", "main")
	r.run("config", "user.email", "test@example.com")
	r.run("config", "user.name", "Test")
	r.run("config", "commit.gpgsign", "false")
	r.commit("init")
	return r
}

func (r *tempRepo) run(args ...string) string {
	r.t.Helper()
	return r.runIn(r.dir, args...)
}

func (r *tempRepo) runIn(dir string, args ...string) string {
	r.t.Helper()
	out, err := r.git.Run(dir, args...)
	if err != nil {
		r.t.Fatalf("git -C %s %s: %v\n%s", dir, strings.Join(args, " "), err, out)
	}
	return out
}

func (r *tempRepo) tip(ref string) string {
	r.t.Helper()
	return strings.TrimSpace(r.run("rev-parse", ref))
}

// commit adds one new file on the repo's current branch and returns the new HEAD.
func (r *tempRepo) commit(msg string) string {
	r.t.Helper()
	return r.commitIn(r.dir, msg)
}

// commitIn is commit for any checkout of the repo — the repo itself or a worktree.
func (r *tempRepo) commitIn(dir, msg string) string {
	r.t.Helper()
	r.n++
	name := "f" + strconv.Itoa(r.n) + ".txt"
	if err := os.WriteFile(filepath.Join(dir, name), []byte(msg+"\n"), 0o644); err != nil {
		r.t.Fatalf("write %s: %v", name, err)
	}
	r.runIn(dir, "add", name)
	r.runIn(dir, "commit", "-q", "-m", msg)
	return strings.TrimSpace(r.runIn(dir, "rev-parse", "HEAD"))
}

// branch creates `name` off `from` with n commits of its own, returns its tip, and
// leaves the repo back on main.
func (r *tempRepo) branch(name, from string, n int) string {
	r.t.Helper()
	r.run("checkout", "-q", "-b", name, from)
	for i := 0; i < n; i++ {
		r.commit(name + " work")
	}
	tip := r.tip("HEAD")
	r.run("checkout", "-q", "main")
	return tip
}

const (
	depA = "swarm/phase-101"
	depB = "swarm/phase-102"
)

// Case 1 — no dependencies: start where every run always started.
func TestResolveBase_NoDeps(t *testing.T) {
	r := newTempRepo(t)
	res, err := resolveBase(r.git, r.dir, nil)
	if err != nil {
		t.Fatalf("resolveBase: %v", err)
	}
	if res.StartRef != "" || res.StackedOn != "" {
		t.Errorf("StartRef=%q StackedOn=%q, want both empty with no dependencies", res.StartRef, res.StackedOn)
	}
	if res.BaseTip != r.tip("main") || res.BaseBranch != "main" {
		t.Errorf("base = %s (%q), want main's tip", res.BaseTip, res.BaseBranch)
	}
	if len(res.DepTips) != 0 {
		t.Errorf("DepTips = %v, want none", res.DepTips)
	}
}

// Case 2 — one unmerged dependency: the run is stacked on its tip.
func TestResolveBase_OneUnmergedDep(t *testing.T) {
	r := newTempRepo(t)
	tip := r.branch(depA, "main", 2)

	res, err := resolveBase(r.git, r.dir, []string{depA})
	if err != nil {
		t.Fatalf("resolveBase: %v", err)
	}
	if res.StartRef != tip {
		t.Errorf("StartRef = %s, want the dependency tip %s", res.StartRef, tip)
	}
	if res.StackedOn != depA {
		t.Errorf("StackedOn = %q, want %q", res.StackedOn, depA)
	}
	if !reflect.DeepEqual(res.DepTips, []string{tip}) {
		t.Errorf("DepTips = %v, want [%s]", res.DepTips, tip)
	}
	if res.fingerprintBase() != tip {
		t.Errorf("fingerprintBase = %s, want the stacked tip", res.fingerprintBase())
	}
}

// Case 3 — a linear chain of two: B was cut from A, so B's tip already carries A.
// The later tip wins, whatever order the dependencies are listed in.
func TestResolveBase_LinearChainLaterTipWins(t *testing.T) {
	r := newTempRepo(t)
	tipA := r.branch(depA, "main", 1)
	tipB := r.branch(depB, depA, 1)
	if tipA == tipB {
		t.Fatal("setup: the chain has one tip")
	}

	for _, order := range [][]string{{depA, depB}, {depB, depA}} {
		res, err := resolveBase(r.git, r.dir, order)
		if err != nil {
			t.Fatalf("resolveBase(%v): %v", order, err)
		}
		if res.StartRef != tipB || res.StackedOn != depB {
			t.Errorf("resolveBase(%v) = %s on %q, want the later tip %s on %q",
				order, res.StartRef, res.StackedOn, tipB, depB)
		}
	}
}

// Case 4 — two divergent dependencies: neither contains the other, so there is no
// single commit to start from. The refusal names BOTH branches and the base.
func TestResolveBase_DivergentDepsAreRefused(t *testing.T) {
	r := newTempRepo(t)
	tipA := r.branch(depA, "main", 1)
	tipB := r.branch(depB, "main", 1)

	res, err := resolveBase(r.git, r.dir, []string{depB, depA})
	if !errors.Is(err, ErrDepsUnmerged) {
		t.Fatalf("err = %v, want ErrDepsUnmerged", err)
	}
	var unmerged *DepsUnmergedError
	if !errors.As(err, &unmerged) {
		t.Fatalf("err = %v, want a *DepsUnmergedError", err)
	}
	if !reflect.DeepEqual(unmerged.Branches, []string{depA, depB}) {
		t.Errorf("Branches = %v, want both, sorted: [%s %s]", unmerged.Branches, depA, depB)
	}
	if unmerged.Base != "main" {
		t.Errorf("Base = %q, want main", unmerged.Base)
	}
	for _, want := range []string{depA, depB, "main"} {
		if !strings.Contains(unmerged.Error(), want) {
			t.Errorf("message %q does not name %s", unmerged.Error(), want)
		}
	}
	// Nothing to start from — but the facts the blocked fingerprint needs are there.
	if res.StartRef != "" {
		t.Errorf("StartRef = %q alongside a refusal, want none", res.StartRef)
	}
	if res.BaseTip != r.tip("main") {
		t.Errorf("BaseTip = %q, want it filled alongside the refusal", res.BaseTip)
	}
	wantTips := []string{tipA, tipB}
	if tipA > tipB {
		wantTips = []string{tipB, tipA}
	}
	if !reflect.DeepEqual(res.DepTips, wantTips) {
		t.Errorf("DepTips = %v, want both tips sorted %v", res.DepTips, wantTips)
	}
}

// Case 5 — a merged dependency (its tip is an ancestor of the base): not stacked.
func TestResolveBase_MergedDepIsAncestor(t *testing.T) {
	for _, mode := range []string{"--ff-only", "--no-ff"} {
		t.Run(mode, func(t *testing.T) {
			r := newTempRepo(t)
			tip := r.branch(depA, "main", 2)
			r.run("merge", "-q", mode, "-m", "merge "+depA, depA)

			res, err := resolveBase(r.git, r.dir, []string{depA})
			if err != nil {
				t.Fatalf("resolveBase: %v", err)
			}
			if res.StartRef != "" || res.StackedOn != "" {
				t.Errorf("StartRef=%q StackedOn=%q, want none — the dependency is on main", res.StartRef, res.StackedOn)
			}
			// Its tip is still part of the situation the fingerprint describes.
			if !reflect.DeepEqual(res.DepTips, []string{tip}) {
				t.Errorf("DepTips = %v, want [%s]", res.DepTips, tip)
			}
		})
	}
}

// Case 6 — a SQUASH-merged dependency: main holds its content and none of its
// commits, so the ancestor test says "unmerged". Merging it would change nothing,
// and that is what makes it merged.
func TestResolveBase_SquashMergedDepIsANoOpMerge(t *testing.T) {
	r := newTempRepo(t)
	tip := r.branch(depA, "main", 2)
	r.run("merge", "-q", "--squash", depA)
	r.run("commit", "-q", "-m", "squash "+depA)

	// The premise: no ancestry survives a squash.
	if _, err := r.git.Run(r.dir, "merge-base", "--is-ancestor", tip, "main"); err == nil {
		t.Fatal("setup: the squashed branch is an ancestor of main — this is not a squash merge")
	}

	res, err := resolveBase(r.git, r.dir, []string{depA})
	if err != nil {
		t.Fatalf("resolveBase: %v", err)
	}
	if res.StartRef != "" || res.StackedOn != "" {
		t.Errorf("StartRef=%q StackedOn=%q, want none — a squash-merged dependency is merged", res.StartRef, res.StackedOn)
	}

	// …and it stays merged when main moves on with unrelated work.
	r.commit("unrelated work on main")
	if res, err := resolveBase(r.git, r.dir, []string{depA}); err != nil || res.StartRef != "" {
		t.Errorf("after main moved on: StartRef=%q err=%v, want still merged", res.StartRef, err)
	}
}

// Case 7 — a dependency whose run branch was deleted counts as merged: a removed
// run branch is the operator saying its work is accounted for.
func TestResolveBase_DeletedDepBranchCountsAsMerged(t *testing.T) {
	r := newTempRepo(t)
	r.branch(depA, "main", 1)
	r.run("branch", "-D", depA)

	res, err := resolveBase(r.git, r.dir, []string{depA})
	if err != nil {
		t.Fatalf("resolveBase: %v", err)
	}
	if res.StartRef != "" || res.StackedOn != "" {
		t.Errorf("StartRef=%q StackedOn=%q, want none for a deleted branch", res.StartRef, res.StackedOn)
	}
	if len(res.DepTips) != 0 {
		t.Errorf("DepTips = %v, want none — the branch has no tip", res.DepTips)
	}
}

// A merged dependency next to an unmerged one: only the unmerged one decides.
func TestResolveBase_MergedAndUnmergedTogether(t *testing.T) {
	r := newTempRepo(t)
	r.branch(depA, "main", 1)
	r.run("merge", "-q", "--ff-only", depA)
	tipB := r.branch(depB, "main", 1)

	res, err := resolveBase(r.git, r.dir, []string{depA, depB})
	if err != nil {
		t.Fatalf("resolveBase: %v", err)
	}
	if res.StartRef != tipB || res.StackedOn != depB {
		t.Errorf("stacked on %q at %s, want %q at %s", res.StackedOn, res.StartRef, depB, tipB)
	}
}

// Two run branches on the SAME commit are one tip, not a divergence.
func TestResolveBase_TwoBranchesOneTip(t *testing.T) {
	r := newTempRepo(t)
	tip := r.branch(depA, "main", 1)
	r.run("branch", depB, depA)

	res, err := resolveBase(r.git, r.dir, []string{depB, depA, depA, " "})
	if err != nil {
		t.Fatalf("resolveBase: %v", err)
	}
	if res.StartRef != tip || res.StackedOn != depA {
		t.Errorf("stacked on %q at %s, want the first name %q at %s", res.StackedOn, res.StartRef, depA, tip)
	}
}

// A detached HEAD has a tip and no name: resolution still works, and a refusal
// reports an empty base rather than inventing one.
func TestResolveBase_DetachedHead(t *testing.T) {
	r := newTempRepo(t)
	r.branch(depA, "main", 1)
	r.branch(depB, "main", 1)
	r.run("checkout", "-q", "--detach", "main")

	res, err := resolveBase(r.git, r.dir, []string{depA, depB})
	var unmerged *DepsUnmergedError
	if !errors.As(err, &unmerged) {
		t.Fatalf("err = %v, want a *DepsUnmergedError", err)
	}
	if unmerged.Base != "" {
		t.Errorf("Base = %q, want empty on a detached HEAD", unmerged.Base)
	}
	if !strings.Contains(unmerged.Error(), "current HEAD") {
		t.Errorf("message %q should say what it measured against", unmerged.Error())
	}
	if res.BaseTip != r.tip("HEAD") || res.BaseBranch != "" {
		t.Errorf("base = %s (%q), want HEAD's sha and no name", res.BaseTip, res.BaseBranch)
	}
}

// exitErr is a git failure that carries an exit status, the way the *exec.ExitError
// inside worktree.ExecGit's errors does. A plain errors.New in a fake git is the
// OTHER kind of failure — no status at all: a timeout, a git that never ran.
type exitErr int

func (e exitErr) Error() string { return "exit status " + strconv.Itoa(int(e)) }
func (e exitErr) ExitCode() int { return int(e) }

// oldGit is a git that predates `merge-tree --write-tree` (< 2.38): it rejects
// the option exactly as such a git does, and runs everything else for real.
type oldGit struct{ worktree.ExecGit }

func (g oldGit) Run(dir string, args ...string) (string, error) {
	if len(args) > 1 && args[0] == "merge-tree" && args[1] == "--write-tree" {
		return "usage: git merge-tree <base-tree> <branch1> <branch2>\n", exitErr(129)
	}
	return g.ExecGit.Run(dir, args...)
}

// The documented fallback for git < 2.38: without merge-tree the ancestor test is
// the only judge, so a squash-merged dependency reads as unmerged and the run is
// stacked on it. Conservative — the tree still contains the dependency's work.
func TestResolveBase_OldGitFallsBackToAncestorTest(t *testing.T) {
	r := newTempRepo(t)
	tip := r.branch(depA, "main", 1)
	r.run("merge", "-q", "--squash", depA)
	r.run("commit", "-q", "-m", "squash "+depA)

	res, err := resolveBase(oldGit{}, r.dir, []string{depA})
	if err != nil {
		t.Fatalf("resolveBase on an old git: %v", err)
	}
	if res.StartRef != tip || res.StackedOn != depA {
		t.Errorf("stacked on %q at %s, want the fallback to stack on %q at %s", res.StackedOn, res.StartRef, depA, tip)
	}
	// A truly merged (ancestor) dependency needs no merge-tree at all.
	r2 := newTempRepo(t)
	r2.branch(depA, "main", 1)
	r2.run("merge", "-q", "--ff-only", depA)
	if res, err := resolveBase(oldGit{}, r2.dir, []string{depA}); err != nil || res.StartRef != "" {
		t.Errorf("ancestor dep on an old git: StartRef=%q err=%v, want merged", res.StartRef, err)
	}

	// …but the stack check has no such fallback. A MERGED dependency that is not an
	// ancestor of the branch to stack on cannot be shown to be in it without the
	// probe, and "cannot be shown" is a refusal, never a stack.
	r3 := newTempRepo(t)
	r3.branch(depA, "main", 1)
	r3.branch(depB, "main", 1)
	r3.run("merge", "-q", "--ff-only", depA)
	_, err = resolveBase(oldGit{}, r3.dir, []string{depA, depB})
	var stale *DepsUnmergedError
	if !errors.As(err, &stale) || stale.Cause != DepsStale {
		t.Errorf("stale stack tip on an old git: err = %v, want a DepsStale refusal", err)
	}
}

// writeShared commits `content` to shared.txt on the repo's current branch.
func (r *tempRepo) writeShared(content, msg string) string {
	r.t.Helper()
	mustWriteDoc(r.t, filepath.Join(r.dir, "shared.txt"), content)
	r.run("add", "shared.txt")
	r.run("commit", "-q", "-m", msg)
	return r.tip("HEAD")
}

// wantRefusal asserts err is a *DepsUnmergedError of the given cause naming
// exactly `branches`, and that nothing was chosen to start on.
func wantRefusal(t *testing.T, res baseResolution, err error, cause string, branches ...string) *DepsUnmergedError {
	t.Helper()
	if !errors.Is(err, ErrDepsUnmerged) {
		t.Fatalf("err = %v, want ErrDepsUnmerged (%s)", err, cause)
	}
	var refused *DepsUnmergedError
	if !errors.As(err, &refused) {
		t.Fatalf("err = %v, want a *DepsUnmergedError", err)
	}
	if refused.Cause != cause {
		t.Errorf("Cause = %q, want %q", refused.Cause, cause)
	}
	if !reflect.DeepEqual(refused.Branches, branches) {
		t.Errorf("Branches = %v, want %v", refused.Branches, branches)
	}
	if res.StartRef != "" || res.StackedOn != "" {
		t.Errorf("StartRef=%q StackedOn=%q alongside a refusal — nothing may be chosen to start on", res.StartRef, res.StackedOn)
	}
	return refused
}

// A dependency that CONFLICTS with the base is refused, not stacked on. "It
// conflicts" cannot tell a branch whose work is missing from the base apart from
// one that was squash-merged before the base edited the same lines — and stacking
// on the second starts the run on a tree older than the base.
func TestResolveBase_ConflictingDepIsRefused(t *testing.T) {
	r := newTempRepo(t)
	r.run("checkout", "-q", "-b", depA, "main")
	r.writeShared("from the dependency\n", "dep writes shared.txt")
	r.run("checkout", "-q", "main")
	r.writeShared("from main\n", "main writes shared.txt")

	res, err := resolveBase(r.git, r.dir, []string{depA})
	refused := wantRefusal(t, res, err, DepsConflict, depA)
	if refused.Base != "main" {
		t.Errorf("Base = %q, want main", refused.Base)
	}
	// The hint: merge it, or delete it if it was already squash-merged.
	for _, want := range []string{depA, "main", "does not merge cleanly", "delete the branch", "squash-merged"} {
		if !strings.Contains(refused.Error(), want) {
			t.Errorf("message %q does not carry %q", refused.Error(), want)
		}
	}
	// The facts the blocked fingerprint needs are still there.
	if res.BaseTip != r.tip("main") || len(res.DepTips) != 1 {
		t.Errorf("BaseTip=%q DepTips=%v, want both filled alongside the refusal", res.BaseTip, res.DepTips)
	}

	// A conflicting branch outranks a stackable one beside it: while it is there,
	// no start point is known to hold its work.
	tipB := r.branch(depB, "main", 1)
	res, err = resolveBase(r.git, r.dir, []string{depA, depB})
	wantRefusal(t, res, err, DepsConflict, depA)
	// …and the remedy the message names works: delete the conflicting branch, and
	// the other dependency is stacked on as usual.
	r.run("branch", "-D", depA)
	if res, err := resolveBase(r.git, r.dir, []string{depA, depB}); err != nil || res.StartRef != tipB {
		t.Errorf("after deleting the conflicting branch: StartRef=%q err=%v, want stacked on %s", res.StartRef, err, tipB)
	}
}

// THE regression this refusal exists for. Dependency A was squash-merged and its
// run branch survives; a later merged change edited the same lines. Merging A into
// main now conflicts — and reading that as "unmerged, stack on it" would pin the
// next dependent to A's stale tip, without anything merged since. Before base
// resolution existed that run started on main and was correct.
func TestResolveBase_SquashMergedThenEditedIsRefused(t *testing.T) {
	r := newTempRepo(t)
	r.writeShared("line one\nline two\n", "main adds shared.txt")
	r.run("checkout", "-q", "-b", depA, "main")
	staleTip := r.writeShared("line one\nline two — phase A\n", "phase A edits line two")
	r.run("checkout", "-q", "main")
	r.run("merge", "-q", "--squash", depA)
	r.run("commit", "-q", "-m", "squash "+depA)

	// Right after the squash the dependency reads as merged (case 6).
	if res, err := resolveBase(r.git, r.dir, []string{depA}); err != nil || res.StartRef != "" {
		t.Fatalf("right after the squash: StartRef=%q err=%v, want merged (test premise)", res.StartRef, err)
	}

	// A later merged phase edits the same hunk.
	r.writeShared("line one\nline two — phase A, then phase C\n", "a later phase edits line two again")

	res, err := resolveBase(r.git, r.dir, []string{depA})
	wantRefusal(t, res, err, DepsConflict, depA)
	if res.StartRef == staleTip {
		t.Errorf("the run was pinned to the stale squash-merged tip %s", staleTip)
	}
}

// THE other regression. Phases 1 and 2 are both cut from main@M0; 1 merges into
// main, 2 does not. A phase depending on both must not be pinned to 2's tip: that
// tree was cut before 1 landed and does not contain it, while the prompt would say
// "stacked". Refused, naming the branch that has to be brought up to date.
func TestResolveBase_StackTipLacksMergedDep(t *testing.T) {
	for _, mode := range []string{"--ff-only", "--no-ff", "--squash"} {
		t.Run(mode, func(t *testing.T) {
			r := newTempRepo(t)
			r.branch(depA, "main", 1) // both cut from main@M0
			tipB := r.branch(depB, "main", 1)
			if mode == "--squash" {
				r.run("merge", "-q", "--squash", depA)
				r.run("commit", "-q", "-m", "squash "+depA)
			} else {
				r.run("merge", "-q", mode, "-m", "merge "+depA, depA)
			}

			res, err := resolveBase(r.git, r.dir, []string{depA, depB})
			refused := wantRefusal(t, res, err, DepsStale, depB)
			if !reflect.DeepEqual(refused.Missing, []string{depA}) {
				t.Errorf("Missing = %v, want the merged dependency [%s]", refused.Missing, depA)
			}
			if refused.Base != "main" {
				t.Errorf("Base = %q, want main", refused.Base)
			}
			for _, want := range []string{depB, depA, "main", "up to date"} {
				if !strings.Contains(refused.Error(), want) {
					t.Errorf("message %q does not carry %q", refused.Error(), want)
				}
			}
			if res.StartRef == tipB {
				t.Errorf("the run was pinned to %s, which lacks %s", depB, depA)
			}

			// The remedy the message names works: bring B up to date with main, and
			// the run is stacked on a tip that now holds both.
			r.run("checkout", "-q", depB)
			r.run("merge", "-q", "--no-ff", "-m", "bring "+depB+" up to date", "main")
			updated := r.tip("HEAD")
			r.run("checkout", "-q", "main")
			res, err = resolveBase(r.git, r.dir, []string{depA, depB})
			if err != nil || res.StartRef != updated || res.StackedOn != depB {
				t.Errorf("after bringing %s up to date: stacked on %q at %s (err %v), want %q at %s",
					depB, res.StackedOn, res.StartRef, err, depB, updated)
			}
		})
	}
}

// A squash-merged dependency IS in a branch cut after the squash landed: not by
// ancestry, but merging it in changes nothing. That stacks.
func TestResolveBase_StackTipHoldsSquashedDep(t *testing.T) {
	r := newTempRepo(t)
	r.branch(depA, "main", 1)
	r.run("merge", "-q", "--squash", depA)
	r.run("commit", "-q", "-m", "squash "+depA)
	tipB := r.branch(depB, "main", 1) // cut AFTER the squash

	res, err := resolveBase(r.git, r.dir, []string{depA, depB})
	if err != nil {
		t.Fatalf("resolveBase: %v", err)
	}
	if res.StartRef != tipB || res.StackedOn != depB {
		t.Errorf("stacked on %q at %s, want %q at %s", res.StackedOn, res.StartRef, depB, tipB)
	}
}

// No git seam: nothing can be asked, so nothing is stacked or refused.
func TestResolveBase_NilGitResolvesNothing(t *testing.T) {
	res, err := resolveBase(nil, "/anywhere", []string{depA})
	if err != nil {
		t.Fatalf("resolveBase(nil git): %v", err)
	}
	if !reflect.DeepEqual(res, baseResolution{}) {
		t.Errorf("res = %+v, want the zero resolution", res)
	}
	if res, err := resolveBase(worktree.ExecGit{}, "", []string{depA}); err != nil || res.StartRef != "" {
		t.Errorf("empty repo root: %+v, %v — want the zero resolution", res, err)
	}
}

// A path that is not a repository: with nothing to stack on the failure is left
// to Acquire (which reports it as itself); with dependencies it is an error,
// because "merged" cannot be decided and must not be guessed.
func TestResolveBase_NotARepo(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	dir := t.TempDir()
	res, err := resolveBase(worktree.ExecGit{}, dir, nil)
	if err != nil || !reflect.DeepEqual(res, baseResolution{}) {
		t.Errorf("no deps in a non-repo = %+v, %v — want the zero resolution and no error", res, err)
	}
	if _, err := resolveBase(worktree.ExecGit{}, dir, []string{depA}); err == nil {
		t.Error("deps in a non-repo resolved without an error")
	}
}

// scriptedGit answers by the first argv tokens; anything unscripted succeeds empty.
type scriptedGit struct {
	answers map[string]scriptedAnswer
}

type scriptedAnswer struct {
	out string
	err error
}

func (g scriptedGit) Run(_ string, args ...string) (string, error) {
	joined := strings.Join(args, " ")
	best := ""
	for prefix := range g.answers {
		if strings.HasPrefix(joined, prefix) && len(prefix) > len(best) {
			best = prefix
		}
	}
	if best == "" {
		return "", nil
	}
	return g.answers[best].out, g.answers[best].err
}

// scriptedHead is the prelude every scripted resolution answers: on `main`, at
// base000.
var scriptedHead = map[string]scriptedAnswer{
	"symbolic-ref --short HEAD": {out: "main\n"},
	"rev-parse refs/heads/main": {out: "base000\n"},
}

// scripted builds a fake git from scriptedHead plus the given answers.
func scripted(extra ...map[string]scriptedAnswer) scriptedGit {
	all := make(map[string]scriptedAnswer)
	for k, v := range scriptedHead {
		all[k] = v
	}
	for _, m := range extra {
		for k, v := range m {
			all[k] = v
		}
	}
	return scriptedGit{answers: all}
}

// A probe git could not answer is an ERROR at every step — never a confident
// "merged", "missing", "not an ancestor" or "conflicts". Two kinds of failure are
// scripted: git exiting with a status that is not the probe's "no" (exitErr), and
// a failure with NO exit status at all (a plain error: a timeout, a git that never
// ran). The second is the one that used to slip through as an answer.
func TestResolveBase_BrokenProbesAreErrors(t *testing.T) {
	loud := exitErr(128)
	silent := errors.New("git rev-parse timed out after 30s")
	oneDep := map[string]scriptedAnswer{"rev-parse --verify --quiet": {out: "dep111\n"}}
	notAncestor := map[string]scriptedAnswer{"merge-base --is-ancestor dep111 base000": {err: exitErr(1)}}

	cases := map[string]scriptedGit{
		"base tip unresolvable": scripted(map[string]scriptedAnswer{
			"rev-parse refs/heads/main": {out: "fatal: bad ref\n", err: loud},
		}),
		"branch probe fails loudly": scripted(map[string]scriptedAnswer{
			"rev-parse --verify --quiet": {out: "fatal: unable to read ref\n", err: loud},
		}),
		// THE case: no output and no exit status. It used to read as "the branch is
		// absent", which resolveBase counts as merged — a wedged git failing toward
		// the stale base.
		"branch probe times out silently": scripted(map[string]scriptedAnswer{
			"rev-parse --verify --quiet": {err: silent},
		}),
		"branch probe exits with another status, silently": scripted(map[string]scriptedAnswer{
			"rev-parse --verify --quiet": {err: loud},
		}),
		"branch probe says no, but prints": scripted(map[string]scriptedAnswer{
			"rev-parse --verify --quiet": {out: "warning: something\n", err: exitErr(1)},
		}),
		"branch probe answers nothing": scripted(map[string]scriptedAnswer{
			"rev-parse --verify --quiet": {out: "\n"},
		}),
		"ancestry probe fails loudly": scripted(oneDep, map[string]scriptedAnswer{
			"merge-base --is-ancestor": {out: "fatal: not a valid object name\n", err: loud},
		}),
		"ancestry probe times out silently": scripted(oneDep, map[string]scriptedAnswer{
			"merge-base --is-ancestor": {err: silent},
		}),
		"merge probe times out": scripted(oneDep, notAncestor, map[string]scriptedAnswer{
			"merge-tree": {err: silent},
		}),
		"merge probe succeeds with nothing": scripted(oneDep, notAncestor, map[string]scriptedAnswer{
			"merge-tree": {out: "\n"},
		}),
		"merge probe's base tree is unresolvable": scripted(oneDep, notAncestor, map[string]scriptedAnswer{
			"merge-tree":               {out: "tree999\n"},
			"rev-parse base000^{tree}": {out: "fatal\n", err: loud},
		}),
	}
	for name, git := range cases {
		t.Run(name, func(t *testing.T) {
			res, err := resolveBase(git, "/repo", []string{depA})
			if err == nil {
				t.Fatalf("resolveBase succeeded on a broken probe: %+v", res)
			}
			if errors.Is(err, ErrDepsUnmerged) {
				t.Fatalf("err = %v — a broken probe was reported as unmerged dependencies", err)
			}
			if res.StartRef != "" {
				t.Errorf("StartRef = %q alongside a probe failure", res.StartRef)
			}
		})
	}

	twoDeps := map[string]scriptedAnswer{
		"rev-parse --verify --quiet refs/heads/" + depA: {out: "aaa111\n"},
		"rev-parse --verify --quiet refs/heads/" + depB: {out: "bbb222\n"},
		"merge-base --is-ancestor aaa111 base000":       {err: exitErr(1)},
		"merge-base --is-ancestor bbb222 base000":       {err: exitErr(1)},
		"merge-tree":               {out: "tree999\n"},
		"rev-parse base000^{tree}": {out: "tree000\n"},
	}
	// The ancestry probe between two unmerged tips fails too.
	chain := scripted(twoDeps, map[string]scriptedAnswer{
		"merge-base --is-ancestor bbb222 aaa111": {out: "fatal: boom\n", err: loud},
	})
	if _, err := resolveBase(chain, "/repo", []string{depA, depB}); err == nil || errors.Is(err, ErrDepsUnmerged) {
		t.Fatalf("err = %v, want the broken tip-to-tip probe surfaced", err)
	}

	// …and so do both probes of the stack check: A is merged (an ancestor of the
	// base), B is the branch to stack on, and whether A is IN B cannot be asked.
	stackCheck := map[string]scriptedAnswer{
		"rev-parse --verify --quiet refs/heads/" + depA: {out: "aaa111\n"},
		"rev-parse --verify --quiet refs/heads/" + depB: {out: "bbb222\n"},
		"merge-base --is-ancestor bbb222 base000":       {err: exitErr(1)},
		"merge-tree --write-tree base000 bbb222":        {out: "tree999\n"},
		"rev-parse base000^{tree}":                      {out: "tree000\n"},
	}
	for name, extra := range map[string]map[string]scriptedAnswer{
		"ancestry of the merged dep in the stack tip": {
			"merge-base --is-ancestor aaa111 bbb222": {err: silent},
		},
		"merge probe of the merged dep into the stack tip": {
			"merge-base --is-ancestor aaa111 bbb222": {err: exitErr(1)},
			"merge-tree --write-tree bbb222 aaa111":  {err: silent},
		},
	} {
		if _, err := resolveBase(scripted(stackCheck, extra), "/repo", []string{depA, depB}); err == nil || errors.Is(err, ErrDepsUnmerged) {
			t.Errorf("%s: err = %v, want the broken probe surfaced", name, err)
		}
	}
}

// The three answers the merge probe CAN give, and the one it gives on a git too
// old to run it, each read for what it is.
func TestResolveBase_MergeProbeAnswers(t *testing.T) {
	prelude := map[string]scriptedAnswer{
		"rev-parse --verify --quiet":              {out: "dep111\n"},
		"merge-base --is-ancestor dep111 base000": {err: exitErr(1)},
		"rev-parse base000^{tree}":                {out: "tree000\n"},
	}
	resolve := func(mergeTree scriptedAnswer) (baseResolution, error) {
		return resolveBase(scripted(prelude, map[string]scriptedAnswer{"merge-tree": mergeTree}), "/repo", []string{depA})
	}

	// The target's own tree ⇒ a no-op merge ⇒ merged. Only the first line is the
	// tree; whatever follows it is not.
	if res, err := resolve(scriptedAnswer{out: "tree000\nextra line\n"}); err != nil || res.StartRef != "" {
		t.Errorf("no-op merge: StartRef=%q err=%v, want merged", res.StartRef, err)
	}
	// A clean merge that changes the tree ⇒ unmerged ⇒ stacked on.
	if res, err := resolve(scriptedAnswer{out: "tree999\n"}); err != nil || res.StartRef != "dep111" {
		t.Errorf("clean, tree-changing merge: StartRef=%q err=%v, want the dependency stacked on", res.StartRef, err)
	}
	// Exit status 1 ⇒ a conflict ⇒ refused. git prints the conflicted tree and the
	// conflict list on this path; none of it is read as a tree.
	res, err := resolve(scriptedAnswer{out: "tree555\n100644 aaa 1\tshared.txt\n\nCONFLICT (content)\n", err: exitErr(1)})
	wantRefusal(t, res, err, DepsConflict, depA)
	// Any other exit status ⇒ this git cannot run the probe ⇒ the ancestor test
	// alone decides (the documented fallback) ⇒ unmerged ⇒ stacked on.
	if res, err := resolve(scriptedAnswer{out: "usage: git merge-tree …\n", err: exitErr(129)}); err != nil || res.StartRef != "dep111" {
		t.Errorf("unsupported probe: StartRef=%q err=%v, want the ancestor-test fallback to stack", res.StartRef, err)
	}
}

// An absent run branch — exit status 1, nothing printed — is the ONE failure of
// the ref lookup that is an answer, and it counts as merged.
func TestResolveBase_AbsentBranchIsExitStatusOne(t *testing.T) {
	git := scripted(map[string]scriptedAnswer{"rev-parse --verify --quiet": {err: exitErr(1)}})
	res, err := resolveBase(git, "/repo", []string{depA})
	if err != nil {
		t.Fatalf("resolveBase: %v", err)
	}
	if res.StartRef != "" || len(res.DepTips) != 0 {
		t.Errorf("StartRef=%q DepTips=%v, want an absent branch to count as merged and contribute no tip", res.StartRef, res.DepTips)
	}

	// exitStatus itself: a status is a status only when git exited with one.
	if status, ok := exitStatus(exitErr(1)); !ok || status != 1 {
		t.Errorf("exitStatus(exit 1) = %d, %v", status, ok)
	}
	if _, ok := exitStatus(errors.New("timed out")); ok {
		t.Error("exitStatus read a status out of an error that carries none")
	}
	if _, ok := exitStatus(exitErr(-1)); ok {
		t.Error("exitStatus read -1 (killed by a signal) as an exit status")
	}
	if _, ok := exitStatus(fmt.Errorf("git rev-parse: %w: tail", exitErr(128))); !ok {
		t.Error("exitStatus did not see through the wrap worktree.ExecGit applies")
	}
}

// depRunBranches reads the STAMPED branches of the direct dependencies only.
func TestDepRunBranches(t *testing.T) {
	db, taskID, p1, p2 := fixture(t)
	s := newTestService(db, &stubRunner{}, &stubWt{})

	info, err := s.loadPhase(p2)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := s.depRunBranches(info); err != nil || len(got) != 0 {
		t.Fatalf("a dependency that never ran = %v, %v — want no branches", got, err)
	}

	mustExec(t, db, `UPDATE epic_phases SET run_branch=? WHERE id=?`, runcore.PhaseBranch(p1), p1)
	// A phase this one does NOT depend on contributes nothing.
	mustExec(t, db, `INSERT INTO epic_phases
		(workspace_task_id, seq, name, doc_path, depends_on, checkboxes_total, checkboxes_done, run_branch)
		VALUES (?, 3, 'Phase 3', '/plan/phase-3.md', '[]', 1, 1, 'swarm/phase-unrelated')`, taskID)
	got, err := s.depRunBranches(info)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, []string{runcore.PhaseBranch(p1)}) {
		t.Errorf("depRunBranches = %v, want only the direct dependency's %s", got, runcore.PhaseBranch(p1))
	}

	// A closed database surfaces as an error, not as "no dependencies".
	db.Close()
	if _, err := s.depRunBranches(info); err == nil {
		t.Error("depRunBranches on a closed db = nil error")
	}
	if _, err := s.resolveRunBase(info); err == nil {
		t.Error("resolveRunBase on a closed db = nil error")
	}
}

// plainWt hides stubWt's stacking methods: embedding the INTERFACE exposes only
// runcore.WorktreeManager, which is what a manager that cannot stack looks like.
type plainWt struct{ runcore.WorktreeManager }

// A worktree manager that cannot stack still serves every unstacked run, measures
// a reclaim against the base branch alone, and REFUSES a run that has to be
// stacked — starting it on the repo's branch tip would be the wrong-base run.
func TestStart_ManagerThatCannotStack(t *testing.T) {
	db, _, p1, _ := fixture(t)
	inner := &stubWt{}
	s := newTestService(db, &stubRunner{}, inner)
	s.Wt = plainWt{inner}
	mustExec(t, db, `UPDATE epic_phases SET run_start_point='abc123' WHERE id=?`, p1)

	if _, err := s.Start(p1, "", ""); err != nil {
		t.Fatalf("an unstacked run on a plain manager: %v", err)
	}
	if got := inner.startRefs; len(got) != 1 || got[0] != "" {
		t.Errorf("start refs = %v, want one acquire at the default tip", got)
	}
	if got := inner.reclaimBases; len(got) != 1 || got[0] != "" {
		t.Errorf("reclaim bases = %v, want the recorded start point dropped by a manager that cannot take one", got)
	}

	if _, err := s.acquire("/repo/p", "p", "phase-9", "deadbeef"); !errors.Is(err, ErrCannotStack) {
		t.Errorf("stacked acquire on a plain manager = %v, want ErrCannotStack", err)
	}
	if got := len(inner.startRefs); got != 1 {
		t.Errorf("acquires = %d, want the refused one never to reach the manager", got)
	}
}
