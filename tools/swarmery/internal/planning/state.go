package planning

// Durable wizard state machine (interactive planning v2, phase 2). The 0034
// tables make the wizard survive daemon restarts: planning_sessions holds one
// row per Start (status machine + latest question/plan), planning_turns the
// per-question history. The in-memory single-flight map stays authoritative
// for "a claude process is alive"; the DB status is authoritative for the
// WIZARD state — the two are joined by processAlive().
//
// Status set (closed): generating | awaiting_answer | proceeding | done |
// failed | cancelled. Transitions:
//
//	Start            →  generating           (row inserted)
//	OnSessionTurns   →  awaiting_answer      (question parsed, or raw fallback
//	                                          once no process is alive — incl. a
//	                                          proceeding run whose reply lacked
//	                                          the "PLAN SAVED:" sentinel or
//	                                          carried an off-convention path)
//	Answer / Refine  →  generating           (resume spawned by the api layer)
//	Proceed          →  proceeding
//	OnSessionTurns   →  done + plan_dir      ("PLAN SAVED:" sentinel with a
//	                                          scanner-visible path)
//	runner failure   →  failed               (only while generating)
//	Cancel / Start   →  cancelled            (explicit, or superseded)
//	stale reconcile  →  awaiting_answer      (generating/proceeding >16min,
//	                                          no process)
//
// Every status UPDATE is guarded on the expected prior status (CAS predicate),
// so a writer holding a stale in-memory row can never overwrite a concurrent
// Cancel/Answer/Start — the loser observes 0 rows affected and backs off.

import (
	"database/sql"
	"encoding/json"
	"errors"
	"log"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/procfind"
)

// Wizard statuses — the closed set persisted in planning_sessions.status.
const (
	StatusGenerating = "generating"
	StatusAwaiting   = "awaiting_answer"
	StatusProceeding = "proceeding"
	StatusDone       = "done"
	StatusFailed     = "failed"
	StatusCancelled  = "cancelled"
)

// Wizard modes — the closed set persisted in planning_sessions.mode. A 'plan'
// wizard mints a new plan dir; a 'revise' wizard stages a proposal against an
// existing one and must never write a plan itself.
const (
	ModePlan   = "plan"
	ModeRevise = "revise"
)

// planSavedMarker is the PHASE B completion sentinel the plan prompt mandates.
const planSavedMarker = "PLAN SAVED:"

// revisionStagedMarker is the PHASE B completion sentinel of a revise session.
const revisionStagedMarker = "REVISION STAGED:"

// staleWindow is the reconcile window for a 'generating' or 'proceeding' row
// with no live process: the api resume timeout (15 min) + 1 min grace, so a
// daemon restart or a lost exit edge cannot wedge the wizard in a spinner
// forever.
const staleWindow = 16 * time.Minute

// Sentinel errors mapped to HTTP statuses by the api layer (409/404).
var (
	// ErrNoSession: no planning_sessions row for the project (404).
	ErrNoSession = errors.New("no planning session for this project")
	// ErrNotAwaiting: the wizard is not in awaiting_answer (409).
	ErrNotAwaiting = errors.New("planning session is not awaiting an answer")
	// ErrWrongQuestion: the answered question id is not the current one (409).
	ErrWrongQuestion = errors.New("answer does not match the current question")
	// ErrEmptyAnswer: a raw-fallback answer (no structured question) carried no
	// free text — there is nothing to resume with (400, client-shape error).
	ErrEmptyAnswer = errors.New("raw-fallback answer requires non-empty otherText")
)

// wizardAnswer is the JSON stamped onto planning_turns.answer.
type wizardAnswer struct {
	Kind              string   `json:"kind"` // answer | refine
	SelectedOptionIDs []string `json:"selectedOptionIds,omitempty"`
	OtherText         string   `json:"otherText,omitempty"`
	Instructions      string   `json:"instructions,omitempty"`
}

