package runcore

import (
	"database/sql"
	"log"
)

// Run-event kinds (migration 0077). The vocabulary is CLOSED and mirrors the
// EndState decisions the completion loop can reach, plus the resume it performs
// to get there.
const (
	// EventContinuation: the run exited cleanly with criteria unticked and no
	// blocked line, so the engine resumed the SAME session. detail carries the
	// message it was resumed with; attempt is 1..MaxContinuations.
	EventContinuation = "continuation"
	// EventBlocked: the run ended with a blocked sentinel. detail is the reason.
	EventBlocked = "blocked"
	// EventPartial: continuations are exhausted and criteria are still unticked.
	EventPartial = "partial"
	// EventDone: every criterion is ticked. Recorded too, so a run's timeline
	// reads end to end instead of starting only when something went sideways.
	EventDone = "done"
)

// RecordRunEvent appends one decision of the completion loop to run_events.
//
// Best-effort by design, like every other observability write on the run path
// (recordDispatchedPrompt, LinkSession): the event EXPLAINS a state transition,
// it does not cause one, and failing to write the explanation must never stop
// the run from reaching its state. A failure is logged loudly rather than
// returned, because a silently missing timeline is indistinguishable from a run
// that simply never needed a continuation.
func RecordRunEvent(db *sql.DB, engine string, subjectID int64, sessionUUID, kind string, attempt int, detail, ts string) {
	if db == nil {
		return
	}
	if _, err := db.Exec(`
		INSERT INTO run_events (engine, subject_id, session_uuid, kind, attempt, detail, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)`,
		engine, subjectID, sessionUUID, kind, attempt, detail, ts); err != nil {
		log.Printf("error: %s: run event %s for subject=%d not recorded: %v", engine, kind, subjectID, err)
	}
}

// RunEvent is one row, for the API layer.
type RunEvent struct {
	ID          int64  `json:"id"`
	SessionUUID string `json:"sessionUuid"`
	Kind        string `json:"kind"`
	Attempt     int    `json:"attempt"`
	Detail      string `json:"detail"`
	CreatedAt   string `json:"createdAt"`
}

// RunEvents reads one run's timeline, oldest first. A read error yields nil —
// the timeline is decoration on a panel whose primary facts (run_state,
// run_error, the checkbox counts) come from the run's own row, so a failure here
// must degrade to "no events shown", never to a failed request.
func RunEvents(db *sql.DB, engine string, subjectID int64) []RunEvent {
	if db == nil {
		return nil
	}
	rows, err := db.Query(`
		SELECT id, session_uuid, kind, attempt, detail, created_at
		  FROM run_events
		 WHERE engine=? AND subject_id=?
		 ORDER BY id`, engine, subjectID)
	if err != nil {
		log.Printf("error: run events for %s subject=%d: %v", engine, subjectID, err)
		return nil
	}
	defer rows.Close()
	var out []RunEvent
	for rows.Next() {
		var e RunEvent
		if err := rows.Scan(&e.ID, &e.SessionUUID, &e.Kind, &e.Attempt, &e.Detail, &e.CreatedAt); err != nil {
			log.Printf("error: run events for %s subject=%d: scan: %v", engine, subjectID, err)
			return out
		}
		out = append(out, e)
	}
	return out
}

// ClearRunEvents drops a subject's timeline. Called when a run STARTS, so the
// panel shows this run's decisions and not the previous attempt's — the same
// reason phaserun resets both edges of its checkbox interval at spawn rather
// than only the left one.
func ClearRunEvents(db *sql.DB, engine string, subjectID int64) {
	if db == nil {
		return
	}
	if _, err := db.Exec(`DELETE FROM run_events WHERE engine=? AND subject_id=?`, engine, subjectID); err != nil {
		log.Printf("warning: %s: could not clear run events for subject=%d: %v", engine, subjectID, err)
	}
}
