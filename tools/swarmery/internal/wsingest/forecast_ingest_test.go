package wsingest

import (
	"database/sql"
	"strings"
	"testing"
)

// The four fixture cases the format has to survive end to end — parsePlan over a
// real plan dir, then applyEpics into a real schema:
//
//	none | prior | prior + posterior | malformed
//
// The malformed one carries the contract that matters most: a forecast is DATA,
// never a fence. Its plan must INGEST — phases, checkboxes and all — and the
// problem must surface as a lint, not as a refused scan. A plan with a broken
// forecast that failed to index would be the daemon deciding a typo in a guess
// is worth losing a whole plan over.

const fcPrior = "## Forecast\n\n```yaml\n" +
	"kind: prior\nwritten_at: 2026-09-23T10:12:00Z\n" +
	"areas: [internal/ingest]\nsize_band: M\nduration_band: 30-90m\n" +
	"outcome: done\nconfidence: 0.7\n```\n"

const fcPosterior = "```yaml\n" +
	"kind: posterior\nwritten_at: 2026-09-23T13:40:00Z\n" +
	"areas: [internal/ingest, internal/api]\nsize_band: L\nduration_band: 90m-4h\n" +
	"outcome: partial\nconfidence: 0.9\n```\n"

// Every rule at once: an unknown kind, an unknown size band, an out-of-range
// confidence and no areas.
const fcMalformed = "## Forecast\n\n```yaml\n" +
	"kind: hunch\nsize_band: ENORMOUS\nduration_band: someday\n" +
	"outcome: shipped\nconfidence: 3.5\n```\n"

func forecastPlan(t *testing.T) string {
	t.Helper()
	return writePlan(t, map[string]string{
		"README.md": "# Epic\n\n| # | Phase | Doc | Depends on |\n|---|---|---|---|\n" +
			"| 1 | None | `phase-1.md` | — |\n" +
			"| 2 | Prior | `phase-2.md` | 1 |\n" +
			"| 3 | Both | `phase-3.md` | 2 |\n" +
			"| 4 | Malformed | `phase-4.md` | 3 |\n",
		"phase-1.md": "# Phase 1 — None\n\n## Acceptance criteria\n- [x] a\n- [ ] b\n\n## Completion Report\n",
		"phase-2.md": "# Phase 2 — Prior\n\n## Acceptance criteria\n- [ ] a\n\n" + fcPrior + "\n## Completion Report\n",
		"phase-3.md": "# Phase 3 — Both\n\n## Acceptance criteria\n- [x] a\n\n" + fcPrior + "\n" + fcPosterior +
			"\n## Completion Report\n\nShipped the thing.\n",
		"phase-4.md": "# Phase 4 — Malformed\n\n## Acceptance criteria\n- [ ] a\n\n" + fcMalformed + "\n## Completion Report\n",
	})
}

func TestParsePlanForecastFixtures(t *testing.T) {
	warn, warns := collectWarn(t)
	phases := parsePlan(forecastPlan(t), warn)
	if len(phases) != 4 {
		t.Fatalf("phases = %d, want 4", len(phases))
	}
	// A forecast must never cost the plan its structure.
	if got := len(*warns); got != 0 {
		t.Errorf("warnings = %v, want none — a forecast never warns the plan scan", *warns)
	}
	if phases[0].checkboxesTotal != 2 || phases[3].checkboxesTotal != 1 {
		t.Errorf("checkbox counts disturbed by the forecast sections: %+v", phases)
	}

	t.Run("none", func(t *testing.T) {
		if got := phases[0].forecasts; got != nil {
			t.Errorf("forecasts = %+v, want nil", got)
		}
		if phases[0].docHash != "" {
			t.Errorf("docHash = %q, want empty for a doc with no forecast", phases[0].docHash)
		}
	})

	t.Run("prior only", func(t *testing.T) {
		fs := phases[1].forecasts
		if len(fs) != 1 || fs[0].Kind != ForecastPrior {
			t.Fatalf("forecasts = %+v, want one prior", fs)
		}
		if fs[0].PostHoc {
			t.Error("postHoc = true, but the doc's Completion Report is an empty stub")
		}
		if len(LintForecasts(fs)) != 0 {
			t.Errorf("lints = %+v, want none", LintForecasts(fs))
		}
		if phases[1].docHash == "" {
			t.Error("docHash is empty for a doc that carries a forecast")
		}
	})

	t.Run("prior and posterior", func(t *testing.T) {
		fs := phases[2].forecasts
		if len(fs) != 2 {
			t.Fatalf("forecasts = %d, want 2", len(fs))
		}
		if fs[0].Kind != ForecastPrior || fs[1].Kind != ForecastPosterior {
			t.Errorf("kinds = %q, %q, want prior then posterior", fs[0].Kind, fs[1].Kind)
		}
		// This doc's Completion Report is FILLED, so its prior was written after
		// the work was reported done — a prediction that cannot have been one.
		if !fs[0].PostHoc {
			t.Error("prior.postHoc = false in a doc with a filled Completion Report")
		}
		if len(LintForecasts(fs)) != 0 {
			t.Errorf("lints = %+v, want none", LintForecasts(fs))
		}
	})

	t.Run("malformed ingests and lints", func(t *testing.T) {
		fs := phases[3].forecasts
		if len(fs) != 1 {
			t.Fatalf("forecasts = %d, want 1 — a malformed block is still read", len(fs))
		}
		got := map[string]bool{}
		for _, l := range LintForecasts(fs) {
			got[l.Code] = true
		}
		for _, want := range []string{LintUnknownKind, LintUnknownSizeBand,
			LintUnknownDurationBand, LintUnknownOutcome, LintBadConfidence, LintMissingAreas} {
			if !got[want] {
				t.Errorf("lint %s not raised; got %v", want, got)
			}
		}
	})
}

