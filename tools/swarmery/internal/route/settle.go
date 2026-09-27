package route

import (
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Settle fills the outcome columns of route_decisions from what the run's own
// tables already say — pull, not hooks. A board card, a verification and a
// phase run each end on a different path (dispatch finish*, verify finishRun,
// actuals OnRecorded); a hook on each would be three places to miss, and a
// missed one is a NULL forever. One idempotent query per surface cannot miss.
//
// The outcome vocabulary, per surface:
//
//	dispatch  done | failed | blocked | review | cancelled — read from the card:
//	          tasks has no single "state" column, so the terminal set is derived
//	          from status, board_column, paused and dispatch_error exactly as
//	          internal/dispatch's finishDone / finishReview / finishBlocked leave
//	          them (a review with a dispatch_error is a failed run: a non-zero
//	          exit, a timeout or a start failure).
//	phaserun  phase_actuals.outcome as-is (phasediag: completed | partial | noop |
//	          failed …), falling back to its run_state when the outcome is blank.
//	both      superseded — a later run of the same card/phase replaced this one
//	          before it was seen terminal; deleted — the card/phase is gone.
//
// superseded and deleted are terminal (the row can never learn more about the
// outcome), which is what keeps them from clogging the batch below forever.
//
// NULL STAYS UNKNOWN. A non-terminal run leaves the row NULL; a cost nobody
// measured stays NULL, never 0.
//
// REFRESH WINDOW. A card reaches in_review BEFORE its verification runs, and
// its transcript may still be ingesting; settling it once would freeze
// verify_status and cost_usd at NULL. So a row settled within SettleRefresh is
// re-read on every call, and a later value fills in (a known value is never
// replaced by NULL, and superseded/deleted never overwrite a concrete outcome).
// outcome_at keeps the FIRST settle time, so the window closes on schedule.

// Outcomes Settle writes beyond the surfaces' own vocabularies.
const (
	OutcomeDone       = "done"
	OutcomeFailed     = "failed"
	OutcomeBlocked    = "blocked"
	OutcomeReview     = "review"
	OutcomeCancelled  = "cancelled"
	OutcomeSuperseded = "superseded"
	OutcomeDeleted    = "deleted"
)

// SettleBatch bounds each of the two reads one Settle call makes — unsettled
// rows and refresh-window rows — newest first, so one call touches at most
// 2×SettleBatch rows. The reads are separate on purpose: under one shared LIMIT
// the unsettled rows sort first, and rows that never settle (a phase run whose
// actuals were never recorded, a card requeued and never re-dispatched) would
// eventually fill it and starve every refresh — late verdicts and costs would
// then never land.
const SettleBatch = 500

// SettleRefresh is how long a settled row keeps being re-read.
const SettleRefresh = 7 * 24 * time.Hour

// settleRow is one candidate: its identity plus what it holds today.
type settleRow struct {
	id               int64
	surface, subject string
	sessionUUID      string
	createdAt        string
	outcome, verify  sql.NullString
	cost             sql.NullFloat64
}

// settled is what the run's tables say now. outcome "" = not terminal yet.
type settled struct {
	outcome string
	verify  sql.NullString
	cost    sql.NullFloat64
}

// Settle settles up to SettleBatch unsettled rows, refreshes up to SettleBatch
// refresh-window rows, and returns how many it changed.
func Settle(db *sql.DB) (int, error) { return settleAt(db, time.Now()) }

func settleAt(db *sql.DB, now time.Time) (int, error) {
	cands, err := settleCandidates(db, now)
	if err != nil {
		return 0, err
	}
	stamp := now.UTC().Format(createdAtFormat)
	n := 0
	for _, r := range cands {
		s, ok, err := readOutcome(db, r)
		if err != nil {
			return n, err
		}
		if !ok {
			continue
		}
		changed, err := applySettled(db, r, s, stamp)
		if err != nil {
			return n, err
		}
		if changed {
			n++
		}
	}
	return n, nil
}

// settleCandidates reads the whole batch up front — unsettled rows, then
// refresh-window rows, each under its own SettleBatch bound (see SettleBatch).
// The store runs on ONE connection, so no other query may run while rows are
// open: each read is drained and closed before the next one and before any row
// is processed.
func settleCandidates(db *sql.DB, now time.Time) ([]settleRow, error) {
	cutoff := now.Add(-SettleRefresh).UTC().Format(createdAtFormat)
	unsettled, err := readCandidates(db, `outcome IS NULL`)
	if err != nil {
		return nil, err
	}
	refresh, err := readCandidates(db, `outcome IS NOT NULL AND outcome_at >= ?`, cutoff)
	if err != nil {
		return nil, err
	}
	return append(unsettled, refresh...), nil
}

// readCandidates runs one bounded, newest-first read of route_decisions and
// drains it. where is one of settleCandidates' two fixed predicates.
func readCandidates(db *sql.DB, where string, args ...any) ([]settleRow, error) {
	rows, err := db.Query(`
		SELECT id, surface, subject, session_uuid, created_at, outcome, verify_status, cost_usd
		  FROM route_decisions
		 WHERE `+where+`
		 ORDER BY id DESC
		 LIMIT ?`, append(args, SettleBatch)...)
	if err != nil {
		return nil, fmt.Errorf("route: settle candidates: %w", err)
	}
	defer rows.Close()
	var out []settleRow
	for rows.Next() {
		var r settleRow
		if err := rows.Scan(&r.id, &r.surface, &r.subject, &r.sessionUUID, &r.createdAt,
			&r.outcome, &r.verify, &r.cost); err != nil {
			return nil, fmt.Errorf("route: settle candidates: %w", err)
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// subjectID parses "<kind>:<id>" for the expected kind.
func subjectID(subject, kind string) (int64, bool) {
	rest, ok := strings.CutPrefix(subject, kind+":")
	if !ok {
		return 0, false
	}
	id, err := strconv.ParseInt(rest, 10, 64)
	return id, err == nil && id > 0
}

// readOutcome dispatches on the surface. ok=false: the row is malformed for its
// surface and is skipped rather than guessed at.
func readOutcome(db *sql.DB, r settleRow) (settled, bool, error) {
	switch Surface(r.surface) {
	case SurfaceDispatch:
		id, ok := subjectID(r.subject, "task")
		if !ok {
			return settled{}, false, nil
		}
		s, err := dispatchOutcome(db, r, id)
		return s, true, err
	case SurfacePhaseRun:
		id, ok := subjectID(r.subject, "phase")
		if !ok {
			return settled{}, false, nil
		}
		s, err := phaseOutcome(db, r, id)
		return s, true, err
	}
	return settled{}, false, nil
}

// cardOutcome maps a board card's columns onto the dispatch vocabulary; ""
// means the card is still in flight (or requeued, which a later run settles).
func cardOutcome(status, column string, paused bool, dispatchErr string) string {
	switch {
	case status == "done" || column == "done" || column == "archived":
		return OutcomeDone
	case status == "failed":
		return OutcomeFailed
	case status == "cancelled":
		return OutcomeCancelled
	case status == "needs_review" || column == "in_review":
		if strings.TrimSpace(dispatchErr) != "" {
			return OutcomeFailed
		}
		return OutcomeReview
	case paused && strings.TrimSpace(dispatchErr) != "":
		return OutcomeBlocked
	}
	return ""
}

func dispatchOutcome(db *sql.DB, r settleRow, taskID int64) (settled, error) {
	var (
		s                           settled
		status, column, errMsg, cur string
		paused                      int
	)
	err := db.QueryRow(`
		SELECT status, board_column, paused, COALESCE(dispatch_error, ''), COALESCE(dispatch_session_uuid, '')
		  FROM tasks WHERE id = ?`, taskID).Scan(&status, &column, &paused, &errMsg, &cur)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		s.outcome = OutcomeDeleted
	case err != nil:
		return s, fmt.Errorf("route: settle %s: %w", r.subject, err)
	case r.sessionUUID != "" && cur != r.sessionUUID:
		// dispatch_session_uuid is set once per admission, so a different one
		// means a later run of this card replaced the one this row describes.
		s.outcome = OutcomeSuperseded
	default:
		s.outcome = cardOutcome(status, column, paused != 0, errMsg)
	}
	end, known, err := runEnd(db, r, cur, s.outcome == OutcomeSuperseded)
	if err != nil || !known {
		// !known: a successor run exists but its start is not ingested yet, so
		// nothing after created_at can be attributed to THIS run. Verdict and cost
		// stay unknown; the refresh window fills them once the successor lands.
		return s, err
	}
	if s.verify, err = runVerdict(db, r, end); err != nil {
		return s, err
	}
	s.cost, err = runCost(db, r, taskID, end)
	return s, err
}

// runEnd is where this run's window closes: the created_at of the subject's
// next route_decisions row, or "" (open-ended) when there is none.
//
// A superseded run with no next row was replaced by a run that left no row
// (routing mode was off for it). An open window would then attribute the
// successor's verdict and sessions to this run, so the window closes at the
// successor's own first session start instead — cur is its uuid. known=false
// when that session is not ingested yet. cur == "" means no successor has been
// admitted, so the window stays open.
func runEnd(db *sql.DB, r settleRow, cur string, superseded bool) (end string, known bool, err error) {
	err = db.QueryRow(`SELECT created_at FROM route_decisions WHERE subject = ? AND id > ? ORDER BY id LIMIT 1`,
		r.subject, r.id).Scan(&end)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return "", false, fmt.Errorf("route: settle %s next run: %w", r.subject, err)
	}
	if end != "" || !superseded || cur == "" {
		return end, true, nil
	}
	err = db.QueryRow(`SELECT started_at FROM sessions WHERE session_uuid = ?`, cur).Scan(&end)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return "", false, nil
	case err != nil:
		return "", false, fmt.Errorf("route: settle %s successor start: %w", r.subject, err)
	}
	return end, true, nil
}

// runVerdict is the latest terminal verification of THIS run: on the row's
// target, started at or after the row and before end ("" = open-ended).
// julianday() compares the two tables' differently-precise timestamps.
func runVerdict(db *sql.DB, r settleRow, end string) (sql.NullString, error) {
	var v sql.NullString
	err := db.QueryRow(`
		SELECT status FROM verification_runs
		 WHERE target_key = ? AND status IN ('pass','fail','inconclusive')
		   AND julianday(started_at) >= julianday(?)
		   AND (? = '' OR julianday(started_at) < julianday(?))
		 ORDER BY id DESC LIMIT 1`, r.subject, r.createdAt, end, end).Scan(&v)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return sql.NullString{}, fmt.Errorf("route: settle %s verdict: %w", r.subject, err)
	}
	return v, nil
}

