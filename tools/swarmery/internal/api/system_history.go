package api

// Agent run history & statistics — GET /api/system/agents/{id}/history?days=90.
//
// Grain decision (see the spec): subagent_start events are NOT linked to the
// registry (events.agent_id is unpopulated), and the same agent shows up under
// three notations — plugin-qualified ("core:tech-lead"), bare ("tech-lead"),
// and, for built-ins, a name with no registry row at all ("Explore"). So we
// aggregate by NORMALISED agent NAME (the part after the last ':'), computed at
// query time. This folds every notation of one agent together and gives the
// cross-project view the panel wants, keyed off the opened row's name.

import (
	"database/sql"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"
)

// timeWindowCutoff is the RFC3339 lower bound for an N-day lookback.
func timeWindowCutoff(days int) string {
	return time.Now().UTC().AddDate(0, 0, -days).Format(time.RFC3339)
}

const (
	historyDefaultDays = 90
	historyMaxDays     = 365
	historyRunsCap     = 50
)

type agentHistoryTotals struct {
	Runs      int     `json:"runs"`
	Sessions  int     `json:"sessions"`
	Projects  int     `json:"projects"`
	OkRuns    int     `json:"okRuns"`
	ErrorRuns int     `json:"errorRuns"`
	ErrorRate float64 `json:"errorRate"` // 0..1 over runs with a known status
}

type agentHistoryDuration struct {
	AvgMs   int64 `json:"avgMs"`
	P50Ms   int64 `json:"p50Ms"`
	P95Ms   int64 `json:"p95Ms"`
	TotalMs int64 `json:"totalMs"`
}

type agentHistoryProject struct {
	Slug      string  `json:"slug"`
	Name      string  `json:"name"`
	Runs      int     `json:"runs"`
	AvgMs     int64   `json:"avgMs"`
	ErrorRate float64 `json:"errorRate"`
	LastUsed  string  `json:"lastUsed"`
}

type agentHistoryDay struct {
	Day  string `json:"day"` // YYYY-MM-DD
	Runs int    `json:"runs"`
}

type agentHistoryRun struct {
	Ts           string `json:"ts"`
	ProjectSlug  string `json:"projectSlug"`
	SessionUUID  string `json:"sessionUuid"`
	SessionTitle string `json:"sessionTitle"`
	Description  string `json:"description"`
	Status       string `json:"status"`
	DurationMs   int64  `json:"durationMs"`
}

type agentHistoryDTO struct {
	AgentName  string                `json:"agentName"`
	WindowDays int                   `json:"windowDays"`
	Totals     agentHistoryTotals    `json:"totals"`
	Duration   agentHistoryDuration  `json:"duration"`
	ByProject  []agentHistoryProject `json:"byProject"`
	ByDay      []agentHistoryDay     `json:"byDay"`
	RecentRuns []agentHistoryRun     `json:"recentRuns"`
}

// normAgentType folds "core:tech-lead" and "tech-lead" to the same key: the
// segment after the last ':'. Built-ins ("Explore", "general-purpose") pass
// through unchanged and simply never match a registry name.
// twin: internal/advisor/rules.go (normAgent, lowercased) — keep in lockstep.
func normAgentType(t string) string {
	if i := strings.LastIndexByte(t, ':'); i >= 0 {
		return t[i+1:]
	}
	return t
}

// historyWindowDays parses ?days, clamped to [1, historyMaxDays].
func historyWindowDays(r *http.Request) int {
	days := historyDefaultDays
	if v := r.URL.Query().Get("days"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			days = n
		}
	}
	if days > historyMaxDays {
		days = historyMaxDays
	}
	return days
}

