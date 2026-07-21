package api

// Analytics uplift: GET /api/stats/errors — error events grouped by a
// normalized message key for the Overview error drill-down.
//
// Payload shapes (verified against internal/ingest/ingest.go):
//   - system api_error → events(type='error', status='error',
//     payload={"error": <raw JSONL error>}) — a string, or an object carrying
//     "message" (possibly nested one level as {"error":{"message":…}}).
//   - tool failure (closeToolCall) → events(status='error',
//     payload={"input": …, "result": <string error text | object>}).
//
// Normalization: lowercase → every digit-bearing token (status codes, request
// ids, hex ids, ports, paths like build-4821) collapses to "#" → whitespace
// folds → truncate to 80 runes. So "Error 529 (req_011abc)" and
// "Error 529 (req_022xyz)" share one group.

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"regexp"
	"sort"
	"strings"
)

type errorSampleDTO struct {
	SessionID int64   `json:"session_id"`
	Title     *string `json:"title"`
}

type errorGroupDTO struct {
	Key     string           `json:"key"`
	Example string           `json:"example"` // newest raw message, ≤160 runes
	Count   int64            `json:"count"`
	LastTs  string           `json:"last_ts"`
	Samples []errorSampleDTO `json:"samples"` // up to 3 distinct sessions, newest first
}

type errorsDTO struct {
	From   string          `json:"from"`
	To     string          `json:"to"`
	Groups []errorGroupDTO `json:"groups"`
	// Approx is true when the range overlaps pruned (rolled-up) days — daily
	// rollups keep only an error COUNT, no error events, so the groups
	// silently undercount there. Same honesty rule as the timeseries badge.
	Approx bool `json:"approx"`
}

var (
	reErrIDToken = regexp.MustCompile(`[a-z0-9_]*[0-9][a-z0-9_]*`)
	reErrSpace   = regexp.MustCompile(`\s+`)
)

// normalizeErrKey folds one raw error message to its group key.
// twin: internal/advisor/rules.go — keep in lockstep.
func normalizeErrKey(msg string) string {
	k := strings.ToLower(msg)
	k = reErrIDToken.ReplaceAllString(k, "#")
	k = reErrSpace.ReplaceAllString(k, " ")
	k = strings.TrimSpace(k)
	if r := []rune(k); len(r) > 80 {
		k = string(r[:80])
	}
	return k
}

// extractErrMsg pulls a human-readable message out of one error event payload.
// twin: internal/advisor/rules.go — keep in lockstep.
func extractErrMsg(typ string, toolName sql.NullString, payload sql.NullString) string {
	var p map[string]any
	if payload.Valid {
		_ = json.Unmarshal([]byte(payload.String), &p)
	}
	if typ == "error" { // system api_error: payload.error
		switch e := p["error"].(type) {
		case string:
			if e != "" {
				return e
			}
		case map[string]any:
			if m, ok := e["message"].(string); ok && m != "" {
				return m
			}
			if inner, ok := e["error"].(map[string]any); ok {
				if m, ok := inner["message"].(string); ok && m != "" {
					return m
				}
			}
			if b, err := json.Marshal(e); err == nil {
				return string(b)
			}
		}
		return "api error"
	}
	// Tool failure: payload.result carries the error text.
	switch res := p["result"].(type) {
	case string:
		if res != "" {
			return res
		}
	case map[string]any:
		if m, ok := res["message"].(string); ok && m != "" {
			return m
		}
		if b, err := json.Marshal(res); err == nil {
			return string(b)
		}
	}
	if toolName.Valid && toolName.String != "" {
		return toolName.String + " error"
	}
	return "error"
}

// errGroupSample is one distinct sample session inside an error group — both
// identity forms travel along so each consumer picks its own (statsErrors
// exposes the numeric row id + title; retroFriction the session uuid).
type errGroupSample struct {
	SessionID   int64
	SessionUUID string
	Title       *string
}

// errGroup is one normalized-message error group as accumulated by
// errorGroups, before shaping into an endpoint-specific DTO.
type errGroup struct {
	Key     string
	Example string // newest raw message, ≤160 runes
	Count   int64
	LastTs  string
	Samples []errGroupSample // up to 3 distinct sessions, newest first
}

