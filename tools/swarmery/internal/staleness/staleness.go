// Package staleness derives whether a task that CLAIMS to be running actually is,
// and says on what evidence.
//
// Derived, never persisted. For a source='workspace' row, tasks.status is a
// projection of the workspace artifacts: internal/wsingest upserts with
// `ON CONFLICT … DO UPDATE SET status = excluded.status`, so anything the daemon
// writes there is silently reverted on the next scan. A persisted verdict would be
// a second source of truth that drifts against the first. Same posture as
// internal/phasediag, which derives a run outcome from the row instead of adding a
// fourth persisted state.
//
// The audit that produced this package measured the cost of NOT having it: 42 tasks
// claiming `running` while every session tied to them had ended, holding 27.9% of
// all recorded spend, with nothing in any interface showing it.
package staleness

import (
	"database/sql"
	"fmt"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/procwatch"
)

// Kind is the verdict.
//
// KindUnknown is deliberately its own value and is never folded into KindLive:
// "we cannot tell" and "it is working" are different answers, and only one of them
// would ever justify acting. Fusion learned this the expensive way — its first
// stuck-task detector was "structurally blind to EPHEMERAL EXECUTOR agents" and
// killed everything running longer than ~30 minutes.
type Kind string

const (
	KindLive     Kind = "live"         // at least one linked session is still open
	KindStale    Kind = "stale"        // claims running, every linked session ended
	KindDeadProc Kind = "dead-process" // dispatcher-owned and its process is provably gone
	KindUnknown  Kind = "unknown"      // no evidence either way
)

// Confidence is the provenance of the session links a verdict rests on. It is not
// cosmetic: 96.3% of task_sessions links in the live database are heuristic
// (154 against 6 explicit), so a verdict built on them is grounds to SHOW an
// operator, not grounds to act unattended.
type Confidence string

const (
	ConfidenceExplicit  Confidence = "explicit"
	ConfidenceHeuristic Confidence = "heuristic"
	ConfidenceNone      Confidence = "none"
)

// Input is everything a verdict may depend on, assembled by the caller from one
// query. The derivation itself touches no database.
type Input struct {
	Status         string // tasks.status
	Source         string // tasks.source — decides whether ACTING is permitted
	LinkedSessions int    // rows in task_sessions for this task
	OpenSessions   int    // of those, sessions.ended_at IS NULL
	DispatchProc   string // proc_state of the session at tasks.dispatch_session_uuid; "" when none
	HeuristicLinks int    // of LinkedSessions, link_source='heuristic'
	AgeDays        int    // whole days since tasks.started_at
}

// Verdict carries the reason, not just the kind. A bare boolean cannot be shown to
// an operator, and "blocked" without a reason is a state this codebase has already
// paid for once. Fusion's getTaskMergeBlocker returns a reason for the same reason.
type Verdict struct {
	Kind       Kind
	Reason     string     // human-readable; names the evidence the verdict rests on
	Actionable bool       // true only when the daemon may WRITE state for this row
	Confidence Confidence // provenance of the links behind the verdict
}

// sourceQueue is the only source whose rows the daemon owns outright.
const sourceQueue = "queue"

// Classify derives the verdict. Pure.
//
// Rule order is load-bearing and must not be reordered for tidiness:
//   - the not-running check comes first so a done/queued task never reaches the
//     evidence rules at all;
//   - dead-process comes before the session rules because it is the strongest
//     evidence available (procwatch observed the process itself, not a proxy);
//   - no-links comes before all-ended, because "no link to check" must not read as
//     "checked and found dead" — SUM over zero rows is zero, and that zero would
//     otherwise satisfy the all-ended test and manufacture evidence from absence.
func Classify(in Input) Verdict {
	conf := confidenceOf(in)

	if in.Status != "running" {
		return Verdict{Kind: KindLive, Reason: "task does not claim to be running", Confidence: conf}
	}

	if in.DispatchProc == procwatch.StateDead {
		return Verdict{
			Kind:       KindDeadProc,
			Reason:     "dispatch process is gone (procwatch: dead)",
			Actionable: in.Source == sourceQueue,
			Confidence: conf,
		}
	}

	if in.LinkedSessions == 0 {
		return Verdict{
			Kind:       KindUnknown,
			Reason:     "claims running, but has no linked session to check",
			Confidence: conf,
		}
	}

	if in.OpenSessions == 0 {
		return Verdict{
			Kind: KindStale,
			Reason: fmt.Sprintf("claims running for %d day(s), but all %d linked session(s) have ended",
				in.AgeDays, in.LinkedSessions),
			Actionable: in.Source == sourceQueue,
			Confidence: conf,
		}
	}

	return Verdict{
		Kind:       KindLive,
		Reason:     fmt.Sprintf("%d of %d linked session(s) still open", in.OpenSessions, in.LinkedSessions),
		Confidence: conf,
	}
}

