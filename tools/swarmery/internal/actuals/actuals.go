// Package actuals records what a finished phase run ACTUALLY did — the
// measured half of the learning loop's forecast-vs-actual comparison (learning
// loop phase 12). One phase_actuals row per run (migration 0081).
//
// DETERMINISTIC, NO LLM. Every column is derived from something the daemon
// already has or can ask git for:
//
//	files / areas / lines / size   git numstat of the run branch vs its pinned start point
//	duration                       epic_phases.run_started_at → run_ended_at
//	cost                           SUM(turns.cost_usd) of the run session
//	outcome                        phasediag.OutcomeFromRow — the same derivation the Plans chip uses
//	verify verdict                 the latest verification_runs row for this phase started during this run
//	test failures (+ unexpected)   test_run events of the run session, judged against the phase forecast
//	continuations                  run_events kind='continuation' for this run
//	model fallback                 first vs last assistant model of the run session (modelid.IsFallback)
//
// NULL MEANS UNKNOWN. A missing input leaves its field nil; nothing is ever
// defaulted to a value that would read as a measurement.
//
// ADVISORY. Nothing gates on these rows, and Recorder.AfterRun — the one entry
// point a run's exit path calls — logs every failure and swallows panics: a
// measurement that could not be taken must never turn a run that shipped work
// into a failed one.
package actuals

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"path/filepath"
	"strings"
	"time"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/gitstat"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/modelid"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/phasediag"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/phaserun"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/verify"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/wsingest"
)

// Sources: which pass wrote a row.
const (
	// SourceRunEnd is written from the run's own exit path, the moment it ends.
	SourceRunEnd = "run-end"
	// SourceRunEndSettled is the same run re-measured SettleDelay later. The
	// transcript is ingested by a separate pipeline that lags the process exit,
	// so the first pass can miss the last turns' cost, a final test run, or the
	// model the run finished on; the second pass converges them.
	SourceRunEndSettled = "run-end-settled"
	// SourceBackfill is `swarmery actuals backfill` over past runs.
	SourceBackfill = "backfill"
)

// DefaultSettleDelay is how long after a run ends the settled pass re-measures.
const DefaultSettleDelay = 2 * time.Minute

var (
	// ErrNoRun: the phase has no recorded run to measure.
	ErrNoRun = errors.New("phase has no recorded run")
	// ErrRunMoved: the phase's current run is not the one the caller asked about
	// (a newer run started since). Its actuals belong to that newer run.
	ErrRunMoved = errors.New("phase run has moved on")
	// ErrNotFinished: the run is still in flight.
	ErrNotFinished = errors.New("phase run has not finished")
)

// Git is the read-only git boundary (worktree.Git satisfies it): run
// `git -C dir args…`, combined output.
type Git interface {
	Run(dir string, args ...string) (string, error)
}

// Actuals is one phase_actuals row. Pointer fields are NULL when nil.
type Actuals struct {
	PhaseID     int64
	SessionUUID string
	RunState    string
	Branch      string
	StartPoint  string

	Files        []gitstat.FileStat // nil ⇒ not measured; empty ⇒ measured, nothing changed
	Areas        []string           // nil iff Files is nil
	AreaDepth    int
	LinesAdded   *int
	LinesRemoved *int
	SizeBand     *string

	DurationS     *int64
	CostUSD       *float64
	Outcome       string
	VerifyVerdict *string

	TestFailures           *int
	TestFailuresUnexpected *int
	Continuations          *int
	ModelFallback          *bool

	Source     string
	ComputedAt string

	// DiffNote says why Files is nil ("" when it was measured). Not stored: it is
	// for the caller that decides whether a row without a diff is worth keeping
	// (the backfill skips it; the run-end pass keeps the rest of the row).
	DiffNote string
}

