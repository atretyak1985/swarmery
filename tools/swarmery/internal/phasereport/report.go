// Package phasereport is the phase-run baseline: one table of counts over a
// [from, to] window that says how plan-phase runs ended, what the noops were
// waiting on, what the router and the verifier did, and how many finished phases
// came back. The API (GET /api/phaseruns/report), the Health "Phase runs" tab and
// `swarmery phase-report` all render THIS struct, so the three can never disagree.
//
// Bookkeeping, not statistics: there is no MinSamples gate (unlike internal/route
// and internal/calibration). A later phase compares windows row by row and needs
// every row, however small.
//
// Read-only. Every source table is read through a sqlite_master check first, so
// the report runs against a store older than the tables it reads — the CLI opens
// the live database WITHOUT migrating it — and reports a missing table as zero
// rows plus a note, never as an error.
package phasereport

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/modelid"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/phasediag"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/wsingest"
)

// DateLayout is the window's wire format.
const DateLayout = "2006-01-02"

// boundLayout is how a window edge is compared against stored timestamps. It
// carries no zone suffix on purpose: stored values are RFC3339 UTC with and
// without milliseconds ("…:05Z", "…:05.000Z"), and a bare "…:05" sorts before
// both, so `ts >= lo AND ts < hi` is exact for either shape.
const boundLayout = "2006-01-02T15:04:05"

// Row keys. Stable: the web tab and Phase 7's comparison key on them.
const (
	KeyRuns            = "runs"
	KeyCompleted       = "runs_completed"
	KeyPartial         = "runs_partial"
	KeyNoop            = "runs_noop"
	KeyFailed          = "runs_failed"
	KeyOther           = "runs_other"
	KeyNoopPushPR      = "noop_push_pr"
	KeyNoopManual      = "noop_manual"
	KeyNoopUnexplained = "noop_unexplained"
	KeyNoopRepeat      = "noop_repeat"
	KeyNoopAfterNoop   = "noop_after_noop"
	KeyNoopCost        = "noop_cost"
	KeyRouter          = "router_decisions"
	KeyRouterApplied   = "router_applied"
	KeyRouterDivergent = "router_divergent"
	KeyReviewRuns      = "review_runs"
	KeyReopens         = "reopens"
)

// Row is one line of the baseline table.
type Row struct {
	Key   string `json:"key"`
	Label string `json:"label"`
	N     int    `json:"n"`
	// CostUSD is set on cost rows only.
	CostUSD *float64 `json:"costUsd,omitempty"`
	// Estimated marks a row derived by a heuristic rather than read from a
	// recorded fact — the UI suffixes it "(est.)".
	Estimated bool `json:"estimated"`
}

// Fallback counts the runs the outcome rows CANNOT see: a phase whose last run
// ended in the window but has no phase_actuals row (runs before migration 0081,
// or a run whose actuals were never computed). epic_phases keeps only the LAST
// run of a phase, so this is a lower bound, and it is reported beside the main
// rows rather than folded into them — the baseline is the phase_actuals ledger.
type Fallback struct {
	N         int            `json:"n"`
	ByOutcome map[string]int `json:"byOutcome"`
}

// Report is the baseline over one window.
type Report struct {
	From string `json:"from"` // inclusive lower edge, RFC3339 UTC
	To   string `json:"to"`   // inclusive upper edge, RFC3339 UTC
	Rows []Row  `json:"rows"`
	// FallbackRows: see Fallback.
	FallbackRows Fallback `json:"fallbackRows"`
	// Notes say which sources were absent or approximated, in words.
	Notes []string `json:"notes"`
}

// Row returns the row with key, and false when there is none.
func (r Report) Row(key string) (Row, bool) {
	for _, row := range r.Rows {
		if row.Key == key {
			return row, true
		}
	}
	return Row{}, false
}

// ErrBadWindow is a malformed or inverted window.
var ErrBadWindow = errors.New("phasereport: bad window")

