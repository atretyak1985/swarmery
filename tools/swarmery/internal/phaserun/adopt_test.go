package phaserun

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/procgroup"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/runcore"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/worktree"
)

// TestHealStale_AdoptsSurvivingRun: a phase run spawned in its own process group
// outlives a daemon restart. The restarted daemon must recognise it instead of
// stamping it 'failed / daemon restart' while it is still visibly working.
func TestHealStale_AdoptsSurvivingRun(t *testing.T) {
	db, _, p1, p2 := fixture(t)
	mustExec(t, db, `UPDATE epic_phases SET run_state='running', run_session_uuid='live-uuid' WHERE id=?`, p1)
	mustExec(t, db, `UPDATE epic_phases SET run_state='running', run_session_uuid='dead-uuid' WHERE id=?`, p2)
	s := newTestService(db, &stubRunner{}, &stubWt{})
	var watcher func()
	s.Go = func(fn func()) { watcher = fn } // hold the watcher: the run stays in flight
	s.FindRun = func(uuid string) (int, bool) { return 4242, uuid == "live-uuid" }
	alive := true
	s.ProcAlive = func(pid int) bool { return alive && pid == 4242 }

	if err := s.HealStale(); err != nil {
		t.Fatalf("HealStale: %v", err)
	}

	if state, _, _, _ := phaseRow(t, db, p1); state != "running" {
		t.Errorf("adopted phase state = %q, want running", state)
	}
	if state, _, _, runErr := phaseRow(t, db, p2); state != "failed" || runErr.String != "daemon restart" {
		t.Errorf("phase with no live process: state=%q run_error=%q, want failed/daemon restart", state, runErr.String)
	}
	// The slot is held, so a Retry cannot put a second executor in the same worktree.
	if _, err := s.Start(p1, "", ""); !errors.Is(err, ErrRunning) {
		t.Errorf("Start on an adopted phase = %v, want ErrRunning", err)
	}

	// The orphan finally exits: the watcher closes the run out.
	alive = false
	if watcher == nil {
		t.Fatal("adoption spawned no watcher")
	}
	watcher()
	// The survivor left the fixture's criteria UNTICKED and said nothing: the same
	// evidence the normal exit path would call `continue`. Adoption cannot continue
	// (the counter died with the previous daemon), so the honest state is `partial`
	// — not the `done` the vanished pid used to buy.
	state, _, _, runErr := phaseRow(t, db, p1)
	if state != "partial" {
		t.Errorf("adopted run after exit: state = %q, want partial", state)
	}
	if !strings.Contains(runErr.String, "not continued") {
		t.Errorf("run_error = %q, want the not-continued note", runErr.String)
	}
	// Slot released — the phase can run again.
	if _, err := s.Start(p1, "", ""); errors.Is(err, ErrRunning) {
		t.Error("slot still held after the adopted run ended")
	}
}

// TestHealStale_NoUUIDIsHealed: a row with no recorded session uuid has nothing to
// match a process against, so it must fall through to the fail sweep rather than
// being silently kept alive.
func TestHealStale_NoUUIDIsHealed(t *testing.T) {
	db, _, p1, _ := fixture(t)
	mustExec(t, db, `UPDATE epic_phases SET run_state='running', run_session_uuid=NULL WHERE id=?`, p1)
	s := newTestService(db, &stubRunner{}, &stubWt{})
	s.FindRun = func(string) (int, bool) { t.Error("must not probe without a uuid"); return 0, false }

	if err := s.HealStale(); err != nil {
		t.Fatalf("HealStale: %v", err)
	}
	if state, _, _, runErr := phaseRow(t, db, p1); state != "failed" || runErr.String != "daemon restart" {
		t.Errorf("state=%q run_error=%q, want failed/daemon restart", state, runErr.String)
	}
}

// TestAdopt_CancelKillsTheOrphan drives the real seam: Stop on an adopted run has
// no child process to cancel, so it must reach the orphan through its process
// group. Uses a genuine process rather than a fake pid — signalling an invented
// pid is exactly the bug this guards against.
func TestAdopt_CancelKillsTheOrphan(t *testing.T) {
	cmd := exec.CommandContext(context.Background(), "sh", "-c", "sleep 30")
	procgroup.Isolate(cmd, 0)
	if err := cmd.Start(); err != nil {
		t.Fatalf("start victim: %v", err)
	}
	pid := cmd.Process.Pid
	// Reap it as soon as it dies, so the liveness probe does not see a zombie.
	go func() { _ = cmd.Wait() }()
	t.Cleanup(func() { _ = procgroup.Kill(pid) })

	db, _, p1, _ := fixture(t)
	mustExec(t, db, `UPDATE epic_phases SET run_state='running', run_session_uuid='live-uuid' WHERE id=?`, p1)
	s := newTestService(db, &stubRunner{}, &stubWt{})
	s.Go = func(fn func()) { go fn() }
	s.adoptPoll = 5 * time.Millisecond
	s.FindRun = func(string) (int, bool) { return pid, true }

	if err := s.HealStale(); err != nil {
		t.Fatalf("HealStale: %v", err)
	}
	if !s.Cancel(p1) {
		t.Fatal("Cancel on an adopted run = false, want true")
	}
	waitFor(t, func() bool {
		state, _, _, runErr := phaseRow(t, db, p1)
		return state == "failed" && runErr.String == "cancelled"
	})
}

