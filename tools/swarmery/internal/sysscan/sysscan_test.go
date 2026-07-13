package sysscan

import (
	"context"
	"database/sql"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/ingest"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/store"
)

const fixtureRoot = "../../testdata/sysconfig"

// setup copies the committed fixture tree into a temp dir (tests mutate
// files), opens a migrated temp DB, and registers the fixture project plus
// the '(unknown)' placeholder row the scanner must skip (format doc §0).
func setup(t *testing.T) (*sql.DB, Config, string) {
	t.Helper()
	root := t.TempDir()
	copyTree(t, fixtureRoot, root)

	db, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	projectPath := filepath.Join(root, "project")
	mustExec(t, db, `INSERT INTO projects (path, slug, name, first_seen) VALUES (?, 'project', 'project', '2026-07-13T00:00:00Z')`, projectPath)
	mustExec(t, db, `INSERT INTO projects (path, slug, name, first_seen) VALUES ('(unknown)', '-unknown-', NULL, '2026-07-13T00:00:00Z')`)
	mustExec(t, db, `INSERT INTO projects (path, slug, name, first_seen, archived) VALUES ('/nonexistent/archived-project', 'arch', 'arch', '2026-07-13T00:00:00Z', 1)`)

	cfg := Config{ClaudeDir: filepath.Join(root, "claude"), RescanInterval: time.Hour}
	return db, cfg, root
}

func copyTree(t *testing.T, src, dst string) {
	t.Helper()
	err := filepath.WalkDir(src, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(target, raw, 0o644)
	})
	if err != nil {
		t.Fatalf("copy fixture: %v", err)
	}
}

func mustExec(t *testing.T, db *sql.DB, q string, args ...any) {
	t.Helper()
	if _, err := db.Exec(q, args...); err != nil {
		t.Fatalf("exec %s: %v", q, err)
	}
}

func count(t *testing.T, db *sql.DB, q string, args ...any) int {
	t.Helper()
	var n int
	if err := db.QueryRow(q, args...).Scan(&n); err != nil {
		t.Fatalf("count %s: %v", q, err)
	}
	return n
}

func TestScanCounts(t *testing.T) {
	db, cfg, _ := setup(t)
	st, err := New(db, cfg, nil).Scan()
	if err != nil {
		t.Fatalf("scan: %v", err)
	}

	// Per-source counts. Global agents: global-agent, x, broken-agent,
	// nested/deep-agent, lint-poor (README.md skipped — no frontmatter).
	// Project agents: proj-agent + x (the agent_name_duplicate fixture).
	if st.Agents != (SourceCounts{Global: 5, Project: 2, Plugin: 1}) {
		t.Errorf("agents counts = %+v, want {5 2 1}", st.Agents)
	}
	// Global skills: global-skill + short-desc (the lint fixture).
	if st.Skills != (SourceCounts{Global: 2, Project: 0, Plugin: 1}) {
		t.Errorf("skills counts = %+v, want {2 0 1}", st.Skills)
	}
	if st.Commands != (SourceCounts{Global: 1}) {
		t.Errorf("commands counts = %+v, want {1 0 0}", st.Commands)
	}
	// settings.json: PreToolUse + PostToolUse + Stop; project settings.local.json: PermissionRequest.
	if st.Hooks != 4 || st.HooksManaged != 2 {
		t.Errorf("hooks = %d (managed %d), want 4 (managed 2)", st.Hooks, st.HooksManaged)
	}
	if st.ParseErrors != 1 {
		t.Errorf("parse errors = %d, want 1 (broken-agent)", st.ParseErrors)
	}

	if n := count(t, db, `SELECT COUNT(*) FROM agents WHERE deleted = 0`); n != 8 {
		t.Errorf("agents rows = %d, want 8", n)
	}
	// README.md never registers.
	if n := count(t, db, `SELECT COUNT(*) FROM agents WHERE name = 'README'`); n != 0 {
		t.Errorf("README.md registered as an agent")
	}
	// Every agent has a current version whose content is the full file.
	if n := count(t, db, `SELECT COUNT(*) FROM agents WHERE current_version_id IS NULL`); n != 0 {
		t.Errorf("%d agents without current_version_id", n)
	}
	if n := count(t, db, `SELECT COUNT(*) FROM agent_versions`); n != 8 {
		t.Errorf("agent_versions rows = %d, want 8 (one per agent)", n)
	}
}

