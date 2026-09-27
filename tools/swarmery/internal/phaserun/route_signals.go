package phaserun

import (
	"database/sql"
	"encoding/json"
	"errors"
	"log"
	"os"
	"strings"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/route"
)

// routeModeEnv is this surface's complexity-router knob: off | shadow | active,
// default shadow (route.ModeFromEnv).
const routeModeEnv = "SWARMERY_ROUTE_PHASERUN"

// routeHistoryWindow bounds the failure-history signal to the project's most
// recent measured phase runs.
const routeHistoryWindow = 30

// signalsForPhase extracts the router's signals for a phase run.
//
// The phase's own PRIOR forecast is the main source: its size band, its areas
// and its named risks. Only the latest prior that is not post-hoc counts — a
// "prediction" written after the Completion Report was filled is not one. No
// usable prior leaves ForecastSize "" and the counts at -1 (unknown, never
// zero). History is the project's last routeHistoryWindow phase_actuals rows
// with a decisive outcome; failed and partial both count as failures.
//
// Every read is best-effort: an error is logged and leaves that signal at its
// unknown value, because a routing record must never fail the run.
func signalsForPhase(db *sql.DB, phaseID int64) route.Signals {
	s := route.Signals{Surface: route.SurfacePhaseRun, FileScope: -1, Areas: -1}

	var depsJSON string
	var projectID int64
	err := db.QueryRow(`
		SELECT e.depends_on, t.project_id
		  FROM epic_phases e JOIN tasks t ON t.id = e.workspace_task_id
		 WHERE e.id = ?`, phaseID).Scan(&depsJSON, &projectID)
	if err != nil {
		log.Printf("warning: phaserun: route signals phase=%d: %v", phaseID, err)
		return s
	}
	var deps []int
	if json.Unmarshal([]byte(depsJSON), &deps) == nil {
		s.Deps = len(deps)
	}

	if err := applyPrior(db, phaseID, &s); err != nil {
		log.Printf("warning: phaserun: route forecast phase=%d: %v", phaseID, err)
	}

	fails, total, err := phaseHistory(db, projectID)
	if err != nil {
		log.Printf("warning: phaserun: route history phase=%d: %v", phaseID, err)
		return s
	}
	s.HistSamples = total
	if total > 0 {
		s.HistFailRate = float64(fails) / float64(total)
	}
	return s
}

// applyPrior copies the latest non-post-hoc prior forecast of the phase onto s.
// No such row leaves s untouched.
func applyPrior(db *sql.DB, phaseID int64, s *route.Signals) error {
	var size, areasJSON, filesJSON, risksJSON string
	err := db.QueryRow(`
		SELECT size_band, areas_json, files_json, risks_json
		  FROM phase_forecasts
		 WHERE phase_id = ? AND LOWER(TRIM(kind)) = 'prior' AND post_hoc = 0
		 ORDER BY id DESC LIMIT 1`, phaseID).Scan(&size, &areasJSON, &filesJSON, &risksJSON)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	s.ForecastSize = strings.TrimSpace(size)
	if n := jsonListLen(areasJSON); n > 0 {
		s.Areas = n
	}
	if n := jsonListLen(filesJSON); n > 0 {
		s.FileScope = n
	}
	var risks []string
	if json.Unmarshal([]byte(risksJSON), &risks) == nil {
		for _, r := range risks {
			if r = strings.TrimSpace(r); r != "" {
				s.RiskPaths = append(s.RiskPaths, r)
			}
		}
	}
	return nil
}

// jsonListLen counts the non-blank strings of a JSON array; 0 for anything else.
func jsonListLen(raw string) int {
	var xs []string
	if json.Unmarshal([]byte(raw), &xs) != nil {
		return 0
	}
	n := 0
	for _, x := range xs {
		if strings.TrimSpace(x) != "" {
			n++
		}
	}
	return n
}

// phaseHistory reads the outcomes of the project's last routeHistoryWindow
// measured phase runs. completed counts as a success; failed and partial as a
// failure; noop and anything else is not a verdict and is skipped.
func phaseHistory(db *sql.DB, projectID int64) (fails, total int, err error) {
	rows, err := db.Query(`
		SELECT pa.outcome
		  FROM phase_actuals pa
		  JOIN epic_phases e ON e.id = pa.phase_id
		  JOIN tasks t ON t.id = e.workspace_task_id
		 WHERE t.project_id = ? AND pa.outcome IN ('completed','failed','partial')
		 ORDER BY pa.id DESC
		 LIMIT ?`, projectID, routeHistoryWindow)
	if err != nil {
		return 0, 0, err
	}
	defer rows.Close()
	for rows.Next() {
		var outcome string
		if err := rows.Scan(&outcome); err != nil {
			return 0, 0, err
		}
		total++
		if outcome != "completed" {
			fails++
		}
	}
	return fails, total, rows.Err()
}

// modelRung names which rung of resolveModel's ladder produced the run's model.
// It mirrors resolveModel's order exactly and is only ever called after
// resolveModel succeeded, so a rung that would have errored cannot be named.
func modelRung(choice, docModel string) string {
	switch {
	case strings.TrimSpace(choice) != "":
		return route.RungRequest
	case strings.TrimSpace(docModel) != "":
		return route.RungDoc
	case strings.TrimSpace(os.Getenv(modelEnv)) != "":
		return route.RungEnv
	}
	return route.RungDefault
}

// recordRoute scores the phase run and writes one route_decisions row — in
// shadow, with applied=0 and used_* holding what resolveModel/resolveEffort put
// on the spawn. It never changes the spawn; every failure is logged and
// swallowed. The router's pick_model is recorded as the alias it chose and is
// deliberately NOT resolved through planning.ResolveModel here (that closed set
// has no haiku); mapping a pick onto a runnable ID belongs to active mode.
func (s *Service) recordRoute(phaseID int64, choice string, info phaseInfo, spec RunSpec) {
	mode := route.ModeFromEnv(routeModeEnv)
	switch mode {
	case route.ModeOff:
		return
	case route.ModeActive:
		log.Printf("warning: phaserun: %s=active is not implemented yet; recording in shadow", routeModeEnv)
		mode = route.ModeShadow
	}
	policy, err := route.LoadPolicy(os.Getenv(route.EnvPolicy))
	if err != nil {
		log.Printf("error: phaserun: route policy phase=%d: %v", phaseID, err)
		return
	}
	sig := signalsForPhase(s.DB, phaseID)
	if err := route.Record(s.DB, route.Row{
		Surface:     route.SurfacePhaseRun,
		Subject:     route.SubjectPhase(phaseID),
		SessionUUID: spec.SessionUUID,
		Mode:        mode,
		Signals:     sig,
		Decision:    route.Decide(sig, policy),
		UsedModel:   spec.Model,
		UsedEffort:  spec.Effort,
		WonRung:     modelRung(choice, info.DocModel),
		// CreatedAt left zero (⇒ wall clock): s.clock() is the run's measurement
		// clock, and an extra read here would shift run_ended_at under a stepping
		// test clock for a record that has nothing to do with the run's interval.
	}); err != nil {
		log.Printf("error: phaserun: phase=%d: %v", phaseID, err)
	}
}