// WizardTurn is one history entry of the extended status DTO.
type WizardTurn struct {
	Seq       int64             `json:"seq"`
	Question  *PlanningQuestion `json:"question"`
	Answer    json.RawMessage   `json:"answer"` // null until answered
	Reasoning string            `json:"reasoning"`
}

// WizardStatus is the extended DTO for GET /api/projects/{id}/planning.
// Field names are FROZEN — phase 3 mirrors them verbatim in TypeScript
// (Mode/ReviseTaskId: plan-revision phase 4 does the same).
type WizardStatus struct {
	Active      bool    `json:"active"`
	SessionUUID string  `json:"sessionUuid"`
	SessionID   *int64  `json:"sessionId"`
	StartedAt   *string `json:"startedAt"`
	Status      string  `json:"status"` // "" when no wizard row (legacy idle)
	Mode        string  `json:"mode"`   // plan|revise; "" when no wizard row
	// Model is the full model ID every turn of this wizard runs on (see Models);
	// "" when no wizard row.
	Model           string            `json:"model"`
	ReviseTaskId    *int64            `json:"reviseTaskId"`
	CurrentQuestion *PlanningQuestion `json:"currentQuestion"`
	RunningPlan     *PlanningSummary  `json:"runningPlan"`
	RawReply        *string           `json:"rawReply"`
	History         []WizardTurn      `json:"history"`
	PlanDir         *string           `json:"planDir"`
	// LastError explains why the wizard is answerable again after an action that
	// did not go through (failed resume spawn, dead planner). Null once the next
	// action starts — it describes the LAST attempt, never the current state.
	LastError *string `json:"lastError"`
}

// wizardRow mirrors one planning_sessions row.
type wizardRow struct {
	id              int64
	projectID       int64
	uuid            string
	status          string
	runningPlan     sql.NullString
	currentQuestion sql.NullString
	rawReply        sql.NullString
	planDir         sql.NullString
	createdAt       string
	updatedAt       string
	mode            string
	reviseTaskID    sql.NullInt64
	lastError       sql.NullString
	model           sql.NullString
}

const wizardCols = `id, project_id, session_uuid, status, running_plan, current_question, raw_reply, plan_dir, created_at, updated_at, mode, revise_task_id, last_error, model`

func scanWizard(scan func(...any) error) (*wizardRow, error) {
	var r wizardRow
	err := scan(&r.id, &r.projectID, &r.uuid, &r.status, &r.runningPlan,
		&r.currentQuestion, &r.rawReply, &r.planDir, &r.createdAt, &r.updatedAt,
		&r.mode, &r.reviseTaskID, &r.lastError, &r.model)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &r, nil
}

// wizardByUUID is the ingest-hook admission check: one SELECT on the UNIQUE
// session_uuid index, nil on miss (not a planner session).
func (s *Service) wizardByUUID(uuid string) (*wizardRow, error) {
	return scanWizard(s.DB.QueryRow(
		`SELECT `+wizardCols+` FROM planning_sessions WHERE session_uuid = ?`, uuid).Scan)
}

// newestWizard resolves THE wizard of a project: the newest row (older rows
// are history — Start supersedes any still-open one to cancelled).
func (s *Service) newestWizard(projectID int64) (*wizardRow, error) {
	return scanWizard(s.DB.QueryRow(
		`SELECT `+wizardCols+` FROM planning_sessions WHERE project_id = ? ORDER BY id DESC LIMIT 1`,
		projectID).Scan)
}

// ts is the stored-timestamp format (RFC3339 UTC, codebase-wide convention).
func (s *Service) ts() string { return s.clock().UTC().Format(time.RFC3339) }

