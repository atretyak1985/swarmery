package sysedit

// Step-10 safety tests: surgical settings.json hook toggle/edit. Everything
// runs inside t.TempDir() — no test ever touches the real ~/.claude,
// ~/.swarmery, or $HOME.
//
// The golden contract: the fixture is CANONICAL stdlib form (2-space indent,
// sorted keys, trailing newline — the shape hookcfg has always written), so
// every edit must leave all foreign sections byte-for-byte identical and a
// disable→enable roundtrip must restore the file byte-for-byte.

import (
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/store"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/sysscan"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/textdiff"
)

// hooksGolden is a realistic settings.json (structure copied from real files,
// stub values) in canonical form — TestHooksFixtureCanonical guards that.
const hooksGolden = `{
  "enabledPlugins": {
    "core@swarmery": true
  },
  "env": {
    "NODE_ENV": "development"
  },
  "hooks": {
    "PostToolUse": [
      {
        "hooks": [
          {
            "command": "node $CLAUDE_PROJECT_DIR/.claude/hooks/format-on-save.cjs",
            "statusMessage": "formatting",
            "timeout": 10,
            "type": "command"
          }
        ],
        "matcher": "Edit|Write"
      }
    ],
    "PreToolUse": [
      {
        "hooks": [
          {
            "command": "$CLAUDE_PROJECT_DIR/.claude/hooks/audit.sh",
            "type": "command"
          },
          {
            "command": "$CLAUDE_PROJECT_DIR/.claude/hooks/lint-gate.sh",
            "timeout": 5,
            "type": "command"
          }
        ],
        "matcher": "Bash"
      }
    ],
    "SessionStart": [
      {
        "hooks": [
          {
            "command": "$CLAUDE_PROJECT_DIR/.claude/hooks/session-start.sh",
            "type": "command"
          }
        ],
        "matcher": "startup|clear|compact"
      }
    ]
  },
  "model": "claude-fable-5",
  "permissions": {
    "allow": [
      "Bash(npm test)",
      "Read(**)"
    ]
  },
  "statusLine": {
    "command": "$CLAUDE_PROJECT_DIR/.claude/statusline.sh",
    "type": "command"
  },
  "unknownFutureKey": {
    "nested": [
      1,
      2
    ]
  }
}
`

// hooksLocalGolden is a settings.local.json with a swarmery-managed entry
// (hookcfg install shape) plus one foreign entry — canonical form.
const hooksLocalGolden = `{
  "hooks": {
    "PermissionRequest": [
      {
        "hooks": [
          {
            "command": "/u/.swarmery/bin/swarmery hook permission-request",
            "timeout": 130,
            "type": "command"
          }
        ],
        "matcher": "*"
      }
    ],
    "Stop": [
      {
        "hooks": [
          {
            "command": "my-custom-audit.sh",
            "type": "command"
          }
        ]
      }
    ]
  }
}
`

// setupHooks builds an isolated world: one project with the given settings
// file on disk, a migrated tmp DB converged by one scan pass, and an editor.
func setupHooks(t *testing.T, fileName, content string) (*Editor, *sql.DB, string) {
	t.Helper()
	root := t.TempDir()
	project := filepath.Join(root, "proj")
	path := filepath.Join(project, ".claude", fileName)
	mustWrite(t, path, content)

	db, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	if _, err := db.Exec(`INSERT INTO projects (id, path, slug, first_seen)
	                      VALUES (1, ?, 'proj', '2026-07-13T00:00:00Z')`, project); err != nil {
		t.Fatalf("seed project: %v", err)
	}

	claude := filepath.Join(root, "claude") // empty user tier
	scanner := sysscan.New(db, sysscan.Config{ClaudeDir: claude, RescanInterval: time.Hour}, nil)
	if _, err := scanner.Scan(); err != nil {
		t.Fatalf("initial scan: %v", err)
	}
	ed := New(db, scanner, Config{ClaudeDir: claude, BackupsDir: filepath.Join(root, "backups")})
	return ed, db, path
}

// hookRow finds one hooks row by command substring.
func hookRow(t *testing.T, db *sql.DB, cmdLike string) (id int64, hash string, enabled bool) {
	t.Helper()
	var en int64
	err := db.QueryRow(`SELECT id, content_hash, enabled FROM hooks WHERE command LIKE ?`,
		"%"+cmdLike+"%").Scan(&id, &hash, &en)
	if err != nil {
		t.Fatalf("hook row %q: %v", cmdLike, err)
	}
	return id, hash, en == 1
}

