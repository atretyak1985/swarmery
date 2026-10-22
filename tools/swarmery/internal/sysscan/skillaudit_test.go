package sysscan

import (
	"database/sql"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/guards"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/store"
)

const skillAuditFixtures = "../../testdata/fixtures/skillaudit"

// auditDB opens a migrated temp DB loaded with the skill-audit fixtures
// (registry first, telemetry second — events reference sessions and skills).
func auditDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "audit.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	for _, f := range []string{"skills.sql", "events.sql"} {
		raw, err := os.ReadFile(filepath.Join(skillAuditFixtures, f))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(string(raw)); err != nil {
			t.Fatalf("load %s: %v", f, err)
		}
	}
	return db
}

func names(cs []AuditComponent) []string {
	out := make([]string, 0, len(cs))
	for _, c := range cs {
		out = append(out, c.Name)
	}
	return out
}

func TestSkillAuditReport(t *testing.T) {
	db := auditDB(t)
	rep, err := ComputeSkillAudit(db, AuditOptions{SimilarityThreshold: 0.8, SessionSkillLoadP95: 2})
	if err != nil {
		t.Fatalf("audit: %v", err)
	}

	// workspace-plans (Skill tool, qualified and bare), run-plan (skill_id FK),
	// graphify (slash command), both pdf skills and preloaded-skill (in the
	// frontmatter of tech-lead, which ran) are used; old-helper's only use is
	// 40 days old and the agent that preloads it never ran; never-used is only
	// quoted in prose.
	if got, want := names(rep.UnusedSkills), []string{"never-used", "old-helper"}; !reflect.DeepEqual(got, want) {
		t.Errorf("unused skills = %v, want %v", got, want)
	}
	for _, s := range rep.UnusedSkills {
		if s.ID == 10 {
			t.Errorf("core:preloaded-skill is preloaded by tech-lead, which ran, yet reported unused")
		}
	}
	// tech-lead runs by name, fk-agent through agent_id; gone-agent is deleted.
	if got, want := names(rep.UnusedAgents), []string{"legacy-bot"}; !reflect.DeepEqual(got, want) {
		t.Errorf("unused agents = %v, want %v", got, want)
	}

	if len(rep.SimilarPairs) != 1 {
		t.Fatalf("similar pairs = %+v, want exactly the pdf-export/pdf-exporter pair", rep.SimilarPairs)
	}
	p := rep.SimilarPairs[0]
	if p.A.ID != 6 || p.B.ID != 7 || p.Similarity != 1 {
		t.Errorf("pair = %d↔%d @%.2f, want 6↔7 @1.00 (the global copies, ordered by name)", p.A.ID, p.B.ID, p.Similarity)
	}

	want := []ProjectSkillLoad{
		{ProjectID: 1, Slug: "alpha", Sessions: 2, P95: 3, Max: 3, OverLimit: true},
		{ProjectID: 2, Slug: "beta", Sessions: 1, P95: 1, Max: 1, OverLimit: false},
	}
	if !reflect.DeepEqual(rep.SkillLoad, want) {
		t.Errorf("skill load = %+v, want %+v", rep.SkillLoad, want)
	}

	// Registered skills only, most sessions first; brainstorming is not in the
	// registry and /clear is a command.
	wantUsed := []SkillUse{{"workspace-plans", 3}, {"graphify", 1}, {"pdf-export", 1}, {"pdf-exporter", 1}}
	if !reflect.DeepEqual(rep.UsedSkills, wantUsed) {
		t.Errorf("used skills = %+v, want %+v", rep.UsedSkills, wantUsed)
	}
}

func TestSkillAuditZeroThresholdDisablesRule(t *testing.T) {
	db := auditDB(t)
	rep, err := ComputeSkillAudit(db, AuditOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.SimilarPairs) != 0 {
		t.Errorf("similar pairs with no threshold = %d, want 0", len(rep.SimilarPairs))
	}
	for _, l := range rep.SkillLoad {
		if l.OverLimit {
			t.Errorf("project %s over a limit that was never set", l.Slug)
		}
	}
	found := auditFindings(rep)
	for _, rule := range []string{RuleSkillDescSimilar, RuleSessionSkillLoadHigh} {
		if _, ok := found[rule]; ok {
			t.Errorf("%s present with no threshold — Lint would resolve rows it never evaluated", rule)
		}
	}
}

