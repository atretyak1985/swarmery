package lessons

// Lesson effectiveness (Opus 5.5 / learning-loop phase 16.1).
//
// A lesson earns its place by making its area LESS surprising. The measure is
// deliberately plain: the median surprise index (phase_surprise, 0082) of the
// scored runs in the lesson's area in the N runs before its activation, against
// the N runs after it. median_drop = before − after, so a positive drop is a
// lesson that helped.
//
// WHICH RUNS ARE "IN THE AREA". A scored run is placed by the areas its
// surprise detail names — the forecast's areas and the areas the run actually
// touched — and a lesson's glob hits it through Matches, the same rule
// injection selects lessons with (surprise.AreaOverlap underneath). There is no
// second matcher.
//
// WHICH RUNS COUNT. Only scores built on a real prediction: a post-hoc forecast
// (a prior written after the Completion Report) scores low by construction and
// would read as a lesson that worked. A run's time is its session's start, or
// the actuals' computed_at when the session was never ingested.
//
// NOT ENOUGH DATA IS NULL, NEVER ZERO. Fewer than MinRuns runs on either side
// leaves the medians and the drop nil: "cannot tell yet" must never read as "it
// made no difference", because only the second may propose a retirement.

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Verification knobs.
const (
	// EnvWindowRuns is N: runs taken on each side of the activation at most.
	EnvWindowRuns = "SWARMERY_LESSON_WINDOW_RUNS"
	// EnvStaleChurn is the share of an area's lines that may change since a
	// lesson's activation before it is proposed as stale.
	EnvStaleChurn = "SWARMERY_LESSON_STALE_CHURN"
	// EnvAutoRetireDays is how long a retirement proposal waits for the operator
	// before the daemon retires the lesson itself. "off" or "0" disables it.
	EnvAutoRetireDays = "SWARMERY_LESSON_AUTO_RETIRE_DAYS"

	DefaultWindowRuns     = 10
	MinEffectRuns         = 5
	DefaultStaleChurn     = 0.5
	DefaultAutoRetireDays = 14
	UnusedDays            = 60
	// KeepCooldownDays: a proposal the operator answered "keep" is not re-made
	// for the same reason inside this many days.
	KeepCooldownDays = 30
)

// VerifyConfig configures the verification pass.
type VerifyConfig struct {
	WindowRuns     int
	MinRuns        int
	StaleChurn     float64
	AutoRetireDays int // 0 = never auto-retire
	UnusedDays     int
	KeepCooldown   int
}

// DefaultVerifyConfig is the documented behaviour.
func DefaultVerifyConfig() VerifyConfig {
	return VerifyConfig{
		WindowRuns: DefaultWindowRuns, MinRuns: MinEffectRuns, StaleChurn: DefaultStaleChurn,
		AutoRetireDays: DefaultAutoRetireDays, UnusedDays: UnusedDays, KeepCooldown: KeepCooldownDays,
	}
}

// VerifyConfigFromEnv reads the three knobs; an unreadable value keeps its
// default and is reported as a warning.
func VerifyConfigFromEnv(getenv func(string) string) (VerifyConfig, []string) {
	c := DefaultVerifyConfig()
	var warn []string
	if v := strings.TrimSpace(getenv(EnvWindowRuns)); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= c.MinRuns {
			c.WindowRuns = n
		} else {
			warn = append(warn, fmt.Sprintf("%s: ignoring %q (want an integer ≥ %d)", EnvWindowRuns, v, c.MinRuns))
		}
	}
	if v := strings.TrimSpace(getenv(EnvStaleChurn)); v != "" {
		if f, err := strconv.ParseFloat(v, 64); err == nil && f > 0 && f <= 1 {
			c.StaleChurn = f
		} else {
			warn = append(warn, fmt.Sprintf("%s: ignoring %q (want a share in (0, 1])", EnvStaleChurn, v))
		}
	}
	if v := strings.ToLower(strings.TrimSpace(getenv(EnvAutoRetireDays))); v != "" {
		if v == "off" {
			c.AutoRetireDays = 0
		} else if n, err := strconv.Atoi(v); err == nil && n >= 0 {
			c.AutoRetireDays = n
		} else {
			warn = append(warn, fmt.Sprintf("%s: ignoring %q (want days ≥ 0 or off)", EnvAutoRetireDays, v))
		}
	}
	return c, warn
}

func (c VerifyConfig) String() string {
	auto := "off"
	if c.AutoRetireDays > 0 {
		auto = strconv.Itoa(c.AutoRetireDays) + "d"
	}
	return fmt.Sprintf("window=%d runs (min %d), stale churn>%.2f, unused>%dd, auto-retire=%s",
		c.WindowRuns, c.MinRuns, c.StaleChurn, c.UnusedDays, auto)
}