// ParseWindow turns the wire window into the instants Build takes. A
// YYYY-MM-DD `from` is 00:00:00 UTC that day and a YYYY-MM-DD `to` is 23:59:59
// UTC that day, both inclusive. An RFC3339 value is taken as the exact instant —
// the way to reproduce a baseline that was taken part-way through its last day.
func ParseWindow(from, to string) (time.Time, time.Time, error) {
	lo, _, err := parseEdge(from)
	if err != nil {
		return time.Time{}, time.Time{}, fmt.Errorf("%w: from: %v", ErrBadWindow, err)
	}
	hi, hiDay, err := parseEdge(to)
	if err != nil {
		return time.Time{}, time.Time{}, fmt.Errorf("%w: to: %v", ErrBadWindow, err)
	}
	if hiDay {
		hi = hi.Add(24*time.Hour - time.Second)
	}
	if hi.Before(lo) {
		return time.Time{}, time.Time{}, fmt.Errorf("%w: to is before from", ErrBadWindow)
	}
	return lo, hi, nil
}

// parseEdge parses one edge; day reports whether it was a bare date.
func parseEdge(s string) (t time.Time, day bool, err error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}, false, errors.New("missing")
	}
	if t, err := time.Parse(DateLayout, s); err == nil {
		return t.UTC(), true, nil
	}
	t, err = time.Parse(time.RFC3339, s)
	if err != nil {
		return time.Time{}, false, fmt.Errorf("%q is neither YYYY-MM-DD nor RFC3339", s)
	}
	return t.UTC(), false, nil
}

// window is the SQL form of [from, to]: ts >= lo AND ts < hi.
type window struct{ lo, hi string }

func newWindow(from, to time.Time) window {
	return window{
		lo: from.UTC().Format(boundLayout),
		hi: to.UTC().Truncate(time.Second).Add(time.Second).Format(boundLayout),
	}
}

// landRe / manualRe are the estimated split of a noop run: an unticked
// criterion that names a landing step (the run cannot push or open a PR) or a
// step only a person can take. The spec's single pattern, split in two. Go's
// \b is ASCII-only, so the Cyrillic words get a Unicode letter boundary instead.
var (
	landRe   = regexp.MustCompile(`(?i)\b(push|pull request|PR|merge|gh pr)\b`)
	manualRe = regexp.MustCompile(`(?i)(?:\b(?:production|manually|console)\b|(?:^|[^\p{L}\p{N}_])(?:прод|вручну)(?:$|[^\p{L}\p{N}_]))`)
)

// actualRun is one phase_actuals row, reduced to what the report reads.
type actualRun struct {
	id          int64
	phaseID     int64
	outcome     string
	startPoint  string
	sessionUUID string
	computedAt  string
	cost        sql.NullFloat64
}

// Build computes the baseline over [from, to], both inclusive.
func Build(db *sql.DB, from, to time.Time) (Report, error) {
	w := newWindow(from, to)
	rep := Report{
		From:         from.UTC().Format(time.RFC3339),
		To:           to.UTC().Format(time.RFC3339),
		FallbackRows: Fallback{ByOutcome: map[string]int{}},
		Notes:        []string{},
	}
	b := builder{db: db, w: w, rep: &rep}
	steps := []func() error{b.runs, b.fallback, b.router, b.verifier, b.reviews, b.reopens}
	for _, step := range steps {
		if err := step(); err != nil {
			return Report{}, err
		}
	}
	return rep, nil
}

type builder struct {
	db  *sql.DB
	w   window
	rep *Report
}

func (b builder) add(r Row) { b.rep.Rows = append(b.rep.Rows, r) }
func (b builder) note(format string, a ...any) {
	b.rep.Notes = append(b.rep.Notes, fmt.Sprintf(format, a...))
}

