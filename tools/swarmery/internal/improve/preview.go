package improve

import (
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/advisor"
)

// Evidence assembles the read-only phase-3 evidence bundle for one (normalized)
// agent key — the same bundle Generate feeds the model, exposed so the
// dashboard can PREVIEW it before triggering a (minutes-long) generation. Never
// mutates any row. Returns ErrAgentNotFound for a built-in agent (no editable
// registry row).
func (s *Service) Evidence(agent string) (*Evidence, error) {
	return buildEvidence(s.DB, agent, s.Repo)
}

// RegistryAgentSet returns the set of normalized (advisor.NormAgent-folded)
// names of every live registry agent — the agents Evidence/Generate can act on.
// Built-in agents (Explore, general-purpose, debugger — no editable .md, no
// registry row) are absent. Mirrors resolveAgent's query so the two never
// drift; used by the scorecards handler to gate the Improve button.
func (s *Service) RegistryAgentSet() (map[string]struct{}, error) {
	rows, err := s.DB.Query(`
		SELECT a.name
		  FROM agents a
		  JOIN agent_versions v ON v.id = a.current_version_id
		 WHERE a.deleted = 0`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]struct{}{}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		out[advisor.NormAgent(name)] = struct{}{}
	}
	return out, rows.Err()
}
