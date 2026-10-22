// Package stall flags a plan phase that claims to be running while nobody is
// working on it (verify-not-trust phase 11; D15, D34, D35, D41).
//
// For every phase with a stage in flight it resolves the ACTIVE stage — the
// executor while run_state is running, else the reviewer whose verdict is not
// recorded yet, else the verifier whose verification_runs row is still running —
// and that stage's session, then applies three rules:
//
//   - permission-pending: a permission request of the stage session has waited
//     longer than stallMinutes (D34);
//   - ended: the stage session is over but left nothing behind — the executor no
//     Completion Report on a run that never settled, the reviewer no verdict, the
//     verifier no finished verification record;
//   - no-turns: the session is open but has produced no turn for stallMinutes
//     (a `**Playbook:**` header naming a playbookOverrides entry stretches this
//     one, e.g. long-tests).
//
// Derived, never persisted: Detect writes nothing. The only table this package
// writes is the dedupe ledger stall_notices (ledger.go), which turns a standing
// stall into exactly one phase_stalled webhook. Notification only — the detector
// never restarts, cancels or kills anything.
package stall

import (
	"database/sql"
	"fmt"
	"time"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/guards"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/playbooks"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/staleness"
)

// Stage names which session of a phase run is stalled.
type Stage string

const (
	StageExecutor Stage = "executor"
	StageReviewer Stage = "reviewer"
	StageVerifier Stage = "verifier"
)

// Reason is why a stage counts as stalled. The values are the ledger's dedupe
// key and the wire form (epic payload, Needs-you, webhook).
type Reason string

const (
	ReasonNoTurns           Reason = "no-turns"
	ReasonEnded             Reason = "ended"
	ReasonPermissionPending Reason = "permission-pending"
)

// DefaultEndedGrace is how long a stage session must have been quiet before
// "ended" counts. A phase run resumes its own session between continuations
// (runcore.MaxContinuations), and procwatch may mark the exited process dead
// for the few seconds before the resume — a stall in that window is noise.
const DefaultEndedGrace = 2 * time.Minute

// Config is what Detect may depend on besides the database.
type Config struct {
	// Guards carries stallMinutes and playbookOverrides (config/guards.json).
	Guards guards.Config
	// EndedGrace overrides DefaultEndedGrace when > 0.
	EndedGrace time.Duration
}

func (c Config) endedGrace() time.Duration {
	if c.EndedGrace > 0 {
		return c.EndedGrace
	}
	return DefaultEndedGrace
}

// Stalled is one phase whose active stage is stalled.
type Stalled struct {
	PhaseID   int64
	TaskID    int64 // epic_phases.workspace_task_id
	PhaseSeq  int
	PhaseName string
	PlanTitle string
	Project   string // projects.slug; "" when the plan's task is gone
	Stage     Stage
	// SessionUUID is the stage's session; "" while the stage has not linked one
	// (a verifier before its spawn parked the uuid).
	SessionUUID string
	Reason      Reason
	// Since is when the stalled condition began: the last sign of life for
	// no-turns and ended, the request time for permission-pending.
	Since time.Time
	// RequestID is the permission_requests.id behind permission-pending, 0 otherwise.
	RequestID int64
	// Playbook is the phase doc's `**Playbook:**`, "" when it names none.
	Playbook string
}

// Describe is a stall's reason in the operator's words — the webhook body and the
// Needs-you preview. The dashboard renders its own localized copy from the same
// stage/reason/minutes.
func Describe(stage Stage, reason Reason, minutes int) string {
	switch reason {
	case ReasonNoTurns:
		return fmt.Sprintf("no new turn for %d min", minutes)
	case ReasonPermissionPending:
		return fmt.Sprintf("a permission request has waited %d min", minutes)
	case ReasonEnded:
		left := "its Completion Report"
		switch stage {
		case StageReviewer:
			left = "a verdict"
		case StageVerifier:
			left = "a verification record"
		}
		return fmt.Sprintf("the session ended without %s (%d min ago)", left, minutes)
	}
	return fmt.Sprintf("%s for %d min", reason, minutes)
}

// Minutes is how long the condition has held at now, in whole minutes.
func (s Stalled) Minutes(now time.Time) int {
	if now.Before(s.Since) {
		return 0
	}
	return int(now.Sub(s.Since) / time.Minute)
}

