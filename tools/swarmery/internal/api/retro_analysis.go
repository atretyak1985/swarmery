package api

// Page-level improvement endpoints: turn the whole retro report into a saved,
// human-gated analysis of the agent system (internal/retroanalysis).
//
// Deliberately NOT part of internal/improve. That service's product is a
// unified diff to exactly one agent definition file, and every part of it —
// the resolver, the base sha, the one-open-proposal invariant, the "minimal
// change" prompt — is built around that. This one's product is prose about
// the whole system, and its gate is a human accepting it. Sharing a table or
// a service between the two would make both worse.
//
// Generation shells out to headless `claude -p` (minutes), so the POST
// validates synchronously, answers 202, and the row carries the outcome —
// the same fire-and-observe pattern as POST /api/retro/*/improve.

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"strconv"
	"strings"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/planning"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/retroanalysis"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/retrodigest"
)

// analysisDigestLimit is the evidence budget for the improver prompt — the
// same 30KB cap internal/improve/bundle.go uses for its own bundle. Not the
// planner's 8000: what has to fit there is the analysis, not its input.
const analysisDigestLimit = reportDigestLimit

// POST /api/retro/analysis — build the report for the requested window, digest
// it, and start a system analysis over it. 202 with the new row; 409 when one
// is already running.
func (h *Handler) startRetroAnalysis(w http.ResponseWriter, r *http.Request) {
	if h.RetroAnalysis == nil {
		writeClientErr(w, http.StatusServiceUnavailable, "analysis not attached")
		return
	}
	dr, err := parseRange(r)
	if err != nil {
		writeClientErr(w, http.StatusBadRequest, err.Error())
		return
	}
	pf, pargs := scopeFilter(r)
	project := r.URL.Query().Get("project")

	rep := h.buildRetroReport(dr, pf, pargs, project)
	digest, _ := retrodigest.Build(reportToDigest(rep), analysisDigestLimit)

	id, err := h.RetroAnalysis.Start(rep.From, rep.To, project, digest, sha256Hex([]byte(digest)))
	switch {
	case errors.Is(err, retroanalysis.ErrAnalysisRunning):
		writeClientErr(w, http.StatusConflict, "an analysis is already running — wait for it to finish")
		return
	case err != nil:
		writeErr(w, err)
		return
	}
	row, err := h.RetroAnalysis.Get(id)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSONStatus(w, http.StatusAccepted, row)
}

// GET /api/retro/analysis?project= — the newest analysis for the scope, or
// null when there is none. The UI polls this while a run is in flight.
func (h *Handler) latestRetroAnalysis(w http.ResponseWriter, r *http.Request) {
	if h.RetroAnalysis == nil {
		writeClientErr(w, http.StatusServiceUnavailable, "analysis not attached")
		return
	}
	row, err := h.RetroAnalysis.Latest(r.URL.Query().Get("project"))
	writeJSON(w, map[string]any{"analysis": row}, err)
}

