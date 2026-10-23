package api

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/sysscan"
)

// skillAuditInsightsDB is the insights fixture plus the skill-audit findings:
// skill_dead on the local 'deploy' (beta), a pair between the two deploy rows,
// a stale pair whose skill is deleted, a load finding on beta, and a RESOLVED
// load finding on alpha.
func skillAuditInsightsDB(t *testing.T) *httptest.Server {
	t.Helper()
	db := insightsDB(t)
	if _, err := db.Exec(`INSERT INTO skills (id, name, scope, project_id, dir_path, origin, plugin_name, deleted) VALUES
		(32, 'release-notes', 'project', 1, '/work/alpha/.claude/skills/release-notes', 'local', NULL, 0),
		(33, 'gone',          'global',  NULL, '/u/.claude/skills/gone',                'local', NULL, 1)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO config_lint_findings (target, rule, severity, message, detected_at, resolved_at) VALUES
		('skill:31',          'skill_dead',              'info', 'skill "deploy": no use in 30 days', '2026-10-20T00:00:00Z', NULL),
		('skill_pair:30:32',  'skill_desc_similar',      'warn', 'skills "core:deploy" and "release-notes": overlap 0.85', '2026-10-20T00:00:00Z', NULL),
		('skill_pair:32:33',  'skill_desc_similar',      'warn', 'pair with a deleted skill', '2026-10-20T00:00:00Z', NULL),
		('project:2',         'session_skill_load_high', 'warn', 'project "beta": p95 14 > 12', '2026-10-20T00:00:00Z', NULL),
		('project:1',         'session_skill_load_high', 'warn', 'was high once', '2026-10-01T00:00:00Z', '2026-10-10T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	h, err := NewServer(db, false)
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return srv
}

func TestSystemInsightsSkillAudit(t *testing.T) {
	ws := t.TempDir()
	AttachWorkspaceRoot(ws)
	t.Cleanup(func() { AttachWorkspaceRoot("") })
	srv := skillAuditInsightsDB(t)

	var got systemInsightsDTO
	getJSON(t, srv.URL+"/api/system/insights", &got)

	// skill_dead rides the dead list next to agent_dead: agents first.
	var kinds []string
	for _, d := range got.Dead {
		kinds = append(kinds, d.Kind+":"+d.Name)
	}
	if want := []string{"agent:helper", "skill:deploy"}; !reflect.DeepEqual(kinds, want) {
		t.Errorf("dead = %v, want %v", kinds, want)
	}

	if len(got.SkillAudit) != 2 {
		t.Fatalf("skillAudit = %+v, want the live pair and beta's load finding", got.SkillAudit)
	}
	pair, load := got.SkillAudit[0], got.SkillAudit[1]
	if pair.Rule != sysscan.RuleSkillDescSimilar || pair.Severity != "warn" ||
		!reflect.DeepEqual(pair.Skills, []string{"core:deploy", "release-notes"}) || pair.ProjectSlug != nil {
		t.Errorf("pair = %+v", pair)
	}
	if load.Rule != sysscan.RuleSessionSkillLoadHigh || load.ProjectSlug == nil || *load.ProjectSlug != "beta" ||
		len(load.Skills) != 0 {
		t.Errorf("load = %+v", load)
	}
	if got.SkillAuditReport != nil {
		t.Errorf("report = %+v before the routine wrote one, want null", got.SkillAuditReport)
	}

	// Project scope: alpha keeps the pair (core:deploy and release-notes are
	// both effective there) and drops beta's load finding; beta keeps its load
	// finding and drops the pair (release-notes is alpha's).
	var alpha systemInsightsDTO
	getJSON(t, srv.URL+"/api/system/insights?projectId=alpha", &alpha)
	if len(alpha.SkillAudit) != 1 || alpha.SkillAudit[0].Rule != sysscan.RuleSkillDescSimilar {
		t.Errorf("alpha skillAudit = %+v, want only the pair", alpha.SkillAudit)
	}
	var beta systemInsightsDTO
	getJSON(t, srv.URL+"/api/system/insights?projectId=beta", &beta)
	if len(beta.SkillAudit) != 1 || beta.SkillAudit[0].Rule != sysscan.RuleSessionSkillLoadHigh {
		t.Errorf("beta skillAudit = %+v, want only beta's load finding (release-notes is alpha's)", beta.SkillAudit)
	}
}

func TestSystemSkillAuditReport(t *testing.T) {
	ws := t.TempDir()
	AttachWorkspaceRoot(ws)
	t.Cleanup(func() { AttachWorkspaceRoot("") })
	srv := skillAuditInsightsDB(t)

	resp, err := http.Get(srv.URL + "/api/system/skill-audit/report")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("no report yet: status %d, want 404", resp.StatusCode)
	}

	dir := sysscan.SkillAuditReportDir(ws)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string]string{
		"skill-audit-2026-10-19.md": "# old\n",
		"skill-audit-2026-10-26.md": "# Skill audit 2026-10-26\n\n## Before a new agent, extend one of these\n\n- workspace-plans\n",
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	var body skillAuditReportBodyDTO
	getJSON(t, srv.URL+"/api/system/skill-audit/report", &body)
	if body.Date != "2026-10-26" || !strings.HasSuffix(body.Path, "skill-audit-2026-10-26.md") ||
		!strings.Contains(body.Markdown, "## Before a new agent, extend one of these") {
		t.Errorf("report = %+v", body)
	}
	var ins systemInsightsDTO
	getJSON(t, srv.URL+"/api/system/insights", &ins)
	if ins.SkillAuditReport == nil || ins.SkillAuditReport.Date != "2026-10-26" {
		t.Errorf("insights report = %+v, want the 2026-10-26 one", ins.SkillAuditReport)
	}
}
