package api

// Operator correction ledger (memory-engineering gaps, phase 3):
//
//	GET /api/corrections?window=14d&limit=200 → {groups: [Group], rows: [Row]}
//
// Read-only. groups is the window folded by norm_key (what advisor R14 counts),
// rows the newest raw entries. The rows are written by the operator endpoints
// in lessons.go, triage.go, revisions.go and decisions.go — there is no POST
// here on purpose: a correction is something the operator DID, not something
// they file.

import (
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/corrections"
)

const (
	correctionsDefaultWindowDays = 14
	correctionsMaxWindowDays     = 365
)

// correctionsWindowDays parses ?window= as "14d" or "14"; anything else, or a
// value outside 1..365, is the 14-day default.
func correctionsWindowDays(raw string) int {
	s := strings.TrimSuffix(strings.TrimSpace(strings.ToLower(raw)), "d")
	n, err := strconv.Atoi(s)
	if err != nil || n < 1 || n > correctionsMaxWindowDays {
		return correctionsDefaultWindowDays
	}
	return n
}

func (h *Handler) listCorrections(w http.ResponseWriter, r *http.Request) {
	days := correctionsWindowDays(r.URL.Query().Get("window"))
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	since := time.Now().AddDate(0, 0, -days)
	groups, err := corrections.Groups(h.DB, since)
	if err != nil {
		writeErr(w, err)
		return
	}
	rows, err := corrections.List(h.DB, since, limit)
	writeJSON(w, map[string]any{"groups": groups, "rows": rows}, err)
}
