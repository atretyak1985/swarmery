package api

// phase 4+: promotion & drift detector — compute layer + endpoint tests.
// The fixture seeds the sysscan registry tables directly in SQL (pattern:
// system_test.go/systemServer) with every detector case: an identical and a
// diverged promotion candidate, single-project and deleted negative controls,
// an identical and a diverged stale plugin override, on-disk command contents
// (commands are not versioned), and an active + a resolved agent_dead finding.

import (
	"database/sql"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/store"
)

const insAgentHelper = `---
name: helper
---

shared body
`

const insAgentDrifterA = `---
name: drifter
---

line one
line two
`

// The beta copy carries a secret-shaped value: served diffs must redact it.
const insAgentDrifterB = `---
name: drifter
---

line one
line two changed
MY_TOKEN=abc123
`

const insAgentReviewer = `---
name: reviewer
---

review the diff
`

const insAgentFixer = `---
name: lint-fixer
---

fix lint
`

const insSkillDeployPlugin = `---
name: deploy
description: Deploys things safely with checks
---

pack steps
`

const insSkillDeployLocal = `---
name: deploy
description: Deploys things safely with checks
---

local steps
extra local line
`

const insCmdShipA = `---
description: ship it
---

deploy step one
`

const insCmdShipB = `---
description: ship it
---

deploy step one
deploy step two
`

