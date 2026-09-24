package dispatch

import (
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/taskdir"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/worktree"
)

// TestHealStale_AdoptsSurvivingRun: an executor in its own process group outlives
// a daemon restart. Requeuing its task would put a SECOND executor into the
// worktree the first is still writing in, so the task is left in_progress and its
// concurrency slot held until the process is observed to exit.
func TestHealStale_AdoptsSurvivingRun(t *testing.T) {
	db := testDB(t)
	s := newTestService(t, db, &stubRunner{}, &stubWt{})
	live := insertTask(t, db, "T-live", taskOpts{column: "in_progress", worktreePath: "/wt/T-live"})
	dead := insertTask(t, db, "T-dead", taskOpts{column: "in_progress", worktreePath: "/wt/T-dead"})
	mustSetUUID(t, db, live, "live-uuid")
	mustSetUUID(t, db, dead, "dead-uuid")

	var watcher func()
	s.Go = func(fn func()) { watcher = fn } // hold the watcher: the run stays in flight
	s.FindRun = func(uuid string) (int, bool) { return 4242, uuid == "live-uuid" }
	alive := true
	s.ProcAlive = func(pid int) bool { return alive && pid == 4242 }

	if err := s.HealStale(); err != nil {
		t.Fatal(err)
	}
	if got := column(t, db, live); got != "in_progress" {
		t.Errorf("adopted task column = %q, want in_progress", got)
	}
	if got := column(t, db, dead); got != "todo" {
		t.Errorf("task with no live process = %q, want todo", got)
	}
	if !s.isActive(live) {
		t.Error("adopted task does not hold its concurrency slot")
	}

	// The orphan exits: the slot is released so the scheduler can use it again,
	// and the task is left for the evidence-based HealDeadProcess to reclaim.
	alive = false
	if watcher == nil {
		t.Fatal("adoption spawned no watcher")
	}
	watcher()
	if s.isActive(live) {
		t.Error("slot still held after the adopted run ended")
	}
	if got := column(t, db, live); got != "in_progress" {
		t.Errorf("column after the adopted run ended = %q, want in_progress (HealDeadProcess owns the reclaim)", got)
	}
}

// TestHealStale_NoUUIDIsHealed: without a session uuid there is nothing to match a
// process against, so the task must fall through to the requeue.
func TestHealStale_NoUUIDIsHealed(t *testing.T) {
	db := testDB(t)
	s := newTestService(t, db, &stubRunner{}, &stubWt{})
	id := insertTask(t, db, "T-nouuid", taskOpts{column: "in_progress", worktreePath: "/wt/T-nouuid"})
	s.FindRun = func(string) (int, bool) { t.Error("must not probe without a uuid"); return 0, false }
	s.adoptPoll = time.Millisecond

	if err := s.HealStale(); err != nil {
		t.Fatal(err)
	}
	if got := column(t, db, id); got != "todo" {
		t.Errorf("column = %q, want todo", got)
	}
}

func mustSetUUID(t *testing.T, db *sql.DB, id int64, uuid string) {
	t.Helper()
	if _, err := db.Exec(`UPDATE tasks SET dispatch_session_uuid=? WHERE id=?`, uuid, id); err != nil {
		t.Fatal(err)
	}
}