func TestPluginOriginAndCollision(t *testing.T) {
	db, cfg, _ := setup(t)
	if _, err := New(db, cfg, nil).Scan(); err != nil {
		t.Fatalf("scan: %v", err)
	}

	// Plugin agent is stored under the composite name with provenance.
	var origin, pluginName, filePath string
	err := db.QueryRow(`SELECT origin, plugin_name, file_path FROM agents WHERE name = 'toolpack:x'`).
		Scan(&origin, &pluginName, &filePath)
	if err != nil {
		t.Fatalf("toolpack:x row: %v", err)
	}
	if origin != "plugin" || pluginName != "toolpack" {
		t.Errorf("toolpack:x origin=%q plugin_name=%q, want plugin/toolpack", origin, pluginName)
	}
	// The .in_use marker on 1.0.0 must beat the 0.9.0 decoy (§5.1).
	if !strings.Contains(filePath, string(os.PathSeparator)+"1.0.0"+string(os.PathSeparator)) {
		t.Errorf("toolpack:x file_path = %q, want the 1.0.0 version dir", filePath)
	}

	// Collision regression: local agents `x` (global + project scope) coexist
	// with the plugin's `toolpack:x`.
	if n := count(t, db, `SELECT COUNT(*) FROM agents WHERE name IN ('x', 'toolpack:x') AND deleted = 0`); n != 3 {
		t.Errorf("x + toolpack:x rows = %d, want 3 (coexisting)", n)
	}
	var localOrigin string
	if err := db.QueryRow(`SELECT origin FROM agents WHERE name = 'x' AND scope = 'global'`).Scan(&localOrigin); err != nil || localOrigin != "local" {
		t.Errorf("global x origin = %q (%v), want local", localOrigin, err)
	}

	// Plugin skill: composite name; workflow-skills/ is never scanned (§2.2).
	if n := count(t, db, `SELECT COUNT(*) FROM skills WHERE name = 'toolpack:y' AND origin = 'plugin' AND plugin_name = 'toolpack'`); n != 1 {
		t.Errorf("toolpack:y plugin skill row missing")
	}
	if n := count(t, db, `SELECT COUNT(*) FROM skills WHERE name LIKE '%z'`); n != 0 {
		t.Errorf("workflow-skills/z was scanned — plugins must scan skills/ only")
	}

	// Command: name = file stem, global scope.
	if n := count(t, db, `SELECT COUNT(*) FROM commands WHERE name = 'deploy' AND scope = 'global' AND origin = 'local'`); n != 1 {
		t.Errorf("deploy command row missing")
	}
}

func TestHooksRows(t *testing.T) {
	db, cfg, root := setup(t)
	if _, err := New(db, cfg, nil).Scan(); err != nil {
		t.Fatalf("scan: %v", err)
	}

	// Entry WITHOUT timeout → NULL column.
	var timeout sql.NullInt64
	err := db.QueryRow(`SELECT timeout FROM hooks WHERE event = 'PostToolUse'`).Scan(&timeout)
	if err != nil {
		t.Fatalf("PostToolUse row: %v", err)
	}
	if timeout.Valid {
		t.Errorf("PostToolUse timeout = %v, want NULL", timeout.Int64)
	}

	// Entry with timeout + statusMessage.
	var to int64
	var msg sql.NullString
	if err := db.QueryRow(`SELECT timeout, status_message FROM hooks WHERE event = 'PreToolUse'`).Scan(&to, &msg); err != nil {
		t.Fatalf("PreToolUse row: %v", err)
	}
	if to != 10 || !msg.Valid || msg.String != "Checking index" {
		t.Errorf("PreToolUse timeout=%d status_message=%v, want 10/'Checking index'", to, msg)
	}

	// swarmery-managed recognition (the hookcfg marker) + matcher NULL when absent.
	var managed sql.NullString
	var matcher sql.NullString
	if err := db.QueryRow(`SELECT managed, matcher FROM hooks WHERE event = 'Stop'`).Scan(&managed, &matcher); err != nil {
		t.Fatalf("Stop row: %v", err)
	}
	if !managed.Valid || managed.String != "swarmery" {
		t.Errorf("Stop managed = %v, want swarmery", managed)
	}
	if matcher.Valid {
		t.Errorf("Stop matcher = %q, want NULL (absent in JSON)", matcher.String)
	}

	// The project settings.local.json is a SEPARATE source_file row (§3.4).
	local := filepath.Join(root, "project", ".claude", "settings.local.json")
	if n := count(t, db, `SELECT COUNT(*) FROM hooks WHERE source_file = ? AND event = 'PermissionRequest' AND managed = 'swarmery' AND scope = 'project'`, local); n != 1 {
		t.Errorf("project settings.local.json PermissionRequest row missing")
	}

	// Rescan without changes keeps row ids stable (no delete-and-insert).
	var maxID int64
	if err := db.QueryRow(`SELECT MAX(id) FROM hooks`).Scan(&maxID); err != nil {
		t.Fatal(err)
	}
	if _, err := New(db, cfg, nil).Scan(); err != nil {
		t.Fatalf("rescan: %v", err)
	}
	var maxID2 int64
	if err := db.QueryRow(`SELECT MAX(id) FROM hooks`).Scan(&maxID2); err != nil {
		t.Fatal(err)
	}
	if maxID2 != maxID {
		t.Errorf("hook row ids churned on a no-op rescan: max id %d → %d", maxID, maxID2)
	}
}