// Recorder computes and stores actuals. DB is required; Git may be nil, in
// which case nothing git-derived is measured (the rest of the row still is).
type Recorder struct {
	DB  *sql.DB
	Git Git
	// SettleDelay is the gap before AfterRun's second pass; 0 disables it.
	SettleDelay time.Duration
	// After schedules f after d (test seam; nil ⇒ time.AfterFunc).
	After func(d time.Duration, f func())
	// Now is the clock (test seam; nil ⇒ time.Now).
	Now func() time.Time
	// OnRecorded is called after a run's row is stored — by the run-end pass, the
	// settled pass and the backfill alike — with the source that wrote it. The
	// daemon wires internal/surprise here, so a run is (re)scored against its
	// forecast every time its actuals change. nil ⇒ nothing downstream. Advisory:
	// a panic in the callee is recovered and logged.
	OnRecorded func(phaseID int64, sessionUUID, source string)
}

// recorded runs the OnRecorded hook, never letting it fail the caller.
func (r *Recorder) recorded(a Actuals) {
	if r.OnRecorded == nil {
		return
	}
	defer func() {
		if p := recover(); p != nil {
			log.Printf("error: actuals: phase=%d uuid=%s downstream hook panicked: %v", a.PhaseID, a.SessionUUID, p)
		}
	}()
	r.OnRecorded(a.PhaseID, a.SessionUUID, a.Source)
}

// NewRecorder builds a Recorder with production defaults.
func NewRecorder(db *sql.DB, git Git) *Recorder {
	return &Recorder{DB: db, Git: git, SettleDelay: DefaultSettleDelay}
}

func (r *Recorder) now() time.Time {
	if r.Now != nil {
		return r.Now()
	}
	return time.Now()
}

func (r *Recorder) after(d time.Duration, f func()) {
	if r.After != nil {
		r.After(d, f)
		return
	}
	time.AfterFunc(d, f)
}

// AfterRun is the phase run's exit hook (phaserun.Service.Actuals): measure the
// run now, then once more after SettleDelay. Advisory end to end — every failure
// is logged and dropped, and a panic is recovered, because nothing about a
// measurement may change how the run itself is reported.
func (r *Recorder) AfterRun(phaseID int64, sessionUUID, repoRoot string) {
	r.recordLogged(phaseID, sessionUUID, repoRoot, SourceRunEnd)
	if r.SettleDelay > 0 {
		r.after(r.SettleDelay, func() {
			r.recordLogged(phaseID, sessionUUID, repoRoot, SourceRunEndSettled)
		})
	}
}

func (r *Recorder) recordLogged(phaseID int64, sessionUUID, repoRoot, source string) {
	defer func() {
		if p := recover(); p != nil {
			log.Printf("error: actuals: phase=%d uuid=%s %s pass panicked: %v", phaseID, sessionUUID, source, p)
		}
	}()
	a, err := r.Record(phaseID, sessionUUID, repoRoot, source, true)
	switch {
	case errors.Is(err, ErrRunMoved), errors.Is(err, ErrNotFinished):
		// A newer run owns the row now; its own exit will measure it.
		log.Printf("actuals: phase=%d uuid=%s %s pass skipped: %v", phaseID, sessionUUID, source, err)
	case err != nil:
		log.Printf("warning: actuals: phase=%d uuid=%s %s pass: %v", phaseID, sessionUUID, source, err)
	case a.DiffNote != "":
		log.Printf("warning: actuals: phase=%d uuid=%s recorded without a diff: %s", phaseID, sessionUUID, a.DiffNote)
	}
}

// Record computes one run's actuals and stores them (upsert on the session).
// eventsRecorded says run_events were being written for this run — true for a
// run that just ended under this daemon, so "no continuation events" is a real
// 0; false for a backfilled run, where it may only mean "predates the table".
func (r *Recorder) Record(phaseID int64, sessionUUID, repoRoot, source string, eventsRecorded bool) (Actuals, error) {
	a, err := r.Compute(phaseID, sessionUUID, repoRoot, eventsRecorded)
	if err != nil {
		return a, err
	}
	a.Source = source
	if err := r.Store(a); err != nil {
		return a, err
	}
	r.recorded(a)
	return a, nil
}

// runRow is the epic_phases read Compute starts from.
type runRow struct {
	sessionUUID, runState, branch, startPoint string
	startedAt, endedAt, runError              string
	total, live                               int
	before, after                             sql.NullInt64
	projectPath, workspaceRoot                string
}

