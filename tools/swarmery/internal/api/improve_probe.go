package api

// Proposal probe endpoints (harness² phase 4, internal/probe). The POST
// validates synchronously — 404 unknown proposal, 422 a proposal that is not
// proposed/approved, 409 a probe already running for it — then runs the probe
// as a background job and answers 202 with the job. The GET lists the stored
// rows plus this daemon's jobs, so the card can show "running" between polls.
//
// Advisory: neither endpoint changes the proposal. Approving, applying and
// rejecting stay on /api/retro/proposals/{id}.

import (
	"database/sql"
	"errors"
	"net/http"
	"strconv"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/probe"
)

// probeID parses {id}; ok=false has already answered 400.
func probeID(w http.ResponseWriter, r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		writeClientErr(w, http.StatusBadRequest, "invalid proposal id")
		return 0, false
	}
	return id, true
}

// POST /api/improve/proposals/{id}/probe
func (h *Handler) startProposalProbe(w http.ResponseWriter, r *http.Request) {
	if h.ProposalProbe == nil {
		writeClientErr(w, http.StatusServiceUnavailable, "proposal probe is not attached")
		return
	}
	id, ok := probeID(w, r)
	if !ok {
		return
	}
	job, err := h.ProposalProbe.Start(id)
	switch {
	case errors.Is(err, probe.ErrProposalNotFound):
		writeClientErr(w, http.StatusNotFound, "proposal not found")
	case errors.Is(err, probe.ErrNotProbeable):
		writeClientErr(w, http.StatusUnprocessableEntity, err.Error())
	case errors.Is(err, probe.ErrJobRunning):
		writeJSONStatus(w, http.StatusConflict, map[string]any{"error": err.Error(), "job": job})
	case err != nil:
		writeErr(w, err)
	default:
		writeJSONStatus(w, http.StatusAccepted, map[string]any{"job": job})
	}
}

type proposalProbesDTO struct {
	ProposalID int64       `json:"proposal_id"`
	MaxUSD     float64     `json:"max_usd"`
	Jobs       []probe.Job `json:"jobs"`
	Probes     []probe.Row `json:"probes"`
}

// GET /api/improve/proposals/{id}/probes — read-only, no origin fence.
func (h *Handler) listProposalProbes(w http.ResponseWriter, r *http.Request) {
	id, ok := probeID(w, r)
	if !ok {
		return
	}
	var one int
	if err := h.rdb().QueryRow(`SELECT 1 FROM agent_change_proposals WHERE id = ?`, id).Scan(&one); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeClientErr(w, http.StatusNotFound, "proposal not found")
			return
		}
		writeErr(w, err)
		return
	}
	rows, err := probe.ListRows(h.rdb(), id)
	if err != nil {
		writeErr(w, err)
		return
	}
	out := proposalProbesDTO{ProposalID: id, MaxUSD: probe.MaxUSD(), Jobs: []probe.Job{}, Probes: rows}
	if h.ProposalProbe != nil {
		out.Jobs = h.ProposalProbe.Jobs(id)
		if h.ProposalProbe.MaxUSD > 0 {
			out.MaxUSD = h.ProposalProbe.MaxUSD
		}
	}
	writeJSON(w, out, nil)
}
