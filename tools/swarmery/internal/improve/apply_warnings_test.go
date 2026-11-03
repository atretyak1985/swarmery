package improve

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func warningsOf(t *testing.T, id int64, svc *Service) []string {
	t.Helper()
	var raw string
	if err := svc.DB.QueryRow(`SELECT warnings_json FROM agent_change_proposals WHERE id = ?`, id).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var ws []string
	if err := json.Unmarshal([]byte(raw), &ws); err != nil {
		t.Fatalf("warnings_json %q: %v", raw, err)
	}
	return ws
}

// SC-8 (apply half): a diff that grows an always-on file stores a warning in
// warnings_json — and the apply still succeeds (a warning is never a gate).
func TestApplyAlwaysOnGrowthWarnsButApplies(t *testing.T) {
	db := applyDB(t)
	seedApprovedProposal(t, db, 1, "tech-lead", coreAgentPath, "body\n", coreDiff)
	f := baseExec("/repo", "/tmp/wt1")
	f.runResp["git diff"] = struct {
		out string
		err error
	}{out: "5\t1\tplugins/core/agents/tech-lead.md\n"}
	svc := &Service{DB: db, Repo: "/repo", Exec: f}

	if err := svc.Apply(context.Background(), 1); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if status, _, errCol := applyRow(t, db, 1); status != "applied" {
		t.Fatalf("status = %q (err %v), want applied despite the warning", status, deref(errCol))
	}
	ws := warningsOf(t, 1, svc)
	if len(ws) != 1 || !strings.Contains(ws[0], "gains 4 lines net (+5 −1)") {
		t.Errorf("warnings = %q, want one +4 net growth warning", ws)
	}
}

// A balanced edit (adds no more than it removes) stores no warning, and the
// apply-time measurement replaces a stale generate-time estimate.
func TestApplyBalancedEditClearsWarnings(t *testing.T) {
	db := applyDB(t)
	seedApprovedProposal(t, db, 1, "tech-lead", coreAgentPath, "body\n", coreDiff)
	mustExec(t, db, `UPDATE agent_change_proposals SET warnings_json = '["stale"]' WHERE id = 1`)
	svc := &Service{DB: db, Repo: "/repo", Exec: baseExec("/repo", "/tmp/wt1")}

	if err := svc.Apply(context.Background(), 1); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if ws := warningsOf(t, 1, svc); len(ws) != 0 {
		t.Errorf("warnings = %q, want none for a +1 −1 edit", ws)
	}
}

func TestAlwaysOnGrowthWarning(t *testing.T) {
	for _, c := range []struct {
		path           string
		added, removed int
		warn           bool
	}{
		{"plugins/core/agents/a.md", 3, 1, true},
		{"plugins/core/agents/a.md", 2, 2, false},
		{"CLAUDE.md", 1, 0, true},
		{"tools/x/CLAUDE.md", 4, 0, true},
		{"plugins/core/skills/s/SKILL.md", 9, 0, false}, // a skill loads on its trigger, not always
	} {
		if got := alwaysOnGrowthWarning(c.path, c.added, c.removed) != ""; got != c.warn {
			t.Errorf("%s +%d −%d: warn = %v, want %v", c.path, c.added, c.removed, got, c.warn)
		}
	}
	if a, r := diffLineCounts("--- a/x\n+++ b/x\n@@ -1 +1,2 @@ lens: procedure\n ctx\n-old\n+new\n+more\n"); a != 2 || r != 1 {
		t.Errorf("diffLineCounts = +%d −%d, want +2 −1", a, r)
	}
}
