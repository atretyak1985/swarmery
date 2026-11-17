package sysscan

import (
	"database/sql"
	"fmt"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/store"
)

const harnessFixtureRoot = "../../testdata/harnesslint"

// setupHarness copies the harness-lint fixture (a plugin skill with present and
// missing helpers, a project with a local skill and a CLAUDE.md) into a temp
// dir, scans it, and returns the DB, config, root and project id.
func setupHarness(t *testing.T) (*sql.DB, Config, string, int64) {
	t.Helper()
	root := t.TempDir()
	copyTree(t, harnessFixtureRoot, root)
	db, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	res, err := db.Exec(`INSERT INTO projects (path, slug, name, first_seen) VALUES (?, 'project', 'project', '2026-07-13T00:00:00Z')`,
		filepath.Join(root, "project"))
	if err != nil {
		t.Fatal(err)
	}
	pid, _ := res.LastInsertId()
	cfg := Config{ClaudeDir: filepath.Join(root, "claude"), RescanInterval: time.Hour}
	if _, err := New(db, cfg, nil).Scan(); err != nil {
		t.Fatalf("scan: %v", err)
	}
	return db, cfg, root, pid
}

func refPaths(refs []HelperRef) []string {
	out := []string{}
	for _, r := range refs {
		out = append(out, r.Path)
	}
	return out
}

func TestHelperRefsShapes(t *testing.T) {
	body := []byte(strings.Join([]string{
		"Read [guide](resources/guide.md#part) and [again](resources/guide.md).",
		"Run `scripts/run.sh --flag`, `./local.md`, `bin/tool`, `templates/t.md`.",
		"Also `bash ${CLAUDE_PLUGIN_ROOT}/bin/x.sh` and `${CLAUDE_PLUGIN_ROOT}/resources/y.md`.",
		"Ignore [web](https://example.com/a.md), [mail](mailto:a@b.c), [abs](/etc/hosts), [home](~/x.md),",
		"[anchor](#top), ![img](data:image/png;base64,AA), `resources/<name>.md`, `templates/{kind}.md`,",
		"`scripts/`, `scripts/*.sh`, `$HOME/x`, `resources/...`, `docs/not-a-prefix.md`, `go test ./...`.",
		"```bash",
		"bash scripts/in-fence.sh",
		"cat ${CLAUDE_PLUGIN_ROOT}/bin/fenced.sh",
		"[fenced link](resources/fenced.md)",
		"```",
	}, "\n"))
	got := HelperRefs(body)
	want := []string{
		"resources/guide.md",
		"scripts/run.sh", "./local.md", "bin/tool", "templates/t.md",
		"${CLAUDE_PLUGIN_ROOT}/bin/x.sh", "${CLAUDE_PLUGIN_ROOT}/resources/y.md",
		"${CLAUDE_PLUGIN_ROOT}/bin/fenced.sh",
	}
	if !reflect.DeepEqual(refPaths(got), want) {
		t.Fatalf("HelperRefs = %v\nwant        %v", refPaths(got), want)
	}
	strict := map[string]bool{}
	for _, r := range got {
		strict[r.Path] = r.Strict
	}
	for path, want := range map[string]bool{
		"resources/guide.md":                  true,  // link
		"templates/t.md":                      true,  // bundled dir
		"${CLAUDE_PLUGIN_ROOT}/bin/x.sh":      true,  // plugin root
		"scripts/run.sh":                      false, // may be the working repo's
		"./local.md":                          false,
		"bin/tool":                            false,
		"${CLAUDE_PLUGIN_ROOT}/bin/fenced.sh": true,
	} {
		if strict[path] != want {
			t.Errorf("%s strict = %v, want %v", path, strict[path], want)
		}
	}
}

func TestMissingHelpersScopes(t *testing.T) {
	root := t.TempDir()
	plugin := filepath.Join(root, "plugin")
	skill := filepath.Join(plugin, "skills", "s")
	writeTestFile(t, filepath.Join(skill, "resources", "here.md"), "x")
	writeTestFile(t, filepath.Join(plugin, "bin", "tool.sh"), "x")
	writeTestFile(t, filepath.Join(plugin, "shared.md"), "x")
	writeTestFile(t, filepath.Join(root, "outside.md"), "x")

	body := []byte("[a](resources/here.md) [b](shared.md) [c](../../shared.md) [d](../../../outside.md) " +
		"`${CLAUDE_PLUGIN_ROOT}/bin/tool.sh` `${CLAUDE_PLUGIN_ROOT}/bin/nope.sh` `scripts/consumer.sh`")
	got := MissingHelpers(HelperScope{SkillDir: skill, PluginRoot: plugin}, body)
	want := []string{"../../../outside.md", "${CLAUDE_PLUGIN_ROOT}/bin/nope.sh"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("plugin scope missing = %v, want %v", got, want)
	}

	// A non-plugin skill: the boundary is the skill dir, ${CLAUDE_PLUGIN_ROOT}
	// is not judged, a link out of the dir does not resolve.
	got = MissingHelpers(HelperScope{SkillDir: skill}, body)
	want = []string{"shared.md", "../../shared.md", "../../../outside.md"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("skill-dir scope missing = %v, want %v", got, want)
	}

	// A loose ref is judged once the skill ships the directory it names.
	writeTestFile(t, filepath.Join(skill, "scripts", "other.sh"), "x")
	got = MissingHelpers(HelperScope{SkillDir: skill, PluginRoot: plugin}, []byte("`scripts/consumer.sh`"))
	if !reflect.DeepEqual(got, []string{"scripts/consumer.sh"}) {
		t.Errorf("shipped scripts/: missing = %v, want [scripts/consumer.sh]", got)
	}
}

