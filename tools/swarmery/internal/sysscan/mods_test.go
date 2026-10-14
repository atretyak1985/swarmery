package sysscan

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/ingest"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/modscan"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/store"
)

// modFixture adds a cache mod to a claude tree and points the scanner at the
// fake claude script (internal/modscan/testdata) — the real CLI never runs.
// It returns the mod dir and the invocation log the fake appends to.
func modFixture(t *testing.T, cfg *Config, mode string) (modDir, logPath string) {
	t.Helper()
	modDir = filepath.Join(cfg.ClaudeDir, "plugins", "cache", "mkt", "linkmod", "1.0.0")
	writeModFile(t, filepath.Join(modDir, ".claude-plugin", "plugin.json"), `{"name":"linkmod","version":"1.0.0"}`)
	writeModFile(t, filepath.Join(modDir, "hooks", "hooks.json"), `{"modules":["./register.tsx"]}`)
	writeModFile(t, filepath.Join(modDir, "hooks", "register.tsx"), "export default 1\n")

	bin, err := filepath.Abs("../modscan/testdata/fake-claude")
	if err != nil {
		t.Fatal(err)
	}
	fixture, err := filepath.Abs("../../testdata/fixtures/mods/validate-json.json")
	if err != nil {
		t.Fatal(err)
	}
	logPath = filepath.Join(t.TempDir(), "calls.log")
	t.Setenv("FAKE_CLAUDE_MODE", mode)
	t.Setenv("FAKE_CLAUDE_FIXTURE", fixture)
	t.Setenv("FAKE_CLAUDE_LOG", logPath)
	cfg.ClaudeBin = bin
	return modDir, logPath
}

func writeModFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func calls(t *testing.T, logPath string) int {
	t.Helper()
	raw, err := os.ReadFile(logPath)
	if os.IsNotExist(err) {
		return 0
	}
	if err != nil {
		t.Fatal(err)
	}
	return strings.Count(string(raw), "\n")
}

// modNotes drains every system_item_updated note of kind mod.
func modNotes(ch <-chan ingest.Notification) []int64 {
	var ids []int64
	for {
		select {
		case n := <-ch:
			if n.Type == ingest.NoteSystemItemUpdated && n.Kind == KindMod {
				ids = append(ids, n.ItemID)
			}
		default:
			return ids
		}
	}
}

type modRowView struct {
	id                       int64
	name, source, dir        string
	pluginID, version, sha   sql.NullString
	events, uses, env, risk  string
	errors, report, verified string
}

func readMod(t *testing.T, db *sql.DB) modRowView {
	t.Helper()
	var r modRowView
	if err := db.QueryRow(`SELECT id, name, source, dir, plugin_id, version, module_sha,
		events, uses, env, risk, errors, report, validated_at FROM mods`).Scan(
		&r.id, &r.name, &r.source, &r.dir, &r.pluginID, &r.version, &r.sha,
		&r.events, &r.uses, &r.env, &r.risk, &r.errors, &r.report, &r.verified); err != nil {
		t.Fatalf("read mods row: %v", err)
	}
	return r
}

