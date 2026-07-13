package sysedit

// Step-08 safety tests. Everything runs inside t.TempDir() — no test ever
// touches the real ~/.claude, ~/.swarmery, or $HOME.

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/store"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/sysscan"
)

const (
	agentV1 = "---\nname: alpha\ndescription: sysedit fixture agent\n---\n\nbody v1\n"
	agentV2 = "---\nname: alpha\ndescription: sysedit fixture agent\n---\n\nbody v2\n"
	skillV1 = "---\nname: myskill\ndescription: sysedit fixture skill for the write tests\n---\n\nskill v1\n"
)

// setup builds an isolated world: a tmp claude tree with one agent and one
// skill, a migrated tmp DB converged by one scan pass, and an editor whose
// backups land in a tmp dir.
func setup(t *testing.T) (*Editor, *sql.DB, string) {
	t.Helper()
	root := t.TempDir()
	claude := filepath.Join(root, "claude")
	mustWrite(t, filepath.Join(claude, "agents", "alpha.md"), agentV1)
	mustWrite(t, filepath.Join(claude, "skills", "myskill", "SKILL.md"), skillV1)

	db, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	scanner := sysscan.New(db, sysscan.Config{ClaudeDir: claude, RescanInterval: time.Hour}, nil)
	if _, err := scanner.Scan(); err != nil {
		t.Fatalf("initial scan: %v", err)
	}

	ed := New(db, scanner, Config{ClaudeDir: claude, BackupsDir: filepath.Join(root, "backups")})
	return ed, db, claude
}