// processAlive joins the liveness sources, cheapest first: the in-memory planner
// slot (initial `claude -p` run), the api layer's resume map (`claude -r`,
// exposed through the ResumeInFlight seam — nil in bare unit tests), and finally
// the process table.
//
// The third source exists because the planner spawn is process-group isolated and
// so OUTLIVES a daemon restart: after a restart both in-memory sources are empty,
// and without a real probe a planner that is still writing its plan would be read
// as dead and rolled back to awaiting_answer under the operator's feet. The probe
// runs last — it shells out to ps, and the two in-memory checks answer for every
// planner this process started itself.
func (s *Service) processAlive(projectID int64, uuid string) bool {
	s.mu.Lock()
	r, ok := s.active[projectID]
	s.mu.Unlock()
	if ok && r.uuid == uuid {
		return true
	}
	if s.ResumeInFlight != nil && s.ResumeInFlight(uuid) {
		return true
	}
	find := s.FindRun
	if find == nil {
		find = procfind.BySessionUUID
	}
	_, live := find(uuid)
	return live
}

// lastAssistantText returns the session's newest assistant turn prose (by
// uuid), "" when nothing is ingested yet. Same query shape as dispatch's
// lastAssistantText. Note: turns.text is text blocks ONLY — ingest drops
// thinking blocks entirely (migration 0005), so the persisted "reasoning" is
// the pre-JSON analysis prose, never extended thinking.
func (s *Service) lastAssistantText(uuid string) string {
	var text sql.NullString
	err := s.DB.QueryRow(`
		SELECT tr.text
		  FROM turns tr JOIN sessions se ON se.id = tr.session_id
		 WHERE se.session_uuid=? AND tr.role='assistant' AND tr.text IS NOT NULL
		 ORDER BY tr.seq DESC LIMIT 1`, uuid).Scan(&text)
	if err != nil || !text.Valid {
		return ""
	}
	return text.String
}

// extractPlanDir pulls the absolute path off the LAST "PLAN SAVED:" line.
func extractPlanDir(text string) string {
	return extractMarkerPath(text, planSavedMarker)
}

// extractStagedDir pulls the scratch dir off the LAST "REVISION STAGED:" line.
func extractStagedDir(text string) string {
	return extractMarkerPath(text, revisionStagedMarker)
}

func extractMarkerPath(text, marker string) string {
	idx := strings.LastIndex(text, marker)
	if idx < 0 {
		return ""
	}
	rest := text[idx+len(marker):]
	if nl := strings.IndexByte(rest, '\n'); nl >= 0 {
		rest = rest[:nl]
	}
	return strings.TrimSpace(rest)
}

// planDirShapeRe is the only task-dir shape the workspace scanner
// (internal/wsingest) ever walks — workspace/{working,archive}/YYYY/MM/DD/<slug>/
// — with the plan/ leaf the sentinel usually points at accepted too. A PLAN
// SAVED path outside this shape would be invisible on the Plans page forever
// (issue #188), so OnSessionTurns refuses to treat it as completion.
var planDirShapeRe = regexp.MustCompile(`/workspace/(?:working|archive)/\d{4}/\d{2}/\d{2}/[^/]+(?:/plan)?/?$`)

// validPlanDir reports whether an extracted PLAN SAVED path matches the
// scanner-visible task-dir shape.
func validPlanDir(dir string) bool {
	return dir != "" && planDirShapeRe.MatchString(filepath.ToSlash(dir))
}

