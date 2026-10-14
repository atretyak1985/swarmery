package turnforecast

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// Half names which side of a turn a payload is.
type Half string

const (
	HalfForecast Half = "forecast"
	HalfActual   Half = "actual"
)

// TSFormat is the fixed-width ms UTC form turn_forecasts.started_at is stored
// in — the link's own timestamp form, so TEXT order is time order.
const TSFormat = "2006-01-02T15:04:05.000Z"

// HitBelow is the surprise under which a turn counts as "the forecast hit" for
// calibration.
const HitBelow = 0.25

// MinCalibration is the turn count under which a calibration is flagged
// tooFew: below it the bins are noise.
const MinCalibration = 30

// Querier is the slice of *sql.DB / *sql.Tx the writer needs, so the link
// ingest can upsert inside its own batch transaction.
type Querier interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// Store reads and writes turn_forecasts. Weights nil means DefaultWeights.
type Store struct {
	DB      *sql.DB
	Weights map[string]float64
}

// Upserted reports what one Upsert left behind.
type Upserted struct {
	ID int64 // turn_forecasts.id
	// Scored is true when the row carries a score after this write — both
	// halves present and at least one component measurable.
	Scored   bool
	Surprise *float64
	Top      string
}

// Upsert stores one half in its own transaction. See UpsertTx.
func (s *Store) Upsert(ctx context.Context, sessionID int64, turnSeq int, half Half, at string, payload []byte) (Upserted, error) {
	if s.DB == nil {
		return Upserted{}, errors.New("turnforecast: no database")
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return Upserted{}, fmt.Errorf("turnforecast: begin: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck // no-op after Commit
	u, err := s.UpsertTx(ctx, tx, sessionID, turnSeq, half, at, payload)
	if err != nil {
		return Upserted{}, err
	}
	if err := tx.Commit(); err != nil {
		return Upserted{}, fmt.Errorf("turnforecast: commit: %w", err)
	}
	return u, nil
}

// UpsertTx stores one half of turn (sessionID, turnSeq) through q. payload is
// the raw event payload (validated here again; it is stored verbatim). at is
// the event's time in TSFormat. Re-sending a half replaces it. When both halves
// are present after the write, the row is (re)scored in the same statement;
// otherwise surprise and vector are NULL.
//
// started_at is the forecast's at once a forecast is present, else the
// actual's. model and forecast_tokens come from the forecast half.
func (s *Store) UpsertTx(ctx context.Context, q Querier, sessionID int64, turnSeq int, half Half, at string, payload []byte) (Upserted, error) {
	switch half {
	case HalfForecast:
		if _, err := ParseForecast(payload); err != nil {
			return Upserted{}, fmt.Errorf("turnforecast: forecast payload: %w", err)
		}
	case HalfActual:
		if _, err := ParseActual(payload); err != nil {
			return Upserted{}, fmt.Errorf("turnforecast: actual payload: %w", err)
		}
	default:
		return Upserted{}, fmt.Errorf("turnforecast: unknown half %q", half)
	}

	var (
		fcRaw, acRaw sql.NullString
		startedAt    sql.NullString
	)
	err := q.QueryRowContext(ctx,
		`SELECT forecast, actual, started_at FROM turn_forecasts WHERE session_id = ? AND turn_seq = ?`,
		sessionID, turnSeq).Scan(&fcRaw, &acRaw, &startedAt)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return Upserted{}, fmt.Errorf("turnforecast: read turn: %w", err)
	}

	if half == HalfForecast {
		fcRaw = sql.NullString{String: string(payload), Valid: true}
		startedAt = sql.NullString{String: at, Valid: true}
	} else {
		acRaw = sql.NullString{String: string(payload), Valid: true}
		if !fcRaw.Valid || !startedAt.Valid {
			startedAt = sql.NullString{String: at, Valid: true}
		}
	}

	var (
		model    any
		tokens   any
		surprise any
		vector   any
		out      Upserted
	)
	var f Forecast
	if fcRaw.Valid {
		if f, err = ParseForecast([]byte(fcRaw.String)); err != nil {
			return Upserted{}, fmt.Errorf("turnforecast: stored forecast: %w", err)
		}
		if f.Model != "" {
			model = f.Model
		}
		if f.ForecastTokens != nil {
			tokens = *f.ForecastTokens
		}
	}
	if fcRaw.Valid && acRaw.Valid {
		a, err := ParseActual([]byte(acRaw.String))
		if err != nil {
			return Upserted{}, fmt.Errorf("turnforecast: stored actual: %w", err)
		}
		if r, ok := Compute(f, a, s.Weights); ok {
			b, err := json.Marshal(r)
			if err != nil {
				return Upserted{}, fmt.Errorf("turnforecast: vector: %w", err)
			}
			surprise, vector = r.Index, string(b)
			idx := r.Index
			out.Scored, out.Surprise, out.Top = true, &idx, r.Top
		}
	}

	err = q.QueryRowContext(ctx, `
		INSERT INTO turn_forecasts (session_id, turn_seq, started_at, forecast, actual, surprise, vector, model, forecast_tokens)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(session_id, turn_seq) DO UPDATE SET
		  started_at = excluded.started_at, forecast = excluded.forecast, actual = excluded.actual,
		  surprise = excluded.surprise, vector = excluded.vector,
		  model = excluded.model, forecast_tokens = excluded.forecast_tokens
		RETURNING id`,
		sessionID, turnSeq, startedAt.String, nullable(fcRaw), nullable(acRaw), surprise, vector, model, tokens,
	).Scan(&out.ID)
	if err != nil {
		return Upserted{}, fmt.Errorf("turnforecast: upsert turn: %w", err)
	}
	return out, nil
}

func nullable(s sql.NullString) any {
	if !s.Valid {
		return nil
	}
	return s.String
}

// Bin is one confidence bucket of a calibration.
type Bin struct {
	Lo float64 `json:"lo"`
	Hi float64 `json:"hi"`
	N  int     `json:"n"`
	// MeanConfidence / HitRate are null for an empty bin — no turns is not a
	// 0 % hit rate.
	MeanConfidence *float64 `json:"meanConfidence"`
	HitRate        *float64 `json:"hitRate"`
}

// Calibration is how well the forecasts' stated confidence matched how often
// they hit (surprise < HitBelow), over the scored turns that carried a
// confidence.
type Calibration struct {
	// N is the number of turns the bins cover: scored AND with a confidence.
	N      int   `json:"n"`
	TooFew bool  `json:"tooFew"`
	Bins   []Bin `json:"bins"`
	// AvgForecastTokens is the mean forecast_tokens over every turn in the
	// window that reported one (scored or not); null when none did.
	AvgForecastTokens *float64 `json:"avgForecastTokens"`
}

// binEdges are the fixed confidence buckets: [0,.2) [.2,.4) [.4,.6) [.6,.8) [.8,1].
var binEdges = [...]float64{0, 0.2, 0.4, 0.6, 0.8, 1}

// binOf places a confidence by explicit comparison (a float division would put
// 0.6 into the [.4,.6) bucket).
func binOf(c float64) int {
	for i := 1; i < len(binEdges)-1; i++ {
		if c < binEdges[i] {
			return i - 1
		}
	}
	return len(binEdges) - 2
}

// windowFilter renders the shared WHERE for a window and an optional project.
func windowFilter(projectID *int64, since time.Time) (string, []any) {
	where := ` WHERE tf.started_at >= ?`
	args := []any{since.UTC().Format(TSFormat)}
	if projectID != nil {
		where += ` AND s.project_id = ?`
		args = append(args, *projectID)
	}
	return where, args
}

// Calibration bins the window's scored turns by forecast confidence.
// projectID nil means every project.
func (s *Store) Calibration(ctx context.Context, projectID *int64, since time.Time) (Calibration, error) {
	cal := Calibration{Bins: make([]Bin, len(binEdges)-1)}
	for i := range cal.Bins {
		cal.Bins[i].Lo, cal.Bins[i].Hi = binEdges[i], binEdges[i+1]
	}
	where, args := windowFilter(projectID, since)

	rows, err := s.DB.QueryContext(ctx, `
		SELECT json_extract(tf.forecast, '$.confidence'), tf.surprise
		  FROM turn_forecasts tf LEFT JOIN sessions s ON s.id = tf.session_id`+where+`
		   AND tf.surprise IS NOT NULL
		   AND json_type(tf.forecast, '$.confidence') IN ('integer', 'real')`, args...)
	if err != nil {
		return Calibration{}, fmt.Errorf("turnforecast: calibration: %w", err)
	}
	sumConf := make([]float64, len(cal.Bins))
	hits := make([]int, len(cal.Bins))
	for rows.Next() {
		var c, surprise float64
		if err := rows.Scan(&c, &surprise); err != nil {
			rows.Close()
			return Calibration{}, fmt.Errorf("turnforecast: calibration scan: %w", err)
		}
		c = clip01(c)
		i := binOf(c)
		cal.Bins[i].N++
		sumConf[i] += c
		if surprise < HitBelow {
			hits[i]++
		}
		cal.N++
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return Calibration{}, fmt.Errorf("turnforecast: calibration rows: %w", err)
	}
	rows.Close()
	for i := range cal.Bins {
		if n := cal.Bins[i].N; n > 0 {
			cal.Bins[i].MeanConfidence = ptr(round4(sumConf[i] / float64(n)))
			cal.Bins[i].HitRate = ptr(round4(float64(hits[i]) / float64(n)))
		}
	}
	cal.TooFew = cal.N < MinCalibration

	var avg sql.NullFloat64
	if err := s.DB.QueryRowContext(ctx, `
		SELECT AVG(tf.forecast_tokens)
		  FROM turn_forecasts tf LEFT JOIN sessions s ON s.id = tf.session_id`+where+`
		   AND tf.forecast_tokens IS NOT NULL`, args...).Scan(&avg); err != nil {
		return Calibration{}, fmt.Errorf("turnforecast: avg tokens: %w", err)
	}
	if avg.Valid {
		cal.AvgForecastTokens = ptr(round4(avg.Float64))
	}
	return cal, nil
}

// Turn is one scored turn as Insights and the turn_surprise frame show it.
type Turn struct {
	ID          int64   `json:"id"`
	SessionID   int64   `json:"sessionId"`
	SessionUUID *string `json:"sessionUuid"`
	ProjectID   *int64  `json:"projectId"`
	TurnSeq     int     `json:"turnSeq"`
	StartedAt   string  `json:"startedAt"`
	Surprise    float64 `json:"surprise"`
	Top         string  `json:"top"`
	Model       string  `json:"model"`
}

const turnSelect = `
	SELECT tf.id, tf.session_id, s.session_uuid, s.project_id, tf.turn_seq, tf.started_at,
	       tf.surprise, COALESCE(json_extract(tf.vector, '$.top'), ''), COALESCE(tf.model, '')
	  FROM turn_forecasts tf LEFT JOIN sessions s ON s.id = tf.session_id`

func scanTurn(scan func(...any) error) (Turn, error) {
	var (
		t       Turn
		uuid    sql.NullString
		project sql.NullInt64
	)
	if err := scan(&t.ID, &t.SessionID, &uuid, &project, &t.TurnSeq, &t.StartedAt, &t.Surprise, &t.Top, &t.Model); err != nil {
		return Turn{}, err
	}
	if uuid.Valid {
		t.SessionUUID = &uuid.String
	}
	if project.Valid {
		t.ProjectID = &project.Int64
	}
	return t, nil
}

// TopSurprises returns the window's most surprising scored turns, highest
// first (ties: newest first, then id). limit ≤ 0 means 10.
func (s *Store) TopSurprises(ctx context.Context, projectID *int64, since time.Time, limit int) ([]Turn, error) {
	if limit <= 0 {
		limit = 10
	}
	where, args := windowFilter(projectID, since)
	rows, err := s.DB.QueryContext(ctx, turnSelect+where+`
		   AND tf.surprise IS NOT NULL
		 ORDER BY tf.surprise DESC, tf.started_at DESC, tf.id DESC
		 LIMIT ?`, append(args, limit)...)
	if err != nil {
		return nil, fmt.Errorf("turnforecast: top surprises: %w", err)
	}
	defer rows.Close()
	out := []Turn{}
	for rows.Next() {
		t, err := scanTurn(rows.Scan)
		if err != nil {
			return nil, fmt.Errorf("turnforecast: top surprises scan: %w", err)
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// LoadTurn reads one scored turn by turn_forecasts.id; nil when the row is gone
// or not scored.
func (s *Store) LoadTurn(ctx context.Context, id int64) (*Turn, error) {
	t, err := scanTurn(s.DB.QueryRowContext(ctx, turnSelect+` WHERE tf.id = ? AND tf.surprise IS NOT NULL`, id).Scan)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("turnforecast: load turn: %w", err)
	}
	return &t, nil
}