// candidatesQuery lists every phase with a stage that may be in flight. The
// reviewer leg is narrowed further in Go (reviewerActive): its stamp must belong
// to the current run, which needs parsed timestamps rather than a text compare.
// The verifier join yields at most one row per phase — idx_verification_running
// allows one running row per target.
const candidatesQuery = `
SELECT e.id, e.workspace_task_id, e.seq, e.name, e.doc_path, e.run_state,
       COALESCE(e.run_session_uuid, ''), COALESCE(e.run_started_at, ''), COALESCE(e.run_ended_at, ''),
       COALESCE(e.completion_report, ''),
       COALESCE(e.review_session_uuid, ''), COALESCE(e.review_started_at, ''),
       CASE WHEN COALESCE(e.review_session_uuid, '') <> '' AND NOT EXISTS
                 (SELECT 1 FROM phase_reviews r WHERE r.session_uuid = e.review_session_uuid)
            THEN 1 ELSE 0 END,
       COALESCE(v.id, 0), COALESCE(v.verify_session_uuid, ''), COALESCE(v.started_at, ''),
       COALESCE(t.title, ''), COALESCE(p.slug, '')
  FROM epic_phases e
  LEFT JOIN verification_runs v ON v.target_key = 'phase:' || e.id AND v.status = 'running'
  LEFT JOIN tasks t    ON t.id = e.workspace_task_id
  LEFT JOIN projects p ON p.id = t.project_id
 WHERE e.run_state = 'running'
    OR (COALESCE(e.review_session_uuid, '') <> '' AND NOT EXISTS
          (SELECT 1 FROM phase_reviews r WHERE r.session_uuid = e.review_session_uuid))
    OR v.id IS NOT NULL
 ORDER BY e.id`

// candidate is one row of candidatesQuery.
type candidate struct {
	phaseID, taskID                       int64
	seq                                   int
	name, docPath, runState               string
	runUUID, runStarted, runEnded, report string
	reviewUUID, reviewStarted             string
	reviewOpen                            bool
	verifyID                              int64
	verifyUUID, verifyStarted             string
	planTitle, project                    string
}

// active is the stage a candidate is in, with the session and the instant the
// stage started (the sign of life used while its session has none of its own).
type active struct {
	stage   Stage
	uuid    string
	started time.Time
}

// activeStage resolves the stage in flight: executor → reviewer → verifier. ok is
// false when nothing is (a reviewer stamp left over from an earlier run).
func (c candidate) activeStage() (active, bool) {
	switch {
	case c.runState == "running":
		return active{StageExecutor, c.runUUID, parseTS(c.runStarted)}, true
	case c.reviewerActive():
		return active{StageReviewer, c.reviewUUID, parseTS(c.reviewStarted)}, true
	case c.verifyID != 0:
		return active{StageVerifier, c.verifyUUID, parseTS(c.verifyStarted)}, true
	}
	return active{}, false
}

// reviewerActive: a reviewer session is parked, no phase_reviews row carries it,
// and it started after the phase's current run ended. A stamp older than the run
// belongs to a previous run whose review never recorded (a daemon restart
// mid-review) — the run that followed superseded it.
func (c candidate) reviewerActive() bool {
	if !c.reviewOpen {
		return false
	}
	runEdge := parseTS(c.runEnded)
	if runEdge.IsZero() {
		runEdge = parseTS(c.runStarted)
	}
	started := parseTS(c.reviewStarted)
	return !started.IsZero() && !started.Before(runEdge)
}

// session is what Detect reads about a stage session.
type session struct {
	found          bool
	id             int64
	ended          bool
	lastActivity   time.Time // newest turn, else the session's start
	pendingRequest int64     // oldest pending permission request, 0 when none
	pendingSince   time.Time
}

// Detect derives every stalled phase at now. Read-only.
//
// The store runs on a single connection: the candidates cursor is drained and
// closed before any per-stage query runs, never nested.
func Detect(db *sql.DB, now time.Time, cfg Config) ([]Stalled, error) {
	cands, err := loadCandidates(db)
	if err != nil {
		return nil, err
	}
	var out []Stalled
	for _, c := range cands {
		a, ok := c.activeStage()
		if !ok {
			continue
		}
		sess, err := loadSession(db, a.uuid)
		if err != nil {
			return nil, err
		}
		playbook := playbooks.PhasePlaybook(c.docPath)
		st, stalled := evaluate(c, a, sess, playbook, now, cfg)
		if !stalled {
			continue
		}
		out = append(out, st)
	}
	return out, nil
}

