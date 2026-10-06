package triage

import (
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"time"
)

// tsLayout is fixed-width so timestamps compare correctly as strings.
const tsLayout = "2006-01-02T15:04:05.000Z"

func fmtTS(t time.Time) string { return t.UTC().Format(tsLayout) }

// HealStale marks every 'running' run a crashed daemon left behind as failed
// (mirrors routines' HealStale), and moves every 'undoing' verdict — an undo
// whose Source.Undo may not have run — back to 'applied' so the operator can
// press undo again (Source.Undo is idempotent). Call it once at startup,
// before any Start or Undo.
func (s *Service) HealStale() error {
	if _, err := s.DB.Exec(
		`UPDATE triage_runs SET status='failed', error='interrupted by daemon restart', finished_at=?
		 WHERE status='running'`, fmtTS(s.clock())); err != nil {
		return err
	}
	_, err := s.DB.Exec(`UPDATE triage_verdicts SET state=?, decided_at=NULL WHERE state=?`,
		StateApplied, StateUndoing)
	return err
}

func (s *Service) insertRun(req StartReq) (int64, error) {
	kinds, _ := json.Marshal(req.Kinds)
	var scope any
	if req.Scope.ProjectID != 0 {
		scope = req.Scope.ProjectID
	}
	res, err := s.DB.Exec(
		`INSERT INTO triage_runs(trigger, scope_project_id, kinds, status, started_at) VALUES(?,?,?,'running',?)`,
		req.Trigger, scope, string(kinds), fmtTS(s.clock()))
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

func (s *Service) saveCounters(r *Run) error {
	sess, _ := json.Marshal(r.SessionUUIDs)
	_, err := s.DB.Exec(
		`UPDATE triage_runs SET total=?, done=?, applied=?, suggested=?, skipped=?, failed=?, rejected=?,
		 cost_usd=?, session_uuids=? WHERE id=?`,
		r.Total, r.Done, r.Applied, r.Suggested, r.Skipped, r.Failed, r.Rejected,
		r.CostUSD, string(sess), r.ID)
	return err
}

func (s *Service) finishRun(id int64, status, errMsg string) error {
	_, err := s.DB.Exec(`UPDATE triage_runs SET status=?, error=?, finished_at=? WHERE id=?`,
		status, errMsg, fmtTS(s.clock()), id)
	return err
}

func rawOr(b json.RawMessage, def string) string {
	if len(b) == 0 {
		return def
	}
	return string(b)
}

func (s *Service) insertVerdict(v Verdict) (int64, error) {
	var project any // NULL when the item has no project (nil or 0)
	if v.ProjectID != nil && *v.ProjectID != 0 {
		project = *v.ProjectID
	}
	res, err := s.DB.Exec(
		`INSERT INTO triage_verdicts(run_id, kind, class, ref, item_key, title, value, reason, payload, prior, state, created_at, project_id)
		 VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		v.RunID, v.Kind, v.Class, v.Ref, v.ItemKey, v.Title, v.Value, v.Reason,
		rawOr(v.Payload, "{}"), rawOr(v.Prior, "{}"), v.State, fmtTS(s.clock()), project)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

const (
	// blockWindow is how far back skips and failures count against a part.
	blockWindow = 7 * 24 * time.Hour
	// maxRecentFailures failed/rejected verdicts within blockWindow block a part.
	maxRecentFailures = 3
)

// partBlocked reports whether kind/ref must not be judged again: it waits on
// the operator (suggested/sample), the operator took an action on it back
// (undone — permanent; undoing — an undo in flight, or one whose undone write
// failed, until HealStale restores it), it was skipped within blockWindow, or
// it failed or was rejected maxRecentFailures times within blockWindow.
func (s *Service) partBlocked(kind, ref string) (bool, error) {
	since := fmtTS(s.clock().Add(-blockWindow))
	var open, failures int
	err := s.DB.QueryRow(
		`SELECT
		   COALESCE(SUM(state IN ('suggested','sample','undone','undoing') OR (state='skipped' AND created_at > ?)), 0),
		   COALESCE(SUM(state IN ('failed','rejected') AND created_at > ?), 0)
		 FROM triage_verdicts WHERE kind=? AND ref=?`,
		since, since, kind, ref).Scan(&open, &failures)
	return open > 0 || failures >= maxRecentFailures, err
}

// setVerdictState moves verdict id from state from to state to. The UPDATE is
// guarded by the expected current state, so a writer that changed the row
// after the caller read it wins; such a zero-row result is not an error.
func (s *Service) setVerdictState(id int64, from, to string) error {
	_, err := s.DB.Exec(`UPDATE triage_verdicts SET state=?, decided_at=? WHERE id=? AND state=?`,
		to, fmtTS(s.clock()), id, from)
	return err
}

const runCols = `id, trigger, scope_project_id, kinds, status, total, done, applied, suggested, skipped,
	failed, rejected, cost_usd, session_uuids, error, started_at, finished_at`

func scanRun(sc interface{ Scan(...any) error }) (Run, error) {
	var r Run
	var scope sql.NullInt64
	var kinds, sess string
	var fin sql.NullString
	err := sc.Scan(&r.ID, &r.Trigger, &scope, &kinds, &r.Status, &r.Total, &r.Done, &r.Applied,
		&r.Suggested, &r.Skipped, &r.Failed, &r.Rejected, &r.CostUSD, &sess, &r.Error, &r.StartedAt, &fin)
	if err != nil {
		return r, err
	}
	if scope.Valid {
		r.ScopeProjectID = &scope.Int64
	}
	if fin.Valid {
		r.FinishedAt = &fin.String
	}
	_ = json.Unmarshal([]byte(kinds), &r.Kinds)
	_ = json.Unmarshal([]byte(sess), &r.SessionUUIDs)
	if r.Kinds == nil {
		r.Kinds = []string{}
	}
	if r.SessionUUIDs == nil {
		r.SessionUUIDs = []string{}
	}
	return r, nil
}

// GetRun returns one run, or ErrNotFound.
func (s *Service) GetRun(id int64) (Run, error) {
	r, err := scanRun(s.DB.QueryRow(`SELECT `+runCols+` FROM triage_runs WHERE id=?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return r, ErrNotFound
	}
	return r, err
}

// ActiveRun returns the running run, or nil when idle.
func (s *Service) ActiveRun() (*Run, error) {
	r, err := scanRun(s.DB.QueryRow(`SELECT ` + runCols + ` FROM triage_runs WHERE status='running' ORDER BY id DESC LIMIT 1`))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &r, nil
}

// ListRuns returns runs newest first (limit <= 0 → 20).
func (s *Service) ListRuns(limit int) ([]Run, error) {
	if limit <= 0 || limit > 500 {
		limit = 20
	}
	rows, err := s.DB.Query(`SELECT `+runCols+` FROM triage_runs ORDER BY id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Run{}
	for rows.Next() {
		r, err := scanRun(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

const verdictCols = `v.id, v.run_id, v.kind, v.class, v.ref, v.item_key, v.title, v.value, v.reason,
	v.payload, v.prior, v.state, v.created_at, v.decided_at, v.project_id`

func scanVerdict(sc interface{ Scan(...any) error }) (Verdict, error) {
	var v Verdict
	var payload, prior string
	var dec sql.NullString
	var project sql.NullInt64
	err := sc.Scan(&v.ID, &v.RunID, &v.Kind, &v.Class, &v.Ref, &v.ItemKey, &v.Title, &v.Value, &v.Reason,
		&payload, &prior, &v.State, &v.CreatedAt, &dec, &project)
	if err != nil {
		return v, err
	}
	if project.Valid {
		v.ProjectID = &project.Int64
	}
	v.Payload, v.Prior = json.RawMessage(payload), json.RawMessage(prior)
	if dec.Valid {
		v.DecidedAt = &dec.String
	}
	return v, nil
}

// GetVerdict returns one verdict, or ErrNotFound.
func (s *Service) GetVerdict(id int64) (Verdict, error) {
	v, err := scanVerdict(s.DB.QueryRow(`SELECT `+verdictCols+` FROM triage_verdicts v WHERE v.id=?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return v, ErrNotFound
	}
	return v, err
}

// ListVerdicts returns verdicts newest first, narrowed by f (limit <= 0 → 200).
func (s *Service) ListVerdicts(f VerdictFilter) ([]Verdict, error) {
	q := `SELECT ` + verdictCols + ` FROM triage_verdicts v WHERE 1=1`
	var args []any
	if len(f.States) > 0 {
		q += ` AND v.state IN (?` + strings.Repeat(",?", len(f.States)-1) + `)`
		for _, st := range f.States {
			args = append(args, st)
		}
	}
	if f.Kind != "" {
		q += ` AND v.kind=?`
		args = append(args, f.Kind)
	}
	if f.RunID != 0 {
		q += ` AND v.run_id=?`
		args = append(args, f.RunID)
	}
	if f.ProjectID != 0 {
		// The item's own project, not the run's scope: a fleet-wide run's
		// verdicts belong to their items' projects; project-less verdicts
		// show under every project filter.
		q += ` AND (v.project_id = ? OR v.project_id IS NULL)`
		args = append(args, f.ProjectID)
	}
	limit := f.Limit
	if limit <= 0 || limit > 1000 {
		limit = 200
	}
	q += ` ORDER BY v.id DESC LIMIT ?`
	args = append(args, limit)
	rows, err := s.DB.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Verdict{}
	for rows.Next() {
		v, err := scanVerdict(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}