func (r *Recorder) loadRun(phaseID int64) (runRow, error) {
	var (
		row                                  runRow
		uuid, state, branch, start           sql.NullString
		started, ended, runErr, path, wsRoot sql.NullString
	)
	err := r.DB.QueryRow(`
		SELECT e.run_session_uuid, e.run_state, e.run_branch, e.run_start_point,
		       e.run_started_at, e.run_ended_at, e.run_error,
		       e.checkboxes_total, e.checkboxes_done,
		       e.run_checkboxes_before, e.run_checkboxes_after,
		       p.path, w.root_path
		  FROM epic_phases e
		  JOIN tasks t ON t.id = e.workspace_task_id
		  JOIN projects p ON p.id = t.project_id
		  LEFT JOIN workspaces w ON w.project_id = p.id
		 WHERE e.id = ?`, phaseID).Scan(
		&uuid, &state, &branch, &start, &started, &ended, &runErr,
		&row.total, &row.live, &row.before, &row.after, &path, &wsRoot)
	if errors.Is(err, sql.ErrNoRows) {
		return row, fmt.Errorf("%w: phase %d not found", ErrNoRun, phaseID)
	}
	if err != nil {
		return row, err
	}
	row.sessionUUID, row.runState = uuid.String, state.String
	row.branch, row.startPoint = branch.String, start.String
	row.startedAt, row.endedAt, row.runError = started.String, ended.String, runErr.String
	row.projectPath, row.workspaceRoot = path.String, wsRoot.String
	return row, nil
}

// Compute measures a phase's CURRENT run without storing anything.
// sessionUUID, when non-empty, must match the phase's current run (else
// ErrRunMoved). repoRoot is the repository the run executed in; "" skips the
// git measurement.
func (r *Recorder) Compute(phaseID int64, sessionUUID, repoRoot string, eventsRecorded bool) (Actuals, error) {
	row, err := r.loadRun(phaseID)
	if err != nil {
		return Actuals{}, err
	}
	if row.sessionUUID == "" {
		return Actuals{}, fmt.Errorf("%w: phase %d", ErrNoRun, phaseID)
	}
	if sessionUUID != "" && sessionUUID != row.sessionUUID {
		return Actuals{}, fmt.Errorf("%w: phase %d is on run %s, not %s", ErrRunMoved, phaseID, row.sessionUUID, sessionUUID)
	}
	switch row.runState {
	case "", "idle", "running":
		return Actuals{}, fmt.Errorf("%w: phase %d is %q", ErrNotFinished, phaseID, row.runState)
	}

	a := Actuals{
		PhaseID:     phaseID,
		SessionUUID: row.sessionUUID,
		RunState:    row.runState,
		Branch:      row.branch,
		StartPoint:  row.startPoint,
		ComputedAt:  r.now().UTC().Format(time.RFC3339),
		Outcome:     phasediag.OutcomeFromRow(row.runState, row.total, row.live, row.before, row.after),
	}

	a.AreaDepth = AreaDepth(projectJSONs(repoRoot, row)...)
	r.measureDiff(&a, repoRoot)
	a.DurationS = duration(row)
	a.VerifyVerdict, err = r.verdict(phaseID, row.startedAt)
	if err != nil {
		return a, err
	}
	a.Continuations, err = r.continuations(phaseID, row.sessionUUID, eventsRecorded)
	if err != nil {
		return a, err
	}
	if err := r.sessionFacts(&a); err != nil {
		return a, err
	}
	return a, nil
}

// projectJSONs lists the project.json files an area-depth knob may live in, in
// priority order: the repo the run executed in, the workspace overlay, the
// project root (the same sources phaserun's repo resolution reads).
func projectJSONs(repoRoot string, row runRow) []string {
	var out []string
	if repoRoot != "" {
		out = append(out, filepath.Join(repoRoot, ".claude", "project.json"))
	}
	if row.workspaceRoot != "" {
		out = append(out, filepath.Join(row.workspaceRoot, "overlay", "project.json"))
	}
	if row.projectPath != "" {
		out = append(out, filepath.Join(row.projectPath, ".claude", "project.json"))
	}
	return out
}

