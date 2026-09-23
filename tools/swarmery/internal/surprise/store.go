package surprise

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/gitstat"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/wsingest"
)

// sourceBackfill mirrors actuals.SourceBackfill. A backfilled score is history:
// it is stored, but it raises no attention event and requests no verification —
// announcing months-old runs would teach the operator to ignore the channel.
const sourceBackfill = "backfill"

// Attention is one scored run that crossed the attention threshold. It is
// raised at most once per run (phase_surprise.notified_at).
type Attention struct {
	PhaseID         int64
	WorkspaceTaskID int64
	SessionUUID     string
	PhaseName       string
	PlanTitle       string
	Index           float64
	Top             string
	Summary         string
}

// Scorer computes, stores and routes surprise scores.
type Scorer struct {
	DB  *sql.DB
	Cfg Config
	// Now is the clock (test seam; nil ⇒ time.Now).
	Now func() time.Time
	// Attention receives a run whose score first reaches Cfg.NotifyAt. nil ⇒
	// nothing is routed (the score is still stored). The daemon wires it to the
	// WS bus (the notch and the dashboard) and the webhook notifier.
	Attention func(Attention)
	// Changed is called with the phase's workspace task id after a score is
	// written or removed, so the Plans page refetches (plan_updated). nil ⇒ none.
	Changed func(taskID int64)
}

// NewScorer builds a Scorer with the given configuration.
func NewScorer(db *sql.DB, cfg Config) *Scorer {
	return &Scorer{DB: db, Cfg: cfg}
}

func (s *Scorer) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

// Stored is a phase_surprise row.
type Stored struct {
	Result
	PhaseID         int64   `json:"phaseId"`
	SessionUUID     string  `json:"sessionUuid"`
	ForecastDocHash string  `json:"forecastDocHash"`
	ActualsSource   string  `json:"actualsSource"`
	NotifiedAt      *string `json:"notifiedAt"`
	AutoVerifyAt    *string `json:"autoVerifyAt"`
	ComputedAt      string  `json:"computedAt"`
}

// ── scoring ──

// Score computes one run's surprise and stores it, replacing any previous
// score for the run. It returns nil (and removes a stale row) when the run is
// not scorable — no actuals, no forecast, or nothing measurable — because an
// absent score must never read as a zero one.
func (s *Scorer) Score(phaseID int64, sessionUUID string) (*Stored, error) {
	if sessionUUID == "" {
		return nil, fmt.Errorf("surprise: phase %d: empty session uuid", phaseID)
	}
	act, ok, err := s.loadActual(phaseID, sessionUUID)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, s.remove(sessionUUID)
	}
	scored, prior, err := s.loadForecast(phaseID)
	if err != nil {
		return nil, err
	}
	if scored == nil {
		return nil, s.remove(sessionUUID)
	}
	res, ok := Compute(*scored, act, s.Cfg.Weights)
	if !ok {
		return nil, s.remove(sessionUUID)
	}
	if scored.Kind == wsingest.ForecastPosterior && prior != nil {
		if rv, ok := ComputeRevision(*prior, *scored); ok {
			res.Revision = &rv
		}
	}
	st := &Stored{
		Result: res, PhaseID: phaseID, SessionUUID: sessionUUID,
		ForecastDocHash: scored.DocHash, ActualsSource: act.Source,
		ComputedAt: s.now().UTC().Format(time.RFC3339),
	}
	if err := s.store(st, scored.PostHoc); err != nil {
		return nil, err
	}
	return s.Load(sessionUUID)
}

// AfterActuals is the hook internal/actuals calls after it stores a run's
// actuals (both the run-end and the settled pass, and the backfill). ADVISORY:
// every failure is logged and dropped and a panic is recovered — a score that
// could not be computed must never change how a run is reported.
func (s *Scorer) AfterActuals(phaseID int64, sessionUUID, source string) {
	defer func() {
		if p := recover(); p != nil {
			log.Printf("error: surprise: phase=%d uuid=%s scoring panicked: %v", phaseID, sessionUUID, p)
		}
	}()
	st, err := s.Score(phaseID, sessionUUID)
	if err != nil {
		log.Printf("warning: surprise: phase=%d uuid=%s: %v", phaseID, sessionUUID, err)
		return
	}
	if st != nil && source != sourceBackfill {
		s.attend(st)
	}
	if s.Changed != nil && source != sourceBackfill {
		if taskID, err := s.taskOf(phaseID); err == nil {
			s.Changed(taskID)
		}
	}
}

