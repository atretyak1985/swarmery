package dispatch

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/taskdir"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/worktree"
)

// workspaceDirOf reads the join column between a card and the micro-plan it
// materialized.
func workspaceDirOf(t *testing.T, s *Service, id int64) string {
	t.Helper()
	return taskField(t, s.DB, id, "workspace_dir").String
}

func TestAdmit_MintsAMicroPlanAndTellsTheExecutorAboutIt(t *testing.T) {
	db := testDB(t)
	r := &stubRunner{}
	// A REAL worktree root: the doc is lent into the worktree before the prompt
	// can name it, so a fake path would make this test assert the degraded
	// docless path instead of the one it is about.
	s := newTestService(t, db, r, &stubWt{root: t.TempDir()})
	s.WorkspaceRoot = t.TempDir()
	id := insertTask(t, db, "T-42", taskOpts{})
	if _, err := db.Exec(`UPDATE tasks SET title='Fix the janitor sweep' WHERE id=?`, id); err != nil {
		t.Fatal(err)
	}

	s.Schedule()
	waitFor(t, func() bool { return column(t, db, id) != "todo" })

	dir := workspaceDirOf(t, s, id)
	if dir == "" {
		t.Fatal("tasks.workspace_dir is empty — nothing joins the card to its micro-plan")
	}
	doc := taskdir.PhaseDocPath(dir)
	if _, err := os.Stat(doc); err != nil {
		t.Fatalf("the micro-plan's phase doc is missing: %v", err)
	}
	if !strings.HasPrefix(dir, s.WorkspaceRoot) {
		t.Errorf("micro-plan landed outside the workspace root: %q", dir)
	}
	if base := filepath.Base(dir); base != "card-t-42" {
		t.Errorf("dir leaf = %q, want card-t-42", base)
	}

	// The executor has to be TOLD about the doc, or the checkboxes and the report
	// never get written and the whole contract is decoration.
	if r.count() == 0 {
		t.Fatal("no run was spawned")
	}
	prompt := r.spec(0).Prompt
	// The doc is named by its WORKTREE-RELATIVE path, not the workspace absolute
	// one: the contract makes the worktree the agent's single root, and an
	// instruction to edit outside it is refused by the sandbox.
	wantRel := filepath.Join(worktree.PlanDocDir, filepath.Base(doc))
	if !strings.Contains(prompt, wantRel) {
		t.Errorf("the prompt does not name the lent phase doc %q:\n%s", wantRel, prompt)
	}
	if strings.Contains(prompt, doc) {
		t.Errorf("the prompt still hands over the out-of-tree absolute path %q:\n%s", doc, prompt)
	}
	if !strings.Contains(prompt, "## Completion Report") {
		t.Errorf("the prompt does not demand a Completion Report:\n%s", prompt)
	}
	if !strings.Contains(prompt, "- [ ] → - [x]") {
		t.Errorf("the prompt does not ask for the checkboxes to be ticked:\n%s", prompt)
	}
}

// SWARMERY_MICRO_PLANS=0 restores the pre-micro-plan behaviour exactly: no dir, no
// column, and — the part that matters — no paragraph in the contract about a file
// that does not exist.
func TestAdmit_MicroPlansDisabledDegradesToTheOldBehaviour(t *testing.T) {
	t.Setenv(microPlansEnv, "0")
	db := testDB(t)
	r := &stubRunner{}
	s := newTestService(t, db, r, &stubWt{})
	s.WorkspaceRoot = t.TempDir()
	id := insertTask(t, db, "T-42", taskOpts{})

	s.Schedule()
	waitFor(t, func() bool { return column(t, db, id) != "todo" })

	if got := workspaceDirOf(t, s, id); got != "" {
		t.Errorf("workspace_dir = %q, want empty with the feature off", got)
	}
	if entries, err := os.ReadDir(s.WorkspaceRoot); err != nil || len(entries) != 0 {
		t.Errorf("the workspace was written to with the feature off: %v %v", entries, err)
	}
	// No instruction about a file that does not exist — but the card still owes
	// the operator a summary, so the docless branch must name its own in-worktree
	// destination instead of falling silent.
	prompt := r.spec(0).Prompt
	if strings.Contains(prompt, "HAS A PLAN DOCUMENT") {
		t.Errorf("the contract names a plan doc that was never created:\n%s", prompt)
	}
	if !strings.Contains(prompt, worktree.ReportPath) {
		t.Errorf("the docless contract names no report destination:\n%s", prompt)
	}
}