// TestLintSkillMissingHelper: the plugin fixture skill gets one warn finding
// naming exactly its missing helpers; the project skill (link resolved under the
// project root) gets none; adding the files resolves the finding.
func TestLintSkillMissingHelper(t *testing.T) {
	db, cfg, root, _ := setupHarness(t)
	s := New(db, cfg, nil)
	if _, err := s.Lint(); err != nil {
		t.Fatalf("lint: %v", err)
	}
	target := lintTarget(t, db, "skill", "skills", "helperpack:uses-helpers")
	var sev, msg string
	if err := db.QueryRow(`SELECT severity, message FROM config_lint_findings
		WHERE target = ? AND rule = ? AND resolved_at IS NULL`, target, RuleSkillMissingHelper).Scan(&sev, &msg); err != nil {
		t.Fatalf("no active %s on %s: %v", RuleSkillMissingHelper, target, err)
	}
	if sev != "warn" {
		t.Errorf("severity = %q, want warn", sev)
	}
	for _, want := range []string{"references 3 helper file(s)", "resources/missing.md", "scripts/absent.sh", "${CLAUDE_PLUGIN_ROOT}/bin/gone.sh"} {
		if !strings.Contains(msg, want) {
			t.Errorf("message lacks %q: %s", want, msg)
		}
	}
	for _, not := range []string{"present.md", "run.sh", "tool.sh", "consumer.sh", "also-not-judged", "example.com", "<name>", "data:"} {
		if strings.Contains(msg, not) {
			t.Errorf("message names %q, which resolves or is not a helper: %s", not, msg)
		}
	}
	if n := activeRule(t, db, RuleSkillMissingHelper); n != 1 {
		t.Errorf("active %s = %d, want 1 (the project skill's link resolves)", RuleSkillMissingHelper, n)
	}

	skillDir := filepath.Join(root, "claude", "plugins", "cache", "mkt", "helperpack", "1.0.0", "skills", "uses-helpers")
	writeTestFile(t, filepath.Join(skillDir, "resources", "missing.md"), "now here")
	writeTestFile(t, filepath.Join(skillDir, "scripts", "absent.sh"), "now here")
	writeTestFile(t, filepath.Join(skillDir, "..", "..", "bin", "gone.sh"), "now here")
	st, err := s.Lint()
	if err != nil {
		t.Fatalf("lint: %v", err)
	}
	if n := activeCount(t, db, target, RuleSkillMissingHelper); n != 0 {
		t.Errorf("finding still active after the helpers were added")
	}
	if st.PerRule[RuleSkillMissingHelper] != 0 || st.Resolved < 1 {
		t.Errorf("stats = %+v, want 0 active and >= 1 resolved", st)
	}
	if n := count(t, db, `SELECT COUNT(*) FROM config_lint_findings WHERE target = ? AND rule = ? AND resolved_at IS NOT NULL`,
		target, RuleSkillMissingHelper); n != 1 {
		t.Errorf("resolved history rows = %d, want 1", n)
	}
}

var harnessSessionSeq int

// seedFirstRequest inserts a recent two-turn session whose first assistant
// request carries a whole prompt of `prompt` tokens.
func seedFirstRequest(t *testing.T, db *sql.DB, pid, prompt int64) {
	t.Helper()
	harnessSessionSeq++
	started := time.Now().Add(-24 * time.Hour).UTC()
	res, err := db.Exec(`INSERT INTO sessions (project_id, session_uuid, started_at) VALUES (?, ?, ?)`,
		pid, fmt.Sprintf("h-%d", harnessSessionSeq), started.Format(time.RFC3339Nano))
	if err != nil {
		t.Fatal(err)
	}
	sid, _ := res.LastInsertId()
	ts := started.Format(time.RFC3339Nano)
	mustExec(t, db, `INSERT INTO turns (session_id, seq, role, started_at, tokens_in) VALUES (?, 1, 'user', ?, 0)`, sid, ts)
	mustExec(t, db, `INSERT INTO turns (session_id, seq, role, started_at, model, tokens_in, tokens_cache_read,
		cache_write_5m_tokens, cache_write_1h_tokens) VALUES (?, 2, 'assistant', ?, 'claude-opus-5-5', 10, ?, 0, 0)`,
		sid, ts, prompt-10)
}

