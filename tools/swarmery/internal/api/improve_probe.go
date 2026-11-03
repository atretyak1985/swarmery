package api

// Proposal probe endpoints (harness² phases 4 and 6, internal/probe). The POST
// validates synchronously — 400 an unknown ?source, 404 unknown proposal, 422 a
// proposal that is not proposed/approved, 409 a probe already running for it —
// then runs the probe as a background job and answers 202 with the job.
// ?source=promptfoo (the default) runs the agent's promptfoo suite;
// ?source=bench runs the frozen bench under its own cap. The GET lists the
// stored rows plus this daemon's jobs, so the card can show "running" between
// polls.
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

// POST /api/improve/proposals/{id}/probe[?source=promptfoo|bench]
func (h *Handler) startProposalProbe(w http.ResponseWriter, r *http.Request) {
	if h.ProposalProbe == nil {
		writeClientErr(w, http.StatusServiceUnavailable, "proposal probe is not attached")
		return
	}
	id, ok := probeID(w, r)
	if !ok {
		return
	}
	source := r.URL.Query().Get("source")
	if source == "" {
		source = probe.SourcePromptfoo
	}
	job, err := h.ProposalProbe.StartSource(id, source)
	switch {
	case errors.Is(err, probe.ErrUnknownSource):
		writeClientErr(w, http.StatusBadRequest, err.Error())
	case errors.Is(err, probe.ErrBenchUnattached):
		writeClientErr(w, http.StatusServiceUnavailable, err.Error())
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
	ProposalID  int64       `json:"proposal_id"`
	MaxUSD      float64     `json:"max_usd"`       // the promptfoo step's cap
	BenchMaxUSD float64     `json:"bench_max_usd"` // the bench step's cap
	Jobs        []probe.Job `json:"jobs"`
	Probes      []probe.Row `json:"probes"`
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
	out := proposalProbesDTO{ProposalID: id, MaxUSD: probe.MaxUSD(), BenchMaxUSD: probe.BenchMaxUSD(),
		Jobs: []probe.Job{}, Probes: rows}
	if h.ProposalProbe != nil {
		out.Jobs = h.ProposalProbe.Jobs(id)
		out.MaxUSD = h.ProposalProbe.CapUSD(probe.SourcePromptfoo)
		out.BenchMaxUSD = h.ProposalProbe.CapUSD(probe.SourceBench)
	}
	writeJSON(w, out, nil)
}
