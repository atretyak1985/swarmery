package advisor

// Opus 5.5 / learning-loop phase 16.3 — R12: the lesson store's own budget.
//
// R10's sibling. R10 watches the always-loaded auto-memory index; R12 watches
// the ACTIVE surprise lessons (surprise_lessons, 0084) that injection hands to
// headless runs. The injection budget (SWARMERY_LESSON_BUDGET_TOKENS) already
// cuts what one run receives, so an oversized store does not blow a prompt — it
// does something quieter: lessons past the cut are never read, and the ranking
// decides silently which of them a run sees. Two triggers, as for R10:
//
//   - too many active lessons in total;
//   - too many active lessons naming the same area (the directory each glob
//     names, "." for a bare "*"), where the cut bites first.
//
// R12 is self-checking (advisor.go's selfCheckingRules): it re-counts the store
// on every pass, so retiring lessons (phase 16.2's queue) simply stops it
// firing and resolveVanished closes the row.

import (
	"database/sql"
	"fmt"
	"sort"
	"strings"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/surprise"
)

const (
	// R12ActiveTotal: more active lessons than this across the store fires.
	R12ActiveTotal = 40
	// R12ActivePerArea: more active lessons than this naming one area fires.
	R12ActivePerArea = 8
	// r12TotalTarget is the target of the store-wide finding.
	r12TotalTarget = "lessons"
	// r12AreaPrefix prefixes a per-area finding's target.
	r12AreaPrefix = "lessons:"
)

// r12LessonAreas counts active lessons per area.
func r12LessonAreas(db *sql.DB) (total int, perArea map[string]int, err error) {
	rows, err := db.Query(`SELECT area_globs FROM surprise_lessons WHERE status = 'active'`)
	if err != nil {
		return 0, nil, err
	}
	defer rows.Close()
	perArea = map[string]int{}
	for rows.Next() {
		var globs string
		if err := rows.Scan(&globs); err != nil {
			return 0, nil, err
		}
		total++
		seen := map[string]bool{}
		for _, g := range strings.Split(globs, ",") {
			if g = strings.TrimSpace(g); g == "" {
				continue
			}
			a := surprise.NormArea(g)
			if a == "" {
				a = "."
			}
			if !seen[a] {
				seen[a] = true
				perArea[a]++
			}
		}
	}
	return total, perArea, rows.Err()
}

// r12LessonBudget flags a lesson store over its total or per-area budget.
func r12LessonBudget(db *sql.DB, win window) ([]finding, error) {
	total, perArea, err := r12LessonAreas(db)
	if err != nil {
		return nil, err
	}
	limits := map[string]any{"active_total": R12ActiveTotal, "active_per_area": R12ActivePerArea}
	var out []finding
	if total > R12ActiveTotal {
		out = append(out, finding{
			rule: "R12", targetKind: "memory", target: r12TotalTarget,
			title:    fmt.Sprintf("Lesson store is over budget: %d active lessons", total),
			detail:   r12Detail(fmt.Sprintf("%d active lessons (budget %d)", total, R12ActiveTotal)),
			evidence: map[string]any{"window": win, "counts": map[string]int{"active": total}, "limits": limits},
		})
	}
	areas := make([]string, 0, len(perArea))
	for a := range perArea {
		areas = append(areas, a)
	}
	sort.Strings(areas)
	for _, a := range areas {
		n := perArea[a]
		if n <= R12ActivePerArea {
			continue
		}
		out = append(out, finding{
			rule: "R12", targetKind: "memory", target: r12AreaPrefix + a,
			title:    fmt.Sprintf("Too many active lessons in %s: %d", a, n),
			detail:   r12Detail(fmt.Sprintf("%d active lessons name %s (budget %d per area)", n, a, R12ActivePerArea)),
			evidence: map[string]any{"window": win, "area": a, "counts": map[string]int{"active": n}, "limits": limits},
		})
	}
	sortFindings(out)
	return out, nil
}

func r12Detail(what string) string {
	return what + ". Injection hands a run only what fits SWARMERY_LESSON_BUDGET_TOKENS, ranked by measured effectiveness, so lessons past the cut are never read. Work the retirement queue on the Lessons page, merge lessons that say the same thing, or promote settled ones into the area's CLAUDE.md."
}

// r12Metric is the verify scalar: the active count the target names.
func r12Metric(db *sql.DB, target string) (float64, bool, error) {
	total, perArea, err := r12LessonAreas(db)
	if err != nil {
		return 0, false, err
	}
	if target == r12TotalTarget {
		return float64(total), true, nil
	}
	return float64(perArea[strings.TrimPrefix(target, r12AreaPrefix)]), true, nil
}