func budgetRow(t *testing.T, db *sql.DB, target string) (sev, msg string, ok bool) {
	t.Helper()
	err := db.QueryRow(`SELECT severity, message FROM config_lint_findings
		WHERE target = ? AND rule = ? AND resolved_at IS NULL`, target, RuleAlwaysOnBudget).Scan(&sev, &msg)
	if err == sql.ErrNoRows {
		return "", "", false
	}
	if err != nil {
		t.Fatal(err)
	}
	return sev, msg, true
}

// TestLintAlwaysOnBudget: with no sessions the rule falls back to the estimate
// (labelled so); with sessions it judges the measured median — info past the
// info budget, warn past the warn budget, resolved under both. The message
// lists the top contributors and points at the First request card.
func TestLintAlwaysOnBudget(t *testing.T) {
	db, cfg, root, pid := setupHarness(t)
	target := AlwaysOnTarget(filepath.Join(root, "project"))

	// Estimate fallback: the fixture's CLAUDE.md + local skill listing is a few
	// dozen tokens — over a 5-token info budget, under a 1000-token warn.
	cfg.AlwaysOnInfoTokens, cfg.AlwaysOnWarnTokens = 5, 1000
	if _, err := New(db, cfg, nil).Lint(); err != nil {
		t.Fatalf("lint: %v", err)
	}
	sev, msg, ok := budgetRow(t, db, target)
	if !ok || sev != "info" {
		t.Fatalf("estimate fallback: active=%v severity=%q, want info", ok, sev)
	}
	for _, want := range []string{"estimate (~bytes/4)", "no sessions", "top contributors: ", "claude md project/CLAUDE.md", "skill local-skill", "First request card"} {
		if !strings.Contains(msg, want) {
			t.Errorf("estimate message lacks %q: %s", want, msg)
		}
	}

	// Measured: median of 40k, 60k, 70k = 60k.
	for _, p := range []int64{40000, 60000, 70000} {
		seedFirstRequest(t, db, pid, p)
	}
	for _, tc := range []struct {
		info, warn int
		want       string // "" = no active finding
	}{
		{info: 50000, warn: 80000, want: "info"},
		{info: 30000, warn: 55000, want: "warn"},
		{info: 60000, warn: 90000, want: ""}, // at the budget is not over it
	} {
		cfg.AlwaysOnInfoTokens, cfg.AlwaysOnWarnTokens = tc.info, tc.warn
		if _, err := New(db, cfg, nil).Lint(); err != nil {
			t.Fatalf("lint: %v", err)
		}
		sev, msg, ok := budgetRow(t, db, target)
		if tc.want == "" {
			if ok {
				t.Errorf("info=%d warn=%d: still active (%s) at a 60k median", tc.info, tc.warn, sev)
			}
			continue
		}
		if !ok || sev != tc.want {
			t.Errorf("info=%d warn=%d: active=%v severity=%q, want %s", tc.info, tc.warn, ok, sev, tc.want)
			continue
		}
		for _, want := range []string{"~60000 tokens", "measured median first request over 3 session(s)", "top contributors: "} {
			if !strings.Contains(msg, want) {
				t.Errorf("measured message lacks %q: %s", want, msg)
			}
		}
	}
	if n := count(t, db, `SELECT COUNT(*) FROM config_lint_findings WHERE target = ? AND rule = ? AND resolved_at IS NOT NULL`,
		target, RuleAlwaysOnBudget); n < 1 {
		t.Errorf("no resolved history row after the median fell under the budget")
	}
}

func TestAlwaysOnThresholdDefaultsAndEnv(t *testing.T) {
	c := Config{}.withDefaults()
	if c.AlwaysOnInfoTokens != DefaultAlwaysOnInfoTokens || c.AlwaysOnWarnTokens != DefaultAlwaysOnWarnTokens {
		t.Errorf("defaults = %d/%d, want %d/%d", c.AlwaysOnInfoTokens, c.AlwaysOnWarnTokens, DefaultAlwaysOnInfoTokens, DefaultAlwaysOnWarnTokens)
	}
	if DefaultAlwaysOnInfoTokens != 50000 || DefaultAlwaysOnWarnTokens != 80000 {
		t.Errorf("documented defaults drifted: %d/%d", DefaultAlwaysOnInfoTokens, DefaultAlwaysOnWarnTokens)
	}
	t.Setenv(EnvAlwaysOnInfoTokens, "1234")
	t.Setenv(EnvAlwaysOnWarnTokens, "999") // below info: clamped up to it
	c = Config{}.withDefaults()
	if c.AlwaysOnInfoTokens != 1234 || c.AlwaysOnWarnTokens != 1234 {
		t.Errorf("env = %d/%d, want 1234/1234", c.AlwaysOnInfoTokens, c.AlwaysOnWarnTokens)
	}
	// An explicit Config value beats the env.
	c = Config{AlwaysOnInfoTokens: 7, AlwaysOnWarnTokens: 9}.withDefaults()
	if c.AlwaysOnInfoTokens != 7 || c.AlwaysOnWarnTokens != 9 {
		t.Errorf("explicit = %d/%d, want 7/9", c.AlwaysOnInfoTokens, c.AlwaysOnWarnTokens)
	}
}