// subtree renders one top-level key of a settings doc back to JSON for
// byte-comparison across an edit.
func subtree(t *testing.T, doc map[string]any, key string) string {
	t.Helper()
	b, err := json.MarshalIndent(doc[key], "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func parseDoc(t *testing.T, raw string) map[string]any {
	t.Helper()
	var doc map[string]any
	if err := json.Unmarshal([]byte(raw), &doc); err != nil {
		t.Fatalf("parse: %v", err)
	}
	return doc
}

// hunkCount counts unified-diff hunk headers.
func hunkCount(diff string) int {
	n := 0
	for _, line := range strings.Split(diff, "\n") {
		if strings.HasPrefix(line, "@@ ") {
			n++
		}
	}
	return n
}

// TestHooksFixtureCanonical guards the golden premise: both fixtures survive
// the canonical stdlib roundtrip byte-for-byte. If this fails every other
// byte-comparison in the file is meaningless.
func TestHooksFixtureCanonical(t *testing.T) {
	for name, fixture := range map[string]string{"settings": hooksGolden, "local": hooksLocalGolden} {
		var doc map[string]any
		if err := json.Unmarshal([]byte(fixture), &doc); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		out, err := json.MarshalIndent(doc, "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		if string(out)+"\n" != fixture {
			t.Errorf("%s fixture is not canonical:\n--- want\n%s\n--- got\n%s\n", name, fixture, out)
		}
	}
}

// ── golden: disable touches ONLY the expected node ───────────────────────────

func TestToggleHookDisableGolden(t *testing.T) {
	ed, db, path := setupHooks(t, "settings.json", hooksGolden)
	id, hash, enabled := hookRow(t, db, "lint-gate.sh")
	if !enabled {
		t.Fatal("fixture row should start enabled")
	}

	if err := ed.ToggleHook(id, false, hash); err != nil {
		t.Fatalf("toggle: %v", err)
	}
	after := mustRead(t, path)

	// Every foreign top-level section is byte-for-byte identical.
	before := parseDoc(t, hooksGolden)
	got := parseDoc(t, after)
	for _, key := range []string{"enabledPlugins", "env", "model", "permissions", "statusLine", "unknownFutureKey"} {
		if subtree(t, before, key) != subtree(t, got, key) {
			t.Errorf("top-level %q changed:\n%s", key, subtree(t, got, key))
		}
	}
	// Untouched events are byte-for-byte identical too.
	hb := before["hooks"].(map[string]any)
	ha := got["hooks"].(map[string]any)
	for _, ev := range []string{"PostToolUse", "SessionStart"} {
		if subtree(t, hb, ev) != subtree(t, ha, ev) {
			t.Errorf("hooks.%s changed:\n%s", ev, subtree(t, ha, ev))
		}
	}

	// The disabled record carries the verbatim entry + position context.
	recs, _ := got[sysscan.DisabledHooksKey].([]any)
	if len(recs) != 1 {
		t.Fatalf("want 1 disabled record, got %d", len(recs))
	}
	rec := recs[0].(map[string]any)
	if rec["event"] != "PreToolUse" || rec["matcher"] != "Bash" ||
		rec["groupIndex"] != float64(0) || rec["hookIndex"] != float64(1) {
		t.Errorf("record context wrong: %v", rec)
	}
	entry := rec["hook"].(map[string]any)
	if !strings.Contains(entry["command"].(string), "lint-gate.sh") ||
		entry["timeout"] != float64(5) || entry["type"] != "command" {
		t.Errorf("record entry not verbatim: %v", entry)
	}

	// Mechanically: exactly two changed regions — the disabled-section
	// insertion at the top (underscore sorts first) + the entry removal.
	if diff := textdiff.UnifiedDiff("before", "after", hooksGolden, after); hunkCount(diff) != 2 {
		t.Errorf("want exactly 2 hunks (insert + removal), got:\n%s", diff)
	}

	// Rescan converged the registry: the entry is now an enabled=0 row.
	_, _, nowEnabled := hookRow(t, db, "lint-gate.sh")
	if nowEnabled {
		t.Error("row still enabled after disable")
	}
	// audit.sh stays active.
	if _, _, en := hookRow(t, db, "audit.sh"); !en {
		t.Error("foreign sibling entry lost its enabled state")
	}
}

// ── roundtrip: disable → enable restores the file byte-for-byte ─────────────

func TestToggleHookRoundtrip(t *testing.T) {
	ed, db, path := setupHooks(t, "settings.json", hooksGolden)
	id, hash, _ := hookRow(t, db, "lint-gate.sh")
	if err := ed.ToggleHook(id, false, hash); err != nil {
		t.Fatalf("disable: %v", err)
	}

	id2, hash2, enabled := hookRow(t, db, "lint-gate.sh")
	if enabled {
		t.Fatal("row should be disabled between the toggles")
	}
	if err := ed.ToggleHook(id2, true, hash2); err != nil {
		t.Fatalf("enable: %v", err)
	}

	if after := mustRead(t, path); after != hooksGolden {
		t.Errorf("roundtrip is not byte-for-byte:\n%s",
			textdiff.UnifiedDiff("original", "roundtrip", hooksGolden, after))
	}
	if _, _, en := hookRow(t, db, "lint-gate.sh"); !en {
		t.Error("row not re-enabled after roundtrip")
	}
}

// ── managed=swarmery: refused, file untouched ────────────────────────────────

func TestToggleHookManaged(t *testing.T) {
	ed, db, path := setupHooks(t, "settings.local.json", hooksLocalGolden)
	id, hash, _ := hookRow(t, db, "swarmery hook permission-request")

	err := ed.ToggleHook(id, false, hash)
	if !errors.Is(err, ErrHookManaged) {
		t.Fatalf("want ErrHookManaged, got %v", err)
	}
	if got := mustRead(t, path); got != hooksLocalGolden {
		t.Error("managed refusal must leave the file untouched")
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(ed.cfg.BackupsDir), "backups")); err == nil {
		if entries, _ := os.ReadDir(ed.cfg.BackupsDir); len(entries) != 0 {
			t.Error("managed refusal must not create backups")
		}
	}
}

