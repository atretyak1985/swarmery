package triage

import (
	"context"
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
	return blocked(s.DB, kind, ref, s.clock(), 0)
}

// BlockedBefore reports whether kind/ref was held back from run runID, which
// started at startedAt, by the verdicts of EARLIER runs — the rule partBlocked
// applies, with the 7-day windows counted back from startedAt. It is the
// read-only audit's side of that rule (`swarmery triage check`): both go
// through blocked, so the audit cannot drift from what the engine skips.
// Verdict states are read as they are now: a verdict undone after the run
// counts as holding, which is what keeps its decision in the queue today.
func BlockedBefore(db *sql.DB, kind, ref string, runID int64, startedAt time.Time) (bool, error) {
	return blocked(db, kind, ref, startedAt, runID)
}

// blocked is the one statement of the blocking rule. beforeRun 0 counts the
// verdicts of every run; a positive beforeRun only those of runs with a lower id.
func blocked(db *sql.DB, kind, ref string, at time.Time, beforeRun int64) (bool, error) {
	since := fmtTS(at.Add(-blockWindow))
	var open, failures int
	err := db.QueryRow(
		`SELECT
		   COALESCE(SUM(state IN ('suggested','sample','undone','undoing') OR (state='skipped' AND created_at > ?)), 0),
		   COALESCE(SUM(state IN ('failed','rejected') AND created_at > ?), 0)
		 FROM triage_verdicts WHERE kind=? AND ref=? AND (? = 0 OR run_id < ?)`,
		since, since, kind, ref, beforeRun, beforeRun).Scan(&open, &failures)
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

// LatestApplied returns the newest applied verdict of kind on ref with value
// (ok=false when there is none). It links an action reverted outside the
// engine back to the verdict that made it — e.g. a mute written by a Source,
// which carries no verdict id because the verdict row did not exist yet.
func (s *Service) LatestApplied(kind, ref, value string) (Verdict, bool, error) {
	v, err := scanVerdict(s.DB.QueryRow(`SELECT `+verdictCols+` FROM triage_verdicts v
		WHERE v.kind=? AND v.ref=? AND v.value=? AND v.state=? ORDER BY v.id DESC LIMIT 1`,
		kind, ref, value, StateApplied))
	if errors.Is(err, sql.ErrNoRows) {
		return Verdict{}, false, nil
	}
	if err != nil {
		return Verdict{}, false, err
	}
	return v, true, nil
}

// AppliedAfter reports whether an applied verdict of kind on ref with value
// exists with an id above afterID: a later verdict re-did the action, so
// undoing the earlier one must leave the action in place.
func AppliedAfter(db *sql.DB, kind, ref, value string, afterID int64) (bool, error) {
	var n int
	err := db.QueryRow(`SELECT COUNT(*) FROM triage_verdicts
		WHERE kind=? AND ref=? AND value=? AND state=? AND id>?`,
		kind, ref, value, StateApplied, afterID).Scan(&n)
	return n > 0, err
}

// ErrNotOpen is returned when a verdict is not (or no longer) a suggestion waiting for the operator.
var ErrNotOpen = errors.New("triage: verdict is not an open suggestion")

// MarkAccepted moves a suggested verdict to accepted (decided_at = now). When payload is
// non-nil it replaces the stored payload (the accept handler records e.g. the created card id).
func (s *Service) MarkAccepted(id int64, payload json.RawMessage) (Verdict, error) {
	return s.closeSuggestion(id, StateAccepted, payload)
}

// MarkStale moves a suggested verdict to stale (decided_at = now): its item was decided elsewhere.
func (s *Service) MarkStale(id int64) (Verdict, error) {
	return s.closeSuggestion(id, StateStale, nil)
}

// VerdictOpen asks the registered source of v.Kind whether v.Ref still waits as
// it did when the verdict was written. No source registered for that kind →
// (false, ErrNotFound).
func (s *Service) VerdictOpen(ctx context.Context, v Verdict) (bool, error) {
	src := s.source(v.Kind)
	if src == nil {
		return false, ErrNotFound
	}
	return src.Open(ctx, v.Ref)
}

// ReopenSuggestion hands an accepted verdict back as a suggestion (decided_at
// back to NULL): the accept handler claims first and calls this when the
// action itself then fails. The UPDATE is guarded by state='accepted'; a
// zero-row result is ErrNotFound for an unknown id and ErrNotOpen otherwise.
func (s *Service) ReopenSuggestion(id int64) (Verdict, error) {
	res, err := s.DB.Exec(`UPDATE triage_verdicts SET state=?, decided_at=NULL WHERE id=? AND state=?`,
		StateSuggested, id, StateAccepted)
	if err != nil {
		return Verdict{}, err
	}
	if n, err := res.RowsAffected(); err != nil {
		return Verdict{}, err
	} else if n == 0 {
		if _, err := s.GetVerdict(id); err != nil {
			return Verdict{}, err
		}
		return Verdict{}, ErrNotOpen
	}
	return s.GetVerdict(id)
}

// MarkUndone moves an applied verdict to undone (decided_at = now) WITHOUT
// calling Source.Undo: it records that the action was already reverted by
// other means (e.g. the operator unmuted a group by hand). ONE UPDATE guarded
// by state='applied'; a zero-row result is ErrNotFound for an unknown id and
// ErrNotUndoable otherwise.
func (s *Service) MarkUndone(id int64) (Verdict, error) {
	res, err := s.DB.Exec(`UPDATE triage_verdicts SET state=?, decided_at=? WHERE id=? AND state=?`,
		StateUndone, fmtTS(s.clock()), id, StateApplied)
	if err != nil {
		return Verdict{}, err
	}
	if n, err := res.RowsAffected(); err != nil {
		return Verdict{}, err
	} else if n == 0 {
		if _, err := s.GetVerdict(id); err != nil {
			return Verdict{}, err
		}
		return Verdict{}, ErrNotUndoable
	}
	return s.GetVerdict(id)
}

// SetVerdictPayload replaces the stored payload of verdict id (valid JSON
// required). An unknown id is ErrNotFound.
func (s *Service) SetVerdictPayload(id int64, payload json.RawMessage) error {
	if !json.Valid(payload) {
		return errors.New("triage: verdict payload is not valid JSON")
	}
	res, err := s.DB.Exec(`UPDATE triage_verdicts SET payload=? WHERE id=?`, string(payload), id)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// closeSuggestion is MarkAccepted/MarkStale: ONE UPDATE guarded by
// state='suggested', so of two concurrent closes only one wins. A zero-row
// result is ErrNotFound for an unknown id and ErrNotOpen otherwise (a sample,
// an applied verdict, or a suggestion already closed). It returns the row as
// it is after the change.
func (s *Service) closeSuggestion(id int64, to string, payload json.RawMessage) (Verdict, error) {
	q, args := `UPDATE triage_verdicts SET state=?, decided_at=?`, []any{to, fmtTS(s.clock())}
	if payload != nil {
		if !json.Valid(payload) {
			return Verdict{}, errors.New("triage: verdict payload is not valid JSON")
		}
		q += `, payload=?`
		args = append(args, string(payload))
	}
	res, err := s.DB.Exec(q+` WHERE id=? AND state=?`, append(args, id, StateSuggested)...)
	if err != nil {
		return Verdict{}, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return Verdict{}, err
	}
	if n == 0 {
		if _, err := s.GetVerdict(id); err != nil {
			return Verdict{}, err
		}
		return Verdict{}, ErrNotOpen
	}
	return s.GetVerdict(id)
}

// suggestedScope is the WHERE clause (and its args) shared by ListSuggestedAfter
// and CountSuggested: state suggested, and — when projectID != 0 — the same
// project rule as ListVerdicts (the item's project, or none).
func suggestedScope(projectID int64) (string, []any) {
	q, args := ` WHERE v.state=?`, []any{StateSuggested}
	if projectID != 0 {
		q += ` AND (v.project_id = ? OR v.project_id IS NULL)`
		args = append(args, projectID)
	}
	return q, args
}

// ListSuggestedAfter is a keyset page of suggested verdicts: id > afterID,
// oldest first, at most limit rows (limit <= 0 or > 1000 → 200), narrowed to
// projectID like ListVerdicts when it is non-zero.
func (s *Service) ListSuggestedAfter(afterID int64, limit int, projectID int64) ([]Verdict, error) {
	if limit <= 0 || limit > 1000 {
		limit = 200
	}
	where, args := suggestedScope(projectID)
	rows, err := s.DB.Query(`SELECT `+verdictCols+` FROM triage_verdicts v`+where+
		` AND v.id > ? ORDER BY v.id ASC LIMIT ?`, append(args, afterID, limit)...)
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

// CountSuggested counts the suggested verdicts in ListSuggestedAfter's scope.
func (s *Service) CountSuggested(projectID int64) (int, error) {
	where, args := suggestedScope(projectID)
	var n int
	err := s.DB.QueryRow(`SELECT COUNT(*) FROM triage_verdicts v`+where, args...).Scan(&n)
	return n, err
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
