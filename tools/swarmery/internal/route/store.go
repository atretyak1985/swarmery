package route

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"time"
)

// This is the package's ONLY database-touching file: Decide and Score stay
// pure, and a caller that wants a decision on the record hands the result here.

// Rungs name which step of an EXISTING ladder produced a value that ran
// (route_decisions.won_rung). RungRoute is reserved for a pick the router
// itself put on the spawn (active mode); shadow never records it.
const (
	RungCard     = "card"
	RungPlaybook = "playbook"
	RungRequest  = "request"
	RungDoc      = "doc"
	RungRoute    = "route"
	RungEnv      = "env"
	RungDefault  = "default"
)

// createdAtFormat matches the millisecond-Z timestamps the dispatch and api
// packages write, so created_at sorts lexically in time order.
const createdAtFormat = "2006-01-02T15:04:05.000Z"

// Row is one route_decisions record: the signals, the router's decision, and
// what the existing ladders actually put on the spawn.
type Row struct {
	Surface     Surface
	Subject     string // SubjectTask / SubjectPhase
	SessionUUID string // the run's first spawned session
	Mode        Mode   // shadow | active — never off: off records nothing
	Signals     Signals
	Decision    Decision
	// Applied is true when ANY part of the decision's pick reached the spawn —
	// its model, its effort or its playbook won its ladder. Only active mode
	// can set it; a shadow row is always applied=0. Compare used_* with pick_*
	// to see which parts ran.
	Applied      bool
	UsedModel    string
	UsedEffort   string
	UsedPlaybook string
	// WonRung is the rung of the MODEL ladder that produced UsedModel — one
	// token, so the effort and playbook rungs are not recorded here. RungRoute
	// means the router's model pick ran, which implies Applied.
	WonRung string
	// CreatedAt defaults to time.Now when zero.
	CreatedAt time.Time
}

// SubjectTask is the subject of a board card's decision: "task:<tasks.id>".
func SubjectTask(id int64) string { return "task:" + strconv.FormatInt(id, 10) }

// SubjectPhase is the subject of a phase run's decision: "phase:<epic_phases.id>".
func SubjectPhase(id int64) string { return "phase:" + strconv.FormatInt(id, 10) }

// signalsDoc is the stable, snake_case JSON shape of Signals in signals_json.
// Kept apart from Signals so the in-memory type carries no storage concerns.
type signalsDoc struct {
	Surface      Surface  `json:"surface"`
	PromptBytes  int      `json:"prompt_bytes"`
	FileScope    int      `json:"file_scope"`
	Areas        int      `json:"areas"`
	RiskPaths    []string `json:"risk_paths"`
	Deps         int      `json:"deps"`
	ForecastSize string   `json:"forecast_size"`
	HistFailRate float64  `json:"hist_fail_rate"`
	HistSamples  int      `json:"hist_samples"`
}

// errRowIncomplete is returned for a row missing a key column.
var errRowIncomplete = errors.New("route: decision row needs surface, subject and a shadow|active mode")

// errRowInconsistent is returned for a row whose applied flag and won_rung
// contradict its mode: a shadow row that claims the pick ran, or a route rung
// that did not count as applied. Refusing it keeps the report's applied split
// honest instead of trusting every caller to get it right.
var errRowInconsistent = errors.New("route: applied/won_rung contradict the mode (shadow never applies; won_rung=route implies applied)")

// Record inserts one route_decisions row. Callers log a failure and carry on:
// a routing record is advisory and must never fail the run it describes.
func Record(db *sql.DB, r Row) error {
	if r.Surface == "" || r.Subject == "" || (r.Mode != ModeShadow && r.Mode != ModeActive) {
		return errRowIncomplete
	}
	if (r.Mode == ModeShadow && (r.Applied || r.WonRung == RungRoute)) ||
		(r.WonRung == RungRoute && !r.Applied) {
		return errRowInconsistent
	}
	risks := r.Signals.RiskPaths
	if risks == nil {
		risks = []string{} // "[]", not null: the column means "matched nothing"
	}
	sig, err := json.Marshal(signalsDoc{
		Surface:      r.Signals.Surface,
		PromptBytes:  r.Signals.PromptBytes,
		FileScope:    r.Signals.FileScope,
		Areas:        r.Signals.Areas,
		RiskPaths:    risks,
		Deps:         r.Signals.Deps,
		ForecastSize: r.Signals.ForecastSize,
		HistFailRate: r.Signals.HistFailRate,
		HistSamples:  r.Signals.HistSamples,
	})
	if err != nil {
		return fmt.Errorf("route: encode signals: %w", err)
	}
	reasons := r.Decision.Reasons
	if reasons == nil {
		reasons = []string{}
	}
	rsn, err := json.Marshal(reasons)
	if err != nil {
		return fmt.Errorf("route: encode reasons: %w", err)
	}
	at := r.CreatedAt
	if at.IsZero() {
		at = time.Now()
	}
	applied := 0
	if r.Applied {
		applied = 1
	}
	if _, err := db.Exec(`
		INSERT INTO route_decisions
		  (surface, subject, session_uuid, mode, signals_json, score, tier,
		   pick_model, pick_effort, pick_playbook, reasons_json, applied,
		   used_model, used_effort, used_playbook, won_rung, created_at)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		string(r.Surface), r.Subject, r.SessionUUID, string(r.Mode), string(sig),
		r.Decision.Score, r.Decision.Tier, r.Decision.Model, r.Decision.Effort,
		r.Decision.Playbook, string(rsn), applied,
		r.UsedModel, r.UsedEffort, r.UsedPlaybook, r.WonRung,
		at.UTC().Format(createdAtFormat)); err != nil {
		return fmt.Errorf("route: record decision for %s: %w", r.Subject, err)
	}
	return nil
}