// ── settings.local.json: foreign entries toggle exactly like settings.json ──

func TestToggleHookLocalSettings(t *testing.T) {
	ed, db, path := setupHooks(t, "settings.local.json", hooksLocalGolden)
	id, hash, _ := hookRow(t, db, "my-custom-audit.sh")

	if err := ed.ToggleHook(id, false, hash); err != nil {
		t.Fatalf("toggle in settings.local.json: %v", err)
	}
	got := parseDoc(t, mustRead(t, path))
	if recs, _ := got[sysscan.DisabledHooksKey].([]any); len(recs) != 1 {
		t.Fatalf("want 1 disabled record, got %v", got[sysscan.DisabledHooksKey])
	}
	if hooks, _ := got["hooks"].(map[string]any); hooks["Stop"] != nil {
		t.Error("emptied Stop event must be dropped (stripOurs discipline)")
	}
	// The swarmery-managed group is untouched.
	before := parseDoc(t, hooksLocalGolden)
	if subtree(t, before["hooks"].(map[string]any), "PermissionRequest") !=
		subtree(t, got["hooks"].(map[string]any), "PermissionRequest") {
		t.Error("managed PermissionRequest group changed")
	}
	if _, _, en := hookRow(t, db, "my-custom-audit.sh"); en {
		t.Error("row still enabled after local-settings disable")
	}
}

// ── 409: stale base_hash, file untouched ─────────────────────────────────────

func TestToggleHookConflict(t *testing.T) {
	ed, db, path := setupHooks(t, "settings.json", hooksGolden)
	id, _, _ := hookRow(t, db, "lint-gate.sh")

	err := ed.ToggleHook(id, false, hashOf("stale view of the row"))
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("want ErrConflict, got %v", err)
	}
	var ce *ConflictError
	if !errors.As(err, &ce) || ce.DiskHash == "" || ce.BaseHash != hashOf("stale view of the row") {
		t.Errorf("conflict payload wrong: %+v", ce)
	}
	if got := mustRead(t, path); got != hooksGolden {
		t.Error("409 must leave the file untouched")
	}
}

// ── kill-switch + idempotence ────────────────────────────────────────────────

func TestToggleHookReadonly(t *testing.T) {
	ed, db, path := setupHooks(t, "settings.json", hooksGolden)
	id, hash, _ := hookRow(t, db, "lint-gate.sh")
	t.Setenv(EnvReadOnly, "1")
	if err := ed.ToggleHook(id, false, hash); !errors.Is(err, ErrReadOnly) {
		t.Fatalf("want ErrReadOnly, got %v", err)
	}
	if got := mustRead(t, path); got != hooksGolden {
		t.Error("readonly mode must leave the file untouched")
	}
}