// insightsDB seeds the full detector world into a temp store.
func insightsDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "insights.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := db.Exec(q, args...); err != nil {
			t.Fatalf("seed %q: %v", q, err)
		}
	}

	exec(`INSERT INTO projects (id, path, slug, name, first_seen) VALUES
	      (1, '/work/alpha', 'alpha', 'Alpha', '2026-07-01T00:00:00Z'),
	      (2, '/work/beta',  'beta',  'Beta',  '2026-07-01T00:00:00Z')`)

	// Agents:
	//  - 'helper' in BOTH projects, same hash → identical promotion candidate;
	//    the global-local copy (id 19) must NOT join the group (scope filter).
	//  - 'drifter' in both projects, different hashes → diverged candidate.
	//  - 'lint-fixer' in ONE project → no candidate.
	//  - 'ghost' deleted in both → no candidate.
	//  - 'reviewer' local vs plugin 'core:reviewer' at the SAME hash →
	//    identical stale override.
	exec(`INSERT INTO agents (id, name, scope, project_id, file_path,
	                          current_version_id, origin, plugin_name, deleted) VALUES
	      (10, 'helper',        'project', 1,    '/work/alpha/.claude/agents/helper.md',     100,  'local',  NULL,   0),
	      (11, 'helper',        'project', 2,    '/work/beta/.claude/agents/helper.md',      101,  'local',  NULL,   0),
	      (12, 'drifter',       'project', 1,    '/work/alpha/.claude/agents/drifter.md',    102,  'local',  NULL,   0),
	      (13, 'drifter',       'project', 2,    '/work/beta/.claude/agents/drifter.md',     103,  'local',  NULL,   0),
	      (14, 'core:reviewer', 'global',  NULL, '/u/.claude/plugins/cache/swarmery/core/1.0.0/agents/reviewer.md', 104, 'plugin', 'core', 0),
	      (15, 'reviewer',      'project', 1,    '/work/alpha/.claude/agents/reviewer.md',   105,  'local',  NULL,   0),
	      (16, 'lint-fixer',    'project', 1,    '/work/alpha/.claude/agents/lint-fixer.md', 106,  'local',  NULL,   0),
	      (17, 'ghost',         'project', 1,    '/work/alpha/.claude/agents/ghost.md',      NULL, 'local',  NULL,   1),
	      (18, 'ghost',         'project', 2,    '/work/beta/.claude/agents/ghost.md',       NULL, 'local',  NULL,   1),
	      (19, 'helper',        'global',  NULL, '/u/.claude/agents/helper.md',              107,  'local',  NULL,   0)`)
	exec(`INSERT INTO agent_versions (id, agent_id, content_hash, content, created_at) VALUES
	      (100, 10, 'helper-hash',   ?, '2026-07-01T00:00:00Z'),
	      (101, 11, 'helper-hash',   ?, '2026-07-01T00:00:00Z'),
	      (102, 12, 'drifter-alpha', ?, '2026-07-01T00:00:00Z'),
	      (103, 13, 'drifter-beta',  ?, '2026-07-01T00:00:00Z'),
	      (104, 14, 'reviewer-hash', ?, '2026-07-01T00:00:00Z'),
	      (105, 15, 'reviewer-hash', ?, '2026-07-01T00:00:00Z'),
	      (106, 16, 'fixer-hash',    ?, '2026-07-01T00:00:00Z'),
	      (107, 19, 'helper-hash',   ?, '2026-07-01T00:00:00Z')`,
		insAgentHelper, insAgentHelper, insAgentDrifterA, insAgentDrifterB,
		insAgentReviewer, insAgentReviewer, insAgentFixer, insAgentHelper)

	// Skills: local 'deploy' (beta) diverges from plugin 'core:deploy'.
	exec(`INSERT INTO skills (id, name, scope, project_id, dir_path,
	                          current_version_id, origin, plugin_name, deleted) VALUES
	      (30, 'core:deploy', 'global',  NULL, '/u/.claude/plugins/cache/swarmery/core/1.0.0/skills/deploy', 300, 'plugin', 'core', 0),
	      (31, 'deploy',      'project', 2,    '/work/beta/.claude/skills/deploy',                           301, 'local',  NULL,   0)`)
	exec(`INSERT INTO skill_versions (id, skill_id, content_hash, content, created_at) VALUES
	      (300, 30, 'deploy-plugin', ?, '2026-07-01T00:00:00Z'),
	      (301, 31, 'deploy-local',  ?, '2026-07-01T00:00:00Z')`,
		insSkillDeployPlugin, insSkillDeployLocal)

	// Commands: 'ship' in both projects, hashes differ. Commands are NOT
	// versioned (registry.go: content_hash only), so contents live ON DISK —
	// the fixture writes real files and points file_path at them.
	cmdDir := t.TempDir()
	shipA := filepath.Join(cmdDir, "alpha-ship.md")
	shipB := filepath.Join(cmdDir, "beta-ship.md")
	writeFile(t, shipA, insCmdShipA)
	writeFile(t, shipB, insCmdShipB)
	exec(`INSERT INTO commands (id, name, scope, project_id, file_path,
	                            origin, plugin_name, content_hash, deleted) VALUES
	      (50, 'ship', 'project', 1, ?, 'local', NULL, 'ship-alpha', 0),
	      (51, 'ship', 'project', 2, ?, 'local', NULL, 'ship-beta',  0)`, shipA, shipB)

	// Dead: ACTIVE agent_dead on 'helper' (alpha); a RESOLVED one must not count.
	exec(`INSERT INTO config_lint_findings (target, rule, severity, message, detected_at, resolved_at) VALUES
	      ('agent:10', 'agent_dead', 'info', 'agent "helper": 0 event mentions in the last 30 days by available telemetry', '2026-07-10T00:00:00Z', NULL),
	      ('agent:12', 'agent_dead', 'info', 'was dead once',                                                               '2026-06-01T00:00:00Z', '2026-06-20T00:00:00Z')`)

	return db
}

