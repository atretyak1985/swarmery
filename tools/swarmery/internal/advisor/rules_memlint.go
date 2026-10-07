package advisor

// memory-engineering phase 1 — R13: auto-memory that states merged PRs as open.
//
// R10 measures the index's SIZE; R13 measures its TRUTH, for the one fact class
// that is cheap to verify offline. A memory line that names a PR and says it
// is still open ("PR #366 UNMERGED (needs a review approval)") is a claim; the
// project's own git history either carries the merge or it does not. Three
// such notes were found in this repo on 2026-10-07, each describing a PR that
// had merged weeks earlier, and every session since had planned around them.
//
// Like R7 and R10 it is self-checking: it re-reads the memory directory and
// re-runs the git lookups every pass, so a corrected line simply stops firing
// and resolveVanished closes the row. It never edits a memory file — the
// correction is the operator's, by hand or through the Memory page.

import (
	"database/sql"
	"errors"
	"log"
	"os"
	"strconv"
	"strings"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/memconsolidate"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/memlint"
)

// R13DetailFindings is how many findings the detail prose names; the rest are
// in the evidence blob and on the Memory page.
const R13DetailFindings = 3

// r13StaleMemory flags non-archived projects whose auto-memory holds at least
// one line claiming a PR is open when the project's git history already carries
// its merge. The directory comes from memconsolidate.AutoMemoryDir, the same
// resolver R10, the Memory page and the CLI use.
func r13StaleMemory(db *sql.DB, win window) ([]finding, error) {
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
		rep, lerr := memlint.Lint(path, memconsolidate.ClaudeDir())
		if lerr != nil {
			// No memory directory is the healthy state. Anything else is
			// skipped too — one bad project must not abort the pass — but said
			// out loud, because a skipped project looks to resolveVanished
			// exactly like a corrected one.
			if !os.IsNotExist(lerr) {
				log.Printf("warn: advisor R13: %s: %v (skipped this pass)", slug, lerr)
			}
			continue
		}
		if len(rep.Findings) == 0 {
			continue
		}
		out = append(out, finding{
			rule:       "R13",
			targetKind: "memory",
			target:     slug,
			title:      "Auto-memory states merged PRs as open: " + slug,
			detail:     r13Detail(rep),
			evidence: map[string]any{
				"window": win,
				"counts": map[string]int{
					"claims": rep.Claims,
					"stale":  len(rep.Findings),
				},
				"findings":   rep.Findings,
				"index_path": memconsolidate.IndexPath(rep.Dir),
			},
		})
	}
	sortFindings(out)
	return out, rows.Err()
}

// r13Detail names the first R13DetailFindings findings, one sentence each, and
// says how to see the rest.
func r13Detail(rep memlint.Report) string {
	var b strings.Builder
	n := len(rep.Findings)
	if n > R13DetailFindings {
		n = R13DetailFindings
	}
	for i := 0; i < n; i++ {
		if i > 0 {
			b.WriteString(" ")
		}
		b.WriteString(rep.Findings[i].Describe())
		b.WriteString(".")
	}
	if rest := len(rep.Findings) - n; rest > 0 {
		b.WriteString(" (+")
		b.WriteString(strconv.Itoa(rest))
		b.WriteString(" more)")
	}
	b.WriteString(" Every later session plans around these lines as if the PRs were still open. Fix the lines by hand (or on the project's Knowledge → Memory tab); `swarmery memory lint --project <path>` lists all of them.")
	return b.String()
}

// r13Metric is the stale count for a project slug: ok=false when the project
// is unknown or has no memory directory (absence of data never verifies).
func r13Metric(db *sql.DB, slug string) (float64, bool, error) {
	var path string
	if err := db.QueryRow(`SELECT path FROM projects WHERE slug = ?`, slug).Scan(&path); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return 0, false, nil
		}
		return 0, false, err
	}
	rep, lerr := memlint.Lint(path, memconsolidate.ClaudeDir())
	if lerr != nil {
		return 0, false, nil
	}
	return float64(len(rep.Findings)), true, nil
}
