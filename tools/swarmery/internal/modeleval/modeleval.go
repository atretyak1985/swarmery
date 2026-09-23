// Package modeleval turns the advisory trajectory judgments into one dated,
// gateable fact: "the agent fleet still behaved on model X".
//
// It does not score anything itself — internal/trajjudge already grades real
// agent trajectories on a 4-dimension rubric. This package aggregates those
// judgments by SUBJECT model (the model the agents were running on, from
// turns.model) rather than by judge model (trajectory_judgments.model, which
// is provenance), applies the golden set's per-agent weights, and writes one
// row to model_validations.
//
// The verdict is deliberately three-valued. `inconclusive` is not a soft fail:
// a model nobody has run yet has no evidence either way, and saying so is the
// only honest answer. The PreModelSwitch gate treats missing and inconclusive
// the same way — block, and name the override — because the override is how
// the first trajectories on a new model get produced at all.
package modeleval

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/modelid"
)

// modelBaseExpr strips the trailing context-window marker from turns.model
// INSIDE SQL: `claude-opus-5-5[1m]` and `claude-opus-5-5` are one model bought
// with two different context windows, and every query here compares model ids
// for equality.
//
// Without it the marker split the evidence three ways at once: a 1M-window
// session never matched `WHERE model = ?` for its own model, so it was invisible
// to gather(); it counted as a SECOND model in `COUNT(DISTINCT model) = 1`, so
// any judged session that also had normal-window turns was discarded as "mixed";
// and it formed its own row in baseline(), diluting the incumbent it should have
// reinforced. The Go side strips the parameter with modelid.Base so both sides
// of every comparison are the same shape.
const modelBaseExpr = `CASE WHEN INSTR(t.model, '[') > 1 ` +
	`THEN SUBSTR(t.model, 1, INSTR(t.model, '[') - 1) ELSE t.model END`

// RegressionMargin is how far below the incumbent baseline a candidate model
// may score before it is called a regression.
//
// The gate is RELATIVE on purpose. An absolute bar was tried first and had to
// be abandoned: measured against the live corpus, trajjudge means sit at 3.04
// (opus-5), 3.25 (opus-4-8) and 1.62 (sonnet-5), so any threshold high enough
// to feel like "good" — 3.5 was the first guess — fails every model including
// the one currently in use. A gate that blocks the status quo is not a gate,
// it is an outage.
//
// The question a model-upgrade gate actually has to answer is not "is this
// model good" but "is this model worse than what we are already running", and
// that question calibrates itself as the judge and the fleet both drift.
const RegressionMargin = 0.35

// The baseline is the VOLUME-WEIGHTED mean across every model that already has
// enough judged trajectories — the fleet's typical measured performance, not
// its best day.
//
// Taking the maximum instead was tried and is wrong twice over. It is a
// one-way ratchet: the luckiest model ever measured sets a bar nothing can
// fall back below. And it hands the most authority to the SMALLEST sample —
// measured on the live corpus, a model with 24 judgments (3.44) set a bar the
// model with 179 (2.83) failed by 0.61, which would have blocked the model in
// daily use on the strength of the thinnest evidence available.
//
// Weighting by judgment count makes the bar reflect the models actually in
// service, and a candidate materially below typical is what a regression is.

// MinTrajectories is the fewest judged trajectories that can support a pass or
// a fail. Below it the verdict is inconclusive: one lucky or unlucky run is not
// evidence about a model.
const MinTrajectories = 5

// baseline returns the volume-weighted mean across models that already carry
// enough judged trajectories, excluding the candidate itself, plus a short
// description of what went into it. ok=false when nothing is established yet —
// the first model ever evaluated has nothing to regress from.
func baseline(db *sql.DB, exclude string) (float64, string, bool, error) {
	// Same two corrections as gather(): dedupe before averaging, and count a
	// judgment only for a session that ran on a single model.
	rows, err := db.Query(`
		WITH scoped AS (
		  SELECT tj.id, tj.overall, MIN(`+modelBaseExpr+`) AS model
		    FROM trajectory_judgments tj
		    JOIN turns t
		      ON t.session_id = tj.session_id
		     AND ( (tj.agent = 'main' AND t.agent_name IS NULL)
		        OR  t.agent_name = tj.agent )
		   WHERE t.model IS NOT NULL AND t.model <> ''
		   GROUP BY tj.id, tj.overall
		  HAVING COUNT(DISTINCT `+modelBaseExpr+`) = 1
		)
		SELECT model, AVG(overall) AS mean, COUNT(*) AS n
		  FROM scoped
		 WHERE model <> ?
		 GROUP BY model
		HAVING n >= ?
		 ORDER BY mean DESC`, exclude, MinTrajectories)
	if err != nil {
		return 0, "", false, err
	}
	defer rows.Close()

	var sum float64
	var total, models int
	for rows.Next() {
		var model string
		var mean float64
		var n int
		if err := rows.Scan(&model, &mean, &n); err != nil {
			return 0, "", false, err
		}
		sum += mean * float64(n)
		total += n
		models++
	}
	if err := rows.Err(); err != nil {
		return 0, "", false, err
	}
	if total == 0 {
		return 0, "", false, nil
	}
	return sum / float64(total), fmt.Sprintf("%d models, %d trajectories", models, total), true, nil
}

