package api

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"io"
	"net/http"
	"sort"
	"testing"
	"time"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/ingest"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/planning"
)

// ── fixtures ──

const wizQuestionJSON = `{"id":"q-scope","type":"single_select","question":"Which direction?",` +
	`"options":[{"id":"opt-a","label":"Option A"},{"id":"other","label":"Other","isOther":true}],` +
	`"runningPlan":{"title":"T","description":"D"}}`

// wizQuestionTurnText is a protocol-conforming assistant reply for ingest-side tests.
const wizQuestionTurnText = "Repo inspected — two directions.\n\n```json\n" +
	`{"type":"question","data":` + wizQuestionJSON + `}` + "\n```\n"

// seedWizard inserts a planning_sessions row (project 1) in the given status.
// withQuestion also sets current_question + running_plan and one history turn.
func seedWizard(t *testing.T, db *sql.DB, uuid, status string, withQuestion bool) {
	t.Helper()
	cq, rp := sql.NullString{}, sql.NullString{}
	if withQuestion {
		cq = sql.NullString{String: wizQuestionJSON, Valid: true}
		rp = sql.NullString{String: `{"title":"T","description":"D"}`, Valid: true}
	}
	res, err := db.Exec(
		`INSERT INTO planning_sessions(project_id, session_uuid, status, idea, current_question, running_plan, created_at, updated_at)
		 VALUES(1, ?, ?, 'wizard idea', ?, ?, '2026-01-01T00:00:00Z', ?)`,
		uuid, status, cq, rp, time.Now().UTC().Format(time.RFC3339))
	if err != nil {
		t.Fatal(err)
	}
	if withQuestion {
		id, _ := res.LastInsertId()
		if _, err := db.Exec(
			`INSERT INTO planning_turns(planning_session_id, seq, question, reasoning, created_at)
			 VALUES(?, 1, ?, 'because reasons', '2026-01-01T00:00:00Z')`, id, wizQuestionJSON); err != nil {
			t.Fatal(err)
		}
	}
}

// seedPlannerSession mints the ingested sessions row the wizard resumes into.
func seedPlannerSession(t *testing.T, db *sql.DB, id int64, uuid string) {
	t.Helper()
	if _, err := db.Exec(
		`INSERT INTO sessions(id, project_id, session_uuid, status, cwd, started_at, source)
		 VALUES(?, 1, ?, 'completed', '/tmp', '2026-01-01T00:00:00Z', 'jsonl')`, id, uuid); err != nil {
		t.Fatal(err)
	}
}

func wizardStatusInDB(t *testing.T, db *sql.DB, uuid string) string {
	t.Helper()
	var s string
	if err := db.QueryRow(`SELECT status FROM planning_sessions WHERE session_uuid=?`, uuid).Scan(&s); err != nil {
		t.Fatal(err)
	}
	return s
}

// ── GET DTO shape ──

func TestWizardGET_DTOFieldNames(t *testing.T) {
	srv, db, _ := serverWithPlanning(t, &planStubRunner{})
	seedWizard(t, db, "uuid-dto", planning.StatusAwaiting, true)
	seedPlannerSession(t, db, 77, "uuid-dto")

	resp, err := http.Get(srv.URL + "/api/projects/1/planning")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	var got map[string]json.RawMessage
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}

	// The FROZEN field-name contract — phase 3 mirrors these in TypeScript
	// (mode/reviseTaskId: plan-revision phase 4 does the same; lastError carries
	// the reason a rolled-back action gives the operator).
	want := []string{"active", "currentQuestion", "history", "lastError", "mode", "model",
		"planDir", "rawReply", "reviseTaskId", "runningPlan", "sessionId",
		"sessionUuid", "startedAt", "status"}
	var keys []string
	for k := range got {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	if len(keys) != len(want) {
		t.Fatalf("DTO keys = %v, want %v", keys, want)
	}
	for i := range want {
		if keys[i] != want[i] {
			t.Fatalf("DTO keys = %v, want %v", keys, want)
		}
	}

	if string(got["status"]) != `"awaiting_answer"` {
		t.Errorf("status = %s", got["status"])
	}
	if string(got["sessionUuid"]) != `"uuid-dto"` {
		t.Errorf("sessionUuid = %s", got["sessionUuid"])
	}
	if string(got["sessionId"]) != "77" {
		t.Errorf("sessionId = %s, want 77", got["sessionId"])
	}
	var history []map[string]json.RawMessage
	if err := json.Unmarshal(got["history"], &history); err != nil || len(history) != 1 {
		t.Fatalf("history = %s", got["history"])
	}
	for _, k := range []string{"seq", "question", "answer", "reasoning"} {
		if _, ok := history[0][k]; !ok {
			t.Errorf("history[0] missing key %q: %v", k, history[0])
		}
	}
}