func TestToggleHookIdempotent(t *testing.T) {
	ed, db, path := setupHooks(t, "settings.json", hooksGolden)
	id, hash, _ := hookRow(t, db, "lint-gate.sh")
	if err := ed.ToggleHook(id, true, hash); err != nil { // already enabled
		t.Fatalf("no-op toggle: %v", err)
	}
	if got := mustRead(t, path); got != hooksGolden {
		t.Error("no-op toggle must not rewrite the file")
	}
	if entries, _ := os.ReadDir(ed.cfg.BackupsDir); len(entries) != 0 {
		t.Error("no-op toggle must not create backups")
	}
}

// ── PUT: command/timeout edit is a single-hunk surgical change ──────────────

func TestUpdateHookGolden(t *testing.T) {
	ed, db, path := setupHooks(t, "settings.json", hooksGolden)
	id, hash, _ := hookRow(t, db, "format-on-save.cjs")

	timeout := int64(30)
	if err := ed.UpdateHook(id, "node $CLAUDE_PROJECT_DIR/.claude/hooks/format-v2.cjs", &timeout, hash); err != nil {
		t.Fatalf("update: %v", err)
	}
	after := mustRead(t, path)

	if diff := textdiff.UnifiedDiff("before", "after", hooksGolden, after); hunkCount(diff) != 1 {
		t.Errorf("want exactly 1 hunk, got:\n%s", diff)
	}
	before := parseDoc(t, hooksGolden)
	got := parseDoc(t, after)
	for _, key := range []string{"enabledPlugins", "env", "model", "permissions", "statusLine", "unknownFutureKey"} {
		if subtree(t, before, key) != subtree(t, got, key) {
			t.Errorf("top-level %q changed", key)
		}
	}
	entry := got["hooks"].(map[string]any)["PostToolUse"].([]any)[0].(map[string]any)["hooks"].([]any)[0].(map[string]any)
	if entry["command"] != "node $CLAUDE_PROJECT_DIR/.claude/hooks/format-v2.cjs" ||
		entry["timeout"] != float64(30) {
		t.Errorf("edit not applied: %v", entry)
	}
	if entry["statusMessage"] != "formatting" || entry["type"] != "command" {
		t.Errorf("untouched entry fields changed: %v", entry)
	}

	var cmd string
	var to sql.NullInt64
	if err := db.QueryRow(`SELECT command, timeout FROM hooks WHERE event = 'PostToolUse'`).
		Scan(&cmd, &to); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(cmd, "format-v2.cjs") || !to.Valid || to.Int64 != 30 {
		t.Errorf("registry not converged: command=%q timeout=%v", cmd, to)
	}
}

func TestUpdateHookRemovesTimeout(t *testing.T) {
	ed, db, path := setupHooks(t, "settings.json", hooksGolden)
	id, hash, _ := hookRow(t, db, "lint-gate.sh")

	if err := ed.UpdateHook(id, "$CLAUDE_PROJECT_DIR/.claude/hooks/lint-gate.sh", nil, hash); err != nil {
		t.Fatalf("update: %v", err)
	}
	got := parseDoc(t, mustRead(t, path))
	entry := got["hooks"].(map[string]any)["PreToolUse"].([]any)[0].(map[string]any)["hooks"].([]any)[1].(map[string]any)
	if _, has := entry["timeout"]; has {
		t.Errorf("nil timeout must remove the key: %v", entry)
	}
	var to sql.NullInt64
	if err := db.QueryRow(`SELECT timeout FROM hooks WHERE command LIKE '%lint-gate%'`).Scan(&to); err != nil {
		t.Fatal(err)
	}
	if to.Valid {
		t.Errorf("registry timeout should be NULL, got %d", to.Int64)
	}
}

// ── editing a DISABLED entry works against its record ───────────────────────

func TestUpdateDisabledHook(t *testing.T) {
	ed, db, path := setupHooks(t, "settings.json", hooksGolden)
	id, hash, _ := hookRow(t, db, "lint-gate.sh")
	if err := ed.ToggleHook(id, false, hash); err != nil {
		t.Fatalf("disable: %v", err)
	}
	id2, hash2, _ := hookRow(t, db, "lint-gate.sh")
	if err := ed.UpdateHook(id2, "$CLAUDE_PROJECT_DIR/.claude/hooks/lint-gate-v2.sh", nil, hash2); err != nil {
		t.Fatalf("update disabled: %v", err)
	}
	got := parseDoc(t, mustRead(t, path))
	rec := got[sysscan.DisabledHooksKey].([]any)[0].(map[string]any)
	entry := rec["hook"].(map[string]any)
	if entry["command"] != "$CLAUDE_PROJECT_DIR/.claude/hooks/lint-gate-v2.sh" {
		t.Errorf("disabled entry not edited: %v", entry)
	}
}
