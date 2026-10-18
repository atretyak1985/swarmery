// Plan in the terminal (mods-swarmery-link phase 7, decision D5): the daemon
// owns plan state and its one markdown parser; the link mod only draws. Contract:
// docs/mods-link-protocol.md "Plan".
//
//	GET  /api/link/plan?session=&cwd=    → 200 planView | 204 no plan
//	POST /api/link/plan/select           → pin a plan (and phase) to the session
//	POST /api/link/plan/tick             → flip one criterion (the PATCH docs core)
//	POST /api/link/plan/run              → start the phase run (the runPhase core)
//	POST /api/link/plan/drift            → record a drift verdict (link_events kind drift)
//	GET  /api/epics/{taskId}/terminal-sessions   (SPA) → which sessions have it open
//
// Every /api/link/plan* route is behind linkAuth (bearer token, 503 while the
// link is not attached); terminal-sessions is a plain SPA read like GET
// /api/epics.
//
// A criterion is addressed by `idx`, its 0-based ordinal among the doc's
// criteria (wsingest.ListCriteria: every checkbox outside a fence, in document
// order). The view and the tick resolve it over the same walker, so the index
// the mod draws is the index it ticks.
package api

import (
	"database/sql"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/ingest"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/link"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/phasegate"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/phaserun"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/wsingest"
)

const (
	// planChosenTerminal / planChosenAuto are session_plan.chosen_by and the
	// view's chosenBy: the operator picked the plan in the terminal, or the
	// daemon resolved it from the session's project.
	planChosenTerminal = "terminal"
	planChosenAuto     = "auto"
	// planAutoWindow is how long an automatic resolution keeps a session in
	// GET /api/epics/{taskId}/terminal-sessions after its last fetch.
	planAutoWindow = 10 * time.Minute
	// maxLinkPlanBody bounds one /api/link/plan/* POST body.
	maxLinkPlanBody = 16 << 10
	// maxDriftReason caps a drift report's reason, in runes.
	maxDriftReason = 200
	// planActiveScanCap bounds how many of a project's newest plans the
	// automatic resolution inspects (each costs one epicPhases read).
	planActiveScanCap = 50
	// sessionPlanTS is the fixed-width ms UTC form session_plan.at is stored in
	// (the link's tsFormat), so the TEXT column orders like time.
	sessionPlanTS = "2006-01-02T15:04:05.000Z"
)

// Phase statuses as the view names them — the doc's own `Status:` vocabulary
// (plan-format.md) plus the completion gate's two refusals. Derived exactly as
// the Plans page derives its phase chip (web/src/pages/Plans.tsx phaseStatus).
const (
	phaseStatusPending    = "Pending"
	phaseStatusInProgress = "In progress"
	phaseStatusBlocked    = "Blocked"
	phaseStatusDone       = "Done"
	phaseStatusUnverified = "Unverified"
	phaseStatusUnreported = "Unreported"
)

// planCriterionDTO is one acceptance criterion of the view's phase.
type planCriterionDTO struct {
	Idx   int    `json:"idx"`
	Text  string `json:"text"`
	Done  bool   `json:"done"`
	Class string `json:"class"` // "" | land | manual
}

// planPhaseView is the phase the terminal shows.
type planPhaseView struct {
	ID       string             `json:"id"` // epic_phases.id, as the epics API names it
	N        int                `json:"n"`  // seq
	Title    string             `json:"title"`
	Status   string             `json:"status"`
	Done     int                `json:"done"`
	Total    int                `json:"total"`
	Criteria []planCriterionDTO `json:"criteria"`
}

// planNextView is the phase after it that is not done yet.
type planNextView struct {
	ID    string `json:"id"`
	N     int    `json:"n"`
	Title string `json:"title"`
}

// planView is the GET /api/link/plan body (and every plan POST's 200).
type planView struct {
	TaskID   string        `json:"taskId"` // tasks.external_id (numeric id when it has none)
	Title    string        `json:"title"`
	URL      string        `json:"url"` // the Plans page deep link to this plan + phase
	ChosenBy string        `json:"chosenBy"`
	Phase    planPhaseView `json:"phase"`
	Next     *planNextView `json:"next"`
}

// planEpic is one workspace task with phases, as the plan view resolves it.
type planEpic struct {
	id       int64
	ref      string // the view's taskId: external_id, else the numeric id
	title    string
	slug     string // project slug — the deep link's /p/<slug>
	planDir  string
	status   string // raw tasks.status
	archived bool
}

// planEpicPhases is an epic with its phases loaded and their view statuses.
type planEpicPhases struct {
	epic     *planEpic
	phases   []epicPhaseDTO
	statuses []string // parallel to phases
	rollup   epicRollupDTO
}

