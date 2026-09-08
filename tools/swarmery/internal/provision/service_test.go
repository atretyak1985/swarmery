package provision

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/claudeacct"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/store"
)

type stubRunner struct {
	calls   [][]string
	dirs    []string // cwd per call — `--scope project` only resolves from the project
	failOn  string   // fail when the args-join has this prefix
	failErr error
	// onRun simulates the side effect of a real user-scope `plugin install`:
	// writing enabledPlugins into the user settings.json. It fires only on the
	// install call — firing it on the preceding `marketplace update` would poison
	// the snapshot the revert is measured against, and pass the test for the
	// wrong reason.
	onRun func()
}

func (s *stubRunner) Claude(_ context.Context, dir, _ string, args ...string) (string, error) {
	s.calls = append(s.calls, args)
	s.dirs = append(s.dirs, dir)
	if s.onRun != nil && len(args) >= 2 && args[1] == "install" {
		s.onRun()
	}
	if s.failOn != "" && strings.HasPrefix(strings.Join(args, " "), s.failOn) {
		return "", s.failErr
	}
	return "", nil
}

// installCall returns the args and cwd of the `plugin install` invocation.
func (s *stubRunner) installCall(t *testing.T) ([]string, string) {
	t.Helper()
	for i, c := range s.calls {
		if len(c) >= 2 && c[0] == "plugin" && c[1] == "install" {
			return c, s.dirs[i]
		}
	}
	t.Fatal("no `plugin install` call was made")
	return nil, ""
}

func (s *stubRunner) generateCalls() int {
	n := 0
	for _, c := range s.calls {
		if len(c) > 0 && c[0] == "-p" {
			n++
		}
	}
	return n
}

// migratedTestDB opens a fully-migrated temp SQLite DB via store.Open — the same
// canonical path internal/advisor's testDB uses (store.Open runs Migrate). This
// is the real helper substituted for the plan's openMigratedTestDB placeholder.
func migratedTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "provision.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