// TestAdopt_TickedCriteriaSettleDone: a survivor that FINISHED its work is still
// `done` — adoption grades by the same evidence, so the fix for the exit-code
// rule must not turn every restart into a `partial`.
func TestAdopt_TickedCriteriaSettleDone(t *testing.T) {
	db, _, p1, _ := fixture(t)
	mustWriteDoc(t, phaseDocPath(t, db, p1), "# Phase 1 — Schema\n\n- [x] a\n- [x] b\n")
	mustExec(t, db, `UPDATE epic_phases SET run_state='running', run_session_uuid='live-uuid' WHERE id=?`, p1)
	seedTranscript(t, db, "live-uuid", "Both criteria landed.\n\nPHASE DONE")

	state, runErr := adoptThenExit(t, db, p1)
	if state != "done" {
		t.Errorf("state = %q, want done — every criterion is ticked", state)
	}
	if !strings.Contains(runErr.String, "exit status unknown") {
		t.Errorf("run_error = %q, want the unknown-exit note kept", runErr.String)
	}
}

// TestAdopt_BlockedTranscriptSettlesBlocked: the ending the executor actually
// wrote outranks the tick count, on the adoption path exactly as on the normal
// one — this is the branch that used to be stamped green by a disappearing pid.
func TestAdopt_BlockedTranscriptSettlesBlocked(t *testing.T) {
	db, _, p1, _ := fixture(t)
	mustWriteDoc(t, phaseDocPath(t, db, p1), "# Phase 1 — Schema\n\n- [x] a\n- [x] b\n")
	mustExec(t, db, `UPDATE epic_phases SET run_state='running', run_session_uuid='live-uuid' WHERE id=?`, p1)
	seedTranscript(t, db, "live-uuid", "PHASE BLOCKED: the credentials the doc assumes are missing")

	state, runErr := adoptThenExit(t, db, p1)
	if state != "blocked" {
		t.Errorf("state = %q, want blocked", state)
	}
	if runErr.String != "the credentials the doc assumes are missing" {
		t.Errorf("run_error = %q, want the blocked reason", runErr.String)
	}
}

// TestAdopt_GradesTheCopyTheExecutorActuallyTicked drives the shape production
// actually produces, which every other adopt test here misses: the executor works
// in the LENT copy inside its worktree (Start's worktree.LendPlanDoc put it
// there), so the workspace document still reads 0/2 at the moment the orphan
// exits having finished everything.
//
// Settling from the workspace copy therefore stamped `partial / 0 of 2 criteria
// ticked` over a finished phase, and — worse — the next Start's LendPlanDoc
// copies that stale doc back OVER the worktree copy, destroying the orphan's
// ticks and its Completion Report. Adoption has to bring the doc home before it
// grades, exactly as the normal exit path does.
func TestAdopt_GradesTheCopyTheExecutorActuallyTicked(t *testing.T) {
	db, _, p1, _ := fixture(t)
	doc := phaseDocPath(t, db, p1)
	wt := &stubWt{root: t.TempDir()}

	// The orphan's worktree, at the deterministic path Start derived, holding the
	// lent doc it ticked and the report it wrote. The workspace copy is untouched.
	lent := lendedDocPath(t, wt, "p", runcore.PhaseTaskName(p1), doc)
	mustWriteDoc(t, lent,
		"# Phase 1 — Schema\n\n- [x] a\n- [x] b\n\n## Completion Report\n\nBoth criteria landed.\n")

	mustExec(t, db, `UPDATE epic_phases SET run_state='running', run_session_uuid='live-uuid' WHERE id=?`, p1)
	// A transcript with no sentinel: the ticks are the only evidence of completion,
	// so the test cannot pass by reading the reply instead of the document.
	seedTranscript(t, db, "live-uuid", "Here is where I got to.")

	state, runErr := adoptThenExitWith(t, db, p1, wt)
	if state != "done" {
		t.Errorf("state = %q, want done — the executor ticked every criterion in the copy it was given", state)
	}
	if strings.Contains(runErr.String, "0 of 2") {
		t.Errorf("run_error = %q asserts a count measured from the pre-run workspace copy", runErr.String)
	}
	// The doc came home: both the ticks and the Completion Report the dashboard
	// renders are now in the workspace document, so the next Start cannot lend the
	// stale copy back over them.
	body, err := os.ReadFile(doc)
	if err != nil {
		t.Fatalf("read workspace doc: %v", err)
	}
	for _, want := range []string{"- [x] a", "- [x] b", "## Completion Report"} {
		if !strings.Contains(string(body), want) {
			t.Errorf("workspace doc lost %q after adoption settled:\n%s", want, body)
		}
	}
	// The measurement interval closes on the returned doc, not on the stale one.
	var after sql.NullInt64
	if err := db.QueryRow(`SELECT run_checkboxes_after FROM epic_phases WHERE id=?`, p1).Scan(&after); err != nil {
		t.Fatalf("read run_checkboxes_after: %v", err)
	}
	if !after.Valid || after.Int64 != 2 {
		t.Errorf("run_checkboxes_after = %v, want 2", after)
	}
}

