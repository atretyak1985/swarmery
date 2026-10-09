package api

// Reopen a finished phase (phase-run outcomes plan, phase 1, decision D1):
//
//	POST /api/epics/{taskId}/phases/{phaseId}/reopen  {reason, fixUrl?, caughtBy, criteria: [label]}
//	     → 201 {reopen, unticked}
//	GET  /api/epics/{taskId}/phases/{phaseId}/reopens → 200 {reopens: [reopenDTO], ticked: [label]}
//
// A reopen is the first real "a defect got past the gates" signal: it writes one
// phase_reopens row (the ledger the baseline report counts by caught_by) AND
// unticks the named criteria in the phase doc (wsingest.UntickCriteriaMatched,
// an atomic rewrite), which is what puts the phase back to work. Labels match
// after wsingest's criterion normalisation; GET …/reopens hands the form the
// ticked labels in exactly that form.
//
// Refusals, all before any write: 400 an empty reason, no criteria, an unknown
// caughtBy or a fixUrl that is not http(s); 404 an unknown phase (or one of
// another plan); 409 a phase that is running or still finishing, or not yet
// finished (a criterion is still unticked); 422 when no named criterion is a
// ticked line of the doc, or a label matches more than one ticked line — then
// neither the doc nor the ledger is touched.
//
// The two writes are ordered so a failure leaves nothing half-done: the ledger
// row is inserted inside a transaction, the doc is rewritten atomically, and
// the transaction commits only once the doc is on disk. A failed insert never
// unticks the doc (so a retry still finds the lines ticked); a failed doc write
// rolls the row back.

import (
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/phasereport"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/wsingest"
)

const (
	reopenReasonMax = 4000
	reopenURLMax    = 2048

	// codePhaseNotDone: reopen is for a finished phase; one with an unticked
	// criterion is still open and needs no ledger row to be worked on.
	codePhaseNotDone = "phase-not-done"
)

// isHTTPURL accepts only absolute http(s) URLs — fixUrl is rendered as a link.
func isHTTPURL(s string) bool {
	u, err := url.Parse(s)
	return err == nil && (u.Scheme == "http" || u.Scheme == "https") && u.Host != ""
}

// reopenDTO is one phase_reopens row (camelCase, mirrored in web/src/api/types.ts
// as PhaseReopen).
type reopenDTO struct {
	ID        int64    `json:"id"`
	Reason    string   `json:"reason"`
	FixURL    string   `json:"fixUrl"`
	CaughtBy  string   `json:"caughtBy"`
	Criteria  []string `json:"criteria"`
	CreatedAt string   `json:"createdAt"`
}

// reopenTarget is the phase a reopen route addresses.
type reopenTarget struct {
	DocPath     string
	RunState    string
	Done, Total int
}

// loadReopenTarget reads the phase; ok=false means it already wrote a 404/500.
func (h *Handler) loadReopenTarget(w http.ResponseWriter, taskID, phaseID int64) (reopenTarget, bool) {
	var (
		t        reopenTarget
		wsTaskID int64
	)
	err := h.DB.QueryRow(`SELECT workspace_task_id, doc_path, run_state, checkboxes_done, checkboxes_total
		FROM epic_phases WHERE id = ?`, phaseID).
		Scan(&wsTaskID, &t.DocPath, &t.RunState, &t.Done, &t.Total)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && wsTaskID != taskID) {
		writeClientErr(w, http.StatusNotFound, "phase not found")
		return reopenTarget{}, false
	}
	if err != nil {
		writeErr(w, err)
		return reopenTarget{}, false
	}
	return t, true
}

