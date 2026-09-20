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
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"path/filepath"
	"strings"
	"time"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/advisor"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/improve"
)

// improveRepoRoot is the git checkout the apply/PR pipeline fetches and
// worktrees from — the marketplace clone Claude Code keeps under
// <claudeDir>/plugins/marketplaces/swarmery. Attached once at startup; empty
// disables the apply pipeline's git ops (generation still works). Mirrors
// AttachPluginCatalog (project_plugins.go:36).
var improveRepoRoot string

// AttachImproveRepo points the apply/PR pipeline at the marketplace clone under
// claudeDir. Call with the same resolved --claude-dir the sys scanner uses.
//
// DELIBERATELY NOT RESOLVED through internal/marketplace's catalogRoot, even
// though that function now reads the same marketplace's real location out of
// plugins/known_marketplaces.json. The divergence is the point, not an
// oversight — do not "fix the inconsistency".
//
// internal/marketplace is a READER: the worst a wrong root costs it is a stale
// or missing catalog. This root is a WRITE target. internal/improve/apply.go
// runs `git fetch origin main` (apply.go:137), `git worktree add -f <tmp>
// origin/main -b <branch>` (:140), `git commit` (:209) and `git push
// --force-with-lease -u origin <branch>` (:212) against it.
//
// For a directory-source marketplace, installLocation is the operator's LIVE
// DEVELOPMENT CHECKOUT (e.g. ~/projects/<repo>) — not a throwaway clone.
// Repointing this variable would aim branch creation, worktree add and a
// force-push at the tree the operator is working in. Left hardcoded, the path
// simply does not exist on such a machine and every git op fails harmlessly,
// which is the safe failure mode.
func AttachImproveRepo(claudeDir string) {
	if claudeDir == "" {
		return
	}
	improveRepoRoot = filepath.Join(claudeDir, "plugins", "marketplaces", pluginMarketplace)
}