// measureDiff fills the git-derived fields, or leaves them nil with DiffNote
// saying why.
func (r *Recorder) measureDiff(a *Actuals, repoRoot string) {
	switch {
	case r.Git == nil:
		a.DiffNote = "no git boundary wired"
		return
	case repoRoot == "":
		a.DiffNote = "run repository unknown"
		return
	case a.Branch == "":
		a.DiffNote = "no run branch recorded"
		return
	case a.StartPoint == "":
		a.DiffNote = "no start point recorded (run predates migration 0057)"
		return
	}
	if !BranchExists(r.Git, repoRoot, a.Branch) {
		a.DiffNote = "run branch " + a.Branch + " no longer exists"
		return
	}
	// Three dots: measured from the merge base, so commits that landed on the
	// base after the run started are not counted as the run's work.
	out, err := r.Git.Run(repoRoot, "-c", "core.quotepath=false",
		"diff", "--numstat", "--no-renames", a.StartPoint+"..."+a.Branch, "--")
	if err != nil {
		a.DiffNote = "git diff: " + err.Error()
		return
	}
	files, err := gitstat.ParseNumstat(out)
	if err != nil {
		a.DiffNote = "numstat: " + err.Error()
		return
	}
	kept := make([]gitstat.FileStat, 0, len(files))
	for _, f := range files {
		if strings.HasPrefix(f.Path, runScaffoldDir) {
			continue
		}
		kept = append(kept, f)
	}
	added, removed := gitstat.Totals(kept)
	band := SizeBand(added + removed)
	a.Files = kept
	a.Areas = Areas(gitstat.Paths(kept), a.AreaDepth)
	a.LinesAdded, a.LinesRemoved, a.SizeBand = &added, &removed, &band
}

// BranchExists reports whether refs/heads/<branch> exists in repoRoot. Any git
// failure reads as "no" — the caller only uses it to decide whether a diff can
// be taken, and an unanswerable probe cannot take one either.
func BranchExists(git Git, repoRoot, branch string) bool {
	if git == nil || repoRoot == "" || branch == "" {
		return false
	}
	_, err := git.Run(repoRoot, "rev-parse", "--verify", "--quiet", "refs/heads/"+branch)
	return err == nil
}

// duration is run_ended_at − run_started_at, nil when either edge is missing or
// unreadable, when the edges are inverted, and for a run healed by a daemon
// restart — its run_ended_at is the RESTART time, not when the run died
// (phaserun.HealStale's caveat).
func duration(row runRow) *int64 {
	if row.runError == "daemon restart" {
		return nil
	}
	start, err1 := time.Parse(time.RFC3339, row.startedAt)
	end, err2 := time.Parse(time.RFC3339, row.endedAt)
	if err1 != nil || err2 != nil || end.Before(start) {
		return nil
	}
	d := int64(end.Sub(start) / time.Second)
	return &d
}

// verdict returns the verdict of the latest finished verification of this phase
// that STARTED during this run. epic_phases.verify_verdict is not usable for
// that: nothing resets it when a new run starts, so a run that was never graded
// would inherit the previous run's verdict.
func (r *Recorder) verdict(phaseID int64, runStartedAt string) (*string, error) {
	start, err := time.Parse(time.RFC3339, runStartedAt)
	if err != nil {
		return nil, nil // unknown run start ⇒ no attribution possible
	}
	rows, err := r.DB.Query(`
		SELECT status, started_at FROM verification_runs
		 WHERE target_key = ? AND status IN ('pass', 'fail', 'inconclusive')
		 ORDER BY id DESC`, verify.PhaseKey(phaseID))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var status, startedAt string
		if err := rows.Scan(&status, &startedAt); err != nil {
			return nil, err
		}
		// verification_runs stamps milliseconds, run_started_at whole seconds;
		// compare as times, never as strings ("…:00.5Z" sorts before "…:00Z").
		t, err := time.Parse(time.RFC3339Nano, startedAt)
		if err != nil {
			continue
		}
		if !t.Before(start) {
			return &status, nil
		}
		// ORDER BY id DESC: every older row started earlier still.
		break
	}
	return nil, rows.Err()
}