// attend raises the attention event once per run, when the score reaches the
// threshold. The UPDATE … WHERE notified_at IS NULL is the claim: the settled
// pass recomputing the same run cannot raise it a second time.
func (s *Scorer) attend(st *Stored) {
	if s.Cfg.NotifyAt == nil || st.Index < *s.Cfg.NotifyAt {
		return
	}
	res, err := s.DB.Exec(`UPDATE phase_surprise SET notified_at = ?
		WHERE session_uuid = ? AND notified_at IS NULL`,
		s.now().UTC().Format(time.RFC3339), st.SessionUUID)
	if err != nil {
		log.Printf("warning: surprise: phase=%d claim notification: %v", st.PhaseID, err)
		return
	}
	if n, _ := res.RowsAffected(); n != 1 || s.Attention == nil {
		return
	}
	a := Attention{PhaseID: st.PhaseID, SessionUUID: st.SessionUUID,
		Index: st.Index, Top: st.Top, Summary: st.Summary}
	var name, title sql.NullString
	if err := s.DB.QueryRow(`
		SELECT e.workspace_task_id, e.name, t.title
		  FROM epic_phases e LEFT JOIN tasks t ON t.id = e.workspace_task_id
		 WHERE e.id = ?`, st.PhaseID).Scan(&a.WorkspaceTaskID, &name, &title); err != nil && !errors.Is(err, sql.ErrNoRows) {
		log.Printf("warning: surprise: phase=%d attention context: %v", st.PhaseID, err)
	}
	a.PhaseName, a.PlanTitle = name.String, title.String
	s.Attention(a)
}

// AutoVerifyHint answers phaserun's question at the end of a run: should this
// run be verified because it surprised us, and with what focus hint? It is
// true at most once per run, and only when auto-verification was explicitly
// enabled (Cfg.AutoVerifyAt set) — the default is off.
func (s *Scorer) AutoVerifyHint(phaseID int64, sessionUUID string) (string, bool) {
	if s.Cfg.AutoVerifyAt == nil {
		return "", false
	}
	st, err := s.Load(sessionUUID)
	if err != nil || st == nil || st.PhaseID != phaseID || st.Index < *s.Cfg.AutoVerifyAt {
		return "", false
	}
	res, err := s.DB.Exec(`UPDATE phase_surprise SET auto_verify_at = ?
		WHERE session_uuid = ? AND auto_verify_at IS NULL`,
		s.now().UTC().Format(time.RFC3339), sessionUUID)
	if err != nil {
		log.Printf("warning: surprise: phase=%d claim auto-verify: %v", phaseID, err)
		return "", false
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return "", false
	}
	return FocusHint(st.Result), true
}

// FocusHint renders a score as the verifier's focus hint: where the run
// diverged from its forecast, so the read-only verifier looks there first. It
// is a HINT — the verifier still grades the phase's acceptance criteria.
func FocusHint(r Result) string {
	var b strings.Builder
	b.WriteString("This run diverged from its forecast (" + r.Summary + ").\n")
	d := r.Detail
	if len(d.UnexpectedAreas) > 0 {
		b.WriteString("- It changed areas the forecast did not name: " + strings.Join(d.UnexpectedAreas, ", ") +
			". Check these changes are intended and covered.\n")
	}
	if len(d.MissedAreas) > 0 {
		b.WriteString("- It never touched forecast areas: " + strings.Join(d.MissedAreas, ", ") +
			". Check whether the criteria that needed them are really met.\n")
	}
	if v := r.Components[CompOutcomeMiss]; v != nil && *v > 0 {
		b.WriteString(fmt.Sprintf("- The forecast said %q but the run ended %q.\n", d.ForecastOutcome, d.ActualOutcome))
	}
	if d.TestFailuresUnexpected != nil && *d.TestFailuresUnexpected > 0 {
		b.WriteString(fmt.Sprintf("- %d test run(s) failed outside the forecast's areas.\n", *d.TestFailuresUnexpected))
	}
	return strings.TrimRight(b.String(), "\n")
}

// ── backfill ──

// BackfillStats counts a rescore over every stored actuals row.
type BackfillStats struct {
	Scanned    int // phase_actuals rows
	Scored     int // rows now carrying a score
	Unscorable int // no forecast, or nothing measurable — no score row
	Failed     int
}

