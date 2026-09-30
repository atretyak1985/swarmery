package phaserun

import (
	"errors"
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

// oldGit is a git that predates `merge-tree --write-tree` (< 2.38): it rejects
// the option exactly as such a git does, and runs everything else for real.
type oldGit struct{ worktree.ExecGit }

func (g oldGit) Run(dir string, args ...string) (string, error) {
	if len(args) > 1 && args[0] == "merge-tree" && args[1] == "--write-tree" {
		return "usage: git merge-tree <base-tree> <branch1> <branch2>\n", errors.New("exit status 129")
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
}

// A dependency that CONFLICTS with the base is not a no-op merge: unmerged.
func TestResolveBase_ConflictingDepIsUnmerged(t *testing.T) {
	r := newTempRepo(t)
	r.run("checkout", "-q", "-b", depA, "main")
	mustWriteDoc(t, filepath.Join(r.dir, "shared.txt"), "from the dependency\n")
	r.run("add", "shared.txt")
	r.run("commit", "-q", "-m", "dep writes shared.txt")
	tip := r.tip("HEAD")
	r.run("checkout", "-q", "main")
	mustWriteDoc(t, filepath.Join(r.dir, "shared.txt"), "from main\n")
	r.run("add", "shared.txt")
	r.run("commit", "-q", "-m", "main writes shared.txt")

	res, err := resolveBase(r.git, r.dir, []string{depA})
	if err != nil {
		t.Fatalf("resolveBase: %v", err)
	}
	if res.StartRef != tip {
		t.Errorf("StartRef = %q, want the conflicting dependency's tip %s", res.StartRef, tip)
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

// A probe git could not answer is an ERROR at every step — never a confident
// "merged", "missing" or "not an ancestor".
func TestResolveBase_BrokenProbesAreErrors(t *testing.T) {
	head := map[string]scriptedAnswer{
		"symbolic-ref --short HEAD": {out: "main\n"},
		"rev-parse refs/heads/main": {out: "base000\n"},
	}
	with := func(extra map[string]scriptedAnswer) scriptedGit {
		all := make(map[string]scriptedAnswer, len(head)+len(extra))
		for k, v := range head {
			all[k] = v
		}
		for k, v := range extra {
			all[k] = v
		}
		return scriptedGit{answers: all}
	}
	broken := errors.New("exit 128")

	cases := map[string]scriptedGit{
		"base tip unresolvable": with(map[string]scriptedAnswer{
			"rev-parse refs/heads/main": {out: "fatal: bad ref\n", err: broken},
		}),
		"branch probe fails loudly": with(map[string]scriptedAnswer{
			"rev-parse --verify --quiet": {out: "fatal: unable to read ref\n", err: broken},
		}),
		"branch probe answers nothing": with(map[string]scriptedAnswer{
			"rev-parse --verify --quiet": {out: "\n"},
		}),
		"ancestry probe fails loudly": with(map[string]scriptedAnswer{
			"rev-parse --verify --quiet": {out: "dep111\n"},
			"merge-base --is-ancestor":   {out: "fatal: not a valid object name\n", err: broken},
		}),
	}
	for name, git := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := resolveBase(git, "/repo", []string{depA}); err == nil {
				t.Fatal("resolveBase succeeded on a broken probe")
			} else if errors.Is(err, ErrDepsUnmerged) {
				t.Fatalf("err = %v — a broken probe was reported as unmerged dependencies", err)
			}
		})
	}

	// The ancestry probe between two unmerged tips fails too.
	chain := with(map[string]scriptedAnswer{
		"rev-parse --verify --quiet refs/heads/" + depA: {out: "aaa111\n"},
		"rev-parse --verify --quiet refs/heads/" + depB: {out: "bbb222\n"},
		"merge-base --is-ancestor aaa111 base000":       {err: errors.New("exit 1")},
		"merge-base --is-ancestor bbb222 base000":       {err: errors.New("exit 1")},
		"merge-tree":                             {err: errors.New("exit 1")},
		"merge-base --is-ancestor bbb222 aaa111": {out: "fatal: boom\n", err: broken},
	})
	if _, err := resolveBase(chain, "/repo", []string{depA, depB}); err == nil || errors.Is(err, ErrDepsUnmerged) {
		t.Fatalf("err = %v, want the broken tip-to-tip probe surfaced", err)
	}
}

// merge-tree answers that cannot be read as a tree are "not a no-op": unmerged.
func TestResolveBase_UnreadableMergeTreeIsUnmerged(t *testing.T) {
	base := map[string]scriptedAnswer{
		"symbolic-ref --short HEAD":               {out: "main\n"},
		"rev-parse refs/heads/main":               {out: "base000\n"},
		"rev-parse --verify --quiet":              {out: "dep111\n"},
		"merge-base --is-ancestor dep111 base000": {err: errors.New("exit 1")},
	}
	cases := map[string]map[string]scriptedAnswer{
		"empty output":            {"merge-tree": {out: "\n"}},
		"base tree unresolvable":  {"merge-tree": {out: "tree999\n"}, "rev-parse base000^{tree}": {out: "fatal\n", err: errors.New("exit 128")}},
		"a different merged tree": {"merge-tree": {out: "tree999\n"}, "rev-parse base000^{tree}": {out: "tree000\n"}},
	}
	for name, extra := range cases {
		t.Run(name, func(t *testing.T) {
			all := make(map[string]scriptedAnswer)
			for k, v := range base {
				all[k] = v
			}
			for k, v := range extra {
				all[k] = v
			}
			res, err := resolveBase(scriptedGit{answers: all}, "/repo", []string{depA})
			if err != nil {
				t.Fatalf("resolveBase: %v", err)
			}
			if res.StartRef != "dep111" {
				t.Errorf("StartRef = %q, want the dependency stacked on", res.StartRef)
			}
		})
	}
	// The matching tree IS a no-op merge.
	all := map[string]scriptedAnswer{"merge-tree": {out: "tree000\nextra line\n"}, "rev-parse base000^{tree}": {out: "tree000\n"}}
	for k, v := range base {
		all[k] = v
	}
	if res, err := resolveBase(scriptedGit{answers: all}, "/repo", []string{depA}); err != nil || res.StartRef != "" {
		t.Errorf("matching tree: StartRef=%q err=%v, want merged", res.StartRef, err)
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