// continuations counts this run's completion-loop resumes. With eventsRecorded
// false, a run with no run_events at all is UNKNOWN (it may predate 0077), not 0.
func (r *Recorder) continuations(phaseID int64, uuid string, eventsRecorded bool) (*int, error) {
	var total, cont int
	if err := r.DB.QueryRow(`
		SELECT COUNT(*), COALESCE(SUM(kind = 'continuation'), 0) FROM run_events
		 WHERE engine = ? AND subject_id = ? AND session_uuid = ?`,
		phaserun.Engine, phaseID, uuid).Scan(&total, &cont); err != nil {
		return nil, err
	}
	if total == 0 && !eventsRecorded {
		return nil, nil
	}
	return &cont, nil
}

// sessionFacts fills everything read from the ingested transcript: cost, the
// model fallback, and the test failures. A session not (yet) ingested leaves
// all of them nil.
func (r *Recorder) sessionFacts(a *Actuals) error {
	var sessionID int64
	err := r.DB.QueryRow(`SELECT id FROM sessions WHERE session_uuid = ?`, a.SessionUUID).Scan(&sessionID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}

	var cost sql.NullFloat64
	if err := r.DB.QueryRow(`SELECT SUM(cost_usd) FROM turns WHERE session_id = ?`, sessionID).Scan(&cost); err != nil {
		return err
	}
	if cost.Valid {
		a.CostUSD = &cost.Float64
	}

	if fb, err := r.fallback(sessionID); err != nil {
		return err
	} else {
		a.ModelFallback = fb
	}

	evs, err := r.testRuns(sessionID)
	if err != nil {
		return err
	}
	scope, err := r.forecastScope(a.PhaseID)
	if err != nil {
		return err
	}
	failures, unexpected := countTestFailures(evs, scope)
	a.TestFailures = &failures
	if scope != nil {
		a.TestFailuresUnexpected = &unexpected
	}
	return nil
}

// fallback compares the run's first and last assistant models. nil when the
// session recorded no model at all.
func (r *Recorder) fallback(sessionID int64) (*bool, error) {
	const q = `SELECT model FROM turns
		WHERE session_id = ? AND role = 'assistant' AND model IS NOT NULL AND model <> ''
		ORDER BY seq %s LIMIT 1`
	var first, last string
	err := r.DB.QueryRow(fmt.Sprintf(q, "ASC"), sessionID).Scan(&first)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if err := r.DB.QueryRow(fmt.Sprintf(q, "DESC"), sessionID).Scan(&last); err != nil {
		return nil, err
	}
	fb := modelid.IsFallback(first, last)
	return &fb, nil
}

