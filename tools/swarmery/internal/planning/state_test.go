package planning

import (
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

// ── fixtures ──

// questionTurnText builds an assistant reply that meets the protocol: prose
// analysis followed by one fenced json question block.
func questionTurnText(id string) string {
	return "I inspected the repo — two viable directions.\n\n```json\n" +
		`{"type":"question","data":{"id":"` + id + `","type":"single_select","question":"Which direction?",` +
		`"options":[{"id":"opt-a","label":"Option A"},{"id":"other","label":"Other","isOther":true}],` +
		`"runningPlan":{"title":"T","description":"D"}}}` + "\n```\n"
}

// insertWizardRow crafts a planning_sessions row directly (bypassing Start) so
// state-machine tests control the starting status.
func insertWizardRow(t *testing.T, db *sql.DB, uuid, status string) int64 {
	t.Helper()
	res, err := db.Exec(
		`INSERT INTO planning_sessions(project_id, session_uuid, status, idea, created_at, updated_at)
		 VALUES(1, ?, ?, 'test idea', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z')`, uuid, status)
	if err != nil {
		t.Fatal(err)
	}
	id, _ := res.LastInsertId()
	return id
}

// insertSessionRow mints the ingested sessions row the planner transcript maps to.
func insertSessionRow(t *testing.T, db *sql.DB, id int64, uuid string) {
	t.Helper()
	if _, err := db.Exec(
		`INSERT INTO sessions(id, project_id, session_uuid, status, cwd, started_at, source)
		 VALUES(?, 1, ?, 'active', '/repo/p', '2026-01-01T00:00:00Z', 'jsonl')`, id, uuid); err != nil {
		t.Fatal(err)
	}
}

// insertAssistantTurn appends one assistant turn with prose text.
func insertAssistantTurn(t *testing.T, db *sql.DB, sessionID int64, seq int, text string) {
	t.Helper()
	if _, err := db.Exec(
		`INSERT INTO turns(session_id, seq, role, started_at, text)
		 VALUES(?, ?, 'assistant', '2026-01-01T00:00:01Z', ?)`, sessionID, seq, text); err != nil {
		t.Fatal(err)
	}
}

// wizardState reads back the columns the state machine mutates.
func wizardState(t *testing.T, db *sql.DB, uuid string) (status string, currentQuestion, rawReply, planDir sql.NullString) {
	t.Helper()
	err := db.QueryRow(
		`SELECT status, current_question, raw_reply, plan_dir FROM planning_sessions WHERE session_uuid=?`,
		uuid).Scan(&status, &currentQuestion, &rawReply, &planDir)
	if err != nil {
		t.Fatal(err)
	}
	return
}

func turnCount(t *testing.T, db *sql.DB, uuid string) int {
	t.Helper()
	var n int
	if err := db.QueryRow(
		`SELECT COUNT(*) FROM planning_turns pt JOIN planning_sessions ps ON ps.id = pt.planning_session_id
		 WHERE ps.session_uuid=?`, uuid).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// wizardFixture: one wizard row (given status) + ingested session + one
// assistant turn, ready for OnSessionTurns.
func wizardFixture(t *testing.T, db *sql.DB, status, turnText string) (s *Service, uuid string) {
	t.Helper()
	uuid = "uuid-wizard"
	insertWizardRow(t, db, uuid, status)
	insertSessionRow(t, db, 42, uuid)
	insertAssistantTurn(t, db, 42, 1, turnText)
	return newInlineService(t, db, &stubRunner{}), uuid
}

// ── Start persists the wizard row ──

func TestStart_InsertsWizardRow(t *testing.T) {
	db := testDB(t)
	s := newInlineService(t, db, &stubRunner{})
	if _, err := s.Start(1, "add a widget", ""); err != nil {
		t.Fatalf("Start: %v", err)
	}
	var status, idea string
	if err := db.QueryRow(
		`SELECT status, idea FROM planning_sessions WHERE session_uuid='uuid-planning'`).Scan(&status, &idea); err != nil {
		t.Fatalf("wizard row not inserted: %v", err)
	}
	if status != StatusGenerating {
		t.Errorf("status = %q, want generating", status)
	}
	if idea != "add a widget" {
		t.Errorf("idea = %q", idea)
	}
}

func TestStart_SupersedesOpenRow(t *testing.T) {
	db := testDB(t)
	insertWizardRow(t, db, "uuid-old", StatusAwaiting)
	s := newInlineService(t, db, &stubRunner{})
	if _, err := s.Start(1, "new idea", ""); err != nil {
		t.Fatalf("Start: %v", err)
	}
	st, _, _, _ := wizardState(t, db, "uuid-old")
	if st != StatusCancelled {
		t.Errorf("old open row status = %q, want cancelled (superseded)", st)
	}
}

// ── OnSessionTurns ──

func TestOnSessionTurns_QuestionTurn(t *testing.T) {
	db := testDB(t)
	s, uuid := wizardFixture(t, db, StatusGenerating, questionTurnText("q-scope"))
	var notified int
	s.Notify = func(int64) { notified++ }

	s.OnSessionTurns(uuid)

	status, cq, raw, _ := wizardState(t, db, uuid)
	if status != StatusAwaiting {
		t.Fatalf("status = %q, want awaiting_answer", status)
	}
	if !cq.Valid || !strings.Contains(cq.String, `"q-scope"`) {
		t.Errorf("current_question = %+v, want the parsed question JSON", cq)
	}
	if raw.Valid {
		t.Errorf("raw_reply = %q, want NULL on a parsed question", raw.String)
	}
	if turnCount(t, db, uuid) != 1 {
		t.Errorf("planning_turns count = %d, want 1", turnCount(t, db, uuid))
	}
	var qJSON, reasoning string
	if err := db.QueryRow(
		`SELECT question, reasoning FROM planning_turns WHERE seq=1`).Scan(&qJSON, &reasoning); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(qJSON, "Which direction?") {
		t.Errorf("turn question = %q", qJSON)
	}
	if !strings.Contains(reasoning, "two viable directions") {
		t.Errorf("turn reasoning = %q, want the pre-JSON prose", reasoning)
	}
	// running plan captured from question.runningPlan
	var rp sql.NullString
	db.QueryRow(`SELECT running_plan FROM planning_sessions WHERE session_uuid=?`, uuid).Scan(&rp)
	if !rp.Valid || !strings.Contains(rp.String, `"T"`) {
		t.Errorf("running_plan = %+v, want the summary JSON", rp)
	}
	if notified == 0 {
		t.Error("Notify not fired on the generating→awaiting_answer transition")
	}
}

func TestOnSessionTurns_IdempotentReingest(t *testing.T) {
	db := testDB(t)
	s, uuid := wizardFixture(t, db, StatusGenerating, questionTurnText("q-scope"))
	s.OnSessionTurns(uuid)

	var notified int
	s.Notify = func(int64) { notified++ }
	s.OnSessionTurns(uuid) // re-ingest of the same turn

	if n := turnCount(t, db, uuid); n != 1 {
		t.Errorf("planning_turns count after re-ingest = %d, want 1 (no dup)", n)
	}
	// The no-change pass must NOT notify — Notify feeds the WS bus whose
	// consumer calls OnSessionTurns again; a notify here would loop forever.
	if notified != 0 {
		t.Errorf("Notify fired %d times on a no-change re-ingest, want 0", notified)
	}
}

func TestOnSessionTurns_ProseFallback(t *testing.T) {
	db := testDB(t)
	s, uuid := wizardFixture(t, db, StatusGenerating, "Just some prose, no JSON block at all.")
	s.OnSessionTurns(uuid)

	status, cq, raw, _ := wizardState(t, db, uuid)
	if status != StatusAwaiting {
		t.Fatalf("status = %q, want awaiting_answer (raw fallback)", status)
	}
	if cq.Valid {
		t.Errorf("current_question = %q, want NULL", cq.String)
	}
	if !raw.Valid || !strings.Contains(raw.String, "Just some prose") {
		t.Errorf("raw_reply = %+v, want the turn text", raw)
	}
	if turnCount(t, db, uuid) != 0 {
		t.Error("raw fallback must not insert a planning_turns row")
	}
}

func TestOnSessionTurns_RawGatedWhileProcessAlive(t *testing.T) {
	db := testDB(t)
	s, uuid := wizardFixture(t, db, StatusGenerating, "intermediate research narration")
	s.ResumeInFlight = func(u string) bool { return u == uuid }

	s.OnSessionTurns(uuid)

	status, _, raw, _ := wizardState(t, db, uuid)
	if status != StatusGenerating || raw.Valid {
		t.Errorf("status=%q raw=%v — a live process's intermediate prose must not flip to raw fallback", status, raw)
	}
}

func TestOnSessionTurns_NonPlannerSessionNoop(t *testing.T) {
	db := testDB(t)
	s := newInlineService(t, db, &stubRunner{})
	s.Notify = func(int64) { t.Error("Notify fired for a non-planner session") }
	s.OnSessionTurns("uuid-not-a-planner") // must be a cheap no-op
}

// ── Answer / Refine / Proceed ──

// answeredFixture drives a wizard to awaiting_answer on question q-scope.
func answeredFixture(t *testing.T, db *sql.DB) (*Service, string) {
	t.Helper()
	s, uuid := wizardFixture(t, db, StatusGenerating, questionTurnText("q-scope"))
	s.OnSessionTurns(uuid)
	return s, uuid
}

func TestAnswer_HappyPath(t *testing.T) {
	db := testDB(t)
	s, uuid := answeredFixture(t, db)

	text, gotUUID, err := s.Answer(1, "q-scope", []string{"opt-a"}, "")
	if err != nil {
		t.Fatalf("Answer: %v", err)
	}
	if gotUUID != uuid {
		t.Errorf("uuid = %q, want %q", gotUUID, uuid)
	}
	if !strings.Contains(text, "opt-a") {
		t.Errorf("resume text = %q, want the selected option id", text)
	}
	status, _, _, _ := wizardState(t, db, uuid)
	if status != StatusGenerating {
		t.Errorf("status = %q, want generating after Answer", status)
	}
	var ans sql.NullString
	db.QueryRow(`SELECT answer FROM planning_turns WHERE seq=1`).Scan(&ans)
	if !ans.Valid || !strings.Contains(ans.String, `"answer"`) || !strings.Contains(ans.String, "opt-a") {
		t.Errorf("stamped answer = %+v, want {kind:answer, selectedOptionIds:[opt-a]}", ans)
	}

	// Re-ingest of the SAME (already answered) question turn — e.g. the resume
	// spawn republishing session_updated — must NOT regress generating→awaiting.
	s.OnSessionTurns(uuid)
	status, _, _, _ = wizardState(t, db, uuid)
	if status != StatusGenerating {
		t.Errorf("status after re-ingest of the answered turn = %q, want generating", status)
	}
}

func TestAnswer_WrongQuestion(t *testing.T) {
	db := testDB(t)
	s, _ := answeredFixture(t, db)
	if _, _, err := s.Answer(1, "q-other", []string{"opt-a"}, ""); !errors.Is(err, ErrWrongQuestion) {
		t.Fatalf("err = %v, want ErrWrongQuestion", err)
	}
}

func TestAnswer_NotAwaiting(t *testing.T) {
	db := testDB(t)
	insertWizardRow(t, db, "uuid-gen", StatusGenerating)
	s := newInlineService(t, db, &stubRunner{})
	if _, _, err := s.Answer(1, "q-scope", []string{"opt-a"}, ""); !errors.Is(err, ErrNotAwaiting) {
		t.Fatalf("err = %v, want ErrNotAwaiting", err)
	}
}

func TestAnswer_NoSession(t *testing.T) {
	db := testDB(t)
	s := newInlineService(t, db, &stubRunner{})
	if _, _, err := s.Answer(1, "q", nil, ""); !errors.Is(err, ErrNoSession) {
		t.Fatalf("err = %v, want ErrNoSession", err)
	}
}

func TestAnswer_RawFallbackFreeText(t *testing.T) {
	db := testDB(t)
	s, uuid := wizardFixture(t, db, StatusGenerating, "prose only, protocol violated")
	s.OnSessionTurns(uuid) // → awaiting_answer with raw_reply, no current_question

	text, _, err := s.Answer(1, "", nil, "just do the simple version")
	if err != nil {
		t.Fatalf("Answer (raw fallback): %v", err)
	}
	if text != "just do the simple version" {
		t.Errorf("resume text = %q, want the raw otherText verbatim", text)
	}
	// A questionId against a raw-fallback state is a contract violation.
	s2, uuid2 := wizardFixture2(t, db)
	_ = uuid2
	if _, _, err := s2.Answer(1, "q-x", nil, "free text"); !errors.Is(err, ErrWrongQuestion) {
		t.Fatalf("err = %v, want ErrWrongQuestion for questionId in raw mode", err)
	}
}

// wizardFixture2 builds a second raw-fallback wizard on project 1 (fresh uuid).
func wizardFixture2(t *testing.T, db *sql.DB) (*Service, string) {
	t.Helper()
	uuid := "uuid-wizard-2"
	insertWizardRow(t, db, uuid, StatusAwaiting)
	return newInlineService(t, db, &stubRunner{}), uuid
}

func TestRefine_HappyPath(t *testing.T) {
	db := testDB(t)
	s, uuid := answeredFixture(t, db)

	text, gotUUID, err := s.Refine(1, "make it smaller")
	if err != nil {
		t.Fatalf("Refine: %v", err)
	}
	if gotUUID != uuid {
		t.Errorf("uuid = %q", gotUUID)
	}
	if !strings.Contains(text, "make it smaller") {
		t.Errorf("resume text = %q, want the instructions", text)
	}
	status, _, _, _ := wizardState(t, db, uuid)
	if status != StatusGenerating {
		t.Errorf("status = %q, want generating after Refine", status)
	}
	var ans sql.NullString
	db.QueryRow(`SELECT answer FROM planning_turns WHERE seq=1`).Scan(&ans)
	if !ans.Valid || !strings.Contains(ans.String, `"refine"`) {
		t.Errorf("stamped answer = %+v, want {kind:refine}", ans)
	}
}

func TestProceed_ThenPlanSaved(t *testing.T) {
	db := testDB(t)
	s, uuid := answeredFixture(t, db)

	text, _, err := s.Proceed(1)
	if err != nil {
		t.Fatalf("Proceed: %v", err)
	}
	if !strings.Contains(text, "PROCEED") {
		t.Errorf("resume text = %q, want the PROCEED instruction", text)
	}
	status, _, _, _ := wizardState(t, db, uuid)
	if status != StatusProceeding {
		t.Fatalf("status = %q, want proceeding", status)
	}

	// A re-ingest of the OLD question turn while proceeding must not regress.
	s.OnSessionTurns(uuid)
	status, _, _, _ = wizardState(t, db, uuid)
	if status != StatusProceeding {
		t.Fatalf("status after old-turn re-ingest = %q, want proceeding", status)
	}

	// The PHASE B reply lands → done + plan_dir.
	savedDir := "/ws/acme/workspace/working/2026/08/09/dark-mode/plan"
	insertAssistantTurn(t, db, 42, 2, "Plan written.\nPLAN SAVED: "+savedDir+"\n")
	s.OnSessionTurns(uuid)
	status, _, _, planDir := wizardState(t, db, uuid)
	if status != StatusDone {
		t.Fatalf("status = %q, want done", status)
	}
	if planDir.String != savedDir {
		t.Errorf("plan_dir = %q, want %q", planDir.String, savedDir)
	}

	// Proceed again on a terminal wizard → ErrNotAwaiting.
	if _, _, err := s.Proceed(1); !errors.Is(err, ErrNotAwaiting) {
		t.Fatalf("Proceed on done wizard err = %v, want ErrNotAwaiting", err)
	}
}

// ── run outcome / cancel / reconcile ──

func TestRunFailure_MarksFailed(t *testing.T) {
	db := testDB(t)
	r := &stubRunner{runFn: func(s RunSpec) (*Run, error) {
		return &Run{SessionUUID: s.SessionUUID, ExitCode: 1, Stderr: "boom"}, nil
	}}
	s := newInlineService(t, db, r)
	if _, err := s.Start(1, "idea", ""); err != nil {
		t.Fatalf("Start: %v", err)
	}
	status, _, _, _ := wizardState(t, db, "uuid-planning")
	if status != StatusFailed {
		t.Errorf("status = %q, want failed after nonzero exit", status)
	}
}

func TestCancel_StampsCancelled(t *testing.T) {
	db := testDB(t)
	insertWizardRow(t, db, "uuid-cancel", StatusAwaiting)
	s := newInlineService(t, db, &stubRunner{})

	if !s.Cancel(1) {
		t.Fatal("Cancel returned false with an open wizard row")
	}
	status, _, _, _ := wizardState(t, db, "uuid-cancel")
	if status != StatusCancelled {
		t.Errorf("status = %q, want cancelled", status)
	}
	// Nothing left to cancel.
	if s.Cancel(1) {
		t.Error("second Cancel returned true")
	}
}

func TestWizardSnapshot_StaleGeneratingReconcile(t *testing.T) {
	db := testDB(t)
	insertWizardRow(t, db, "uuid-stale", StatusGenerating)
	// updated_at 2026-01-01 is far older than the 16-minute window vs real now.
	s := newInlineService(t, db, &stubRunner{})

	st, err := s.WizardSnapshot(1)
	if err != nil {
		t.Fatalf("WizardSnapshot: %v", err)
	}
	if st.Status != StatusAwaiting {
		t.Errorf("snapshot status = %q, want awaiting_answer (stale reconcile)", st.Status)
	}
	status, _, _, _ := wizardState(t, db, "uuid-stale")
	if status != StatusAwaiting {
		t.Errorf("persisted status = %q, want awaiting_answer", status)
	}
}

func TestWizardSnapshot_NoReconcileWhileResumeInFlight(t *testing.T) {
	db := testDB(t)
	insertWizardRow(t, db, "uuid-busy", StatusGenerating)
	s := newInlineService(t, db, &stubRunner{})
	s.ResumeInFlight = func(string) bool { return true }

	st, err := s.WizardSnapshot(1)
	if err != nil {
		t.Fatalf("WizardSnapshot: %v", err)
	}
	if st.Status != StatusGenerating {
		t.Errorf("status = %q, want generating (resume still in flight)", st.Status)
	}
	if !st.Active {
		t.Error("active = false, want true while a resume is in flight")
	}
}

func TestWizardSnapshot_DTO(t *testing.T) {
	db := testDB(t)
	s, uuid := answeredFixture(t, db)
	if _, _, err := s.Answer(1, "q-scope", []string{"opt-a"}, "and a note"); err != nil {
		t.Fatal(err)
	}

	st, err := s.WizardSnapshot(1)
	if err != nil {
		t.Fatalf("WizardSnapshot: %v", err)
	}
	if st.SessionUUID != uuid {
		t.Errorf("sessionUuid = %q, want %q", st.SessionUUID, uuid)
	}
	if st.SessionID == nil || *st.SessionID != 42 {
		t.Errorf("sessionId = %v, want 42", st.SessionID)
	}
	if st.Status != StatusGenerating {
		t.Errorf("status = %q, want generating", st.Status)
	}
	if st.CurrentQuestion == nil || st.CurrentQuestion.ID != "q-scope" {
		t.Errorf("currentQuestion = %+v", st.CurrentQuestion)
	}
	if st.RunningPlan == nil || st.RunningPlan.Title != "T" {
		t.Errorf("runningPlan = %+v", st.RunningPlan)
	}
	if len(st.History) != 1 {
		t.Fatalf("history len = %d, want 1", len(st.History))
	}
	h := st.History[0]
	if h.Seq != 1 || h.Question == nil || h.Question.ID != "q-scope" {
		t.Errorf("history[0] = %+v", h)
	}
	var stamped map[string]any
	if err := json.Unmarshal(h.Answer, &stamped); err != nil || stamped["kind"] != "answer" {
		t.Errorf("history[0].answer = %s", h.Answer)
	}
	if !strings.Contains(h.Reasoning, "two viable directions") {
		t.Errorf("history[0].reasoning = %q", h.Reasoning)
	}
}

func TestWizardSnapshot_NoRowLegacyIdle(t *testing.T) {
	db := testDB(t)
	s := newInlineService(t, db, &stubRunner{})
	st, err := s.WizardSnapshot(1)
	if err != nil {
		t.Fatalf("WizardSnapshot: %v", err)
	}
	if st.Active || st.Status != "" || st.CurrentQuestion != nil || st.PlanDir != nil {
		t.Errorf("legacy idle snapshot = %+v, want inactive/empty", st)
	}
	if st.History == nil || len(st.History) != 0 {
		t.Errorf("history = %#v, want empty non-nil slice", st.History)
	}
}

func TestRevertToAwaiting(t *testing.T) {
	db := testDB(t)
	insertWizardRow(t, db, "uuid-revert", StatusGenerating)
	s := newInlineService(t, db, &stubRunner{})
	s.RevertToAwaiting("uuid-revert", "resume died")
	status, _, _, _ := wizardState(t, db, "uuid-revert")
	if status != StatusAwaiting {
		t.Errorf("status = %q, want awaiting_answer after revert", status)
	}
	// Terminal states are never revived.
	db.Exec(`UPDATE planning_sessions SET status='done' WHERE session_uuid='uuid-revert'`)
	s.RevertToAwaiting("uuid-revert", "resume died")
	status, _, _, _ = wizardState(t, db, "uuid-revert")
	if status != StatusDone {
		t.Errorf("status = %q, want done (revert must not touch terminal states)", status)
	}
}

// ── last_error: why the wizard is answerable again ──

// THE regression this column exists for: a failed resume rolls the wizard back
// to awaiting_answer with the SAME question, which is byte-identical to the
// planner asking again. Without a stamped reason the operator re-answers forever
// (observed: a resume that could not find the transcript, 56 min of looping).
func TestRevertToAwaiting_StampsReasonAndSnapshotSurfacesIt(t *testing.T) {
	db := testDB(t)
	s, uuid := answeredFixture(t, db)
	if _, _, err := s.Answer(1, "q-scope", []string{"opt-a"}, ""); err != nil {
		t.Fatalf("Answer: %v", err)
	}

	s.RevertToAwaiting(uuid, "the planner run failed (exit status 1)")

	st, err := s.WizardSnapshot(1)
	if err != nil {
		t.Fatalf("WizardSnapshot: %v", err)
	}
	if st.Status != StatusAwaiting {
		t.Fatalf("status = %q, want awaiting_answer", st.Status)
	}
	if st.LastError == nil || !strings.Contains(*st.LastError, "exit status 1") {
		t.Fatalf("lastError = %v, want the rollback reason", st.LastError)
	}
	// The question is unchanged — which is exactly why the reason must be there.
	if st.CurrentQuestion == nil || st.CurrentQuestion.ID != "q-scope" {
		t.Errorf("currentQuestion = %+v, want the same q-scope question", st.CurrentQuestion)
	}
}

// A rollback with no reason still stamps something: an unexplained revert is the
// silent state this column was added to make impossible.
func TestRevertToAwaiting_BlankReasonStillExplains(t *testing.T) {
	db := testDB(t)
	insertWizardRow(t, db, "uuid-blank-reason", StatusGenerating)
	s := newInlineService(t, db, &stubRunner{})

	s.RevertToAwaiting("uuid-blank-reason", "   ")

	st, err := s.WizardSnapshot(1)
	if err != nil {
		t.Fatalf("WizardSnapshot: %v", err)
	}
	if st.LastError == nil || *st.LastError == "" {
		t.Fatalf("lastError = %v, want a non-empty fallback reason", st.LastError)
	}
}

// The banner describes the LAST attempt, so the next action must clear it — the
// operator's retry, not its outcome, is what makes the old failure history.
func TestLastErrorClearedByNextAction(t *testing.T) {
	db := testDB(t)
	s, uuid := answeredFixture(t, db)
	if _, _, err := s.Answer(1, "q-scope", []string{"opt-a"}, ""); err != nil {
		t.Fatalf("Answer: %v", err)
	}
	s.RevertToAwaiting(uuid, "the planner run failed (exit status 1)")

	if _, _, err := s.Answer(1, "q-scope", []string{"opt-a"}, ""); err != nil {
		t.Fatalf("retry Answer: %v", err)
	}
	st, err := s.WizardSnapshot(1)
	if err != nil {
		t.Fatalf("WizardSnapshot: %v", err)
	}
	if st.LastError != nil {
		t.Errorf("lastError = %q, want null once the retry started", *st.LastError)
	}
}

// A fresh question landing while a stale failure is stamped clears it too: the
// planner answered, so the banner is history no matter which path got us here.
func TestLastErrorClearedByNewQuestion(t *testing.T) {
	db := testDB(t)
	s, uuid := answeredFixture(t, db)
	if _, _, err := s.Answer(1, "q-scope", []string{"opt-a"}, ""); err != nil {
		t.Fatalf("Answer: %v", err)
	}
	s.RevertToAwaiting(uuid, "the planner run failed (exit status 1)")

	s.applyQuestionTurn(mustWizardRow(t, s, uuid), ParsedTurn{
		Question: &PlanningQuestion{ID: "q-next", Type: "single_select", Question: "next?"},
	})

	st, err := s.WizardSnapshot(1)
	if err != nil {
		t.Fatalf("WizardSnapshot: %v", err)
	}
	if st.LastError != nil {
		t.Errorf("lastError = %q, want null once a new question landed", *st.LastError)
	}
	if st.CurrentQuestion == nil || st.CurrentQuestion.ID != "q-next" {
		t.Errorf("currentQuestion = %+v, want q-next", st.CurrentQuestion)
	}
}

// mustWizardRow loads the row the apply* helpers take.
func mustWizardRow(t *testing.T, s *Service, uuid string) *wizardRow {
	t.Helper()
	row, err := s.wizardByUUID(uuid)
	if err != nil || row == nil {
		t.Fatalf("wizardByUUID(%q) = %v, %v", uuid, row, err)
	}
	return row
}

// A stale resume's failure (cancel + start a new idea while a 15-min resume is
// in flight) must roll back ITS OWN row only — never the project's newer wizard.
func TestRevertToAwaiting_StaleUUIDDoesNotTouchNewerWizard(t *testing.T) {
	db := testDB(t)
	insertWizardRow(t, db, "uuid-stale-resume", StatusCancelled) // superseded by Start
	insertWizardRow(t, db, "uuid-newer", StatusGenerating)       // the new wizard
	s := newInlineService(t, db, &stubRunner{})

	s.RevertToAwaiting("uuid-stale-resume", "resume died")

	if st, _, _, _ := wizardState(t, db, "uuid-stale-resume"); st != StatusCancelled {
		t.Errorf("stale row status = %q, want cancelled (terminal, never revived)", st)
	}
	if st, _, _, _ := wizardState(t, db, "uuid-newer"); st != StatusGenerating {
		t.Errorf("newer row status = %q, want generating (stale revert must not touch it)", st)
	}
}

// ── CAS status writes (TOCTOU hardening) ──

// Cancel landing between admitAwaiting's read and setStatus's write must win:
// the stale flip gets ErrNotAwaiting and cancelled is never overwritten.
func TestSetStatus_CancelBetweenAdmitAndFlip(t *testing.T) {
	db := testDB(t)
	insertWizardRow(t, db, "uuid-cas-cancel", StatusAwaiting)
	s := newInlineService(t, db, &stubRunner{})
	row, err := s.admitAwaiting(1)
	if err != nil {
		t.Fatalf("admitAwaiting: %v", err)
	}
	if !s.Cancel(1) {
		t.Fatal("Cancel returned false with an open wizard row")
	}
	if err := s.setStatus(row, StatusGenerating); !errors.Is(err, ErrNotAwaiting) {
		t.Fatalf("setStatus after concurrent Cancel err = %v, want ErrNotAwaiting", err)
	}
	if st, _, _, _ := wizardState(t, db, "uuid-cas-cancel"); st != StatusCancelled {
		t.Errorf("status = %q, want cancelled (stale flip must not overwrite Cancel)", st)
	}
}

// Two concurrent Answers both pass admission; the loser's flip must fail
// cleanly (409 sentinel) and leave the winner's 'generating' in place.
func TestSetStatus_DoubleAnswerLoser(t *testing.T) {
	db := testDB(t)
	s, uuid := answeredFixture(t, db)
	rowA, err := s.admitAwaiting(1)
	if err != nil {
		t.Fatalf("admit A: %v", err)
	}
	rowB, err := s.admitAwaiting(1)
	if err != nil {
		t.Fatalf("admit B: %v", err)
	}
	if err := s.setStatus(rowA, StatusGenerating); err != nil {
		t.Fatalf("winner setStatus: %v", err)
	}
	if err := s.setStatus(rowB, StatusGenerating); !errors.Is(err, ErrNotAwaiting) {
		t.Fatalf("loser setStatus err = %v, want ErrNotAwaiting", err)
	}
	if st, _, _, _ := wizardState(t, db, uuid); st != StatusGenerating {
		t.Errorf("status = %q, want generating (loser must not disturb the winner)", st)
	}
}

// A stale in-memory row (loaded before a concurrent Cancel) must not let
// applyQuestionTurn resurrect a cancelled wizard to awaiting_answer.
func TestApplyQuestionTurn_StaleRowNeverResurrectsCancelled(t *testing.T) {
	db := testDB(t)
	s, uuid := wizardFixture(t, db, StatusGenerating, questionTurnText("q-scope"))
	row, err := s.wizardByUUID(uuid)
	if err != nil || row == nil {
		t.Fatalf("wizardByUUID: row=%v err=%v", row, err)
	}
	// Cancel lands after the read, before the write.
	db.Exec(`UPDATE planning_sessions SET status=? WHERE session_uuid=?`, StatusCancelled, uuid)
	s.Notify = func(int64) { t.Error("Notify fired for a write that must have lost the CAS") }

	s.applyQuestionTurn(row, ParseTurn(questionTurnText("q-scope")))

	if st, _, _, _ := wizardState(t, db, uuid); st != StatusCancelled {
		t.Errorf("status = %q, want cancelled (stale question turn must not resurrect)", st)
	}
}

// Same TOCTOU for the raw fallback: a stale row must not flip cancelled back
// to awaiting_answer.
func TestApplyRawTurn_StaleRowNeverResurrectsCancelled(t *testing.T) {
	db := testDB(t)
	s, uuid := wizardFixture(t, db, StatusGenerating, "prose only")
	row, err := s.wizardByUUID(uuid)
	if err != nil || row == nil {
		t.Fatalf("wizardByUUID: row=%v err=%v", row, err)
	}
	db.Exec(`UPDATE planning_sessions SET status=? WHERE session_uuid=?`, StatusCancelled, uuid)
	s.Notify = func(int64) { t.Error("Notify fired for a write that must have lost the CAS") }

	s.applyRawTurn(row, "prose only")

	if st, _, _, _ := wizardState(t, db, uuid); st != StatusCancelled {
		t.Errorf("status = %q, want cancelled (stale raw turn must not resurrect)", st)
	}
}

// ── proceeding un-wedge (item 3) ──

// A PROCEED resume that exits with prose lacking the PLAN SAVED sentinel must
// not wedge the wizard: with no process alive the raw fallback applies, so the
// operator sees the reply and can Proceed again.
func TestOnSessionTurns_ProceedingDeadProcessRawFallback(t *testing.T) {
	db := testDB(t)
	s, uuid := wizardFixture(t, db, StatusProceeding, "Here is the plan narrative, sentinel forgotten.")

	s.OnSessionTurns(uuid)

	status, cq, raw, _ := wizardState(t, db, uuid)
	if status != StatusAwaiting {
		t.Fatalf("status = %q, want awaiting_answer (proceeding + prose + dead process)", status)
	}
	if cq.Valid {
		t.Errorf("current_question = %q, want NULL", cq.String)
	}
	if !raw.Valid || !strings.Contains(raw.String, "sentinel forgotten") {
		t.Errorf("raw_reply = %+v, want the prose turn", raw)
	}
}

// While the PROCEED resume is still alive its intermediate prose must NOT
// flip proceeding back — the fall-through is for dead processes only.
func TestOnSessionTurns_ProceedingGatedWhileProcessAlive(t *testing.T) {
	db := testDB(t)
	s, uuid := wizardFixture(t, db, StatusProceeding, "writing the plan…")
	s.ResumeInFlight = func(u string) bool { return u == uuid }

	s.OnSessionTurns(uuid)

	status, _, raw, _ := wizardState(t, db, uuid)
	if status != StatusProceeding || raw.Valid {
		t.Errorf("status=%q raw=%v — live proceeding prose must stay proceeding", status, raw)
	}
}

// Belt for the no-new-turn case: a 'proceeding' row past the resume window
// with no live process reconciles to awaiting_answer on read, like generating.
func TestWizardSnapshot_StaleProceedingReconcile(t *testing.T) {
	db := testDB(t)
	insertWizardRow(t, db, "uuid-stale-proceed", StatusProceeding)
	// updated_at 2026-01-01 is far older than the 16-minute window vs real now.
	s := newInlineService(t, db, &stubRunner{})

	st, err := s.WizardSnapshot(1)
	if err != nil {
		t.Fatalf("WizardSnapshot: %v", err)
	}
	if st.Status != StatusAwaiting {
		t.Errorf("snapshot status = %q, want awaiting_answer (stale proceeding reconcile)", st.Status)
	}
	status, _, _, _ := wizardState(t, db, "uuid-stale-proceed")
	if status != StatusAwaiting {
		t.Errorf("persisted status = %q, want awaiting_answer", status)
	}
}

// ── PLAN SAVED path-shape gate (issue #188) ──

func TestValidPlanDir(t *testing.T) {
	for _, dir := range []string{
		"/ws/acme/workspace/working/2026/08/09/dark-mode/plan",
		"/ws/acme/workspace/working/2026/08/09/dark-mode/plan/",
		"/ws/acme/workspace/working/2026/08/09/dark-mode",
		"/ws/acme/workspace/archive/2026/01/31/old-task/plan",
	} {
		if !validPlanDir(dir) {
			t.Errorf("validPlanDir(%q) = false, want true", dir)
		}
	}
	for _, dir := range []string{
		"",
		"/ws/p/plan",
		"/ws/acme/workspace/plans/2026-08-06-milestone/plan",   // frozen flat tree — the #188 repro
		"/ws/acme/workspace/working/2026-08-09/dark-mode/plan", // date not split into dirs
		"/ws/acme/workspace/working/2026/08/dark-mode/plan",    // day segment missing
	} {
		if validPlanDir(dir) {
			t.Errorf("validPlanDir(%q) = true, want false", dir)
		}
	}
}

// A PHASE B reply whose PLAN SAVED path is a shape the workspace scanner never
// walks (e.g. the frozen workspace/plans/ tree) must NOT mark the wizard done —
// the plan would silently never reach the Plans page. With no process alive the
// raw fallback surfaces the reply, so the operator can resume with a correction.
func TestOnSessionTurns_PlanSavedOffShapePathNotDone(t *testing.T) {
	db := testDB(t)
	s, uuid := wizardFixture(t, db, StatusProceeding,
		"Plan written.\nPLAN SAVED: /ws/acme/workspace/plans/2026-08-06-milestone/plan\n")

	s.OnSessionTurns(uuid)

	status, cq, raw, planDir := wizardState(t, db, uuid)
	if status != StatusAwaiting {
		t.Fatalf("status = %q, want awaiting_answer (off-shape PLAN SAVED must not mark done)", status)
	}
	if cq.Valid {
		t.Errorf("current_question = %q, want NULL", cq.String)
	}
	if planDir.Valid {
		t.Errorf("plan_dir = %q, want NULL", planDir.String)
	}
	if !raw.Valid || !strings.Contains(raw.String, "PLAN SAVED:") {
		t.Errorf("raw_reply = %+v, want the reply surfaced to the operator", raw)
	}
}

// While the resume is still alive an off-shape reply must not flip the wizard —
// same gating as any raw turn.
func TestOnSessionTurns_PlanSavedOffShapeGatedWhileAlive(t *testing.T) {
	db := testDB(t)
	s, uuid := wizardFixture(t, db, StatusProceeding,
		"PLAN SAVED: /ws/acme/workspace/plans/milestone/plan\n")
	s.ResumeInFlight = func(u string) bool { return u == uuid }

	s.OnSessionTurns(uuid)

	status, _, raw, planDir := wizardState(t, db, uuid)
	if status != StatusProceeding || raw.Valid || planDir.Valid {
		t.Errorf("status=%q raw=%v planDir=%v — live off-shape reply must stay proceeding", status, raw, planDir)
	}
}

// ── raw-fallback answer validation (item 4) ──

func TestAnswer_RawFallbackEmptyTextRejected(t *testing.T) {
	db := testDB(t)
	s, uuid := wizardFixture(t, db, StatusGenerating, "prose only, protocol violated")
	s.OnSessionTurns(uuid) // → awaiting_answer raw fallback (no current_question)

	if _, _, err := s.Answer(1, "", []string{"opt-a"}, "   "); !errors.Is(err, ErrEmptyAnswer) {
		t.Fatalf("err = %v, want ErrEmptyAnswer for blank otherText in raw mode", err)
	}
	if st, _, _, _ := wizardState(t, db, uuid); st != StatusAwaiting {
		t.Errorf("status = %q, want awaiting_answer (rejected answer must not flip)", st)
	}
}
