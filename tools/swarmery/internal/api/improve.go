package api

// Agent-rewriter endpoints (self-improvement phase 3, internal/improve):
// trigger proposal generation from an accepted agent-kind recommendation or
// ad hoc per agent, list proposals, retry failed ones. Generation shells out
// to headless `claude -p` (minutes), so the POSTs validate synchronously —
// 404/409/422 come back immediately — then run the pipeline async and answer
// 202; pipeline failures land as rows with status='failed' (the same
// fire-and-observe daemon pattern as POST /api/retro/advise, which runs the
// deterministic engine inline because it is fast).

import (
	"context"
	"database/sql"
	"errors"
	"log"
	"net/http"
	"strings"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/advisor"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/improve"
)

// spawnImprove runs one generation pipeline asynchronously; the improveGo
// seam (nil in production) lets tests run it inline for determinism.
func (h *Handler) spawnImprove(fn func()) {
	if h.improveGo != nil {
		h.improveGo(fn)
		return
	}
	go fn()
}

// improveAccepted is the shared tail of both improve triggers: open-proposal
// dedup (409) + async Generate + 202 envelope.
func (h *Handler) improveAccepted(w http.ResponseWriter, agent string, recID *int64) {
	open, err := h.Improve.OpenProposalID(agent)
	if err != nil {
		writeErr(w, err)
		return
	}
	if open != 0 {
		writeJSONStatus(w, http.StatusConflict, map[string]any{
			"error":       "an open proposal already exists for agent " + agent,
			"proposal_id": open,
		})
		return
	}
	h.spawnImprove(func() {
		if _, err := h.Improve.Generate(context.Background(), improve.GenerateReq{
			Agent: agent, RecommendationID: recID,
		}); err != nil {
			log.Printf("error: improve: generate for agent %s: %v", agent, err)
		}
	})
	writeJSONStatus(w, http.StatusAccepted, map[string]string{
		"status": "generating", "agent": agent,
	})
}

// POST /api/retro/recommendations/{id}/improve — generate a proposal for an
// ACCEPTED agent-kind recommendation. 404 unknown id; 422 wrong status/kind
// or target absent from the registry; 409 open proposal exists.
func (h *Handler) improveRecommendation(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var recID int64
	var targetKind, target, status string
	err := h.DB.QueryRow(
		`SELECT id, target_kind, target, status FROM recommendations WHERE id = ?`, id).
		Scan(&recID, &targetKind, &target, &status)
	if errors.Is(err, sql.ErrNoRows) {
		writeClientErr(w, http.StatusNotFound, "recommendation not found")
		return
	}
	if err != nil {
		writeErr(w, err)
		return
	}
	if status != "accepted" || targetKind != "agent" {
		writeClientErr(w, http.StatusUnprocessableEntity,
			"recommendation must be accepted and agent-kind (got "+status+"/"+targetKind+")")
		return
	}
	agent := advisor.NormAgent(target)
	ok, err := h.Improve.AgentInRegistry(agent)
	if err != nil {
		writeErr(w, err)
		return
	}
	if !ok {
		writeClientErr(w, http.StatusUnprocessableEntity,
			"agent "+agent+" not found in registry")
		return
	}
	h.improveAccepted(w, agent, &recID)
}

// POST /api/retro/agents/{agent}/improve — the ad-hoc trigger, same pipeline
// with recommendation_id NULL. 404 when the agent is not in the registry.
func (h *Handler) improveAgent(w http.ResponseWriter, r *http.Request) {
	agent := advisor.NormAgent(r.PathValue("agent"))
	ok, err := h.Improve.AgentInRegistry(agent)
	if err != nil {
		writeErr(w, err)
		return
	}
	if !ok {
		writeClientErr(w, http.StatusNotFound, "agent "+agent+" not found in registry")
		return
	}
	h.improveAccepted(w, agent, nil)
}

