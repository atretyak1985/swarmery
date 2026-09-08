package exploration

// Share folds `events.type = 'tool_call'` into a per-local-day exploration
// share. It is the one number the graft context layer is judged by, so the
// query stays deliberately dumb: fetch the rows in the window, classify each
// one in Go, bucket by LOCAL day. No SQL-side classification — the rules live
// in classify.go and must not drift into a second dialect.

import (
	"database/sql"
	"sort"
	"strings"
	"time"
)

// dayFmt is the local-day bucket key, matching internal/api's dayFmt.
const dayFmt = "2006-01-02"

// topN caps the ranked explore-tool list served with every response.
const topN = 10

// Range is the resolved window: the local-day buckets to emit plus the UTC
// bounds the stored ISO-8601 timestamps are filtered on. Built by the caller
// (internal/api's parseRange) so this package owns no HTTP parsing.
type Range struct {
	// Days are "2006-01-02" local days, ascending and inclusive.
	Days []string
	// Start is the UTC bound of the first day's local midnight (inclusive).
	Start string
	// End is the UTC bound of the day AFTER the last one (exclusive).
	End string
}

// DayRow is one local day's call counts plus that day's exploration share.
type DayRow struct {
	Day     string  `json:"day"`
	Explore int     `json:"explore"`
	Edit    int     `json:"edit"`
	Run     int     `json:"run"`
	Other   int     `json:"other"`
	Calls   int     `json:"calls"`
	Share   float64 `json:"share"`
}

// Totals aggregates the whole range. Share is the range's explore/calls ratio,
// NOT the mean of the daily shares — a quiet day must not weigh as much as a
// busy one.
type Totals struct {
	Explore int     `json:"explore"`
	Edit    int     `json:"edit"`
	Run     int     `json:"run"`
	Other   int     `json:"other"`
	Calls   int     `json:"calls"`
	Share   float64 `json:"share"`
}

// ToolCount is one entry of the ranked explore-tool list.
type ToolCount struct {
	Tool string `json:"tool"`
	N    int    `json:"n"`
}

// Result is the served shape: aligned daily series, range totals, and the
// tools driving the exploration share.
type Result struct {
	Days   []DayRow    `json:"days"`
	Totals Totals      `json:"totals"`
	Top    []ToolCount `json:"top"`
}

// shareQuery pulls every tool call in the window with the two fields the
// classifier needs. filterSQL is appended verbatim — it is a caller-owned
// constant predicate (internal/api's scopeFilter), never user text.
const shareQuery = `
	SELECT e.ts, e.tool_name, json_extract(e.payload, '$.input.command')
	  FROM events e
	  JOIN sessions s ON s.id = e.session_id
	  JOIN projects p ON p.id = s.project_id
	 WHERE e.type = 'tool_call'
	   AND e.ts >= ? AND e.ts < ?
	   AND p.archived = 0`

// Share computes the exploration share over rng, optionally scoped by a
// project predicate (filterSQL + filterArgs, from internal/api's scopeFilter;
// pass "" and nil for the unscoped view).
//
// Every day in rng.Days is emitted, including the ones with no calls — the
// sparkline needs a fixed-length series, and a gap must read as a zero rather
// than shifting the line.
func Share(db *sql.DB, rng Range, filterSQL string, filterArgs []any) (Result, error) {
	res := Result{
		Days: make([]DayRow, len(rng.Days)),
		Top:  []ToolCount{},
	}
	index := make(map[string]int, len(rng.Days))
	for i, d := range rng.Days {
		res.Days[i] = DayRow{Day: d}
		index[d] = i
	}

	args := append([]any{rng.Start, rng.End}, filterArgs...)
	rows, err := db.Query(shareQuery+filterSQL, args...)
	if err != nil {
		return Result{}, err
	}
	defer rows.Close()

	byTool := map[string]int{}
	for rows.Next() {
		var ts string
		var tool, command sql.NullString
		if err := rows.Scan(&ts, &tool, &command); err != nil {
			return Result{}, err
		}
		day, ok := localDay(ts)
		if !ok {
			continue
		}
		idx, ok := index[day]
		if !ok {
			continue // inside the UTC span but outside the local-day buckets
		}
		kind := Classify(tool.String, command.String)
		row := &res.Days[idx]
		row.Calls++
		res.Totals.Calls++
		switch kind {
		case Explore:
			row.Explore++
			res.Totals.Explore++
			byTool[topKey(tool.String, command.String)]++
		case Edit:
			row.Edit++
			res.Totals.Edit++
		case Run:
			row.Run++
			res.Totals.Run++
		case Other:
			row.Other++
			res.Totals.Other++
		}
	}
	if err := rows.Err(); err != nil {
		return Result{}, err
	}

	for i := range res.Days {
		res.Days[i].Share = ratio(res.Days[i].Explore, res.Days[i].Calls)
	}
	res.Totals.Share = ratio(res.Totals.Explore, res.Totals.Calls)
	res.Top = rank(byTool)
	return res, nil
}

// topKey labels one explore call for the ranked list. Bash is broken out by
// the command's own name ("grep", "rg", …) rather than collapsing into a
// single "Bash" bar: the point of the list is to say WHICH discovery habit
// dominates, and half of them arrive through the shell.
func topKey(tool, command string) string {
	if !strings.EqualFold(strings.TrimSpace(tool), "bash") {
		if tool == "" {
			return "unknown"
		}
		return tool
	}
	toks := stripPrefixes(tokenize(command))
	if len(toks) == 0 {
		return "bash"
	}
	return baseCmd(toks[0])
}

// rank sorts the explore-tool counts by descending count, ties broken by name
// so the list is stable between requests, and caps it at topN.
func rank(byTool map[string]int) []ToolCount {
	out := make([]ToolCount, 0, len(byTool))
	for tool, n := range byTool {
		out = append(out, ToolCount{Tool: tool, N: n})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].N != out[j].N {
			return out[i].N > out[j].N
		}
		return out[i].Tool < out[j].Tool
	})
	if len(out) > topN {
		out = out[:topN]
	}
	return out
}

// ratio is explore/calls, guarded against the empty day.
func ratio(explore, calls int) float64 {
	if calls == 0 {
		return 0
	}
	return float64(explore) / float64(calls)
}

// localDay maps a stored UTC timestamp string to its local YYYY-MM-DD.
// Twin of internal/api/analytics.go and internal/advisor/rules.go localDay —
// keep all three in lockstep (the advisor set the duplicate-with-a-pointer
// precedent rather than exporting it from an HTTP package).
func localDay(utcTS string) (string, bool) {
	t, err := time.Parse(time.RFC3339, utcTS)
	if err != nil {
		// zone-suffix-free bound form ("2006-01-02T15:04:05").
		t, err = time.Parse("2006-01-02T15:04:05", utcTS)
		if err != nil {
			return "", false
		}
	}
	return t.Local().Format(dayFmt), true
}