func TestScanModsValidatesHashSkipsAndPublishes(t *testing.T) {
	db, cfg, _ := setup(t)
	modDir, logPath := modFixture(t, &cfg, "ok")
	bus := ingest.NewBus()
	ch, cancel := bus.Subscribe(256)
	defer cancel()
	s := New(db, cfg, bus)

	st, err := s.ScanMods(context.Background(), false)
	if err != nil {
		t.Fatal(err)
	}
	if st.Mods != 1 || st.Validated != 1 || calls(t, logPath) != 1 {
		t.Fatalf("first pass: %s, %d validate calls; want 1 mod validated once", st, calls(t, logPath))
	}
	r := readMod(t, db)
	if r.name != "linkmod" || r.source != modscan.SourceCache || r.dir != modDir ||
		r.pluginID.String != "linkmod@mkt" || r.version.String != "1.0.0" || !r.sha.Valid || r.verified == "" {
		t.Errorf("row = %+v", r)
	}
	var events, uses []string
	_ = json.Unmarshal([]byte(r.events), &events)
	_ = json.Unmarshal([]byte(r.uses), &uses)
	if !contains(events, "tool.check") || !contains(uses, "http.fetch") || r.env != `{"reads":["HOME"],"writes":[]}` {
		t.Errorf("events=%v uses=%v env=%s", events, uses, r.env)
	}
	var risk []modscan.Badge
	if err := json.Unmarshal([]byte(r.risk), &risk); err != nil || len(risk) == 0 || risk[0].Level != modscan.LevelRed {
		t.Errorf("risk = %s (%v)", r.risk, err)
	}
	if ids := modNotes(ch); len(ids) != 1 || ids[0] != r.id {
		t.Errorf("mod notes = %v, want one for row %d", ids, r.id)
	}

	// Unchanged → hash-skip: no validate, no note.
	if st, _ = s.ScanMods(context.Background(), false); st.Validated != 0 || calls(t, logPath) != 1 {
		t.Errorf("unchanged pass validated again: %s", st)
	}
	if ids := modNotes(ch); len(ids) != 0 {
		t.Errorf("unchanged pass published %v", ids)
	}

	// A module edit moves module_sha → validate again (same report → no note).
	writeModFile(t, filepath.Join(modDir, "hooks", "register.tsx"), "export default 2\n")
	if st, _ = s.ScanMods(context.Background(), false); st.Validated != 1 || calls(t, logPath) != 2 {
		t.Errorf("edited module: %s, %d calls; want a re-validate", st, calls(t, logPath))
	}
	if readMod(t, db).sha.String == r.sha.String {
		t.Error("module_sha did not move after a module edit")
	}

	// A forced pass validates regardless of module_sha.
	if fs, err := s.ScanMods(context.Background(), true); err != nil || fs.Validated != 1 || calls(t, logPath) != 3 {
		t.Errorf("forced pass: %s, %v, %d calls", fs, err, calls(t, logPath))
	}

	// The mod vanishes → its row is deleted and a note announces it.
	modNotes(ch)
	if err := os.RemoveAll(filepath.Dir(modDir)); err != nil {
		t.Fatal(err)
	}
	if st, _ = s.ScanMods(context.Background(), false); st.Removed != 1 || count(t, db, `SELECT COUNT(*) FROM mods`) != 0 {
		t.Errorf("vanished mod: %s, rows=%d", st, count(t, db, `SELECT COUNT(*) FROM mods`))
	}
	if ids := modNotes(ch); len(ids) != 1 || ids[0] != r.id {
		t.Errorf("removal notes = %v, want [%d]", ids, r.id)
	}
}

func contains(xs []string, x string) bool {
	for _, v := range xs {
		if v == x {
			return true
		}
	}
	return false
}

func openParseErrors(t *testing.T, db *sql.DB, target string) int {
	t.Helper()
	return count(t, db, `SELECT COUNT(*) FROM config_lint_findings
		WHERE target = ? AND rule = 'parse_error' AND severity = 'error' AND resolved_at IS NULL`, target)
}