// OnSessionTurns advances the wizard when new transcript turns land for a
// session. Called by the api layer's ingest-bus consumer for EVERY
// session_updated — the wizardByUUID miss (one indexed SELECT) is the cheap
// non-planner early-out. Recomputes from the LATEST assistant turn only, so
// re-ingest is idempotent; every no-change path returns WITHOUT Notify,
// because Notify feeds the same bus this is called from (a notify on a
// no-op would loop notify→bus→OnSessionTurns forever).
func (s *Service) OnSessionTurns(sessionUUID string) {
	row, err := s.wizardByUUID(sessionUUID)
	if err != nil {
		log.Printf("error: planning: wizard lookup for uuid %s: %v", sessionUUID, err)
		return
	}
	if row == nil {
		return // not a planner session
	}
	switch row.status {
	case StatusDone, StatusFailed, StatusCancelled:
		return // terminal — the transcript can no longer move the wizard
	}
	text := s.lastAssistantText(sessionUUID)
	if text == "" {
		return // transcript not (yet) ingested
	}

	// Mode branch BEFORE the PLAN SAVED check: a revise session has its own
	// sentinel and must never be completed by (or write) a new plan dir.
	// mode='plan' keeps the pre-revision behaviour below byte-for-byte.
	if row.mode == ModeRevise {
		s.onReviseTurn(row, text)
		return
	}

	// PHASE B completion sentinel. Tolerated while 'generating' too — the model
	// may write the plan without a PROCEED if the idea needed no interview.
	if (row.status == StatusProceeding || row.status == StatusGenerating) &&
		strings.Contains(text, planSavedMarker) {
		if dir := extractPlanDir(text); validPlanDir(dir) {
			// CAS: a Cancel landing between the row read and this write must win —
			// 'done' may only replace the still-open statuses the read observed.
			res, err := s.DB.Exec(
				`UPDATE planning_sessions SET status=?, plan_dir=?, updated_at=? WHERE id=? AND status IN (?, ?)`,
				StatusDone, dir, s.ts(), row.id, StatusProceeding, StatusGenerating)
			if err != nil {
				log.Printf("error: planning: mark done uuid=%s: %v", sessionUUID, err)
				return
			}
			if n, _ := res.RowsAffected(); n == 0 {
				return // lost to a concurrent terminal write — never overwrite it
			}
			log.Printf("planning: wizard uuid=%s done — plan at %q", sessionUUID, dir)
			s.notify(row.projectID)
			return
		} else {
			// Sentinel present but the path is a shape the scanner never walks
			// (e.g. the frozen workspace/plans/ tree) — stamping done would hide
			// the plan from the Plans page forever (#188). Fall through to the
			// raw fallback instead, so the reply surfaces to the operator, who
			// can resume with a corrective instruction.
			log.Printf("warn: planning: wizard uuid=%s PLAN SAVED path %q is off-convention — not marking done", sessionUUID, dir)
		}
	}

	pt := ParseTurn(text)
	if pt.Question != nil {
		s.applyQuestionTurn(row, pt)
		return
	}
	s.applyRawTurn(row, text)
}

