package phaserun

import (
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/runcore"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/worktree"
)

// A RETURNED run (StartOptions.Returned): the operator reviewed the phase's last
// run and sent it back with feedback. These tests pin what that changes — the
// blocked re-run guard and nothing else among the gates, one sentence in the
// prompt, the landing_state reset at the run's end, and a run branch holding the
// returned work being continued rather than refused.

func landingState(t *testing.T, db *sql.DB, phaseID int64) string {
	t.Helper()
	var s string
	if err := db.QueryRow(`SELECT landing_state FROM epic_phases WHERE id=?`, phaseID).Scan(&s); err != nil {
		t.Fatalf("read landing_state: %v", err)
	}
	return s
}

// TestReturnedBypassesBlockedGuard: the blocked-unchanged refusal that a plain
// run (Force:false, Returned:false) still gets is bypassed by a returned run —
// and a returned run still answers to every other gate (unmet dependencies here).
func TestReturnedBypassesBlockedGuard(t *testing.T) {
	db, _, p1, p2 := fixture(t)
	s, r, _ := blockPhase(t, db, p1)

	if _, err := s.StartWith(p1, StartOptions{Force: false, Returned: false}); !errors.Is(err, ErrBlockedUnchanged) {
		t.Fatalf("plain re-run: err = %v, want ErrBlockedUnchanged", err)
	}
	if n := r.specCount(); n != 1 {
		t.Fatalf("spawned %d times after the refusal, want 1", n)
	}

	if _, err := s.StartWith(p1, StartOptions{Returned: true}); err != nil {
		t.Fatalf("returned re-run: %v, want admitted", err)
	}
	if n := r.specCount(); n != 2 {
		t.Errorf("spawned %d times, want the returned run admitted (2)", n)
	}
	if !r.lastSpec().Returned {
		t.Error("RunSpec.Returned = false on a returned run")
	}

	// Only that one guard: phase 2's dependency (phase 1) is not complete.
	var unmet *DepsUnmetError
	if _, err := s.StartWith(p2, StartOptions{Returned: true}); !errors.As(err, &unmet) {
		t.Errorf("returned run with unmet deps: err = %v, want *DepsUnmetError", err)
	}
}

// TestReturnedPromptSentence: a returned run's prompt carries ReturnedNote exactly
// once, right after the first paragraph; a plain run's carries none and is
// byte-identical to BuildPromptStacked's; the forecast contract is undisturbed.
func TestReturnedPromptSentence(t *testing.T) {
	plain := BuildPromptStacked("plan/p.md", "p.md", "BODY", "", "", "", "", runcore.Budget{})
	if got := BuildPromptRun("plan/p.md", "p.md", "BODY", "", "", "", "", false, runcore.Budget{}); got != plain {
		t.Error("BuildPromptRun(returned=false) differs from BuildPromptStacked — must be byte-identical")
	}
	if strings.Contains(plain, ReturnedNote) {
		t.Error("a plain run's prompt carries the returned sentence")
	}

	got := BuildPromptRun("plan/p.md", "p.md", "BODY", "", "", "", "", true, runcore.Budget{})
	if n := strings.Count(got, ReturnedNote); n != 1 {
		t.Fatalf("returned sentence appears %d times, want exactly 1", n)
	}
	firstParaEnd := strings.Index(got, "everything you need has been placed inside.\n\n")
	note := strings.Index(got, ReturnedNote)
	contract := strings.Index(got, "The phase document below is your complete contract.")
	if !(firstParaEnd >= 0 && firstParaEnd < note && note < contract) {
		t.Errorf("note at %d, want right after the first paragraph (%d) and before the contract (%d)", note, firstParaEnd, contract)
	}
	if n := strings.Count(got, notALimit); n != 1 {
		t.Errorf("forecast sentence appears %d times in a returned prompt, want exactly 1", n)
	}

	// Through the service: the spawned prompt carries it once.
	db, _, p1, _ := fixture(t)
	r := &stubRunner{}
	s := newTestService(db, r, &stubWt{})
	if _, err := s.StartWith(p1, StartOptions{Returned: true}); err != nil {
		t.Fatalf("StartWith: %v", err)
	}
	if n := strings.Count(r.firstSpec().Prompt, ReturnedNote); n != 1 {
		t.Errorf("spawned prompt carries the returned sentence %d times, want 1", n)
	}
}