// confidenceOf reports what the links behind a verdict are worth. A task with no
// links at all is ConfidenceNone rather than ConfidenceExplicit: zero heuristic
// links out of zero links is not cleanliness, it is absence of evidence.
func confidenceOf(in Input) Confidence {
	switch {
	case in.LinkedSessions == 0:
		return ConfidenceNone
	case in.HeuristicLinks > 0:
		return ConfidenceHeuristic
	default:
		return ConfidenceExplicit
	}
}

// SessionEnded reports whether one sessions row is over: a terminal status
// (completed | killed) or a backing process procwatch observed dead. Exported for
// internal/stall, which asks the same question of a single stage session.
//
// sessions.ended_at is deliberately not consulted: ingest stamps it with the
// transcript's LAST record timestamp on every batch, so it is set on a session
// that is still writing. The status column is what the ticker and procwatch move
// to a terminal value, and proc_state=dead is the strongest evidence available
// (the same rule order Classify follows).
func SessionEnded(status, procState string) bool {
	return status == SessionCompleted || status == SessionKilled || procState == procwatch.StateDead
}

// The terminal sessions.status values.
const (
	SessionCompleted = "completed"
	SessionKilled    = "killed"
)

// Row is one task with its derived verdict, as returned by Load.
type Row struct {
	TaskID  int64
	Title   string
	Input   Input
	Verdict Verdict
}

// loadQuery assembles Input for every live task of a project.
//
// LEFT JOIN, not JOIN: a task with no task_sessions row at all must still reach
// Classify's no-links rule. An inner join would drop it from the result entirely,
// and it would then be invisible in exactly the interface built to make invisible
// tasks visible. (internal/economics/queries.go uses an inner join for the same
// shape on purpose — it only wants tasks that carry spend.)
const loadQuery = `
SELECT t.id, t.title, t.status, t.source,
       COUNT(ts.session_id)                                             AS linked,
       COALESCE(SUM(CASE WHEN s.ended_at IS NULL THEN 1 ELSE 0 END), 0)  AS open_sessions,
       COALESCE(SUM(CASE WHEN ts.link_source='heuristic' THEN 1 ELSE 0 END), 0) AS heuristic_links,
       COALESCE((SELECT ds.proc_state FROM sessions ds
                  WHERE ds.session_uuid = t.dispatch_session_uuid), '')  AS dispatch_proc,
       COALESCE(CAST(julianday('now') - julianday(t.started_at) AS INTEGER), 0) AS age_days
  FROM tasks t
  LEFT JOIN task_sessions ts ON ts.task_id = t.id
  LEFT JOIN sessions s       ON s.id = ts.session_id
 WHERE t.archived_at IS NULL`

// Load returns every non-archived task with its derived verdict. projectID <= 0
// means all projects — the CLI wants the whole picture, the board API wants one
// project.
func Load(db *sql.DB, projectID int64) ([]Row, error) {
	q := loadQuery
	args := []any{}
	if projectID > 0 {
		q += ` AND t.project_id = ?`
		args = append(args, projectID)
	}
	q += ` GROUP BY t.id ORDER BY t.id`

	rows, err := db.Query(q, args...)
	if err != nil {
		return nil, fmt.Errorf("staleness: load: %w", err)
	}
	defer rows.Close()

	var out []Row
	for rows.Next() {
		var r Row
		var dispatchProc sql.NullString
		if err := rows.Scan(&r.TaskID, &r.Title, &r.Input.Status, &r.Input.Source,
			&r.Input.LinkedSessions, &r.Input.OpenSessions, &r.Input.HeuristicLinks,
			&dispatchProc, &r.Input.AgeDays); err != nil {
			return nil, fmt.Errorf("staleness: scan: %w", err)
		}
		r.Input.DispatchProc = dispatchProc.String
		r.Verdict = Classify(r.Input)
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("staleness: rows: %w", err)
	}
	return out, nil
}
