package api

// Analytics uplift: GET /api/stats/durations — session-length and
// approval-wait aggregates over a local-day range. Spans are computed in Go
// from the stored ISO-8601 UTC strings: the median needs the sorted set
// anyway, and Go parsing sidesteps SQLite julianday/timezone-suffix quirks.

import (
	"net/http"
	"sort"
	"time"
)

type durationsDTO struct {
	From string `json:"from"`
	To   string `json:"to"`
	// Completed sessions started in range with a recorded end.
	SessionCount     int64    `json:"session_count"`
	AvgSessionSec    *float64 `json:"avg_session_sec"`    // nil when count is 0
	MedianSessionSec *float64 `json:"median_session_sec"` // nil when count is 0
	// permission_requests rows requested in range and already resolved.
	ApprovalsResolved int64    `json:"approvals_resolved"`
	AvgResolveSec     *float64 `json:"avg_resolve_sec"` // nil when none resolved
	WaitTotalMin      float64  `json:"wait_total_min"`
}

// parseUTCts parses a stored timestamp, tolerating both RFC3339 and the
// zone-suffix-free bound form — same tolerance as localDay (analytics.go).
func parseUTCts(s string) (time.Time, bool) {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		t, err = time.Parse("2006-01-02T15:04:05", s)
		if err != nil {
			return time.Time{}, false
		}
	}
	return t, true
}

// querySpans runs a (start, end) two-column query and returns each
// non-negative span in seconds; rows with unparseable timestamps are skipped.
func (h *Handler) querySpans(q string, args []any) ([]float64, error) {
	rows, err := h.DB.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []float64
	for rows.Next() {
		var start, end string
		if err := rows.Scan(&start, &end); err != nil {
			return nil, err
		}
		st, ok1 := parseUTCts(start)
		en, ok2 := parseUTCts(end)
		if !ok1 || !ok2 {
			continue
		}
		if d := en.Sub(st).Seconds(); d >= 0 {
			out = append(out, d)
		}
	}
	return out, rows.Err()
}

// GET /api/stats/durations?from&to&project — project is the optional global
// scope (slug or id, resolved by scopeFilter).
func (h *Handler) statsDurations(w http.ResponseWriter, r *http.Request) {
	dr, err := parseRange(r)
	if err != nil {
		http.Error(w, `{"error":"`+err.Error()+`"}`, http.StatusBadRequest)
		return
	}
	pf, pargs := scopeFilter(r)
	out := durationsDTO{From: dr.days[0], To: dr.days[len(dr.days)-1]}

	spans, err := h.querySpans(`
		SELECT s.started_at, s.ended_at
		  FROM sessions s
		  JOIN projects p ON p.id = s.project_id
		 WHERE s.status = 'completed' AND s.ended_at IS NOT NULL
		   AND s.started_at >= ? AND s.started_at < ? AND p.archived = 0`+pf,
		append([]any{dr.start, dr.end}, pargs...))
	if err != nil {
		writeErr(w, err)
		return
	}
	out.SessionCount = int64(len(spans))
	if n := len(spans); n > 0 {
		sort.Float64s(spans)
		var sum float64
		for _, s := range spans {
			sum += s
		}
		avg := sum / float64(n)
		out.AvgSessionSec = &avg
		var med float64
		if n%2 == 1 {
			med = spans[n/2]
		} else {
			med = (spans[n/2-1] + spans[n/2]) / 2
		}
		out.MedianSessionSec = &med
	}

	waits, err := h.querySpans(`
		SELECT pr.requested_at, pr.resolved_at
		  FROM permission_requests pr
		  JOIN sessions s ON s.id = pr.session_id
		  JOIN projects p ON p.id = s.project_id
		 WHERE pr.resolved_at IS NOT NULL
		   AND pr.requested_at >= ? AND pr.requested_at < ? AND p.archived = 0`+pf,
		append([]any{dr.start, dr.end}, pargs...))
	if err != nil {
		writeErr(w, err)
		return
	}
	out.ApprovalsResolved = int64(len(waits))
	if n := len(waits); n > 0 {
		var sum float64
		for _, s := range waits {
			sum += s
		}
		avg := sum / float64(n)
		out.AvgResolveSec = &avg
		out.WaitTotalMin = sum / 60
	}
	writeJSON(w, out, nil)
}