// Backfill (re)scores every run that has actuals. Idempotent: a recompute
// replaces the run's row. It raises no attention and requests no verification.
func (s *Scorer) Backfill() (BackfillStats, error) {
	var st BackfillStats
	rows, err := s.DB.Query(`SELECT phase_id, session_uuid FROM phase_actuals ORDER BY id`)
	if err != nil {
		return st, err
	}
	type run struct {
		phase int64
		uuid  string
	}
	var runs []run
	for rows.Next() {
		var r run
		if err := rows.Scan(&r.phase, &r.uuid); err != nil {
			rows.Close()
			return st, err
		}
		runs = append(runs, r)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return st, err
	}
	for _, r := range runs {
		st.Scanned++
		got, err := s.Score(r.phase, r.uuid)
		switch {
		case err != nil:
			st.Failed++
			log.Printf("warning: surprise backfill: phase=%d uuid=%s: %v", r.phase, r.uuid, err)
		case got == nil:
			st.Unscorable++
		default:
			st.Scored++
		}
	}
	return st, nil
}

// ── storage ──

func (s *Scorer) loadActual(phaseID int64, uuid string) (Actual, bool, error) {
	var (
		a                    Actual
		files, areas, size   sql.NullString
		duration, unexpected sql.NullInt64
	)
	err := s.DB.QueryRow(`
		SELECT files_json, areas_json, area_depth, size_band, duration_s, outcome,
		       test_failures_unexpected, source
		  FROM phase_actuals WHERE session_uuid = ? AND phase_id = ?`, uuid, phaseID).Scan(
		&files, &areas, &a.AreaDepth, &size, &duration, &a.Outcome, &unexpected, &a.Source)
	if errors.Is(err, sql.ErrNoRows) {
		return a, false, nil
	}
	if err != nil {
		return a, false, err
	}
	if files.Valid {
		var fs []gitstat.FileStat
		if err := json.Unmarshal([]byte(files.String), &fs); err == nil {
			a.Files = gitstat.Paths(fs)
			if a.Files == nil {
				a.Files = []string{}
			}
		}
	}
	if areas.Valid {
		_ = json.Unmarshal([]byte(areas.String), &a.Areas)
	}
	if size.Valid {
		a.SizeBand = &size.String
	}
	if duration.Valid {
		a.DurationS = &duration.Int64
	}
	if unexpected.Valid {
		n := int(unexpected.Int64)
		a.TestFailuresUnexpected = &n
	}
	return a, true, nil
}

// loadForecast picks the forecast a run is scored against — the first
// non-post-hoc POSTERIOR, else the first PRIOR; the same rule internal/actuals
// judges test failures by — and returns the first prior for the revision.
func (s *Scorer) loadForecast(phaseID int64) (scored, prior *Forecast, err error) {
	rows, err := s.DB.Query(`
		SELECT kind, areas_json, files_json, size_band, duration_band, outcome,
		       confidence, post_hoc, doc_hash
		  FROM phase_forecasts WHERE phase_id = ? ORDER BY id`, phaseID)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	var posterior *Forecast
	for rows.Next() {
		var (
			f            Forecast
			areas, files string
			conf         sql.NullFloat64
			postHoc      int
		)
		if err := rows.Scan(&f.Kind, &areas, &files, &f.SizeBand, &f.DurationBand, &f.Outcome,
			&conf, &postHoc, &f.DocHash); err != nil {
			return nil, nil, err
		}
		_ = json.Unmarshal([]byte(areas), &f.Areas)
		_ = json.Unmarshal([]byte(files), &f.Files)
		if conf.Valid {
			v := conf.Float64
			f.Confidence = &v
		}
		f.PostHoc = postHoc != 0
		switch {
		case f.Kind == wsingest.ForecastPosterior && !f.PostHoc && posterior == nil:
			fc := f
			posterior = &fc
		case f.Kind == wsingest.ForecastPrior && prior == nil:
			fc := f
			prior = &fc
		}
	}
	if err := rows.Err(); err != nil {
		return nil, nil, err
	}
	if posterior != nil {
		return posterior, prior, nil
	}
	return prior, prior, nil
}