type proposalDTO struct {
	ID               int64   `json:"id"`
	RecommendationID *int64  `json:"recommendation_id"`
	Agent            string  `json:"agent"`
	AgentPath        string  `json:"agent_path"`
	BaseSHA256       string  `json:"base_sha256"`
	Diff             string  `json:"diff"`
	Rationale        string  `json:"rationale"`
	Status           string  `json:"status"`
	Error            *string `json:"error"`
	PRURL            *string `json:"pr_url"`
	CreatedAt        string  `json:"created_at"`
	DecidedAt        *string `json:"decided_at"`
}

type proposalsDTO struct {
	Proposals []proposalDTO `json:"proposals"`
}

// propStatuses is the closed status vocabulary of migration 0021.
var propStatuses = map[string]bool{
	"proposed": true, "approved": true, "applied": true,
	"rejected": true, "failed": true,
}

// GET /api/retro/proposals?status=proposed,failed — newest first; no filter
// returns everything.
func (h *Handler) listProposals(w http.ResponseWriter, r *http.Request) {
	q := `SELECT id, recommendation_id, agent, agent_path, base_sha256, diff,
	             rationale, status, error, pr_url, created_at, decided_at
	        FROM agent_change_proposals`
	var args []any
	if filter := r.URL.Query().Get("status"); filter != "" {
		parts := strings.Split(filter, ",")
		ph := make([]string, 0, len(parts))
		for _, p := range parts {
			p = strings.TrimSpace(p)
			if !propStatuses[p] {
				writeClientErr(w, http.StatusBadRequest, "unknown status "+p)
				return
			}
			ph = append(ph, "?")
			args = append(args, p)
		}
		q += ` WHERE status IN (` + strings.Join(ph, ",") + `)`
	}
	q += ` ORDER BY id DESC`

	rows, err := h.DB.Query(q, args...)
	if err != nil {
		writeErr(w, err)
		return
	}
	defer rows.Close()
	out := proposalsDTO{Proposals: []proposalDTO{}}
	for rows.Next() {
		var d proposalDTO
		if err := rows.Scan(&d.ID, &d.RecommendationID, &d.Agent, &d.AgentPath,
			&d.BaseSHA256, &d.Diff, &d.Rationale, &d.Status, &d.Error, &d.PRURL,
			&d.CreatedAt, &d.DecidedAt); err != nil {
			writeErr(w, err)
			return
		}
		out.Proposals = append(out.Proposals, d)
	}
	writeJSON(w, out, rows.Err())
}

// POST /api/retro/proposals/{id}/retry — re-run generation for a FAILED
// proposal. 404 unknown id; 422 not failed; 409 an open proposal for the
// same agent appeared since the failure.
func (h *Handler) retryProposal(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var rowID int64
	var agent, status string
	err := h.DB.QueryRow(
		`SELECT id, agent, status FROM agent_change_proposals WHERE id = ?`, id).
		Scan(&rowID, &agent, &status)
	if errors.Is(err, sql.ErrNoRows) {
		writeClientErr(w, http.StatusNotFound, "proposal not found")
		return
	}
	if err != nil {
		writeErr(w, err)
		return
	}
	if status != "failed" {
		writeClientErr(w, http.StatusUnprocessableEntity,
			"only failed proposals can be retried (status "+status+")")
		return
	}
	open, err := h.Improve.OpenProposalID(agent)
	if err != nil {
		writeErr(w, err)
		return
	}
	if open != 0 {
		writeJSONStatus(w, http.StatusConflict, map[string]any{
			"error":       "an open proposal already exists for agent " + agent,
			"proposal_id": open,
		})
		return
	}
	h.spawnImprove(func() {
		if err := h.Improve.Retry(context.Background(), rowID); err != nil {
			log.Printf("error: improve: retry %d: %v", rowID, err)
		}
	})
	writeJSONStatus(w, http.StatusAccepted, map[string]any{
		"status": "generating", "id": rowID, "agent": agent,
	})
}
