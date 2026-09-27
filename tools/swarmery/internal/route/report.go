package route

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/modelid"
)

// MinSamples is the gate below which a report group is never returned — the
// same 20 internal/calibration uses, for the same reason: a failure rate drawn
// from a handful of runs is noise that reads as a finding. It is a copy, not an
// import, because calibration → surprise → actuals → verify → phaserun → route
// would be a cycle; api's tests pin the two together.
const MinSamples = 20

// Group is one shown cell of the report.
type Group struct {
	Surface string `json:"surface"`
	Tier    string `json:"tier,omitempty"`
	// Model is the model that RAN, normalised to its family ("sonnet").
	Model string `json:"model,omitempty"`
	// Pick is the router's pick (divergent cells only).
	Pick     string   `json:"pick,omitempty"`
	N        int      `json:"n"`
	Failures int      `json:"failures"`
	FailRate float64  `json:"failRate"`
	CostN    int      `json:"costN"` // rows with a known cost; mean/p90 are over these
	MeanCost *float64 `json:"meanCost"`
	P90Cost  *float64 `json:"p90Cost"`
	// Agree is the share of rows whose pick and used model name the same model.
	Agree float64 `json:"agree"`
	// PickRan is, for a divergent cell, the same surface and tier when the
	// PICKED model is the one that ran — the other half of "would it have been
	// cheaper/safer". nil when that cell is under the gate or empty.
	PickRan *Group `json:"pickRan,omitempty"`
}

// Section is one grouping: the cells shown and how many were hidden.
type Section struct {
	Groups       []Group `json:"groups"`
	HiddenGroups int     `json:"hiddenGroups"`
	HiddenRuns   int     `json:"hiddenRuns"`
}

// ReportResult is the routing report over one window.
type ReportResult struct {
	Surface    string `json:"surface"` // "" = both
	Days       int    `json:"days"`
	MinSamples int    `json:"minSamples"`
	// Rows counts settled rows with evidence; Unsettled those still NULL.
	Rows      int `json:"rows"`
	Unsettled int `json:"unsettled"`
	// ByTier groups by (surface, tier); ByModel by (surface, model that ran);
	// Divergent holds shadow rows whose pick differed from what ran, by
	// (surface, tier, pick, ran).
	ByTier    Section `json:"byTier"`
	ByModel   Section `json:"byModel"`
	Divergent Section `json:"divergent"`
	// HiddenGroups sums the three sections' hidden cells.
	HiddenGroups int `json:"hiddenGroups"`
}

// reportRow is one settled route_decisions row, reduced to what the report reads.
type reportRow struct {
	surface, tier, mode string
	pick, used          string // normalised
	outcome, verify     string
	cost                sql.NullFloat64
}

// normModel reduces a model name to what an agreement check can compare: its
// family when modelid knows one (so the pick alias "haiku" meets
// "claude-haiku-4-5", and "sonnet" meets "claude-sonnet-5"), otherwise the
// lower-cased id without its context-window marker.
func normModel(m string) string {
	if fam := modelid.Family(m); fam != "" {
		return fam
	}
	return strings.ToLower(modelid.Base(m))
}

// failed is the report's failure definition: the run ended failed, blocked or
// partial, or its verification failed.
func (r reportRow) failed() bool {
	switch r.outcome {
	case OutcomeFailed, OutcomeBlocked, "partial":
		return true
	}
	return r.verify == "fail"
}

// hasEvidence: a superseded or deleted row with no verdict says nothing about
// how its run went, so it is not a sample.
func (r reportRow) hasEvidence() bool {
	return !noOutcome(r.outcome) || r.verify == "pass" || r.verify == "fail"
}

// Report settles nothing: callers run Settle first. surface is "" for both.
func Report(db *sql.DB, surface string, days int) (ReportResult, error) {
	return reportAt(db, surface, days, time.Now())
}