// No workspace root wired (a daemon started before the plumbing, or a test) is the
// same degradation, without needing the env var.
func TestAdmit_NoWorkspaceRootMintsNothing(t *testing.T) {
	db := testDB(t)
	r := &stubRunner{}
	s := newTestService(t, db, r, &stubWt{}) // WorkspaceRoot unset
	id := insertTask(t, db, "T-42", taskOpts{})

	s.Schedule()
	waitFor(t, func() bool { return column(t, db, id) != "todo" })

	if got := workspaceDirOf(t, s, id); got != "" {
		t.Errorf("workspace_dir = %q, want empty", got)
	}
	if r.count() != 1 {
		t.Fatalf("runs = %d, want 1 — the card must still run", r.count())
	}
}

// A mint that FAILS must not stop the work. The run proceeds docless, the card
// carries no error, and the column is untouched — a markdown file the daemon could
// not write is not a reason to refuse a task.
func TestAdmit_MintFailureIsNonFatal(t *testing.T) {
	db := testDB(t)
	r := &stubRunner{}
	s := newTestService(t, db, r, &stubWt{})
	// A FILE where the workspace root should be: every MkdirAll under it fails.
	blocked := filepath.Join(t.TempDir(), "not-a-dir")
	if err := os.WriteFile(blocked, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	s.WorkspaceRoot = blocked
	id := insertTask(t, db, "T-42", taskOpts{})

	s.Schedule()
	waitFor(t, func() bool { return column(t, db, id) != "todo" })

	if got := workspaceDirOf(t, s, id); got != "" {
		t.Errorf("workspace_dir = %q, want empty after a failed mint", got)
	}
	if e := taskField(t, db, id, "dispatch_error"); e.Valid && e.String != "" {
		t.Errorf("dispatch_error = %q — a mint failure must not read as a failed task", e.String)
	}
	if r.count() != 1 {
		t.Fatalf("runs = %d, want 1 — the run proceeds docless", r.count())
	}
	prompt := r.spec(0).Prompt
	if strings.Contains(prompt, "HAS A PLAN DOCUMENT") {
		t.Errorf("the contract names a doc that failed to be created:\n%s", prompt)
	}
	if !strings.Contains(prompt, worktree.ReportPath) {
		t.Errorf("a failed mint must still leave a report destination:\n%s", prompt)
	}
}

// Re-dispatch resolves to the SAME dir (deterministic slug) and leaves the
// executor's ticks alone — the idempotency taskdir guarantees, asserted through
// dispatch because this is the path that exercises it in production.
func TestAdmit_ReDispatchReusesTheMicroPlan(t *testing.T) {
	db := testDB(t)
	s := newTestService(t, db, &stubRunner{}, &stubWt{})
	s.WorkspaceRoot = t.TempDir()
	id := insertTask(t, db, "T-42", taskOpts{})

	s.Schedule()
	waitFor(t, func() bool { return column(t, db, id) != "todo" })
	dir := workspaceDirOf(t, s, id)

	// The executor ticked a box and wrote its report.
	doc := taskdir.PhaseDocPath(dir)
	raw, err := os.ReadFile(doc)
	if err != nil {
		t.Fatal(err)
	}
	worked := strings.Replace(string(raw), "- [ ] Either the task", "- [x] Either the task", 1) + "\nShipped it.\n"
	if err := os.WriteFile(doc, []byte(worked), 0o644); err != nil {
		t.Fatal(err)
	}

	// Back to todo and dispatched again.
	if _, err := db.Exec(`UPDATE tasks SET board_column='todo', status='queued' WHERE id=?`, id); err != nil {
		t.Fatal(err)
	}
	s.clearActive(id)
	s.Schedule()
	waitFor(t, func() bool { return column(t, db, id) != "todo" })

	if again := workspaceDirOf(t, s, id); again != dir {
		t.Errorf("re-dispatch minted %q, want the same dir %q", again, dir)
	}
	after, err := os.ReadFile(doc)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(after), "- [x] Either the task") || !strings.Contains(string(after), "Shipped it.") {
		t.Errorf("re-dispatch erased the previous run's record:\n%s", after)
	}
}

