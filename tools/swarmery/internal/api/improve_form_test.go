package api

import (
	"encoding/json"
	"net/http"
	"testing"
)

// The proposal list carries the harness² phase-3 fields: outcome, lens, form,
// follow-ups and warnings — and a hand-broken JSON cell degrades to [] rather
// than failing the whole response.
func TestListProposalsCarriesFormLensFields(t *testing.T) {
	srv, db := improveServer(t, &improveMockRunner{out: improveValidOut})
	seedRegistryAgent(t, db, 1, "tech-lead")
	seedRecommendation(t, db, 1, "agent", "tech-lead", "accepted")
	postJSON(t, srv.URL+"/api/retro/recommendations/1/improve", http.StatusAccepted)
	if _, err := db.Exec(`INSERT INTO agent_change_proposals
		(agent, agent_path, base_sha256, diff, rationale, status, outcome, form,
		 suggested_followups_json, warnings_json, created_at, decided_at)
		VALUES ('code-auditor', 'plugins/core/agents/code-auditor.md', 'x', '', 'already covered',
		        'rejected', 'kept_base', 'keep_base', 'not json', '[]',
		        '2026-11-02T00:00:00.000Z', '2026-11-02T00:00:00.000Z')`); err != nil {
		t.Fatal(err)
	}

	var out struct {
		Proposals []struct {
			Agent              string            `json:"agent"`
			Status             string            `json:"status"`
			Outcome            string            `json:"outcome"`
			Lens               string            `json:"lens"`
			Form               string            `json:"form"`
			SuggestedFollowups []json.RawMessage `json:"suggested_followups"`
			Warnings           []string          `json:"warnings"`
		} `json:"proposals"`
	}
	getJSON(t, srv.URL+"/api/retro/proposals", &out)
	if len(out.Proposals) != 2 {
		t.Fatalf("proposals = %+v", out.Proposals)
	}
	kept, gen := out.Proposals[0], out.Proposals[1]
	if kept.Outcome != "kept_base" || kept.Status != "rejected" || kept.Form != "keep_base" ||
		kept.SuggestedFollowups == nil || len(kept.SuggestedFollowups) != 0 {
		t.Errorf("kept_base row = %+v", kept)
	}
	if gen.Outcome != "" || gen.Lens != "knowledge" || gen.Form != "always-on" ||
		len(gen.SuggestedFollowups) != 1 || len(gen.Warnings) != 0 {
		// improveValidOut is a +1 −1 edit: no always-on growth, no warning.
		t.Errorf("generated row = %+v", gen)
	}
}