// TestAdopt_UnlocatableWorktreeDoesNotAssertACount: when the lent copy cannot be
// reached at all, the tick count in the workspace doc describes the run's INPUT.
// Printing it as "0 of N criteria ticked" would state a measurement that was
// never taken — the same falsehood, one layer down. Name the gap instead.
func TestAdopt_UnlocatableWorktreeDoesNotAssertACount(t *testing.T) {
	db, _, p1, _ := fixture(t)
	mustExec(t, db, `UPDATE epic_phases SET run_state='running', run_session_uuid='live-uuid' WHERE id=?`, p1)
	seedTranscript(t, db, "live-uuid", "Here is where I got to.")

	wt := &stubWt{pathErr: errors.New("no home directory")}
	state, runErr := adoptThenExitWith(t, db, p1, wt)
	if state != "partial" {
		t.Errorf("state = %q, want partial", state)
	}
	if strings.Contains(runErr.String, "0 of 2") {
		t.Errorf("run_error = %q quotes a count it never measured", runErr.String)
	}
	if !strings.Contains(runErr.String, "could not be recovered") {
		t.Errorf("run_error = %q, want the unrecoverable doc named", runErr.String)
	}
	if !strings.Contains(runErr.String, "not continued") {
		t.Errorf("run_error = %q, want the not-continued note kept", runErr.String)
	}
}

// lendedDocPath reproduces what Start does on the way in: it creates the
// worktree the stub's Path derives and puts the phase doc where
// worktree.LendPlanDoc puts it, returning that lent copy's path.
func lendedDocPath(t *testing.T, wt *stubWt, slug, taskName, docPath string) string {
	t.Helper()
	wtPath, err := wt.Path(slug, taskName)
	if err != nil {
		t.Fatalf("stub worktree path: %v", err)
	}
	if err := os.MkdirAll(wtPath, 0o755); err != nil {
		t.Fatalf("create worktree: %v", err)
	}
	rel, err := worktree.LendPlanDoc(wtPath, docPath)
	if err != nil {
		t.Fatalf("lend plan doc: %v", err)
	}
	return filepath.Join(wtPath, rel)
}

// adoptThenExit adopts phase id's live process, then lets it disappear, and
// returns the terminal row the adoption watcher wrote.
func adoptThenExit(t *testing.T, db *sql.DB, id int64) (string, sql.NullString) {
	t.Helper()
	return adoptThenExitWith(t, db, id, &stubWt{})
}

// adoptThenExitWith is adoptThenExit with the worktree seam supplied, so a test
// can put a REAL checkout (and the doc lent into it) behind the derived path.
func adoptThenExitWith(t *testing.T, db *sql.DB, id int64, wt *stubWt) (string, sql.NullString) {
	t.Helper()
	s := newTestService(db, &stubRunner{}, wt)
	var watcher func()
	s.Go = func(fn func()) { watcher = fn }
	s.FindRun = func(uuid string) (int, bool) { return 4242, uuid == "live-uuid" }
	alive := true
	s.ProcAlive = func(pid int) bool { return alive && pid == 4242 }
	if err := s.HealStale(); err != nil {
		t.Fatalf("HealStale: %v", err)
	}
	if watcher == nil {
		t.Fatal("adoption spawned no watcher")
	}
	alive = false
	watcher()
	state, _, _, runErr := phaseRow(t, db, id)
	return state, runErr
}