func reportAt(db *sql.DB, surface string, days int, now time.Time) (ReportResult, error) {
	since := now.AddDate(0, 0, -days).UTC().Format(createdAtFormat)
	rows, err := db.Query(`
		SELECT surface, tier, mode, pick_model, used_model, outcome, COALESCE(verify_status, ''), cost_usd
		  FROM route_decisions
		 WHERE created_at >= ? AND (? = '' OR surface = ?)`, since, surface, surface)
	if err != nil {
		return ReportResult{}, fmt.Errorf("route: report: %w", err)
	}
	defer rows.Close()
	var (
		settledRows []reportRow
		unsettled   int
	)
	for rows.Next() {
		var (
			r       reportRow
			outcome sql.NullString
		)
		if err := rows.Scan(&r.surface, &r.tier, &r.mode, &r.pick, &r.used, &outcome, &r.verify, &r.cost); err != nil {
			return ReportResult{}, fmt.Errorf("route: report: %w", err)
		}
		if !outcome.Valid {
			unsettled++
			continue
		}
		r.outcome, r.pick, r.used = outcome.String, normModel(r.pick), normModel(r.used)
		settledRows = append(settledRows, r)
	}
	if err := rows.Err(); err != nil {
		return ReportResult{}, fmt.Errorf("route: report: %w", err)
	}
	rep := buildReport(settledRows, MinSamples)
	rep.Surface, rep.Days, rep.Unsettled = surface, days, unsettled
	return rep, nil
}

// cellKey names one cell of one grouping.
type cellKey struct{ surface, tier, used, pick string }

// buildReport groups settled rows. Pure; unit-tested.
func buildReport(all []reportRow, minSamples int) ReportResult {
	var rows []reportRow
	for _, r := range all {
		if r.hasEvidence() {
			rows = append(rows, r)
		}
	}
	rep := ReportResult{MinSamples: minSamples, Rows: len(rows)}
	rep.ByTier = section(rows, minSamples, func(r reportRow) (cellKey, bool) {
		return cellKey{surface: r.surface, tier: r.tier}, true
	})
	rep.ByModel = section(rows, minSamples, func(r reportRow) (cellKey, bool) {
		return cellKey{surface: r.surface, used: r.used}, true
	})
	rep.Divergent = section(rows, minSamples, func(r reportRow) (cellKey, bool) {
		return cellKey{surface: r.surface, tier: r.tier, pick: r.pick, used: r.used},
			r.mode == string(ModeShadow) && r.pick != r.used
	})
	// The other half of each divergent cell: same surface and tier, pick ran.
	ran := section(rows, minSamples, func(r reportRow) (cellKey, bool) {
		return cellKey{surface: r.surface, tier: r.tier, used: r.used}, true
	})
	for i := range rep.Divergent.Groups {
		d := &rep.Divergent.Groups[i]
		for j := range ran.Groups {
			g := ran.Groups[j]
			if g.Surface == d.Surface && g.Tier == d.Tier && g.Model == d.Pick {
				d.PickRan = &g
				break
			}
		}
	}
	rep.HiddenGroups = rep.ByTier.HiddenGroups + rep.ByModel.HiddenGroups + rep.Divergent.HiddenGroups
	return rep
}

// section buckets rows by key (skipping rows key rejects) and summarises every
// bucket at or above the gate; smaller ones are only counted.
func section(rows []reportRow, minSamples int, key func(reportRow) (cellKey, bool)) Section {
	buckets := map[cellKey][]reportRow{}
	var order []cellKey
	for _, r := range rows {
		k, ok := key(r)
		if !ok {
			continue
		}
		if _, seen := buckets[k]; !seen {
			order = append(order, k)
		}
		buckets[k] = append(buckets[k], r)
	}
	s := Section{Groups: []Group{}}
	for _, k := range order {
		rs := buckets[k]
		if len(rs) < minSamples {
			s.HiddenGroups++
			s.HiddenRuns += len(rs)
			continue
		}
		s.Groups = append(s.Groups, summarize(k, rs))
	}
	sort.SliceStable(s.Groups, func(i, j int) bool {
		a, b := s.Groups[i], s.Groups[j]
		if a.N != b.N {
			return a.N > b.N
		}
		return a.Surface+a.Tier+a.Model+a.Pick < b.Surface+b.Tier+b.Model+b.Pick
	})
	return s
}

