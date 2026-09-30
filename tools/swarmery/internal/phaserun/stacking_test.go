package phaserun

import (
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/runcore"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/worktree"
)

// Stacking through the real Start: a temp repository, the real worktree.Manager
// and real git, with only the executor stubbed. These are the tests that prove the
// thing the phase is for — the tree a run gets CONTAINS its dependency's work —
// which no scripted worktree manager can show.

// stackEnv is a plan whose phase 1 finished on its run branch and was never
// merged, in a temp repository, with phase 2 depending on it.
type stackEnv struct {
	repo   *tempRepo
	db     *sql.DB
	taskID int64
	p1, p2 int64
	svc    *Service
	runner *stubRunner
	mgr    *worktree.Manager
	// depBranch / depTip are phase 1's run branch and its tip; depFile the file its
	// commit added, which is what "the tree contains the dependency" is checked by.
	depBranch, depTip, depFile string
}

func newStackEnv(t *testing.T) *stackEnv {
	t.Helper()
	repo := newTempRepo(t)
	db, taskID, p1, p2 := fixture(t)
	mustExec(t, db, `UPDATE projects SET path=? WHERE id=1`, repo.dir)

	e := &stackEnv{repo: repo, db: db, taskID: taskID, p1: p1, p2: p2, depBranch: runcore.PhaseBranch(p1)}
	e.depTip = repo.branch(e.depBranch, "main", 1)
	e.depFile = strings.TrimSpace(repo.run("diff", "--name-only", "main", e.depBranch))
	// Phase 1 is COMPLETE by the only measure the dependency gate accepts — every
	// criterion ticked — and its work sits on a branch nobody merged.
	mustExec(t, db, `UPDATE epic_phases SET checkboxes_done=2, run_state='done', run_branch=?, run_start_point=?
		WHERE id=?`, e.depBranch, repo.tip("main"), p1)

	e.runner = &stubRunner{}
	e.mgr = &worktree.Manager{Git: repo.git, Root: filepath.Join(t.TempDir(), "wts")}
	e.svc = newTestService(db, e.runner, &stubWt{})
	e.svc.Wt = e.mgr
	e.svc.Git = repo.git
	return e
}

// finishPhase2 is the executor doing phase 2's work: one commit of its own in the
// worktree, and the lent phase doc ticked.
func (e *stackEnv) finishPhase2(t *testing.T, spec RunSpec) string {
	t.Helper()
	own := e.repo.commitIn(spec.Cwd, "phase 2 work")
	lent := filepath.Join(spec.Cwd, worktree.LentPlanDocRel(phaseDocPath(t, e.db, e.p2)))
	mustWriteDoc(t, lent, "# Phase 2 — UI\n\n- [x] c\n")
	return own
}

func (e *stackEnv) setRun(fn func(spec RunSpec) (*Run, error)) {
	e.runner.mu.Lock()
	e.runner.runFn = fn
	e.runner.mu.Unlock()
}

