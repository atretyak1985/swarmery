package phaserun

import (
	"database/sql"
	"os"
	"strings"
	"testing"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/runcore"
)

// ── the completion loop (phase 3) ──
//
// Every test here drives ONE branch of runcore.ClassifyEnd through the real
// service, with a FIXTURE TRANSCRIPT: a sessions row plus assistant turns, which
// is exactly what runcore.LastAssistantText reads in production. The stub runner
// plays the executor — what it writes to the phase doc and what the transcript
// says are the two independent inputs the loop weighs, so each test sets both.

// seedTranscript writes the session + assistant turn the completion loop reads.
// Each call appends a turn, so a test can script what the run says on its first
// turn and what it says after a continuation.
func seedTranscript(t *testing.T, db *sql.DB, uuid, text string) {
	t.Helper()
	var sid int64
	err := db.QueryRow(`SELECT id FROM sessions WHERE session_uuid=?`, uuid).Scan(&sid)
	if err == sql.ErrNoRows {
		res, ierr := db.Exec(`INSERT INTO sessions (project_id, session_uuid, started_at)
			VALUES (1, ?, '2026-07-28T12:00:00Z')`, uuid)
		if ierr != nil {
			t.Fatalf("seed session: %v", ierr)
		}
		sid, _ = res.LastInsertId()
	} else if err != nil {
		t.Fatalf("seed session: %v", err)
	}
	var next int
	if err := db.QueryRow(`SELECT COALESCE(MAX(seq),0)+1 FROM turns WHERE session_id=?`, sid).Scan(&next); err != nil {
		t.Fatalf("seed turn seq: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO turns (session_id, seq, role, text, started_at)
		VALUES (?, ?, 'assistant', ?, '2026-07-28T12:00:00Z')`, sid, next, text); err != nil {
		t.Fatalf("seed turn: %v", err)
	}
}

func runEventKinds(t *testing.T, db *sql.DB, phaseID int64) []string {
	t.Helper()
	var kinds []string
	for _, e := range runcore.RunEvents(db, Engine, phaseID) {
		kinds = append(kinds, e.Kind)
	}
	return kinds
}

// TestSettle_Done: the executor ticked every criterion. One spawn, run_state
// `done`, and an uneventful run writes no timeline.
func TestSettle_Done(t *testing.T) {
	db, _, p1, _ := fixture(t)
	doc := phaseDocPath(t, db, p1)

	r := &stubRunner{}
	r.runFn = func(spec RunSpec) (*Run, error) {
		mustWriteDoc(t, doc, "# Phase 1 — Schema\n\n- [x] a\n- [x] b\n")
		seedTranscript(t, db, spec.SessionUUID, "Implemented both criteria.\n\nPHASE DONE")
		return &Run{SessionUUID: spec.SessionUUID, ExitCode: 0}, nil
	}
	s := newTestService(db, r, &stubWt{})
	if _, err := s.Start(p1, "", ""); err != nil {
		t.Fatalf("Start: %v", err)
	}

	state, _, _, runErr := phaseRow(t, db, p1)
	if state != "done" {
		t.Errorf("run_state = %q, want done", state)
	}
	if runErr.Valid {
		t.Errorf("run_error = %q, want NULL", runErr.String)
	}
	if n := r.specCount(); n != 1 {
		t.Errorf("spawned %d times, want 1 — a finished run must never be continued", n)
	}
	if k := runEventKinds(t, db, p1); len(k) != 0 {
		t.Errorf("run events = %v, want none for an uneventful run", k)
	}
}

// TestSettle_Blocked: the reason reaches run_error and the run is NOT continued.
// Nudging a run that asked for a human is exactly the loop the operator pays for
// and then interrupts.
func TestSettle_Blocked(t *testing.T) {
	db, _, p1, _ := fixture(t)

	r := &stubRunner{}
	r.runFn = func(spec RunSpec) (*Run, error) {
		seedTranscript(t, db, spec.SessionUUID,
			"I read the code.\n\nPHASE BLOCKED: the table the doc describes does not exist")
		return &Run{SessionUUID: spec.SessionUUID, ExitCode: 0}, nil
	}
	s := newTestService(db, r, &stubWt{})
	if _, err := s.Start(p1, "", ""); err != nil {
		t.Fatalf("Start: %v", err)
	}

	state, _, _, runErr := phaseRow(t, db, p1)
	if state != "blocked" {
		t.Errorf("run_state = %q, want blocked", state)
	}
	if runErr.String != "the table the doc describes does not exist" {
		t.Errorf("run_error = %q, want the blocked reason", runErr.String)
	}
	if n := r.specCount(); n != 1 {
		t.Errorf("spawned %d times, want 1 — a blocked run must not be continued", n)
	}
	if k := runEventKinds(t, db, p1); len(k) != 1 || k[0] != runcore.EventBlocked {
		t.Errorf("run events = %v, want one blocked event", k)
	}
}

// TestSettle_ContinuedThenDone: the classic Opus-5.x shape — the first turn ends
// with a progress report and unticked criteria, the nudge finishes the work.
func TestSettle_ContinuedThenDone(t *testing.T) {
	db, _, p1, _ := fixture(t)
	doc := phaseDocPath(t, db, p1)

	r := &stubRunner{}
	r.runFn = func(spec RunSpec) (*Run, error) {
		if spec.Resume {
			mustWriteDoc(t, doc, "# Phase 1 — Schema\n\n- [x] a\n- [x] b\n")
			seedTranscript(t, db, spec.SessionUUID, "Finished the second one.\n\nPHASE DONE")
			return &Run{SessionUUID: spec.SessionUUID, ExitCode: 0}, nil
		}
		mustWriteDoc(t, doc, "# Phase 1 — Schema\n\n- [x] a\n- [ ] b\n")
		seedTranscript(t, db, spec.SessionUUID,
			"I finished the first criterion. Next I would do the second — let me know if you want me to continue.")
		return &Run{SessionUUID: spec.SessionUUID, ExitCode: 0}, nil
	}
	s := newTestService(db, r, &stubWt{})
	if _, err := s.Start(p1, "", ""); err != nil {
		t.Fatalf("Start: %v", err)
	}

	state, _, _, runErr := phaseRow(t, db, p1)
	if state != "done" {
		t.Errorf("run_state = %q, want done after the continuation finished the work", state)
	}
	if runErr.Valid {
		t.Errorf("run_error = %q, want NULL", runErr.String)
	}
	if n := r.specCount(); n != 2 {
		t.Fatalf("spawned %d times, want 2 (original + one continuation)", n)
	}

	// The continuation is a RESUME of the same session with the same flags, and
	// it names the criterion that was left.
	cont := r.lastSpec()
	first := r.firstSpec()
	if !cont.Resume {
		t.Error("the continuation must be a resume, not a fresh session")
	}
	if cont.SessionUUID != first.SessionUUID {
		t.Errorf("continuation uuid = %q, want the original %q", cont.SessionUUID, first.SessionUUID)
	}
	if cont.Model != first.Model || cont.Effort != first.Effort ||
		cont.SettingsFile != first.SettingsFile || cont.Cwd != first.Cwd ||
		cont.ProjectPath != first.ProjectPath {
		t.Errorf("continuation changed the run's flags:\n got %+v\nwant %+v", cont, first)
	}
	if cont.Effort == "" {
		t.Error("an unpinned --effort on a resume is the CLI's xhigh, never a cheap default")
	}
	if !strings.Contains(cont.Prompt, "1 acceptance criteria are still unticked") ||
		!strings.Contains(cont.Prompt, "- b") {
		t.Errorf("continuation prompt does not name the unticked criterion:\n%s", cont.Prompt)
	}
	if !strings.Contains(cont.Prompt, "PHASE BLOCKED: <reason>") {
		t.Errorf("continuation prompt does not offer the blocked ending:\n%s", cont.Prompt)
	}
	if !strings.Contains(cont.Prompt, "elapsed ") {
		t.Errorf("continuation prompt carries no time signal:\n%s", cont.Prompt)
	}

	k := runEventKinds(t, db, p1)
	if len(k) != 2 || k[0] != runcore.EventContinuation || k[1] != runcore.EventDone {
		t.Errorf("run events = %v, want [continuation done]", k)
	}
}

// TestSettle_ContinuedTwiceThenPartial: the cap is hard. Two nudges, then the
// run is stamped `partial` with an honest count — never `done`, and never a third
// billed turn.
func TestSettle_ContinuedTwiceThenPartial(t *testing.T) {
	db, _, p1, _ := fixture(t)
	doc := phaseDocPath(t, db, p1)

	r := &stubRunner{}
	r.runFn = func(spec RunSpec) (*Run, error) {
		mustWriteDoc(t, doc, "# Phase 1 — Schema\n\n- [x] a\n- [ ] b\n")
		seedTranscript(t, db, spec.SessionUUID, "Still working on the second one. Here is a status update.")
		return &Run{SessionUUID: spec.SessionUUID, ExitCode: 0}, nil
	}
	s := newTestService(db, r, &stubWt{})
	if _, err := s.Start(p1, "", ""); err != nil {
		t.Fatalf("Start: %v", err)
	}

	state, _, _, runErr := phaseRow(t, db, p1)
	if state != "partial" {
		t.Errorf("run_state = %q, want partial", state)
	}
	if !strings.Contains(runErr.String, "1 of 2 criteria ticked after 2 continuations") {
		t.Errorf("run_error = %q, want the honest count", runErr.String)
	}
	if n := r.specCount(); n != 1+runcore.MaxContinuations {
		t.Errorf("spawned %d times, want %d — the cap is a money bound", n, 1+runcore.MaxContinuations)
	}

	k := runEventKinds(t, db, p1)
	want := []string{runcore.EventContinuation, runcore.EventContinuation, runcore.EventPartial}
	if strings.Join(k, ",") != strings.Join(want, ",") {
		t.Errorf("run events = %v, want %v", k, want)
	}
}

// TestSettle_ContinuationFailureIsPartialNotDone: a continuation that exits
// non-zero must not be laundered into a clean finish.
func TestSettle_ContinuationFailureIsPartialNotDone(t *testing.T) {
	db, _, p1, _ := fixture(t)
	doc := phaseDocPath(t, db, p1)

	r := &stubRunner{}
	r.runFn = func(spec RunSpec) (*Run, error) {
		if spec.Resume {
			return &Run{SessionUUID: spec.SessionUUID, ExitCode: 2, Stderr: "boom"}, nil
		}
		mustWriteDoc(t, doc, "# Phase 1 — Schema\n\n- [ ] a\n- [ ] b\n")
		seedTranscript(t, db, spec.SessionUUID, "progress report")
		return &Run{SessionUUID: spec.SessionUUID, ExitCode: 0}, nil
	}
	s := newTestService(db, r, &stubWt{})
	if _, err := s.Start(p1, "", ""); err != nil {
		t.Fatalf("Start: %v", err)
	}

	state, _, _, runErr := phaseRow(t, db, p1)
	if state != "partial" {
		t.Errorf("run_state = %q, want partial", state)
	}
	if !strings.Contains(runErr.String, "continuation exited 2") {
		t.Errorf("run_error = %q, want the continuation's own failure", runErr.String)
	}
}

// TestSettle_DocUnreadableAtExitIsPartialNotDone drives the branch the name
// promises: admission accepts a doc that EXISTS, the executor's run removes it
// (an operator rename, a plan revision), and settle then has no tick count.
//
// An unknown tick count is not evidence of completion, so the run must settle
// `partial` naming the cause — never the green `done` the exit code used to buy.
func TestSettle_DocUnreadableAtExitIsPartialNotDone(t *testing.T) {
	db, _, p1, _ := fixture(t)
	doc := phaseDocPath(t, db, p1)

	r := &stubRunner{}
	r.runFn = func(spec RunSpec) (*Run, error) {
		seedTranscript(t, db, spec.SessionUUID, "a report with no sentinel")
		if err := os.Remove(doc); err != nil { // the doc vanishes mid-run
			t.Fatalf("remove doc: %v", err)
		}
		return &Run{SessionUUID: spec.SessionUUID, ExitCode: 0}, nil
	}
	s := newTestService(db, r, &stubWt{})
	if _, err := s.Start(p1, "", ""); err != nil {
		t.Fatalf("Start: %v", err)
	}

	state, _, _, runErr := phaseRow(t, db, p1)
	if state != "partial" {
		t.Errorf("run_state = %q, want partial — an unreadable doc proves nothing", state)
	}
	if !strings.Contains(runErr.String, "phase doc unreadable at exit") {
		t.Errorf("run_error = %q, want the cause named", runErr.String)
	}
	if n := r.specCount(); n != 1 {
		t.Errorf("spawned %d times, want 1 — there is no unticked list to nudge with", n)
	}
}

// TestSettle_BlockedWinsWhenTheDocIsUnreadable: the transcript is classified
// BEFORE the doc is read, so a `PHASE BLOCKED:` ending survives the doc going
// away. Previously the unreadable-doc branch returned early and stamped the run
// green with a NULL run_error over an explicit blocked report.
func TestSettle_BlockedWinsWhenTheDocIsUnreadable(t *testing.T) {
	db, _, p1, _ := fixture(t)
	doc := phaseDocPath(t, db, p1)

	r := &stubRunner{}
	r.runFn = func(spec RunSpec) (*Run, error) {
		seedTranscript(t, db, spec.SessionUUID,
			"I could not find the migration.\n\nPHASE BLOCKED: the doc names a table that does not exist")
		if err := os.Remove(doc); err != nil {
			t.Fatalf("remove doc: %v", err)
		}
		return &Run{SessionUUID: spec.SessionUUID, ExitCode: 0}, nil
	}
	s := newTestService(db, r, &stubWt{})
	if _, err := s.Start(p1, "", ""); err != nil {
		t.Fatalf("Start: %v", err)
	}

	state, _, _, runErr := phaseRow(t, db, p1)
	if state != "blocked" {
		t.Errorf("run_state = %q, want blocked — the sentinel is evidence without the doc", state)
	}
	if runErr.String != "the doc names a table that does not exist" {
		t.Errorf("run_error = %q, want the blocked reason", runErr.String)
	}
	if k := runEventKinds(t, db, p1); len(k) != 1 || k[0] != runcore.EventBlocked {
		t.Errorf("run events = %v, want one blocked event", k)
	}
}

// TestSettle_NoTranscriptStillDecidesFromTheTicks: ingest is a separate pipeline
// with its own lag, so a finished run whose transcript has not landed must still
// be recognised as finished.
func TestSettle_NoTranscriptStillDecidesFromTheTicks(t *testing.T) {
	db, _, p1, _ := fixture(t)
	doc := phaseDocPath(t, db, p1)

	r := &stubRunner{}
	r.runFn = func(spec RunSpec) (*Run, error) {
		mustWriteDoc(t, doc, "# Phase 1 — Schema\n\n- [x] a\n- [x] b\n")
		return &Run{SessionUUID: spec.SessionUUID, ExitCode: 0}, nil
	}
	s := newTestService(db, r, &stubWt{})
	if _, err := s.Start(p1, "", ""); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if state, _, _, _ := phaseRow(t, db, p1); state != "done" {
		t.Errorf("run_state = %q, want done — the ticks are the proof, not the transcript", state)
	}
	if n := r.specCount(); n != 1 {
		t.Errorf("spawned %d times, want 1", n)
	}
}

// TestPrompt_CarriesTheStandingInstructionAndBudgetExactlyOnce is step 3.4/3.5's
// acceptance criterion on the rendered phase-run prompt.
func TestPrompt_CarriesTheStandingInstructionAndBudgetExactlyOnce(t *testing.T) {
	db, _, p1, _ := fixture(t)
	r := &stubRunner{}
	s := newTestService(db, r, &stubWt{})
	if _, err := s.Start(p1, "", ""); err != nil {
		t.Fatalf("Start: %v", err)
	}

	got := r.firstSpec().Prompt
	if n := strings.Count(got, "HOW YOUR TURN ENDS"); n != 1 {
		t.Errorf("standing instruction appears %d times, want exactly 1:\n%s", n, got)
	}
	if n := strings.Count(got, "Budget: "); n != 1 {
		t.Errorf("budget line appears %d times, want exactly 1:\n%s", n, got)
	}
	if !strings.Contains(got, "; started 2026-07-28T12:00:0") {
		t.Errorf("budget line does not state the run's start instant:\n%s", got)
	}
	// The pre-existing contract must survive beside it.
	for _, want := range []string{"PHASE BLOCKED:", "PHASE DONE", "## Completion Report"} {
		if !strings.Contains(got, want) {
			t.Errorf("prompt lost %q:\n%s", want, got)
		}
	}
}