func TestWizardGET_LegacyIdleShape(t *testing.T) {
	srv, _, _ := serverWithPlanning(t, &planStubRunner{})
	resp, err := http.Get(srv.URL + "/api/projects/1/planning")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var st planning.WizardStatus
	if err := json.NewDecoder(resp.Body).Decode(&st); err != nil {
		t.Fatal(err)
	}
	if st.Active || st.Status != "" || st.CurrentQuestion != nil || st.PlanDir != nil {
		t.Errorf("idle DTO = %+v, want inactive/empty", st)
	}
	if st.History == nil {
		t.Error("history must be [] (non-null) in the idle shape")
	}
}

// ── POST error matrix ──

func TestWizardAnswer_404NoSession(t *testing.T) {
	srv, _, _ := serverWithPlanning(t, &planStubRunner{})
	resp := postPlanningJSON(t, srv.URL+"/api/projects/1/planning/answer",
		map[string]any{"questionId": "q-scope", "selectedOptionIds": []string{"opt-a"}})
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("answer with no wizard = %d, want 404", resp.StatusCode)
	}
}

func TestWizardAnswer_400BadBody(t *testing.T) {
	srv, db, _ := serverWithPlanning(t, &planStubRunner{})
	seedWizard(t, db, "uuid-badbody", planning.StatusAwaiting, true)

	resp, err := http.Post(srv.URL+"/api/projects/1/planning/answer", "application/json",
		bytes.NewReader([]byte(`{not json`)))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("malformed body = %d, want 400", resp.StatusCode)
	}

	// No selection and no free text — nothing to answer with.
	resp2 := postPlanningJSON(t, srv.URL+"/api/projects/1/planning/answer",
		map[string]any{"questionId": "q-scope", "selectedOptionIds": []string{}, "otherText": "  "})
	defer resp2.Body.Close()
	if resp2.StatusCode != http.StatusBadRequest {
		t.Errorf("empty answer = %d, want 400", resp2.StatusCode)
	}
}

// Raw-fallback mode (current_question NULL) has no options to select — an
// answer with only selectedOptionIds and blank otherText is a client-shape
// error: 400, no status flip, no spawn.
func TestWizardAnswer_400RawFallbackEmptyOtherText(t *testing.T) {
	srv, db, _ := serverWithPlanning(t, &planStubRunner{})
	seedWizard(t, db, "uuid-raw-empty", planning.StatusAwaiting, false) // no question
	resp := postPlanningJSON(t, srv.URL+"/api/projects/1/planning/answer",
		map[string]any{"questionId": "", "selectedOptionIds": []string{"opt-a"}, "otherText": "   "})
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("raw-mode empty otherText = %d, want 400", resp.StatusCode)
	}
	if s := wizardStatusInDB(t, db, "uuid-raw-empty"); s != planning.StatusAwaiting {
		t.Errorf("status = %q, want awaiting_answer (rejected answer must not flip or spawn)", s)
	}
	if resumeInFlight("uuid-raw-empty") {
		t.Error("a resume was spawned for a rejected answer")
	}
}

func TestWizardAnswer_409WrongQuestion(t *testing.T) {
	srv, db, _ := serverWithPlanning(t, &planStubRunner{})
	seedWizard(t, db, "uuid-wrongq", planning.StatusAwaiting, true)
	resp := postPlanningJSON(t, srv.URL+"/api/projects/1/planning/answer",
		map[string]any{"questionId": "q-stale", "selectedOptionIds": []string{"opt-a"}})
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusConflict {
		t.Errorf("wrong question id = %d, want 409", resp.StatusCode)
	}
}

func TestWizardAnswer_409NotAwaiting(t *testing.T) {
	srv, db, _ := serverWithPlanning(t, &planStubRunner{})
	seedWizard(t, db, "uuid-notaw", planning.StatusGenerating, true)
	resp := postPlanningJSON(t, srv.URL+"/api/projects/1/planning/answer",
		map[string]any{"questionId": "q-scope", "selectedOptionIds": []string{"opt-a"}})
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusConflict {
		t.Errorf("not awaiting = %d, want 409", resp.StatusCode)
	}
}