// TestStartStacksOnDependency: the dependency is complete and unmerged, so the
// run's worktree is cut from the dependency's tip — it contains the dependency's
// commit — and run_start_point records that tip, not main's.
func TestStartStacksOnDependency(t *testing.T) {
	e := newStackEnv(t)
	mainTip := e.repo.tip("main")

	var headAtSpawn, own string
	var sawDepFile bool
	e.setRun(func(spec RunSpec) (*Run, error) {
		headAtSpawn = strings.TrimSpace(e.repo.runIn(spec.Cwd, "rev-parse", "HEAD"))
		_, statErr := os.Stat(filepath.Join(spec.Cwd, e.depFile))
		sawDepFile = statErr == nil
		own = e.finishPhase2(t, spec)
		return &Run{SessionUUID: spec.SessionUUID, ExitCode: 0}, nil
	})

	if _, err := e.svc.Start(e.p2, "", ""); err != nil {
		t.Fatalf("Start: %v", err)
	}

	// The worktree the executor was handed contains the dependency's commit.
	if headAtSpawn != e.depTip {
		t.Errorf("worktree HEAD at spawn = %s, want the dependency tip %s (main is %s)", headAtSpawn, e.depTip, mainTip)
	}
	if !sawDepFile {
		t.Errorf("the dependency's file %s was NOT in the run's worktree — the phase started without the work it depends on", e.depFile)
	}
	// run_start_point == the dependency tip.
	if sp := phaseStartPoint(t, e.db, e.p2); sp.String != e.depTip {
		t.Errorf("run_start_point = %q, want the dependency tip %s", sp.String, e.depTip)
	}
	if got := runBranchOf(t, e.db, e.p2); got.String != runcore.PhaseBranch(e.p2) {
		t.Errorf("run_branch = %q, want %q", got.String, runcore.PhaseBranch(e.p2))
	}
	if state, _, _, _ := phaseRow(t, e.db, e.p2); state != "done" {
		t.Errorf("run_state = %q, want done", state)
	}

	// The run's branch is the dependency plus exactly its own commit.
	branch := "refs/heads/" + runcore.PhaseBranch(e.p2)
	if got := e.repo.tip(branch); got != own {
		t.Errorf("run branch tip = %s, want the run's commit %s", got, own)
	}
	if n := strings.TrimSpace(e.repo.run("rev-list", "--count", e.depTip+".."+branch)); n != "1" {
		t.Errorf("commits on the run branch after the dependency tip = %s, want 1", n)
	}
	// Nothing was merged anywhere: the daemon stacks, it never merges.
	if got := e.repo.tip("main"); got != mainTip {
		t.Errorf("main moved from %s to %s — the daemon must never merge", mainTip, got)
	}
	if got := e.repo.tip("refs/heads/" + e.depBranch); got != e.depTip {
		t.Errorf("the dependency branch moved from %s to %s", e.depTip, got)
	}

	// The executor was told what it is standing on.
	prompt := e.runner.firstSpec().Prompt
	for _, want := range []string{
		"This worktree is stacked on `" + e.depBranch + "`; its commits are not yours.",
		"Open your PR against that branch, or rebase onto the default branch once it merges.",
	} {
		if !strings.Contains(prompt, want) {
			t.Errorf("prompt does not carry %q", want)
		}
	}
}

// TestStartMergedDependencyStartsOnBase: once the dependency IS on main the run
// starts where every run always did, and the prompt says nothing about stacking.
func TestStartMergedDependencyStartsOnBase(t *testing.T) {
	e := newStackEnv(t)
	e.repo.run("merge", "-q", "--ff-only", e.depBranch)
	mainTip := e.repo.tip("main")

	var headAtSpawn string
	e.setRun(func(spec RunSpec) (*Run, error) {
		headAtSpawn = strings.TrimSpace(e.repo.runIn(spec.Cwd, "rev-parse", "HEAD"))
		e.finishPhase2(t, spec)
		return &Run{SessionUUID: spec.SessionUUID, ExitCode: 0}, nil
	})
	if _, err := e.svc.Start(e.p2, "", ""); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if headAtSpawn != mainTip {
		t.Errorf("worktree HEAD at spawn = %s, want main's tip %s", headAtSpawn, mainTip)
	}
	if sp := phaseStartPoint(t, e.db, e.p2); sp.String != mainTip {
		t.Errorf("run_start_point = %q, want main's tip %s", sp.String, mainTip)
	}
	if strings.Contains(e.runner.firstSpec().Prompt, "STACKED BASE") {
		t.Error("an unstacked run's prompt carries the stacking note")
	}
}