// EffectivenessRow is one lesson_effectiveness row. Pointer fields are nil for
// "not enough data" / "never injected".
type EffectivenessRow struct {
	LessonID     int64    `json:"lessonId"`
	WindowN      int      `json:"windowN"`
	MinRuns      int      `json:"minRuns"`
	BeforeN      int      `json:"beforeN"`
	AfterN       int      `json:"afterN"`
	MedianBefore *float64 `json:"medianBefore"`
	MedianAfter  *float64 `json:"medianAfter"`
	MedianDrop   *float64 `json:"medianDrop"`
	Uses         int      `json:"uses"`
	Relied       int      `json:"relied"`
	ReliedRate   *float64 `json:"reliedRate"`
	ComputedAt   string   `json:"computedAt"`
}

// AreaRun is one scored run as effectiveness reads it.
type AreaRun struct {
	SessionUUID string
	At          time.Time
	Index       float64
	Areas       []string
}

// Median of xs (nil for none).
func Median(xs []float64) *float64 {
	if len(xs) == 0 {
		return nil
	}
	s := append([]float64(nil), xs...)
	sort.Float64s(s)
	m := s[len(s)/2]
	if len(s)%2 == 0 {
		m = (s[len(s)/2-1] + s[len(s)/2]) / 2
	}
	m = math.Round(m*1e4) / 1e4
	return &m
}

// Effect splits runs (any order) around activatedAt and measures the drop: the
// window runs closest to the activation on each side. Pure; unit-tested.
func Effect(runs []AreaRun, activatedAt time.Time, window, minRuns int) (before, after []float64, drop, mb, ma *float64) {
	sorted := append([]AreaRun(nil), runs...)
	sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].At.Before(sorted[j].At) })
	var pre, post []AreaRun
	for _, r := range sorted {
		if r.At.Before(activatedAt) {
			pre = append(pre, r)
		} else {
			post = append(post, r)
		}
	}
	if len(pre) > window {
		pre = pre[len(pre)-window:]
	}
	if len(post) > window {
		post = post[:window]
	}
	for _, r := range pre {
		before = append(before, r.Index)
	}
	for _, r := range post {
		after = append(after, r.Index)
	}
	if len(before) < minRuns || len(after) < minRuns {
		return before, after, nil, nil, nil
	}
	mb, ma = Median(before), Median(after)
	d := math.Round((*mb-*ma)*1e4) / 1e4
	return before, after, &d, mb, ma
}

func parseTS(s string) (time.Time, bool) {
	s = strings.TrimSpace(s)
	for _, layout := range []string{time.RFC3339Nano, "2006-01-02 15:04:05", "2006-01-02T15:04:05"} {
		if t, err := time.Parse(layout, s); err == nil {
			return t.UTC(), true
		}
	}
	return time.Time{}, false
}