// hasTable reports whether name exists in the store.
func (b builder) hasTable(name string) (bool, error) {
	var n int
	err := b.db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = ?`, name).Scan(&n)
	if err != nil {
		return false, fmt.Errorf("phasereport: %w", err)
	}
	return n > 0, nil
}

// hasColumns reports whether table carries every one of cols.
func (b builder) hasColumns(table string, cols ...string) (bool, error) {
	rows, err := b.db.Query(`SELECT name FROM pragma_table_info(?)`, table)
	if err != nil {
		return false, fmt.Errorf("phasereport: %w", err)
	}
	defer rows.Close()
	have := map[string]bool{}
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			return false, fmt.Errorf("phasereport: %w", err)
		}
		have[n] = true
	}
	if err := rows.Err(); err != nil {
		return false, fmt.Errorf("phasereport: %w", err)
	}
	for _, c := range cols {
		if !have[c] {
			return false, nil
		}
	}
	return true, nil
}

// runs: outcome counts, the noop split, repeats and noop cost — all from
// phase_actuals, one row per run (the table's UNIQUE(session_uuid)).
func (b builder) runs() error {
	ok, err := b.hasTable("phase_actuals")
	if err != nil {
		return err
	}
	var runs []actualRun
	if ok {
		runs, err = b.actualRuns()
		if err != nil {
			return err
		}
	} else {
		b.note("phase_actuals is absent: no run rows")
	}

	byOutcome := map[string]int{}
	var noops []actualRun
	noopCost := 0.0
	for _, r := range runs {
		byOutcome[r.outcome]++
		if r.outcome == phasediag.OutcomeNoop {
			noops = append(noops, r)
			if r.cost.Valid {
				noopCost += r.cost.Float64
			}
		}
	}
	other := len(runs) - byOutcome[phasediag.OutcomeCompleted] - byOutcome[phasediag.OutcomePartial] -
		byOutcome[phasediag.OutcomeNoop] - byOutcome[phasediag.OutcomeFailed]
	b.add(Row{Key: KeyRuns, Label: "phase runs", N: len(runs)})
	b.add(Row{Key: KeyCompleted, Label: "completed", N: byOutcome[phasediag.OutcomeCompleted]})
	b.add(Row{Key: KeyPartial, Label: "partial", N: byOutcome[phasediag.OutcomePartial]})
	b.add(Row{Key: KeyNoop, Label: "noop", N: byOutcome[phasediag.OutcomeNoop]})
	b.add(Row{Key: KeyFailed, Label: "failed", N: byOutcome[phasediag.OutcomeFailed]})
	if other > 0 {
		b.add(Row{Key: KeyOther, Label: "other outcome", N: other})
	}

	pushPR, manual, estimated, err := b.noopSplit(noops)
	if err != nil {
		return err
	}
	b.add(Row{Key: KeyNoopPushPR, Label: "noop · waiting on push/PR", N: pushPR, Estimated: estimated})
	b.add(Row{Key: KeyNoopManual, Label: "noop · waiting on a manual step", N: manual, Estimated: estimated})
	b.add(Row{Key: KeyNoopUnexplained, Label: "noop · neither", N: len(noops) - pushPR - manual, Estimated: estimated})

	repeat, afterNoop, err := b.repeats(noops)
	if err != nil {
		return err
	}
	b.add(Row{Key: KeyNoopRepeat, Label: "noop repeat (same base as the noop before)", N: repeat})
	b.add(Row{Key: KeyNoopAfterNoop, Label: "noop after a noop", N: afterNoop})
	cost := noopCost
	b.add(Row{Key: KeyNoopCost, Label: "noop cost", N: len(noops), CostUSD: &cost})
	return nil
}

func (b builder) actualRuns() ([]actualRun, error) {
	rows, err := b.db.Query(`
		SELECT id, phase_id, outcome, start_point, session_uuid, computed_at, cost_usd
		  FROM phase_actuals
		 WHERE computed_at >= ? AND computed_at < ?
		 ORDER BY id`, b.w.lo, b.w.hi)
	if err != nil {
		return nil, fmt.Errorf("phasereport: runs: %w", err)
	}
	defer rows.Close()
	var out []actualRun
	for rows.Next() {
		var r actualRun
		if err := rows.Scan(&r.id, &r.phaseID, &r.outcome, &r.startPoint, &r.sessionUUID, &r.computedAt, &r.cost); err != nil {
			return nil, fmt.Errorf("phasereport: runs: %w", err)
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("phasereport: runs: %w", err)
	}
	return out, nil
}

// noopSplit counts noop runs waiting on a landing step and on a manual step.
//
// When epic_phases carries criteria_land_open / criteria_manual_open (a later
// phase of this plan), those recorded counts decide. Until then it is an
// ESTIMATE: landRe / manualRe over the run's own evidence —
//
//   - the run session's last assistant message (the executor's PHASE BLOCKED
//     line names what it could not do),
//   - the completion loop's run_events for this run (the blocked reason, and the
//     unticked list each continuation was sent),
//   - the phase doc's unticked criteria as they are on disk NOW (labels from
//     wsingest.UntickedCheckboxes; a later run may have ticked them since).
//
// A run counts once: landing wins over manual.
func (b builder) noopSplit(noops []actualRun) (pushPR, manual int, estimated bool, err error) {
	if len(noops) == 0 {
		return 0, 0, false, nil
	}
	recorded, err := b.hasColumns("epic_phases", "criteria_land_open", "criteria_manual_open")
	if err != nil {
		return 0, 0, false, err
	}
	if recorded {
		for _, r := range noops {
			var land, man int
			qerr := b.db.QueryRow(`
				SELECT COALESCE(criteria_land_open, 0), COALESCE(criteria_manual_open, 0)
				  FROM epic_phases WHERE id = ?`, r.phaseID).Scan(&land, &man)
			if errors.Is(qerr, sql.ErrNoRows) {
				continue
			}
			if qerr != nil {
				return 0, 0, false, fmt.Errorf("phasereport: noop split: %w", qerr)
			}
			switch {
			case land > 0:
				pushPR++
			case man > 0:
				manual++
			}
		}
		return pushPR, manual, false, nil
	}

	ev, err := b.evidence()
	if err != nil {
		return 0, 0, false, err
	}
	for _, r := range noops {
		texts, err := ev.of(r)
		if err != nil {
			return 0, 0, false, err
		}
		land, man := classify(texts)
		switch {
		case land:
			pushPR++
		case man:
			manual++
		}
	}
	if ev.missingDocs > 0 {
		b.note("%d noop run(s) had no phase doc to read; split on their transcript and run events only", ev.missingDocs)
	}
	b.note("noop push/PR and manual rows are estimated: the run's last message, its run events and the doc's unticked criteria")
	return pushPR, manual, true, nil
}

// evidence reads the texts a noop run is classified on; absent sources are
// skipped, never an error.
type evidenceReader struct {
	db                  *sql.DB
	hasEvents, hasTurns bool
	docs                map[int64]docEvidence
	missingDocs         int
}

// docEvidence is one phase doc's unticked criteria; found=false when the phase
// row or its doc is gone.
type docEvidence struct {
	found  bool
	labels []string
}

func (b builder) evidence() (*evidenceReader, error) {
	ev := &evidenceReader{db: b.db, docs: map[int64]docEvidence{}}
	var err error
	if ev.hasEvents, err = b.hasTable("run_events"); err != nil {
		return nil, err
	}
	turns, err := b.hasTable("turns")
	if err != nil {
		return nil, err
	}
	sessions, err := b.hasTable("sessions")
	if err != nil {
		return nil, err
	}
	ev.hasTurns = turns && sessions
	return ev, nil
}

func (ev *evidenceReader) of(r actualRun) ([]string, error) {
	var texts []string
	doc, seen := ev.docs[r.phaseID]
	if !seen {
		var docPath string
		qerr := ev.db.QueryRow(`SELECT doc_path FROM epic_phases WHERE id = ?`, r.phaseID).Scan(&docPath)
		switch {
		case errors.Is(qerr, sql.ErrNoRows):
		case qerr != nil:
			return nil, fmt.Errorf("phasereport: noop split: %w", qerr)
		default:
			if body, rerr := os.ReadFile(docPath); rerr == nil {
				doc = docEvidence{found: true, labels: wsingest.UntickedCheckboxes(string(body))}
			}
		}
		ev.docs[r.phaseID] = doc
	}
	if !doc.found {
		ev.missingDocs++
	}
	labels := doc.labels
	texts = append(texts, labels...)

	if ev.hasTurns && r.sessionUUID != "" {
		var last string
		qerr := ev.db.QueryRow(`
			SELECT t.text FROM turns t JOIN sessions s ON s.id = t.session_id
			 WHERE s.session_uuid = ? AND t.role = 'assistant' AND COALESCE(t.text, '') <> ''
			 ORDER BY t.seq DESC LIMIT 1`, r.sessionUUID).Scan(&last)
		if qerr != nil && !errors.Is(qerr, sql.ErrNoRows) {
			return nil, fmt.Errorf("phasereport: noop split: %w", qerr)
		}
		texts = append(texts, last)
	}

	if ev.hasEvents {
		// This run's events: the phase's, after the previous actuals row was
		// computed and up to this one.
		rows, qerr := ev.db.Query(`
			SELECT detail FROM run_events
			 WHERE engine = 'phaserun' AND subject_id = ? AND created_at <= ?
			   AND created_at > COALESCE((SELECT MAX(p.computed_at) FROM phase_actuals p
			                               WHERE p.phase_id = ? AND p.id < ?), '')`,
			r.phaseID, r.computedAt, r.phaseID, r.id)
		if qerr != nil {
			return nil, fmt.Errorf("phasereport: noop split: %w", qerr)
		}
		defer rows.Close()
		for rows.Next() {
			var d string
			if err := rows.Scan(&d); err != nil {
				return nil, fmt.Errorf("phasereport: noop split: %w", err)
			}
			texts = append(texts, d)
		}
		if err := rows.Err(); err != nil {
			return nil, fmt.Errorf("phasereport: noop split: %w", err)
		}
	}
	return texts, nil
}

// classify reports whether any text names a landing step, and any a manual one.
func classify(texts []string) (land, manual bool) {
	for _, l := range texts {
		if landRe.MatchString(l) {
			land = true
		}
		if manualRe.MatchString(l) {
			manual = true
		}
	}
	return land, manual
}

// repeats counts, per noop run in the window, whether the phase's PREVIOUS
// phase_actuals row (in or before the window) was a noop too — afterNoop — and,
// of those, the ones that ran from the same non-empty base commit as that noop:
// repeat, the same attempt made twice with nothing changed underneath it.
func (b builder) repeats(noops []actualRun) (repeat, afterNoop int, err error) {
	for _, r := range noops {
		var outcome, start string
		qerr := b.db.QueryRow(`
			SELECT outcome, start_point FROM phase_actuals
			 WHERE phase_id = ? AND id < ? ORDER BY id DESC LIMIT 1`, r.phaseID, r.id).Scan(&outcome, &start)
		if errors.Is(qerr, sql.ErrNoRows) {
			continue
		}
		if qerr != nil {
			return 0, 0, fmt.Errorf("phasereport: repeats: %w", qerr)
		}
		if outcome != phasediag.OutcomeNoop {
			continue
		}
		afterNoop++
		if start != "" && start == r.startPoint {
			repeat++
		}
	}
	return repeat, afterNoop, nil
}

// fallback counts phases whose last run ended in the window with no actuals row.
func (b builder) fallback() error {
	ok, err := b.hasTable("epic_phases")
	if err != nil || !ok {
		return err
	}
	hasActuals, err := b.hasTable("phase_actuals")
	if err != nil {
		return err
	}
	q := `
		SELECT e.run_state, e.checkboxes_total, e.checkboxes_done,
		       e.run_checkboxes_before, e.run_checkboxes_after
		  FROM epic_phases e
		 WHERE e.run_ended_at >= ? AND e.run_ended_at < ?
		   AND COALESCE(e.run_session_uuid, '') <> ''`
	if hasActuals {
		q += ` AND NOT EXISTS (SELECT 1 FROM phase_actuals a WHERE a.session_uuid = e.run_session_uuid)`
	}
	rows, err := b.db.Query(q, b.w.lo, b.w.hi)
	if err != nil {
		return fmt.Errorf("phasereport: fallback: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var (
			state         string
			total, live   int
			before, after sql.NullInt64
		)
		if err := rows.Scan(&state, &total, &live, &before, &after); err != nil {
			return fmt.Errorf("phasereport: fallback: %w", err)
		}
		b.rep.FallbackRows.N++
		b.rep.FallbackRows.ByOutcome[phasediag.OutcomeFromRow(state, total, live, before, after)]++
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("phasereport: fallback: %w", err)
	}
	if b.rep.FallbackRows.N > 0 {
		b.note("%d run(s) ended in the window with no phase_actuals row; counted in fallbackRows only",
			b.rep.FallbackRows.N)
	}
	return nil
}

// router: the complexity router's phaserun decisions.
func (b builder) router() error {
	ok, err := b.hasTable("route_decisions")
	if err != nil {
		return err
	}
	var total, applied, divergent int
	if ok {
		rows, err := b.db.Query(`
			SELECT applied, pick_model, used_model FROM route_decisions
			 WHERE surface = 'phaserun' AND created_at >= ? AND created_at < ?`, b.w.lo, b.w.hi)
		if err != nil {
			return fmt.Errorf("phasereport: router: %w", err)
		}
		defer rows.Close()
		for rows.Next() {
			var (
				app        int
				pick, used string
			)
			if err := rows.Scan(&app, &pick, &used); err != nil {
				return fmt.Errorf("phasereport: router: %w", err)
			}
			total++
			if app != 0 {
				applied++
			}
			if used != "" && family(pick) != family(used) {
				divergent++
			}
		}
		if err := rows.Err(); err != nil {
			return fmt.Errorf("phasereport: router: %w", err)
		}
	} else {
		b.note("route_decisions is absent: no router rows")
	}
	b.add(Row{Key: KeyRouter, Label: "router decisions (phaserun)", N: total})
	b.add(Row{Key: KeyRouterApplied, Label: "router · applied", N: applied})
	b.add(Row{Key: KeyRouterDivergent, Label: "router · pick ≠ ran", N: divergent})
	return nil
}

// family reduces a model name to its family, or its lower-cased base id.
func family(m string) string {
	if f := modelid.Family(m); f != "" {
		return f
	}
	return strings.ToLower(modelid.Base(m))
}

// verifierStatuses are the finished verdicts, in display order.
var verifierStatuses = []string{"pass", "fail", "inconclusive", "error"}

// verifier: verification_runs by verdict, for phase targets (the phase gate)
// and for every target (the fleet verifier as a whole), plus the phase verdicts'
// classes — the detail up to its first ": ".
func (b builder) verifier() error {
	ok, err := b.hasTable("verification_runs")
	if err != nil {
		return err
	}
	phase := map[string]int{}
	all := map[string]int{}
	classes := map[string]int{}
	if ok {
		rows, err := b.db.Query(`
			SELECT target_key, status, COALESCE(detail, '') FROM verification_runs
			 WHERE started_at >= ? AND started_at < ?`, b.w.lo, b.w.hi)
		if err != nil {
			return fmt.Errorf("phasereport: verifier: %w", err)
		}
		defer rows.Close()
		for rows.Next() {
			var target, status, detail string
			if err := rows.Scan(&target, &status, &detail); err != nil {
				return fmt.Errorf("phasereport: verifier: %w", err)
			}
			all[status]++
			if strings.HasPrefix(target, "phase:") {
				phase[status]++
				if status != "pass" && status != "running" {
					classes[verdictClass(detail)]++
				}
			}
		}
		if err := rows.Err(); err != nil {
			return fmt.Errorf("phasereport: verifier: %w", err)
		}
	} else {
		b.note("verification_runs is absent: no verifier rows")
	}
	for _, s := range verifierStatuses {
		b.add(Row{Key: "verifier_phase_" + s, Label: "verifier on phases · " + s, N: phase[s]})
	}
	names := make([]string, 0, len(classes))
	for c := range classes {
		names = append(names, c)
	}
	sort.Strings(names)
	for _, c := range names {
		b.add(Row{Key: "verifier_class:" + c, Label: "verifier on phases · class " + c, N: classes[c]})
	}
	for _, s := range verifierStatuses {
		b.add(Row{Key: "verifier_all_" + s, Label: "verifier, every target · " + s, N: all[s]})
	}
	return nil
}

// verdictClass is a verdict detail up to its first ": " ("" → "unspecified").
func verdictClass(detail string) string {
	d := strings.TrimSpace(detail)
	if i := strings.Index(d, ": "); i >= 0 {
		d = d[:i]
	}
	if d == "" {
		return "unspecified"
	}
	if r := []rune(d); len(r) > 60 {
		d = string(r[:60]) + "…"
	}
	return d
}

// reviews: phase_reviews rows (a later phase adds the table).
func (b builder) reviews() error {
	ok, err := b.hasTable("phase_reviews")
	if err != nil {
		return err
	}
	n := 0
	if ok {
		if err := b.db.QueryRow(`SELECT COUNT(*) FROM phase_reviews WHERE created_at >= ? AND created_at < ?`,
			b.w.lo, b.w.hi).Scan(&n); err != nil {
			return fmt.Errorf("phasereport: reviews: %w", err)
		}
	} else {
		b.note("phase_reviews is absent: review runs read 0")
	}
	b.add(Row{Key: KeyReviewRuns, Label: "review runs", N: n})
	return nil
}

// CaughtBy is phase_reopens.caught_by's vocabulary, in display order.
var CaughtBy = []string{"verifier", "review", "operator", "none"}

// reopens: phase_reopens by caught_by.
func (b builder) reopens() error {
	ok, err := b.hasTable("phase_reopens")
	if err != nil {
		return err
	}
	by := map[string]int{}
	total := 0
	if ok {
		rows, err := b.db.Query(`
			SELECT caught_by, COUNT(*) FROM phase_reopens
			 WHERE created_at >= ? AND created_at < ? GROUP BY caught_by`, b.w.lo, b.w.hi)
		if err != nil {
			return fmt.Errorf("phasereport: reopens: %w", err)
		}
		defer rows.Close()
		for rows.Next() {
			var (
				c string
				n int
			)
			if err := rows.Scan(&c, &n); err != nil {
				return fmt.Errorf("phasereport: reopens: %w", err)
			}
			by[c] = n
			total += n
		}
		if err := rows.Err(); err != nil {
			return fmt.Errorf("phasereport: reopens: %w", err)
		}
	} else {
		b.note("phase_reopens is absent: reopens read 0")
	}
	b.add(Row{Key: KeyReopens, Label: "reopened phases", N: total})
	for _, c := range CaughtBy {
		b.add(Row{Key: KeyReopens + "_" + c, Label: "reopened · caught by " + c, N: by[c]})
	}
	return nil
}

// Render writes the report as indented JSON, or as text: one row per line,
// labels left-aligned, numbers right-aligned, estimated rows suffixed "(est.)".
func Render(w io.Writer, r Report, asJSON bool) error {
	if asJSON {
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		return enc.Encode(r)
	}
	width := 0
	for _, row := range r.Rows {
		if n := len([]rune(row.Label)); n > width {
			width = n
		}
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "phase runs %s … %s\n", r.From, r.To)
	for _, row := range r.Rows {
		pad := strings.Repeat(" ", width-len([]rune(row.Label)))
		line := fmt.Sprintf("  %s%s  %6d", row.Label, pad, row.N)
		if row.CostUSD != nil {
			line += fmt.Sprintf("  $%.2f", *row.CostUSD)
		}
		if row.Estimated {
			line += "  (est.)"
		}
		sb.WriteString(line + "\n")
	}
	if r.FallbackRows.N > 0 {
		keys := make([]string, 0, len(r.FallbackRows.ByOutcome))
		for k := range r.FallbackRows.ByOutcome {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		parts := make([]string, 0, len(keys))
		for _, k := range keys {
			parts = append(parts, fmt.Sprintf("%s %d", k, r.FallbackRows.ByOutcome[k]))
		}
		fmt.Fprintf(&sb, "fallback (no actuals row): %d — %s\n", r.FallbackRows.N, strings.Join(parts, ", "))
	}
	for _, n := range r.Notes {
		sb.WriteString("note: " + n + "\n")
	}
	_, err := io.WriteString(w, sb.String())
	return err
}