func summarize(k cellKey, rs []reportRow) Group {
	g := Group{Surface: k.surface, Tier: k.tier, Model: k.used, Pick: k.pick, N: len(rs)}
	var agree int
	var costs []float64
	for _, r := range rs {
		if r.failed() {
			g.Failures++
		}
		if r.pick == r.used {
			agree++
		}
		if r.cost.Valid {
			costs = append(costs, r.cost.Float64)
		}
	}
	g.FailRate = round4(float64(g.Failures) / float64(g.N))
	g.Agree = round4(float64(agree) / float64(g.N))
	g.CostN = len(costs)
	if len(costs) > 0 {
		sort.Float64s(costs)
		var sum float64
		for _, c := range costs {
			sum += c
		}
		mean := round4(sum / float64(len(costs)))
		// Nearest-rank p90: the smallest cost at or above 90% of the runs.
		p90 := round4(costs[int(math.Ceil(0.9*float64(len(costs))))-1])
		g.MeanCost, g.P90Cost = &mean, &p90
	}
	return g
}

func round4(v float64) float64 { return math.Round(v*1e4) / 1e4 }

// Decision view: the latest route_decisions row for one subject, for the task
// card's "Route" line.

// DecisionView is one recorded decision as the API serves it.
type DecisionView struct {
	Surface      string   `json:"surface"`
	Subject      string   `json:"subject"`
	Mode         string   `json:"mode"`
	Score        int      `json:"score"`
	Tier         string   `json:"tier"`
	PickModel    string   `json:"pickModel"`
	PickEffort   string   `json:"pickEffort"`
	PickPlaybook string   `json:"pickPlaybook"`
	Reasons      []string `json:"reasons"`
	Applied      bool     `json:"applied"`
	UsedModel    string   `json:"usedModel"`
	UsedEffort   string   `json:"usedEffort"`
	UsedPlaybook string   `json:"usedPlaybook"`
	WonRung      string   `json:"wonRung"`
	Outcome      *string  `json:"outcome"`
	VerifyStatus *string  `json:"verifyStatus"`
	CostUSD      *float64 `json:"costUsd"`
	CreatedAt    string   `json:"createdAt"`
}

// ErrNoDecision: the subject has no recorded decision.
var ErrNoDecision = errors.New("route: no decision recorded for subject")

// LatestDecision returns the subject's newest decision.
func LatestDecision(db *sql.DB, subject string) (DecisionView, error) {
	var (
		v               DecisionView
		reasons         string
		applied         int
		outcome, verify sql.NullString
		cost            sql.NullFloat64
	)
	err := db.QueryRow(`
		SELECT surface, subject, mode, score, tier, pick_model, pick_effort, pick_playbook,
		       reasons_json, applied, used_model, used_effort, used_playbook, won_rung,
		       outcome, verify_status, cost_usd, created_at
		  FROM route_decisions WHERE subject = ? ORDER BY id DESC LIMIT 1`, subject).Scan(
		&v.Surface, &v.Subject, &v.Mode, &v.Score, &v.Tier, &v.PickModel, &v.PickEffort, &v.PickPlaybook,
		&reasons, &applied, &v.UsedModel, &v.UsedEffort, &v.UsedPlaybook, &v.WonRung,
		&outcome, &verify, &cost, &v.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return v, ErrNoDecision
	}
	if err != nil {
		return v, fmt.Errorf("route: latest decision %s: %w", subject, err)
	}
	v.Applied = applied != 0
	if json.Unmarshal([]byte(reasons), &v.Reasons) != nil || v.Reasons == nil {
		v.Reasons = []string{} // a malformed or null list reads as none
	}
	if outcome.Valid {
		v.Outcome = &outcome.String
	}
	if verify.Valid {
		v.VerifyStatus = &verify.String
	}
	if cost.Valid {
		v.CostUSD = &cost.Float64
	}
	return v, nil
}