// The run's session must link to BOTH task rows: the card (so the board shows the
// transcript) and the micro-plan's workspace task (so the Plans page does). Without
// the second link a micro-plan renders as a plan no session ever ran — exactly the
// blindness phase 3 removed for phase and plan runs.
func TestRunPlaybook_LinksTheSessionToTheMicroPlanToo(t *testing.T) {
	db := testDB(t)
	s := newTestService(t, db, &stubRunner{}, &stubWt{})
	s.WorkspaceRoot = t.TempDir()
	id := insertTask(t, db, "T-42", taskOpts{})

	// The transcript is already ingested, so linking resolves on the first try.
	if _, err := db.Exec(`INSERT INTO sessions (session_uuid, started_at, project_id)
		VALUES ('uuid-1', '2026-08-17T10:00:00Z', 1)`); err != nil {
		t.Fatal(err)
	}
	// And wsingest has already indexed the dir the mint is about to create: the
	// workspace task row plus the 'plan' artifact that points at its plan/ dir.
	dir := taskdir.Dir(s.WorkspaceRoot, "p", "T-42", s.clock())
	res, err := db.Exec(`INSERT INTO tasks (project_id, title, prompt, status, created_at,
		source, external_id) VALUES (1,'micro','goal','running','2026-08-17T00:00:00Z',
		'workspace','2026-08-17-card-t-42')`)
	if err != nil {
		t.Fatal(err)
	}
	wsTaskID, _ := res.LastInsertId()
	if _, err := db.Exec(`INSERT INTO task_artifacts (task_id, kind, path, content_hash, parsed_at)
		VALUES (?, 'plan', ?, 'h', '2026-08-17T00:00:00Z')`, wsTaskID, filepath.Join(dir, "plan")); err != nil {
		t.Fatal(err)
	}

	s.Schedule()
	waitFor(t, func() bool { return column(t, db, id) != "todo" })

	for _, tc := range []struct {
		name   string
		taskID int64
	}{{"the board card", id}, {"the micro-plan's workspace task", wsTaskID}} {
		var n int
		if err := db.QueryRow(`
			SELECT COUNT(*) FROM task_sessions ts JOIN sessions se ON se.id = ts.session_id
			 WHERE ts.task_id = ? AND se.session_uuid = 'uuid-1' AND ts.link_source='explicit'`,
			tc.taskID).Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n != 1 {
			t.Errorf("%s has %d explicit links to the run's session, want 1", tc.name, n)
		}
	}
}

// A card with NO plan doc is told to write worktree.ReportPath, so the daemon
// has to read that file back onto the card. If it does not, the contract is
// pointing the agent at a file nobody opens — worse than saying nothing, because
// it costs a turn and reads as a promise.
func TestRunPlaybook_DoclessReportLandsOnTheCard(t *testing.T) {
	t.Setenv(microPlansEnv, "0") // no plan doc ⇒ the docless branch
	db := testDB(t)
	root := t.TempDir()
	wt := &stubWt{root: root}
	r := &stubRunner{}
	// The agent's side of the contract: write the report inside the worktree.
	r.run = func(spec RunSpec) (*Run, error) {
		if err := os.MkdirAll(filepath.Join(spec.Cwd, filepath.Dir(worktree.ReportPath)), 0o755); err != nil {
			return nil, err
		}
		if err := os.WriteFile(filepath.Join(spec.Cwd, worktree.ReportPath),
			[]byte("Rewired the janitor sweep; see internal/prune/sweep.go."), 0o644); err != nil {
			return nil, err
		}
		return &Run{SessionUUID: spec.SessionUUID, ExitCode: 0}, nil
	}
	s := newTestService(t, db, r, wt)
	id := insertTask(t, db, "T-77", taskOpts{})

	s.Schedule()
	waitFor(t, func() bool { return column(t, db, id) != "todo" })
	waitFor(t, func() bool { return taskField(t, db, id, "result_note").String != "" })

	note := taskField(t, db, id, "result_note").String
	if !strings.Contains(note, "Rewired the janitor sweep") {
		t.Errorf("result_note = %q, want the report the agent wrote", note)
	}
}

// The other half: an agent that wrote no report must not blank a note that is
// already there, and must not fail the run.
func TestRunPlaybook_NoDoclessReportLeavesTheCardAlone(t *testing.T) {
	t.Setenv(microPlansEnv, "0")
	db := testDB(t)
	r := &stubRunner{}
	s := newTestService(t, db, r, &stubWt{root: t.TempDir()})
	id := insertTask(t, db, "T-78", taskOpts{})

	s.Schedule()
	waitFor(t, func() bool { return column(t, db, id) != "todo" })

	if e := taskField(t, db, id, "dispatch_error"); e.Valid && e.String != "" {
		t.Errorf("a missing report became an error: %q", e.String)
	}
}