// errorGroups fetches the range's status='error' events and folds them by
// normalized message key — the grouping engine shared by /api/stats/errors
// and /api/retro/friction. Groups come back count desc (first-seen = newest
// as the stable tie-break).
func (h *Handler) errorGroups(dr dateRange, pf string, pargs []any) ([]errGroup, error) {
	// The type IN (…) predicate is semantics-preserving — these six are the
	// only event types ingest.go ever marks status='error' ('error' from
	// system api_error; tool_call/skill_use/subagent_start via closeToolCall's
	// status UPDATE; subagent_stop and test_run inserted with the mirrored
	// status) — and it lets SQLite drive the query off idx_events_type
	// instead of a full events scan. EXPLAIN QUERY PLAN without it:
	// "SCAN e"; with it: "SEARCH e USING INDEX idx_events_type
	// (type=? AND ts>? AND ts<?)".
	rows, err := h.DB.Query(`
		SELECT e.type, e.tool_name, e.payload, e.ts, s.id, s.session_uuid, s.title
		  FROM events e
		  JOIN sessions s ON s.id = e.session_id
		  JOIN projects p ON p.id = s.project_id
		 WHERE e.status = 'error'
		   AND e.type IN ('error','tool_call','skill_use','subagent_start','subagent_stop','test_run')
		   AND e.ts >= ? AND e.ts < ? AND p.archived = 0`+pf+`
		 ORDER BY e.ts DESC`,
		append([]any{dr.start, dr.end}, pargs...)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	type agg struct {
		group    errGroup
		sessions map[int64]struct{}
	}
	acc := map[string]*agg{}
	var order []string // first-seen (= newest-first) for stable tie-breaking
	for rows.Next() {
		var typ, ts, sessUUID string
		var toolName, payload, title sql.NullString
		var sessID int64
		if err := rows.Scan(&typ, &toolName, &payload, &ts, &sessID, &sessUUID, &title); err != nil {
			return nil, err
		}
		msg := extractErrMsg(typ, toolName, payload)
		key := normalizeErrKey(msg)
		a := acc[key]
		if a == nil {
			example := msg
			if rn := []rune(example); len(rn) > 160 {
				example = string(rn[:160])
			}
			// Rows arrive ts DESC, so the first row of a group is its newest.
			a = &agg{group: errGroup{Key: key, Example: example, LastTs: ts}, sessions: map[int64]struct{}{}}
			acc[key] = a
			order = append(order, key)
		}
		a.group.Count++
		if _, seen := a.sessions[sessID]; !seen && len(a.group.Samples) < 3 {
			a.sessions[sessID] = struct{}{}
			var tp *string
			if title.Valid {
				tt := title.String
				tp = &tt
			}
			a.group.Samples = append(a.group.Samples, errGroupSample{SessionID: sessID, SessionUUID: sessUUID, Title: tp})
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	out := make([]errGroup, 0, len(acc))
	for _, key := range order {
		out = append(out, acc[key].group)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Count > out[j].Count })
	return out, nil
}

// GET /api/stats/errors?from&to&project — project is the optional global
// scope (slug or id, resolved by scopeFilter).
func (h *Handler) statsErrors(w http.ResponseWriter, r *http.Request) {
	dr, err := parseRange(r)
	if err != nil {
		http.Error(w, `{"error":"`+err.Error()+`"}`, http.StatusBadRequest)
		return
	}
	pf, pargs := scopeFilter(r)
	groups, err := h.errorGroups(dr, pf, pargs)
	if err != nil {
		writeErr(w, err)
		return
	}

	out := errorsDTO{From: dr.days[0], To: dr.days[len(dr.days)-1], Groups: make([]errorGroupDTO, 0, len(groups))}
	for _, g := range groups {
		dto := errorGroupDTO{Key: g.Key, Example: g.Example, Count: g.Count, LastTs: g.LastTs,
			Samples: make([]errorSampleDTO, 0, len(g.Samples))}
		for _, s := range g.Samples {
			dto.Samples = append(dto.Samples, errorSampleDTO{SessionID: s.SessionID, Title: s.Title})
		}
		out.Groups = append(out.Groups, dto)
	}
	rolled, err := h.hasRolledUpDays(dr.days[0], dr.days[len(dr.days)-1], pf, pargs)
	if err != nil {
		writeErr(w, err)
		return
	}
	out.Approx = rolled
	writeJSON(w, out, nil)
}
