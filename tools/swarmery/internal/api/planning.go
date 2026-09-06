package api

// Planning Mode endpoints (fusion phase 8): turn a rough idea into a structured
// plan. POST /api/projects/{id}/planning spawns a headless planner run (through
// internal/planning) in the project directory; GET returns its live status. The
// planner asks clarifying questions as reply TEXT (the phase-8 spike proved
// AskUserQuestion does NOT fire the permission hook under `claude -p`) which the
// Planning page surfaces from the ingested transcript; the user answers via the
// EXISTING session-resume chat (POST /api/sessions/{id}/message), and the run
// writes a plan into the private workspace that wsingest surfaces as a workspace
// task row to activate into board tasks.
//
// The service is attached once at daemon startup (AttachPlanning) — the same
// package-var idiom as dispatchSvc/approvalsSvc — so httptest handlers built with
// &Handler{DB: db} stay hermetic (planningSvc nil ⇒ endpoints 503, no spawn).
// Writes carry the D4 requireLocalOrigin hardening; the POST also 409s when a
// run is already active for the project.

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/ingest"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/planning"
)

// planningSvc is attached once at daemon startup (nil ⇒ planning endpoints 503
// and the notify adapter is never wired).
var planningSvc *planning.Service

// planningBusStop tears down the wizard's ingest-bus subscription (set by
// AttachPlanning when both the service and the bus are attached).
var planningBusStop func()

// AttachPlanning wires the planning service into the api layer and gives it the
// api-owned session_updated emitter, keyed by project: on a planner-run edge the
// adapter resolves the in-flight session row (once ingest/the hook mints it) and
// republishes it over the FROZEN session_updated frame — no new WS type. The
// emitter closes over the SERVICE (never the planningSvc package var) so a
// run-goroutine Notify firing during a test's AttachPlanning(nil) teardown does
// not race the package-var write. Called from cmd/swarmery after the service is
// constructed. Left nil in unit tests so board/session writes never trigger a
// real headless spawn.
func AttachPlanning(s *planning.Service) {
	if planningBusStop != nil {
		planningBusStop()
		planningBusStop = nil
	}
	if s != nil {
		s.Notify = func(projectID int64) {
			// The planner session's numeric row is minted by ingest AFTER spawn,
			// so at the start edge there may be no row yet — then this is a no-op
			// and the page's reconcile poll (60s WS net + its own settle poll)
			// catches up. Reads the in-flight uuid from the service snapshot.
			if st := s.Snapshot(projectID); st.SessionID != nil {
				publishSessionUpdated(*st.SessionID)
			}
		}
		// The wizard's "process alive" seam: the raw-fallback parse and the
		// stale-generating reconcile must not fire while a `claude -r` resume
		// is mid-flight for the planner session (resume.go's map).
		s.ResumeInFlight = resumeInFlight
		if wsBus != nil {
			planningBusStop = watchPlanningTurns(s, wsBus)
		}
	}
	planningSvc = s
}

// watchPlanningTurns subscribes the planning service to the ingest bus so new
// transcript turns advance the wizard (phase-2 ingest hook).
//
// Deliberately NOT the ws.go fan-out the phase doc sketched: buildWSMessage
// runs once per CONNECTED WebSocket client, so with zero dashboards open the
// wizard would never advance and with N dashboards every turn would be parsed
// N times. A dedicated subscription is the one consumer that runs exactly once
// regardless of clients. Only session_started/session_updated are consumed —
// every ingest batch that appends turns publishes exactly one such frame
// (ingest.Pipeline.tailOne), so the per-event event_appended frames carry no
// extra signal for the wizard and are skipped wholesale.
func watchPlanningTurns(s *planning.Service, bus *ingest.Bus) (stop func()) {
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		for ctx.Err() == nil {
			ch, unsub := bus.Subscribe(wsSubscriberBuffer)
			consumePlanningNotes(ctx, s, ch)
			unsub()
			// Channel closed as a laggard (buffer overflow) → resubscribe.
			// Missed frames self-heal: OnSessionTurns recomputes from the
			// LATEST turn only, and the snapshot's stale-generating reconcile
			// covers a lost exit edge.
		}
	}()
	return func() { cancel(); <-done }
}

