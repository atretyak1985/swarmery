package advisor

// agent-memory phase 4 — R11: a lesson the fleet keeps re-learning.
//
// A retrospective lesson is, by construction, something that already went
// wrong once. The fleet writes them down honestly and then learns the same one
// again three tasks later, because a lesson lives in a finished task's retro doc
// and nothing carries it forward into the procedure the next run actually reads.
//
// R11 is the detector for exactly that failure: fold the window's lessons by
// their cross-task identity (retro_lessons.norm_title, migration 0070) and raise
// the ones that turned up in R11MinTasks or more DISTINCT tasks. One task
// writing the same lesson twice is a duplicated paragraph, not a pattern — the
// count is over tasks, never over rows.
//
// The finding's target_kind is `skill` (migration 0072), NOT `process` like R5.
// R5 points at one retro's improvement row and asks a human to go do it; R11
// points at a procedure that should have carried the lesson and never did, and
// the improve loop's skill proposals (agent-memory phase 5) route on that kind.
// The kind IS the routing key here, so it is load-bearing, not decoration.
//
// R11 is deliberately NOT in advisor.go's selfCheckingRules. It reads stored
// rows over a trailing window, so "it did not fire" is ambiguous in exactly the
// way that list warns about: a fortnight in which nobody wrote a retrospective
// looks identical to a fortnight in which the lesson was finally absorbed.
// Closing an ACCEPTED row on that would be guessing. A `proposed` row is still
// swept when the rule goes quiet (resolveVanished closes those on any rule) —
// nobody had committed to it, and the rule re-proposes if the lesson returns.

import (
	"database/sql"
	"fmt"
	"strings"
)

const (
	// R11MinTasks: how many DISTINCT tasks must have learned the same lesson
	// before it stops being coincidence. Three is the smallest number that can
	// show a trend rather than a repeat.
	R11MinTasks = 3
	// R11MaxSlugs: how many task ids the detail line names. Enough to go look,
	// short enough to read — the full list is in the evidence JSON.
	R11MaxSlugs = 5
)

// lessonGroup is one folded lesson identity within the window.
type lessonGroup struct {
	norm   string
	title  string   // the most recent wording
	action string   // the most recent `**Action**:` line, "" when absent
	tasks  []string // distinct external task ids, newest first
}

// r11RecurringLesson flags lesson identities that recurred across at least
// R11MinTasks distinct tasks inside the window.
//
// Window-bound on the TASK's start date, the same predicate /api/retro/lessons
// uses, so a card on the page and a recommendation about it cite the same set of
// tasks. That also gives the rule a metric that can move: the count is "in the
// last 14 days", so a lesson that genuinely got absorbed into a procedure stops
// being re-learned and the number falls (see metricValue's R11 case).
//
// Rows whose norm_title is still ” are excluded — ” is the "not folded yet"
// marker, not an identity, and grouping by it would invent one enormous bogus
// finding out of every unrelated pre-0070 lesson in the database.
func r11RecurringLesson(db *sql.DB, win window) ([]finding, error) {
	groups, err := lessonGroups(db, win)
	if err != nil {
		return nil, err
	}
	var out []finding
	for _, g := range groups {
		if len(g.tasks) < R11MinTasks {
			continue
		}
		ev := map[string]any{
			"window":     win,
			"counts":     map[string]int{"tasks": len(g.tasks)},
			"tasks":      g.tasks,
			"norm_title": g.norm,
			"title":      g.title,
			"limits":     map[string]any{"min_tasks": R11MinTasks},
		}
		if g.action != "" {
			ev["latest_action"] = g.action
		}
		out = append(out, finding{
			rule:       "R11",
			targetKind: "skill",
			target:     g.norm,
			title:      "Recurring lesson: " + capRunes(g.title, 80),
			detail:     r11Detail(g),
			evidence:   ev,
		})
	}
	sortFindings(out)
	return out, nil
}

