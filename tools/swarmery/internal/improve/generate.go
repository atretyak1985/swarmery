package improve

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log"
	"time"
)

// errNoChangeRow is the error column text of an ErrNoChange outcome.
const errNoChangeRow = "model found no justified change"

// OpenProposalID returns the id of the agent's open (proposed|approved)
// proposal, or 0 when none exists — the code-level half of the
// one-open-proposal invariant (migration 0021 keeps no partial index).
func (s *Service) OpenProposalID(agent string) (int64, error) {
	var id int64
	err := s.DB.QueryRow(`
		SELECT id FROM agent_change_proposals
		 WHERE agent = ? AND status IN ('proposed','approved')
		 ORDER BY id DESC LIMIT 1`, agent).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	return id, err
}

// AgentInRegistry reports whether the (normalized) agent key resolves to a
// live registry row.
func (s *Service) AgentInRegistry(agent string) (bool, error) {
	_, err := resolveAgent(s.DB, agent)
	if errors.Is(err, ErrAgentNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}

// Generate runs the full pipeline for one agent: evidence bundle → prompt →
// runner → parse → row. Pre-flight failures (unknown agent, open proposal,
// DB errors) return a typed error and write nothing; pipeline failures
// (runner error, no-change answer, contract violation) are CAPTURED as a
// row with status='failed' — the returned error is nil then, the row is the
// outcome.
func (s *Service) Generate(ctx context.Context, req GenerateReq) (int64, error) {
	ev, err := buildEvidence(s.DB, req.Agent, s.Repo)
	if err != nil {
		return 0, err
	}
	if open, err := s.OpenProposalID(req.Agent); err != nil {
		return 0, err
	} else if open != 0 {
		return 0, fmt.Errorf("%w (proposal %d)", ErrOpenProposal, open)
	}

	diff, rationale, runErr := s.run(ctx, ev)
	return s.insertOutcome(req, ev, diff, rationale, runErr)
}

// run executes the runner + output-contract parse, folding every failure
// into one error the caller stores on the row.
func (s *Service) run(ctx context.Context, ev *Evidence) (diff, rationale string, err error) {
	out, err := s.Runner.Run(ctx, renderPrompt(ev.AgentPath, ev.AgentContent, ev.Bundle))
	if err != nil {
		return "", "", err
	}
	diff, rationale, err = splitDiffRationale(out)
	if errors.Is(err, ErrNoChange) {
		return "", "", errors.New(errNoChangeRow)
	}
	if err != nil {
		return "", "", fmt.Errorf("output contract: %w", err)
	}
	return diff, rationale, nil
}

// insertOutcome persists one pipeline outcome: proposed on success, failed
// (with the error text) otherwise.
func (s *Service) insertOutcome(req GenerateReq, ev *Evidence, diff, rationale string, runErr error) (int64, error) {
	status, errCol := "proposed", any(nil)
	if runErr != nil {
		status, errCol = "failed", runErr.Error()
		diff, rationale = "", ""
		log.Printf("warn: improve: agent %s: %v", req.Agent, runErr)
	}
	var recID any
	if req.RecommendationID != nil {
		recID = *req.RecommendationID
	}
	res, err := s.DB.Exec(`
		INSERT INTO agent_change_proposals
			(recommendation_id, agent, agent_path, base_sha256, diff, rationale, status, error, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		recID, req.Agent, ev.AgentPath, ev.BaseSHA256, diff, rationale, status, errCol, fmtTS(time.Now()))
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// Retry re-runs the pipeline for a failed proposal, updating the row in
// place: success flips failed → proposed with a fresh diff/rationale/base
// SHA; another failure refreshes the error text. Only 'failed' rows are
// retriable; an open proposal for the same agent (created since the
// failure) blocks the retry.
func (s *Service) Retry(ctx context.Context, id int64) error {
	var agent, status string
	err := s.DB.QueryRow(
		`SELECT agent, status FROM agent_change_proposals WHERE id = ?`, id).
		Scan(&agent, &status)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrProposalNotFound
	}
	if err != nil {
		return err
	}
	if status != "failed" {
		return fmt.Errorf("%w (status %s)", ErrNotRetriable, status)
	}
	if open, err := s.OpenProposalID(agent); err != nil {
		return err
	} else if open != 0 {
		return fmt.Errorf("%w (proposal %d)", ErrOpenProposal, open)
	}

	ev, err := buildEvidence(s.DB, agent, s.Repo)
	if err != nil {
		return err
	}
	diff, rationale, runErr := s.run(ctx, ev)
	if runErr != nil {
		log.Printf("warn: improve: retry %d (agent %s): %v", id, agent, runErr)
		_, err = s.DB.Exec(`
			UPDATE agent_change_proposals
			   SET agent_path = ?, base_sha256 = ?, error = ? WHERE id = ?`,
			ev.AgentPath, ev.BaseSHA256, runErr.Error(), id)
		return err
	}
	_, err = s.DB.Exec(`
		UPDATE agent_change_proposals
		   SET agent_path = ?, base_sha256 = ?, diff = ?, rationale = ?,
		       status = 'proposed', error = NULL
		 WHERE id = ? AND status = 'failed'`,
		ev.AgentPath, ev.BaseSHA256, diff, rationale, id)
	return err
}
