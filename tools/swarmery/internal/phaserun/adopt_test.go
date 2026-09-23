package phaserun

import (
	"context"
	"database/sql"
	"errors"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/procgroup"
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

// adoptThenExit adopts phase id's live process, then lets it disappear, and
// returns the terminal row the adoption watcher wrote.
func adoptThenExit(t *testing.T, db *sql.DB, id int64) (string, sql.NullString) {
	t.Helper()
	s := newTestService(db, &stubRunner{}, &stubWt{})
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