// applyQuestionTurn handles a protocol-conforming question turn.
func (s *Service) applyQuestionTurn(row *wizardRow, pt ParsedTurn) {
	qJSON, err := json.Marshal(pt.Question)
	if err != nil {
		log.Printf("error: planning: marshal question uuid=%s: %v", row.uuid, err)
		return
	}

	// Newest history turn — the idempotency anchor.
	var lastTurnID int64
	var lastQ string
	var lastAns sql.NullString
	lerr := s.DB.QueryRow(
		`SELECT id, question, answer FROM planning_turns
		  WHERE planning_session_id=? ORDER BY seq DESC LIMIT 1`, row.id).Scan(&lastTurnID, &lastQ, &lastAns)
	haveLast := lerr == nil
	if lerr != nil && !errors.Is(lerr, sql.ErrNoRows) {
		log.Printf("error: planning: newest turn uuid=%s: %v", row.uuid, lerr)
		return
	}
	var lastQID string
	if haveLast {
		var q PlanningQuestion
		if json.Unmarshal([]byte(lastQ), &q) == nil {
			lastQID = q.ID
		}
	}

	sameAsLast := haveLast && lastQID == pt.Question.ID
	if sameAsLast && lastAns.Valid {
		// The last assistant turn is still the question the operator already
		// answered/refined — the reply turn hasn't landed yet (resume in
		// flight or ingest lag). Regressing generating→awaiting here would
		// re-open a consumed question.
		return
	}
	if sameAsLast && row.status == StatusProceeding {
		// PROCEED dismissed this question without stamping an answer; the
		// stale re-parse must not regress proceeding→awaiting.
		return
	}

	if sameAsLast {
		// Re-ingest refresh: same question id ⇒ update the newest row, no dup.
		if _, err := s.DB.Exec(
			`UPDATE planning_turns SET question=?, reasoning=? WHERE id=?`,
			string(qJSON), pt.Reasoning, lastTurnID); err != nil {
			log.Printf("error: planning: refresh turn uuid=%s: %v", row.uuid, err)
			return
		}
		if row.status == StatusAwaiting && row.currentQuestion.Valid && row.currentQuestion.String == string(qJSON) {
			return // nothing changed — MUST not notify (loop guard, see OnSessionTurns)
		}
	} else {
		if _, err := s.DB.Exec(
			`INSERT INTO planning_turns(planning_session_id, seq, question, reasoning, created_at)
			 VALUES(?, COALESCE((SELECT MAX(seq)+1 FROM planning_turns WHERE planning_session_id=?),1), ?, ?, ?)`,
			row.id, row.id, string(qJSON), pt.Reasoning, s.ts()); err != nil {
			log.Printf("error: planning: insert turn uuid=%s: %v", row.uuid, err)
			return
		}
	}

	// running_plan only moves forward — a question without one keeps the last.
	var rpJSON any // nil keeps existing via COALESCE
	if pt.Question.RunningPlan != nil {
		if b, err := json.Marshal(pt.Question.RunningPlan); err == nil {
			rpJSON = string(b)
		}
	}
	// CAS: only the open statuses the read observed may flip to awaiting — a
	// Cancel/Start racing this write must not be resurrected by a stale row.
	res, err := s.DB.Exec(
		`UPDATE planning_sessions
		    SET current_question=?, running_plan=COALESCE(?, running_plan),
		        raw_reply=NULL, status=?, last_error=NULL, updated_at=?
		  WHERE id=? AND status IN (?, ?, ?)`,
		string(qJSON), rpJSON, StatusAwaiting, s.ts(), row.id,
		StatusGenerating, StatusAwaiting, StatusProceeding)
	if err != nil {
		log.Printf("error: planning: apply question uuid=%s: %v", row.uuid, err)
		return
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return // lost to a concurrent terminal write — never overwrite it
	}
	s.notify(row.projectID)
}

// applyRawTurn handles a turn that failed the protocol parse. While a claude
// process is still alive this is just intermediate narration (research turns
// of the initial run, or the reply being generated) — flipping to the raw
// fallback would surface half-finished prose, so we wait for the exit edge:
// runAndHandle's settle pass / the resume's post-release session_updated
// re-runs OnSessionTurns with the process dead.
func (s *Service) applyRawTurn(row *wizardRow, text string) {
	if s.processAlive(row.projectID, row.uuid) {
		return
	}
	// 'proceeding' with NO live process falls through to the fallback: a
	// PROCEED resume that exited with prose lacking the "PLAN SAVED:" sentinel
	// would otherwise wedge the wizard forever (admitAwaiting 409s every
	// retry) — surfacing the prose as raw_reply puts the operator back in
	// control (they can read the reply and Proceed again).
	if row.status == StatusAwaiting && !row.currentQuestion.Valid &&
		row.rawReply.Valid && row.rawReply.String == text {
		return // idempotent re-ingest — no notify (loop guard)
	}
	// CAS: only the open statuses the read observed may flip to awaiting — a
	// Cancel/Start racing this write must not be resurrected by a stale row.
	res, err := s.DB.Exec(
		`UPDATE planning_sessions SET current_question=NULL, raw_reply=?, status=?,
		        last_error=NULL, updated_at=?
		  WHERE id=? AND status IN (?, ?, ?)`,
		text, StatusAwaiting, s.ts(), row.id,
		StatusGenerating, StatusAwaiting, StatusProceeding)
	if err != nil {
		log.Printf("error: planning: apply raw turn uuid=%s: %v", row.uuid, err)
		return
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return // lost to a concurrent terminal write — never overwrite it
	}
	s.notify(row.projectID)
}

// admitAwaiting loads the project's wizard and enforces the shared admission
// gate of Answer/Refine/Proceed: a row exists and is awaiting_answer.
func (s *Service) admitAwaiting(projectID int64) (*wizardRow, error) {
	row, err := s.newestWizard(projectID)
	if err != nil {
		return nil, err
	}
	if row == nil {
		return nil, ErrNoSession
	}
	if row.status != StatusAwaiting {
		return nil, ErrNotAwaiting
	}
	return row, nil
}