// GET /api/system/agents/{id}/history?days=90
func (h *Handler) getSystemAgentHistory(w http.ResponseWriter, r *http.Request) {
	id, ok := systemItemID(w, r)
	if !ok {
		return
	}

	var name string
	err := h.DB.QueryRow(`SELECT name FROM agents WHERE id = ?`, id).Scan(&name)
	if err == sql.ErrNoRows {
		http.Error(w, `{"error":"agent not found"}`, http.StatusNotFound)
		return
	}
	if err != nil {
		writeErr(w, err)
		return
	}

	// Fold the anchor row's own name too: plugin/global rows are stored
	// plugin-qualified ("core:tech-lead"), so comparing a normalised event
	// type against a raw anchor name would never match. Normalise both sides.
	wantName := normAgentType(name)

	days := historyWindowDays(r)
	cutoff := timeWindowCutoff(days)

	rows, err := h.DB.Query(`
		SELECT e.ts, e.status, e.duration_ms,
		       json_extract(e.payload, '$.subagent_type'),
		       json_extract(e.payload, '$.description'),
		       s.session_uuid, s.title, p.slug, p.name
		FROM events e
		JOIN sessions s ON s.id = e.session_id
		LEFT JOIN projects p ON p.id = s.project_id
		WHERE e.type = 'subagent_start' AND e.ts >= ?
		ORDER BY e.ts DESC`, cutoff)
	if err != nil {
		writeErr(w, err)
		return
	}
	defer rows.Close()

	out := agentHistoryDTO{
		AgentName:  name,
		WindowDays: days,
		ByProject:  []agentHistoryProject{},
		ByDay:      []agentHistoryDay{},
		RecentRuns: []agentHistoryRun{},
	}

	type projAgg struct {
		name       string
		runs       int
		sumMs      int64
		nDur       int
		errRuns    int
		statusRuns int
		lastUsed   string
	}
	projects := map[string]*projAgg{}
	byDay := map[string]int{}
	sessions := map[string]struct{}{}
	var durations []int64

	for rows.Next() {
		var ts, status, stype, descr, sessUUID, title, slug, pname sql.NullString
		var durMs sql.NullInt64
		if err := rows.Scan(&ts, &status, &durMs, &stype, &descr, &sessUUID, &title, &slug, &pname); err != nil {
			writeErr(w, err)
			return
		}
		if normAgentType(stype.String) != wantName {
			continue
		}

		out.Totals.Runs++
		if sessUUID.Valid && sessUUID.String != "" {
			sessions[sessUUID.String] = struct{}{}
		}

		hasStatus := status.Valid && status.String != ""
		isErr := status.String == "error"
		switch {
		case !hasStatus:
			// unknown status — counts toward Runs only
		case isErr:
			out.Totals.ErrorRuns++
		default:
			out.Totals.OkRuns++
		}

		if durMs.Valid {
			durations = append(durations, durMs.Int64)
			out.Duration.TotalMs += durMs.Int64
		}

		if ts.Valid && len(ts.String) >= 10 {
			byDay[ts.String[:10]]++
		}

		pslug := slug.String
		pa := projects[pslug]
		if pa == nil {
			pa = &projAgg{name: pname.String}
			projects[pslug] = pa
		}
		pa.runs++
		if durMs.Valid {
			pa.sumMs += durMs.Int64
			pa.nDur++
		}
		if hasStatus {
			pa.statusRuns++
			if isErr {
				pa.errRuns++
			}
		}
		if ts.Valid && ts.String > pa.lastUsed {
			pa.lastUsed = ts.String
		}

		if len(out.RecentRuns) < historyRunsCap {
			out.RecentRuns = append(out.RecentRuns, agentHistoryRun{
				Ts:           ts.String,
				ProjectSlug:  pslug,
				SessionUUID:  sessUUID.String,
				SessionTitle: title.String,
				Description:  descr.String,
				Status:       status.String,
				DurationMs:   durMs.Int64,
			})
		}
	}
	if err := rows.Err(); err != nil {
		writeErr(w, err)
		return
	}

	out.Totals.Sessions = len(sessions)
	out.Totals.Projects = len(projects)
	statusRuns := out.Totals.OkRuns + out.Totals.ErrorRuns
	if statusRuns > 0 {
		out.Totals.ErrorRate = float64(out.Totals.ErrorRuns) / float64(statusRuns)
	}

	if n := len(durations); n > 0 {
		sort.Slice(durations, func(i, j int) bool { return durations[i] < durations[j] })
		out.Duration.AvgMs = out.Duration.TotalMs / int64(n)
		out.Duration.P50Ms = percentile(durations, 50)
		out.Duration.P95Ms = percentile(durations, 95)
	}

	for slug, pa := range projects {
		hp := agentHistoryProject{
			Slug:     slug,
			Name:     pa.name,
			Runs:     pa.runs,
			LastUsed: pa.lastUsed,
		}
		if pa.nDur > 0 {
			hp.AvgMs = pa.sumMs / int64(pa.nDur)
		}
		if pa.statusRuns > 0 {
			hp.ErrorRate = float64(pa.errRuns) / float64(pa.statusRuns)
		}
		out.ByProject = append(out.ByProject, hp)
	}
	sort.Slice(out.ByProject, func(i, j int) bool {
		if out.ByProject[i].Runs != out.ByProject[j].Runs {
			return out.ByProject[i].Runs > out.ByProject[j].Runs
		}
		return out.ByProject[i].Slug < out.ByProject[j].Slug
	})

	for day, n := range byDay {
		out.ByDay = append(out.ByDay, agentHistoryDay{Day: day, Runs: n})
	}
	sort.Slice(out.ByDay, func(i, j int) bool { return out.ByDay[i].Day < out.ByDay[j].Day })

	writeJSON(w, out, nil)
}

// percentile returns the p-th percentile (nearest-rank) of an ascending slice.
func percentile(sorted []int64, p int) int64 {
	n := len(sorted)
	if n == 0 {
		return 0
	}
	rank := (p*n + 99) / 100 // ceil(p/100 * n)
	if rank < 1 {
		rank = 1
	}
	if rank > n {
		rank = n
	}
	return sorted[rank-1]
}