func TestScanModsParseErrorFinding(t *testing.T) {
	cases := []struct {
		name, mode, reason string
	}{
		{"broken JSON", "garbage", "invalid JSON"},
		{"timeout", "sleep", "timeout"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			db, cfg, _ := setup(t)
			modFixture(t, &cfg, c.mode)
			if c.mode == "sleep" {
				prev := modscan.ValidateTimeout
				modscan.ValidateTimeout = 300 * time.Millisecond
				t.Cleanup(func() { modscan.ValidateTimeout = prev })
			}
			s := New(db, cfg, nil)
			st, err := s.ScanMods(context.Background(), false)
			if err != nil {
				t.Fatal(err)
			}
			if st.ParseErrors != 1 {
				t.Errorf("stats = %s, want 1 parse error", st)
			}
			r := readMod(t, db)
			if openParseErrors(t, db, ModTarget(r.id)) != 1 {
				t.Fatalf("no open parse_error finding for %s", ModTarget(r.id))
			}
			var msg string
			if err := db.QueryRow(`SELECT message FROM config_lint_findings WHERE target = ?`, ModTarget(r.id)).Scan(&msg); err != nil ||
				!strings.Contains(msg, c.reason) {
				t.Errorf("finding message = %q (%v), want it to name %q", msg, err, c.reason)
			}
			if !strings.Contains(r.errors, c.reason) || !strings.Contains(r.risk, modscan.LevelUnknown) {
				t.Errorf("row errors=%s risk=%s", r.errors, r.risk)
			}

			// The validator recovers: a forced rescan resolves the finding.
			t.Setenv("FAKE_CLAUDE_MODE", "ok")
			if _, err := s.ScanMods(context.Background(), true); err != nil {
				t.Fatal(err)
			}
			if openParseErrors(t, db, ModTarget(r.id)) != 0 {
				t.Error("parse_error finding still open after a clean validate")
			}
		})
	}
}

func TestScanModsNoClaudeBinary(t *testing.T) {
	db, cfg, _ := setup(t)
	modFixture(t, &cfg, "ok")
	cfg.ClaudeBin = filepath.Join(t.TempDir(), "no-claude-here")
	s := New(db, cfg, nil)
	if _, err := s.ScanMods(context.Background(), false); err != nil {
		t.Fatal(err)
	}
	r := readMod(t, db)
	if r.errors != `["claude binary not found"]` || r.sha.Valid || !strings.Contains(r.risk, `"unknown"`) {
		t.Errorf("row errors=%s sha=%v risk=%s; want the not-found error, NULL sha (retry), unknown badge", r.errors, r.sha, r.risk)
	}
	if openParseErrors(t, db, ModTarget(r.id)) != 0 {
		t.Error("a missing binary is not a parse_error")
	}
}

// F2: sysedit rescans synchronously after every write on the promise that
// Scan is cheap, so Scan must never run `claude plugin validate` nor wait on
// a mods pass in flight (a manual rescan holds the mods lock for up to
// N × ValidateTimeout).
func TestScanNeverWaitsOnModsPass(t *testing.T) {
	db, cfg, _ := setup(t)
	_, logPath := modFixture(t, &cfg, "sleep")
	prev := modscan.ValidateTimeout
	modscan.ValidateTimeout = 4 * time.Second // bounds the pre-fix failure, not the assertion
	t.Cleanup(func() { modscan.ValidateTimeout = prev })
	s := New(db, cfg, nil)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = s.ScanMods(ctx, true) // the manual rescan, stuck in validate
	}()
	t.Cleanup(func() { cancel(); <-done })

	deadline := time.Now().Add(5 * time.Second)
	for calls(t, logPath) == 0 {
		if time.Now().After(deadline) {
			t.Fatal("the rescan never reached the validator")
		}
		time.Sleep(10 * time.Millisecond)
	}

	start := time.Now()
	if _, err := s.Scan(); err != nil {
		t.Fatal(err)
	}
	if d := time.Since(start); d > time.Second {
		t.Errorf("Scan took %s while a mods rescan was in flight; want < 1s", d)
	}
	if n := calls(t, logPath); n != 1 {
		t.Errorf("validate calls = %d, want 1 (Scan must never run validate)", n)
	}
}

// The mods table still fills on the daemon's periodic pass (Run), with the
// validate behind it.
func TestRunFillsModsTable(t *testing.T) {
	db, cfg, _ := setup(t)
	_, logPath := modFixture(t, &cfg, "ok")
	cfg.RescanInterval = time.Hour

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = New(db, cfg, nil).Run(ctx)
	}()
	t.Cleanup(func() { cancel(); <-done })

	deadline := time.Now().Add(5 * time.Second)
	for count(t, db, `SELECT COUNT(*) FROM mods WHERE validated_at IS NOT NULL`) != 1 {
		if time.Now().After(deadline) {
			t.Fatal("Run never filled the mods table")
		}
		time.Sleep(25 * time.Millisecond)
	}
	if calls(t, logPath) != 1 {
		t.Errorf("validate calls = %d, want 1", calls(t, logPath))
	}
}