// TestReturnedRunResetsLandingState: once the run the feedback started ends, the
// phase is no longer `returned`. Only `returned` is reset — a landed phase keeps
// its state across a later run.
func TestReturnedRunResetsLandingState(t *testing.T) {
	db, _, p1, _ := fixture(t)
	mustExec(t, db, `UPDATE epic_phases SET landing_state='returned' WHERE id=?`, p1)

	s := newTestService(db, &stubRunner{}, &stubWt{})
	if _, err := s.StartWith(p1, StartOptions{Returned: true}); err != nil {
		t.Fatalf("StartWith: %v", err)
	}
	if state, _, _, _ := phaseRow(t, db, p1); state != "done" {
		t.Fatalf("run_state = %q, want done (test premise)", state)
	}
	if got := landingState(t, db, p1); got != "none" {
		t.Errorf("landing_state = %q after the returned run ended, want none", got)
	}

	// A failed end resets it too: the run is over either way.
	db2, _, q1, _ := fixture(t)
	mustExec(t, db2, `UPDATE epic_phases SET landing_state='returned' WHERE id=?`, q1)
	failing := &stubRunner{runFn: func(spec RunSpec) (*Run, error) {
		return &Run{SessionUUID: spec.SessionUUID, ExitCode: 1, Stderr: "boom"}, nil
	}}
	if _, err := newTestService(db2, failing, &stubWt{}).StartWith(q1, StartOptions{Returned: true}); err != nil {
		t.Fatalf("StartWith (failing): %v", err)
	}
	if got := landingState(t, db2, q1); got != "none" {
		t.Errorf("landing_state = %q after a failed returned run, want none", got)
	}

	// A pushed phase re-run is not touched.
	db3, _, r1, _ := fixture(t)
	mustExec(t, db3, `UPDATE epic_phases SET landing_state='pushed' WHERE id=?`, r1)
	if _, err := newTestService(db3, &stubRunner{}, &stubWt{}).StartWith(r1, StartOptions{}); err != nil {
		t.Fatalf("StartWith (pushed): %v", err)
	}
	if got := landingState(t, db3, r1); got != "pushed" {
		t.Errorf("landing_state = %q, want pushed kept", got)
	}
}

