package advisor

// agent-memory phase 3 — R10: the auto-memory index budget.
//
// R1–R6 are rates over stored events; R7 reads the architecture map off disk.
// R10 belongs with R7: it re-reads `<auto-memory>/MEMORY.md` every pass and
// re-counts it, so a consolidated index simply stops firing and resolveVanished
// closes the row (selfCheckingRules, advisor.go).
//
// The thing it measures is a standing cost, not an incident. MEMORY.md is loaded
// into EVERY conversation of the project, so its bytes are re-read on every turn
// forever, and every line describing work that is already finished is rent paid
// for nothing. Two independent triggers, because the two failure shapes differ:
// an index can be over budget while still being all-live (too many topics), and
// it can be small but mostly dead (a long tail of DONE lines).

import (
	"database/sql"
	"fmt"
	"log"
	"os"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/memconsolidate"
)

const (
	// R10IndexBytes: an index bigger than this is over budget on size alone —
	// it is re-read on every turn of every session in the project.
	R10IndexBytes = 6144
	// R10ClosedShare: or the index is dominated by finished work. Strictly
	// greater, so an exactly-half-closed index is not yet a finding.
	R10ClosedShare = 0.5
	// R10MinLines: the share test needs a denominator worth dividing by — a
	// 3-line index that is 2/3 closed is noise, not a budget problem.
	R10MinLines = 10
)

// r10MemoryIndex flags non-archived projects whose always-loaded auto-memory
// index is over the byte budget or dominated by closed entries.
//
// The auto-memory root comes from memconsolidate.AutoMemoryDir — the same
// resolver internal/api/memory.go uses for its `auto-memory` root — so this
// rule, the Memory page and `swarmery memory consolidate` can never disagree
// about which directory they are talking about. A project with no index on disk
// is skipped silently: that is the healthy state, not a finding.
func r10MemoryIndex(db *sql.DB, win window) ([]finding, error) {
	rows, err := db.Query(`SELECT path, slug FROM projects WHERE archived = 0 ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []finding
	for rows.Next() {
		var path, slug string
		if err := rows.Scan(&path, &slug); err != nil {
			return nil, err
		}
		st, serr := memconsolidate.Inspect(memconsolidate.AutoMemoryDir(path))
		if serr != nil {
			// No index is the healthy state. Anything else (permissions, a
			// directory where the file should be) is skipped too — one bad
			// project must not abort the whole advisor pass — but it is said
			// out loud: a skipped project looks to resolveVanished exactly like
			// a consolidated one, and an accepted R10 would be closed on it.
			if !os.IsNotExist(serr) {
				log.Printf("warn: advisor R10: %s: %v (skipped this pass)", slug, serr)
			}
			continue
		}
		if !r10Fires(st) {
			continue
		}
		out = append(out, finding{
			rule:       "R10",
			targetKind: "memory",
			target:     slug,
			title:      "Auto-memory index is over budget: " + slug,
			detail:     r10Detail(st),
			evidence: map[string]any{
				"window": win,
				"counts": map[string]int{
					"index_bytes":  st.IndexBytes,
					"index_lines":  st.TotalLines,
					"closed_lines": st.ClosedCount,
				},
				"closed_share": st.ClosedShare,
				"index_path":   st.IndexPath,
				"limits": map[string]any{
					"index_bytes":  R10IndexBytes,
					"closed_share": R10ClosedShare,
					"min_lines":    R10MinLines,
				},
			},
		})
	}
	sortFindings(out)
	return out, rows.Err()
}

// r10Fires is the two-trigger predicate, kept separate so the test can drive it
// with fixture indexes instead of a database.
func r10Fires(st memconsolidate.Stats) bool {
	if st.IndexBytes > R10IndexBytes {
		return true
	}
	return st.TotalLines >= R10MinLines && st.ClosedShare > R10ClosedShare
}

// r10Detail writes the finding's prose. The shape the operator reads first is
// the count — "index 8.1 KB, 28/53 lines closed (53%)".
func r10Detail(st memconsolidate.Stats) string {
	return fmt.Sprintf(
		"index %s, %d/%d lines closed (%.0f%%). MEMORY.md is loaded into every conversation in this project, so each line is re-read on every turn. Run `swarmery memory consolidate --project <path> --dry-run` to see which closed entries can move to memory/closed/ (entries with an open tail, and entries an open memory still [[links]] to, are held back).",
		memconsolidate.HumanBytes(st.IndexBytes), st.ClosedCount, st.TotalLines, st.ClosedShare*100)
}