// A PREMISE STALE / NO-OP / DUPLICATE exit is a card that needed no change. Its
// micro-plan must still read Done — both boxes ticked — with the sentinel line as
// the written evidence in its Completion Report. This is the dispatch half of the
// regression for "a card cannot close without a commit".
//
// The worktree root is REAL so the doc is actually lent and the deferred
// returnRunOutput actually runs: stubWt.Remove deletes nothing, which is exactly
// the failed-removal case where a stale lent copy would otherwise be copied back
// over the ticked doc.
func TestSentinelDone_ClosesTheMicroPlanWithTheSentinelAsEvidence(t *testing.T) {
	db := testDB(t)
	r := &stubRunner{run: func(spec RunSpec) (*Run, error) {
		ingestSession(t, db, spec.SessionUUID, "PREMISE STALE: already on HEAD")
		return &Run{SessionUUID: spec.SessionUUID, ExitCode: 0}, nil
	}}
	s := newTestService(t, db, r, &stubWt{root: t.TempDir()})
	s.WorkspaceRoot = t.TempDir()
	id := insertTask(t, db, "T-stale-mp", taskOpts{})

	s.Schedule()
	waitFor(t, func() bool { return column(t, db, id) == "done" && !s.IsActive(id) })

	dir := workspaceDirOf(t, s, id)
	if dir == "" {
		t.Fatal("no micro-plan was minted")
	}
	raw, err := os.ReadFile(taskdir.PhaseDocPath(dir))
	if err != nil {
		t.Fatal(err)
	}
	doc := string(raw)
	report := doc[strings.LastIndex(doc, "## Completion Report"):]
	if !strings.Contains(report, "PREMISE STALE: already on HEAD") {
		t.Errorf("the sentinel line is not in the Completion Report:\n%s", doc)
	}
	if !strings.Contains(report, "closed without a commit") {
		t.Errorf("the report does not say the card closed without a commit:\n%s", doc)
	}
	if strings.Contains(doc, "- [ ]") || strings.Count(doc, "- [x]") != 2 {
		t.Errorf("want both boxes ticked:\n%s", doc)
	}
}

// An executor that wrote its OWN report in the lent copy before exiting with a
// sentinel keeps it: the copy comes home before the worktree is torn down, and the
// sentinel line is not stacked on top of a report that already explains the close.
func TestSentinelDone_KeepsTheExecutorsOwnReport(t *testing.T) {
	db := testDB(t)
	r := &stubRunner{run: func(spec RunSpec) (*Run, error) {
		lent := filepath.Join(spec.Cwd, worktree.PlanDocDir, taskdir.PhaseDocName)
		raw, err := os.ReadFile(lent)
		if err != nil {
			t.Errorf("read lent doc: %v", err)
		} else if err := os.WriteFile(lent, append(raw, []byte("\nThe guard is already at auth.go:42.\n")...), 0o644); err != nil {
			t.Errorf("write lent doc: %v", err)
		}
		ingestSession(t, db, spec.SessionUUID, "NO-OP: guard already present")
		return &Run{SessionUUID: spec.SessionUUID, ExitCode: 0}, nil
	}}
	s := newTestService(t, db, r, &stubWt{root: t.TempDir()})
	s.WorkspaceRoot = t.TempDir()
	id := insertTask(t, db, "T-noop-mp", taskOpts{})

	s.Schedule()
	waitFor(t, func() bool { return column(t, db, id) == "done" && !s.IsActive(id) })

	raw, err := os.ReadFile(taskdir.PhaseDocPath(workspaceDirOf(t, s, id)))
	if err != nil {
		t.Fatal(err)
	}
	doc := string(raw)
	if !strings.Contains(doc, "The guard is already at auth.go:42.") {
		t.Errorf("the executor's own report was lost:\n%s", doc)
	}
	if strings.Contains(doc, "closed without a commit") {
		t.Errorf("the sentinel evidence was stacked on the executor's report:\n%s", doc)
	}
	if strings.Contains(doc, "- [ ]") {
		t.Errorf("want both boxes ticked:\n%s", doc)
	}
}