// TestReturnedContinuesOnOwnBranch — real git, real worktree.Manager. The run
// teardown removes the worktree and keeps the branch, so the returned phase's
// branch holds the previous run's commit and no worktree: the reclaim counts it as
// unmerged work. A plain re-run is refused exactly as before (branch-dirty); a
// returned run continues ON that branch — its worktree starts at the previous
// tip, the new commit stacks on top, and run_start_point still names where the
// phase's work began.
func TestReturnedContinuesOnOwnBranch(t *testing.T) {
	repo := newTempRepo(t)
	db, _, p1, _ := fixture(t)
	mustExec(t, db, `UPDATE projects SET path=? WHERE id=1`, repo.dir)
	mainTip := repo.tip("main")

	r := &stubRunner{}
	s := newTestService(db, r, &stubWt{})
	s.Wt = &worktree.Manager{Git: repo.git, Root: filepath.Join(t.TempDir(), "wts")}
	s.Git = repo.git
	doc := phaseDocPath(t, db, p1)
	branch := runcore.PhaseBranch(p1)

	var headAtSpawn, firstCommit, secondCommit string
	var sawFirstFile bool
	work := func(msg string, out *string) func(spec RunSpec) (*Run, error) {
		return func(spec RunSpec) (*Run, error) {
			headAtSpawn = strings.TrimSpace(repo.runIn(spec.Cwd, "rev-parse", "HEAD"))
			_, statErr := os.Stat(filepath.Join(spec.Cwd, "f2.txt"))
			sawFirstFile = statErr == nil
			*out = repo.commitIn(spec.Cwd, msg)
			mustWriteDoc(t, filepath.Join(spec.Cwd, worktree.LentPlanDocRel(doc)), "# Phase 1 — Schema\n\n- [x] a\n- [x] b\n")
			return &Run{SessionUUID: spec.SessionUUID, ExitCode: 0}, nil
		}
	}

	// Run 1: the work the operator will send back.
	r.runFn = work("phase 1 first pass", &firstCommit)
	if _, err := s.StartWith(p1, StartOptions{}); err != nil {
		t.Fatalf("first run: %v", err)
	}
	if got := repo.tip("refs/heads/" + branch); got != firstCommit {
		t.Fatalf("branch tip after run 1 = %s, want its commit %s (premise)", got, firstCommit)
	}
	if sp := phaseStartPoint(t, db, p1); sp.String != mainTip {
		t.Fatalf("run_start_point = %q, want main %s (premise)", sp.String, mainTip)
	}

	// A plain re-run: the existing reclaim refuses the branch that holds the work.
	_, err := s.StartWith(p1, StartOptions{})
	var dirty *BranchDirtyError
	if !errors.As(err, &dirty) {
		t.Fatalf("plain re-run: err = %v, want *BranchDirtyError", err)
	}
	if want := "run branch " + branch + " has 1 unmerged commit(s)"; err.Error() != want {
		t.Errorf("refusal = %q, want %q", err.Error(), want)
	}

	// The returned run continues on it.
	mustExec(t, db, `UPDATE epic_phases SET landing_state='returned' WHERE id=?`, p1)
	r.mu.Lock()
	r.runFn = work("phase 1 after feedback", &secondCommit)
	r.mu.Unlock()
	if _, err := s.StartWith(p1, StartOptions{Returned: true}); err != nil {
		t.Fatalf("returned run: %v", err)
	}
	if headAtSpawn != firstCommit {
		t.Errorf("worktree HEAD at spawn = %s, want the returned work's tip %s", headAtSpawn, firstCommit)
	}
	if !sawFirstFile {
		t.Error("the first pass's file was not in the returned run's worktree")
	}
	if got := repo.tip("refs/heads/" + branch); got != secondCommit {
		t.Errorf("branch tip = %s, want the returned run's commit %s", got, secondCommit)
	}
	if n := strings.TrimSpace(repo.run("rev-list", "--count", mainTip+"..refs/heads/"+branch)); n != "2" {
		t.Errorf("commits on %s past main = %s, want 2 (both passes)", branch, n)
	}
	if sp := phaseStartPoint(t, db, p1); sp.String != mainTip {
		t.Errorf("run_start_point = %q, want the phase's original start %s kept", sp.String, mainTip)
	}
	if got := runBranchOf(t, db, p1); got.String != branch {
		t.Errorf("run_branch = %q, want %q", got.String, branch)
	}
	if state, _, _, _ := phaseRow(t, db, p1); state != "done" {
		t.Errorf("run_state = %q, want done", state)
	}
	if got := landingState(t, db, p1); got != "none" {
		t.Errorf("landing_state = %q, want none after the returned run", got)
	}
	if got := repo.tip("main"); got != mainTip {
		t.Errorf("main moved to %s — the daemon must never merge", got)
	}
}