func (r *Recorder) testRuns(sessionID int64) ([]testRunEvent, error) {
	rows, err := r.DB.Query(`
		SELECT COALESCE(e.status, ''), COALESCE(e.payload, ''), COALESCE(p.payload, '')
		  FROM events e
		  LEFT JOIN events p ON p.id = e.parent_event_id
		 WHERE e.session_id = ? AND e.type = 'test_run'
		 ORDER BY e.id`, sessionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []testRunEvent
	for rows.Next() {
		var e testRunEvent
		if err := rows.Scan(&e.Status, &e.Payload, &e.ParentPayload); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// forecastScope picks the forecast a test failure is judged against, by the
// rule phase 13 scores with: the first posterior that is not post hoc, else the
// first prior. nil when the phase has neither — then "unexpected" has no
// meaning and is stored as NULL.
func (r *Recorder) forecastScope(phaseID int64) (*forecastScope, error) {
	rows, err := r.DB.Query(`
		SELECT kind, areas_json, files_json, risks_json, post_hoc
		  FROM phase_forecasts WHERE phase_id = ? ORDER BY id`, phaseID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var prior, posterior *forecastScope
	for rows.Next() {
		var kind, areas, files, risks string
		var postHoc int
		if err := rows.Scan(&kind, &areas, &files, &risks, &postHoc); err != nil {
			return nil, err
		}
		s := &forecastScope{Areas: strList(areas), Files: strList(files), Risks: strList(risks)}
		switch {
		case kind == wsingest.ForecastPosterior && postHoc == 0 && posterior == nil:
			posterior = s
		case kind == wsingest.ForecastPrior && prior == nil:
			prior = s
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if posterior != nil {
		return posterior, nil
	}
	return prior, nil
}

func strList(s string) []string {
	var out []string
	_ = json.Unmarshal([]byte(s), &out)
	return out
}

// Store upserts a row keyed on the run's session uuid, so a recompute (the
// settled pass, a forced backfill) replaces the run's row instead of adding one.
func (r *Recorder) Store(a Actuals) error {
	var filesJSON, areasJSON any
	if a.Files != nil {
		b, err := json.Marshal(a.Files)
		if err != nil {
			return err
		}
		filesJSON = string(b)
		areas := a.Areas
		if areas == nil {
			areas = []string{}
		}
		b, err = json.Marshal(areas)
		if err != nil {
			return err
		}
		areasJSON = string(b)
	}
	source := a.Source
	if source == "" {
		source = SourceRunEnd
	}
	depth := a.AreaDepth
	if depth < 1 {
		depth = DefaultAreaDepth
	}
	_, err := r.DB.Exec(`
		INSERT INTO phase_actuals
			(phase_id, session_uuid, run_state, branch, start_point, files_json, areas_json,
			 area_depth, lines_added, lines_removed, size_band, duration_s, cost_usd, outcome,
			 verify_verdict, test_failures, test_failures_unexpected, continuations,
			 model_fallback, source, computed_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(session_uuid) DO UPDATE SET
			phase_id = excluded.phase_id, run_state = excluded.run_state,
			branch = excluded.branch, start_point = excluded.start_point,
			-- A pass that could not read the diff (branch deleted or merged since the
			-- previous pass) keeps the measured diff instead of erasing it to unknown.
			files_json = COALESCE(excluded.files_json, phase_actuals.files_json),
			areas_json = COALESCE(excluded.areas_json, phase_actuals.areas_json),
			area_depth = excluded.area_depth,
			lines_added = COALESCE(excluded.lines_added, phase_actuals.lines_added),
			lines_removed = COALESCE(excluded.lines_removed, phase_actuals.lines_removed),
			size_band = COALESCE(excluded.size_band, phase_actuals.size_band),
			duration_s = excluded.duration_s, cost_usd = excluded.cost_usd,
			outcome = excluded.outcome, verify_verdict = excluded.verify_verdict,
			test_failures = excluded.test_failures,
			test_failures_unexpected = excluded.test_failures_unexpected,
			continuations = excluded.continuations, model_fallback = excluded.model_fallback,
			source = excluded.source, computed_at = excluded.computed_at`,
		a.PhaseID, a.SessionUUID, a.RunState, a.Branch, a.StartPoint, filesJSON, areasJSON,
		depth, intPtr(a.LinesAdded), intPtr(a.LinesRemoved), strPtr(a.SizeBand),
		int64Ptr(a.DurationS), floatPtr(a.CostUSD), a.Outcome, strPtr(a.VerifyVerdict),
		intPtr(a.TestFailures), intPtr(a.TestFailuresUnexpected), intPtr(a.Continuations),
		boolPtr(a.ModelFallback), source, a.ComputedAt)
	return err
}

// The *Ptr helpers turn a nil pointer into a SQL NULL.
func intPtr(p *int) any {
	if p == nil {
		return nil
	}
	return *p
}

func int64Ptr(p *int64) any {
	if p == nil {
		return nil
	}
	return *p
}

func floatPtr(p *float64) any {
	if p == nil {
		return nil
	}
	return *p
}

func strPtr(p *string) any {
	if p == nil {
		return nil
	}
	return *p
}

func boolPtr(p *bool) any {
	if p == nil {
		return nil
	}
	if *p {
		return 1
	}
	return 0
}