func TestWizardAnswer_409SessionNotMintedRollsBack(t *testing.T) {
	srv, db, _ := serverWithPlanning(t, &planStubRunner{})
	seedWizard(t, db, "uuid-nomint", planning.StatusAwaiting, true)
	// No sessions row for the uuid — ingest hasn't minted it yet.
	resp := postPlanningJSON(t, srv.URL+"/api/projects/1/planning/answer",
		map[string]any{"questionId": "q-scope", "selectedOptionIds": []string{"opt-a"}})
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusConflict {
		t.Errorf("unminted session = %d, want 409", resp.StatusCode)
	}
	// The status must be rolled back so the operator can retry.
	if s := wizardStatusInDB(t, db, "uuid-nomint"); s != planning.StatusAwaiting {
		t.Errorf("status after failed spawn = %q, want awaiting_answer (rollback)", s)
	}
}

func TestWizardAnswer_202Happy(t *testing.T) {
	t.Setenv("SWARMERY_CLAUDE_BIN", "/usr/bin/true")
	srv, db, _ := serverWithPlanning(t, &planStubRunner{})
	seedWizard(t, db, "uuid-happy", planning.StatusAwaiting, true)
	seedPlannerSession(t, db, 80, "uuid-happy")

	resp := postPlanningJSON(t, srv.URL+"/api/projects/1/planning/answer",
		map[string]any{"questionId": "q-scope", "selectedOptionIds": []string{"opt-a"}, "otherText": "note"})
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("answer = %d, want 202; body: %s", resp.StatusCode, body)
	}
	var out map[string]string
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	if out["status"] != "generating" {
		t.Errorf("body status = %q, want generating", out["status"])
	}
	if s := wizardStatusInDB(t, db, "uuid-happy"); s != planning.StatusGenerating {
		t.Errorf("db status = %q, want generating", s)
	}
	// The answer JSON landed on the history turn.
	var ans sql.NullString
	db.QueryRow(`SELECT answer FROM planning_turns WHERE seq=1`).Scan(&ans)
	if !ans.Valid {
		t.Error("answer not stamped on the history turn")
	}
	waitResumeSettled(t, "uuid-happy")
}

func TestWizardRefine_Validation(t *testing.T) {
	srv, db, _ := serverWithPlanning(t, &planStubRunner{})
	seedWizard(t, db, "uuid-refine-v", planning.StatusAwaiting, true)

	resp := postPlanningJSON(t, srv.URL+"/api/projects/1/planning/refine",
		map[string]string{"instructions": "   "})
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("empty instructions = %d, want 400", resp.StatusCode)
	}

	long := make([]byte, 4001)
	for i := range long {
		long[i] = 'x'
	}
	resp2 := postPlanningJSON(t, srv.URL+"/api/projects/1/planning/refine",
		map[string]string{"instructions": string(long)})
	defer resp2.Body.Close()
	if resp2.StatusCode != http.StatusBadRequest {
		t.Errorf("oversized instructions = %d, want 400", resp2.StatusCode)
	}
}

func TestWizardRefine_404NoSessionAnd409NotAwaiting(t *testing.T) {
	srv, db, _ := serverWithPlanning(t, &planStubRunner{})

	// No wizard row at all → 404.
	resp := postPlanningJSON(t, srv.URL+"/api/projects/1/planning/refine",
		map[string]string{"instructions": "tighten scope"})
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("refine with no wizard = %d, want 404", resp.StatusCode)
	}

	// Row exists but is not awaiting_answer → 409.
	seedWizard(t, db, "uuid-refine-409", planning.StatusGenerating, true)
	resp2 := postPlanningJSON(t, srv.URL+"/api/projects/1/planning/refine",
		map[string]string{"instructions": "tighten scope"})
	defer resp2.Body.Close()
	if resp2.StatusCode != http.StatusConflict {
		t.Errorf("refine while generating = %d, want 409", resp2.StatusCode)
	}
}

func TestWizardProceed_404NoSession(t *testing.T) {
	srv, _, _ := serverWithPlanning(t, &planStubRunner{})
	resp := postPlanningJSON(t, srv.URL+"/api/projects/1/planning/proceed", nil)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("proceed with no wizard = %d, want 404", resp.StatusCode)
	}
}