// TestReturnedRefusedStartRetriedByPlainRun — real git. The return endpoint wrote
// the feedback and stamped `returned`, then its start was refused (the run budget
// is full). A later PLAIN start (StartOptions{}) — the operator pressing Run, or
// a retry once a slot frees — is the returned run: admitted, continuing on the
// phase's own branch (not refused as branch-dirty), with the returned sentence in
// its prompt exactly once.
func TestReturnedRefusedStartRetriedByPlainRun(t *testing.T) {
	repo := newTempRepo(t)
	db, _, p1, _ := fixture(t)
	mustExec(t, db, `UPDATE projects SET path=? WHERE id=1`, repo.dir)

	r := &stubRunner{}
	s := newTestService(db, r, &stubWt{})
	s.Wt = &worktree.Manager{Git: repo.git, Root: filepath.Join(t.TempDir(), "wts")}
	s.Git = repo.git
	doc := phaseDocPath(t, db, p1)

	var headAtSpawn, firstCommit, secondCommit string
	work := func(msg string, out *string) func(spec RunSpec) (*Run, error) {
		return func(spec RunSpec) (*Run, error) {
			headAtSpawn = strings.TrimSpace(repo.runIn(spec.Cwd, "rev-parse", "HEAD"))
			*out = repo.commitIn(spec.Cwd, msg)
			mustWriteDoc(t, filepath.Join(spec.Cwd, worktree.LentPlanDocRel(doc)), "# Phase 1 — Schema\n\n- [x] a\n- [x] b\n")
			return &Run{SessionUUID: spec.SessionUUID, ExitCode: 0}, nil
		}
	}
	r.runFn = work("phase 1 first pass", &firstCommit)
	if _, err := s.StartWith(p1, StartOptions{}); err != nil {
		t.Fatalf("first run: %v", err)
	}

	// The return: feedback written, phase stamped, and the start refused.
	mustExec(t, db, `UPDATE epic_phases SET landing_state='returned' WHERE id=?`, p1)
	s.Slots = runcore.NewSlots(1)
	if _, err := s.Slots.TryAcquire(runcore.SlotKey("planrun", 77), "u-plan", nil); err != nil {
		t.Fatal(err)
	}
	var noSlot *runcore.NoSlotError
	if _, err := s.StartWith(p1, StartOptions{Returned: true}); !errors.As(err, &noSlot) {
		t.Fatalf("returned start with a full budget: err = %v, want *runcore.NoSlotError", err)
	}
	if got := landingState(t, db, p1); got != "returned" {
		t.Fatalf("landing_state = %q after the refused start, want returned kept", got)
	}

	// The slot frees; a plain start is the returned run.
	s.Slots.Release(runcore.SlotKey("planrun", 77))
	r.mu.Lock()
	r.runFn = work("phase 1 after feedback", &secondCommit)
	r.mu.Unlock()
	if _, err := s.StartWith(p1, StartOptions{}); err != nil {
		t.Fatalf("plain start of a returned phase: %v, want admitted on its own branch", err)
	}
	spec := r.lastSpec()
	if !spec.Returned {
		t.Error("RunSpec.Returned = false for a plain start of a returned phase")
	}
	if n := strings.Count(spec.Prompt, ReturnedNote); n != 1 {
		t.Errorf("the prompt carries the returned sentence %d times, want exactly 1", n)
	}
	if headAtSpawn != firstCommit {
		t.Errorf("worktree HEAD at spawn = %s, want the returned work's tip %s", headAtSpawn, firstCommit)
	}
	if got := repo.tip("refs/heads/" + runcore.PhaseBranch(p1)); got != secondCommit {
		t.Errorf("branch tip = %s, want the returned run's commit %s", got, secondCommit)
	}
	if got := landingState(t, db, p1); got != "none" {
		t.Errorf("landing_state = %q after the returned run ended, want none", got)
	}
}

// TestReturnedPhaseBypassesBlockedGuardOnPlainStart: a returned phase whose last
// run blocked is admitted by a plain start — the stamped `returned` is the
// operator's feedback, which is what the guard waits for — while Force stays
// false on the spec's options and a phase that is NOT returned is still refused.
func TestReturnedPhaseBypassesBlockedGuardOnPlainStart(t *testing.T) {
	db, _, p1, _ := fixture(t)
	s, r, _ := blockPhase(t, db, p1)
	if _, err := s.StartWith(p1, StartOptions{}); !errors.Is(err, ErrBlockedUnchanged) {
		t.Fatalf("plain re-run of a blocked phase: err = %v, want ErrBlockedUnchanged (premise)", err)
	}
	mustExec(t, db, `UPDATE epic_phases SET landing_state='returned' WHERE id=?`, p1)
	if _, err := s.StartWith(p1, StartOptions{}); err != nil {
		t.Fatalf("plain start of a returned phase: %v, want admitted", err)
	}
	if !r.lastSpec().Returned {
		t.Error("RunSpec.Returned = false for a plain start of a returned phase")
	}
}