func TestComputeInsightsPromotionCandidates(t *testing.T) {
	db := insightsDB(t)
	got, err := computeInsights(db)
	if err != nil {
		t.Fatalf("computeInsights: %v", err)
	}

	// Deterministic order: kinds agent→skill→command, names A→Z inside a kind.
	if len(got.PromotionCandidates) != 3 {
		t.Fatalf("promotion candidates = %d, want 3 (drifter, helper, ship)", len(got.PromotionCandidates))
	}

	drifter := got.PromotionCandidates[0]
	if drifter.Kind != "agent" || drifter.Name != "drifter" || drifter.Similarity != "diverged" {
		t.Errorf("candidate[0] = %+v, want diverged agent drifter", drifter)
	}
	if drifter.DiffStat == nil || drifter.DiffStat.Added != 2 || drifter.DiffStat.Removed != 1 {
		t.Errorf("drifter diffStat = %+v, want +2/-1", drifter.DiffStat)
	}
	if !strings.Contains(drifter.Diff, "-line two") || !strings.Contains(drifter.Diff, "+line two changed") {
		t.Errorf("drifter diff = %q, want a real unified diff between the copies", drifter.Diff)
	}
	if strings.Contains(drifter.Diff, "abc123") {
		t.Errorf("drifter diff leaked the seeded token: %q", drifter.Diff)
	}
	if !strings.Contains(drifter.Diff, "MY_TOKEN=•••") {
		t.Errorf("drifter diff = %q, want the value-side redaction marker", drifter.Diff)
	}
	if len(drifter.Copies) != 2 ||
		drifter.Copies[0].ProjectSlug == nil || *drifter.Copies[0].ProjectSlug != "alpha" ||
		drifter.Copies[1].ProjectSlug == nil || *drifter.Copies[1].ProjectSlug != "beta" {
		t.Errorf("drifter copies = %+v, want alpha+beta", drifter.Copies)
	}

	helper := got.PromotionCandidates[1]
	if helper.Kind != "agent" || helper.Name != "helper" || helper.Similarity != "identical" {
		t.Errorf("candidate[1] = %+v, want identical agent helper", helper)
	}
	if helper.Diff != "" || helper.DiffStat != nil {
		t.Errorf("helper diff = %q / %+v, want none for an identical candidate", helper.Diff, helper.DiffStat)
	}
	if len(helper.Copies) != 2 {
		t.Errorf("helper copies = %d, want 2 (the global-local copy must not join the group)", len(helper.Copies))
	}

	ship := got.PromotionCandidates[2]
	if ship.Kind != "command" || ship.Name != "ship" || ship.Similarity != "diverged" {
		t.Errorf("candidate[2] = %+v, want diverged command ship", ship)
	}
	if ship.DiffStat == nil || ship.DiffStat.Added != 1 || ship.DiffStat.Removed != 0 {
		t.Errorf("ship diffStat = %+v, want +1/-0 (contents read from disk)", ship.DiffStat)
	}
	if !strings.Contains(ship.Diff, "+deploy step two") {
		t.Errorf("ship diff = %q", ship.Diff)
	}
	for _, c := range got.PromotionCandidates {
		if !strings.Contains(c.Hint, "de-flavor") {
			t.Errorf("%s %q hint = %q, want the EXTENDING.md promotion recipe", c.Kind, c.Name, c.Hint)
		}
	}
}

func TestComputeInsightsStaleOverrides(t *testing.T) {
	db := insightsDB(t)
	got, err := computeInsights(db)
	if err != nil {
		t.Fatalf("computeInsights: %v", err)
	}

	if len(got.StaleOverrides) != 2 {
		t.Fatalf("stale overrides = %d, want 2 (agent reviewer, skill deploy)", len(got.StaleOverrides))
	}

	rev := got.StaleOverrides[0]
	if rev.Kind != "agent" || rev.Name != "reviewer" || rev.PluginName != "core" {
		t.Errorf("override[0] = %+v, want agent reviewer vs plugin core", rev)
	}
	if !rev.Identical || rev.Diff != "" || rev.DiffStat != nil {
		t.Errorf("reviewer override = identical=%v diff=%q stat=%+v, want identical/no-diff (pointless override)",
			rev.Identical, rev.Diff, rev.DiffStat)
	}
	if rev.Local.ItemID != 15 || rev.Plugin.ItemID != 14 {
		t.Errorf("reviewer ids = local %d / plugin %d, want 15/14", rev.Local.ItemID, rev.Plugin.ItemID)
	}
	if rev.Local.ProjectSlug == nil || *rev.Local.ProjectSlug != "alpha" || rev.Plugin.Scope != "global" {
		t.Errorf("reviewer sides = %+v / %+v", rev.Local, rev.Plugin)
	}
	if !strings.Contains(rev.Hint, "pointless") {
		t.Errorf("identical override hint = %q", rev.Hint)
	}

	dep := got.StaleOverrides[1]
	if dep.Kind != "skill" || dep.Name != "deploy" || dep.Identical {
		t.Errorf("override[1] = %+v, want diverged skill deploy", dep)
	}
	if dep.DiffStat == nil || dep.DiffStat.Added != 2 || dep.DiffStat.Removed != 1 {
		t.Errorf("deploy diffStat = %+v, want +2/-1 (both sides from *_versions — plugin content IS stored)", dep.DiffStat)
	}
	if !strings.Contains(dep.Diff, "+local steps") || !strings.Contains(dep.Diff, "-pack steps") {
		t.Errorf("deploy diff = %q", dep.Diff)
	}
	if !strings.Contains(dep.Hint, "intentional override or drift") {
		t.Errorf("diverged override hint = %q", dep.Hint)
	}
}

