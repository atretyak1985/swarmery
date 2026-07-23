package advisor

// Exported read-only views over the R2 window accumulator and the error-group
// event fold, consumed by internal/improve (the phase-3 rewriter). The
// rewriter must cite the SAME numbers the advisor and the Retro scorecards
// show, so it calls these instead of re-implementing the SQL — keep them
// thin wrappers over agentErrorWindow / the errGroupDays query.

import (
	"database/sql"
	"sort"
	"time"
)

// NormAgent is the exported agent-name fold (normAgent): "core:tech-lead"
// and "tech-lead" collapse to the same lowercase registry key.
func NormAgent(t string) string { return normAgent(t) }

// ErrGroupCount is one folded error group with its window count and class.
type ErrGroupCount struct {
	Key   string
	Count int64
	Class ErrClass
}

// AgentScorecard is one agent's slice of the R2 window accumulator —
// the evidence-bundle "scorecard" grain (runs, failed-run dedupe,
// errors_by_class, top error groups).
type AgentScorecard struct {
	From, To           string
	Runs               int64
	Errors             int64
	FailedRuns         int64
	BehaviorFailedRuns int64
	ErrorsByClass      map[ErrClass]int64
	// TopGroups is sorted by count DESC then key ASC, capped at
	// scorecardTopGroups.
	TopGroups []ErrGroupCount
}

// scorecardTopGroups caps AgentScorecard.TopGroups.
const scorecardTopGroups = 5

// ScorecardFor computes one agent's trailing-WindowDays scorecard via
// agentErrorWindow (the exact R2 grain). An agent absent from the window
// returns a zero scorecard, not an error.
func ScorecardFor(db *sql.DB, agent string, now time.Time) (AgentScorecard, error) {
	win := window{From: fmtTS(now.AddDate(0, 0, -WindowDays)), To: fmtTS(now)}
	sc := AgentScorecard{From: win.From, To: win.To, ErrorsByClass: map[ErrClass]int64{}}
	acc, err := agentErrorWindow(db, win)
	if err != nil {
		return sc, err
	}
	a := acc[normAgent(agent)]
	if a == nil {
		return sc, nil
	}
	sc.Runs = a.runs
	sc.Errors = a.errors
	sc.FailedRuns = a.failedRuns()
	sc.BehaviorFailedRuns = a.behaviorFailedRuns()
	for c, n := range a.byClass {
		sc.ErrorsByClass[c] = n
	}
	for k, n := range a.errKeys {
		sc.TopGroups = append(sc.TopGroups, ErrGroupCount{Key: k, Count: n, Class: Classify(k)})
	}
	sort.Slice(sc.TopGroups, func(i, j int) bool {
		if sc.TopGroups[i].Count != sc.TopGroups[j].Count {
			return sc.TopGroups[i].Count > sc.TopGroups[j].Count
		}
		return sc.TopGroups[i].Key < sc.TopGroups[j].Key
	})
	if len(sc.TopGroups) > scorecardTopGroups {
		sc.TopGroups = sc.TopGroups[:scorecardTopGroups]
	}
	return sc, nil
}

// ErrEvent locates one error event of a folded group in the transcript.
type ErrEvent struct {
	EventID   int64
	SessionID int64
	TS        string
	Msg       string
}

// ErrGroupEvents returns the newest ≤ limit error events whose folded key
// (normalizeErrKey ∘ extractErrMsg) equals key, over the trailing-WindowDays
// window — the same row set and fold as errGroupDays, exposed with event
// coordinates so the rewriter can pull transcript context around them.
func ErrGroupEvents(db *sql.DB, key string, now time.Time, limit int) ([]ErrEvent, error) {
	win := window{From: fmtTS(now.AddDate(0, 0, -WindowDays)), To: fmtTS(now)}
	rows, err := db.Query(`
		SELECT e.id, e.session_id, e.type, e.tool_name, e.payload, e.ts
		  FROM events e
		  JOIN sessions s ON s.id = e.session_id
		  JOIN projects p ON p.id = s.project_id
		 WHERE e.status = 'error'
		   AND e.type IN ('error','tool_call','skill_use','subagent_start','subagent_stop','test_run')
		   AND e.ts >= ? AND e.ts < ? AND p.archived = 0
		 ORDER BY e.ts DESC, e.id DESC`,
		win.From, win.To)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ErrEvent
	for rows.Next() {
		var ev ErrEvent
		var typ string
		var toolName, payload sql.NullString
		if err := rows.Scan(&ev.EventID, &ev.SessionID, &typ, &toolName, &payload, &ev.TS); err != nil {
			return nil, err
		}
		msg := extractErrMsg(typ, toolName, payload)
		if normalizeErrKey(msg) != key {
			continue
		}
		ev.Msg = msg
		out = append(out, ev)
		if len(out) >= limit {
			break
		}
	}
	return out, rows.Err()
}