// orphanDoc lays out what a dispatched run leaves behind when the daemon dies
// under it: a micro-plan in the workspace (tasks.workspace_dir) and the lent copy
// in the worktree, into which the executor has already written its report.
// Returns the workspace doc path.
func orphanDoc(t *testing.T, db *sql.DB, id int64, wt string) string {
	t.Helper()
	wsDir := t.TempDir()
	doc := taskdir.PhaseDocPath(wsDir)
	if err := os.MkdirAll(filepath.Dir(doc), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(doc, []byte("# Phase\n\n- [ ] 1.1 do it\n\n## Completion Report\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	rel, err := worktree.LendPlanDoc(wt, doc)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(wt, rel),
		[]byte("# Phase\n\n- [x] 1.1 do it\n\n## Completion Report\n\nShipped the thing.\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE tasks SET workspace_dir=? WHERE id=?`, wsDir, id); err != nil {
		t.Fatal(err)
	}
	return doc
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// TestAdopt_EndedReturnsLentDoc: runPlaybook returns the lent plan doc in a
// defer, and a daemon restart is the one exit no defer survives. An adopted run
// must therefore get the same return trip when it ends — otherwise its
// Completion Report stays in the worktree and the dashboard shows "no summary".
func TestAdopt_EndedReturnsLentDoc(t *testing.T) {
	db := testDB(t)
	s := newTestService(t, db, &stubRunner{}, &stubWt{})
	wt := t.TempDir()
	id := insertTask(t, db, "T-adopt-doc", taskOpts{column: "in_progress", worktreePath: wt})
	mustSetUUID(t, db, id, "live-uuid")
	doc := orphanDoc(t, db, id, wt)

	var watcher func()
	s.Go = func(fn func()) { watcher = fn }
	s.FindRun = func(uuid string) (int, bool) { return 4242, uuid == "live-uuid" }
	alive := true
	s.ProcAlive = func(int) bool { return alive }

	if err := s.HealStale(); err != nil {
		t.Fatal(err)
	}
	// Still running: the orphan is still writing the lent copy, so nothing is
	// returned yet — a half-written report must not be copied home.
	if got := readFile(t, doc); strings.Contains(got, "Shipped the thing.") {
		t.Fatal("the lent doc was returned while the adopted run was still alive")
	}

	alive = false
	if watcher == nil {
		t.Fatal("adoption spawned no watcher")
	}
	watcher()
	if got := readFile(t, doc); !strings.Contains(got, "Shipped the thing.") || !strings.Contains(got, "- [x] 1.1") {
		t.Errorf("workspace doc after the adopted run ended lacks the worktree's report and ticks:\n%s", got)
	}
}

// TestHealStale_DeadAtBootReturnsLentDoc: a run that died WITH the previous
// daemon is not adopted but requeued — and the re-admission lends the workspace
// doc back into the worktree, overwriting the report. It has to come home first.
func TestHealStale_DeadAtBootReturnsLentDoc(t *testing.T) {
	db := testDB(t)
	s := newTestService(t, db, &stubRunner{}, &stubWt{})
	wt := t.TempDir()
	id := insertTask(t, db, "T-dead-doc", taskOpts{column: "in_progress", worktreePath: wt})
	mustSetUUID(t, db, id, "dead-uuid")
	doc := orphanDoc(t, db, id, wt)
	s.FindRun = func(string) (int, bool) { return 0, false }

	if err := s.HealStale(); err != nil {
		t.Fatal(err)
	}
	if got := column(t, db, id); got != "todo" {
		t.Errorf("column = %q, want todo", got)
	}
	if got := readFile(t, doc); !strings.Contains(got, "Shipped the thing.") {
		t.Errorf("workspace doc after the heal lacks the worktree's report:\n%s", got)
	}
}

// TestAdopt_EndedCollectsDoclessReport: a card with no plan doc writes its report
// to worktree.ReportPath, and the normal path reads it onto result_note. The
// adopted run takes that same half of the return trip.
func TestAdopt_EndedCollectsDoclessReport(t *testing.T) {
	db := testDB(t)
	s := newTestService(t, db, &stubRunner{}, &stubWt{})
	wt := t.TempDir()
	id := insertTask(t, db, "T-adopt-docless", taskOpts{column: "in_progress", worktreePath: wt})
	mustSetUUID(t, db, id, "live-uuid")
	if err := os.MkdirAll(filepath.Join(wt, filepath.Dir(worktree.ReportPath)), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(wt, worktree.ReportPath), []byte("Docless report.\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	var watcher func()
	s.Go = func(fn func()) { watcher = fn }
	s.FindRun = func(uuid string) (int, bool) { return 4242, uuid == "live-uuid" }
	s.ProcAlive = func(int) bool { return false }

	if err := s.HealStale(); err != nil {
		t.Fatal(err)
	}
	if watcher == nil {
		t.Fatal("adoption spawned no watcher")
	}
	watcher()
	if got := taskField(t, db, id, "result_note").String; got != "Docless report." {
		t.Errorf("result_note = %q, want the worktree's report", got)
	}
}
