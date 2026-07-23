// Package improve is the agent rewriter of the retro improvement loop
// (self-improvement phase 3): for an accepted agent-kind recommendation (or
// an ad-hoc per-agent trigger) it assembles an evidence bundle from the data
// already in SQLite, spawns headless `claude -p`, and stores the returned
// unified-diff proposal with its rationale in agent_change_proposals
// (migration 0021). Applying the diff and opening a PR is phase 4 — this
// package never touches agent files.
//
// Proposal lifecycle: proposed → approved → applied | rejected; failed is
// terminal-retriable (POST /api/retro/proposals/{id}/retry). "One open
// proposal per agent" (status proposed|approved) is enforced here in code,
// not by the schema.
package improve

import (
	"database/sql"
	"errors"
	"time"
)

// Typed failures the API maps to status codes (404/409/422).
var (
	// ErrAgentNotFound — the agent key resolves to no live registry row.
	ErrAgentNotFound = errors.New("agent not found in registry")
	// ErrOpenProposal — a proposal in proposed|approved already exists for
	// the agent (the one-open-proposal invariant).
	ErrOpenProposal = errors.New("an open proposal already exists for this agent")
	// ErrProposalNotFound — Retry target row does not exist.
	ErrProposalNotFound = errors.New("proposal not found")
	// ErrNotRetriable — Retry target is not in status 'failed'.
	ErrNotRetriable = errors.New("only failed proposals can be retried")
)

// tsFmt matches the ingest timestamp shape so string range predicates behave.
const tsFmt = "2006-01-02T15:04:05.000Z"

func fmtTS(t time.Time) string { return t.UTC().Format(tsFmt) }

// Service generates agent change proposals.
type Service struct {
	DB *sql.DB
	// Repo is the marketplace repo root — the git checkout the phase-4 apply/PR
	// pipeline fetches and worktrees from (unused by generation).
	Repo   string
	Runner Runner
	// Exec is the git/gh + filesystem boundary of the apply pipeline; nil is
	// fine for a generation-only Service. Production wires OSExec; tests fake it.
	Exec Exec
}

// GenerateReq parameterizes one proposal generation.
type GenerateReq struct {
	// Agent is the registry key; normalized (advisor.NormAgent fold) on entry.
	Agent string
	// RecommendationID links the proposal to its accepted recommendation;
	// nil for the ad-hoc per-agent trigger.
	RecommendationID *int64
}