// evaluate applies the three rules to one active stage. Rule order is
// load-bearing:
//   - permission-pending first: a pending request is the operator's wait, so
//     while one is younger than stallMinutes the stage is NOT stalled, however
//     quiet its transcript is — the approval item already names the blocker;
//   - ended before no-turns: a session that is over is not "silent", and the
//     more specific reason is the one worth a webhook;
//   - no-turns last, the only rule a `**Playbook:**` override stretches.
func evaluate(c candidate, a active, s session, playbook string, now time.Time, cfg Config) (Stalled, bool) {
	st := Stalled{
		PhaseID: c.phaseID, TaskID: c.taskID, PhaseSeq: c.seq, PhaseName: c.name,
		PlanTitle: c.planTitle, Project: c.project,
		Stage: a.stage, SessionUUID: a.uuid, Playbook: playbook,
	}
	base := time.Duration(cfg.Guards.StallMinutes) * time.Minute
	silence := time.Duration(cfg.Guards.StallMinutesFor(playbook)) * time.Minute

	if s.pendingRequest != 0 {
		if now.Sub(s.pendingSince) < base {
			return Stalled{}, false
		}
		st.Reason, st.Since, st.RequestID = ReasonPermissionPending, s.pendingSince, s.pendingRequest
		return st, true
	}

	last := a.started
	if s.lastActivity.After(last) {
		last = s.lastActivity
	}
	if last.IsZero() {
		// No clock at all (a row with no run_started_at and no transcript): no
		// evidence either way, and "unknown" must not read as "stalled".
		return Stalled{}, false
	}
	quiet := now.Sub(last)

	if s.found && s.ended {
		if quiet >= cfg.endedGrace() && leftNothing(a.stage, c) {
			st.Reason, st.Since = ReasonEnded, last
			return st, true
		}
		// An ended session that delivered (an executor with its report written)
		// is the daemon's to settle, not a stall of the stage.
		return Stalled{}, false
	}

	if quiet >= silence {
		st.Reason, st.Since = ReasonNoTurns, last
		return st, true
	}
	return Stalled{}, false
}

// leftNothing is the "ended without …" half of the ended rule. For the reviewer
// and the verifier the candidate query already proves it — the stage is active
// only while its verdict row is missing or its verification row still running.
// The executor's is the Completion Report: a run that ended with the report
// written is waiting on the daemon's settle, not stalled.
func leftNothing(stage Stage, c candidate) bool {
	if stage == StageExecutor {
		return c.report == ""
	}
	return true
}

func loadCandidates(db *sql.DB) ([]candidate, error) {
	rows, err := db.Query(candidatesQuery)
	if err != nil {
		return nil, fmt.Errorf("stall: candidates: %w", err)
	}
	defer rows.Close()
	var out []candidate
	for rows.Next() {
		var c candidate
		var reviewOpen int
		if err := rows.Scan(&c.phaseID, &c.taskID, &c.seq, &c.name, &c.docPath, &c.runState,
			&c.runUUID, &c.runStarted, &c.runEnded, &c.report,
			&c.reviewUUID, &c.reviewStarted, &reviewOpen,
			&c.verifyID, &c.verifyUUID, &c.verifyStarted,
			&c.planTitle, &c.project); err != nil {
			return nil, fmt.Errorf("stall: scan candidate: %w", err)
		}
		c.reviewOpen = reviewOpen == 1
		out = append(out, c)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("stall: candidates: %w", err)
	}
	return out, nil
}

// loadSession reads the stage session's liveness, its newest turn and its oldest
// pending permission request. A uuid with no sessions row (not ingested yet, or
// "" before the stage parked one) is found=false: the stage's own start is then
// the only sign of life.
//
// Any turn counts as a sign of life, a tool result as much as an assistant
// message: a turn's ended_at moves with every record of it, so a long tool call
// shows up as one quiet stretch, which is what the long-tests override is for.
func loadSession(db *sql.DB, uuid string) (session, error) {
	var s session
	if uuid == "" {
		return s, nil
	}
	var status, procState, started, lastTurn string
	err := db.QueryRow(`
		SELECT s.id, s.status, COALESCE(s.proc_state, ''), COALESCE(s.started_at, ''),
		       COALESCE((SELECT MAX(COALESCE(t.ended_at, t.started_at)) FROM turns t WHERE t.session_id = s.id), '')
		  FROM sessions s WHERE s.session_uuid = ?`, uuid).Scan(&s.id, &status, &procState, &started, &lastTurn)
	switch {
	case err == sql.ErrNoRows:
		return s, nil
	case err != nil:
		return s, fmt.Errorf("stall: session %s: %w", uuid, err)
	}
	s.found = true
	s.ended = staleness.SessionEnded(status, procState)
	s.lastActivity = parseTS(lastTurn)
	if s.lastActivity.IsZero() {
		s.lastActivity = parseTS(started)
	}

	var requested string
	err = db.QueryRow(`
		SELECT id, requested_at FROM permission_requests
		 WHERE session_id = ? AND status = 'pending'
		 ORDER BY requested_at, id LIMIT 1`, s.id).Scan(&s.pendingRequest, &requested)
	switch {
	case err == sql.ErrNoRows:
		s.pendingRequest = 0
	case err != nil:
		return s, fmt.Errorf("stall: permission requests of %s: %w", uuid, err)
	default:
		s.pendingSince = parseTS(requested)
		if s.pendingSince.IsZero() {
			// An unparseable request time cannot age; treat it as no request
			// rather than as one pending forever.
			s.pendingRequest = 0
		}
	}
	return s, nil
}

// parseTS parses the store's timestamps (RFC3339, with or without fractional
// seconds); zero when the value is empty or does not parse.
func parseTS(v string) time.Time {
	if v == "" {
		return time.Time{}
	}
	t, err := time.Parse(time.RFC3339Nano, v)
	if err != nil {
		return time.Time{}
	}
	return t
}
