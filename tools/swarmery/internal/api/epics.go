// Epic rollup + doc-editor API (fusion phase 10 — DESIGN.md §2 items 9–10):
//
//	GET   /api/epics?projectId=                                  → epics + phases + rollup
//	GET   /api/epics/{taskId}/docs?path=                         → read a plan doc
//	PUT   /api/epics/{taskId}/docs?path=                         → write a plan doc (backup)
//	PATCH /api/epics/{taskId}/docs?path=  {line, done}           → flip one checkbox by line index
//
// An "epic" is a workspace task (source='workspace') whose plan/ dir the
// wsingest scanner parsed into epic_phases. Reads are self-wiring over h.DB
// (like presets.go / project_meta.go). The doc endpoints turn the workspace
// folder into invisible infrastructure — plans are read and edited from the
// platform; the confinement fence keeps every path strictly under that task's
// plan/ dir (EvalSymlinks + prefix check), and writes take a timestamped backup
// first (mirroring the System write surface). All writes carry the same
// requireLocalOrigin D4 hardening as every other mutating endpoint.
// NOTE: the POST /activate route was removed in interactive-planning-v2 phase 4
// (Board is exclusively for tasks created on the board; plan phases run directly
// via phase 5's run mechanism). Historical activated_board_task_id links are
// preserved in the DB and still served read-only in the epic list DTO.

package api

import (
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/modelid"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/phasediag"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/phasegate"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/phaserun"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/planrun"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/runcore"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/wsingest"
)

// ── DTOs ────────────────────────────────────────────────────────────────────

// epicPhaseDTO is one phase row (camelCase, mirrored in web/src/api/types.ts).
type epicPhaseDTO struct {
	ID              int64  `json:"id"`
	Seq             int    `json:"seq"`
	Name            string `json:"name"`
	DocPath         string `json:"docPath"`
	DocRelPath      string `json:"docRelPath"` // path relative to plan/ — the ?path= value
	DependsOn       []int  `json:"dependsOn"`
	CheckboxesDone  int    `json:"checkboxesDone"`
	CheckboxesTotal int    `json:"checkboxesTotal"`
	// Normalized `Status:` header marker from the phase doc itself
	// (pending|in_progress|done); null when the doc carries none. Lets an
	// executor flag "working on this now" before the first checkbox tick.
	DocStatus *string `json:"docStatus"`
	// RFC3339 mtime of the phase doc at scan time — a liveness signal (every
	// executor edit changes the doc and re-triggers the scan).
	DocUpdatedAt *string `json:"docUpdatedAt"`
	// The doc's `## Completion Report` section (markdown, verbatim) — what the
	// executor shipped. Null until the section is written; the UI offers a
	// summary modal on done phases when present.
	CompletionReport *string `json:"completionReport"`
	ActivatedAt      *string `json:"activatedAt"`
	// The external_id of the board task an activation minted (null until activated).
	BoardTaskExternalID *string `json:"boardTaskExternalId"`
	BoardTaskID         *int64  `json:"boardTaskId"`
	BoardColumn         *string `json:"boardColumn"`
	// Phase-run state (interactive planning v2 phase 5, migration 0034):
	// idle | running | done | failed, plus the run's session uuid / start /
	// error. Consumed by the Plans page's Run/Cancel UI (phase 6).
	RunState       string  `json:"runState"`
	RunSessionUUID *string `json:"runSessionUuid"`
	// The model the linked run session ACTUALLY used (sessions.model), read
	// through run_session_uuid rather than copied onto the phase row — decision
	// D3 of the phase-model-selection plan: one owner for the fact, so there is
	// no second copy to drift. Null when the phase has never run, when the run's
	// session has not been ingested yet, or when that session carries no model.
	RunModel *string `json:"runModel"`
	// RunModels is every model the run's session actually ran an assistant turn
	// on, in the order they first appeared, with that model's turn count.
	//
	// RunModel above (sessions.model) is the FIRST of them, and for most runs it
	// is the only one — the list is then a single entry and the UI shows what it
	// always did. It stops being the only one exactly when it matters: an Opus
	// 5.5 safeguard refusal moves the session onto an older model and the run
	// carries on there, so "the model this run used" was a true statement about
	// its first turn and a false one about its output. Empty when the phase has
	// never run or its session is not ingested.
	RunModels []phaseModelUseDTO `json:"runModels"`
	// RunModelFellBack is true when the LAST of those models is a weaker family
	// or an older generation than the first — i.e. the list is not just a
	// rename or a context-window marker. This, not len(RunModels) > 1, is what
	// the "fell back to X" chip renders on.
	RunModelFellBack bool `json:"runModelFellBack"`
	// The model the phase DOC asks for (`**Model:** opus`, epic_phases.doc_model,
	// migration 0069) — rung 2 of the ladder. Null when the doc declares nothing.
	//
	// A DIFFERENT FACT from RunModel, which is why it is a separate field and not a
	// fallback inside it: DocModel is what the plan ASKS FOR and applies to the next
	// run; RunModel is what a past run actually USED. They disagree legitimately —
	// after the author edits the line, or when the operator overrode the doc from the
	// picker — and folding them into one field would make the UI unable to say which
	// of the two it is showing.
	//
	// VERBATIM, including a value this daemon does not know: the run refuses such a
	// phase (409 doc-model-unknown) and the operator has to SEE the offending text to
	// fix it.
	DocModel     *string `json:"docModel"`
	RunStartedAt *string `json:"runStartedAt"`
	RunError     *string `json:"runError"`
	// Derived: what the run ACHIEVED, as opposed to how the process ended.
	// completed | partial | noop | failed | running | idle — see
	// internal/phasediag.OutcomeFromRow, the single row-aware implementation, so
	// the list chip and the diagnosis modal can never disagree.
	RunOutcome string `json:"runOutcome"`
	// End of the last run (null while running / never run) and the ticked-criteria
	// count snapshotted at its start (null for rows predating migration 0041 —
	// UNMEASURED, not zero).
	RunEndedAt          *string `json:"runEndedAt"`
	RunCheckboxesBefore *int    `json:"runCheckboxesBefore"`
	// Post-run verification (migration 0057): the doc's opt-in and the verdict it
	// produced. VerifyMode is off|normal|strict — `off`, the default, is what makes
	// verification invisible to every plan that never asked for it. VerifyVerdict is
	// pass|fail|inconclusive, null until a run has been graded.
	//
	// Beside RunOutcome, NOT inside it: the outcome is checkbox-derived and stays that
	// way (decision D5), and a `fail` also surfaces as a verify-failed blocker in the
	// diagnosis. The chip the UI renders from these fields reports the grade; the
	// outcome chip next to it reports whether work landed. Two different questions.
	VerifyMode    string  `json:"verifyMode"`
	VerifyVerdict *string `json:"verifyVerdict"`
	VerifyDetail  *string `json:"verifyDetail"`
	// RunEvents is this run's completion-loop timeline (migration 0077): the
	// continuations the harness sent, and the decision it settled on.
	//
	// It exists because RunState collapses a decision CHAIN into one word. A phase
	// that reads `partial` may have been nudged twice and made progress each time,
	// or stalled on the first turn and repeated itself — the operator cannot tell,
	// and the difference is exactly what says whether the phase doc or the executor
	// is at fault. Empty (and omitted from the UI) for the overwhelmingly common
	// case of a run that finished on its first turn.
	RunEvents []runcore.RunEvent `json:"runEvents"`
	// THE completion gate's answer: complete | unverified | incomplete
	// (internal/phasegate). Distinct from RunOutcome, which reports whether work
	// landed, and from VerifyVerdict, which reports the grade: this reports whether
	// the phase may be called DONE. `unverified` is the state that did not exist
	// before — criteria all ticked, and the grade the doc asked for never arrived.
	//
	// Every surface reads this instead of re-deriving `done === total`, so the list
	// chip, the diagnosis modal, the dependency gate and the client cannot disagree
	// about the same row.
	CompletionState string `json:"completionState"`
	// Why the gate refused, in the operator's words. Empty when complete. A LIST
	// because one gate cites every reason it has, rather than several gates each
	// refusing for its own.
	CompletionBlockers []string `json:"completionBlockers"`
	// Forecasts are the doc's `## Forecast` blocks (migration 0079): the PRIOR the
	// planner wrote and/or the POSTERIOR the executor wrote, in document order.
	// Empty for every phase that declares none, which is all of them until an
	// author opts in.
	//
	// READ-ONLY DATA, deliberately absent from every field above it: nothing in
	// CompletionState, CompletionBlockers or RunOutcome consults a forecast, and
	// nothing may start to. A forecast is a prediction to be scored later, not a
	// contract a phase can violate.
	Forecasts []phaseForecastDTO `json:"forecasts"`
	// ForecastLints is what is wrong with those blocks — an unknown band, a
	// confidence outside 0..1, a forecast naming no areas, a posterior with no
	// prior to score against.
	//
	// Computed in the READ path from the stored rows, exactly like the spec
	// rollup's `unknownRefs` beside it, and for the same reason: a lint is a thing
	// to show the operator, never a thing that can refuse an ingest or a run. A
	// plan whose every forecast fails every rule still indexes and still runs.
	ForecastLints []wsingest.ForecastLint `json:"forecastLints"`
}