// newSvc builds a Service on a migrated temp DB with the given actions.
func newSvc(t *testing.T, r Runner, actions map[string]GenerateAction) *Service {
	t.Helper()
	db := migratedTestDB(t)
	// seed a project row so the FK holds (mirror how other tests insert projects)
	if _, err := db.Exec(`INSERT INTO projects(id, path, slug, first_seen) VALUES(1,'/tmp/p','p','2026-01-01T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	s := NewService(db, r)
	if actions != nil {
		s.Actions = actions
	}
	return s
}

func TestEnqueueSingleFlight(t *testing.T) {
	s := newSvc(t, &stubRunner{}, map[string]GenerateAction{})
	id1, started1, err := s.Enqueue(1, "architecture-pack")
	if err != nil || !started1 {
		t.Fatalf("first enqueue: started=%v err=%v", started1, err)
	}
	id2, started2, err := s.Enqueue(1, "architecture-pack")
	if err != nil || started2 || id2 != id1 {
		t.Fatalf("second enqueue must reuse: id=%d started=%v err=%v", id2, started2, err)
	}
}

// TestInflightUniqueIndex proves the migration's partial unique index rejects a
// second in-flight row for the same (project, pack) — the DB-level backstop for
// Enqueue's TOCTOU window.
func TestInflightUniqueIndex(t *testing.T) {
	s := newSvc(t, &stubRunner{}, map[string]GenerateAction{})
	ins := func() error {
		_, err := s.DB.Exec(
			`INSERT INTO provision_jobs(project_id, pack, status, started_at) VALUES(1,'architecture-pack','pending','2026-01-01T00:00:00Z')`)
		return err
	}
	if err := ins(); err != nil {
		t.Fatalf("first in-flight insert: %v", err)
	}
	if err := ins(); err == nil {
		t.Fatal("second in-flight insert must violate the partial unique index")
	}
	// A terminal row for the same key is allowed (the index only covers in-flight).
	if _, err := s.DB.Exec(
		`INSERT INTO provision_jobs(project_id, pack, status, started_at, finished_at) VALUES(1,'architecture-pack','done','2026-01-01T00:00:00Z','2026-01-01T00:00:01Z')`); err != nil {
		t.Fatalf("terminal row for same key must be allowed: %v", err)
	}
}

func TestRunInstallOnlyPack(t *testing.T) {
	r := &stubRunner{}
	s := newSvc(t, r, map[string]GenerateAction{}) // no action for any pack
	id, _, _ := s.Enqueue(1, "graphify-pack")
	if err := s.Run(context.Background(), id, "/tmp/p", "graphify-pack"); err != nil {
		t.Fatal(err)
	}
	if r.generateCalls() != 0 {
		t.Fatal("install-only pack must not generate")
	}
	if got := status(t, s, id); got != "installed" {
		t.Fatalf("status=%s", got)
	}
}

func TestRunSkipsWhenFresh(t *testing.T) {
	r := &stubRunner{}
	acts := map[string]GenerateAction{"architecture-pack": {Prompt: "x", Timeout: time.Minute, Fresh: func(string) bool { return true }}}
	s := newSvc(t, r, acts)
	id, _, _ := s.Enqueue(1, "architecture-pack")
	if err := s.Run(context.Background(), id, "/tmp/p", "architecture-pack"); err != nil {
		t.Fatal(err)
	}
	if r.generateCalls() != 0 {
		t.Fatal("fresh → no generate spawn")
	}
	if got := status(t, s, id); got != "skipped" {
		t.Fatalf("status=%s", got)
	}
}

func TestRunGeneratesWhenStale(t *testing.T) {
	r := &stubRunner{}
	acts := map[string]GenerateAction{"architecture-pack": {Prompt: "x", Timeout: time.Minute, Fresh: func(string) bool { return false }}}
	s := newSvc(t, r, acts)
	id, _, _ := s.Enqueue(1, "architecture-pack")
	if err := s.Run(context.Background(), id, "/tmp/p", "architecture-pack"); err != nil {
		t.Fatal(err)
	}
	if r.generateCalls() != 1 {
		t.Fatalf("stale → exactly one generate, got %d", r.generateCalls())
	}
	if got := status(t, s, id); got != "done" {
		t.Fatalf("status=%s", got)
	}
}

// Enabling a pack for ONE project must not install it for the machine. The CLI
// defaults `plugin install` to --scope user, which installs and enables the pack
// everywhere — a per-project toggle with a machine-wide effect.
func TestRunInstallsAtProjectScope(t *testing.T) {
	r := &stubRunner{}
	s := newSvc(t, r, map[string]GenerateAction{})
	dir := t.TempDir() // a plain .claude-less project: not a symlinked overlay
	id, _, _ := s.Enqueue(1, "graphify-pack")
	if err := s.Run(context.Background(), id, dir, "graphify-pack"); err != nil {
		t.Fatal(err)
	}

	args, cwd := r.installCall(t)
	want := []string{"plugin", "install", "graphify-pack@swarmery", "--scope", "project"}
	if strings.Join(args, " ") != strings.Join(want, " ") {
		t.Errorf("args = %v, want %v", args, want)
	}
	// --scope project resolves from the cwd; without it the CLI has no project.
	if cwd != dir {
		t.Errorf("cwd = %q, want the project path %q", cwd, dir)
	}
}

// The multi-repo overlay consumer (.claude is a symlink into a shared agents/
// repo): the CLI refuses to write project-scope settings through it, so the
// install falls back to user scope and reverts the global enable afterwards.
func TestRunInstallFallsBackToUserScopeOnSymlinkedClaudeDir(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "agents"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("agents", filepath.Join(dir, ".claude")); err != nil {
		t.Skipf("symlink unsupported: %v", err)
	}
	claudeDir := t.TempDir()
	userSettings := filepath.Join(claudeDir, "settings.json")
	if err := os.WriteFile(userSettings, []byte(`{"enabledPlugins":{"core@swarmery":true}}`), 0o644); err != nil {
		t.Fatal(err)
	}

	r := &stubRunner{}
	r.onRun = func() { // the real CLI's user-scope side effect
		if err := os.WriteFile(userSettings,
			[]byte(`{"enabledPlugins":{"core@swarmery":true,"graphify-pack@swarmery":true}}`), 0o644); err != nil {
			t.Error(err)
		}
	}
	s := newSvc(t, r, map[string]GenerateAction{})
	s.ClaudeDir = claudeDir

	id, _, _ := s.Enqueue(1, "graphify-pack")
	if err := s.Run(context.Background(), id, dir, "graphify-pack"); err != nil {
		t.Fatal(err)
	}

	args, cwd := r.installCall(t)
	want := []string{"plugin", "install", "graphify-pack@swarmery"}
	if strings.Join(args, " ") != strings.Join(want, " ") {
		t.Errorf("args = %v, want %v — project scope cannot succeed here", args, want)
	}
	// User scope ignores cwd, but the dir is what binds the call to the
	// project's Claude account — a bare "" would install into ~/.claude for a
	// project whose sessions run somewhere else entirely.
	if cwd != dir {
		t.Errorf("cwd = %q, want the project dir %q so the account env applies", cwd, dir)
	}

	raw, err := os.ReadFile(userSettings)
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]map[string]any
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	if _, present := got["enabledPlugins"]["graphify-pack@swarmery"]; present {
		t.Error("graphify-pack is still globally enabled — enabling it for one project turned it on for every project")
	}
	if got["enabledPlugins"]["core@swarmery"] != true {
		t.Error("a foreign global enable was clobbered by the revert")
	}
}

// A user settings.json that cannot be snapshotted must stop the fallback: a
// global enable the daemon cannot revert is worse than an uninstalled pack.
func TestRunInstallRefusesUnrevertableFallback(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "agents"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("agents", filepath.Join(dir, ".claude")); err != nil {
		t.Skipf("symlink unsupported: %v", err)
	}
	claudeDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(claudeDir, "settings.json"), []byte(`{"enabledPlugins": [broken`), 0o644); err != nil {
		t.Fatal(err)
	}

	r := &stubRunner{}
	s := newSvc(t, r, map[string]GenerateAction{})
	s.ClaudeDir = claudeDir

	id, _, _ := s.Enqueue(1, "graphify-pack")
	if err := s.Run(context.Background(), id, dir, "graphify-pack"); err == nil {
		t.Fatal("expected a refusal")
	}
	for _, c := range r.calls {
		if len(c) >= 2 && c[1] == "install" {
			t.Fatalf("install ran without a trustworthy snapshot: %v", c)
		}
	}
	j, _, _ := s.Latest(1, "graphify-pack")
	if j.Status != "failed" || !strings.Contains(j.Error, "every project on this machine") {
		t.Fatalf("job = %+v, want a failure explaining the refusal", j)
	}
}

func TestRunFailsOnInstallError(t *testing.T) {
	r := &stubRunner{failOn: "plugin install", failErr: errors.New("boom")}
	s := newSvc(t, r, map[string]GenerateAction{})
	id, _, _ := s.Enqueue(1, "architecture-pack")
	if err := s.Run(context.Background(), id, "/tmp/p", "architecture-pack"); err == nil {
		t.Fatal("expected error")
	}
	j, _, _ := s.Latest(1, "architecture-pack")
	if j.Status != "failed" || !strings.Contains(j.Error, "boom") {
		t.Fatalf("job=%+v", j)
	}
}

func TestHealStale(t *testing.T) {
	s := newSvc(t, &stubRunner{}, map[string]GenerateAction{})
	id, _, _ := s.Enqueue(1, "architecture-pack") // pending
	if err := s.HealStale(); err != nil {
		t.Fatal(err)
	}
	if got := status(t, s, id); got != "failed" {
		t.Fatalf("status=%s", got)
	}
}

func status(t *testing.T, s *Service, id int64) string {
	t.Helper()
	var st string
	if err := s.DB.QueryRow(`SELECT status FROM provision_jobs WHERE id=?`, id).Scan(&st); err != nil {
		t.Fatal(err)
	}
	return st
}

// A project bound to a non-default Claude account runs its sessions against
// that account's config dir, so a user-scope fallback install must land there
// too: every CLI call carries the project dir (Runner turns it into
// CLAUDE_CONFIG_DIR), and the global-enable snapshot/revert reads the ACCOUNT's
// settings.json, not the daemon's default. Before this, a symlinked-.claude
// project on account X got the pack installed into ~/.claude while its generate
// step ran in ~/.claude-X and reported "no such skill".
func TestRunUserScopeFallbackTargetsTheAccountConfigDir(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "agents"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("agents", filepath.Join(dir, ".claude")); err != nil {
		t.Skipf("symlink unsupported: %v", err)
	}
	if err := claudeacct.SetBinding(dir, "acct"); err != nil {
		t.Fatalf("SetBinding: %v", err)
	}

	acctDir := filepath.Join(home, ".claude-acct")
	if err := os.MkdirAll(acctDir, 0o755); err != nil {
		t.Fatal(err)
	}
	acctSettings := filepath.Join(acctDir, "settings.json")
	if err := os.WriteFile(acctSettings, []byte(`{"enabledPlugins":{"core@swarmery":true}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	// The daemon's default dir must stay untouched throughout.
	defaultDir := t.TempDir()
	defaultSettings := filepath.Join(defaultDir, "settings.json")
	if err := os.WriteFile(defaultSettings, []byte(`{"enabledPlugins":{}}`), 0o644); err != nil {
		t.Fatal(err)
	}

	r := &stubRunner{}
	r.onRun = func() { // the real CLI writes the enable into the ACCOUNT's settings
		if err := os.WriteFile(acctSettings,
			[]byte(`{"enabledPlugins":{"core@swarmery":true,"graphify-pack@swarmery":true}}`), 0o644); err != nil {
			t.Error(err)
		}
	}
	s := newSvc(t, r, map[string]GenerateAction{})
	s.ClaudeDir = defaultDir

	id, _, _ := s.Enqueue(1, "graphify-pack")
	if err := s.Run(context.Background(), id, dir, "graphify-pack"); err != nil {
		t.Fatal(err)
	}

	for i, c := range r.calls {
		if r.dirs[i] != dir {
			t.Errorf("call %v ran with dir %q, want the project dir %q — only that binds the account env", c, r.dirs[i], dir)
		}
	}

	raw, err := os.ReadFile(acctSettings)
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]map[string]any
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	if _, present := got["enabledPlugins"]["graphify-pack@swarmery"]; present {
		t.Error("the account's global enable was not reverted — the snapshot was taken from the wrong config dir")
	}
	if got["enabledPlugins"]["core@swarmery"] != true {
		t.Error("a foreign global enable in the account dir was clobbered by the revert")
	}
	if b, err := os.ReadFile(defaultSettings); err != nil || string(b) != `{"enabledPlugins":{}}` {
		t.Errorf("default settings.json changed to %q — the revert touched the wrong dir", string(b))
	}
}