func (h *Handler) reopenPhase(w http.ResponseWriter, r *http.Request) {
	taskID, phaseID, ok := parseLandingParams(w, r)
	if !ok {
		return
	}
	var body struct {
		Reason   string   `json:"reason"`
		FixURL   string   `json:"fixUrl"`
		CaughtBy string   `json:"caughtBy"`
		Criteria []string `json:"criteria"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&body); err != nil {
		writeClientErr(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	reason := strings.TrimSpace(body.Reason)
	fixURL := strings.TrimSpace(body.FixURL)
	caughtBy := strings.TrimSpace(body.CaughtBy)
	var criteria []string
	for _, c := range body.Criteria {
		if c = strings.TrimSpace(c); c != "" {
			criteria = append(criteria, c)
		}
	}
	switch {
	case reason == "":
		writeClientErr(w, http.StatusBadRequest, "reason is required — say what slipped through")
		return
	case len(reason) > reopenReasonMax:
		writeClientErr(w, http.StatusBadRequest, "reason is too long")
		return
	case len(fixURL) > reopenURLMax:
		writeClientErr(w, http.StatusBadRequest, "fixUrl is too long")
		return
	case fixURL != "" && !isHTTPURL(fixURL):
		writeClientErr(w, http.StatusBadRequest, "fixUrl must be an absolute http(s) URL")
		return
	case len(criteria) == 0:
		writeClientErr(w, http.StatusBadRequest, "name at least one criterion to untick")
		return
	case !slices.Contains(phasereport.CaughtBy, caughtBy):
		writeClientErr(w, http.StatusBadRequest, `caughtBy must be one of "verifier", "review", "operator", "none"`)
		return
	}
	t, ok := h.loadReopenTarget(w, taskID, phaseID)
	if !ok {
		return
	}
	if t.RunState == "running" || (phaserunSvc != nil && phaserunSvc.InFlight(phaseID)) {
		writeConflict(w, codePhaseRunning, "this phase is still running — let the run finish before reopening it")
		return
	}
	if t.Total == 0 || t.Done < t.Total {
		writeConflict(w, codePhaseNotDone, "only a finished phase (every criterion ticked) can be reopened — this one still has open criteria")
		return
	}
	pending, err := wsingest.PrepareUntick(t.DocPath, criteria)
	switch {
	case err == nil:
	case os.IsNotExist(err):
		writeClientErr(w, http.StatusNotFound, "phase doc not found")
		return
	case errors.Is(err, wsingest.ErrAmbiguousCriteria):
		writeClientErr(w, http.StatusUnprocessableEntity, err.Error()+" — untick it in the doc by hand")
		return
	default:
		writeErr(w, err)
		return
	}
	matched := pending.Matched
	if len(matched) == 0 {
		writeClientErr(w, http.StatusUnprocessableEntity,
			"none of the named criteria is a ticked criterion of this phase's doc — nothing was reopened")
		return
	}
	labels, _ := json.Marshal(matched)
	now := time.Now().UTC().Format(time.RFC3339)
	tx, err := h.DB.Begin()
	if err != nil {
		writeErr(w, err)
		return
	}
	res, err := tx.Exec(`INSERT INTO phase_reopens
		(phase_id, workspace_task_id, doc_path, reason, fix_url, caught_by, criteria_json, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		phaseID, taskID, t.DocPath, reason, fixURL, caughtBy, string(labels), now)
	if err != nil {
		_ = tx.Rollback()
		writeErr(w, err)
		return
	}
	if err := pending.Commit(); err != nil {
		_ = tx.Rollback()
		writeErr(w, err)
		return
	}
	if err := tx.Commit(); err != nil {
		writeErr(w, err)
		return
	}
	id, _ := res.LastInsertId()
	publishPlanUpdated(taskID)
	writeJSONStatus(w, http.StatusCreated, map[string]any{
		"reopen": reopenDTO{
			ID: id, Reason: reason, FixURL: fixURL, CaughtBy: caughtBy, Criteria: matched, CreatedAt: now,
		},
		"unticked": len(matched),
	})
}

func (h *Handler) listPhaseReopens(w http.ResponseWriter, r *http.Request) {
	taskID, phaseID, ok := parseLandingParams(w, r)
	if !ok {
		return
	}
	t, ok := h.loadReopenTarget(w, taskID, phaseID)
	if !ok {
		return
	}
	reopens := h.phaseReopens(taskID, map[int64]string{phaseID: t.DocPath})[phaseID]
	if reopens == nil {
		reopens = []reopenDTO{}
	}
	ticked := []string{}
	if body, err := os.ReadFile(t.DocPath); err == nil {
		if l := wsingest.TickedCriteriaLabels(string(body)); l != nil {
			ticked = l
		}
	}
	writeJSON(w, map[string]any{"reopens": reopens, "ticked": ticked}, nil)
}

// phaseReopens returns, per phase id, the plan's reopens, oldest first. A row
// whose phase_id is no longer one of the given phases (the doc was renamed, so
// its epic_phases row was re-inserted) is matched by doc_path instead.
//
// Like phaseForecasts it degrades to an empty map on error: the ledger decorates
// the Plans page and must never take it down.
func (h *Handler) phaseReopens(taskID int64, docs map[int64]string) map[int64][]reopenDTO {
	out := map[int64][]reopenDTO{}
	byDoc := map[string]int64{}
	for id, doc := range docs {
		byDoc[doc] = id
	}
	rows, err := h.DB.Query(`
		SELECT id, phase_id, doc_path, reason, fix_url, caught_by, criteria_json, created_at
		  FROM phase_reopens WHERE workspace_task_id = ? ORDER BY id`, taskID)
	if err != nil {
		return out
	}
	defer rows.Close()
	for rows.Next() {
		var (
			d        reopenDTO
			phaseID  int64
			doc, raw string
		)
		if err := rows.Scan(&d.ID, &phaseID, &doc, &d.Reason, &d.FixURL, &d.CaughtBy, &raw, &d.CreatedAt); err != nil {
			return map[int64][]reopenDTO{}
		}
		if err := json.Unmarshal([]byte(raw), &d.Criteria); err != nil || d.Criteria == nil {
			d.Criteria = []string{}
		}
		if _, known := docs[phaseID]; !known {
			id, found := byDoc[doc]
			if !found {
				continue
			}
			phaseID = id
		}
		out[phaseID] = append(out[phaseID], d)
	}
	return out
}