// runCost is SUM(turns.cost_usd) over every session of THIS run: the row's own
// session, plus every session explicitly linked to the card (task_sessions,
// which dispatch writes for each playbook stage — stages 2+ mint their own
// uuids) whose start falls in [created_at, end). Heuristic links are guesses
// from cwd/path overlap and are not counted. NULL when no session is ingested
// or no turn carries a cost: unknown, never 0.
func runCost(db *sql.DB, r settleRow, taskID int64, end string) (sql.NullFloat64, error) {
	var c sql.NullFloat64
	err := db.QueryRow(`
		SELECT SUM(tu.cost_usd) FROM turns tu JOIN sessions s ON s.id = tu.session_id
		 WHERE (? <> '' AND s.session_uuid = ?)
		    OR (s.id IN (SELECT session_id FROM task_sessions WHERE task_id = ? AND link_source = 'explicit')
		        AND julianday(s.started_at) >= julianday(?)
		        AND (? = '' OR julianday(s.started_at) < julianday(?)))`,
		r.sessionUUID, r.sessionUUID, taskID, r.createdAt, end, end).Scan(&c)
	if err != nil {
		return c, fmt.Errorf("route: settle %s cost: %w", r.subject, err)
	}
	return c, nil
}

func phaseOutcome(db *sql.DB, r settleRow, phaseID int64) (settled, error) {
	var (
		s                 settled
		outcome, runState string
	)
	err := db.QueryRow(`SELECT outcome, run_state, verify_verdict, cost_usd FROM phase_actuals WHERE session_uuid = ?`,
		r.sessionUUID).Scan(&outcome, &runState, &s.verify, &s.cost)
	if err == nil {
		s.outcome = outcome
		if strings.TrimSpace(s.outcome) == "" {
			s.outcome = runState
		}
		return s, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return s, fmt.Errorf("route: settle %s actuals: %w", r.subject, err)
	}
	// No actuals yet. Terminal only if the phase is gone or has moved on to a
	// later run; otherwise the recorder may still write them.
	var cur sql.NullString
	err = db.QueryRow(`SELECT run_session_uuid FROM epic_phases WHERE id = ?`, phaseID).Scan(&cur)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		s.outcome = OutcomeDeleted
	case err != nil:
		return s, fmt.Errorf("route: settle %s phase: %w", r.subject, err)
	case r.sessionUUID != "" && cur.String != r.sessionUUID:
		s.outcome = OutcomeSuperseded
	}
	return s, nil
}