// TestStartRefusesDivergentDeps: two complete dependencies on branches that
// diverged. There is no commit that contains both, and combining them is the
// operator's call — so the run is refused with both branches named, and nothing
// at all is left behind.
func TestStartRefusesDivergentDeps(t *testing.T) {
	e := newStackEnv(t)
	const otherBranch = "swarm/phase-777"
	e.repo.branch(otherBranch, "main", 1)
	mustExec(t, e.db, `INSERT INTO epic_phases
		(workspace_task_id, seq, name, doc_path, depends_on, checkboxes_total, checkboxes_done, run_state, run_branch)
		VALUES (?, 3, 'Phase 3', '/plan/phase-3.md', '[]', 1, 1, 'done', ?)`, e.taskID, otherBranch)
	mustExec(t, e.db, `UPDATE epic_phases SET depends_on='[1,3]' WHERE id=?`, e.p2)

	_, err := e.svc.Start(e.p2, "", "")
	if !errors.Is(err, ErrDepsUnmerged) {
		t.Fatalf("err = %v, want ErrDepsUnmerged", err)
	}
	var unmerged *DepsUnmergedError
	if !errors.As(err, &unmerged) {
		t.Fatalf("err = %v, want a *DepsUnmergedError", err)
	}
	want := []string{e.depBranch, otherBranch}
	if want[0] > want[1] {
		want[0], want[1] = want[1], want[0]
	}
	if !reflect.DeepEqual(unmerged.Branches, want) {
		t.Errorf("Branches = %v, want both dependency branches %v", unmerged.Branches, want)
	}
	if unmerged.Base != "main" {
		t.Errorf("Base = %q, want main", unmerged.Base)
	}

	// An admission verdict: no spawn, no worktree, no branch, no slot, no stamp.
	if n := e.runner.specCount(); n != 0 {
		t.Errorf("spawned %d times, want 0", n)
	}
	if out := strings.TrimSpace(e.repo.run("worktree", "list", "--porcelain")); strings.Count(out, "worktree ") != 1 {
		t.Errorf("worktrees after the refusal:\n%s\nwant only the repo itself", out)
	}
	if _, err := e.repo.git.Run(e.repo.dir, "rev-parse", "--verify", "--quiet", "refs/heads/"+runcore.PhaseBranch(e.p2)); err == nil {
		t.Error("a run branch was minted for a refused run")
	}
	if e.svc.Slots.IsActive(e.svc.slotKey(e.p2)) {
		t.Error("the refused run is holding the slot")
	}
	if state, uuid, _, _ := phaseRow(t, e.db, e.p2); state != "idle" || uuid.Valid {
		t.Errorf("row after the refusal = %q / %v, want idle and unstamped", state, uuid)
	}

	// The remedy the refusal names works: once one branch contains the other, the
	// run is admitted and stacked on the one that carries both.
	e.repo.run("checkout", "-q", otherBranch)
	e.repo.run("merge", "-q", "--no-ff", "-m", "merge the other dependency", e.depBranch)
	combined := e.repo.tip("HEAD")
	e.repo.run("checkout", "-q", "main")
	e.setRun(func(spec RunSpec) (*Run, error) {
		e.finishPhase2(t, spec)
		return &Run{SessionUUID: spec.SessionUUID, ExitCode: 0}, nil
	})
	if _, err := e.svc.Start(e.p2, "", ""); err != nil {
		t.Fatalf("Start after the operator merged one dependency into the other: %v", err)
	}
	if sp := phaseStartPoint(t, e.db, e.p2); sp.String != combined {
		t.Errorf("run_start_point = %q, want the combined tip %s", sp.String, combined)
	}
}

// TestVerifyDiffExcludesDependencyCommits: the verifier grades `base...HEAD`. For
// a stacked run the base it is handed is the dependency tip, so the diff holds
// this phase's commit and none of the dependency's — measured against main, the
// dependency's work would be graded as this phase's.
func TestVerifyDiffExcludesDependencyCommits(t *testing.T) {
	e := newStackEnv(t)
	setVerifyMode(t, e.db, e.p2, "strict")

	var ownFile string
	e.setRun(func(spec RunSpec) (*Run, error) {
		own := e.finishPhase2(t, spec)
		ownFile = strings.TrimSpace(e.repo.runIn(spec.Cwd, "diff", "--name-only", own+"^", own))
		return &Run{SessionUUID: spec.SessionUUID, ExitCode: 0}, nil
	})

	// The verifier runs while the worktree still exists, so what it would diff is
	// measured right there, with the request's own base.
	var subjects, files, againstMain []string
	v := &stubVerifier{}
	v.onVerify = func() {
		calls := v.calls()
		req := calls[len(calls)-1]
		subjects = lines(e.repo.runIn(req.WorktreePath, "log", "--format=%s", req.StartPoint+"..HEAD"))
		files = lines(e.repo.runIn(req.WorktreePath, "diff", "--name-only", req.StartPoint+"...HEAD"))
		againstMain = lines(e.repo.runIn(req.WorktreePath, "diff", "--name-only", "main...HEAD"))
	}
	e.svc.Verify = v

	if _, err := e.svc.Start(e.p2, "", ""); err != nil {
		t.Fatalf("Start: %v", err)
	}
	calls := v.calls()
	if len(calls) != 1 {
		t.Fatalf("verify calls = %d, want 1", len(calls))
	}
	if calls[0].StartPoint != e.depTip {
		t.Errorf("verify StartPoint = %s, want the dependency tip %s", calls[0].StartPoint, e.depTip)
	}
	if !reflect.DeepEqual(subjects, []string{"phase 2 work"}) {
		t.Errorf("commits in the verifier's range = %v, want only this phase's", subjects)
	}
	if !reflect.DeepEqual(files, []string{ownFile}) {
		t.Errorf("files in the verifier's diff = %v, want only this phase's %s", files, ownFile)
	}
	for _, f := range files {
		if f == e.depFile {
			t.Errorf("the dependency's file %s is in the verifier's diff", e.depFile)
		}
	}
	// The premise: against main the same worktree shows the dependency's file too.
	if len(againstMain) != 2 {
		t.Errorf("diff against main = %v, want both files — the premise that the base matters", againstMain)
	}
}

