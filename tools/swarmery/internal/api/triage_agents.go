package api

// The agent Source: improvable agents that fail in most of their recent runs.
// A rule decides (no model call): each one gets a T2 recommendation plus a
// follow-up advisor suggestion to improve the agent, which the operator accepts.

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"math"
	"os"
	"strconv"
	"time"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/advisor"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/triage"
)

const (
	agentTriageKind = "agent"
	agentFile       = "file"
	// agentRule is the recommendation rule a file verdict files.
	agentRule = "T2"
	// agentMinRuns is the fewest runs in the window before a rate counts.
	agentMinRuns = 5
	// agentErrorRateEnv overrides agentErrorRateDefault, a share in (0, 1].
	agentErrorRateEnv     = "SWARMERY_TRIAGE_AGENT_ERROR_RATE"
	agentErrorRateDefault = 0.5
)

// agentErrorRate is the failing-run share threshold; an unparsable or
// out-of-range override falls back to the default with one log line.
func agentErrorRate() float64 {
	raw := os.Getenv(agentErrorRateEnv)
	if raw == "" {
		return agentErrorRateDefault
	}
	v, err := strconv.ParseFloat(raw, 64)
	if err != nil || math.IsNaN(v) || v <= 0 || v > 1 {
		log.Printf("warning: triage: %s=%q is not a share in (0, 1]; using %g", agentErrorRateEnv, raw, agentErrorRateDefault)
		return agentErrorRateDefault
	}
	return v
}

// agentNumbers is an agent item's Evidence (JSON): the scorecard row's numbers.
type agentNumbers struct {
	Agent     string  `json:"agent"`
	Runs      int64   `json:"runs"`
	Failed    int64   `json:"failed_runs"`
	ErrorRate float64 `json:"error_rate"`
	Errors    int64   `json:"errors"`
	Window    int     `json:"window_days"`
}

type agentSource struct{ h *Handler }

var (
	_ triage.Source  = (*agentSource)(nil)
	_ triage.Decider = (*agentSource)(nil)
)

func (*agentSource) Kind() string { return agentTriageKind }

func agentTitle(agent string) string { return "Agent fails in most runs: " + agent }

// Collect lists the improvable agents with enough runs whose behaviour-failed
// run share is at or over the threshold and that nothing tracks yet.
func (s *agentSource) Collect(_ context.Context, sc triage.Scope, limit int) ([]triage.Item, error) {
	pf, pargs := triageScope(sc)
	dr := triageWindow()
	rows, err := s.h.buildRetroAgents(dr, pf, pargs)
	if err != nil {
		return nil, err
	}
	tracked, err := s.trackedAgents()
	if err != nil {
		return nil, err
	}
	dismissed, err := recentlyDismissed(s.h.DB, "agent", time.Now())
	if err != nil {
		return nil, err
	}
	suppressed := make(map[string]bool, len(dismissed))
	for t := range dismissed {
		suppressed[advisor.NormAgent(t)] = true
	}
	threshold := agentErrorRate()
	var items []triage.Item
	for _, a := range rows.Agents {
		if !a.Improvable || a.Runs < agentMinRuns || a.ErrorRate < threshold ||
			tracked[advisor.NormAgent(a.Agent)] || suppressed[advisor.NormAgent(a.Agent)] {
			continue
		}
		open, err := s.openProposal(a.Agent)
		if err != nil {
			return nil, err
		}
		if open {
			continue
		}
		ev, err := json.Marshal(agentNumbers{Agent: a.Agent, Runs: a.Runs,
			Failed: int64(math.Round(a.ErrorRate * float64(a.Runs))), ErrorRate: a.ErrorRate,
			Errors: a.Errors, Window: len(dr.days)})
		if err != nil {
			return nil, err
		}
		items = append(items, triage.Item{
			Kind: agentTriageKind, Key: a.Agent, Title: agentTitle(a.Agent), Evidence: string(ev),
			Parts: []triage.Part{{Ref: a.Agent, Label: a.Agent, Allowed: []string{agentFile}}},
		})
		if limit > 0 && len(items) == limit {
			break
		}
	}
	return items, nil
}

// trackedAgents is the set (normalized names) of agents an open recommendation
// of any rule targets.
func (s *agentSource) trackedAgents() (map[string]bool, error) {
	rows, err := s.h.DB.Query(`SELECT target FROM recommendations
		WHERE target_kind = 'agent' AND status IN ('proposed','accepted','adopted')`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var t string
		if err := rows.Scan(&t); err != nil {
			return nil, err
		}
		out[advisor.NormAgent(t)] = true
	}
	return out, rows.Err()
}

// openProposal: an open improve proposal already works on the agent. A nil
// Improve service means generation is off — nothing can be open.
func (s *agentSource) openProposal(agent string) (bool, error) {
	if s.h.Improve == nil {
		return false, nil
	}
	id, err := s.h.Improve.OpenProposalID(agent)
	return id != 0, err
}

func agentReason(n agentNumbers) string {
	return fmt.Sprintf("%s failed in %d of %d runs in %d days (%.0f%% behaviour-fixable)",
		n.Agent, n.Failed, n.Runs, n.Window, n.ErrorRate*100)
}

// Decide files every collected agent by rule; no model is asked.
func (*agentSource) Decide(it triage.Item) (triage.Answer, bool) {
	var n agentNumbers
	if err := json.Unmarshal([]byte(it.Evidence), &n); err != nil || n.Agent == "" {
		n = agentNumbers{Agent: it.Key, Window: 14}
	}
	vals := make(map[string]string, len(it.Parts))
	for _, p := range it.Parts {
		vals[p.Ref] = agentFile
	}
	return triage.Answer{Values: vals, Reason: agentReason(n)}, true
}

func (s *agentSource) Apply(_ context.Context, it triage.Item, p triage.Part, value, reason string, _ json.RawMessage) (triage.Applied, error) {
	if value != agentFile {
		return triage.Applied{}, fmt.Errorf("agent: unknown value %q", value)
	}
	open, err := s.openProposal(p.Ref)
	if err != nil {
		return triage.Applied{}, err
	}
	if open {
		return triage.Applied{}, errAlreadyTracked
	}
	title := agentTitle(p.Ref)
	id, err := fileTriageRec(s.h.DB, agentRule, "agent", p.Ref, title, reason, []byte(it.Evidence))
	if err != nil {
		return triage.Applied{}, err
	}
	ref := strconv.FormatInt(id, 10)
	return triage.Applied{Prior: recPrior(id), Follow: []triage.Suggestion{{
		Kind: advisorKind, Class: triage.ClassImprove, Ref: ref, ItemKey: ref,
		Title: title, Value: "improve", Reason: reason,
	}}}, nil
}

// Undo dismisses the filed recommendation while it is still proposed; idempotent.
func (s *agentSource) Undo(_ context.Context, v triage.Verdict) error {
	return dismissTriageRec(s.h.DB, v.Prior)
}

// Open: the agent still waits while nothing tracks it and no proposal is open.
func (s *agentSource) Open(_ context.Context, ref string) (bool, error) {
	tracked, err := s.trackedAgents()
	if err != nil {
		return false, err
	}
	if tracked[advisor.NormAgent(ref)] {
		return false, nil
	}
	open, err := s.openProposal(ref)
	return !open, err
}