// phaseForecastDTO is one stored `## Forecast` block. Every text field is
// VERBATIM — the value the author wrote, not a normalized band — because the
// operator cannot fix a typo the API has already hidden (the same decision
// epic_phases.doc_model carries).
type phaseForecastDTO struct {
	Kind         string   `json:"kind"`      // prior | posterior; "" when the block declares none
	WrittenAt    string   `json:"writtenAt"` // RFC3339 as written; "" when absent
	Areas        []string `json:"areas"`
	Files        []string `json:"files"`
	SizeBand     string   `json:"sizeBand"`
	DurationBand string   `json:"durationBand"`
	Outcome      string   `json:"outcome"`
	Risks        []string `json:"risks"`
	// null, not 0, when the author said nothing readable — see migration 0079.
	Confidence *float64 `json:"confidence"`
	// True when this forecast cannot have been a prediction, so calibration must
	// not score it. PostHocReason says which observation decided that:
	// "report-filled" (a prior in a doc whose `## Completion Report` was already
	// filled) or "after-first-edit" (a posterior the run's transcript shows was
	// written after the run's first change to another file). "" when not post hoc.
	PostHoc       bool   `json:"postHoc"`
	PostHocReason string `json:"postHocReason"`
	DocHash       string `json:"docHash"`
}

// epicRollupDTO is a checkbox rollup across all of an epic's phases.
type epicRollupDTO struct {
	Done  int     `json:"done"`
	Total int     `json:"total"`
	Pct   float64 `json:"pct"` // 0..100, 0 when total==0 (no divide-by-zero)
	// How many of the plan's phases the completion gate refuses (phasegate.Check).
	// Feeds planStatus: a plan is "done" only when none of its phases is refused,
	// so a fully-ticked plan whose grades never landed stays `active`.
	IncompletePhases int `json:"incompletePhases"`
}

// specCriterionDTO is one SC-tagged acceptance criterion from plan/spec.md,
// with the phases that declare they deliver it.
type specCriterionDTO struct {
	Cid       string `json:"cid"`
	Text      string `json:"text"`
	Done      bool   `json:"done"`
	CoveredBy []int  `json:"coveredBy"` // phase seqs declaring this cid; empty = uncovered
}

// specUnknownRefDTO is a phase Covers reference to an id the spec never
// declared — a speculation signal.
type specUnknownRefDTO struct {
	Seq int    `json:"seq"`
	Cid string `json:"cid"`
}

// epicSpecDTO is the per-epic spec-coverage rollup (criteria × phase Covers).
type epicSpecDTO struct {
	Criteria    []specCriterionDTO  `json:"criteria"`
	Covered     int                 `json:"covered"`
	Total       int                 `json:"total"`
	UnknownRefs []specUnknownRefDTO `json:"unknownRefs"`
}

// epicDTO is one epic (workspace task) with its phases and rollup.
type epicDTO struct {
	TaskID      int64   `json:"taskId"`
	ExternalID  string  `json:"externalId"`
	ProjectID   int64   `json:"projectId"`
	ProjectSlug string  `json:"projectSlug"`
	Title       string  `json:"title"`
	Status      string  `json:"status"` // active | paused | done | archived (planStatus)
	StartedAt   *string `json:"startedAt"`
	PlanDir     string  `json:"planDir"`
	// True when plan/SUMMARY.md exists — the plan-level completion summary the
	// executor writes when the whole plan lands. The UI opens it (via the docs
	// endpoint, path=SUMMARY.md) in a summary modal on done plans.
	HasSummary bool `json:"hasSummary"`
	// True when plan/spec.md exists — the business-level spec the plan derives
	// from. Independent of Spec below: the file may exist before the scanner has
	// parsed criteria rows out of it.
	HasSpec bool `json:"hasSpec"`
	// Spec is the per-epic spec coverage (criteria + coveredBy phase seqs +
	// covered/total + unknown refs); null when the task has no spec_criteria rows.
	Spec   *epicSpecDTO   `json:"spec"`
	Phases []epicPhaseDTO `json:"phases"`
	Rollup epicRollupDTO  `json:"rollup"`
	// Whole-plan run state (migration 0040), null until the plan has ever been
	// run. Distinct from the per-phase RunState above: this is ONE agent handed
	// the whole plan, so it never stamps individual phases.
	PlanRun *planRunDTO `json:"planRun"`
	// LinkedSessions is every session task_sessions attaches to this plan — the
	// daemon's own runs (explicit) AND the interactive sessions inferred from the
	// files they edited (heuristic). Never null; an empty list means nothing has
	// worked on this plan yet, which is a different statement from "we don't know",
	// and until phase 3 the panel could only make the second one.
	LinkedSessions []linkedSessionDTO `json:"linkedSessions"`
	// CardExternalID is set when this plan is a MICRO-PLAN: the board card that
	// materialized it at dispatch (phase 4). The two are one unit of work seen from
	// two pages, and the chip is what makes that navigable instead of a coincidence
	// of names.
	CardExternalID *string `json:"cardExternalId"`
}