// PATCH /api/retro/analysis/{id} — the operator's gate. {"status":"accepted"}
// or {"status":"dismissed"}, only from `proposed`.
func (h *Handler) patchRetroAnalysis(w http.ResponseWriter, r *http.Request) {
	if h.RetroAnalysis == nil {
		writeClientErr(w, http.StatusServiceUnavailable, "analysis not attached")
		return
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeClientErr(w, http.StatusBadRequest, "invalid analysis id")
		return
	}
	var body struct {
		Status string `json:"status"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&body); err != nil {
		writeClientErr(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	status := strings.TrimSpace(body.Status)
	if status != "accepted" && status != "dismissed" {
		writeClientErr(w, http.StatusBadRequest, `status must be "accepted" or "dismissed"`)
		return
	}
	row, err := h.RetroAnalysis.Decide(id, status)
	switch {
	case errors.Is(err, retroanalysis.ErrNotFound):
		writeClientErr(w, http.StatusNotFound, "analysis not found")
		return
	case errors.Is(err, retroanalysis.ErrBadTransition):
		writeClientErr(w, http.StatusConflict, err.Error())
		return
	case err != nil:
		writeErr(w, err)
		return
	}
	writeJSON(w, row, nil)
}

// POST /api/retro/analysis/{id}/plan — hand an ACCEPTED analysis to the
// existing Planning Mode as the idea for a normal planning interview.
//
// No new planning engine and no new tables: planning.Service.Start already
// spawns the headless planner, runs the interview, writes the phased plan into
// the private workspace, and wsingest already surfaces it on /plans. All this
// endpoint adds is the gate and the handoff.
func (h *Handler) planFromRetroAnalysis(w http.ResponseWriter, r *http.Request) {
	if h.RetroAnalysis == nil {
		writeClientErr(w, http.StatusServiceUnavailable, "analysis not attached")
		return
	}
	if planningSvc == nil {
		writeClientErr(w, http.StatusServiceUnavailable, "planning not attached")
		return
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeClientErr(w, http.StatusBadRequest, "invalid analysis id")
		return
	}
	var body struct {
		ProjectID int64 `json:"projectId"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&body); err != nil {
		writeClientErr(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if body.ProjectID <= 0 {
		writeClientErr(w, http.StatusBadRequest, "projectId is required — pick the project to plan in")
		return
	}

	row, err := h.RetroAnalysis.Get(id)
	switch {
	case errors.Is(err, retroanalysis.ErrNotFound):
		writeClientErr(w, http.StatusNotFound, "analysis not found")
		return
	case err != nil:
		writeErr(w, err)
		return
	}
	// The SC-5 gate. proposed / dismissed / failed / planned all stop here, and
	// planning.Start is never reached.
	if row.Status != "accepted" {
		writeClientErr(w, http.StatusConflict, planningRefusal(row.Status))
		return
	}

	idea := analysisIdea(row)
	if len(idea) > maxPlanningIdeaLen {
		// Not truncated: cutting a change list mid-sentence would hand the
		// planner a different plan than the one that was accepted. This is a
		// signal that the improver overran its section budget.
		writeClientErr(w, http.StatusUnprocessableEntity, fmt.Sprintf(
			"the accepted analysis makes a %d-byte planning idea, over the %d-byte limit — shorten its %q section and re-run",
			len(idea), maxPlanningIdeaLen, retroanalysis.ChangeSection))
		return
	}

	uuid, err := planningSvc.Start(body.ProjectID, idea, "")
	switch {
	case errors.Is(err, planning.ErrProjectNotFound):
		writeClientErr(w, http.StatusNotFound, "project not found")
		return
	case errors.Is(err, planning.ErrNoPath):
		writeClientErr(w, http.StatusConflict, "project has no known path to plan in")
		return
	case errors.Is(err, planning.ErrActive):
		// Mirrors startPlanning: the body carries the ACTIVE session's uuid so
		// the UI can link to it instead of printing a bare 409.
		writeJSONStatus(w, http.StatusConflict, map[string]any{
			"error":       "a planning run is already active for this project",
			"sessionUuid": planningSvc.Snapshot(body.ProjectID).SessionUUID,
			"projectSlug": h.projectSlug(body.ProjectID),
		})
		return
	case err != nil:
		writeErr(w, err)
		return
	}

	planned, err := h.RetroAnalysis.MarkPlanned(id, uuid)
	if err != nil {
		// The planner IS running — reporting a failure here would be worse than
		// the bookkeeping gap it describes, so say exactly what happened.
		log.Printf("error: retro analysis %d started planning session %s but could not be marked planned: %v", id, uuid, err)
	}
	writeJSONStatus(w, http.StatusAccepted, map[string]any{
		"sessionUuid": uuid,
		"projectSlug": h.projectSlug(body.ProjectID),
		"analysis":    planned,
	})
}

// planningRefusal turns a status into the sentence an operator can act on.
// "409" alone tells them nothing about which button to press next.
func planningRefusal(status string) string {
	switch status {
	case "proposed":
		return "this analysis has not been accepted yet — accept it first"
	case "dismissed":
		return "this analysis was dismissed; run a new one to plan from"
	case "failed":
		return "this analysis failed and has nothing to plan from"
	case "planned":
		return "this analysis already started a planning session"
	case "running":
		return "this analysis is still generating"
	}
	return "this analysis cannot start planning from status " + status
}

// analysisIdea builds the planner's idea from an accepted analysis: a one-line
// provenance header plus the change section verbatim.
//
// Only the change section travels. The full analysis is diagnosis, and the
// planner's interview does not need the argument — it needs the conclusion,
// and the 8000-byte budget is spent better on detail than on restated evidence.
func analysisIdea(a *retroanalysis.Analysis) string {
	scope := "the whole agent fleet"
	if a.Scope != "" {
		scope = "project " + a.Scope
	}
	return fmt.Sprintf(
		"Improve the agent system from the retrospective window %s → %s (%s).\n\n%s\n",
		a.WindowFrom, a.WindowTo, scope, retroanalysis.ChangeIdea(a.Markdown))
}

// projectSlug resolves a project's path-derived slug for the UI's deep link.
// The DB slug (not the pretty name slug) is returned deliberately: it always
// resolves exactly in the SPA's findProject, with no clash rules to replicate
// server-side. Empty when the project vanished — the link is then omitted
// rather than pointing somewhere wrong.
func (h *Handler) projectSlug(projectID int64) string {
	var slug string
	if err := h.DB.QueryRow(`SELECT slug FROM projects WHERE id = ?`, projectID).Scan(&slug); err != nil {
		return ""
	}
	return slug
}