func mustWrite(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func mustRead(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(b)
}

func hashOf(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

func agentRef(t *testing.T, db *sql.DB) ItemRef {
	t.Helper()
	var id int64
	if err := db.QueryRow(`SELECT id FROM agents WHERE name = 'alpha'`).Scan(&id); err != nil {
		t.Fatalf("agent id: %v", err)
	}
	return ItemRef{Kind: sysscan.KindAgent, ID: id}
}

func versionCount(t *testing.T, db *sql.DB, id int64) int {
	t.Helper()
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM agent_versions WHERE agent_id = ?`, id).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func globTmp(t *testing.T, dir string) []string {
	t.Helper()
	m, err := filepath.Glob(filepath.Join(dir, tmpPrefix+"*"))
	if err != nil {
		t.Fatal(err)
	}
	return m
}

// ── scenario 1: 409 — stale baseHash → ErrConflict, disk untouched ──────────

func TestWriteFileConflict(t *testing.T) {
	ed, db, claude := setup(t)
	ref := agentRef(t, db)
	path := filepath.Join(claude, "agents", "alpha.md")

	// The caller based their edit on v1; meanwhile the disk drifted to v2.
	external := strings.Replace(agentV2, "body v2", "body v2 external", 1)
	mustWrite(t, path, external)

	_, err := ed.WriteFile(ref, []byte("---\nname: alpha\n---\nproposed v3\n"), hashOf(agentV1))
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("want ErrConflict, got %v", err)
	}
	var ce *ConflictError
	if !errors.As(err, &ce) {
		t.Fatalf("want *ConflictError, got %T", err)
	}
	if ce.BaseHash != hashOf(agentV1) || ce.DiskHash != hashOf(external) {
		t.Errorf("hashes: base=%s disk=%s", ce.BaseHash, ce.DiskHash)
	}
	// Base content v1 is in agent_versions → the diff is base→disk.
	if !strings.Contains(ce.Diff, "+body v2 external") || !strings.Contains(ce.Diff, "-body v1") {
		t.Errorf("diff should show base→disk drift, got:\n%s", ce.Diff)
	}
	if got := mustRead(t, path); got != external {
		t.Errorf("disk was modified on conflict:\n%s", got)
	}
	if n := versionCount(t, db, ref.ID); n != 1 {
		t.Errorf("conflict must not create versions, have %d", n)
	}
}

// ── scenario 2: crash between tmp and rename — original intact, tmp swept ───

func TestWriteFileCrashBetweenTmpAndRename(t *testing.T) {
	ed, db, claude := setup(t)
	ref := agentRef(t, db)
	path := filepath.Join(claude, "agents", "alpha.md")
	dir := filepath.Dir(path)

	// Injection point: Editor.commit — the pipeline's step f finalizer.
	ed.commit = func(tmp, dst string) error { panic("simulated crash between tmp and rename") }
	func() {
		defer func() {
			if recover() == nil {
				t.Fatal("expected the injected crash panic")
			}
		}()
		_, _ = ed.WriteFile(ref, []byte(agentV2), hashOf(agentV1))
	}()

	if got := mustRead(t, path); got != agentV1 {
		t.Fatalf("original corrupted by crash:\n%s", got)
	}
	if n := len(globTmp(t, dir)); n != 1 {
		t.Fatalf("want exactly 1 leftover tmp after crash, have %d", n)
	}

	// Next run sweeps the leftover and succeeds.
	ed.commit = os.Rename
	vid, err := ed.WriteFile(ref, []byte(agentV2), hashOf(agentV1))
	if err != nil {
		t.Fatalf("retry after crash: %v", err)
	}
	if vid == 0 {
		t.Error("want a new version id after retry")
	}
	if n := len(globTmp(t, dir)); n != 0 {
		t.Errorf("stale tmp not swept by next run, %d left", n)
	}
	if got := mustRead(t, path); got != agentV2 {
		t.Errorf("retry did not land:\n%s", got)
	}
}

// ── scenario 3: plugin origin → ErrPluginManaged, file untouched ────────────

func TestWriteFilePluginManaged(t *testing.T) {
	ed, db, claude := setup(t)
	path := filepath.Join(claude, "plugins", "cache", "mkt", "pl", "1.0.0", "agents", "p.md")
	mustWrite(t, path, agentV1)
	res, err := db.Exec(
		`INSERT INTO agents (name, scope, project_id, file_path, origin, plugin_name, deleted)
		 VALUES ('pl:p', 'global', NULL, ?, 'plugin', 'pl', 0)`, path)
	if err != nil {
		t.Fatal(err)
	}
	id, _ := res.LastInsertId()

	ref := ItemRef{Kind: sysscan.KindAgent, ID: id}
	if _, err := ed.WriteFile(ref, []byte(agentV2), hashOf(agentV1)); !errors.Is(err, ErrPluginManaged) {
		t.Fatalf("want ErrPluginManaged, got %v", err)
	}
	if got := mustRead(t, path); got != agentV1 {
		t.Errorf("plugin file was modified:\n%s", got)
	}
	if err := ed.DeleteFile(ref); !errors.Is(err, ErrPluginManaged) {
		t.Fatalf("delete: want ErrPluginManaged, got %v", err)
	}
}

// ── scenario 4: kill-switch → ErrReadOnly ───────────────────────────────────

func TestWriteFileReadonly(t *testing.T) {
	ed, db, claude := setup(t)
	ref := agentRef(t, db)
	path := filepath.Join(claude, "agents", "alpha.md")

	for _, v := range []string{"1", "true"} {
		t.Setenv(EnvReadOnly, v)
		if _, err := ed.WriteFile(ref, []byte(agentV2), hashOf(agentV1)); !errors.Is(err, ErrReadOnly) {
			t.Fatalf("%s=%s: want ErrReadOnly, got %v", EnvReadOnly, v, err)
		}
		if err := ed.DeleteFile(ref); !errors.Is(err, ErrReadOnly) {
			t.Fatalf("%s=%s delete: want ErrReadOnly, got %v", EnvReadOnly, v, err)
		}
	}
	if got := mustRead(t, path); got != agentV1 {
		t.Errorf("readonly mode still modified the file:\n%s", got)
	}

	t.Setenv(EnvReadOnly, "") // switch off — writes work again
	if _, err := ed.WriteFile(ref, []byte(agentV2), hashOf(agentV1)); err != nil {
		t.Fatalf("write after clearing kill-switch: %v", err)
	}
}

// ── scenario 5: path outside known roots → ErrPathOutsideRoots ──────────────

func TestWriteFilePathOutsideRoots(t *testing.T) {
	ed, db, _ := setup(t)
	outside := filepath.Join(t.TempDir(), "evil.md")
	mustWrite(t, outside, agentV1)
	res, err := db.Exec(
		`INSERT INTO agents (name, scope, project_id, file_path, origin, deleted)
		 VALUES ('evil', 'global', NULL, ?, 'local', 0)`, outside)
	if err != nil {
		t.Fatal(err)
	}
	id, _ := res.LastInsertId()

	ref := ItemRef{Kind: sysscan.KindAgent, ID: id}
	if _, err := ed.WriteFile(ref, []byte(agentV2), hashOf(agentV1)); !errors.Is(err, ErrPathOutsideRoots) {
		t.Fatalf("want ErrPathOutsideRoots, got %v", err)
	}
	if err := ed.DeleteFile(ref); !errors.Is(err, ErrPathOutsideRoots) {
		t.Fatalf("delete: want ErrPathOutsideRoots, got %v", err)
	}
	if got := mustRead(t, outside); got != agentV1 {
		t.Errorf("outside file was modified:\n%s", got)
	}
}

// ── scenario 6: backup exists BEFORE the change, byte-for-byte original ─────

func TestBackupCreatedBeforeWrite(t *testing.T) {
	ed, db, claude := setup(t)
	ref := agentRef(t, db)
	path := filepath.Join(claude, "agents", "alpha.md")

	if _, err := ed.WriteFile(ref, []byte(agentV2), hashOf(agentV1)); err != nil {
		t.Fatal(err)
	}
	tsDirs, err := os.ReadDir(ed.cfg.BackupsDir)
	if err != nil || len(tsDirs) != 1 {
		t.Fatalf("want exactly 1 backup dir, got %d (err=%v)", len(tsDirs), err)
	}
	// Backup mirrors the FULL original path under the timestamp dir.
	backup := filepath.Join(ed.cfg.BackupsDir, tsDirs[0].Name(),
		strings.TrimPrefix(path, string(os.PathSeparator)))
	if got := mustRead(t, backup); got != agentV1 {
		t.Errorf("backup is not byte-for-byte the ORIGINAL:\n%s", got)
	}
	if got := mustRead(t, path); got != agentV2 {
		t.Errorf("write did not land:\n%s", got)
	}
}

// TestBackupFailureAborts: an unwritable backup dir must abort BEFORE any
// modification of the target.
func TestBackupFailureAborts(t *testing.T) {
	ed, db, claude := setup(t)
	ref := agentRef(t, db)
	path := filepath.Join(claude, "agents", "alpha.md")

	mustWrite(t, ed.cfg.BackupsDir, "a file where the backups dir should be") // MkdirAll will fail
	if _, err := ed.WriteFile(ref, []byte(agentV2), hashOf(agentV1)); err == nil {
		t.Fatal("want backup failure to abort the write")
	}
	if got := mustRead(t, path); got != agentV1 {
		t.Errorf("file modified although backup failed:\n%s", got)
	}
}

// ── scenario 7: rotation — the 51st backup evicts the oldest, 50 remain ─────

func TestBackupRotation(t *testing.T) {
	ed, db, _ := setup(t)
	ref := agentRef(t, db)

	// Deterministic clock: each backup gets its own 1s-apart timestamp dir.
	base := time.Date(2026, 7, 13, 10, 0, 0, 0, time.UTC)
	tick := 0
	ed.now = func() time.Time { tick++; return base.Add(time.Duration(tick) * time.Second) }

	cur := agentV1
	for i := 1; i <= DefaultKeepBackups+1; i++ {
		next := fmt.Sprintf("---\nname: alpha\ndescription: sysedit fixture agent\n---\n\nbody r%d\n", i)
		if _, err := ed.WriteFile(ref, []byte(next), hashOf(cur)); err != nil {
			t.Fatalf("write %d: %v", i, err)
		}
		cur = next
	}

	entries, err := os.ReadDir(ed.cfg.BackupsDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != DefaultKeepBackups {
		t.Fatalf("want %d backup dirs after rotation, have %d", DefaultKeepBackups, len(entries))
	}
	oldest := base.Add(1 * time.Second).Format(backupTimeFormat)
	if _, err := os.Stat(filepath.Join(ed.cfg.BackupsDir, oldest)); !os.IsNotExist(err) {
		t.Errorf("oldest backup dir %s must be rotated away (err=%v)", oldest, err)
	}
	second := base.Add(2 * time.Second).Format(backupTimeFormat)
	if _, err := os.Stat(filepath.Join(ed.cfg.BackupsDir, second)); err != nil {
		t.Errorf("second backup dir %s must survive: %v", second, err)
	}
}

// Rotation's RemoveAll is fenced: a victim resolving outside config-backups
// is refused (assert-prefix-before-RemoveAll contract).
func TestRemoveBackupDirGuard(t *testing.T) {
	parent := t.TempDir()
	root := filepath.Join(parent, "backups")
	victim := filepath.Join(parent, "victim")
	for _, d := range []string{root, victim} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for _, name := range []string{"../victim", "..", "."} {
		if err := removeBackupDir(root, name); err == nil {
			t.Errorf("removeBackupDir(%q) must refuse paths escaping the backups root", name)
		}
	}
	if _, err := os.Stat(victim); err != nil {
		t.Fatalf("victim dir outside backups was removed: %v", err)
	}
	// Sanity: a legitimate child IS removed.
	child := filepath.Join(root, "2026-07-13T10-00-00Z")
	if err := os.MkdirAll(child, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := removeBackupDir(root, "2026-07-13T10-00-00Z"); err != nil {
		t.Fatalf("legitimate rotation victim refused: %v", err)
	}
	if _, err := os.Stat(child); !os.IsNotExist(err) {
		t.Error("legitimate rotation victim survived")
	}
}

// ── scenario 8: one write → exactly 1 new row in *_versions ─────────────────

func TestWriteFileCreatesExactlyOneVersion(t *testing.T) {
	ed, db, _ := setup(t)
	ref := agentRef(t, db)

	before := versionCount(t, db, ref.ID) // 1 — the initial scan
	vid, err := ed.WriteFile(ref, []byte(agentV2), hashOf(agentV1))
	if err != nil {
		t.Fatal(err)
	}
	if after := versionCount(t, db, ref.ID); after != before+1 {
		t.Fatalf("want exactly 1 new version (%d → %d)", before, after)
	}
	var current sql.NullInt64
	var hash string
	if err := db.QueryRow(`SELECT a.current_version_id, v.content_hash
		FROM agents a JOIN agent_versions v ON v.id = a.current_version_id
		WHERE a.id = ?`, ref.ID).Scan(&current, &hash); err != nil {
		t.Fatal(err)
	}
	if !current.Valid || current.Int64 != vid {
		t.Errorf("returned version id %d != current_version_id %v", vid, current)
	}
	if hash != hashOf(agentV2) {
		t.Errorf("new version hash mismatch: %s", hash)
	}
}

// Skills resolve through dir_path → SKILL.md.
func TestWriteFileSkill(t *testing.T) {
	ed, db, claude := setup(t)
	var id int64
	if err := db.QueryRow(`SELECT id FROM skills WHERE name = 'myskill'`).Scan(&id); err != nil {
		t.Fatal(err)
	}
	next := strings.Replace(skillV1, "skill v1", "skill v2", 1)
	vid, err := ed.WriteFile(ItemRef{Kind: sysscan.KindSkill, ID: id}, []byte(next), hashOf(skillV1))
	if err != nil {
		t.Fatal(err)
	}
	if vid == 0 {
		t.Error("want a skill_versions id")
	}
	if got := mustRead(t, filepath.Join(claude, "skills", "myskill", "SKILL.md")); got != next {
		t.Errorf("skill write did not land:\n%s", got)
	}
}

// ── DeleteFile: soft delete = move into backups + deleted=1, never destroy ──

func TestDeleteFileSoft(t *testing.T) {
	ed, db, claude := setup(t)
	ref := agentRef(t, db)
	path := filepath.Join(claude, "agents", "alpha.md")

	if err := ed.DeleteFile(ref); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("original must be gone after soft delete (err=%v)", err)
	}
	tsDirs, err := os.ReadDir(ed.cfg.BackupsDir)
	if err != nil || len(tsDirs) != 1 {
		t.Fatalf("want the moved file in 1 backup dir, got %d (err=%v)", len(tsDirs), err)
	}
	moved := filepath.Join(ed.cfg.BackupsDir, tsDirs[0].Name(),
		strings.TrimPrefix(path, string(os.PathSeparator)))
	if got := mustRead(t, moved); got != agentV1 {
		t.Errorf("moved backup content mismatch:\n%s", got)
	}
	var deleted int
	if err := db.QueryRow(`SELECT deleted FROM agents WHERE id = ?`, ref.ID).Scan(&deleted); err != nil {
		t.Fatal(err)
	}
	if deleted != 1 {
		t.Error("row must be deleted=1")
	}
	// A soft-deleted item refuses further writes.
	if _, err := ed.WriteFile(ref, []byte(agentV2), hashOf(agentV1)); !errors.Is(err, ErrNotFound) {
		t.Fatalf("write after delete: want ErrNotFound, got %v", err)
	}
}

// Unknown ids and kinds are ErrNotFound — no fallbacks, no guessing.
func TestResolveNotFound(t *testing.T) {
	ed, _, _ := setup(t)
	if _, err := ed.WriteFile(ItemRef{Kind: sysscan.KindAgent, ID: 9999}, []byte("x"), "h"); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown id: want ErrNotFound, got %v", err)
	}
	if _, err := ed.WriteFile(ItemRef{Kind: "wat", ID: 1}, []byte("x"), "h"); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown kind: want ErrNotFound, got %v", err)
	}
}

// File permissions survive the atomic rewrite (Stat before write).
func TestWriteFilePreservesMode(t *testing.T) {
	ed, db, claude := setup(t)
	ref := agentRef(t, db)
	path := filepath.Join(claude, "agents", "alpha.md")
	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := ed.WriteFile(ref, []byte(agentV2), hashOf(agentV1)); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("mode not preserved: %v", info.Mode().Perm())
	}
}