// linkedSessionDTO is one task_sessions row joined to its session, as the Plans
// page's sessions panel renders it.
//
// LinkSource is the DB's own two-valued vocabulary; the UI shows a third badge
// ("run") for the sessions a phase or plan run stamped on its own row, which it
// can tell apart without another column because those uuids are already in the
// phase/planRun DTOs.
type linkedSessionDTO struct {
	SessionUUID string   `json:"sessionUuid"`
	LinkSource  string   `json:"linkSource"` // explicit | heuristic
	Confidence  *float64 `json:"confidence"` // null for links written without one
	CostUSD     *float64 `json:"costUsd"`    // null while no turn of the session is priced
	StartedAt   string   `json:"startedAt"`
	EndedAt     *string  `json:"endedAt"`
}

// planRunDTO is the plan_runs row for one epic.
type planRunDTO struct {
	Agent    *string `json:"agent"`
	Mode     string  `json:"mode"` // auto | subagents | inline
	// RunState: idle | running | done | failed | blocked | partial. The last two
	// arrived with the completion loop (phase 3) — they are both CLEAN exits, and
	// which one it was is decided from the plan's ticked criteria and the run's
	// final assistant text, never from the exit code.
	RunState       string  `json:"runState"`
	RunSessionUUID *string `json:"runSessionUuid"`
	RunStartedAt   *string `json:"runStartedAt"`
	RunError       *string `json:"runError"`
	// RunEvents is this run's completion-loop timeline — see epicPhaseDTO.RunEvents.
	RunEvents []runcore.RunEvent `json:"runEvents"`
}

// wsPlanPayload is the plan_updated WS payload (frozen once shipped) — a thin
// cache-invalidation hint, not data: clients refetch GET /api/epics.
type wsPlanPayload struct {
	TaskID    int64 `json:"taskId"`
	ProjectID int64 `json:"projectId"`
}