// Case is one golden-set selector: which agent, graded how, weighted what.
// It names no session — see the plan's Step A2. Trajectories are resolved
// against the local corpus for the subject model at run time.
type Case struct {
	ID     string `json:"id"`
	Agent  string `json:"agent"`
	Rubric string `json:"rubric"`
	Weight int    `json:"weight"`
}

// GoldenSet is the parsed manifest.
type GoldenSet struct {
	Version string `json:"golden_set_version"`
	Note    string `json:"note"`
	Cases   []Case `json:"cases"`
}

// LoadGoldenSet reads and validates a manifest.
func LoadGoldenSet(path string) (*GoldenSet, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var gs GoldenSet
	if err := json.Unmarshal(raw, &gs); err != nil {
		return nil, fmt.Errorf("golden set %s: %w", path, err)
	}
	if strings.TrimSpace(gs.Version) == "" {
		return nil, fmt.Errorf("golden set %s: golden_set_version is required — "+
			"a verdict without one silently outlives the set it was measured against", path)
	}
	if len(gs.Cases) == 0 {
		return nil, fmt.Errorf("golden set %s: no cases", path)
	}
	for i, c := range gs.Cases {
		if c.ID == "" || c.Agent == "" || strings.TrimSpace(c.Rubric) == "" {
			return nil, fmt.Errorf("golden set %s: case %d needs id, agent and rubric", path, i)
		}
		if c.Weight <= 0 {
			return nil, fmt.Errorf("golden set %s: case %s has weight %d, want >= 1", path, c.ID, c.Weight)
		}
	}
	return &gs, nil
}

// Result is one evaluation outcome, mirroring the model_validations row.
type Result struct {
	Model            string
	GoldenSetVersion string
	Verdict          string // pass | fail | inconclusive
	Score            float64
	Trajectories     int // judged trajectories on this subject model
	AgentsCovered    int // distinct golden-set agents with evidence
	Detail           string
}

// evidence is what the corpus can say about one subject model.
type evidence struct {
	mean    float64
	n       int
	byAgent map[string]bool
}

// gather joins judgments to the model the judged trajectory was actually
// RUNNING on, and reports which agents that evidence covers.
//
// trajectory_judgments.model is the JUDGE model, so it cannot answer this.
// The subject model comes from turns.model, matched at the same agent grain as
// the judgment: a judgment for 'main' is the orchestrator, whose turns carry
// agent_name IS NULL; a judgment for a named agent matches that agent's turns.
//
// Today the judging pipeline only ever produces agent='main' rows
// (trajectory_scores holds nothing else), so in practice this reads the
// orchestrator's trajectories. The agent-grain match is written properly
// anyway, so per-agent judging starts counting the day it lands, with no
// change here.
func gather(db *sql.DB, model string) (evidence, error) {
	ev := evidence{byAgent: map[string]bool{}}
	// Two things this query must NOT do, both found by auditing its own first
	// version against the live corpus:
	//
	//  1. Average AFTER the join. The join multiplies each judgment by the
	//     number of matching turns, so a plain AVG weights a judgment by how
	//     long its session was. Measured: one model read 1.62 turn-weighted and
	//     1.27 judgment-weighted. Hence the inner DISTINCT.
	//
	//  2. Attribute a mixed session to every model in it. 21% of judged
	//     sessions ran on more than one model; counting such a judgment once
	//     per model credits (or blames) each for work the others did. Hence the
	//     HAVING COUNT(DISTINCT model) = 1 — a judgment counts for a model only
	//     when that agent ran on exactly that one model for the whole session.
	rows, err := db.Query(`
		WITH scoped AS (
		  SELECT tj.id, tj.agent, tj.overall, MIN(`+modelBaseExpr+`) AS model
		    FROM trajectory_judgments tj
		    JOIN turns t
		      ON t.session_id = tj.session_id
		     AND ( (tj.agent = 'main' AND t.agent_name IS NULL)
		        OR  t.agent_name = tj.agent )
		   WHERE t.model IS NOT NULL AND t.model <> ''
		   GROUP BY tj.id, tj.agent, tj.overall
		  HAVING COUNT(DISTINCT `+modelBaseExpr+`) = 1
		)
		SELECT agent, AVG(overall) AS mean, COUNT(*) AS n
		  FROM scoped
		 WHERE model = ?
		 GROUP BY agent`, model)
	if err != nil {
		return ev, err
	}
	defer rows.Close()

	var sum float64
	for rows.Next() {
		var agent string
		var mean float64
		var n int
		if err := rows.Scan(&agent, &mean, &n); err != nil {
			return ev, err
		}
		ev.byAgent[agent] = true
		sum += mean * float64(n)
		ev.n += n
	}
	if err := rows.Err(); err != nil {
		return ev, err
	}
	if ev.n > 0 {
		ev.mean = sum / float64(ev.n)
	}
	return ev, nil
}