// TestSkillAuditLint drives the rules through Lint: the acceptance trio (a
// skill with a Skill event is not dead; near-identical descriptions are ONE
// pair; p95 above the limit is a project finding) plus the lifecycle.
func TestSkillAuditLint(t *testing.T) {
	db := auditDB(t)
	cfg := Config{ClaudeDir: t.TempDir(), SkillSimilarityThreshold: 0.8, SessionSkillLoadP95: 2}
	s := New(db, cfg, nil)
	st, err := s.Lint()
	if err != nil {
		t.Fatalf("lint: %v", err)
	}

	if n := activeCount(t, db, "skill:1", RuleSkillDead); n != 0 {
		t.Errorf("workspace-plans has a Skill event in the window but is skill_dead")
	}
	for _, target := range []string{"skill:4", "skill:5"} {
		if n := count(t, db, `SELECT COUNT(*) FROM config_lint_findings
			WHERE target = ? AND rule = ? AND severity = 'info' AND resolved_at IS NULL`, target, RuleSkillDead); n != 1 {
			t.Errorf("%s: active info skill_dead = %d, want 1", target, n)
		}
	}
	if st.PerRule[RuleSkillDead] != 2 || st.PerRule[RuleAgentDead] != 1 {
		t.Errorf("per-rule = %v, want skill_dead=2 agent_dead=1", st.PerRule)
	}
	if n := activeCount(t, db, "agent:2", RuleAgentDead); n != 1 {
		t.Errorf("legacy-bot not agent_dead")
	}

	var target, severity, msg string
	if err := db.QueryRow(`SELECT target, severity, message FROM config_lint_findings
		WHERE rule = ? AND resolved_at IS NULL`, RuleSkillDescSimilar).Scan(&target, &severity, &msg); err != nil {
		t.Fatalf("one active skill_desc_similar row: %v", err)
	}
	if target != "skill_pair:6:7" || severity != "warn" {
		t.Errorf("pair finding = %s/%s, want skill_pair:6:7/warn", target, severity)
	}
	for _, name := range []string{`"pdf-export"`, `"pdf-exporter"`} {
		if !strings.Contains(msg, name) {
			t.Errorf("pair message %q does not name %s", msg, name)
		}
	}

	if n := count(t, db, `SELECT COUNT(*) FROM config_lint_findings
		WHERE rule = ? AND severity = 'warn' AND resolved_at IS NULL`, RuleSessionSkillLoadHigh); n != 1 {
		t.Errorf("session_skill_load_high actives = %d, want 1", n)
	}
	if n := activeCount(t, db, "project:1", RuleSessionSkillLoadHigh); n != 1 {
		t.Errorf("alpha (p95 3 > 2) not flagged")
	}

	// Raise the limit: the project finding resolves, the others stay put.
	s2 := New(db, Config{ClaudeDir: cfg.ClaudeDir, SkillSimilarityThreshold: 0.8, SessionSkillLoadP95: 5}, nil)
	if _, err := s2.Lint(); err != nil {
		t.Fatal(err)
	}
	if n := activeCount(t, db, "project:1", RuleSessionSkillLoadHigh); n != 0 {
		t.Errorf("alpha still flagged at limit 5")
	}
	if n := activeCount(t, db, "skill_pair:6:7", RuleSkillDescSimilar); n != 1 {
		t.Errorf("pair finding lost on a re-lint")
	}
}

// With no Config override the thresholds come from guards.json: 0.8 flags the
// identical pair, and alpha's p95 of 3 is under the shipped limit.
func TestSkillAuditLintReadsGuards(t *testing.T) {
	t.Setenv(guards.EnvOverride, "")
	g, err := guards.Defaults()
	if err != nil {
		t.Fatal(err)
	}
	if g.SessionSkillLoadP95 < 3 || g.SkillSimilarityThreshold > 1 {
		t.Fatalf("fixture assumes p95 limit >= 3 and threshold <= 1, guards has %d / %.2f",
			g.SessionSkillLoadP95, g.SkillSimilarityThreshold)
	}
	db := auditDB(t)
	if _, err := New(db, Config{ClaudeDir: t.TempDir()}, nil).Lint(); err != nil {
		t.Fatal(err)
	}
	if n := activeRule(t, db, RuleSkillDescSimilar); n != 1 {
		t.Errorf("skill_desc_similar = %d, want 1 at the shipped threshold", n)
	}
	if n := activeRule(t, db, RuleSessionSkillLoadHigh); n != 0 {
		t.Errorf("session_skill_load_high = %d, want 0 under the shipped limit", n)
	}
}

