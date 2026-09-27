package phaserun

import (
	"database/sql"
	"encoding/json"
	"errors"
	"log"
	"os"
	"strings"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/claudeflags"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/route"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/wsingest"
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

// phaseRoute is the router's answer for one phase run, computed once at
// admission before the model and effort ladders are walked.
type phaseRoute struct {
	mode     route.Mode // off ⇒ nothing below is set
	ok       bool       // a decision exists: shadow|active and the policy loaded
	signals  route.Signals
	decision route.Decision
	// The route rungs, "" unless active: model is a FULL ID (route.ModelID), so
	// resolveModel never has to know that haiku is not in planning.Models.
	model  string
	effort string
}

// routePhase scores the phase and, in active mode, turns the decision into its
// route rungs. Off consults nothing. An unloadable policy is logged and routes
// nothing — the run keeps its pre-router ladders and no row is written. A pick
// that cannot run drops only its own rung, never the run.
func (s *Service) routePhase(phaseID int64) phaseRoute {
	rt := phaseRoute{mode: route.ModeFromEnv(routeModeEnv)}
	if rt.mode == route.ModeOff {
		return rt
	}
	policy, err := route.LoadPolicy(os.Getenv(route.EnvPolicy))
	if err != nil {
		log.Printf("error: phaserun: route policy phase=%d: %v", phaseID, err)
		return rt
	}
	rt.ok = true
	rt.signals = signalsForPhase(s.DB, phaseID)
	rt.decision = route.Decide(rt.signals, policy)
	if rt.mode != route.ModeActive {
		return rt
	}
	if id, err := route.ModelID(rt.decision.Model); err == nil {
		rt.model = id
	} else {
		log.Printf("warning: phaserun: phase=%d route model %q unusable, skipping the route model rung: %v", phaseID, rt.decision.Model, err)
	}
	if e, ok := claudeflags.NormalizeEffort(rt.decision.Effort); ok && e != "" {
		rt.effort = e
	} else {
		log.Printf("warning: phaserun: phase=%d route effort %q unusable, skipping the route effort rung", phaseID, rt.decision.Effort)
	}
	return rt
}

// modelRung names which rung of resolveModel's ladder produced the run's model.
// It mirrors resolveModel's order exactly and is only ever called after
// resolveModel succeeded, so a rung that would have errored cannot be named.
// routed is the route rung's value ("" unless active).
func modelRung(choice, docModel, routed string) string {
	switch {
	case strings.TrimSpace(choice) != "":
		return route.RungRequest
	case strings.TrimSpace(docModel) != "":
		return route.RungDoc
	case strings.TrimSpace(routed) != "":
		return route.RungRoute
	case strings.TrimSpace(os.Getenv(modelEnv)) != "":
		return route.RungEnv
	}
	return route.RungDefault
}

// effortRung names which rung of resolveEffort's ladder produced the run's
// effort, mirroring it exactly (a request or doc value that normalises to ""
// falls through there, so it falls through here). Only called after
// resolveEffort succeeded. Used for route_decisions.applied — won_rung is the
// model rung only.
func effortRung(choice, doc, routed string) string {
	if c, _ := claudeflags.NormalizeEffort(choice); c != "" {
		return route.RungRequest
	}
	if c, _ := claudeflags.NormalizeEffort(wsingest.ParseEffort(doc)); c != "" {
		return route.RungDoc
	}
	if c, ok := claudeflags.NormalizeEffort(routed); ok && c != "" {
		return route.RungRoute
	}
	return route.RungEnv // env or default: claudeflags does not say which
}

// recordRoute writes the phase run's route_decisions row: the decision beside
// what resolveModel/resolveEffort put on the spawn. In shadow the row says
// applied=0 and used_* are the pre-router picks; in active, applied says
// whether the router's model or effort won its ladder and won_rung names the
// model rung (route when the router's model ran). It never changes the spawn;
// every failure is logged and swallowed. pick_model is recorded as the alias
// the router chose; used_model is the full ID that ran.
func (s *Service) recordRoute(phaseID int64, choice, effortChoice, doc string, info phaseInfo, spec RunSpec, rt phaseRoute) {
	if !rt.ok {
		return
	}
	rung := modelRung(choice, info.DocModel, rt.model)
	applied := rt.mode == route.ModeActive &&
		(rung == route.RungRoute || effortRung(effortChoice, doc, rt.effort) == route.RungRoute)
	if err := route.Record(s.DB, route.Row{
		Surface:     route.SurfacePhaseRun,
		Subject:     route.SubjectPhase(phaseID),
		SessionUUID: spec.SessionUUID,
		Mode:        rt.mode,
		Signals:     rt.signals,
		Decision:    rt.decision,
		Applied:     applied,
		UsedModel:   spec.Model,
		UsedEffort:  spec.Effort,
		WonRung:     rung,
		// CreatedAt left zero (⇒ wall clock): s.clock() is the run's measurement
		// clock, and an extra read here would shift run_ended_at under a stepping
		// test clock for a record that has nothing to do with the run's interval.
	}); err != nil {
		log.Printf("error: phaserun: phase=%d: %v", phaseID, err)
	}
}