// Evaluate grades one subject model against the golden set. Pure over the DB:
// it reads judgments and writes nothing.
//
// The verdict rests on judged trajectories, because that is the grain the
// pipeline produces. The golden set decides how much of the intended surface
// that evidence covers, and thin coverage is reported rather than hidden — a
// pass drawn from one agent's runs should not look like a pass across the
// roster.
func Evaluate(db *sql.DB, gs *GoldenSet, model string) (Result, error) {
	// Normalised ONCE, at the entry point, so gather/baseline/Persist all speak
	// about the same id: a verdict recorded against `claude-opus-5-5[1m]` would
	// never be found by a gate asking about `claude-opus-5-5`, and the operator
	// would be blocked by a model they had just validated.
	model = modelid.Base(model)
	res := Result{Model: model, GoldenSetVersion: gs.Version}

	ev, err := gather(db, model)
	if err != nil {
		return res, err
	}
	res.Trajectories = ev.n
	res.Score = ev.mean

	wanted := map[string]bool{}
	for _, c := range gs.Cases {
		wanted[c.Agent] = true
	}
	var missing []string
	for a := range wanted {
		if ev.byAgent[a] {
			res.AgentsCovered++
		} else {
			missing = append(missing, a)
		}
	}
	sort.Strings(missing)

	base, baseModel, haveBase, err := baseline(db, model)
	if err != nil {
		return res, err
	}

	switch {
	case res.Trajectories < MinTrajectories:
		res.Verdict = "inconclusive"
		res.Detail = fmt.Sprintf(
			"only %d judged trajectories on %s (need >= %d). Run the fleet on this "+
				"model — the override exists for exactly this — then re-evaluate.",
			res.Trajectories, model, MinTrajectories)
	case !haveBase:
		// Nothing established to regress from. Saying "pass" would be inventing
		// a comparison that does not exist.
		res.Verdict = "inconclusive"
		res.Detail = fmt.Sprintf(
			"mean %.2f over %d trajectories, but no other model has >= %d judged "+
				"trajectories to compare against yet.", ev.mean, ev.n, MinTrajectories)
	case ev.mean >= base-RegressionMargin:
		res.Verdict = "pass"
		res.Detail = fmt.Sprintf("mean %.2f over %d trajectories, within %.2f of the "+
			"%.2f baseline (%s)", ev.mean, ev.n, RegressionMargin, base, baseModel)
	default:
		res.Verdict = "fail"
		res.Detail = fmt.Sprintf("mean %.2f over %d trajectories, %.2f below the "+
			"%.2f baseline (%s) — more than the %.2f margin",
			ev.mean, ev.n, base-ev.mean, base, baseModel, RegressionMargin)
	}

	res.Detail += fmt.Sprintf(" Golden-set agents with evidence: %d/%d.",
		res.AgentsCovered, len(wanted))
	if len(missing) > 0 && len(missing) <= 6 {
		res.Detail += " Uncovered: " + strings.Join(missing, ", ") + "."
	}
	return res, nil
}

// Persist upserts the result. One row per (model, golden_set_version), so a
// re-run converges instead of accumulating and "the newest verdict" stays
// unambiguous.
func Persist(db *sql.DB, res Result, now time.Time) error {
	_, err := db.Exec(`
		INSERT INTO model_validations
		    (model, golden_set_version, verdict, score, trajectories, agents_covered, detail, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(model, golden_set_version) DO UPDATE SET
		    verdict        = excluded.verdict,
		    score          = excluded.score,
		    trajectories   = excluded.trajectories,
		    agents_covered = excluded.agents_covered,
		    detail         = excluded.detail,
		    created_at     = excluded.created_at`,
		res.Model, res.GoldenSetVersion, res.Verdict, res.Score,
		res.Trajectories, res.AgentsCovered, res.Detail, now.UTC().Format(time.RFC3339))
	return err
}

// Newest returns the most recent verdict for a model, or ok=false when the
// model has never been evaluated — which the gate must treat as "unknown",
// not as "fine".
func Newest(db *sql.DB, model string) (Result, bool, error) {
	model = modelid.Base(model) // the gate may be asked about a 1M-window id
	var r Result
	err := db.QueryRow(`
		SELECT model, golden_set_version, verdict, COALESCE(score,0),
		       trajectories, agents_covered, COALESCE(detail,'')
		  FROM model_validations
		 WHERE model = ?
		 ORDER BY created_at DESC
		 LIMIT 1`, model).Scan(&r.Model, &r.GoldenSetVersion, &r.Verdict,
		&r.Score, &r.Trajectories, &r.AgentsCovered, &r.Detail)
	if err == sql.ErrNoRows {
		return r, false, nil
	}
	if err != nil {
		return r, false, err
	}
	return r, true, nil
}