// stampNewestTurn writes the answer JSON onto the newest planning_turns row
// (no-op when the wizard is in raw-fallback mode with no turns yet).
func (s *Service) stampNewestTurn(sessionID int64, ans wizardAnswer) {
	b, err := json.Marshal(ans)
	if err != nil {
		return
	}
	if _, err := s.DB.Exec(
		`UPDATE planning_turns SET answer=?
		  WHERE id = (SELECT id FROM planning_turns WHERE planning_session_id=? ORDER BY seq DESC LIMIT 1)`,
		string(b), sessionID); err != nil {
		log.Printf("error: planning: stamp answer session=%d: %v", sessionID, err)
	}
}

// setStatus CAS-moves the wizard row from the status the caller loaded to the
// target, and notifies the project. Guarded on the expected prior status so a
// concurrent writer that already moved the row wins: (a) a Cancel landing
// between admitAwaiting's read and this write is never overwritten back to
// generating; (b) of two concurrent Answers only one flips — the loser gets
// ErrNotAwaiting (a clean 409 upstream, no spawn, no revert).
// The previous action's failure is cleared here rather than on the next
// successful turn: the operator has retried, so the stale banner must go the
// moment the retry starts, not when it lands.
func (s *Service) setStatus(row *wizardRow, status string) error {
	res, err := s.DB.Exec(
		`UPDATE planning_sessions SET status=?, last_error=NULL, updated_at=? WHERE id=? AND status=?`,
		status, s.ts(), row.id, row.status)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotAwaiting
	}
	s.notify(row.projectID)
	return nil
}

// Answer consumes the current question: validates admission, CAS-flips to
// generating (the consumption point — of two concurrent Answers only one
// wins), stamps the answer JSON on the newest turn, and returns the resume
// text the api layer sends via startResume. Raw-fallback mode (parse failed,
// current_question NULL) requires non-blank free text through otherText with
// an empty questionID and forwards it verbatim.
func (s *Service) Answer(projectID int64, questionID string, selected []string, otherText string) (text, sessionUUID string, err error) {
	row, err := s.admitAwaiting(projectID)
	if err != nil {
		return "", "", err
	}
	var q PlanningQuestion
	raw := !row.currentQuestion.Valid
	if raw {
		// Raw-fallback: there is no structured question to match, and the free
		// text IS the whole resume message — blank would resume with nothing.
		if questionID != "" {
			return "", "", ErrWrongQuestion
		}
		if strings.TrimSpace(otherText) == "" {
			return "", "", ErrEmptyAnswer
		}
	} else {
		if uerr := json.Unmarshal([]byte(row.currentQuestion.String), &q); uerr != nil {
			return "", "", uerr
		}
		if q.ID != questionID {
			return "", "", ErrWrongQuestion
		}
	}
	// CAS flip BEFORE the stamp: a concurrent loser must leave the winner's
	// answer stamp untouched.
	if err := s.setStatus(row, StatusGenerating); err != nil {
		return "", "", err
	}
	if raw {
		text = otherText
	} else {
		s.stampNewestTurn(row.id, wizardAnswer{Kind: "answer", SelectedOptionIDs: selected, OtherText: otherText})
		text = BuildAnswerMessage(q, selected, otherText)
	}
	return text, row.uuid, nil
}

// Refine sends free-form course-correction instructions: same admission as
// Answer (both modes — a raw-fallback wizard is refinable too).
func (s *Service) Refine(projectID int64, instructions string) (text, sessionUUID string, err error) {
	row, err := s.admitAwaiting(projectID)
	if err != nil {
		return "", "", err
	}
	// CAS flip BEFORE the stamp — see Answer.
	if err := s.setStatus(row, StatusGenerating); err != nil {
		return "", "", err
	}
	s.stampNewestTurn(row.id, wizardAnswer{Kind: "refine", Instructions: instructions})
	return BuildRefineMessage(instructions), row.uuid, nil
}