// TestStartRetriesAStackedRunThatCommittedNothing: a stacked run that died before
// committing leaves a branch sitting on the dependency's tip. Measured against
// main that is "1 commit ahead" and the retry would be refused as a dirty branch;
// measured against the recorded start point it is empty, and the retry runs.
func TestStartRetriesAStackedRunThatCommittedNothing(t *testing.T) {
	e := newStackEnv(t)
	e.setRun(func(spec RunSpec) (*Run, error) {
		return &Run{SessionUUID: spec.SessionUUID, ExitCode: 2, Stderr: "boom"}, nil
	})
	if _, err := e.svc.Start(e.p2, "", ""); err != nil {
		t.Fatalf("first Start: %v", err)
	}
	if state, _, _, _ := phaseRow(t, e.db, e.p2); state != "failed" {
		t.Fatalf("run_state = %q, want failed (test premise)", state)
	}
	branch := runcore.PhaseBranch(e.p2)
	if got := e.repo.tip("refs/heads/" + branch); got != e.depTip {
		t.Fatalf("leftover branch = %s, want it on the dependency tip %s (test premise)", got, e.depTip)
	}
	if n := strings.TrimSpace(e.repo.run("rev-list", "--count", "main..refs/heads/"+branch)); n != "1" {
		t.Fatalf("leftover branch is %s ahead of main, want 1 (test premise)", n)
	}

	e.setRun(func(spec RunSpec) (*Run, error) {
		e.finishPhase2(t, spec)
		return &Run{SessionUUID: spec.SessionUUID, ExitCode: 0}, nil
	})
	if _, err := e.svc.Start(e.p2, "", ""); err != nil {
		t.Fatalf("retry: %v — a stacked branch with no commits of its own must be reclaimable", err)
	}
	if state, _, _, _ := phaseRow(t, e.db, e.p2); state != "done" {
		t.Errorf("run_state after the retry = %q, want done", state)
	}
}

// TestStartKeepsAStackedBranchWithItsOwnWork: the other half of the reclaim — a
// stacked run that DID commit is still refused as dirty, and counts its own
// commit, not the dependency's.
func TestStartKeepsAStackedBranchWithItsOwnWork(t *testing.T) {
	e := newStackEnv(t)
	e.setRun(func(spec RunSpec) (*Run, error) {
		e.repo.commitIn(spec.Cwd, "half of phase 2")
		return &Run{SessionUUID: spec.SessionUUID, ExitCode: 2, Stderr: "boom"}, nil
	})
	if _, err := e.svc.Start(e.p2, "", ""); err != nil {
		t.Fatalf("first Start: %v", err)
	}

	_, err := e.svc.Start(e.p2, "", "")
	var dirty *BranchDirtyError
	if !errors.As(err, &dirty) {
		t.Fatalf("retry err = %v, want a *BranchDirtyError", err)
	}
	if dirty.CommitsAhead != 1 {
		t.Errorf("CommitsAhead = %d, want 1 — this run's commit, not the dependency's as well", dirty.CommitsAhead)
	}
}