func (s *Scorer) store(st *Stored, postHoc bool) error {
	comps, err := json.Marshal(st.Components)
	if err != nil {
		return err
	}
	weights, err := json.Marshal(st.Weights)
	if err != nil {
		return err
	}
	detail, err := json.Marshal(st.Detail)
	if err != nil {
		return err
	}
	var revIndex, revJSON any
	if st.Revision != nil {
		b, err := json.Marshal(st.Revision)
		if err != nil {
			return err
		}
		revIndex, revJSON = st.Revision.Index, string(b)
	}
	posthoc := 0
	if postHoc {
		posthoc = 1
	}
	// notified_at / auto_verify_at are deliberately absent from the UPDATE: they
	// record side effects that already happened for this run.
	_, err = s.DB.Exec(`
		INSERT INTO phase_surprise
			(phase_id, session_uuid, forecast_kind, forecast_post_hoc, forecast_doc_hash,
			 surprise_index, top_component, components_json, weights_json, detail_json,
			 revision_index, revision_json, summary, actuals_source, computed_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(session_uuid) DO UPDATE SET
			phase_id = excluded.phase_id, forecast_kind = excluded.forecast_kind,
			forecast_post_hoc = excluded.forecast_post_hoc,
			forecast_doc_hash = excluded.forecast_doc_hash,
			surprise_index = excluded.surprise_index, top_component = excluded.top_component,
			components_json = excluded.components_json, weights_json = excluded.weights_json,
			detail_json = excluded.detail_json, revision_index = excluded.revision_index,
			revision_json = excluded.revision_json, summary = excluded.summary,
			actuals_source = excluded.actuals_source, computed_at = excluded.computed_at`,
		st.PhaseID, st.SessionUUID, st.Detail.ForecastKind, posthoc, st.ForecastDocHash,
		st.Index, st.Top, string(comps), string(weights), string(detail),
		revIndex, revJSON, st.Summary, st.ActualsSource, st.ComputedAt)
	return err
}

func (s *Scorer) remove(uuid string) error {
	_, err := s.DB.Exec(`DELETE FROM phase_surprise WHERE session_uuid = ?`, uuid)
	return err
}

func (s *Scorer) taskOf(phaseID int64) (int64, error) {
	var id int64
	err := s.DB.QueryRow(`SELECT workspace_task_id FROM epic_phases WHERE id = ?`, phaseID).Scan(&id)
	return id, err
}

// Load reads one run's stored score; nil when the run has none.
func (s *Scorer) Load(sessionUUID string) (*Stored, error) {
	return LoadBySession(s.DB, sessionUUID)
}

// LoadBySession reads one run's stored score; nil when the run has none.
func LoadBySession(db *sql.DB, sessionUUID string) (*Stored, error) {
	row := db.QueryRow(SelectStored+` WHERE session_uuid = ?`, sessionUUID)
	st, err := ScanStored(row.Scan)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return st, err
}

// SelectStored / ScanStored are the one read shape of a phase_surprise row,
// shared by the scorer and the API read path, which appends its own WHERE.
const SelectStored = `SELECT phase_id, session_uuid, forecast_doc_hash, surprise_index, top_component,
	components_json, weights_json, detail_json, revision_json, summary, actuals_source,
	notified_at, auto_verify_at, computed_at FROM phase_surprise`

// ScanStored scans one SelectStored row.
func ScanStored(scan func(dest ...any) error) (*Stored, error) {
	var (
		st                       Stored
		comps, weights, detail   string
		rev, notified, autoVerif sql.NullString
	)
	if err := scan(&st.PhaseID, &st.SessionUUID, &st.ForecastDocHash, &st.Index, &st.Top,
		&comps, &weights, &detail, &rev, &st.Summary, &st.ActualsSource,
		&notified, &autoVerif, &st.ComputedAt); err != nil {
		return nil, err
	}
	st.Components = map[string]*float64{}
	_ = json.Unmarshal([]byte(comps), &st.Components)
	st.Weights = map[string]float64{}
	_ = json.Unmarshal([]byte(weights), &st.Weights)
	_ = json.Unmarshal([]byte(detail), &st.Detail)
	if rev.Valid && rev.String != "" {
		var rv Revision
		if json.Unmarshal([]byte(rev.String), &rv) == nil {
			st.Revision = &rv
		}
	}
	if notified.Valid {
		st.NotifiedAt = &notified.String
	}
	if autoVerif.Valid {
		st.AutoVerifyAt = &autoVerif.String
	}
	return &st, nil
}
