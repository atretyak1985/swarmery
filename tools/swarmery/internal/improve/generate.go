package improve

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log"
	"strings"
	"time"
)

// errNoChangeRow is the error column text of an ErrNoChange outcome.
const errNoChangeRow = "model found no justified change"

// OpenProposalID returns the id of the agent's open (proposed|approved)
// proposal, or 0 when none exists — the code-level half of the
// one-open-proposal invariant (migration 0022 adds the partial unique index
// that enforces the DB-level half).
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

// Generate runs the full pipeline for one agent: evidence bundle → placeholder
// row → prompt → runner → parse → in-place update. Pre-flight failures
// (unknown agent, open proposal, DB errors) return a typed error; pipeline
// failures (runner error, no-change answer, contract violation) are CAPTURED on
// the row as status='failed' — the returned error is nil then, the row is the
// outcome.
//
// The one-open-proposal invariant is enforced by INSERTing a placeholder
// 'proposed' row up front, before the (minutes-long) model run: the partial
// unique index idx_agent_proposals_one_open (migration 0022) makes a concurrent
// second Generate for the same agent fail the insert immediately — closing the
// TOCTOU gap the code-level OpenProposalID check alone left open. The row is
// then updated in place on completion (like Retry).
func (s *Service) Generate(ctx context.Context, req GenerateReq) (int64, error) {
	ev, err := buildEvidence(s.DB, req.Agent, s.Repo)
	if err != nil {
		return 0, err
	}
	// Fast-path the friendly conflict (a proposal already open) before the
	// insert, so the common case reports ErrOpenProposal with the existing id.
	if open, err := s.OpenProposalID(req.Agent); err != nil {
		return 0, err
	} else if open != 0 {
		return 0, fmt.Errorf("%w (proposal %d)", ErrOpenProposal, open)
	}

	id, err := s.insertPlaceholder(req, ev)
	if err != nil {
		return 0, err
	}

	// If the (minutes-long) model run panics, finishProposal never executes and
	// the placeholder stays 'proposed' — the migration-0022 partial unique index
	// would then wedge every future Generate for this agent (409) with Retry
	// refusing it (only 'failed' is retriable). Flip the placeholder to 'failed'
	// (releasing the one-open slot, enabling Retry) and re-panic so the outer
	// spawnImprove recover still logs it.
	defer s.releaseOnPanic(id)

	diff, rationale, runErr := s.run(ctx, ev)
	if err := s.finishProposal(id, ev, diff, rationale, runErr); err != nil {
		return 0, err
	}
	return id, nil
}

// releaseOnPanic is deferred around the model run: on a panic it flips the still
// 'proposed' placeholder row to 'failed' (guarded on status so it only touches
// the placeholder, never a row a concurrent decision moved on), then re-panics
// so the caller's recover logs the crash.
func (s *Service) releaseOnPanic(id int64) {
	r := recover()
	if r == nil {
		return
	}
	if _, err := s.DB.Exec(`
		UPDATE agent_change_proposals
		   SET status = 'failed', diff = '', rationale = '', error = ?
		 WHERE id = ? AND status = 'proposed'`,
		fmt.Sprintf("pipeline panic: %v", r), id); err != nil {
		log.Printf("error: improve: release placeholder %d after panic: %v", id, err)
	}
	panic(r)
}

// insertPlaceholder reserves the agent's single open slot with an empty
// 'proposed' row. A UNIQUE-constraint failure (a concurrent Generate won the
// race) maps to ErrOpenProposal.
func (s *Service) insertPlaceholder(req GenerateReq, ev *Evidence) (int64, error) {
	var recID any
	if req.RecommendationID != nil {
		recID = *req.RecommendationID
	}
	res, err := s.DB.Exec(`
		INSERT INTO agent_change_proposals
			(recommendation_id, agent, agent_path, base_sha256, diff, rationale, status, created_at)
		VALUES (?, ?, ?, ?, '', '', 'proposed', ?)`,
		recID, req.Agent, ev.AgentPath, ev.BaseSHA256, fmtTS(time.Now()))
	if err != nil {
		if isUniqueViolation(err) {
			// A concurrent caller reserved the slot first. Report its id if we can.
			open, oerr := s.OpenProposalID(req.Agent)
			if oerr == nil && open != 0 {
				return 0, fmt.Errorf("%w (proposal %d)", ErrOpenProposal, open)
			}
			return 0, ErrOpenProposal
		}
		return 0, err
	}
	return res.LastInsertId()
}

// finishProposal writes the pipeline outcome onto the reserved row: the diff +
// rationale on success, or status='failed' with the error text otherwise (which
// releases the open slot for a retry).
func (s *Service) finishProposal(id int64, ev *Evidence, diff, rationale string, runErr error) error {
	if runErr != nil {
		log.Printf("warn: improve: agent %s: %v", ev.AgentPath, runErr)
		_, err := s.DB.Exec(`
			UPDATE agent_change_proposals
			   SET status = 'failed', diff = '', rationale = '', error = ?
			 WHERE id = ?`, runErr.Error(), id)
		return err
	}
	_, err := s.DB.Exec(`
		UPDATE agent_change_proposals
		   SET diff = ?, rationale = ?, status = 'proposed', error = NULL
		 WHERE id = ?`, diff, rationale, id)
	return err
}

// isUniqueViolation reports whether err is a SQLite UNIQUE-constraint failure
// (the partial index on open proposals). modernc.org/sqlite surfaces it as a
// message string, so a substring match is the portable check.
func isUniqueViolation(err error) bool {
	return err != nil && strings.Contains(err.Error(), "UNIQUE constraint failed")
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