// TestStartHandsReclaimTheRecordedStartPoint: both reclaims — the deterministic
// branch and a previous row id's branch — are measured against run_start_point.
func TestStartHandsReclaimTheRecordedStartPoint(t *testing.T) {
	db, _, p1, _ := fixture(t)
	const orphan = "swarm/phase-1280"
	mustExec(t, db, `UPDATE epic_phases SET run_state='done', run_branch=?, run_start_point='cafe1234' WHERE id=?`, orphan, p1)
	wt := &stubWt{}
	s := newTestService(db, &stubRunner{}, wt)

	if _, err := s.Start(p1, "", ""); err != nil {
		t.Fatalf("Start: %v", err)
	}
	wt.mu.Lock()
	defer wt.mu.Unlock()
	if !reflect.DeepEqual(wt.reclaimBases, []string{"cafe1234", "cafe1234"}) {
		t.Errorf("reclaim bases = %v, want the recorded start point for both reclaims", wt.reclaimBases)
	}
	if !reflect.DeepEqual(wt.startRefs, []string{""}) {
		t.Errorf("start refs = %v, want one unstacked acquire", wt.startRefs)
	}
}

// TestStartClearsTheFingerprintWhenItOpensARun: a running row has no blocked run
// to guard against.
func TestStartClearsTheFingerprintWhenItOpensARun(t *testing.T) {
	db, _, p1, _ := fixture(t)
	mustExec(t, db, `UPDATE epic_phases SET run_blocked_fingerprint='stale' WHERE id=?`, p1)
	r := &stubRunner{block: make(chan struct{})}
	s := newTestService(db, r, &stubWt{})
	s.Go = nil // a real goroutine: the run stays in flight

	if _, err := s.Start(p1, "", ""); err != nil {
		t.Fatalf("Start: %v", err)
	}
	waitFor(t, func() bool {
		state, _, _, _ := phaseRow(t, db, p1)
		return state == "running"
	})
	if fp := storedFingerprint(t, db, p1); fp.Valid {
		t.Errorf("run_blocked_fingerprint = %q on a running row, want NULL", fp.String)
	}
	close(r.block)
	waitFor(t, func() bool { return !s.Slots.IsActive(s.slotKey(p1)) })
}

// TestBuildPromptStacked: the note appears only for a stacked run, names the
// branch, and leaves an unstacked prompt byte-identical.
func TestBuildPromptStacked(t *testing.T) {
	plain := BuildPromptIn("plan/p.md", "p.md", "BODY", "", "", "", runcore.Budget{})
	if got := BuildPromptStacked("plan/p.md", "p.md", "BODY", "", "", "", "", runcore.Budget{}); got != plain {
		t.Error("an unstacked BuildPromptStacked differs from BuildPromptIn — the prompt must be byte-identical")
	}

	stacked := BuildPromptStacked("plan/p.md", "p.md", "BODY", "", "", "", "swarm/phase-41", runcore.Budget{})
	const sentence = "This worktree is stacked on `swarm/phase-41`; its commits are not yours. " +
		"Open your PR against that branch, or rebase onto the default branch once it merges."
	if !strings.Contains(stacked, sentence) {
		t.Errorf("stacked prompt does not carry the sentence:\n%s", stacked)
	}
	// It must not read as permission to push or open a PR from inside the run.
	if !strings.Contains(stacked, "do NOT push, do NOT open a PR") {
		t.Error("the stacking note does not restate that the run itself neither pushes nor opens a PR")
	}
	// The note sits in the orientation block, ahead of the document.
	if strings.Index(stacked, "STACKED BASE") > strings.Index(stacked, "PHASE DOCUMENT (") {
		t.Error("the stacking note comes after the phase document")
	}
	if strings.Replace(stacked, stackNote("swarm/phase-41"), "", 1) != plain {
		t.Error("the stacking note is not the only difference from the unstacked prompt")
	}
}

func lines(out string) []string {
	var res []string
	for _, l := range strings.Split(strings.TrimSpace(out), "\n") {
		if l = strings.TrimSpace(l); l != "" {
			res = append(res, l)
		}
	}
	return res
}