// cutOnlyWt is a real worktree.Manager seen through the interfaces a manager
// WITHOUT AcquireExisting offers (the embedded interface promotes only its own
// methods), so continueOwnBranch takes its release-and-re-cut fallback.
// failAcquire makes the re-cut's AcquireAt fail after the branch was released.
type cutOnlyWt struct {
	runcore.WorktreeManager
	m           *worktree.Manager
	failAcquire bool
}

func (w *cutOnlyWt) AcquireAt(repoRoot, projectSlug, taskID, startRef string) (worktree.Acquired, error) {
	if w.failAcquire {
		return worktree.Acquired{}, errors.New("acquire refused by the test")
	}
	return w.m.AcquireAt(repoRoot, projectSlug, taskID, startRef)
}

func (w *cutOnlyWt) ReclaimEmptyBranchAt(repoRoot, branch, baseRef string) (int, error) {
	return w.m.ReclaimEmptyBranchAt(repoRoot, branch, baseRef)
}

// TestReturnedFallbackRestoresBranchOnFailedAcquire — real git. A manager that
// cannot check an existing branch out makes continueOwnBranch release the branch
// and re-cut it at its tip. When that re-cut fails, the branch is restored at the
// same tip — the returned work is never left unnamed — and the start is refused
// with the phase still `returned`. Once the manager can acquire again, the same
// fallback continues the branch.
func TestReturnedFallbackRestoresBranchOnFailedAcquire(t *testing.T) {
	repo := newTempRepo(t)
	db, _, p1, _ := fixture(t)
	mustExec(t, db, `UPDATE projects SET path=? WHERE id=1`, repo.dir)

	r := &stubRunner{}
	s := newTestService(db, r, &stubWt{})
	mgr := &worktree.Manager{Git: repo.git, Root: filepath.Join(t.TempDir(), "wts")}
	s.Wt = mgr
	s.Git = repo.git
	doc := phaseDocPath(t, db, p1)
	branch := runcore.PhaseBranch(p1)

	var headAtSpawn, firstCommit, secondCommit string
	work := func(msg string, out *string) func(spec RunSpec) (*Run, error) {
		return func(spec RunSpec) (*Run, error) {
			headAtSpawn = strings.TrimSpace(repo.runIn(spec.Cwd, "rev-parse", "HEAD"))
			*out = repo.commitIn(spec.Cwd, msg)
			mustWriteDoc(t, filepath.Join(spec.Cwd, worktree.LentPlanDocRel(doc)), "# Phase 1 — Schema\n\n- [x] a\n- [x] b\n")
			return &Run{SessionUUID: spec.SessionUUID, ExitCode: 0}, nil
		}
	}
	r.runFn = work("phase 1 first pass", &firstCommit)
	if _, err := s.StartWith(p1, StartOptions{}); err != nil {
		t.Fatalf("first run: %v", err)
	}

	mustExec(t, db, `UPDATE epic_phases SET landing_state='returned' WHERE id=?`, p1)
	cut := &cutOnlyWt{WorktreeManager: mgr, m: mgr, failAcquire: true}
	s.Wt = cut
	if _, err := s.StartWith(p1, StartOptions{Returned: true}); err == nil {
		t.Fatal("returned start with a failing acquire: err = nil, want the acquire failure")
	}
	if got := repo.tip("refs/heads/" + branch); got != firstCommit {
		t.Fatalf("branch tip after the failed re-cut = %q, want %s restored", got, firstCommit)
	}
	if got := landingState(t, db, p1); got != "returned" {
		t.Errorf("landing_state = %q after the refused start, want returned kept", got)
	}

	cut.failAcquire = false
	r.mu.Lock()
	r.runFn = work("phase 1 after feedback", &secondCommit)
	r.mu.Unlock()
	if _, err := s.StartWith(p1, StartOptions{}); err != nil {
		t.Fatalf("retry through the fallback: %v", err)
	}
	if headAtSpawn != firstCommit {
		t.Errorf("worktree HEAD at spawn = %s, want the returned work's tip %s", headAtSpawn, firstCommit)
	}
	if got := repo.tip("refs/heads/" + branch); got != secondCommit {
		t.Errorf("branch tip = %s, want the returned run's commit %s", got, secondCommit)
	}
}