func TestComputeInsightsDead(t *testing.T) {
	db := insightsDB(t)
	got, err := computeInsights(db)
	if err != nil {
		t.Fatalf("computeInsights: %v", err)
	}

	if len(got.Dead) != 1 {
		t.Fatalf("dead = %d, want 1 (the resolved finding must not count)", len(got.Dead))
	}
	d := got.Dead[0]
	if d.Kind != "agent" || d.ID != 10 || d.Name != "helper" || d.Scope != "project" ||
		d.ProjectSlug == nil || *d.ProjectSlug != "alpha" {
		t.Errorf("dead[0] = %+v", d)
	}
	if !strings.Contains(d.Message, "0 event mentions") {
		t.Errorf("dead message = %q", d.Message)
	}
}

// insightsServer wraps the fixture DB in a real HTTP server.
func insightsServer(t *testing.T) *httptest.Server {
	t.Helper()
	db := insightsDB(t)
	h, err := NewServer(db, false)
	if err != nil {
		t.Fatalf("new server: %v", err)
	}
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return srv
}

func TestSystemInsightsEndpoint(t *testing.T) {
	srv := insightsServer(t)

	var resp map[string]any
	getJSON(t, srv.URL+"/api/system/insights", &resp)
	if n := len(resp["promotionCandidates"].([]any)); n != 3 {
		t.Errorf("promotionCandidates = %d, want 3", n)
	}
	if n := len(resp["staleOverrides"].([]any)); n != 2 {
		t.Errorf("staleOverrides = %d, want 2", n)
	}
	if n := len(resp["dead"].([]any)); n != 1 {
		t.Errorf("dead = %d, want 1", n)
	}

	// Wire-shape spot check on the first candidate (camelCase JSON keys).
	first := resp["promotionCandidates"].([]any)[0].(map[string]any)
	if first["kind"] != "agent" || first["name"] != "drifter" || first["similarity"] != "diverged" {
		t.Errorf("candidate[0] = %v", first)
	}
	stat := first["diffStat"].(map[string]any)
	if stat["added"].(float64) != 2 || stat["removed"].(float64) != 1 {
		t.Errorf("candidate[0].diffStat = %v, want added=2 removed=1", stat)
	}
	if !strings.Contains(first["hint"].(string), "de-flavor") {
		t.Errorf("candidate[0].hint = %v", first["hint"])
	}

	// Full-body redaction sweep: the seeded token never leaves the daemon.
	raw, err := http.Get(srv.URL + "/api/system/insights")
	if err != nil {
		t.Fatal(err)
	}
	body, err := io.ReadAll(raw.Body)
	raw.Body.Close()
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(body), "abc123") {
		t.Errorf("insights response leaked the seeded token")
	}
}

// TestSystemInsightsQuietWorld: the step-05 fixture has no promotion pairs
// and no plugin-name collisions — lists must be EMPTY ARRAYS (never null),
// and its active agent_dead finding must surface as the one dead entry.
func TestSystemInsightsQuietWorld(t *testing.T) {
	srv := systemServer(t)

	var resp map[string]any
	getJSON(t, srv.URL+"/api/system/insights", &resp)
	for key, want := range map[string]int{"promotionCandidates": 0, "staleOverrides": 0, "dead": 1} {
		list, ok := resp[key].([]any)
		if !ok {
			t.Fatalf("%s = %v (%T), want a JSON array (never null)", key, resp[key], resp[key])
		}
		if len(list) != want {
			t.Errorf("%s = %d, want %d", key, len(list), want)
		}
	}
}

func TestInsightCounts(t *testing.T) {
	db := insightsDB(t)
	promos, stales, err := insightCounts(db)
	if err != nil {
		t.Fatalf("insightCounts: %v", err)
	}
	if promos != 3 || stales != 2 {
		t.Errorf("insightCounts = %d/%d, want 3/2 (must match len(computeInsights lists))", promos, stales)
	}
}