// noOutcome names the outcomes that say nothing about how the run went.
func noOutcome(o string) bool { return o == OutcomeSuperseded || o == OutcomeDeleted }

// applySettled writes s over r when it is terminal and says something new.
func applySettled(db *sql.DB, r settleRow, s settled, stamp string) (bool, error) {
	if s.outcome == "" {
		return false, nil // still running — or requeued; never un-settle a row
	}
	if !r.outcome.Valid {
		_, err := db.Exec(`UPDATE route_decisions SET outcome = ?, verify_status = ?, cost_usd = ?, outcome_at = ?
			WHERE id = ? AND outcome IS NULL`, s.outcome, s.verify, s.cost, stamp, r.id)
		if err != nil {
			return false, fmt.Errorf("route: settle row %d: %w", r.id, err)
		}
		return true, nil
	}
	// Refresh: fill what was missing, keep what was known.
	outcome := s.outcome
	if noOutcome(outcome) && !noOutcome(r.outcome.String) {
		outcome = r.outcome.String
	}
	verify, cost := s.verify, s.cost
	if !verify.Valid {
		verify = r.verify
	}
	if !cost.Valid {
		cost = r.cost
	}
	if outcome == r.outcome.String && verify == r.verify && cost == r.cost {
		return false, nil
	}
	if _, err := db.Exec(`UPDATE route_decisions SET outcome = ?, verify_status = ?, cost_usd = ? WHERE id = ?`,
		outcome, verify, cost, r.id); err != nil {
		return false, fmt.Errorf("route: refresh row %d: %w", r.id, err)
	}
	return true, nil
}