// consumePlanningNotes drains one bus subscription until ctx cancels or the
// bus closes the channel (overflow). The id→uuid resolve is a PK point query
// and the service's own uuid lookup (UNIQUE index) rejects non-planner
// sessions — two microsecond SELECTs per session_updated frame. OnSessionTurns
// runs inline in this goroutine so per-session ordering is preserved.
func consumePlanningNotes(ctx context.Context, s *planning.Service, ch <-chan ingest.Notification) {
	for {
		select {
		case <-ctx.Done():
			return
		case n, ok := <-ch:
			if !ok {
				return
			}
			if n.Type != ingest.NoteSessionStarted && n.Type != ingest.NoteSessionUpdated {
				continue
			}
			var uuid string
			if err := s.DB.QueryRow(
				`SELECT session_uuid FROM sessions WHERE id = ?`, n.SessionID).Scan(&uuid); err != nil {
				continue // row vanished or not yet minted — nothing to parse
			}
			s.OnSessionTurns(uuid)
		}
	}
}

// GET /api/projects/{id}/planning — the extended wizard DTO for a project
// (phase 2): the legacy active/sessionUuid/sessionId/startedAt fields plus
// status, currentQuestion, runningPlan, rawReply, history, planDir. Field
// names are FROZEN (planning.WizardStatus — phase 3 mirrors them in TS).
// A project with no planning_sessions row gets the legacy idle shape
// (active:false, empty extended fields). 503 when the planning service is not
// attached (serve --no-ingest, or a test handler).
func (h *Handler) getPlanning(w http.ResponseWriter, r *http.Request) {
	if planningSvc == nil {
		writeClientErr(w, http.StatusServiceUnavailable, "planning not attached")
		return
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeClientErr(w, http.StatusBadRequest, "invalid project id")
		return
	}
	st, err := planningSvc.WizardSnapshot(id)
	writeJSON(w, st, err)
}