// The DB half of the same four cases: the rows land, the malformed plan still
// commits, and the values are stored VERBATIM so the operator can see the typo.
func TestApplyEpicsStoresForecasts(t *testing.T) {
	db := carryFixture(t)
	warn, _ := collectWarn(t)
	phases := parsePlan(forecastPlan(t), warn)
	applyPhases(t, db, phases)

	count := func(docSuffix string) int {
		t.Helper()
		var n int
		if err := db.QueryRow(`
			SELECT COUNT(*) FROM phase_forecasts f
			  JOIN epic_phases e ON e.id = f.phase_id
			 WHERE e.doc_path LIKE ?`, "%"+docSuffix).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	for doc, want := range map[string]int{"phase-1.md": 0, "phase-2.md": 1, "phase-3.md": 2, "phase-4.md": 1} {
		if got := count(doc); got != want {
			t.Errorf("%s: %d forecast rows, want %d", doc, got, want)
		}
	}

	// Four phases still indexed — the malformed forecast cost the plan nothing.
	var phaseRows int
	if err := db.QueryRow(`SELECT COUNT(*) FROM epic_phases WHERE workspace_task_id = ?`, carryTaskID).
		Scan(&phaseRows); err != nil {
		t.Fatal(err)
	}
	if phaseRows != 4 {
		t.Fatalf("epic_phases = %d, want 4 — the malformed forecast broke the ingest", phaseRows)
	}

	// Verbatim, not normalized: the operator has to SEE "ENORMOUS" to fix it.
	var (
		kind, sizeBand, areas, docHash string
		confidence                     sql.NullFloat64
		postHoc                        int
	)
	if err := db.QueryRow(`
		SELECT f.kind, f.size_band, f.areas_json, f.doc_hash, f.confidence, f.post_hoc
		  FROM phase_forecasts f JOIN epic_phases e ON e.id = f.phase_id
		 WHERE e.doc_path LIKE '%phase-4.md'`).
		Scan(&kind, &sizeBand, &areas, &docHash, &confidence, &postHoc); err != nil {
		t.Fatal(err)
	}
	if kind != "hunch" || sizeBand != "ENORMOUS" {
		t.Errorf("stored (kind, size_band) = (%q, %q), want the author's own words", kind, sizeBand)
	}
	if areas != "[]" {
		t.Errorf("areas_json = %q, want [] rather than null", areas)
	}
	if !confidence.Valid || confidence.Float64 != 3.5 {
		t.Errorf("confidence = %v, want 3.5 stored as written", confidence)
	}
	if docHash == "" {
		t.Error("doc_hash is empty — a later scoring pass cannot tell if the doc moved on")
	}

	// A prior in a doc whose Completion Report is filled is marked post hoc.
	if err := db.QueryRow(`
		SELECT f.post_hoc FROM phase_forecasts f JOIN epic_phases e ON e.id = f.phase_id
		 WHERE e.doc_path LIKE '%phase-3.md' AND f.kind = 'prior'`).Scan(&postHoc); err != nil {
		t.Fatal(err)
	}
	if postHoc != 1 {
		t.Error("post_hoc = 0 for a prior written into an already-reported phase")
	}
}

// Doc-owned, therefore retractable: deleting the `## Forecast` section must
// actually withdraw the forecast, the way deleting a `**Covers:**` line
// withdraws the coverage claim. Re-running the scan must also not duplicate it.
func TestApplyEpicsForecastsAreDocOwned(t *testing.T) {
	db := carryFixture(t)
	warn, _ := collectWarn(t)

	withForecast := parsePlan(forecastPlan(t), warn)
	applyPhases(t, db, withForecast)
	applyPhases(t, db, withForecast) // idempotent: a rescan replaces, never appends

	rows := func() int {
		t.Helper()
		var n int
		if err := db.QueryRow(`SELECT COUNT(*) FROM phase_forecasts`).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	if got := rows(); got != 4 {
		t.Fatalf("forecast rows after two identical scans = %d, want 4", got)
	}

	// The author deletes every `## Forecast` section.
	stripped := make([]epicPhase, len(withForecast))
	copy(stripped, withForecast)
	for i := range stripped {
		stripped[i].forecasts = nil
		stripped[i].docHash = ""
	}
	applyPhases(t, db, stripped)
	if got := rows(); got != 0 {
		t.Errorf("forecast rows after the sections were deleted = %d, want 0", got)
	}
}

// A phase doc removed from the plan takes its forecasts with it: phase_forecasts
// carries no foreign key (0079, following 0077's reasoning), so the sweep in
// applyEpics is the only thing standing between a deleted phase and rows that
// nothing will ever read or delete.
func TestApplyEpicsSweepsForecastsOfPrunedPhases(t *testing.T) {
	db := carryFixture(t)
	warn, _ := collectWarn(t)
	phases := parsePlan(forecastPlan(t), warn)
	applyPhases(t, db, phases)

	applyPhases(t, db, phases[:2]) // phases 3 and 4 dropped from the README

	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM phase_forecasts`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 { // only phase-2's prior survives (phase-1 never had one)
		t.Errorf("forecast rows after the prune = %d, want 1", n)
	}
	var orphans int
	if err := db.QueryRow(
		`SELECT COUNT(*) FROM phase_forecasts WHERE phase_id NOT IN (SELECT id FROM epic_phases)`).
		Scan(&orphans); err != nil {
		t.Fatal(err)
	}
	if orphans != 0 {
		t.Errorf("orphaned forecast rows = %d, want 0", orphans)
	}
}

// phase_actuals (0081) is swept by the same rule as phase_forecasts: a phase
// removed from the plan leaves no measured-run rows behind, and a surviving
// phase keeps its own.
func TestApplyEpicsSweepsActualsOfPrunedPhases(t *testing.T) {
	db := carryFixture(t)
	warn, _ := collectWarn(t)
	phases := parsePlan(forecastPlan(t), warn)
	applyPhases(t, db, phases)

	if _, err := db.Exec(`
		INSERT INTO phase_actuals (phase_id, session_uuid, computed_at)
		SELECT id, 'run-' || id, '2026-09-23T00:00:00Z' FROM epic_phases`); err != nil {
		t.Fatal(err)
	}
	applyPhases(t, db, phases[:2]) // phases 3 and 4 dropped from the README

	var n, orphans int
	if err := db.QueryRow(`SELECT COUNT(*) FROM phase_actuals`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Errorf("actuals rows after the prune = %d, want 2 (phases 1 and 2 survive)", n)
	}
	if err := db.QueryRow(
		`SELECT COUNT(*) FROM phase_actuals WHERE phase_id NOT IN (SELECT id FROM epic_phases)`).
		Scan(&orphans); err != nil {
		t.Fatal(err)
	}
	if orphans != 0 {
		t.Errorf("orphaned actuals rows = %d, want 0", orphans)
	}
}

// parserVersion is part of the identity of a parse result, not just the bytes:
// without a bump, a plan whose author ADDS a Forecast section to an
// already-indexed doc keeps zero forecast rows until some other byte changes.
func TestParserVersionBumpedForForecasts(t *testing.T) {
	if !strings.HasPrefix(parserVersion, "v") || parserVersion == "v6" {
		t.Errorf("parserVersion = %q — teaching parsePlan a new field must bump it past v6", parserVersion)
	}
}