// TestReturnedRunRestoresOpenPR: a phase returned while its change request was
// open goes back to pr_open when the returned run ends — not to none, which would
// read `ready` and make the next `land pr` try to open a second change request.
// pr_url and pr_number survive the return and the run untouched.
func TestReturnedRunRestoresOpenPR(t *testing.T) {
	const prURL = "https://github.com/acme/widgets/pull/9"
	db, _, p1, _ := fixture(t)
	mustExec(t, db, `UPDATE epic_phases SET landing_state='returned', pr_url=?, pr_number=9 WHERE id=?`, prURL, p1)

	if _, err := newTestService(db, &stubRunner{}, &stubWt{}).StartWith(p1, StartOptions{Returned: true}); err != nil {
		t.Fatalf("StartWith: %v", err)
	}
	if state, _, _, _ := phaseRow(t, db, p1); state != "done" {
		t.Fatalf("run_state = %q, want done (test premise)", state)
	}
	if got := landingState(t, db, p1); got != "pr_open" {
		t.Errorf("landing_state = %q after the returned run ended, want pr_open restored", got)
	}
	var url string
	var num int
	if err := db.QueryRow(`SELECT pr_url, pr_number FROM epic_phases WHERE id=?`, p1).Scan(&url, &num); err != nil {
		t.Fatal(err)
	}
	if url != prURL || num != 9 {
		t.Errorf("pr_url/pr_number = %q/%d, want %q/9 kept", url, num, prURL)
	}
}

// TestHealStaleResetsReturned: a returned phase whose run a daemon restart
// orphaned is healed like stamp() settles one — out of `returned`, back to pr_open
// when a change request was open and to none otherwise. A returned phase that is
// NOT running (its start was refused) is not the heal's to touch: it is still
// waiting for its run.
func TestHealStaleResetsReturned(t *testing.T) {
	db, _, p1, p2 := fixture(t)
	mustExec(t, db, `UPDATE epic_phases SET run_state='running', landing_state='returned',
		pr_url='https://github.com/acme/widgets/pull/9' WHERE id=?`, p1)
	mustExec(t, db, `UPDATE epic_phases SET run_state='running', landing_state='returned' WHERE id=?`, p2)
	if err := newTestService(db, &stubRunner{}, &stubWt{}).HealStale(); err != nil {
		t.Fatalf("HealStale: %v", err)
	}
	if got := landingState(t, db, p1); got != "pr_open" {
		t.Errorf("healed returned phase with a PR: landing_state = %q, want pr_open", got)
	}
	if got := landingState(t, db, p2); got != "none" {
		t.Errorf("healed returned phase without a PR: landing_state = %q, want none", got)
	}

	db2, _, q1, _ := fixture(t)
	mustExec(t, db2, `UPDATE epic_phases SET run_state='done', landing_state='returned' WHERE id=?`, q1)
	if err := newTestService(db2, &stubRunner{}, &stubWt{}).HealStale(); err != nil {
		t.Fatalf("HealStale: %v", err)
	}
	if got := landingState(t, db2, q1); got != "returned" {
		t.Errorf("idle returned phase: landing_state = %q, want returned kept", got)
	}
}