func TestWizardRefine_202Happy(t *testing.T) {
	t.Setenv("SWARMERY_CLAUDE_BIN", "/usr/bin/true")
	srv, db, _ := serverWithPlanning(t, &planStubRunner{})
	seedWizard(t, db, "uuid-refine", planning.StatusAwaiting, true)
	seedPlannerSession(t, db, 81, "uuid-refine")

	resp := postPlanningJSON(t, srv.URL+"/api/projects/1/planning/refine",
		map[string]string{"instructions": "make it smaller"})
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("refine = %d, want 202; body: %s", resp.StatusCode, body)
	}
	var out map[string]string
	json.NewDecoder(resp.Body).Decode(&out)
	if out["status"] != "generating" {
		t.Errorf("body status = %q, want generating", out["status"])
	}
	waitResumeSettled(t, "uuid-refine")
}

func TestWizardProceed_202AndNotAwaiting409(t *testing.T) {
	t.Setenv("SWARMERY_CLAUDE_BIN", "/usr/bin/true")
	srv, db, _ := serverWithPlanning(t, &planStubRunner{})
	seedWizard(t, db, "uuid-proceed", planning.StatusAwaiting, true)
	seedPlannerSession(t, db, 82, "uuid-proceed")

	resp := postPlanningJSON(t, srv.URL+"/api/projects/1/planning/proceed", nil)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("proceed = %d, want 202; body: %s", resp.StatusCode, body)
	}
	var out map[string]string
	json.NewDecoder(resp.Body).Decode(&out)
	if out["status"] != "proceeding" {
		t.Errorf("body status = %q, want proceeding", out["status"])
	}
	if s := wizardStatusInDB(t, db, "uuid-proceed"); s != planning.StatusProceeding {
		t.Errorf("db status = %q, want proceeding", s)
	}

	// Second proceed: no longer awaiting → 409.
	resp2 := postPlanningJSON(t, srv.URL+"/api/projects/1/planning/proceed", nil)
	defer resp2.Body.Close()
	if resp2.StatusCode != http.StatusConflict {
		t.Errorf("second proceed = %d, want 409", resp2.StatusCode)
	}
	waitResumeSettled(t, "uuid-proceed")
}

func TestWizardAnswer_503WhenUnattached(t *testing.T) {
	AttachPlanning(nil)
	srv := testServer(t)
	resp := postPlanningJSON(t, srv.URL+"/api/projects/1/planning/answer",
		map[string]any{"questionId": "q", "selectedOptionIds": []string{"a"}})
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("unattached answer = %d, want 503", resp.StatusCode)
	}
}

func TestWizardAnswer_CrossOriginRejected(t *testing.T) {
	srv, db, _ := serverWithPlanning(t, &planStubRunner{})
	seedWizard(t, db, "uuid-origin", planning.StatusAwaiting, true)
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/api/projects/1/planning/answer",
		bytes.NewReader([]byte(`{"questionId":"q-scope","selectedOptionIds":["opt-a"]}`)))
	req.Header.Set("Origin", "http://evil.example.com")
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("cross-origin answer = %d, want 403", resp.StatusCode)
	}
}

// ── ingest hook ──

// TestPlanningBusConsumer_AdvancesWizard proves the AttachPlanning bus
// subscription drives OnSessionTurns: a session_updated frame for the planner
// session moves generating → awaiting_answer without any HTTP call.
func TestPlanningBusConsumer_AdvancesWizard(t *testing.T) {
	bus := ingest.NewBus()
	AttachBus(bus)
	t.Cleanup(func() { AttachBus(nil) }) // registered first → runs last (after AttachPlanning(nil))

	srv, db, _ := serverWithPlanning(t, &planStubRunner{})
	_ = srv
	seedWizard(t, db, "uuid-bus", planning.StatusGenerating, false)
	seedPlannerSession(t, db, 90, "uuid-bus")
	if _, err := db.Exec(
		`INSERT INTO turns(session_id, seq, role, started_at, text)
		 VALUES(90, 1, 'assistant', '2026-01-01T00:00:01Z', ?)`, wizQuestionTurnText); err != nil {
		t.Fatal(err)
	}

	bus.Publish(ingest.Notification{Type: ingest.NoteSessionUpdated, SessionID: 90})

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if wizardStatusInDB(t, db, "uuid-bus") == planning.StatusAwaiting {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("wizard status = %q, want awaiting_answer via the bus consumer",
		wizardStatusInDB(t, db, "uuid-bus"))
}

// waitResumeSettled parks until the spawned stub resume (/usr/bin/true) exits
// and releases its msgInFlight slot, so its onExit closure cannot touch a DB a
// later test already closed.
func waitResumeSettled(t *testing.T, uuid string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if !resumeInFlight(uuid) {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("resume for %s never settled", uuid)
}