// syncBuf is a goroutine-safe log sink.
type syncBuf struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuf) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuf) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// While a manual rescan holds the mods lock (up to N × ValidateTimeout), the
// Run loop's own mods pass must skip instead of queueing behind it — a queued
// pass stalls every fsnotify/ticker registry rescan for as long.
func TestRunSkipsModsPassWhileRescanRuns(t *testing.T) {
	db, cfg, _ := setup(t)
	_, logPath := modFixture(t, &cfg, "sleep")
	prev := modscan.ValidateTimeout
	modscan.ValidateTimeout = 4 * time.Second // bounds the pre-fix stall, not the assertion
	t.Cleanup(func() { modscan.ValidateTimeout = prev })
	cfg.RescanInterval = 100 * time.Millisecond

	logs := &syncBuf{}
	log.SetOutput(logs)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })

	s := New(db, cfg, nil)
	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	t.Cleanup(func() { cancel(); wg.Wait() })

	wg.Add(1)
	go func() {
		defer wg.Done()
		_, _ = s.ScanMods(ctx, true) // the manual rescan, stuck in validate
	}()
	deadline := time.Now().Add(5 * time.Second)
	for calls(t, logPath) == 0 {
		if time.Now().After(deadline) {
			t.Fatal("the rescan never reached the validator")
		}
		time.Sleep(10 * time.Millisecond)
	}

	wg.Add(1)
	go func() {
		defer wg.Done()
		_ = s.Run(ctx)
	}()

	// Three registry passes well inside the 4 s the held lock would stall a
	// queued mods pass for.
	deadline = time.Now().Add(2 * time.Second)
	for strings.Count(logs.String(), "sysscan lint: ") < 3 {
		if time.Now().After(deadline) {
			t.Fatalf("Run stalled behind the manual rescan; registry passes logged so far:\n%s", logs.String())
		}
		time.Sleep(20 * time.Millisecond)
	}
	if !strings.Contains(logs.String(), "debug: sysscan mods: a mods pass is already running — skipped") {
		t.Errorf("no skip line logged:\n%s", logs.String())
	}
	if n := calls(t, logPath); n != 1 {
		t.Errorf("validate calls = %d, want 1 (the skipped passes never validate)", n)
	}
}

func TestScanModsReadsPluginDirsFromSettings(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "mods.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	claude := t.TempDir()
	local := filepath.Join(t.TempDir(), "local-mod")
	writeModFile(t, filepath.Join(local, "hooks", "hooks.json"), `{"modules":["./m.tsx"]}`)
	writeModFile(t, filepath.Join(local, "hooks", "m.tsx"), "x")
	writeModFile(t, filepath.Join(claude, "settings.json"),
		`{"env":{"CLAUDE_CODE_PLUGIN_DIRS":"`+local+`"}}`)

	cfg := Config{ClaudeDir: claude, RescanInterval: time.Hour}
	modFixture(t, &cfg, "ok") // also adds a cache mod under this claude dir
	if _, err := New(db, cfg, nil).ScanMods(context.Background(), false); err != nil {
		t.Fatal(err)
	}
	var src, name string
	if err := db.QueryRow(`SELECT source, name FROM mods WHERE dir = ?`, local).Scan(&src, &name); err != nil {
		t.Fatalf("plugin-dir mod not stored: %v", err)
	}
	if src != modscan.SourcePluginDir || name != "local-mod" {
		t.Errorf("plugin-dir mod = %s/%s", src, name)
	}
	if n := count(t, db, `SELECT COUNT(*) FROM mods`); n != 2 {
		t.Errorf("mods rows = %d, want 2 (cache + plugin-dir)", n)
	}
}