// r11Detail writes the finding's prose. The shape the operator reads first is
// "how many tasks", then which ones, so the claim is checkable without opening
// the evidence JSON.
func r11Detail(g lessonGroup) string {
	slugs := g.tasks
	suffix := ""
	if len(slugs) > R11MaxSlugs {
		suffix = fmt.Sprintf(" (+%d more)", len(slugs)-R11MaxSlugs)
		slugs = slugs[:R11MaxSlugs]
	}
	// Plain quotes around %s rather than %q: %q Go-escapes any quote already in
	// the lesson title into \", which renders as noise in an operator-facing line.
	// docs/retro.md documents the shape as `"<title>" recurred in N tasks`.
	detail := fmt.Sprintf("\"%s\" recurred in %d tasks: %s%s.",
		capRunes(g.title, 160), len(g.tasks), strings.Join(slugs, ", "), suffix)
	if g.action != "" {
		detail += fmt.Sprintf(" Latest action: %s.", capRunes(g.action, 160))
	}
	return detail + " A lesson learned this often belongs in the procedure the next run reads, not in a finished task's retro."
}

// lessonGroups folds the window's lessons by norm_title, newest task first.
//
// Twin of internal/api/retro.go buildRetroLessonGroups — same join, same window
// predicate, same "first row wins the display fields" rule. They are duplicated
// rather than shared because internal/api imports this package (BaselineFor /
// Run from the handlers) and importing back would cycle; keep them in lockstep.
func lessonGroups(db *sql.DB, win window) ([]lessonGroup, error) {
	rows, err := db.Query(`
		SELECT l.norm_title, l.title, COALESCE(l.action, ''),
		       COALESCE(t.external_id, CAST(t.id AS TEXT))
		  FROM retro_lessons l
		  JOIN task_retros tr ON tr.id = l.retro_id
		  JOIN tasks t ON t.id = tr.task_id
		  JOIN projects p ON p.id = t.project_id
		 WHERE t.started_at >= ? AND t.started_at < ? AND p.archived = 0
		   AND l.norm_title <> ''
		 ORDER BY t.started_at DESC, t.external_id DESC, l.seq ASC`,
		win.From, win.To)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []lessonGroup
	index := map[string]int{}
	seen := map[string]bool{}
	for rows.Next() {
		var norm, title, action, externalID string
		if err := rows.Scan(&norm, &title, &action, &externalID); err != nil {
			return nil, err
		}
		i, known := index[norm]
		if !known {
			out = append(out, lessonGroup{norm: norm, title: title, action: action})
			i = len(out) - 1
			index[norm] = i
		}
		if key := norm + "\x00" + externalID; !seen[key] {
			seen[key] = true
			out[i].tasks = append(out[i].tasks, externalID)
		}
	}
	return out, rows.Err()
}

// lessonTaskCount is R11's scalar metric: how many distinct tasks learned this
// lesson inside the window. Lower is better (relImprovement's default
// direction), so a lesson that got absorbed into a procedure and stopped
// recurring reads as a real improvement rather than as missing data.
//
// Zero rows is ok=false, never a measured zero — that is this rule's activity
// floor, the same contract metricValue documents for R1/R2/R4/R10. A fortnight
// in which nobody wrote a retrospective is indistinguishable from here from one
// in which the lesson was finally absorbed, and reading it as base=3 -> cur=0
// would auto-verify a 100% improvement nobody made. R11 is not in
// selfCheckingRules today so nothing reaches that path, but the safety must not
// rest on one kind list in a different file.
func lessonTaskCount(db *sql.DB, norm string, win window) (float64, bool, error) {
	if norm == "" {
		return 0, false, nil
	}
	var n int64
	err := db.QueryRow(`
		SELECT COUNT(DISTINCT tr.task_id)
		  FROM retro_lessons l
		  JOIN task_retros tr ON tr.id = l.retro_id
		  JOIN tasks t ON t.id = tr.task_id
		  JOIN projects p ON p.id = t.project_id
		 WHERE t.started_at >= ? AND t.started_at < ? AND p.archived = 0
		   AND l.norm_title = ?`, win.From, win.To, norm).Scan(&n)
	if err != nil {
		return 0, false, err
	}
	if n == 0 {
		return 0, false, nil
	}
	return float64(n), true, nil
}