// planUpdatedPayload resolves the plan_updated payload for one workspace task
// (the same task→project resolution listEpics performs). nil when the row is
// gone — the WS layer then skips the frame.
func (h *Handler) planUpdatedPayload(taskID int64) (*wsPlanPayload, error) {
	var p wsPlanPayload
	err := h.DB.QueryRow(
		`SELECT id, project_id FROM tasks WHERE id = ? AND source = 'workspace'`,
		taskID).Scan(&p.TaskID, &p.ProjectID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &p, nil
}

// planStatus derives the plan lifecycle state. Precedence: zone > README >
// rollup — an archived task is "archived" whatever its README says, a paused
// README beats a complete rollup, and a full rollup reads "done" even before
// the plan is archived. epicDTO.Status is always one of these four values.
//
// allPhasesComplete is the completion gate's verdict across the plan's phases
// (phasegate.Check per phase). A plan whose checkboxes are all ticked but whose
// phases could not be verified reads `active`, not `done` — the four-value
// contract is unchanged, and "done" now means what it says. The per-phase
// `completionState` carries WHY for display; widening this enum instead would
// have forced every consumer of a plan's status to learn a fifth value to say
// something the phase rows already say.
func planStatus(archived bool, taskStatus string, done, total int, allPhasesComplete bool) string {
	switch {
	case archived:
		return "archived"
	case taskStatus == "paused":
		return "paused"
	case phasediag.CriteriaMet(done, total) && allPhasesComplete:
		return "done"
	default:
		return "active"
	}
}

// ── GET /api/epics ──────────────────────────────────────────────────────────

// listEpics returns every workspace task that has ≥1 epic_phase, optionally
// scoped by projectId, newest first, each with its phases and checkbox rollup.
func (h *Handler) listEpics(w http.ResponseWriter, r *http.Request) {
	q := `
		SELECT t.id, COALESCE(t.external_id,''), t.project_id, p.slug, t.title,
		       t.status, t.archived_at IS NOT NULL, t.started_at,
		       (SELECT path FROM task_artifacts WHERE task_id = t.id AND kind = 'plan')
		FROM tasks t
		JOIN projects p ON p.id = t.project_id
		WHERE t.source = 'workspace'
		  AND EXISTS (SELECT 1 FROM epic_phases e WHERE e.workspace_task_id = t.id)`
	var args []any
	if pid := r.URL.Query().Get("projectId"); pid != "" {
		q += ` AND t.project_id = ?`
		args = append(args, pid)
	}
	q += ` ORDER BY t.started_at DESC, t.id DESC`

	rows, err := h.DB.Query(q, args...)
	if err != nil {
		writeErr(w, err)
		return
	}
	// Materialize the epic rows FIRST, then close the cursor — the SQLite pool
	// is single-connection, so hydrating phases (a nested Query) while this
	// cursor is open would deadlock. Second pass runs the per-epic queries.
	out := []epicDTO{}
	archived := []bool{} // parallel to out — feeds the planStatus derivation
	for rows.Next() {
		var e epicDTO
		var arch bool
		var planDir sql.NullString
		if err := rows.Scan(&e.TaskID, &e.ExternalID, &e.ProjectID, &e.ProjectSlug,
			&e.Title, &e.Status, &arch, &e.StartedAt, &planDir); err != nil {
			rows.Close()
			writeErr(w, err)
			return
		}
		e.PlanDir = planDir.String
		out = append(out, e)
		archived = append(archived, arch)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		writeErr(w, err)
		return
	}

	// One query for every plan's run state, overlaid below — not a per-epic
	// lookup inside the loop.
	planRuns, err := h.planRunsByTask()
	if err != nil {
		writeErr(w, err)
		return
	}
	// Same posture for spec criteria: one query keyed by task, materialized
	// before the per-epic pass (single-connection pool — see the comment above).
	specCriteria, err := h.specCriteriaByTask()
	if err != nil {
		writeErr(w, err)
		return
	}
	// And for the linked sessions. Same reason again: one query, materialized
	// before the loop, never a per-epic lookup inside it.
	linkedSessions, err := h.linkedSessionsByTask()
	if err != nil {
		writeErr(w, err)
		return
	}
	// Which of these plans are micro-plans, and of which card. Same posture.
	microPlanCards, err := h.microPlanCardsByTask()
	if err != nil {
		writeErr(w, err)
		return
	}

	for i := range out {
		out[i].PlanRun = planRuns[out[i].TaskID]
		phases, rollup, covers, err := h.epicPhases(out[i].TaskID, out[i].PlanDir)
		if err != nil {
			writeErr(w, err)
			return
		}
		out[i].Phases = phases
		out[i].Rollup = rollup
		out[i].Spec = buildEpicSpec(specCriteria[out[i].TaskID], covers)
		if card, ok := microPlanCards[out[i].TaskID]; ok {
			out[i].CardExternalID = &card
		}
		out[i].LinkedSessions = linkedSessions[out[i].TaskID]
		if out[i].LinkedSessions == nil {
			out[i].LinkedSessions = []linkedSessionDTO{} // [] not null: the UI maps over it
		}
		if out[i].PlanDir != "" {
			if fi, err := os.Stat(filepath.Join(out[i].PlanDir, "SUMMARY.md")); err == nil && !fi.IsDir() {
				out[i].HasSummary = true
			}
			if fi, err := os.Stat(filepath.Join(out[i].PlanDir, "spec.md")); err == nil && !fi.IsDir() {
				out[i].HasSpec = true
			}
		}
		// Normalize the raw tasks.status (running|paused|done) into the plan
		// lifecycle contract: active | paused | done | archived.
		out[i].Status = planStatus(archived[i], out[i].Status, rollup.Done, rollup.Total,
			rollup.IncompletePhases == 0)
	}
	writeJSON(w, out, nil)
}

// microPlanCardsByTask maps a workspace task id to the external id of the board
// card whose dispatch materialized it — empty for every hand-written plan.
//
// Joined through tasks.workspace_dir rather than a foreign key because that column
// holds the dir, which is the durable identity: a workspace task row is re-derived
// from the tree, so an id cached at mint time would go stale on the first rename.
func (h *Handler) microPlanCardsByTask() (map[int64]string, error) {
	rows, err := h.DB.Query(`
		SELECT ta.task_id, c.external_id
		  FROM tasks c
		  JOIN task_artifacts ta ON ta.kind = 'plan' AND ta.path = c.workspace_dir || '/plan'
		 WHERE c.source = 'queue' AND c.workspace_dir IS NOT NULL AND c.workspace_dir <> ''
		   AND c.external_id IS NOT NULL AND c.external_id <> ''`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int64]string{}
	for rows.Next() {
		var (
			taskID int64
			extID  string
		)
		if err := rows.Scan(&taskID, &extID); err != nil {
			return nil, err
		}
		out[taskID] = extID
	}
	return out, rows.Err()
}

// linkedSessionsByTask loads every workspace task's linked sessions, keyed by task
// id, newest first.
//
// Restricted to source='workspace' rows so a board card's dispatch links never
// arrive here — the board has its own surface for those, and the join is otherwise
// over every link in the database.
//
// Cost comes from the session's turns (SUM(cost_usd), null while nothing is priced),
// the same expression the sessions list uses, so a session's cost reads identically
// in both places.
func (h *Handler) linkedSessionsByTask() (map[int64][]linkedSessionDTO, error) {
	rows, err := h.DB.Query(`
		SELECT ts.task_id, se.session_uuid, ts.link_source, ts.confidence,
		       (SELECT SUM(t.cost_usd) FROM turns t WHERE t.session_id = se.id),
		       se.started_at, se.ended_at
		  FROM task_sessions ts
		  JOIN sessions se ON se.id = ts.session_id
		  JOIN tasks tk ON tk.id = ts.task_id
		 WHERE tk.source = 'workspace'
		 ORDER BY ts.task_id, se.started_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int64][]linkedSessionDTO{}
	for rows.Next() {
		var (
			taskID int64
			d      linkedSessionDTO
			conf   sql.NullFloat64
			cost   sql.NullFloat64
			ended  sql.NullString
		)
		if err := rows.Scan(&taskID, &d.SessionUUID, &d.LinkSource, &conf, &cost,
			&d.StartedAt, &ended); err != nil {
			return nil, err
		}
		if conf.Valid {
			d.Confidence = &conf.Float64
		}
		if cost.Valid {
			d.CostUSD = &cost.Float64
		}
		if ended.Valid {
			d.EndedAt = &ended.String
		}
		out[taskID] = append(out[taskID], d)
	}
	return out, rows.Err()
}

// planRunsByTask loads every plan_runs row keyed by workspace task id. Plan
// runs are rare (one row per plan that has ever been run), so the whole table is
// cheaper to fetch once than to look up per epic.
func (h *Handler) planRunsByTask() (map[int64]*planRunDTO, error) {
	rows, err := h.DB.Query(`
		SELECT workspace_task_id, agent, mode, run_state, run_session_uuid, run_started_at, run_error
		  FROM plan_runs`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int64]*planRunDTO{}
	for rows.Next() {
		var (
			taskID                            int64
			dto                               planRunDTO
			agent, uuid, startedAt, runErrStr sql.NullString
		)
		if err := rows.Scan(&taskID, &agent, &dto.Mode, &dto.RunState, &uuid, &startedAt, &runErrStr); err != nil {
			return nil, err
		}
		if agent.Valid {
			dto.Agent = &agent.String
		}
		if uuid.Valid {
			dto.RunSessionUUID = &uuid.String
		}
		if startedAt.Valid {
			dto.RunStartedAt = &startedAt.String
		}
		if runErrStr.Valid {
			dto.RunError = &runErrStr.String
		}
		out[taskID] = &dto
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	// After the cursor is closed, never inside the loop — see loadPhases for the
	// single-connection deadlock this ordering avoids.
	rows.Close()
	for taskID, dto := range out {
		dto.RunEvents = runcore.RunEvents(h.DB, planrun.Engine, taskID)
	}
	return out, nil
}

// specCriteriaByTask loads every spec_criteria row keyed by workspace task id,
// in spec.md order. Plans with a spec are rare enough that one whole-table
// fetch is cheaper (and pool-safer) than a per-epic lookup.
func (h *Handler) specCriteriaByTask() (map[int64][]specCriterionDTO, error) {
	rows, err := h.DB.Query(`
		SELECT workspace_task_id, cid, text, done
		  FROM spec_criteria
		 ORDER BY workspace_task_id, pos`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int64][]specCriterionDTO{}
	for rows.Next() {
		var (
			taskID int64
			c      specCriterionDTO
		)
		if err := rows.Scan(&taskID, &c.Cid, &c.Text, &c.Done); err != nil {
			return nil, err
		}
		c.CoveredBy = []int{} // empty array, not null — "uncovered" is a value
		out[taskID] = append(out[taskID], c)
	}
	return out, rows.Err()
}

// phaseCovers pairs a phase's seq with the spec-criterion ids its doc declares
// (epic_phases.covers, parsed by wsingest).
type phaseCovers struct {
	seq  int
	cids []string
}

// buildEpicSpec cross-joins a task's spec criteria with its phases' Covers
// declarations: CoveredBy = seqs of phases declaring the cid, UnknownRefs =
// (seq, cid) pairs where a phase covers an id the spec never declared. nil when
// the task has zero criteria rows — the DTO's `spec` is null for spec-less plans.
func buildEpicSpec(criteria []specCriterionDTO, covers []phaseCovers) *epicSpecDTO {
	if len(criteria) == 0 {
		return nil
	}
	byCid := make(map[string]int, len(criteria))
	for i, c := range criteria {
		byCid[c.Cid] = i
	}
	spec := &epicSpecDTO{
		Criteria:    criteria,
		Total:       len(criteria),
		UnknownRefs: []specUnknownRefDTO{},
	}
	seenRef := map[string]bool{} // dedupes both coveredBy seqs and unknown pairs
	for _, pc := range covers {
		for _, cid := range pc.cids {
			key := strconv.Itoa(pc.seq) + " " + cid
			if seenRef[key] {
				continue
			}
			seenRef[key] = true
			if i, ok := byCid[cid]; ok {
				spec.Criteria[i].CoveredBy = append(spec.Criteria[i].CoveredBy, pc.seq)
			} else {
				spec.UnknownRefs = append(spec.UnknownRefs, specUnknownRefDTO{Seq: pc.seq, Cid: cid})
			}
		}
	}
	for _, c := range spec.Criteria {
		if len(c.CoveredBy) > 0 {
			spec.Covered++
		}
	}
	return spec
}

// epicPhases loads one epic's phases (joined to the board task an activation
// minted) plus the checkbox rollup and each phase's Covers declaration. planDir
// is used to compute each phase's path relative to plan/ (the ?path= the doc
// endpoints accept).
func (h *Handler) epicPhases(taskID int64, planDir string) ([]epicPhaseDTO, epicRollupDTO, []phaseCovers, error) {
	// Whether the closure condition applies at all is a process-level fact,
	// resolved once. (It used to also resolve a per-plan lesson here; that
	// condition was removed from the gate — see phasegate.Check.)
	closureRequired := phasegate.ClosureGateEnabled()

	rows, err := h.DB.Query(`
		SELECT e.id, e.seq, e.name, e.doc_path, e.depends_on, e.covers,
		       e.checkboxes_total, e.checkboxes_done, e.doc_status, e.doc_updated_at,
		       e.completion_report, e.activated_at, e.activated_board_task_id,
		       bt.external_id, bt.board_column,
		       e.run_state, e.run_session_uuid, e.run_started_at, e.run_error,
		       e.run_ended_at, e.run_checkboxes_before, e.run_checkboxes_after,
		       e.verify_mode, e.verify_verdict, e.verify_detail,
		       e.doc_model,
		       se.model
		FROM epic_phases e
		LEFT JOIN tasks bt ON bt.id = e.activated_board_task_id
		-- The run's own record of which model it used. LEFT, so a phase that never
		-- ran (or whose session has not been ingested) still comes back; sessions
		-- .session_uuid is UNIQUE, so this can never fan a phase row out.
		LEFT JOIN sessions se ON se.session_uuid = e.run_session_uuid
		WHERE e.workspace_task_id = ?
		ORDER BY e.seq, e.id`, taskID)
	if err != nil {
		return nil, epicRollupDTO{}, nil, err
	}
	defer rows.Close()

	phases := []epicPhaseDTO{}
	covers := []phaseCovers{}
	var rollup epicRollupDTO
	for rows.Next() {
		var (
			p            epicPhaseDTO
			depsJSON     string
			coversJSON   string
			docStatus    sql.NullString
			docUpdatedAt sql.NullString
			completion   sql.NullString
			boardTaskID  sql.NullInt64
			boardExtID   sql.NullString
			boardCol     sql.NullString
			runUUID      sql.NullString
			runStartedAt sql.NullString
			runError     sql.NullString
			runEndedAt   sql.NullString
			// The run's measurement interval (migrations 0041/0042). Both stay
			// sql.NullInt64 all the way into OutcomeFromRow — it is the NULLness
			// itself that carries "unmeasured".
			runCheckboxesBefore sql.NullInt64
			runCheckboxesAfter  sql.NullInt64
			// The verdict columns (0057). NULL until the phase has been graded, which
			// is the normal state: verification is opt-in per doc.
			verifyVerdict sql.NullString
			verifyDetail  sql.NullString
			// sessions.model for the linked run. NULL both when no session is
			// joined and when that session has no model recorded yet.
			runModel sql.NullString
			// epic_phases.doc_model (0069) — the doc's own declaration. NULL for
			// every phase whose doc carries no `**Model:**` header, which is all of
			// them until an author opts in.
			docModel sql.NullString
		)
		if err := rows.Scan(&p.ID, &p.Seq, &p.Name, &p.DocPath, &depsJSON, &coversJSON,
			&p.CheckboxesTotal, &p.CheckboxesDone, &docStatus, &docUpdatedAt,
			&completion, &p.ActivatedAt, &boardTaskID, &boardExtID, &boardCol,
			&p.RunState, &runUUID, &runStartedAt, &runError,
			&runEndedAt, &runCheckboxesBefore, &runCheckboxesAfter,
			&p.VerifyMode, &verifyVerdict, &verifyDetail, &docModel, &runModel); err != nil {
			return nil, epicRollupDTO{}, nil, err
		}
		p.DependsOn = decodeIntList(depsJSON)
		covers = append(covers, phaseCovers{seq: p.Seq, cids: decodeStrList(coversJSON)})
		p.DocRelPath = relToPlan(planDir, p.DocPath)
		if docStatus.Valid {
			p.DocStatus = &docStatus.String
		}
		if docUpdatedAt.Valid {
			p.DocUpdatedAt = &docUpdatedAt.String
		}
		if completion.Valid {
			p.CompletionReport = &completion.String
		}
		if boardTaskID.Valid {
			p.BoardTaskID = &boardTaskID.Int64
		}
		if boardExtID.Valid {
			p.BoardTaskExternalID = &boardExtID.String
		}
		if boardCol.Valid {
			p.BoardColumn = &boardCol.String
		}
		if runUUID.Valid {
			p.RunSessionUUID = &runUUID.String
		}
		// An empty sessions.model is "not recorded", not a model name — it must
		// read as null rather than as a chip with no text in it.
		if runModel.Valid && runModel.String != "" {
			p.RunModel = &runModel.String
		}
		// Same guard, same reason: an empty declaration is "the doc asks for
		// nothing", not a model whose name is the empty string.
		if docModel.Valid && docModel.String != "" {
			p.DocModel = &docModel.String
		}
		if runStartedAt.Valid {
			p.RunStartedAt = &runStartedAt.String
		}
		if runError.Valid {
			p.RunError = &runError.String
		}
		if runCheckboxesBefore.Valid {
			v := int(runCheckboxesBefore.Int64)
			p.RunCheckboxesBefore = &v
		}
		if runEndedAt.Valid {
			p.RunEndedAt = &runEndedAt.String
		}
		if verifyVerdict.Valid {
			p.VerifyVerdict = &verifyVerdict.String
		}
		if verifyDetail.Valid {
			p.VerifyDetail = &verifyDetail.String
		}
		// OutcomeFromRow, never the pure Outcome: the row-aware version is where the
		// stamped right edge beats the live count and a NULL baseline stays
		// unmeasured. Diagnose goes through the same call, so the list chip and the
		// diagnosis modal cannot disagree.
		p.RunOutcome = phasediag.OutcomeFromRow(
			p.RunState, p.CheckboxesTotal, p.CheckboxesDone,
			runCheckboxesBefore, runCheckboxesAfter)
		// THE gate — the same call phaserun's dependency check and the diagnosis
		// modal make, so no surface can privately decide this row is done.
		gateIn := phasegate.Input{
			CriteriaDone:     p.CheckboxesDone,
			CriteriaTotal:    p.CheckboxesTotal,
			VerifyMode:       p.VerifyMode,
			VerifyVerdict:    verifyVerdict.String,
			LegacyDone:       boardCol.String == "done" || (p.BoardTaskID != nil && p.ActivatedAt != nil && boardCol.String == "archived"),
			CompletionReport: completion.String,
			Ran:              runEndedAt.Valid,
			ClosureRequired:  closureRequired,
		}
		gate := phasegate.Check(gateIn)
		p.CompletionState = gate.State
		p.CompletionBlockers = gate.Reasons
		if p.CompletionBlockers == nil {
			p.CompletionBlockers = []string{} // [] not null: the UI maps over it
		}
		if gate.HoldsPlanBack(gateIn) {
			rollup.IncompletePhases++
		}
		rollup.Done += p.CheckboxesDone
		rollup.Total += p.CheckboxesTotal
		phases = append(phases, p)
	}
	if err := rows.Err(); err != nil {
		return nil, epicRollupDTO{}, nil, err
	}
	// The run_events read happens AFTER this result set is closed, never inside
	// the loop above, and that is not a style preference: the store runs on a
	// single SQLite connection, so issuing a second query while these rows are
	// still open takes the only connection the outer cursor is holding and the
	// handler deadlocks until the test binary's 10-minute panic timeout. (Observed
	// exactly that way on the first draft of this change.) The explicit Close is
	// what makes the ordering true — the deferred one runs far too late.
	rows.Close()
	// Same rule, same reason: one query for the whole task AFTER the cursor is
	// closed, never one per phase inside the loop.
	usage := h.phaseRunModels(taskID)
	forecasts := h.phaseForecasts(taskID) // same rule, same reason: one query, cursor closed
	for i := range phases {
		phases[i].RunEvents = runcore.RunEvents(h.DB, phaserun.Engine, phases[i].ID)
		used := usage[phases[i].ID]
		if used == nil {
			used = []phaseModelUseDTO{} // [] not null: the UI maps over it
		}
		phases[i].RunModels, phases[i].RunModelFellBack = used, fellBack(used)
		fs := forecasts[phases[i].ID]
		phases[i].Forecasts = forecastDTOs(fs)
		phases[i].ForecastLints = wsingest.LintForecasts(forecastsOnly(fs))
	}
	if rollup.Total > 0 {
		rollup.Pct = float64(rollup.Done) / float64(rollup.Total) * 100
	}
	return phases, rollup, covers, nil
}

// storedForecast is a phase_forecasts row: the parsed forecast wsingest owns the
// shape of, plus the doc hash the scan stamped on it. doc_hash lives here rather
// than on wsingest.Forecast because it is not part of the FORMAT — the parser
// cannot know it, the scan derives it from the file it read, and only a later
// scoring pass (is the forecast I am grading still the one in the doc?) cares.
type storedForecast struct {
	wsingest.Forecast
	DocHash string
}

// forecastDTOs renders stored forecasts for the wire. Always a non-nil slice:
// the UI maps over it, and "this phase declares no forecast" is [] rather than
// null for the same reason CompletionBlockers is.
func forecastDTOs(fs []storedForecast) []phaseForecastDTO {
	out := make([]phaseForecastDTO, 0, len(fs))
	for _, f := range fs {
		out = append(out, phaseForecastDTO{
			Kind:         f.Kind,
			WrittenAt:    f.WrittenAt,
			Areas:        nonNilStrs(f.Areas),
			Files:        nonNilStrs(f.Files),
			SizeBand:     f.SizeBand,
			DurationBand: f.DurationBand,
			Outcome:      f.Outcome,
			Risks:        nonNilStrs(f.Risks),
			Confidence:   f.Confidence,
			PostHoc:      f.PostHoc,
			DocHash:      f.DocHash,
		})
	}
	return out
}

// forecastsOnly strips the storage wrapper so the lint sees exactly the parsed
// shape it is written against.
func forecastsOnly(fs []storedForecast) []wsingest.Forecast {
	out := make([]wsingest.Forecast, 0, len(fs))
	for _, f := range fs {
		out = append(out, f.Forecast)
	}
	return out
}

func nonNilStrs(v []string) []string {
	if v == nil {
		return []string{}
	}
	return v
}

// phaseForecasts returns, per phase id of this task, the `## Forecast` blocks
// wsingest stored for it, in document order.
//
// One query for the whole task, called only AFTER the phase cursor is closed —
// the store runs on a single SQLite connection and a second query issued while
// that cursor is open deadlocks the handler (see the note at the call site).
//
// Errors degrade to an empty map, like phaseRunModels: a forecast is a decoration
// on a page that must still render, and it is the one field on the row that must
// never be able to take the Plans page down.
func (h *Handler) phaseForecasts(taskID int64) map[int64][]storedForecast {
	out := map[int64][]storedForecast{}
	rows, err := h.DB.Query(`
		SELECT f.phase_id, f.kind, f.written_at, f.areas_json, f.files_json,
		       f.size_band, f.duration_band, f.outcome, f.risks_json,
		       f.confidence, f.post_hoc, f.doc_hash, f.post_hoc_reason
		  FROM phase_forecasts f
		  JOIN epic_phases e ON e.id = f.phase_id
		 WHERE e.workspace_task_id = ?
		 ORDER BY f.phase_id, f.id`, taskID)
	if err != nil {
		log.Printf("warning: epics: phase forecasts unreadable (task %d): %v", taskID, err)
		return out
	}
	defer rows.Close()
	for rows.Next() {
		var (
			phaseID                         int64
			s                               storedForecast
			areasJSON, filesJSON, risksJSON string
			confidence                      sql.NullFloat64
			postHoc                         int
		)
		if err := rows.Scan(&phaseID, &s.Kind, &s.WrittenAt, &areasJSON, &filesJSON,
			&s.SizeBand, &s.DurationBand, &s.Outcome, &risksJSON,
			&confidence, &postHoc, &s.DocHash, &s.PostHocReason); err != nil {
			log.Printf("warning: epics: phase forecast row (task %d): %v", taskID, err)
			return out
		}
		s.Areas, s.Files, s.Risks = decodeStrList(areasJSON), decodeStrList(filesJSON), decodeStrList(risksJSON)
		if confidence.Valid {
			v := confidence.Float64
			s.Confidence = &v
		}
		s.PostHoc = postHoc != 0
		out[phaseID] = append(out[phaseID], s)
	}
	if err := rows.Err(); err != nil {
		log.Printf("warning: epics: phase forecasts (task %d): %v", taskID, err)
	}
	return out
}

// phaseModelUseDTO is one model a phase run actually used, and how many
// assistant turns it carried. The count is what makes the list readable: "42
// turns on opus 5.5, 3 on opus 4.1" says the run was nearly finished when the
// safeguard hit, which "two models" does not.
type phaseModelUseDTO struct {
	Model string `json:"model"`
	Turns int    `json:"turns"`
}

// phaseRunModels returns, per phase id of this task, the models its run session
// used in FIRST-APPEARANCE order.
//
// Chronological rather than heaviest-first on purpose: the last element is "what
// this run finished on", which is the number the chip shows, and a
// frequency-ordered list would put it anywhere. Errors degrade to an empty map —
// this is a decoration on a page that must still render.
func (h *Handler) phaseRunModels(taskID int64) map[int64][]phaseModelUseDTO {
	out := map[int64][]phaseModelUseDTO{}
	rows, err := h.DB.Query(`
		SELECT e.id, tr.model, COUNT(*) AS turns, MIN(tr.seq) AS first_seq
		  FROM epic_phases e
		  JOIN sessions se ON se.session_uuid = e.run_session_uuid
		  JOIN turns tr ON tr.session_id = se.id
		 WHERE e.workspace_task_id = ?
		   AND tr.role = 'assistant' AND tr.model IS NOT NULL AND tr.model <> ''
		 GROUP BY e.id, tr.model
		 ORDER BY e.id, first_seq`, taskID)
	if err != nil {
		log.Printf("warning: epics: phase run models unreadable (task %d): %v", taskID, err)
		return out
	}
	defer rows.Close()
	for rows.Next() {
		var (
			phaseID  int64
			model    string
			turns    int
			firstSeq int64
		)
		if err := rows.Scan(&phaseID, &model, &turns, &firstSeq); err != nil {
			log.Printf("warning: epics: phase run model row unreadable (task %d): %v", taskID, err)
			return out
		}
		out[phaseID] = append(out[phaseID], phaseModelUseDTO{Model: model, Turns: turns})
	}
	return out
}

// fellBack reports whether a run ENDED on a weaker model than it started on.
//
// Not len(use) > 1: `claude-opus-5-5` and `claude-opus-5-5[1m]` are two rows for
// one model (the bracket is a context-window marker, not a model), and a rename
// or a date suffix would likewise light a chip that claims something untrue.
func fellBack(use []phaseModelUseDTO) bool {
	if len(use) < 2 {
		return false
	}
	// The direction rule lives in modelid.IsFallback, not here: the session row
	// renders the same claim off the same two ids, and two copies of "is this
	// weaker" is how one surface starts calling an escalation a fallback.
	return modelid.IsFallback(use[0].Model, use[len(use)-1].Model)
}

// decodeIntList parses a JSON array of ints; [] on empty/garbage.
func decodeIntList(s string) []int {
	out := []int{}
	if strings.TrimSpace(s) == "" {
		return out
	}
	if err := json.Unmarshal([]byte(s), &out); err != nil || out == nil {
		return []int{}
	}
	return out
}

// decodeStrList parses a JSON array of strings; [] on empty/garbage (the same
// posture decodeIntList takes for depends_on).
func decodeStrList(s string) []string {
	out := []string{}
	if strings.TrimSpace(s) == "" {
		return out
	}
	if err := json.Unmarshal([]byte(s), &out); err != nil || out == nil {
		return []string{}
	}
	return out
}

// relToPlan returns doc's path relative to planDir, or the basename when it is
// not under planDir (best-effort — the doc endpoints re-confine anyway).
func relToPlan(planDir, doc string) string {
	if planDir == "" {
		return filepath.Base(doc)
	}
	if rel, err := filepath.Rel(planDir, doc); err == nil && !strings.HasPrefix(rel, "..") {
		return rel
	}
	return filepath.Base(doc)
}

// ── plan-doc editor: GET/PUT/PATCH /api/epics/{taskId}/docs?path= ───────────

// planDocMaxBytes caps a plan doc write (generous — plans are prose).
const planDocMaxBytes = 1 << 20 // 1 MiB

// resolvePlanDoc confines ?path= to the task's plan/ dir and returns the
// absolute file path. The plan dir comes from task_artifacts (kind='plan').
// Confinement: both the plan dir and the target are resolved through
// EvalSymlinks (the dir must exist; the file may not yet, so its PARENT is
// resolved) and the target must be strictly under the plan dir. A traversal or
// symlink escape yields ErrPathEscape.
var errPathEscape = errors.New("path escapes the plan directory")

func (h *Handler) resolvePlanDoc(taskID int64, rel string) (string, error) {
	var planDir string
	err := h.DB.QueryRow(
		`SELECT path FROM task_artifacts WHERE task_id = ? AND kind = 'plan'`, taskID).Scan(&planDir)
	if errors.Is(err, sql.ErrNoRows) {
		return "", sql.ErrNoRows
	}
	if err != nil {
		return "", err
	}

	rootAbs, err := filepath.EvalSymlinks(planDir)
	if err != nil {
		return "", err
	}
	// Join rel onto the plan dir; a leading "/" or ".." must not escape. Clean
	// first, then verify the resolved parent stays under the root.
	rel = strings.TrimPrefix(filepath.Clean("/"+rel), "/") // strip any leading slash, normalize ..
	target := filepath.Join(rootAbs, rel)

	// Resolve the target's PARENT (the file itself may not exist on a fresh
	// write) and re-check containment against the real root.
	parentReal, err := filepath.EvalSymlinks(filepath.Dir(target))
	if err != nil {
		return "", errPathEscape // a non-existent/again-symlinked parent → refuse
	}
	final := filepath.Join(parentReal, filepath.Base(target))
	if final != rootAbs && !strings.HasPrefix(final, rootAbs+string(os.PathSeparator)) {
		return "", errPathEscape
	}
	if !strings.HasSuffix(strings.ToLower(final), ".md") {
		return "", errPathEscape // only markdown plan docs are editable
	}
	// If the file itself EXISTS, resolve its full symlink chain and re-check
	// containment — a symlink INSIDE plan/ pointing OUT (its parent resolves to
	// the plan dir, so the check above passes) must not leak the target.
	if realFinal, err := filepath.EvalSymlinks(final); err == nil {
		if realFinal != rootAbs && !strings.HasPrefix(realFinal, rootAbs+string(os.PathSeparator)) {
			return "", errPathEscape
		}
	}
	return final, nil
}

// planDocResponse is the GET/PUT body: the content + its relative path.
type planDocResponse struct {
	Path    string `json:"path"`
	Content string `json:"content"`
	// Backup is the on-disk backup path a PUT/PATCH wrote (absent on GET).
	Backup string `json:"backup,omitempty"`
}

// getPlanDoc — GET /api/epics/{taskId}/docs?path=. Read a plan doc.
func (h *Handler) getPlanDoc(w http.ResponseWriter, r *http.Request) {
	taskID, ok := parseTaskIDParam(w, r)
	if !ok {
		return
	}
	rel := r.URL.Query().Get("path")
	if strings.TrimSpace(rel) == "" {
		writeClientErr(w, http.StatusBadRequest, "path query param required")
		return
	}
	path, err := h.resolvePlanDoc(taskID, rel)
	if err != nil {
		writePlanDocErr(w, err)
		return
	}
	body, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			writeClientErr(w, http.StatusNotFound, "doc not found")
			return
		}
		writeErr(w, err)
		return
	}
	writeJSON(w, planDocResponse{Path: rel, Content: string(body)}, nil)
}

// putPlanDoc — PUT /api/epics/{taskId}/docs?path= {content}. Overwrite a plan
// doc after taking a timestamped backup next to it. requireLocalOrigin.
func (h *Handler) putPlanDoc(w http.ResponseWriter, r *http.Request) {
	taskID, ok := parseTaskIDParam(w, r)
	if !ok {
		return
	}
	rel := r.URL.Query().Get("path")
	if strings.TrimSpace(rel) == "" {
		writeClientErr(w, http.StatusBadRequest, "path query param required")
		return
	}
	var reqBody struct {
		Content string `json:"content"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, planDocMaxBytes+4096)).Decode(&reqBody); err != nil {
		writeClientErr(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if len(reqBody.Content) > planDocMaxBytes {
		writeClientErr(w, http.StatusRequestEntityTooLarge, "doc too large")
		return
	}
	path, err := h.resolvePlanDoc(taskID, rel)
	if err != nil {
		writePlanDocErr(w, err)
		return
	}
	backup, err := writePlanDocFile(path, reqBody.Content)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, planDocResponse{Path: rel, Content: reqBody.Content, Backup: backup}, nil)
}

// checkboxLineRe matches an acceptance checkbox line and captures its state.
var checkboxLineRe = regexp.MustCompile(`^(\s*[-*]\s+\[)( |x|X)(\]\s.*)$`)

// patchPlanDoc — PATCH /api/epics/{taskId}/docs?path= {line, done}. Flip one
// checkbox by 0-based line index (the exact `- [ ]`↔`- [x]` line). Takes a
// backup first; the next wsingest rescan folds the new count into the rollup.
// requireLocalOrigin.
func (h *Handler) patchPlanDoc(w http.ResponseWriter, r *http.Request) {
	taskID, ok := parseTaskIDParam(w, r)
	if !ok {
		return
	}
	rel := r.URL.Query().Get("path")
	if strings.TrimSpace(rel) == "" {
		writeClientErr(w, http.StatusBadRequest, "path query param required")
		return
	}
	var reqBody struct {
		Line *int  `json:"line"`
		Done *bool `json:"done"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 4096)).Decode(&reqBody); err != nil {
		writeClientErr(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if reqBody.Line == nil || reqBody.Done == nil {
		writeClientErr(w, http.StatusBadRequest, "line and done are required")
		return
	}
	path, err := h.resolvePlanDoc(taskID, rel)
	if err != nil {
		writePlanDocErr(w, err)
		return
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			writeClientErr(w, http.StatusNotFound, "doc not found")
			return
		}
		writeErr(w, err)
		return
	}
	// Split preserving the trailing-newline shape: strings.Split keeps a final
	// "" for a trailing \n, which Join restores exactly.
	lines := strings.Split(string(raw), "\n")
	i := *reqBody.Line
	if i < 0 || i >= len(lines) {
		writeClientErr(w, http.StatusBadRequest, "line index out of range")
		return
	}
	m := checkboxLineRe.FindStringSubmatch(lines[i])
	if m == nil {
		writeClientErr(w, http.StatusBadRequest, "line is not a checkbox")
		return
	}
	mark := " "
	if *reqBody.Done {
		mark = "x"
	}
	lines[i] = m[1] + mark + m[3]

	backup, err := writePlanDocFile(path, strings.Join(lines, "\n"))
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, planDocResponse{Path: rel, Content: strings.Join(lines, "\n"), Backup: backup}, nil)
}

