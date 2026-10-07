package advisor

// memory-engineering gaps phase 3 — R14: a correction the operator keeps making.
//
// Every operator override the dashboard accepts — a lesson rewritten, dismissed
// or retired by hand, a triage verdict undone, a plan revision rejected, a
// classifier answer contradicted — lands as one operator_corrections row
// (migration 0102, internal/corrections). Each of those surfaces absorbed its
// correction and forgot it; the ledger is what lets the same correction be
// seen twice.
//
// R14 is the detector: fold the window's corrections by norm_key (the reason,
// or the after text, folded by wsingest.NormalizeLessonTitle — the identity
// R11 already uses for lessons) and raise the keys corrected R14MinCorrections
// or more times across R14MinRefs or more DISTINCT refs. One lesson edited
// twice is the operator finishing a thought, not a pattern — the second
// threshold is over refs, never over rows.
//
// The finding routes like R11: target_kind `skill`, so the improve loop's
// skill proposals (migration 0074) pick it up and the recommendation is the
// human gate before anything changes. And like R11 it is deliberately NOT in
// advisor.go's selfCheckingRules: it reads stored rows over a trailing window,
// so a fortnight in which nobody corrected anything looks identical to a
// fortnight in which the mistake stopped being made.

import (
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/corrections"
)

const (
	// R14MinCorrections: how many ledger rows a key needs inside the window.
	R14MinCorrections = 2
	// R14MinRefs: across how many DISTINCT refs those rows must be spread.
	R14MinRefs = 2
	// R14MaxRefs: how many refs the detail line names; the full list is in the
	// evidence JSON.
	R14MaxRefs = 5
)

// r14RepeatedCorrection flags correction identities repeated across refs
// inside the window.
func r14RepeatedCorrection(db *sql.DB, win window) ([]finding, error) {
	since, err := time.Parse(time.RFC3339, win.From)
	if err != nil {
		return nil, fmt.Errorf("R14: window from %q: %w", win.From, err)
	}
	groups, err := corrections.Groups(db, since)
	if err != nil {
		return nil, err
	}
	var out []finding
	for _, g := range groups {
		if g.Count < R14MinCorrections || len(g.Refs) < R14MinRefs {
			continue
		}
		out = append(out, finding{
			rule:       "R14",
			targetKind: "skill",
			target:     g.NormKey,
			title:      "Repeated operator correction: " + capRunes(g.Sample, 80),
			detail:     r14Detail(g),
			evidence: map[string]any{
				"window":   win,
				"counts":   map[string]int{"corrections": g.Count, "refs": len(g.Refs)},
				"refs":     g.Refs,
				"sources":  g.Sources,
				"norm_key": g.NormKey,
				"sample":   g.Sample,
				"latest":   g.Latest,
				"limits":   map[string]any{"min_corrections": R14MinCorrections, "min_refs": R14MinRefs},
			},
		})
	}
	sortFindings(out)
	return out, nil
}

// r14Detail writes the finding's prose: how many times, across which refs,
// from which surfaces — checkable without opening the evidence JSON. Plain
// quotes rather than %q for the same reason as r11Detail.
func r14Detail(g corrections.Group) string {
	refs := g.Refs
	suffix := ""
	if len(refs) > R14MaxRefs {
		suffix = fmt.Sprintf(" (+%d more)", len(refs)-R14MaxRefs)
		refs = refs[:R14MaxRefs]
	}
	return fmt.Sprintf("\"%s\" was corrected %d times across %s%s (%s).",
		capRunes(g.Sample, 160), g.Count, strings.Join(refs, ", "), suffix, strings.Join(g.Sources, ", ")) +
		" A correction the operator keeps making by hand belongs in the procedure that produced the mistake."
}

// correctionCount is R14's scalar metric: how many ledger rows carry the key
// inside the window. Lower is better. Zero rows is ok=false, never a measured
// zero — the same activity floor metricValue documents for R11: a window in
// which nobody corrected anything is not evidence the mistake stopped.
func correctionCount(db *sql.DB, key string, win window) (float64, bool, error) {
	if key == "" {
		return 0, false, nil
	}
	var n int64
	err := db.QueryRow(`SELECT COUNT(*) FROM operator_corrections
		WHERE norm_key = ? AND created_at >= ? AND created_at < ?`, key, win.From, win.To).Scan(&n)
	if err != nil {
		return 0, false, err
	}
	if n == 0 {
		return 0, false, nil
	}
	return float64(n), true, nil
}
