package wsingest

import (
	"database/sql"
	"path/filepath"
	"testing"
	"time"
)

// ── the pure half ────────────────────────────────────────────────────────────

func ts(t *testing.T, s string) time.Time {
	t.Helper()
	v, err := time.Parse(time.RFC3339, s)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func TestDocEditOrderingPostHoc(t *testing.T) {
	early := ts(t, "2026-09-23T10:00:00Z")
	late := ts(t, "2026-09-23T11:00:00Z")

	cases := []struct {
		name string
		o    DocEditOrdering
		want bool
	}{
		{"posterior before the first other edit is a prediction",
			DocEditOrdering{PosteriorAt: early, FirstOtherChangeAt: late}, false},
		{"posterior after the first other edit is post hoc",
			DocEditOrdering{PosteriorAt: late, FirstOtherChangeAt: early}, true},
		{"same instant is the same turn, not a violation",
			DocEditOrdering{PosteriorAt: early, FirstOtherChangeAt: early}, false},
		{"no posterior edit in the transcript is no evidence",
			DocEditOrdering{FirstOtherChangeAt: early}, false},
		{"the posterior was the run's only change",
			DocEditOrdering{PosteriorAt: late}, false},
		{"nothing observed at all",
			DocEditOrdering{}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.o.PostHoc(); got != tc.want {
				t.Errorf("PostHoc() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestEscapeLikeNeutralizesWildcards(t *testing.T) {
	// `_` is the one that bites: phase-4-line_items.md must not also match
	// phase-4-lineXitems.md.
	if got := escapeLike("phase-4-line_items.md"); got != `phase-4-line\_items.md` {
		t.Errorf("escapeLike = %q", got)
	}
	if got := docBase(`C:\plans\phase-2.md`); got != "phase-2.md" {
		t.Errorf("docBase on a windows path = %q", got)
	}
}

// ── the transcript half ──────────────────────────────────────────────────────

const orderingSessionUUID = "11111111-2222-4333-8444-555555555555"

// seedSession inserts the run session the ordering check reads through.
func seedSession(t *testing.T, db *sql.DB) {
	t.Helper()
	mustExec(t, db, `INSERT INTO sessions (id, project_id, session_uuid, started_at)
		VALUES (7, 1, ?, '2026-09-23T09:00:00Z')`, orderingSessionUUID)
}

// seedChange records one Edit: an events row carrying the timestamp and a
// file_changes row carrying the path and the patch, exactly the pair ingest
// writes for an Edit/Write tool result.
func seedChange(t *testing.T, db *sql.DB, id int64, at, path, diff string) {
	t.Helper()
	mustExec(t, db, `INSERT INTO events (id, session_id, ts, type, tool_name, dedup_key)
		VALUES (?, 7, ?, 'tool_call', 'Edit', ?)`, id, at, "dedup-"+at+path)
	mustExec(t, db, `INSERT INTO file_changes (event_id, session_id, file_path, change_type, additions, deletions, diff)
		VALUES (?, 7, ?, 'edit', 1, 0, ?)`, id, path, diff)
}

const posteriorPatch = "@@ -10,0 +11,3 @@\n+```yaml\n+kind: posterior\n+areas: [internal/api]\n"
const codePatch = "@@ -1,1 +1,1 @@\n-old\n+new\n"

// THE ACCEPTANCE CRITERION of step 11.3: a posterior written to the phase doc
// after the run had already changed another file is stored with post_hoc = 1.
// Driven through the real scan (parsePlan → applyEpics), not through the query
// in isolation, because the thing that can break is the wiring.
func TestPosteriorAfterFirstEditIsPostHoc(t *testing.T) {
	db := carryFixture(t)
	seedSession(t, db)
	warn, _ := collectWarn(t)
	planDir := forecastPlan(t)
	phases := parsePlan(planDir, warn)

	// First scan: the phase rows appear, and the run session is attached to
	// phase-3 (the fixture doc that carries prior + posterior) the way a phase run
	// would have attached it.
	applyPhases(t, db, phases)
	mustExec(t, db, `UPDATE epic_phases SET run_session_uuid = ?
		WHERE workspace_task_id = ? AND doc_path LIKE '%phase-3.md'`, orderingSessionUUID, carryTaskID)

	// The run edited code FIRST, and only then wrote its forecast — in a
	// worktree copy of the doc, which is what phaserun actually lends the
	// executor, so the check must match on the basename and not the plan path.
	seedChange(t, db, 100, "2026-09-23T12:00:00Z", "/wt/internal/api/epics.go", codePatch)
	seedChange(t, db, 101, "2026-09-23T12:30:00Z", "/wt/plan/phase-3.md", posteriorPatch)

	applyPhases(t, db, phases) // rescan: the ordering check runs here

	posterior := readForecast(t, db, "phase-3.md", ForecastPosterior)
	if posterior.postHoc != 1 {
		t.Errorf("posterior post_hoc = %d, want 1 — it was written after the run's first edit", posterior.postHoc)
	}
	if posterior.reason != PostHocAfterFirstEdit {
		t.Errorf("posterior post_hoc_reason = %q, want %q", posterior.reason, PostHocAfterFirstEdit)
	}

	// The prior in the same doc keeps ITS OWN reason. The two signals share the
	// flag on purpose (one predicate for calibration) and must stay tellable
	// apart, because they are two different mistakes to fix.
	prior := readForecast(t, db, "phase-3.md", ForecastPrior)
	if prior.postHoc != 1 || prior.reason != PostHocReportFilled {
		t.Errorf("prior = (post_hoc %d, reason %q), want (1, %q)", prior.postHoc, prior.reason, PostHocReportFilled)
	}
}

// The mirror case, and the one a false positive would quietly destroy: a
// posterior written BEFORE the run touched anything else is a real prediction
// and must stay in calibration's sample.
func TestPosteriorBeforeFirstEditIsNotPostHoc(t *testing.T) {
	db := carryFixture(t)
	seedSession(t, db)
	warn, _ := collectWarn(t)
	phases := parsePlan(forecastPlan(t), warn)
	applyPhases(t, db, phases)
	mustExec(t, db, `UPDATE epic_phases SET run_session_uuid = ?
		WHERE workspace_task_id = ? AND doc_path LIKE '%phase-3.md'`, orderingSessionUUID, carryTaskID)

	seedChange(t, db, 100, "2026-09-23T12:00:00Z", "/wt/plan/phase-3.md", posteriorPatch)
	seedChange(t, db, 101, "2026-09-23T12:30:00Z", "/wt/internal/api/epics.go", codePatch)

	applyPhases(t, db, phases)

	if got := readForecast(t, db, "phase-3.md", ForecastPosterior); got.postHoc != 0 || got.reason != "" {
		t.Errorf("posterior = (post_hoc %d, reason %q), want (0, \"\") — it predicted before it edited",
			got.postHoc, got.reason)
	}
}

// No run session, or a session nothing was ingested for: absent evidence is NOT
// evidence. Flagging here would delete a real prediction from the learning
// loop's training set on the strength of a transcript that had not arrived yet.
func TestPosteriorWithoutTranscriptIsNotFlagged(t *testing.T) {
	db := carryFixture(t)
	warn, _ := collectWarn(t)
	phases := parsePlan(forecastPlan(t), warn)
	applyPhases(t, db, phases) // no session attached at all

	if got := readForecast(t, db, "phase-3.md", ForecastPosterior); got.postHoc != 0 {
		t.Errorf("posterior post_hoc = %d with no transcript, want 0", got.postHoc)
	}

	seedSession(t, db)
	mustExec(t, db, `UPDATE epic_phases SET run_session_uuid = ?
		WHERE workspace_task_id = ? AND doc_path LIKE '%phase-3.md'`, orderingSessionUUID, carryTaskID)
	applyPhases(t, db, phases) // session exists, but it has no file changes

	if got := readForecast(t, db, "phase-3.md", ForecastPosterior); got.postHoc != 0 {
		t.Errorf("posterior post_hoc = %d with an empty transcript, want 0", got.postHoc)
	}
}

// An edit to the phase doc that does NOT add a posterior (ticking a checkbox,
// filling the Completion Report) must not be mistaken for the forecast.
func TestOrderingIgnoresUnrelatedDocEdits(t *testing.T) {
	db := carryFixture(t)
	seedSession(t, db)
	warn, _ := collectWarn(t)
	phases := parsePlan(forecastPlan(t), warn)
	applyPhases(t, db, phases)
	mustExec(t, db, `UPDATE epic_phases SET run_session_uuid = ?
		WHERE workspace_task_id = ? AND doc_path LIKE '%phase-3.md'`, orderingSessionUUID, carryTaskID)

	seedChange(t, db, 100, "2026-09-23T12:00:00Z", "/wt/plan/phase-3.md", "@@ -3,1 +3,1 @@\n-- [ ] a\n+- [x] a\n")
	seedChange(t, db, 101, "2026-09-23T12:10:00Z", "/wt/internal/api/epics.go", codePatch)
	seedChange(t, db, 102, "2026-09-23T12:20:00Z", "/wt/plan/phase-3.md", posteriorPatch)

	applyPhases(t, db, phases)

	if got := readForecast(t, db, "phase-3.md", ForecastPosterior); got.postHoc != 1 {
		t.Errorf("post_hoc = %d, want 1 — the tick at 12:00 is not the forecast; the 12:20 block is", got.postHoc)
	}
}

// A run in ANOTHER session must not decide this phase's forecast.
func TestOrderingIsScopedToThePhaseRunSession(t *testing.T) {
	db := carryFixture(t)
	seedSession(t, db)
	mustExec(t, db, `INSERT INTO sessions (id, project_id, session_uuid, started_at)
		VALUES (8, 1, 'other-session', '2026-09-23T09:00:00Z')`)
	warn, _ := collectWarn(t)
	phases := parsePlan(forecastPlan(t), warn)
	applyPhases(t, db, phases)
	mustExec(t, db, `UPDATE epic_phases SET run_session_uuid = ?
		WHERE workspace_task_id = ? AND doc_path LIKE '%phase-3.md'`, orderingSessionUUID, carryTaskID)

	// Session 8 edited code long before session 7 wrote the posterior.
	mustExec(t, db, `INSERT INTO events (id, session_id, ts, type, tool_name, dedup_key)
		VALUES (200, 8, '2026-09-23T08:00:00Z', 'tool_call', 'Edit', 'dedup-other')`)
	mustExec(t, db, `INSERT INTO file_changes (event_id, session_id, file_path, change_type, diff)
		VALUES (200, 8, '/wt/internal/api/epics.go', 'edit', ?)`, codePatch)
	seedChange(t, db, 100, "2026-09-23T12:00:00Z", "/wt/plan/phase-3.md", posteriorPatch)

	applyPhases(t, db, phases)

	if got := readForecast(t, db, "phase-3.md", ForecastPosterior); got.postHoc != 0 {
		t.Errorf("post_hoc = %d, want 0 — another session's edits are not this run's", got.postHoc)
	}
}

type storedForecastRow struct {
	postHoc int
	reason  string
}

func readForecast(t *testing.T, db *sql.DB, docSuffix, kind string) storedForecastRow {
	t.Helper()
	var r storedForecastRow
	err := db.QueryRow(`
		SELECT f.post_hoc, f.post_hoc_reason
		  FROM phase_forecasts f JOIN epic_phases e ON e.id = f.phase_id
		 WHERE e.doc_path LIKE ? AND f.kind = ?`, "%"+filepath.Base(docSuffix), kind).
		Scan(&r.postHoc, &r.reason)
	if err != nil {
		t.Fatalf("read %s forecast of %s: %v", kind, docSuffix, err)
	}
	return r
}