// POST /api/projects/{id}/planning {idea, model?} → 202 {sessionUuid}.
// requireLocalOrigin. model is a planning.Models short name or full ID ("" or
// absent = the planner default). 400 invalid id / empty idea / unknown model;
// 404 unknown project; 409 a run is already active; 503 the service is not
// attached.
func (h *Handler) startPlanning(w http.ResponseWriter, r *http.Request) {
	if planningSvc == nil {
		writeClientErr(w, http.StatusServiceUnavailable, "planning not attached")
		return
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeClientErr(w, http.StatusBadRequest, "invalid project id")
		return
	}
	var body struct {
		Idea  string `json:"idea"`
		Model string `json:"model"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&body); err != nil {
		writeClientErr(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if len(body.Idea) > maxPlanningIdeaLen {
		writeClientErr(w, http.StatusRequestEntityTooLarge, "idea too long")
		return
	}
	if strings.TrimSpace(body.Idea) == "" {
		writeClientErr(w, http.StatusBadRequest, "idea is required")
		return
	}

	uuid, err := planningSvc.Start(id, body.Idea, body.Model)
	switch {
	case errors.Is(err, planning.ErrUnknownModel):
		writeClientErr(w, http.StatusBadRequest, err.Error())
		return
	case errors.Is(err, planning.ErrProjectNotFound):
		writeClientErr(w, http.StatusNotFound, "project not found")
		return
	case errors.Is(err, planning.ErrNoPath):
		writeClientErr(w, http.StatusConflict, "project has no known path to plan in")
		return
	case errors.Is(err, planning.ErrActive):
		writeJSONStatus(w, http.StatusConflict, map[string]any{
			"error":       "a planning run is already active for this project",
			"sessionUuid": planningSvc.Snapshot(id).SessionUUID,
		})
		return
	case err != nil:
		writeErr(w, err)
		return
	}
	writeJSONStatus(w, http.StatusAccepted, map[string]string{"sessionUuid": uuid})
}

// POST /api/projects/{id}/planning/cancel — abort the in-flight planner run.
// requireLocalOrigin. 409 when nothing is in flight; 503 when not attached.
func (h *Handler) cancelPlanning(w http.ResponseWriter, r *http.Request) {
	if planningSvc == nil {
		writeClientErr(w, http.StatusServiceUnavailable, "planning not attached")
		return
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeClientErr(w, http.StatusBadRequest, "invalid project id")
		return
	}
	if !planningSvc.Cancel(id) {
		writeClientErr(w, http.StatusConflict, "no planning run is active for this project")
		return
	}
	writeJSONStatus(w, http.StatusAccepted, map[string]any{"status": "cancelling", "projectId": id})
}

// maxPlanningIdeaLen bounds the idea payload in BYTES. The bound is technical,
// not editorial: the idea travels inside the planner prompt, and the prompt is
// ONE argv element of the spawned `claude -p` (see internal/runcore). Linux
// caps a single argument at 128 KiB (MAX_ARG_STRLEN); macOS caps the whole argv
// at 1 MiB. 100k leaves the prompt template and the rest of the argv room under
// the stricter of the two, so a whole spec document pastes in, while a payload
// that would make the spawn fail with E2BIG is refused up front instead.
const maxPlanningIdeaLen = 100_000

// maxRefineLen bounds refinement instructions (free-form prose, not a spec).
const maxRefineLen = 4000

// writeWizardErr maps the planning sentinels onto HTTP statuses. Returns true
// when it wrote a response (the handler must return).
func writeWizardErr(w http.ResponseWriter, err error) bool {
	switch {
	case err == nil:
		return false
	case errors.Is(err, planning.ErrNoSession):
		writeClientErr(w, http.StatusNotFound, "no planning session for this project")
	case errors.Is(err, planning.ErrNotAwaiting):
		writeClientErr(w, http.StatusConflict, "planning session is not awaiting an answer")
	case errors.Is(err, planning.ErrWrongQuestion):
		writeClientErr(w, http.StatusConflict, "answer does not match the current question")
	case errors.Is(err, planning.ErrEmptyAnswer):
		writeClientErr(w, http.StatusBadRequest, "raw-fallback answer requires non-empty otherText")
	default:
		writeErr(w, err)
	}
	return true
}

// spawnWizardResume is the shared back half of answer/refine/proceed: the
// service already moved the wizard to generating/proceeding and produced the
// resume text — resolve the ingested sessions row and hand it to startResume
// (the SAME single-flight map as the composer, so a wizard reply and a manual
// message can never interleave one transcript). EVERY failure path rolls the
// status back to awaiting_answer so the operator's action stays retryable —
// keyed by the wizard's session uuid, so a stale resume's late failure can
// never roll back a NEWER wizard the operator started meanwhile.
func (h *Handler) spawnWizardResume(w http.ResponseWriter, svc *planning.Service, uuid, text, okStatus string) {
	var (
		sid     int64
		cwd     sql.NullString
		account sql.NullString
	)
	err := h.DB.QueryRow(`SELECT id, cwd, account FROM sessions WHERE session_uuid = ?`, uuid).Scan(&sid, &cwd, &account)
	if errors.Is(err, sql.ErrNoRows) {
		const msg = "planner session not ingested yet — retry in a moment"
		svc.RevertToAwaiting(uuid, msg)
		writeClientErr(w, http.StatusConflict, msg)
		return
	}
	if err != nil {
		svc.RevertToAwaiting(uuid, "could not read the planner session row: "+err.Error())
		writeErr(w, err)
		return
	}
	if strings.TrimSpace(cwd.String) == "" {
		const msg = "planner session has no known working directory to resume in"
		svc.RevertToAwaiting(uuid, msg)
		writeClientErr(w, http.StatusConflict, msg)
		return
	}
	// The wizard's pinned model rides along on every turn: the first spawn passed
	// --model, and a resume without it would silently switch the interview to
	// the account default mid-way.
	started, err := startResume(sid, uuid, cwd.String, account.String, text, svc.Model(uuid), func(runErr error) {
		// Runs after process exit, BEFORE the resume slot release. A failed or
		// timed-out resume rolls back so the wizard is answerable again, WITH the
		// process error as the reason — this is the one rollback the operator
		// cannot otherwise see: the POST already answered 202, so a bare revert
		// looks exactly like the planner asking the same question again.
		// On success nothing to do here: the post-release session_updated frame
		// reaches watchPlanningTurns, which re-parses with the slot free (the
		// path that also settles a raw-fallback reply ingested mid-run).
		if runErr != nil {
			svc.RevertToAwaiting(uuid, "the planner run failed ("+runErr.Error()+") — your reply was not delivered, retry it")
		}
	})
	if errors.Is(err, errResumeCwdGone) {
		msg := "the planner session's working directory (" + cwd.String + ") no longer exists — start a new planning session"
		svc.RevertToAwaiting(uuid, msg)
		writeClientErr(w, http.StatusConflict, msg)
		return
	}
	if err != nil {
		const msg = "claude executable not found (set SWARMERY_CLAUDE_BIN)"
		svc.RevertToAwaiting(uuid, msg)
		writeClientErr(w, http.StatusServiceUnavailable, msg)
		return
	}
	if !started {
		const msg = "a resume is already running for this session"
		svc.RevertToAwaiting(uuid, msg)
		writeClientErr(w, http.StatusConflict, msg)
		return
	}
	writeJSONStatus(w, http.StatusAccepted, map[string]string{"status": okStatus})
}

// wizardProject parses the {id} path value shared by the wizard POSTs.
func wizardProject(w http.ResponseWriter, r *http.Request) (int64, *planning.Service, bool) {
	svc := planningSvc
	if svc == nil {
		writeClientErr(w, http.StatusServiceUnavailable, "planning not attached")
		return 0, nil, false
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeClientErr(w, http.StatusBadRequest, "invalid project id")
		return 0, nil, false
	}
	return id, svc, true
}

// POST /api/projects/{id}/planning/answer {questionId, selectedOptionIds,
// otherText?} → 202 {status:"generating"}. requireLocalOrigin. 400 bad body /
// nothing selected; 404 no wizard; 409 not awaiting, wrong question, session
// not yet ingested, or a resume already in flight.
func (h *Handler) answerPlanning(w http.ResponseWriter, r *http.Request) {
	id, svc, ok := wizardProject(w, r)
	if !ok {
		return
	}
	var body struct {
		QuestionID        string   `json:"questionId"`
		SelectedOptionIDs []string `json:"selectedOptionIds"`
		OtherText         string   `json:"otherText"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&body); err != nil {
		writeClientErr(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	body.OtherText = strings.TrimSpace(body.OtherText)
	if len(body.SelectedOptionIDs) == 0 && body.OtherText == "" {
		writeClientErr(w, http.StatusBadRequest, "select at least one option or provide otherText")
		return
	}
	text, uuid, err := svc.Answer(id, body.QuestionID, body.SelectedOptionIDs, body.OtherText)
	if writeWizardErr(w, err) {
		return
	}
	h.spawnWizardResume(w, svc, uuid, text, "generating")
}

// POST /api/projects/{id}/planning/refine {instructions} → 202
// {status:"generating"}. requireLocalOrigin. Same error matrix as answer.
func (h *Handler) refinePlanning(w http.ResponseWriter, r *http.Request) {
	id, svc, ok := wizardProject(w, r)
	if !ok {
		return
	}
	var body struct {
		Instructions string `json:"instructions"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&body); err != nil {
		writeClientErr(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	body.Instructions = strings.TrimSpace(body.Instructions)
	if body.Instructions == "" {
		writeClientErr(w, http.StatusBadRequest, "instructions are required")
		return
	}
	if len(body.Instructions) > maxRefineLen {
		writeClientErr(w, http.StatusBadRequest, "instructions too long (max 4000 chars)")
		return
	}
	text, uuid, err := svc.Refine(id, body.Instructions)
	if writeWizardErr(w, err) {
		return
	}
	h.spawnWizardResume(w, svc, uuid, text, "generating")
}

// POST /api/projects/{id}/planning/proceed (no body) → 202
// {status:"proceeding"}: end the interview, trigger PHASE B plan writing.
// requireLocalOrigin. 404/409 as answer.
func (h *Handler) proceedPlanning(w http.ResponseWriter, r *http.Request) {
	id, svc, ok := wizardProject(w, r)
	if !ok {
		return
	}
	text, uuid, err := svc.Proceed(id)
	if writeWizardErr(w, err) {
		return
	}
	h.spawnWizardResume(w, svc, uuid, text, "proceeding")
}