// LoadAreaRuns reads every scored, non-post-hoc run with the areas it names.
func LoadAreaRuns(db *sql.DB) ([]AreaRun, error) {
	rows, err := db.Query(`SELECT ps.session_uuid, ps.surprise_index, ps.detail_json,
		COALESCE(s.started_at, pa.computed_at, ps.computed_at), COALESCE(pa.areas_json, '[]')
		FROM phase_surprise ps
		LEFT JOIN sessions s ON s.session_uuid = ps.session_uuid
		LEFT JOIN phase_actuals pa ON pa.session_uuid = ps.session_uuid
		WHERE ps.forecast_post_hoc = 0`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []AreaRun
	for rows.Next() {
		var (
			r                  AreaRun
			detail, at, actual string
			d                  struct{ ForecastAreas, ActualAreas []string }
			actualAreas        []string
		)
		if err := rows.Scan(&r.SessionUUID, &r.Index, &detail, &at, &actual); err != nil {
			return nil, err
		}
		t, ok := parseTS(at)
		if !ok {
			continue
		}
		r.At = t
		_ = json.Unmarshal([]byte(detail), &d)
		_ = json.Unmarshal([]byte(actual), &actualAreas)
		r.Areas = append(append(append(r.Areas, d.ForecastAreas...), d.ActualAreas...), actualAreas...)
		out = append(out, r)
	}
	return out, rows.Err()
}

// inArea keeps the runs a lesson's globs overlap.
func inArea(globs []string, runs []AreaRun) []AreaRun {
	var out []AreaRun
	for _, r := range runs {
		if Matches(globs, Scope{Areas: r.Areas}) {
			out = append(out, r)
		}
	}
	return out
}

// ComputeEffectiveness measures one lesson against pre-loaded runs.
func ComputeEffectiveness(db *sql.DB, l Active, runs []AreaRun, cfg VerifyConfig, now time.Time) (EffectivenessRow, error) {
	row := EffectivenessRow{LessonID: l.ID, WindowN: cfg.WindowRuns, MinRuns: cfg.MinRuns, ComputedAt: stamp(now)}
	if act, ok := parseTS(l.ActivatedAt); ok {
		before, after, drop, mb, ma := Effect(inArea(l.AreaGlobs, runs), act, cfg.WindowRuns, cfg.MinRuns)
		row.BeforeN, row.AfterN, row.MedianDrop, row.MedianBefore, row.MedianAfter = len(before), len(after), drop, mb, ma
	}
	if err := db.QueryRow(`SELECT COUNT(*), COALESCE(SUM(relied_on), 0) FROM lesson_uses WHERE lesson_id = ?`,
		l.ID).Scan(&row.Uses, &row.Relied); err != nil {
		return row, err
	}
	if row.Uses > 0 {
		r := math.Round(float64(row.Relied)/float64(row.Uses)*1e4) / 1e4
		row.ReliedRate = &r
	}
	return row, nil
}

// StoreEffectiveness replaces the lesson's row.
func StoreEffectiveness(db *sql.DB, r EffectivenessRow) error {
	_, err := db.Exec(`INSERT INTO lesson_effectiveness (lesson_id, window_n, min_runs, before_n, after_n,
		median_before, median_after, median_drop, uses, relied, relied_rate, computed_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(lesson_id) DO UPDATE SET window_n = excluded.window_n, min_runs = excluded.min_runs,
		before_n = excluded.before_n, after_n = excluded.after_n, median_before = excluded.median_before,
		median_after = excluded.median_after, median_drop = excluded.median_drop, uses = excluded.uses,
		relied = excluded.relied, relied_rate = excluded.relied_rate, computed_at = excluded.computed_at`,
		r.LessonID, r.WindowN, r.MinRuns, r.BeforeN, r.AfterN, nullF(r.MedianBefore), nullF(r.MedianAfter),
		nullF(r.MedianDrop), r.Uses, r.Relied, nullF(r.ReliedRate), r.ComputedAt)
	return err
}

func nullF(p *float64) any {
	if p == nil {
		return nil
	}
	return *p
}

const selectEffectiveness = `SELECT lesson_id, window_n, min_runs, before_n, after_n, median_before,
	median_after, median_drop, uses, relied, relied_rate, computed_at FROM lesson_effectiveness`

func scanEffectiveness(scan func(...any) error) (EffectivenessRow, error) {
	var (
		r              EffectivenessRow
		mb, ma, d, rel sql.NullFloat64
	)
	err := scan(&r.LessonID, &r.WindowN, &r.MinRuns, &r.BeforeN, &r.AfterN, &mb, &ma, &d, &r.Uses, &r.Relied,
		&rel, &r.ComputedAt)
	r.MedianBefore, r.MedianAfter, r.MedianDrop, r.ReliedRate = fptr(mb), fptr(ma), fptr(d), fptr(rel)
	return r, err
}

func fptr(v sql.NullFloat64) *float64 {
	if !v.Valid {
		return nil
	}
	f := v.Float64
	return &f
}

// LoadEffectiveness reads the stored rows for ids (every row when ids is empty).
func LoadEffectiveness(db *sql.DB, ids []int64) (map[int64]EffectivenessRow, error) {
	q, args := selectEffectiveness, []any{}
	if len(ids) > 0 {
		q += ` WHERE lesson_id IN (` + strings.TrimSuffix(strings.Repeat("?,", len(ids)), ",") + `)`
		for _, id := range ids {
			args = append(args, id)
		}
	}
	rows, err := db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int64]EffectivenessRow{}
	for rows.Next() {
		r, err := scanEffectiveness(rows.Scan)
		if err != nil {
			return nil, err
		}
		out[r.LessonID] = r
	}
	return out, rows.Err()
}

// StoredEffectiveness is the injection ranking hook: the stored median drop of
// every lesson that has one. A lesson without enough data stays unscored.
func StoredEffectiveness(db *sql.DB, ids []int64) (map[int64]float64, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	rows, err := LoadEffectiveness(db, ids)
	if err != nil {
		return nil, err
	}
	out := map[int64]float64{}
	for id, r := range rows {
		if r.MedianDrop != nil {
			out[id] = *r.MedianDrop
		}
	}
	return out, nil
}

// attachEffectiveness fills Effectiveness on every lesson in ls.
func attachEffectiveness(db *sql.DB, ls []Lesson) error {
	if len(ls) == 0 {
		return nil
	}
	ids := make([]int64, len(ls))
	for i, l := range ls {
		ids[i] = l.ID
	}
	rows, err := LoadEffectiveness(db, ids)
	if err != nil {
		return err
	}
	for i := range ls {
		if r, ok := rows[ls[i].ID]; ok {
			r := r
			ls[i].Effectiveness = &r
		}
	}
	return nil
}