// flexID is an id the mod may send as a JSON string or a number.
type flexID string

func (f *flexID) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err == nil {
		*f = flexID(strings.TrimSpace(s))
		return nil
	}
	var n json.Number
	if err := json.Unmarshal(b, &n); err != nil {
		return err
	}
	*f = flexID(n.String())
	return nil
}

// ── reads ─────────────────────────────────────────────────────────────────────

// planEpicCols is the column list scanPlanEpic reads.
const planEpicCols = `t.id, COALESCE(t.external_id, ''), t.title, p.slug, t.status,
	t.archived_at IS NOT NULL,
	COALESCE((SELECT path FROM task_artifacts WHERE task_id = t.id AND kind = 'plan'), '')`

func scanPlanEpic(sc interface{ Scan(...any) error }) (*planEpic, error) {
	var e planEpic
	if err := sc.Scan(&e.id, &e.ref, &e.title, &e.slug, &e.status, &e.archived, &e.planDir); err != nil {
		return nil, err
	}
	if e.ref == "" {
		e.ref = strconv.FormatInt(e.id, 10)
	}
	return &e, nil
}

// planScope confines a link request's plan lookups to one project: the
// session's, else the project owning its cwd (planProjectFor).
// tasks.external_id is unique per workspace only, so two projects can carry
// the same `yyyy-mm-dd-slug`; unscoped, a ref could resolve to the other
// project's plan. A request whose session and cwd name no project (a session
// the daemon has not indexed yet, no cwd) looks across every project.
type planScope struct {
	projectID int64
	known     bool
}

// sql is the scope's extra WHERE clause (on tasks aliased t) and its args.
func (s planScope) sql() (string, []any) {
	if !s.known {
		return "", nil
	}
	return ` AND t.project_id = ?`, []any{s.projectID}
}

func (h *Handler) planScopeFor(uuid, cwd string) (planScope, error) {
	pid, ok, err := h.planProjectFor(uuid, cwd)
	if err != nil {
		return planScope{}, err
	}
	return planScope{projectID: pid, known: ok}, nil
}

// planEpicOrder makes a lookup that may match several plans deterministic (an
// unscoped one, with two projects sharing a ref): an unarchived plan first,
// then the newest task.
const planEpicOrder = ` ORDER BY t.archived_at IS NOT NULL, t.id DESC LIMIT 1`

// matchesRef reports whether ref names e: its external_id or its numeric id.
func (e *planEpic) matchesRef(ref string) bool {
	ref = strings.TrimSpace(ref)
	return ref != "" && (ref == e.ref || ref == strconv.FormatInt(e.id, 10))
}

// planEpicByRef finds the workspace plan a link body names, within scope: by
// external_id, else — for a ref that is all digits — by tasks.id. nil, nil
// when there is no such plan (a task without phases is not one).
func (h *Handler) planEpicByRef(ref string, scope planScope) (*planEpic, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return nil, nil
	}
	const q = `SELECT ` + planEpicCols + `
		  FROM tasks t JOIN projects p ON p.id = t.project_id
		 WHERE t.source = 'workspace'
		   AND EXISTS (SELECT 1 FROM epic_phases e WHERE e.workspace_task_id = t.id)`
	clause, args := scope.sql()
	e, err := scanPlanEpic(h.DB.QueryRow(q+` AND t.external_id = ?`+clause+planEpicOrder, append([]any{ref}, args...)...))
	if err == nil {
		return e, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	id, perr := strconv.ParseInt(ref, 10, 64)
	if perr != nil {
		return nil, nil
	}
	e, err = scanPlanEpic(h.DB.QueryRow(q+` AND t.id = ?`+clause, append([]any{id}, args...)...))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return e, err
}

