package routines

import (
	"crypto/rand"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// tsFormat matches the RFC3339 style provision writes (the routines row
// timestamps are compared lexically in the due query, so a single format is
// mandatory across every writer).
const tsFormat = time.RFC3339

// ErrNotFound is returned by Get/updates when the routine id is unknown.
var ErrNotFound = errors.New("routine not found")

// Routine is the durable DTO of a routines row.
type Routine struct {
	ID           string
	ProjectID    sql.NullInt64
	Name         string
	CronExpr     string
	Enabled      bool
	CatchUp      string // run_one|skip
	Steps        []Step
	WebhookToken string
	TimeoutSec   int
	CreatedAt    string
	UpdatedAt    string
	LastRunAt    sql.NullString
	NextRunAt    sql.NullString
}

// Run is the durable DTO of a routine_runs row.
type Run struct {
	ID         int64
	RoutineID  string
	Trigger    string
	Status     string
	Detail     sql.NullString
	StartedAt  string
	FinishedAt sql.NullString
}

func (s *Service) ts() string { return s.clock().UTC().Format(tsFormat) }

// NewID mints a "R-" + 6-char base36 routine id.
func NewID() (string, error) {
	const alphabet = "0123456789abcdefghijklmnopqrstuvwxyz"
	buf := make([]byte, 6)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	for i := range buf {
		buf[i] = alphabet[int(buf[i])%len(alphabet)]
	}
	return "R-" + string(buf), nil
}

// NewToken mints a 32-hex-char webhook token (128 bits).
func NewToken() (string, error) {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	const hexdigits = "0123456789abcdef"
	out := make([]byte, 32)
	for i, b := range buf {
		out[i*2] = hexdigits[b>>4]
		out[i*2+1] = hexdigits[b&0x0f]
	}
	return string(out), nil
}

// CreateParams is the validated input to Create (the API validates + defaults,
// then calls this). Steps are already validated.
type CreateParams struct {
	ProjectID    sql.NullInt64
	Name         string
	CronExpr     string
	Enabled      bool
	CatchUp      string
	Steps        []Step
	WebhookToken string
	TimeoutSec   int
}

// Create inserts a routine, computing next_run_at from the cron expression.
// Returns the created row.
func (s *Service) Create(p CreateParams) (Routine, error) {
	id, err := NewID()
	if err != nil {
		return Routine{}, err
	}
	stepsJSON, err := MarshalSteps(p.Steps)
	if err != nil {
		return Routine{}, err
	}
	now := s.ts()
	var nextRun any
	if p.Enabled {
		if t, ok := NextRun(p.CronExpr, s.clock()); ok {
			nextRun = t.UTC().Format(tsFormat)
		}
	}
	var token any
	if p.WebhookToken != "" {
		token = p.WebhookToken
	}
	var cron any
	if p.CronExpr != "" {
		cron = p.CronExpr
	}
	if _, err := s.DB.Exec(`
		INSERT INTO routines
		  (id, project_id, name, cron_expr, enabled, catch_up, steps,
		   webhook_token, timeout_sec, created_at, updated_at, next_run_at)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?)`,
		id, p.ProjectID, p.Name, cron, boolToInt(p.Enabled), p.CatchUp, stepsJSON,
		token, p.TimeoutSec, now, now, nextRun); err != nil {
		return Routine{}, err
	}
	return s.Get(id)
}

// UpdateParams carries the settable fields; nil pointers are left unchanged.
type UpdateParams struct {
	Name       *string
	CronExpr   *string
	Enabled    *bool
	CatchUp    *string
	Steps      *[]Step
	TimeoutSec *int
	// RotateToken, when non-nil, sets (non-empty) or clears (empty) the webhook
	// token. Absent → unchanged.
	WebhookToken *string
}

// Update applies a partial update and recomputes next_run_at whenever the cron
// expression or enabled flag could have changed. Returns the updated row.
func (s *Service) Update(id string, p UpdateParams) (Routine, error) {
	cur, err := s.Get(id)
	if err != nil {
		return Routine{}, err
	}
	if p.Name != nil {
		cur.Name = *p.Name
	}
	if p.CronExpr != nil {
		cur.CronExpr = *p.CronExpr
	}
	if p.Enabled != nil {
		cur.Enabled = *p.Enabled
	}
	if p.CatchUp != nil {
		cur.CatchUp = *p.CatchUp
	}
	if p.Steps != nil {
		cur.Steps = *p.Steps
	}
	if p.TimeoutSec != nil {
		cur.TimeoutSec = *p.TimeoutSec
	}
	if p.WebhookToken != nil {
		cur.WebhookToken = *p.WebhookToken
	}
	stepsJSON, err := MarshalSteps(cur.Steps)
	if err != nil {
		return Routine{}, err
	}
	// Recompute next_run_at from the (possibly new) cron/enabled state.
	var nextRun any
	if cur.Enabled {
		if t, ok := NextRun(cur.CronExpr, s.clock()); ok {
			nextRun = t.UTC().Format(tsFormat)
		}
	}
	res, err := s.DB.Exec(`
		UPDATE routines SET name=?, cron_expr=?, enabled=?, catch_up=?, steps=?,
		                    webhook_token=?, timeout_sec=?, updated_at=?, next_run_at=?
		 WHERE id=?`,
		cur.Name, nullifyStr(cur.CronExpr), boolToInt(cur.Enabled), cur.CatchUp, stepsJSON,
		nullifyStr(cur.WebhookToken), cur.TimeoutSec, s.ts(), nextRun, id)
	if err != nil {
		return Routine{}, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return Routine{}, ErrNotFound
	}
	return s.Get(id)
}

// Delete removes a routine (its runs cascade via the FK). Returns ErrNotFound
// when the id is unknown.
func (s *Service) Delete(id string) error {
	res, err := s.DB.Exec(`DELETE FROM routines WHERE id=?`, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

const routineCols = `id, project_id, name, COALESCE(cron_expr,''), enabled, catch_up,
	steps, COALESCE(webhook_token,''), timeout_sec, created_at, updated_at, last_run_at, next_run_at`

func scanRoutine(scan func(...any) error) (Routine, error) {
	var r Routine
	var stepsJSON string
	if err := scan(&r.ID, &r.ProjectID, &r.Name, &r.CronExpr, &boolScan{&r.Enabled},
		&r.CatchUp, &stepsJSON, &r.WebhookToken, &r.TimeoutSec, &r.CreatedAt,
		&r.UpdatedAt, &r.LastRunAt, &r.NextRunAt); err != nil {
		return Routine{}, err
	}
	steps, err := decodeSteps(stepsJSON)
	if err != nil {
		return Routine{}, err
	}
	r.Steps = steps
	return r, nil
}

// Get returns one routine by id, ErrNotFound when absent.
func (s *Service) Get(id string) (Routine, error) {
	r, err := scanRoutine(s.DB.QueryRow(`SELECT `+routineCols+` FROM routines WHERE id=?`, id).Scan)
	if errors.Is(err, sql.ErrNoRows) {
		return Routine{}, ErrNotFound
	}
	return r, err
}

// List returns all routines (optionally scoped to a project), newest first.
// projectID <= 0 → all (global + project-scoped).
func (s *Service) List(projectID int64) ([]Routine, error) {
	q := `SELECT ` + routineCols + ` FROM routines`
	var args []any
	if projectID > 0 {
		q += ` WHERE project_id=?`
		args = append(args, projectID)
	}
	q += ` ORDER BY created_at DESC, id DESC`
	rows, err := s.DB.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Routine{}
	for rows.Next() {
		r, err := scanRoutine(rows.Scan)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// due returns the routines eligible to fire now: enabled, with a non-empty cron
// expression, whose next_run_at is at or before `now`. Ordered by next_run_at so
// the most overdue fires first.
func (s *Service) due(now time.Time) ([]Routine, error) {
	rows, err := s.DB.Query(`
		SELECT `+routineCols+` FROM routines
		 WHERE enabled=1 AND cron_expr IS NOT NULL AND cron_expr <> ''
		   AND next_run_at IS NOT NULL AND next_run_at <= ?
		 ORDER BY next_run_at ASC`, now.UTC().Format(tsFormat))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Routine
	for rows.Next() {
		r, err := scanRoutine(rows.Scan)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// advanceSchedule recomputes next_run_at (and stamps last_run_at) after a run.
// For catch_up='skip' AND multiple missed slots, next_run_at jumps to the next
// slot strictly after `now` — missed slots are silently dropped. For 'run_one'
// the single run already happened; next_run_at likewise advances to the next
// future slot (the ONE catch-up run is enough). So both policies advance the
// same way; the difference is purely how many runs fire before the advance
// (handled in the scheduler), which is why this is one method.
func (s *Service) advanceSchedule(id, cronExpr string, ranAt time.Time) error {
	var nextRun any
	if t, ok := NextRun(cronExpr, ranAt); ok {
		nextRun = t.UTC().Format(tsFormat)
	}
	_, err := s.DB.Exec(
		`UPDATE routines SET last_run_at=?, next_run_at=? WHERE id=?`,
		ranAt.UTC().Format(tsFormat), nextRun, id)
	return err
}

// advanceOnly recomputes next_run_at WITHOUT stamping last_run_at — used by the
// 'skip' policy when it drops missed slots without running (nothing actually
// ran, so last_run_at must not move).
func (s *Service) advanceOnly(id, cronExpr string, from time.Time) error {
	var nextRun any
	if t, ok := NextRun(cronExpr, from); ok {
		nextRun = t.UTC().Format(tsFormat)
	}
	_, err := s.DB.Exec(`UPDATE routines SET next_run_at=? WHERE id=?`, nextRun, id)
	return err
}

// startRun inserts a 'running' routine_runs row and returns its id.
func (s *Service) startRun(routineID, trigger string) (int64, error) {
	res, err := s.DB.Exec(
		`INSERT INTO routine_runs (routine_id, trigger, status, started_at) VALUES (?,?,'running',?)`,
		routineID, trigger, s.ts())
	if err != nil {
		return 0, err
	}
	id, _ := res.LastInsertId()
	if err := s.pruneRuns(routineID); err != nil {
		return id, err
	}
	return id, nil
}

// finishRun stamps a terminal status + detail on a run row.
func (s *Service) finishRun(runID int64, status, detail string) error {
	_, err := s.DB.Exec(
		`UPDATE routine_runs SET status=?, detail=NULLIF(?, ''), finished_at=? WHERE id=?`,
		status, detail, s.ts(), runID)
	return err
}

// pruneRuns keeps only the newest MaxRunHistory rows for a routine.
func (s *Service) pruneRuns(routineID string) error {
	_, err := s.DB.Exec(`
		DELETE FROM routine_runs
		 WHERE routine_id=? AND id NOT IN (
		   SELECT id FROM routine_runs WHERE routine_id=? ORDER BY id DESC LIMIT ?
		 )`, routineID, routineID, MaxRunHistory)
	return err
}

// Runs returns the newest run-history rows for a routine (capped at
// MaxRunHistory). Empty slice when the routine has never run.
func (s *Service) Runs(routineID string) ([]Run, error) {
	rows, err := s.DB.Query(`
		SELECT id, routine_id, trigger, status, detail, started_at, finished_at
		  FROM routine_runs WHERE routine_id=? ORDER BY id DESC LIMIT ?`,
		routineID, MaxRunHistory)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Run{}
	for rows.Next() {
		var r Run
		if err := rows.Scan(&r.ID, &r.RoutineID, &r.Trigger, &r.Status, &r.Detail,
			&r.StartedAt, &r.FinishedAt); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// HealStale marks running routine_runs rows (left by a crashed/restarted daemon)
// as 'failed' — nothing is executing them anymore. Mirrors provision.HealStale.
func (s *Service) HealStale() error {
	res, err := s.DB.Exec(
		`UPDATE routine_runs SET status='failed', detail='interrupted by daemon restart', finished_at=?
		  WHERE status='running'`, s.ts())
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n > 0 {
		logHealed(n)
	}
	return nil
}

// projectPath resolves a routine's project working directory (global → ""). The
// command/ai-prompt steps run there.
func (s *Service) projectPath(projectID sql.NullInt64) (string, error) {
	if !projectID.Valid {
		return "", nil
	}
	var path string
	err := s.DB.QueryRow(`SELECT path FROM projects WHERE id=?`, projectID.Int64).Scan(&path)
	if errors.Is(err, sql.ErrNoRows) {
		return "", fmt.Errorf("routine project %d not found", projectID.Int64)
	}
	return path, err
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

func nullifyStr(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// boolScan adapts an INTEGER 0/1 column into a *bool during Scan.
type boolScan struct{ b *bool }

func (bs *boolScan) Scan(v any) error {
	switch n := v.(type) {
	case int64:
		*bs.b = n != 0
	case nil:
		*bs.b = false
	default:
		return fmt.Errorf("boolScan: unexpected %T", v)
	}
	return nil
}