// writePlanDocFile backs up the current file (when it exists) next to it under
// a `.backups/<ts>/` dir, then writes content. Returns the backup path ("" when
// the file did not exist yet). The backup dir is inside plan/, so it stays
// under the same confinement root and travels with the workspace git repo.
func writePlanDocFile(path, content string) (string, error) {
	backup := ""
	if cur, err := os.ReadFile(path); err == nil {
		ts := time.Now().UTC().Format("2006-01-02T15-04-05Z")
		bdir := filepath.Join(filepath.Dir(path), ".backups", ts)
		if err := os.MkdirAll(bdir, 0o755); err != nil {
			return "", err
		}
		backup = filepath.Join(bdir, filepath.Base(path))
		if err := os.WriteFile(backup, cur, 0o644); err != nil {
			return "", err
		}
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		return "", err
	}
	return backup, nil
}

// parseTaskIDParam parses {taskId}; writes a 400 and returns ok=false on failure.
func parseTaskIDParam(w http.ResponseWriter, r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue("taskId"), 10, 64)
	if err != nil {
		writeClientErr(w, http.StatusBadRequest, "invalid task id")
		return 0, false
	}
	return id, true
}

// writePlanDocErr maps confinement/lookup errors to the right status.
func writePlanDocErr(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, errPathEscape):
		writeClientErr(w, http.StatusBadRequest, "invalid path")
	case errors.Is(err, sql.ErrNoRows):
		writeClientErr(w, http.StatusNotFound, "no plan directory for this task")
	default:
		writeErr(w, err)
	}
}