func TestVersionOnChangeAndNoopResave(t *testing.T) {
	db, cfg, _ := setup(t)
	s := New(db, cfg, nil)
	if _, err := s.Scan(); err != nil {
		t.Fatalf("scan: %v", err)
	}

	agentPath := filepath.Join(cfg.ClaudeDir, "agents", "global-agent.md")
	original, err := os.ReadFile(agentPath)
	if err != nil {
		t.Fatal(err)
	}

	versions := func() int {
		return count(t, db,
			`SELECT COUNT(*) FROM agent_versions v JOIN agents a ON a.id = v.agent_id WHERE a.name = 'global-agent'`)
	}
	if n := versions(); n != 1 {
		t.Fatalf("initial versions = %d, want 1", n)
	}

	// Edit → exactly 1 new version, current_version_id repointed.
	if err := os.WriteFile(agentPath, append(original, []byte("\nEdited line.\n")...), 0o644); err != nil {
		t.Fatal(err)
	}
	st, err := s.Scan()
	if err != nil {
		t.Fatalf("scan after edit: %v", err)
	}
	if n := versions(); n != 2 {
		t.Errorf("versions after edit = %d, want 2", n)
	}
	if st.NewVersions != 1 {
		t.Errorf("NewVersions = %d, want 1", st.NewVersions)
	}
	var curHash string
	if err := db.QueryRow(
		`SELECT v.content_hash FROM agents a JOIN agent_versions v ON v.id = a.current_version_id WHERE a.name = 'global-agent'`).
		Scan(&curHash); err != nil {
		t.Fatal(err)
	}
	edited, _ := os.ReadFile(agentPath)
	if curHash != sha256Hex(edited) {
		t.Errorf("current_version_id does not point at the edited content")
	}

	// No-op resave (same bytes) → 0 new versions.
	if err := os.WriteFile(agentPath, edited, 0o644); err != nil {
		t.Fatal(err)
	}
	st, err = s.Scan()
	if err != nil {
		t.Fatalf("scan after resave: %v", err)
	}
	if n := versions(); n != 2 {
		t.Errorf("versions after no-op resave = %d, want 2", n)
	}
	if st.NewVersions != 0 {
		t.Errorf("NewVersions after no-op resave = %d, want 0", st.NewVersions)
	}

	// Revert to the ORIGINAL content: UNIQUE(agent_id, content_hash) dedups —
	// still 2 rows, current repoints to the old version.
	if err := os.WriteFile(agentPath, original, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Scan(); err != nil {
		t.Fatalf("scan after revert: %v", err)
	}
	if n := versions(); n != 2 {
		t.Errorf("versions after revert = %d, want 2 (dedup)", n)
	}
}

func TestParseErrorDoesNotStopScan(t *testing.T) {
	db, cfg, _ := setup(t)
	st, err := New(db, cfg, nil).Scan()
	if err != nil {
		t.Fatalf("scan returned error despite tolerant contract: %v", err)
	}
	if st.ParseErrors != 1 {
		t.Fatalf("parse errors = %d, want 1", st.ParseErrors)
	}

	// The broken item is KEPT (name = file stem) …
	var id int64
	if err := db.QueryRow(`SELECT id FROM agents WHERE name = 'broken-agent' AND deleted = 0`).Scan(&id); err != nil {
		t.Fatalf("broken-agent row missing: %v", err)
	}
	// … with exactly one unresolved parse_error finding.
	if n := count(t, db,
		`SELECT COUNT(*) FROM config_lint_findings WHERE target = 'agent:' || ? AND rule = 'parse_error' AND severity = 'error' AND resolved_at IS NULL`,
		id); n != 1 {
		t.Errorf("parse_error findings for broken-agent = %d, want 1", n)
	}

	// Rescan must not pile up duplicate findings.
	if _, err := New(db, cfg, nil).Scan(); err != nil {
		t.Fatal(err)
	}
	if n := count(t, db, `SELECT COUNT(*) FROM config_lint_findings WHERE rule = 'parse_error'`); n != 1 {
		t.Errorf("findings after rescan = %d, want 1 (no duplicates)", n)
	}
}

func TestBrokenSettingsJSONKeepsRows(t *testing.T) {
	db, cfg, root := setup(t)
	s := New(db, cfg, nil)
	if _, err := s.Scan(); err != nil {
		t.Fatal(err)
	}
	local := filepath.Join(root, "project", ".claude", "settings.local.json")
	before := count(t, db, `SELECT COUNT(*) FROM hooks WHERE source_file = ?`, local)
	if before != 1 {
		t.Fatalf("precondition: %d rows for settings.local.json, want 1", before)
	}

	if err := os.WriteFile(local, []byte(`{ not json`), 0o644); err != nil {
		t.Fatal(err)
	}
	st, err := s.Scan()
	if err != nil {
		t.Fatalf("scan with broken settings JSON: %v", err)
	}
	if n := count(t, db, `SELECT COUNT(*) FROM hooks WHERE source_file = ?`, local); n != before {
		t.Errorf("rows after broken JSON = %d, want %d (kept)", n, before)
	}
	// Exactly 1: the settings file. broken-agent is hash-skipped on rescans
	// (unchanged content never re-parses), so it does not re-count here.
	if st.ParseErrors != 1 {
		t.Errorf("ParseErrors = %d, want 1 (broken settings only)", st.ParseErrors)
	}
	if n := count(t, db, `SELECT COUNT(*) FROM config_lint_findings WHERE target = ? AND rule = 'parse_error' AND resolved_at IS NULL`,
		"hooks:"+local); n != 1 {
		t.Errorf("settings parse_error finding missing")
	}
}

func TestDeletedOnVanishAndRestore(t *testing.T) {
	db, cfg, root := setup(t)
	s := New(db, cfg, nil)
	if _, err := s.Scan(); err != nil {
		t.Fatal(err)
	}

	projAgent := filepath.Join(root, "project", ".claude", "agents", "proj-agent.md")
	content, err := os.ReadFile(projAgent)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(projAgent); err != nil {
		t.Fatal(err)
	}

	st, err := s.Scan()
	if err != nil {
		t.Fatal(err)
	}
	if st.Deleted != 1 {
		t.Errorf("Deleted = %d, want 1", st.Deleted)
	}
	var deleted int
	if err := db.QueryRow(`SELECT deleted FROM agents WHERE name = 'proj-agent'`).Scan(&deleted); err != nil {
		t.Fatalf("proj-agent row vanished physically: %v", err)
	}
	if deleted != 1 {
		t.Errorf("proj-agent deleted = %d, want 1 (soft delete)", deleted)
	}
	// Versions survive the soft delete (rollback source).
	if n := count(t, db,
		`SELECT COUNT(*) FROM agent_versions v JOIN agents a ON a.id = v.agent_id WHERE a.name = 'proj-agent'`); n != 1 {
		t.Errorf("proj-agent versions = %d, want 1 (kept)", n)
	}

	// Restore → undelete, still one version (content unchanged).
	if err := os.WriteFile(projAgent, content, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Scan(); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT deleted FROM agents WHERE name = 'proj-agent'`).Scan(&deleted); err != nil {
		t.Fatal(err)
	}
	if deleted != 0 {
		t.Errorf("proj-agent deleted = %d after restore, want 0", deleted)
	}
}

func TestBusNotifications(t *testing.T) {
	db, cfg, _ := setup(t)
	bus := ingest.NewBus()
	ch, cancel := bus.Subscribe(1024)
	defer cancel()

	s := New(db, cfg, bus)
	if _, err := s.Scan(); err != nil {
		t.Fatal(err)
	}
	notes := drain(ch)
	if len(notes) == 0 {
		t.Fatal("first scan published no system_item_updated notes")
	}
	kinds := map[string]bool{}
	for _, n := range notes {
		if n.Type != ingest.NoteSystemItemUpdated {
			t.Fatalf("unexpected note type %q", n.Type)
		}
		if n.ItemID <= 0 {
			t.Fatalf("note without ItemID: %+v", n)
		}
		kinds[n.Kind] = true
	}
	for _, k := range []string{KindAgent, KindSkill, KindHook, KindCommand} {
		if !kinds[k] {
			t.Errorf("no note published for kind %q", k)
		}
	}

	// Converged: a no-op rescan publishes nothing.
	if _, err := s.Scan(); err != nil {
		t.Fatal(err)
	}
	if again := drain(ch); len(again) != 0 {
		t.Errorf("no-op rescan published %d notes, want 0", len(again))
	}
}

func drain(ch <-chan ingest.Notification) []ingest.Notification {
	var out []ingest.Notification
	for {
		select {
		case n := <-ch:
			out = append(out, n)
		default:
			return out
		}
	}
}

func TestRunWatchPicksUpEdit(t *testing.T) {
	db, cfg, _ := setup(t)
	cfg.RescanInterval = 100 * time.Millisecond // fallback keeps the test deterministic

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = New(db, cfg, nil).Run(ctx)
	}()

	waitFor := func(desc string, cond func() bool) {
		t.Helper()
		deadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(deadline) {
			if cond() {
				return
			}
			time.Sleep(25 * time.Millisecond)
		}
		t.Fatalf("timeout waiting for %s", desc)
	}

	waitFor("initial scan", func() bool {
		return count(t, db, `SELECT COUNT(*) FROM agents WHERE deleted = 0`) == 8
	})

	agentPath := filepath.Join(cfg.ClaudeDir, "agents", "x.md")
	raw, err := os.ReadFile(agentPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(agentPath, append(raw, []byte("\nWatched edit.\n")...), 0o644); err != nil {
		t.Fatal(err)
	}
	waitFor("new version after watched edit", func() bool {
		return count(t, db,
			`SELECT COUNT(*) FROM agent_versions v JOIN agents a ON a.id = v.agent_id WHERE a.name = 'x'`) == 2
	})

	cancel()
	<-done
}

// TestCollisionPrefersInstalledMarketplace: when two marketplaces cache the
// same plugin name, the one recorded in installed_plugins.json must win over
// an orphaned cache — even when it loses alphabetically (agentry vs swarmery
// on the real machine, format doc §5.2 / §7).
func TestCollisionPrefersInstalledMarketplace(t *testing.T) {
	claudeDir := t.TempDir()
	for _, p := range []string{
		"plugins/cache/aaa-mkt/tool/1.0.0/agents",
		"plugins/cache/zzz-mkt/tool/2.0.0/agents",
	} {
		if err := os.MkdirAll(filepath.Join(claudeDir, p), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	warn := func(string, ...any) {}

	// No install record → alphabetical fallback keeps aaa-mkt.
	roots := pluginRoots(claudeDir, warn)
	if len(roots) != 1 || roots[0].marketplace != "aaa-mkt" {
		t.Fatalf("fallback roots = %+v, want single aaa-mkt", roots)
	}

	// tool@zzz-mkt installed → zzz-mkt shadows the orphaned aaa-mkt cache.
	installed := `{"version":2,"plugins":{"tool@zzz-mkt":[{"scope":"user"}]}}`
	if err := os.WriteFile(filepath.Join(claudeDir, "plugins", "installed_plugins.json"), []byte(installed), 0o644); err != nil {
		t.Fatal(err)
	}
	roots = pluginRoots(claudeDir, warn)
	if len(roots) != 1 || roots[0].marketplace != "zzz-mkt" {
		t.Fatalf("installed-preference roots = %+v, want single zzz-mkt", roots)
	}
	if !strings.Contains(roots[0].root, "2.0.0") {
		t.Errorf("root = %q, want the zzz-mkt 2.0.0 version dir", roots[0].root)
	}
}