// spawnImprove runs one pipeline asynchronously; the improveGo seam (nil in
// production) lets tests run it inline for determinism. label names the
// proposal/agent the closure operates on, so a recovered panic can be
// correlated to the (possibly wedged) row.
func (h *Handler) spawnImprove(label string, fn func()) {
	// A panic in the long-running Generate/Apply pipeline must never take the
	// daemon down — recover, log (with the row label so the wedged proposal is
	// identifiable), and let the row stay in whatever state it reached (a stuck
	// 'approved'/'proposed' is re-runnable from the dashboard).
	wrapped := func() {
		defer func() {
			if r := recover(); r != nil {
				log.Printf("error: improve: async pipeline panic recovered (%s): %v", label, r)
			}
		}()
		fn()
	}
	if h.improveGo != nil {
		h.improveGo(wrapped)
		return
	}
	go wrapped()
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
	h.spawnImprove("generate agent "+agent, func() {
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
	if status != "accepted" {
		writeClientErr(w, http.StatusUnprocessableEntity,
			"recommendation must be accepted (got "+status+")")
		return
	}
	if targetKind == improve.TargetSkill {
		h.improveSkillRecommendation(w, recID, target)
		return
	}
	if targetKind != improve.TargetAgent {
		writeClientErr(w, http.StatusUnprocessableEntity,
			"recommendation must be agent- or skill-kind (got "+targetKind+")")
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

// improveSkillRecommendation routes an accepted R11 (skill-kind)
// recommendation into a SKILL.md proposal. The recommendation's `target` IS the
// lesson identity (retro_lessons.norm_title) — that is what R11 puts there — so
// no registry lookup applies: the target FILE is resolved from the lesson's
// latest action inside improve.RouteSkill, which falls back to a needs_target
// row rather than guessing. Both outcomes answer 202; the row on the Retro page
// is the result.
func (h *Handler) improveSkillRecommendation(w http.ResponseWriter, recID int64, norm string) {
	if strings.TrimSpace(norm) == "" {
		writeClientErr(w, http.StatusUnprocessableEntity,
			"skill recommendation has no lesson identity (empty target)")
		return
	}
	open, err := h.Improve.OpenLessonProposalID(norm)
	if err != nil {
		writeErr(w, err)
		return
	}
	if open != 0 {
		writeJSONStatus(w, http.StatusConflict, map[string]any{
			"error":       "an open proposal already exists for lesson " + norm,
			"proposal_id": open,
		})
		return
	}
	h.spawnImprove("route skill lesson "+norm, func() {
		if _, err := h.Improve.RouteSkill(context.Background(), improve.SkillRouteReq{
			NormTitle: norm, RecommendationID: &recID,
		}); err != nil {
			log.Printf("error: improve: route skill lesson %s: %v", norm, err)
		}
	})
	writeJSONStatus(w, http.StatusAccepted, map[string]string{
		"status": "generating", "target_kind": improve.TargetSkill, "lesson": norm,
	})
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

// GET /api/retro/agents/{agent}/evidence — READ-ONLY preview of the evidence
// bundle the rewriter would feed the model, so the dashboard can show it before
// the user triggers a (minutes-long) generation. No requireLocalOrigin — it
// mutates nothing. A built-in agent (no editable registry row) answers 200
// {"in_registry":false}; a registered one returns the path, base SHA and the
// full bundle (never AgentContent).
func (h *Handler) agentEvidence(w http.ResponseWriter, r *http.Request) {
	agent := advisor.NormAgent(r.PathValue("agent"))
	ev, err := h.Improve.Evidence(agent)
	if errors.Is(err, improve.ErrAgentNotFound) {
		writeJSON(w, map[string]any{"agent": agent, "in_registry": false}, nil)
		return
	}
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, map[string]any{
		"agent":       agent,
		"in_registry": true,
		"agent_path":  ev.AgentPath,
		"base_sha256": ev.BaseSHA256,
		"bundle":      ev.Bundle,
	}, nil)
}

type proposalDTO struct {
	ID               int64   `json:"id"`
	RecommendationID *int64  `json:"recommendation_id"`
	Agent            string  `json:"agent"`
	AgentPath        string  `json:"agent_path"`
	TargetKind       string  `json:"target_kind"`
	TargetPath       string  `json:"target_path"`
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

// propStatuses is the closed status vocabulary of migrations 0021 + 0074
// ('needs_target': a routed skill recommendation whose SKILL.md is not resolved
// yet and which an operator has to point at a file).
var propStatuses = map[string]bool{
	"proposed": true, "approved": true, "applied": true,
	"rejected": true, "failed": true, improve.StatusNeedsTarget: true,
}

// GET /api/retro/proposals?status=proposed,failed — newest first; no filter
// returns everything.
func (h *Handler) listProposals(w http.ResponseWriter, r *http.Request) {
	q := `SELECT id, recommendation_id, agent, agent_path, target_kind, target_path,
	             base_sha256, diff, rationale, status, error, pr_url, created_at, decided_at
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
			&d.TargetKind, &d.TargetPath,
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
// same TARGET appeared since the failure.
//
// The pre-flight must compute the target key exactly the way improve.Retry
// does, because Retry's own ErrOpenProposal is only logged — the HTTP reply has
// already been written by then. A skill row's `agent` column holds the LESSON
// SENTENCE, not a file, so the old agent-keyed pre-flight never matched a skill
// row: Retry answered 202 "generating", failed silently on the open-proposal
// check, and the row stayed `failed` with no reason on screen.
func (h *Handler) retryProposal(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var rowID int64
	var agent, targetKind, targetPath, status string
	err := h.DB.QueryRow(
		`SELECT id, agent, target_kind, target_path, status
		   FROM agent_change_proposals WHERE id = ?`, id).
		Scan(&rowID, &agent, &targetKind, &targetPath, &status)
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
	// Same key improve.Retry computes: the SKILL.md for a resolved skill row,
	// the agent key otherwise. Sharing the computation is the point — a
	// pre-flight that can disagree with the real check is not a pre-flight.
	key := agent
	subject := "agent " + agent
	if targetKind == improve.TargetSkill && targetPath != "" {
		key = targetPath
		subject = "skill " + targetPath
	}
	open, err := h.Improve.OpenTargetProposalID(targetKind, key)
	if err != nil {
		writeErr(w, err)
		return
	}
	if open != 0 {
		writeJSONStatus(w, http.StatusConflict, map[string]any{
			"error":       "an open proposal already exists for " + subject,
			"proposal_id": open,
		})
		return
	}
	h.spawnImprove(fmt.Sprintf("retry proposal %d (%s)", rowID, subject), func() {
		if err := h.Improve.Retry(context.Background(), rowID); err != nil {
			log.Printf("error: improve: retry %d: %v", rowID, err)
		}
	})
	writeJSONStatus(w, http.StatusAccepted, map[string]any{
		"status": "generating", "id": rowID, "agent": agent,
	})
}

// legalProposalTransition guards the human decision on a proposal: a proposed
// row may be approved or rejected; nothing else is a PATCH (applied/failed are
// pipeline outcomes, retry has its own endpoint).
//
// A needs_target row may be REJECTED but never approved. Rejecting is the
// operator saying "this lesson is not a skill edit" — without it the row would
// hold its target's one-open slot forever, because it has no file to apply and
// no failure to retry. Approving is refused for the same reason: there is
// nothing to apply.
func legalProposalTransition(from, to string) bool {
	if from == improve.StatusNeedsTarget {
		return to == "rejected"
	}
	return from == "proposed" && (to == "approved" || to == "rejected")
}

// spawnApply fires the async apply/PR pipeline for an approved proposal,
// reusing the improveGo test seam so httptest runs it inline.
func (h *Handler) spawnApply(id int64) {
	h.spawnImprove(fmt.Sprintf("apply proposal %d", id), func() {
		if err := h.Improve.Apply(context.Background(), id); err != nil {
			log.Printf("error: improve: apply %d: %v", id, err)
		}
	})
}

// PATCH /api/retro/proposals/{id} — body {"status":"approved"|"rejected"}.
// 422 unless the current status is 'proposed' (mirrors the recommendations
// PATCH). Approving stamps decided_at and fires the apply/PR pipeline async;
// rejecting only stamps decided_at.
func (h *Handler) patchProposal(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var body struct {
		Status string `json:"status"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeClientErr(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if body.Status != "approved" && body.Status != "rejected" {
		writeClientErr(w, http.StatusUnprocessableEntity,
			"status must be approved or rejected")
		return
	}

	var rowID int64
	var status string
	err := h.DB.QueryRow(
		`SELECT id, status FROM agent_change_proposals WHERE id = ?`, id).
		Scan(&rowID, &status)
	if errors.Is(err, sql.ErrNoRows) {
		writeClientErr(w, http.StatusNotFound, "proposal not found")
		return
	}
	if err != nil {
		writeErr(w, err)
		return
	}
	if !legalProposalTransition(status, body.Status) {
		writeClientErr(w, http.StatusUnprocessableEntity,
			"illegal transition "+status+" -> "+body.Status)
		return
	}

	now := time.Now().UTC().Format("2006-01-02T15:04:05.000Z")
	// Guarded write: the status predicate re-checks the value we validated
	// against, so a concurrent decision can't be silently overwritten.
	res, err := h.DB.Exec(`
		UPDATE agent_change_proposals SET status = ?, decided_at = ?
		 WHERE id = ? AND status = ?`, body.Status, now, rowID, status)
	if err != nil {
		writeErr(w, err)
		return
	}
	if n, aerr := res.RowsAffected(); aerr != nil {
		writeErr(w, aerr)
		return
	} else if n == 0 {
		var cur string
		if rerr := h.DB.QueryRow(
			`SELECT status FROM agent_change_proposals WHERE id = ?`, id).Scan(&cur); rerr != nil {
			writeErr(w, rerr)
			return
		}
		writeJSONStatus(w, http.StatusConflict, map[string]string{
			"error":  "status changed concurrently: now " + cur,
			"status": cur,
		})
		return
	}

	if body.Status == "approved" {
		h.spawnApply(rowID)
	}
	writeJSONStatus(w, http.StatusOK, map[string]any{
		"id": rowID, "status": body.Status,
	})
}

// POST /api/retro/proposals/{id}/apply — manual re-run of the apply/PR
// pipeline for a proposal stuck in 'approved' (e.g. a prior gh outage). 404
// unknown id; 422 unless the row is currently 'approved'.
func (h *Handler) applyProposal(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var rowID int64
	var status string
	err := h.DB.QueryRow(
		`SELECT id, status FROM agent_change_proposals WHERE id = ?`, id).
		Scan(&rowID, &status)
	if errors.Is(err, sql.ErrNoRows) {
		writeClientErr(w, http.StatusNotFound, "proposal not found")
		return
	}
	if err != nil {
		writeErr(w, err)
		return
	}
	if status != "approved" {
		writeClientErr(w, http.StatusUnprocessableEntity,
			"only approved proposals can be applied (status "+status+")")
		return
	}
	h.spawnApply(rowID)
	writeJSONStatus(w, http.StatusAccepted, map[string]any{
		"status": "applying", "id": rowID,
	})
}