// A broken guards override is loud and never a fallback: the two threshold
// rules are left exactly as they were, the usage rules still sync.
func TestSkillAuditBrokenGuardsSkipsThresholdRules(t *testing.T) {
	bad := filepath.Join(t.TempDir(), "guards.json")
	if err := os.WriteFile(bad, []byte(`{"stallMinutes": 5}`), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv(guards.EnvOverride, bad)
	db := auditDB(t)
	mustExec(t, db, `INSERT INTO config_lint_findings (target, rule, severity, message, detected_at)
		VALUES ('project:9', ?, 'warn', 'from an earlier pass', '2026-10-01T00:00:00Z')`, RuleSessionSkillLoadHigh)

	st, err := New(db, Config{ClaudeDir: t.TempDir()}, nil).Lint()
	if err != nil {
		t.Fatalf("lint must not fail on a broken guards override: %v", err)
	}
	if n := activeCount(t, db, "project:9", RuleSessionSkillLoadHigh); n != 1 {
		t.Errorf("an unevaluated rule resolved its earlier row")
	}
	if n := activeRule(t, db, RuleSkillDescSimilar); n != 0 {
		t.Errorf("skill_desc_similar evaluated against a guessed threshold")
	}
	if st.PerRule[RuleSkillDead] != 2 {
		t.Errorf("skill_dead = %d, want 2 — usage rules need no threshold", st.PerRule[RuleSkillDead])
	}
}

func TestDescTokensAndJaccard(t *testing.T) {
	a := descTokens("Use this skill when you need to export the report to a PDF.")
	want := map[string]bool{"need": true, "export": true, "report": true, "pdf": true}
	if !reflect.DeepEqual(a, want) {
		t.Errorf("tokens = %v, want %v (stopwords and boilerplate dropped)", a, want)
	}
	b := descTokens("Export a report as PDF")
	if got := jaccard(a, b); got != 0.75 {
		t.Errorf("jaccard = %v, want 0.75 (3 shared of 4)", got)
	}
	if got := jaccard(descTokens(""), descTokens("")); got != 0 {
		t.Errorf("two empty descriptions compare as %v, want 0", got)
	}
}

func TestP95NearestRank(t *testing.T) {
	cases := []struct {
		in   []int
		want int
	}{
		{nil, 0},
		{[]int{4}, 4},
		{[]int{3, 1, 2}, 3},
		{[]int{1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 9}, 1}, // 20 samples: rank 19
		{[]int{1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 9, 9}, 9},
	}
	for _, c := range cases {
		if got := p95(append([]int(nil), c.in...)); got != c.want {
			t.Errorf("p95(%v) = %d, want %d", c.in, got, c.want)
		}
	}
}

func TestLatestSkillAuditReport(t *testing.T) {
	root := t.TempDir()
	dir := SkillAuditReportDir(root)
	if _, _, ok, err := LatestSkillAuditReport(dir); ok || err != nil {
		t.Fatalf("missing dir = ok %v err %v, want no report and no error", ok, err)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"skill-audit-2026-10-19.md", "skill-audit-2026-10-26.md", "skill-audit-notes.md", "other-2026-12-01.md"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("# x\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	path, date, ok, err := LatestSkillAuditReport(dir)
	if err != nil || !ok {
		t.Fatalf("latest: ok %v err %v", ok, err)
	}
	if date != "2026-10-26" || filepath.Base(path) != "skill-audit-2026-10-26.md" {
		t.Errorf("latest = %s (%s), want the 2026-10-26 report", path, date)
	}
	if want := filepath.Join(root, "swarmery", "workspace", "retro"); dir != want {
		t.Errorf("report dir = %s, want %s", dir, want)
	}
}