// Proceed ends the interview: flips to proceeding and returns the PHASE B
// trigger. The question is dismissed WITHOUT an answer stamp — see the
// proceeding guards in applyQuestionTurn.
func (s *Service) Proceed(projectID int64) (text, sessionUUID string, err error) {
	row, err := s.admitAwaiting(projectID)
	if err != nil {
		return "", "", err
	}
	if err := s.setStatus(row, StatusProceeding); err != nil {
		return "", "", err
	}
	return BuildProceedMessage(), row.uuid, nil
}

// RevertToAwaiting rolls a generating/proceeding wizard back to
// awaiting_answer — the api layer's onExit error path (failed resume spawn ⇒
// the operator's action is retryable). Keyed by the wizard's session uuid so a
// STALE resume's failure (the operator cancelled and started a new idea while
// a 15-min resume was in flight) can only touch its own row, never the
// project's newer wizard. Terminal states are never revived (status guard).
//
// reason is stamped on the row and surfaced next to the question. It is what
// distinguishes "your answer did not go through" from "the planner asked again",
// which are otherwise the same awaiting_answer + same current_question state —
// the shape that let a broken resume look like an endless interview. A blank
// reason is refused rather than stored: a rollback with no explanation is the
// bug this column exists to prevent.
func (s *Service) RevertToAwaiting(sessionUUID, reason string) {
	if strings.TrimSpace(reason) == "" {
		reason = "the planner run failed for an unrecorded reason — retry your answer"
	}
	res, err := s.DB.Exec(
		`UPDATE planning_sessions SET status=?, last_error=?, updated_at=?
		  WHERE session_uuid=? AND status IN (?, ?)`,
		StatusAwaiting, reason, s.ts(), sessionUUID, StatusGenerating, StatusProceeding)
	if err != nil {
		log.Printf("error: planning: revert uuid=%s: %v", sessionUUID, err)
		return
	}
	if n, _ := res.RowsAffected(); n > 0 {
		var projectID int64
		if err := s.DB.QueryRow(
			`SELECT project_id FROM planning_sessions WHERE session_uuid=?`, sessionUUID).Scan(&projectID); err == nil {
			s.notify(projectID)
		}
	}
}

// markFailed stamps a wizard failed after a runner failure — guarded on
// status='generating' so it can never overwrite cancelled/done.
func (s *Service) markFailed(sessionUUID string) {
	res, err := s.DB.Exec(
		`UPDATE planning_sessions SET status=?, updated_at=? WHERE session_uuid=? AND status=?`,
		StatusFailed, s.ts(), sessionUUID, StatusGenerating)
	if err != nil {
		log.Printf("error: planning: mark failed uuid=%s: %v", sessionUUID, err)
		return
	}
	if n, _ := res.RowsAffected(); n > 0 {
		log.Printf("planning: wizard uuid=%s marked failed", sessionUUID)
	}
}

// markCancelled stamps every open wizard row of a project cancelled (Cancel
// and the Start supersede path). Returns whether any row moved.
func (s *Service) markCancelled(projectID int64) bool {
	res, err := s.DB.Exec(
		`UPDATE planning_sessions SET status=?, updated_at=?
		  WHERE project_id=? AND status IN (?, ?, ?)`,
		StatusCancelled, s.ts(), projectID, StatusGenerating, StatusAwaiting, StatusProceeding)
	if err != nil {
		log.Printf("error: planning: mark cancelled project=%d: %v", projectID, err)
		return false
	}
	n, _ := res.RowsAffected()
	return n > 0
}