// planEpicByPhase finds the plan that owns phase phaseID (epic_phases.id is
// unique across projects, so no scope is needed), provided ref names it too.
// nil, nil for a phase that is not a number, unknown, or another plan's.
func (h *Handler) planEpicByPhase(phaseID, ref string) (*planEpic, error) {
	id, err := strconv.ParseInt(strings.TrimSpace(phaseID), 10, 64)
	if err != nil {
		return nil, nil
	}
	e, err := scanPlanEpic(h.DB.QueryRow(`SELECT `+planEpicCols+`
		  FROM epic_phases ph
		  JOIN tasks t ON t.id = ph.workspace_task_id
		  JOIN projects p ON p.id = t.project_id
		 WHERE ph.id = ? AND t.source = 'workspace'`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if !e.matchesRef(ref) {
		return nil, nil
	}
	return e, nil
}

// planEpicFor resolves a plan by ref and, when phaseID is set, by that phase:
// the phase row first (exact), else the ref within scope. ok=false: the phase
// is set but is not the plan's — the epic returned is then the ref's plan (or
// nil), so a caller can tell an unknown plan from a foreign phase.
func (h *Handler) planEpicFor(ref, phaseID string, scope planScope) (*planEpic, bool, error) {
	if phaseID != "" {
		e, err := h.planEpicByPhase(phaseID, ref)
		if err != nil || e != nil {
			return e, err == nil, err
		}
		e, err = h.planEpicByRef(ref, scope)
		return e, false, err
	}
	e, err := h.planEpicByRef(ref, scope)
	return e, true, err
}

// loadPlanPhases reads an epic's phases (the epics API's own read, so every
// gate field is the one the Plans page sees) and derives their view statuses.
func (h *Handler) loadPlanPhases(e *planEpic) (*planEpicPhases, error) {
	phases, rollup, _, err := h.epicPhases(e.id, e.planDir)
	if err != nil {
		return nil, err
	}
	return &planEpicPhases{epic: e, phases: phases, statuses: phaseViewStatuses(phases), rollup: rollup}, nil
}

// phaseViewStatuses mirrors the Plans page's phaseStatus: the board column and
// the completion gate first, then the doc's own `Status: In progress`, a live
// run, the dependency gate, and any ticked criterion.
func phaseViewStatuses(phases []epicPhaseDTO) []string {
	resolvedCol := func(col *string) bool {
		return col != nil && (*col == "done" || *col == "archived")
	}
	resolved := map[int]bool{}
	for _, p := range phases {
		if resolvedCol(p.BoardColumn) || p.CompletionState == phasegate.StateComplete {
			resolved[p.Seq] = true
		}
	}
	out := make([]string, len(phases))
	for i, p := range phases {
		out[i] = phaseViewStatus(p, resolvedCol(p.BoardColumn), resolved)
	}
	return out
}

func phaseViewStatus(p epicPhaseDTO, colResolved bool, resolved map[int]bool) string {
	switch {
	case colResolved, p.CompletionState == phasegate.StateComplete:
		return phaseStatusDone
	case p.CompletionState == phasegate.StateUnverified:
		return phaseStatusUnverified
	case p.CompletionState == phasegate.StateUnreported:
		return phaseStatusUnreported
	case p.DocStatus != nil && *p.DocStatus == "in_progress", p.RunState == "running":
		return phaseStatusInProgress
	}
	for _, seq := range p.DependsOn {
		if !resolved[seq] {
			return phaseStatusBlocked
		}
	}
	if p.CheckboxesDone > 0 || (p.BoardColumn != nil && (*p.BoardColumn == "in_progress" || *p.BoardColumn == "in_review")) {
		return phaseStatusInProgress
	}
	return phaseStatusPending
}

// latestDocUpdate is the newest phase-doc mtime of an epic — "most recently
// updated" for the automatic resolution. Zero when no phase carries one.
func latestDocUpdate(phases []epicPhaseDTO) time.Time {
	var best time.Time
	for _, p := range phases {
		if p.DocUpdatedAt == nil {
			continue
		}
		if t, err := time.Parse(time.RFC3339Nano, *p.DocUpdatedAt); err == nil && t.After(best) {
			best = t
		}
	}
	return best
}

// activePlanEpic is the automatic resolution for a project: the plan with a
// phase In progress (the most recently updated when several), else the newest
// plan the epics list reports `active`. nil when the project has neither.
func (h *Handler) activePlanEpic(projectID int64) (*planEpicPhases, error) {
	rows, err := h.DB.Query(`SELECT `+planEpicCols+`
		  FROM tasks t JOIN projects p ON p.id = t.project_id
		 WHERE t.source = 'workspace' AND t.project_id = ? AND t.archived_at IS NULL
		   AND EXISTS (SELECT 1 FROM epic_phases e WHERE e.workspace_task_id = t.id)
		 ORDER BY t.started_at DESC, t.id DESC
		 LIMIT ?`, projectID, planActiveScanCap)
	if err != nil {
		return nil, err
	}
	// Materialize, then close: the pool is single-connection and every epic
	// below costs further queries (see listEpics).
	var epics []*planEpic
	for rows.Next() {
		e, err := scanPlanEpic(rows)
		if err != nil {
			rows.Close()
			return nil, err
		}
		epics = append(epics, e)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}

	var (
		best     *planEpicPhases
		bestAt   time.Time
		fallback *planEpicPhases
	)
	for _, e := range epics {
		ep, err := h.loadPlanPhases(e)
		if err != nil {
			return nil, err
		}
		inProgress := false
		for _, s := range ep.statuses {
			if s == phaseStatusInProgress {
				inProgress = true
				break
			}
		}
		if inProgress {
			// Strictly newer only: on a tie the newer plan (scan order) keeps it.
			if at := latestDocUpdate(ep.phases); best == nil || at.After(bestAt) {
				best, bestAt = ep, at
			}
			continue
		}
		if fallback == nil && planStatus(e.archived, e.status, ep.rollup.Done, ep.rollup.Total,
			ep.rollup.IncompletePhases == 0) == "active" {
			fallback = ep
		}
	}
	if best != nil {
		return best, nil
	}
	return fallback, nil
}

// planProjectFor resolves the project a link session works in: the session
// row's project, else the registered project owning cwd (the path itself or its
// closest registered ancestor — ingest.CanonicalProjectPath, the resolver
// attribution uses). ok=false when neither names a project.
func (h *Handler) planProjectFor(uuid, cwd string) (int64, bool, error) {
	if uuid != "" {
		var (
			id   int64
			path string
		)
		err := h.DB.QueryRow(`SELECT p.id, p.path FROM sessions s JOIN projects p ON p.id = s.project_id
			 WHERE s.session_uuid = ?`, uuid).Scan(&id, &path)
		switch {
		case err == nil && path != ingest.UnknownProjectPath:
			return id, true, nil
		case err != nil && !errors.Is(err, sql.ErrNoRows):
			return 0, false, err
		}
	}
	if cwd == "" || !filepath.IsAbs(cwd) {
		return 0, false, nil
	}
	canon := ingest.CanonicalProjectPath(h.DB, filepath.Clean(cwd))
	var id int64
	err := h.DB.QueryRow(`SELECT id FROM projects WHERE path = ?`, canon).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, err
	}
	return id, true, nil
}

// sessionPlanRow is one session_plan row.
type sessionPlanRow struct {
	taskID   string
	phaseID  string // "" when NULL
	chosenBy string
}

func (h *Handler) sessionPlan(uuid string) (*sessionPlanRow, error) {
	if uuid == "" {
		return nil, nil
	}
	var (
		row   sessionPlanRow
		phase sql.NullString
	)
	err := h.DB.QueryRow(`SELECT task_id, phase_id, chosen_by FROM session_plan WHERE session_id = ?`, uuid).
		Scan(&row.taskID, &phase, &row.chosenBy)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	row.phaseID = phase.String
	return &row, nil
}

// chosenByFor is the session's chosenBy for a view the request did not resolve
// itself (tick): terminal while the session has a terminal pick, else auto.
func (h *Handler) chosenByFor(uuid string) string {
	row, err := h.sessionPlan(uuid)
	if err != nil {
		log.Printf("warning: api: link plan: session_plan %s: %v", uuid, err)
	}
	if row != nil && row.chosenBy == planChosenTerminal {
		return planChosenTerminal
	}
	return planChosenAuto
}

// upsertSessionPlan writes the session's row. An automatic write never replaces
// a terminal pick — the operator's choice is sticky until the next select.
func (h *Handler) upsertSessionPlan(uuid, taskRef, phaseID, chosenBy string) error {
	var phase any
	if phaseID != "" {
		phase = phaseID
	}
	q := `INSERT INTO session_plan (session_id, task_id, phase_id, chosen_by, at) VALUES (?, ?, ?, ?, ?)
		ON CONFLICT(session_id) DO UPDATE SET task_id = excluded.task_id, phase_id = excluded.phase_id,
		       chosen_by = excluded.chosen_by, at = excluded.at`
	if chosenBy == planChosenAuto {
		q += ` WHERE session_plan.chosen_by = 'auto'`
	}
	_, err := h.DB.Exec(q, uuid, taskRef, phase, chosenBy, time.Now().UTC().Format(sessionPlanTS))
	return err
}

// phaseTitlePrefixRe strips a leading "Phase 7 — " from a phase name: the view
// carries the number in `n`, and the mod draws both.
var phaseTitlePrefixRe = regexp.MustCompile(`(?i)^\s*(?:phase|step)\s+\d+\s*[—–:\-.]\s*`)

func phaseViewTitle(name string) string {
	if t := phaseTitlePrefixRe.ReplaceAllString(name, ""); strings.TrimSpace(t) != "" {
		return strings.TrimSpace(t)
	}
	return strings.TrimSpace(name)
}

// buildPlanView renders the view of ep at phase wantPhase (0 = the active phase:
// the first In progress, else the first not Done, else the last). nil when the
// plan has no phases.
func buildPlanView(r *http.Request, ep *planEpicPhases, wantPhase int64, chosenBy string) *planView {
	if len(ep.phases) == 0 {
		return nil
	}
	at := -1
	if wantPhase != 0 {
		for i, p := range ep.phases {
			if p.ID == wantPhase {
				at = i
				break
			}
		}
	}
	if at < 0 {
		at = activePhaseIndex(ep.statuses)
	}
	p := ep.phases[at]
	ph := planPhaseView{
		ID:       strconv.FormatInt(p.ID, 10),
		N:        p.Seq,
		Title:    phaseViewTitle(p.Name),
		Status:   ep.statuses[at],
		Done:     p.CheckboxesDone,
		Total:    p.CheckboxesTotal,
		Criteria: []planCriterionDTO{},
	}
	// The doc itself, not the scanned counts: a tick lands on disk now and in
	// epic_phases only on the next rescan, and the terminal must show it at once.
	if body, err := os.ReadFile(p.DocPath); err == nil {
		crit := wsingest.ListCriteria(string(body))
		ph.Done, ph.Total = 0, len(crit)
		for i, c := range crit {
			if c.Done {
				ph.Done++
			}
			ph.Criteria = append(ph.Criteria, planCriterionDTO{
				Idx: i, Text: c.Text, Done: c.Done, Class: strings.ToLower(c.Class),
			})
		}
	} else if p.DocPath != "" {
		log.Printf("warning: api: link plan: phase doc %s unreadable: %v", p.DocPath, err)
	}
	v := &planView{
		TaskID:   ep.epic.ref,
		Title:    ep.epic.title,
		URL:      planDeepLink(r, ep.epic, p.Seq),
		ChosenBy: chosenBy,
		Phase:    ph,
	}
	for i := at + 1; i < len(ep.phases); i++ {
		if ep.statuses[i] != phaseStatusDone {
			n := ep.phases[i]
			v.Next = &planNextView{ID: strconv.FormatInt(n.ID, 10), N: n.Seq, Title: phaseViewTitle(n.Name)}
			break
		}
	}
	return v
}

func activePhaseIndex(statuses []string) int {
	for i, s := range statuses {
		if s == phaseStatusInProgress {
			return i
		}
	}
	for i, s := range statuses {
		if s != phaseStatusDone {
			return i
		}
	}
	return len(statuses) - 1
}

// planDeepLink is the daemon's own origin (the request's Host — the Host fence
// has already vouched for it) plus the Plans page path of this plan's phase:
// /p/<slug>/plans/<externalId>/phase/<seq> (web/src/pages/plans/plansUrl.ts).
func planDeepLink(r *http.Request, e *planEpic, seq int) string {
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	return scheme + "://" + r.Host + "/p/" + url.PathEscape(e.slug) + "/plans/" +
		url.PathEscape(e.ref) + "/phase/" + strconv.Itoa(seq)
}

// phaseOf finds a phase of ep by its id as the wire carries it.
func (ep *planEpicPhases) phaseOf(id string) (epicPhaseDTO, bool) {
	n, err := strconv.ParseInt(strings.TrimSpace(id), 10, 64)
	if err != nil {
		return epicPhaseDTO{}, false
	}
	for _, p := range ep.phases {
		if p.ID == n {
			return p, true
		}
	}
	return epicPhaseDTO{}, false
}

// ── GET /api/link/plan ────────────────────────────────────────────────────────

// linkPlanGet — GET /api/link/plan?session=<uuid>&cwd=<abs path>.
//
//	200 planView   the session's terminal pick, else its project's active plan
//	204            no plan for this session / project
//	400            neither session nor cwd
func (h *Handler) linkPlanGet(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	uuid := strings.TrimSpace(q.Get("session"))
	cwd := strings.TrimSpace(q.Get("cwd"))
	if uuid == "" && cwd == "" {
		writeClientErr(w, http.StatusBadRequest, "session or cwd is required")
		return
	}

	// The session's project scopes every lookup below.
	scope, err := h.planScopeFor(uuid, cwd)
	if err != nil {
		writeErr(w, err)
		return
	}

	// 1. The operator's pick in the terminal.
	row, err := h.sessionPlan(uuid)
	if err != nil {
		writeErr(w, err)
		return
	}
	if row != nil && row.chosenBy == planChosenTerminal {
		// A stored phase pins the plan exactly; a phase that is gone (or not
		// the plan's) leaves the ref, looked up within the project.
		e, _, err := h.planEpicFor(row.taskID, row.phaseID, scope)
		if err != nil {
			writeErr(w, err)
			return
		}
		if e != nil {
			ep, err := h.loadPlanPhases(e)
			if err != nil {
				writeErr(w, err)
				return
			}
			want, _ := strconv.ParseInt(row.phaseID, 10, 64)
			if v := buildPlanView(r, ep, want, planChosenTerminal); v != nil {
				writeJSON(w, v, nil)
				return
			}
		}
		// The picked plan is gone (archived out of the index, renamed): fall
		// through to the automatic resolution rather than answer nothing.
	}

	// 2. The active plan of the session's project.
	if !scope.known {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	ep, err := h.activePlanEpic(scope.projectID)
	if err != nil {
		writeErr(w, err)
		return
	}
	var v *planView
	if ep != nil {
		v = buildPlanView(r, ep, 0, planChosenAuto)
	}
	if v == nil {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if uuid != "" {
		// A record for the SPA's terminal-sessions list; best effort — the view
		// is right whether or not the row lands.
		if err := h.upsertSessionPlan(uuid, v.TaskID, v.Phase.ID, planChosenAuto); err != nil {
			log.Printf("warning: api: link plan: record auto %s: %v", uuid, err)
		}
	}
	writeJSON(w, v, nil)
}

// ── POST bodies ───────────────────────────────────────────────────────────────

// linkPlanBody is the union of the /api/link/plan/* POST bodies; each route
// reads its own fields.
type linkPlanBody struct {
	Session string `json:"session"`
	// Cwd (optional) scopes the plan lookup to the project owning it when
	// the daemon has no sessions row for Session yet, as GET's ?cwd= does.
	Cwd     string `json:"cwd"`
	TaskID  flexID `json:"taskId"`
	PhaseID flexID `json:"phaseId"`
	Idx     *int   `json:"idx"`
	Done    *bool  `json:"done"`
	// Text (tick, optional) is the criterion's text as the mod drew it: the
	// tick is refused 409 when the doc's idx-th criterion no longer reads so.
	Text    *string `json:"text"`
	Verdict string  `json:"verdict"`
	Reason  string  `json:"reason"`
}

// decodeLinkPlanBody reads a bounded JSON body; writes the 400/413 itself.
func decodeLinkPlanBody(w http.ResponseWriter, r *http.Request) (linkPlanBody, bool) {
	var b linkPlanBody
	r.Body = http.MaxBytesReader(w, r.Body, maxLinkPlanBody)
	if err := json.NewDecoder(r.Body).Decode(&b); err != nil {
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			writeClientErr(w, http.StatusRequestEntityTooLarge, "body exceeds 16 KiB")
			return b, false
		}
		writeClientErr(w, http.StatusBadRequest, "invalid JSON body")
		return b, false
	}
	b.Session = strings.TrimSpace(b.Session)
	b.Cwd = strings.TrimSpace(b.Cwd)
	return b, true
}

// resolvePlanTarget loads the body's plan and, when wantPhase, its phase;
// writes the 400/404 itself. A phaseId pins the plan through its phase row;
// otherwise the taskId is looked up within the session's project.
func (h *Handler) resolvePlanTarget(w http.ResponseWriter, b linkPlanBody, wantPhase bool) (*planEpicPhases, epicPhaseDTO, bool) {
	if b.TaskID == "" {
		writeClientErr(w, http.StatusBadRequest, "taskId is required")
		return nil, epicPhaseDTO{}, false
	}
	if wantPhase && b.PhaseID == "" {
		writeClientErr(w, http.StatusBadRequest, "phaseId is required")
		return nil, epicPhaseDTO{}, false
	}
	scope, err := h.planScopeFor(b.Session, b.Cwd)
	if err != nil {
		writeErr(w, err)
		return nil, epicPhaseDTO{}, false
	}
	e, phaseOK, err := h.planEpicFor(string(b.TaskID), string(b.PhaseID), scope)
	if err != nil {
		writeErr(w, err)
		return nil, epicPhaseDTO{}, false
	}
	if e == nil {
		writeClientErr(w, http.StatusNotFound, "plan not found")
		return nil, epicPhaseDTO{}, false
	}
	if !phaseOK {
		writeClientErr(w, http.StatusNotFound, "phase not found")
		return nil, epicPhaseDTO{}, false
	}
	ep, err := h.loadPlanPhases(e)
	if err != nil {
		writeErr(w, err)
		return nil, epicPhaseDTO{}, false
	}
	if b.PhaseID == "" {
		return ep, epicPhaseDTO{}, true
	}
	p, ok := ep.phaseOf(string(b.PhaseID))
	if !ok {
		writeClientErr(w, http.StatusNotFound, "phase not found")
		return nil, epicPhaseDTO{}, false
	}
	return ep, p, true
}

// ── POST /api/link/plan/select ────────────────────────────────────────────────

// linkPlanSelect — POST /api/link/plan/select {session, taskId, phaseId?}: pin
// the plan (and phase) to the session, chosen_by terminal. 200 planView; 400 bad
// body; 404 unknown plan or phase.
func (h *Handler) linkPlanSelect(w http.ResponseWriter, r *http.Request) {
	b, ok := decodeLinkPlanBody(w, r)
	if !ok {
		return
	}
	if b.Session == "" {
		writeClientErr(w, http.StatusBadRequest, "session is required")
		return
	}
	ep, p, ok := h.resolvePlanTarget(w, b, false)
	if !ok {
		return
	}
	phaseID := ""
	if p.ID != 0 {
		phaseID = strconv.FormatInt(p.ID, 10)
	}
	if err := h.upsertSessionPlan(b.Session, ep.epic.ref, phaseID, planChosenTerminal); err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, buildPlanView(r, ep, p.ID, planChosenTerminal), nil)
}

// ── POST /api/link/plan/tick ──────────────────────────────────────────────────

// criterionChanged is the tick's 409 message: the drawn text no longer matches.
const criterionChanged = "criterion changed"

// linkPlanTick — POST /api/link/plan/tick {session, taskId, phaseId, idx, done,
// text?}: flip the idx-th criterion of the phase doc through
// flipPlanDocCheckbox (the PATCH /api/epics/{taskId}/docs core), then publish
// plan_updated. 200 planView; 400 bad body / negative idx; 404 unknown plan,
// phase, doc or idx; 409 "criterion changed" when text is sent and the doc's
// idx-th criterion no longer reads so (or is gone).
func (h *Handler) linkPlanTick(w http.ResponseWriter, r *http.Request) {
	b, ok := decodeLinkPlanBody(w, r)
	if !ok {
		return
	}
	if b.Idx == nil || b.Done == nil {
		writeClientErr(w, http.StatusBadRequest, "idx and done are required")
		return
	}
	if *b.Idx < 0 {
		writeClientErr(w, http.StatusBadRequest, "idx must be ≥ 0")
		return
	}
	ep, p, ok := h.resolvePlanTarget(w, b, true)
	if !ok {
		return
	}
	idx := *b.Idx
	rel := relToPlan(ep.epic.planDir, p.DocPath)
	_, err := h.flipPlanDocCheckbox(ep.epic.id, rel, *b.Done, func(lines []string) (int, error) {
		crit := wsingest.ListCriteria(strings.Join(lines, "\n"))
		// With the drawn text, a doc that moved under the mod (a line
		// inserted above, a criterion reworded or removed) is a conflict,
		// never a tick of whatever now sits at idx.
		if b.Text != nil && (idx >= len(crit) || strings.TrimSpace(crit[idx].Text) != strings.TrimSpace(*b.Text)) {
			return 0, &planDocRefusal{status: http.StatusConflict, msg: criterionChanged}
		}
		if idx >= len(crit) {
			return 0, &planDocRefusal{status: http.StatusNotFound,
				msg: "no criterion at idx " + strconv.Itoa(idx) + " (the doc has " + strconv.Itoa(len(crit)) + ")"}
		}
		return crit[idx].Line, nil
	})
	if err != nil {
		writePlanDocEditErr(w, err)
		return
	}
	// The scanner folds the new counts in on its next pass and publishes again;
	// this frame makes an open Plans page refetch now, like a run edge does.
	publishPlanUpdated(ep.epic.id)
	writeJSON(w, buildPlanView(r, ep, p.ID, h.chosenByFor(b.Session)), nil)
}

// ── POST /api/link/plan/run ───────────────────────────────────────────────────

// linkPlanRun — POST /api/link/plan/run {session, taskId, phaseId}: start the
// phase run through startPhaseRun, the POST /api/epics/{taskId}/phases/{phaseId}/run
// core — same 202 {status, sessionUuid}, same refusals (409 + code). 400 bad
// body; 404 unknown plan or phase; 503 phase runs not attached.
func (h *Handler) linkPlanRun(w http.ResponseWriter, r *http.Request) {
	if phaserunSvc == nil {
		writeClientErr(w, http.StatusServiceUnavailable, "phase runs not attached")
		return
	}
	b, ok := decodeLinkPlanBody(w, r)
	if !ok {
		return
	}
	_, p, ok := h.resolvePlanTarget(w, b, true)
	if !ok {
		return
	}
	startPhaseRun(w, p.ID, phaserun.StartOptions{})
}

// ── POST /api/link/plan/drift ─────────────────────────────────────────────────

// driftVerdicts is the closed set a drift report's verdict comes from.
var driftVerdicts = map[string]bool{"off-phase": true, "adjacent": true, "on-phase": true}

// driftPayload is the stored link_events payload: the body minus the session.
type driftPayload struct {
	TaskID  string `json:"taskId"`
	PhaseID string `json:"phaseId"`
	Verdict string `json:"verdict"`
	Reason  string `json:"reason"`
}

// linkPlanDrift — POST /api/link/plan/drift {session, taskId, phaseId, verdict,
// reason}: store a link_events row kind drift for the session (reason cut to 200
// characters). 202 {"stored": bool} (false: a report already sits at that
// millisecond); 400 bad body or verdict; 404 unknown session.
func (h *Handler) linkPlanDrift(w http.ResponseWriter, r *http.Request) {
	b, ok := decodeLinkPlanBody(w, r)
	if !ok {
		return
	}
	if b.Session == "" {
		writeClientErr(w, http.StatusBadRequest, "session is required")
		return
	}
	if !driftVerdicts[b.Verdict] {
		writeClientErr(w, http.StatusBadRequest, `verdict must be "off-phase", "adjacent" or "on-phase"`)
		return
	}
	reason := strings.TrimSpace(b.Reason)
	if rs := []rune(reason); len(rs) > maxDriftReason {
		reason = string(rs[:maxDriftReason])
	}
	stored, err := link.RecordDrift(r.Context(), h.DB, b.Session, time.Now(), driftPayload{
		TaskID: string(b.TaskID), PhaseID: string(b.PhaseID), Verdict: b.Verdict, Reason: reason,
	})
	if errors.Is(err, link.ErrUnknownSession) {
		writeClientErr(w, http.StatusNotFound, "unknown session")
		return
	}
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSONStatus(w, http.StatusAccepted, map[string]bool{"stored": stored})
}

// ── GET /api/epics/{taskId}/terminal-sessions (SPA) ───────────────────────────

// terminalSessionDTO is one session that has the plan open in its terminal.
type terminalSessionDTO struct {
	SessionID   int64   `json:"sessionId"`
	SessionUUID string  `json:"sessionUuid"`
	PhaseID     *string `json:"phaseId"`
	ChosenBy    string  `json:"chosenBy"`
	At          string  `json:"at"`
	Live        bool    `json:"live"`
}

// terminalSessions — GET /api/epics/{taskId}/terminal-sessions: the sessions
// whose terminal picked this plan, plus those that fetched it by automatic
// resolution in the last 10 minutes, newest first. A row matches by the
// numeric task id, by one of the task's phases, or by its external id for a
// session of the task's own project (see the query). A session_plan row whose
// session has no sessions row yet is left out (there is no id to link to).
// 200 [] when none; 400 bad id; 404 no such workspace task.
func (h *Handler) terminalSessions(w http.ResponseWriter, r *http.Request) {
	taskID, ok := parseTaskIDParam(w, r)
	if !ok {
		return
	}
	var (
		ext       string
		projectID int64
	)
	err := h.DB.QueryRow(`SELECT COALESCE(external_id, ''), project_id FROM tasks WHERE id = ? AND source = 'workspace'`, taskID).
		Scan(&ext, &projectID)
	if errors.Is(err, sql.ErrNoRows) {
		writeClientErr(w, http.StatusNotFound, "plan not found")
		return
	}
	if err != nil {
		writeErr(w, err)
		return
	}
	numeric := strconv.FormatInt(taskID, 10)
	if ext == "" {
		ext = numeric
	}
	now := time.Now().UTC()
	// session_plan.task_id carries the view's taskId — the external id, which
	// is unique per workspace only. The numeric id is this task's alone. The
	// external id counts for a row pinned to one of this task's phases
	// (epic_phases.id is unique), or for a session of this task's project
	// whose row is not pinned to another plan's phase.
	rows, err := h.DB.Query(`
		SELECT s.id, sp.session_id, sp.phase_id, sp.chosen_by, sp.at, COALESCE(s.link_seen_at, '')
		  FROM session_plan sp
		  JOIN sessions s ON s.session_uuid = sp.session_id
		 WHERE (sp.task_id = ?
		        OR (sp.task_id = ? AND (
		              sp.phase_id IN (SELECT CAST(e.id AS TEXT) FROM epic_phases e WHERE e.workspace_task_id = ?)
		           OR (s.project_id = ? AND NOT EXISTS (SELECT 1 FROM epic_phases o
		                WHERE CAST(o.id AS TEXT) = sp.phase_id AND o.workspace_task_id <> ?)))))
		   AND (sp.chosen_by = 'terminal' OR sp.at >= ?)
		 ORDER BY sp.at DESC, s.id DESC`,
		numeric, ext, taskID, projectID, taskID, now.Add(-planAutoWindow).Format(sessionPlanTS))
	if err != nil {
		writeErr(w, err)
		return
	}
	defer rows.Close()
	out := []terminalSessionDTO{}
	for rows.Next() {
		var (
			d      terminalSessionDTO
			phase  sql.NullString
			seenAt string
		)
		if err := rows.Scan(&d.SessionID, &d.SessionUUID, &phase, &d.ChosenBy, &d.At, &seenAt); err != nil {
			writeErr(w, err)
			return
		}
		if phase.Valid {
			d.PhaseID = &phase.String
		}
		d.Live = ingest.LinkFresh(seenAt, now, linkThresholds)
		out = append(out, d)
	}
	writeJSON(w, out, rows.Err())
}