// WizardSnapshot builds the extended DTO for a project: the newest
// planning_sessions row joined with its turns, plus the legacy in-memory
// fields. Also the daemon-restart heal: a 'generating' row older than the
// resume window with no live process flips to awaiting_answer here, on read.
func (s *Service) WizardSnapshot(projectID int64) (WizardStatus, error) {
	row, err := s.newestWizard(projectID)
	if err != nil {
		return WizardStatus{}, err
	}
	if row == nil {
		// Legacy idle shape — pre-phase-2 daemons had no rows; the in-memory
		// snapshot still covers a run mid-upgrade.
		legacy := s.Snapshot(projectID)
		return WizardStatus{
			Active:      legacy.Active,
			SessionUUID: legacy.SessionUUID,
			SessionID:   legacy.SessionID,
			StartedAt:   legacy.StartedAt,
			History:     []WizardTurn{},
		}, nil
	}

	alive := s.processAlive(projectID, row.uuid)
	if (row.status == StatusGenerating || row.status == StatusProceeding) && !alive {
		if t, perr := time.Parse(time.RFC3339, row.updatedAt); perr == nil && s.clock().Sub(t) > staleWindow {
			// Stamped like a rollback, because that is what it is: the operator's
			// action produced no reply and the wizard is answerable again. Without
			// the reason the reconcile is the silent path that reads as a repeated
			// question.
			const reason = "the planner process ended without a reply — retry your answer"
			if _, uerr := s.DB.Exec(
				`UPDATE planning_sessions SET status=?, last_error=?, updated_at=? WHERE id=? AND status=?`,
				StatusAwaiting, reason, s.ts(), row.id, row.status); uerr == nil {
				log.Printf("planning: wizard uuid=%s stale %s — reconciled to awaiting_answer", row.uuid, row.status)
				row.status = StatusAwaiting
				row.lastError = sql.NullString{String: reason, Valid: true}
			}
		}
	}

	startedAt := row.createdAt
	st := WizardStatus{
		Active:      alive,
		SessionUUID: row.uuid,
		StartedAt:   &startedAt,
		Status:      row.status,
		Mode:        row.mode,
		Model:       DefaultModel,
		History:     []WizardTurn{},
	}
	if row.model.Valid && row.model.String != "" {
		st.Model = row.model.String
	}
	if row.reviseTaskID.Valid {
		v := row.reviseTaskID.Int64
		st.ReviseTaskId = &v
	}
	var sid int64
	if err := s.DB.QueryRow(`SELECT id FROM sessions WHERE session_uuid = ?`, row.uuid).Scan(&sid); err == nil {
		st.SessionID = &sid
	} else if !errors.Is(err, sql.ErrNoRows) {
		return WizardStatus{}, err
	}
	if row.currentQuestion.Valid {
		var q PlanningQuestion
		if json.Unmarshal([]byte(row.currentQuestion.String), &q) == nil {
			st.CurrentQuestion = &q
		}
	}
	if row.runningPlan.Valid {
		var p PlanningSummary
		if json.Unmarshal([]byte(row.runningPlan.String), &p) == nil {
			st.RunningPlan = &p
		}
	}
	if row.rawReply.Valid {
		st.RawReply = &row.rawReply.String
	}
	if row.planDir.Valid {
		st.PlanDir = &row.planDir.String
	}
	if row.lastError.Valid && strings.TrimSpace(row.lastError.String) != "" {
		st.LastError = &row.lastError.String
	}

	rows, err := s.DB.Query(
		`SELECT seq, question, answer, reasoning FROM planning_turns
		  WHERE planning_session_id=? ORDER BY seq ASC`, row.id)
	if err != nil {
		return WizardStatus{}, err
	}
	defer rows.Close()
	for rows.Next() {
		var (
			turn      WizardTurn
			qJSON     string
			ans       sql.NullString
			reasoning sql.NullString
		)
		if err := rows.Scan(&turn.Seq, &qJSON, &ans, &reasoning); err != nil {
			return WizardStatus{}, err
		}
		var q PlanningQuestion
		if json.Unmarshal([]byte(qJSON), &q) == nil {
			turn.Question = &q
		}
		if ans.Valid {
			turn.Answer = json.RawMessage(ans.String)
		}
		turn.Reasoning = reasoning.String
		st.History = append(st.History, turn)
	}
	return st, rows.Err()
}
