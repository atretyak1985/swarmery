package api

// fusion phase 12: the Memory surface's daemon tests. Exercises the three roots
// (claude-md / auto-memory / serena), the missing-root tolerance, the traversal
// fence (../ walk + symlink escape → 400), the read (content+hash), and the
// versioned PUT (backup on disk, atomic overwrite, 409 on base_hash drift).

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/ingest"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/store"
)

// memoryFixture wires a project with all three memory roots populated under
// temp dirs, plus a redirected auto-memory claude dir and backups dir (restored
// on cleanup). It returns the server, the project path, and the resolved
// auto-memory dir so tests can address individual files.
type memoryFixture struct {
	srv         *httptest.Server
	projectPath string
	autoDir     string // <claudeDir>/projects/<slug>/memory
	backupsDir  string
}

func newMemoryFixture(t *testing.T, seed bool) *memoryFixture {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "memory.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	projectPath := t.TempDir()
	claudeDir := t.TempDir()
	backupsDir := filepath.Join(t.TempDir(), "config-backups")
	autoDir := filepath.Join(claudeDir, "projects", ingest.SlugForPath(projectPath), "memory")

	// Redirect the package-level roots at the temp locations, restore after.
	prevClaude, prevBackups := memoryClaudeDir, memoryBackupsDir
	AttachMemoryDirs(claudeDir, backupsDir)
	t.Cleanup(func() {
		memoryClaudeDir = prevClaude
		memoryBackupsDir = prevBackups
	})

	if seed {
		writeFixture(t, filepath.Join(projectPath, "CLAUDE.md"), "# project instructions\n")
		if err := os.MkdirAll(autoDir, 0o755); err != nil {
			t.Fatalf("mkdir autoDir: %v", err)
		}
		writeFixture(t, filepath.Join(autoDir, "MEMORY.md"), "# auto memory index\n")
		writeFixture(t, filepath.Join(autoDir, "user_role.md"), "senior engineer\n")
		serenaDir := filepath.Join(projectPath, ".serena", "memories")
		if err := os.MkdirAll(serenaDir, 0o755); err != nil {
			t.Fatalf("mkdir serenaDir: %v", err)
		}
		writeFixture(t, filepath.Join(serenaDir, "arch.md"), "serena note\n")
		// A non-.md sibling must be ignored by the lister.
		writeFixture(t, filepath.Join(autoDir, "notes.txt"), "ignored\n")
	}

	mustExec := func(q string, args ...any) {
		t.Helper()
		if _, err := db.Exec(q, args...); err != nil {
			t.Fatalf("exec: %v\n%s", err, q)
		}
	}
	mustExec(`INSERT INTO projects (id, path, slug, name, first_seen)
		VALUES (1, ?, ?, 'Mem', '2026-07-24T00:00:00Z')`,
		projectPath, ingest.SlugForPath(projectPath))

	h, err := NewServer(db, false)
	if err != nil {
		t.Fatalf("new server: %v", err)
	}
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)

	return &memoryFixture{srv: srv, projectPath: projectPath, autoDir: autoDir, backupsDir: backupsDir}
}

func writeFixture(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func sha256HexTest(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

func TestMemoryList(t *testing.T) {
	fx := newMemoryFixture(t, true)

	var out memoryListDTO
	getJSON(t, fx.srv.URL+"/api/projects/1/memory", &out)

	// claude-md (1) + auto-memory MEMORY.md,user_role.md (2) + serena arch.md (1)
	// = 4 files; notes.txt is filtered out.
	if len(out.Files) != 4 {
		t.Fatalf("files = %d, want 4: %+v", len(out.Files), out.Files)
	}
	// Stable kind order: claude-md, then auto-memory (name-sorted), then serena.
	wantKinds := []memoryKind{kindClaudeMD, kindAutoMemory, kindAutoMemory, kindSerena}
	for i, f := range out.Files {
		if f.Kind != wantKinds[i] {
			t.Errorf("file[%d].kind = %s, want %s", i, f.Kind, wantKinds[i])
		}
		if f.SizeBytes <= 0 {
			t.Errorf("file[%d] %s has non-positive size %d", i, f.Name, f.SizeBytes)
		}
		if !f.Writable {
			t.Errorf("file[%d] %s should be writable outside readonly mode", i, f.Name)
		}
	}
	// Auto-memory files are alphabetically sorted within the kind.
	if out.Files[1].Name != "MEMORY.md" || out.Files[2].Name != "user_role.md" {
		t.Errorf("auto-memory order = [%s,%s], want [MEMORY.md,user_role.md]",
			out.Files[1].Name, out.Files[2].Name)
	}
}

func TestMemoryListEmptyProjectTolerated(t *testing.T) {
	fx := newMemoryFixture(t, false) // no roots exist on disk

	var out memoryListDTO
	getJSON(t, fx.srv.URL+"/api/projects/1/memory", &out)
	if out.Files == nil {
		t.Fatal("files must serialize as [] not null for an empty project")
	}
	if len(out.Files) != 0 {
		t.Errorf("files = %d, want 0 for a project with no memory", len(out.Files))
	}
}

func TestMemoryListUnknownProject(t *testing.T) {
	fx := newMemoryFixture(t, true)
	res, err := http.Get(fx.srv.URL + "/api/projects/999/memory")
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusNotFound {
		t.Fatalf("unknown project status = %d, want 404", res.StatusCode)
	}
}

func TestMemoryGetFile(t *testing.T) {
	fx := newMemoryFixture(t, true)
	target := filepath.Join(fx.projectPath, "CLAUDE.md")

	var out memoryFileContentDTO
	getJSON(t, fx.srv.URL+"/api/projects/1/memory/file?path="+target, &out)
	if out.Content != "# project instructions\n" {
		t.Errorf("content = %q", out.Content)
	}
	if out.Kind != string(kindClaudeMD) {
		t.Errorf("kind = %q, want claude-md", out.Kind)
	}
	if out.Hash != sha256HexTest(out.Content) {
		t.Errorf("hash mismatch: %s", out.Hash)
	}
}

// A path that clears the fence but whose file was removed after listing is a
// genuine 404 (the read path's os.IsNotExist branch), not a fence rejection.
func TestMemoryGetFileMissingAfterList(t *testing.T) {
	fx := newMemoryFixture(t, true)
	// auto-memory MEMORY.md is a valid handle; delete it, then read.
	target := filepath.Join(fx.autoDir, "MEMORY.md")
	if err := os.Remove(target); err != nil {
		t.Fatal(err)
	}
	res, err := http.Get(fx.srv.URL + "/api/projects/1/memory/file?path=" + target)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusNotFound {
		t.Fatalf("removed-file read status = %d, want 404", res.StatusCode)
	}
}

// The path-error type carries its message verbatim (used by writeMemoryPathErr
// to surface a 400 reason).
func TestMemoryPathErrorMessage(t *testing.T) {
	if got := errBadMemoryPath("nope").Error(); got != "nope" {
		t.Errorf("Error() = %q, want %q", got, "nope")
	}
}

func TestMemoryTraversalRejected(t *testing.T) {
	fx := newMemoryFixture(t, true)

	// A secret OUTSIDE any root — the fence must never reach it.
	secretDir := t.TempDir()
	secret := filepath.Join(secretDir, "secret.md")
	writeFixture(t, secret, "TOP SECRET\n")

	cases := []struct {
		name string
		path string
	}{
		{"dotdot walk out of claude-md root",
			filepath.Join(fx.projectPath, "..", filepath.Base(secretDir), "secret.md")},
		{"absolute path outside every root", secret},
		{"nested handle under auto-memory (dirs are flat)",
			filepath.Join(fx.autoDir, "sub", "deep.md")},
		{"claude-md root but wrong basename",
			filepath.Join(fx.projectPath, "OTHER.md")},
		{"relative path", "relative/CLAUDE.md"},
		{"empty path", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			res, err := http.Get(fx.srv.URL + "/api/projects/1/memory/file?path=" + c.path)
			if err != nil {
				t.Fatal(err)
			}
			body := decodeErr(t, res)
			if res.StatusCode != http.StatusBadRequest {
				t.Fatalf("path %q status = %d (%s), want 400", c.path, res.StatusCode, body)
			}
		})
	}
}

// A symlink INSIDE a root that points OUT of it must not be a read handle: the
// fence resolves symlinks in the candidate's ancestry before the prefix check.
func TestMemorySymlinkEscapeRejected(t *testing.T) {
	fx := newMemoryFixture(t, true)

	secretDir := t.TempDir()
	writeFixture(t, filepath.Join(secretDir, "secret.md"), "TOP SECRET\n")

	// <autoDir>/escape → <secretDir> (a symlinked directory component).
	link := filepath.Join(fx.autoDir, "escape")
	if err := os.Symlink(secretDir, link); err != nil {
		t.Skipf("symlink unsupported: %v", err)
	}
	escaped := filepath.Join(link, "secret.md")

	res, err := http.Get(fx.srv.URL + "/api/projects/1/memory/file?path=" + escaped)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusBadRequest {
		t.Fatalf("symlink escape status = %d, want 400", res.StatusCode)
	}
}

func TestMemoryPutVersioned(t *testing.T) {
	fx := newMemoryFixture(t, true)
	target := filepath.Join(fx.projectPath, "CLAUDE.md")
	orig := "# project instructions\n"
	next := "# project instructions\n\nupdated by the memory editor\n"

	body, _ := json.Marshal(memoryWriteRequest{Content: next, BaseHash: sha256HexTest(orig)})
	res := doPut(t, fx.srv.URL+"/api/projects/1/memory/file?path="+target, body)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("PUT status = %d (%s), want 200", res.StatusCode, decodeErr(t, res))
	}
	res.Body.Close()

	// The new content is on disk.
	disk, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(disk) != next {
		t.Errorf("disk content = %q, want updated", string(disk))
	}

	// A backup of the ORIGINAL content exists somewhere under the backups dir.
	if !backupContains(t, fx.backupsDir, orig) {
		t.Errorf("no backup with the original content found under %s", fx.backupsDir)
	}
}

func TestMemoryPutConflict(t *testing.T) {
	fx := newMemoryFixture(t, true)
	target := filepath.Join(fx.projectPath, "CLAUDE.md")

	// base_hash of a version that is NOT what's on disk → 409.
	body, _ := json.Marshal(memoryWriteRequest{
		Content:  "whatever\n",
		BaseHash: sha256HexTest("a stale version the client never saw\n"),
	})
	res := doPut(t, fx.srv.URL+"/api/projects/1/memory/file?path="+target, body)
	defer res.Body.Close()
	if res.StatusCode != http.StatusConflict {
		t.Fatalf("stale base_hash status = %d, want 409", res.StatusCode)
	}
	var conflict memoryConflictDTO
	if err := json.NewDecoder(res.Body).Decode(&conflict); err != nil {
		t.Fatalf("decode conflict: %v", err)
	}
	if conflict.DiskHash != sha256HexTest("# project instructions\n") {
		t.Errorf("conflict disk_hash = %s", conflict.DiskHash)
	}

	// The file is untouched after a rejected write.
	disk, _ := os.ReadFile(target)
	if string(disk) != "# project instructions\n" {
		t.Errorf("file mutated on a 409: %q", string(disk))
	}
}

func TestMemoryPutMissingBaseHash(t *testing.T) {
	fx := newMemoryFixture(t, true)
	target := filepath.Join(fx.projectPath, "CLAUDE.md")
	body, _ := json.Marshal(map[string]string{"content": "x"})
	res := doPut(t, fx.srv.URL+"/api/projects/1/memory/file?path="+target, body)
	defer res.Body.Close()
	if res.StatusCode != http.StatusBadRequest {
		t.Fatalf("missing base_hash status = %d, want 400", res.StatusCode)
	}
}

func TestMemoryPutTraversalRejected(t *testing.T) {
	fx := newMemoryFixture(t, true)
	secretDir := t.TempDir()
	secret := filepath.Join(secretDir, "pwn.md")
	body, _ := json.Marshal(memoryWriteRequest{Content: "pwned\n", BaseHash: sha256HexTest("x")})
	res := doPut(t, fx.srv.URL+"/api/projects/1/memory/file?path="+secret, body)
	defer res.Body.Close()
	if res.StatusCode != http.StatusBadRequest {
		t.Fatalf("PUT traversal status = %d, want 400", res.StatusCode)
	}
	if _, err := os.Stat(secret); !os.IsNotExist(err) {
		t.Errorf("traversal PUT created a file outside the roots: %s", secret)
	}
}

func TestMemoryReadOnlyBlocksWrite(t *testing.T) {
	t.Setenv("SWARMERY_SYSTEM_READONLY", "1")
	fx := newMemoryFixture(t, true)
	target := filepath.Join(fx.projectPath, "CLAUDE.md")
	body, _ := json.Marshal(memoryWriteRequest{
		Content: "x\n", BaseHash: sha256HexTest("# project instructions\n"),
	})
	res := doPut(t, fx.srv.URL+"/api/projects/1/memory/file?path="+target, body)
	defer res.Body.Close()
	if res.StatusCode != http.StatusForbidden {
		t.Fatalf("readonly PUT status = %d, want 403", res.StatusCode)
	}
	// The list still reports the file as unwritable in readonly mode.
	var out memoryListDTO
	getJSON(t, fx.srv.URL+"/api/projects/1/memory", &out)
	for _, f := range out.Files {
		if f.Writable {
			t.Errorf("file %s reports writable in readonly mode", f.Name)
		}
	}
}

// Backup rotation: after more than memoryKeepBackups writes, the oldest
// timestamp dirs are pruned through the prefix-asserting remover, leaving
// exactly the cap. Driven at the helper level so the (fast) loop controls the
// backups dir directly.
func TestMemoryBackupRotation(t *testing.T) {
	prev := memoryBackupsDir
	memoryBackupsDir = filepath.Join(t.TempDir(), "backups")
	t.Cleanup(func() { memoryBackupsDir = prev })

	src := filepath.Join(t.TempDir(), "CLAUDE.md")
	writeFixture(t, src, "v0\n")

	total := memoryKeepBackups + 2
	for i := 0; i < total; i++ {
		// Distinct mtime per dir so the rotation ordering is deterministic; the
		// newMemoryBackupDir de-collision suffix already disambiguates names
		// within the same second.
		if err := backupMemoryFile(src); err != nil {
			t.Fatalf("backup %d: %v", i, err)
		}
		writeFixture(t, filepath.Join(memoryBackupsDir, ".touch"), "x") // keep dir mtimes moving
		os.Remove(filepath.Join(memoryBackupsDir, ".touch"))
	}

	entries, err := os.ReadDir(memoryBackupsDir)
	if err != nil {
		t.Fatal(err)
	}
	dirs := 0
	for _, e := range entries {
		if e.IsDir() {
			dirs++
		}
	}
	if dirs != memoryKeepBackups {
		t.Fatalf("backup dirs = %d, want cap %d", dirs, memoryKeepBackups)
	}
}

// The prefix-asserting remover refuses to touch a path outside the backups root
// (defense against a crafted name); a legitimate child is removed.
func TestMemoryRemoveBackupDirFence(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "keep"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := removeMemoryBackupDir(root, "../escape"); err == nil {
		t.Error("removeMemoryBackupDir accepted a ../ escape name")
	}
	if err := removeMemoryBackupDir(root, "keep"); err != nil {
		t.Errorf("removeMemoryBackupDir rejected a legitimate child: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "keep")); !os.IsNotExist(err) {
		t.Error("legitimate child was not removed")
	}
}

// ---- helpers ----------------------------------------------------------------

func doPut(t *testing.T, url string, body []byte) *http.Response {
	t.Helper()
	req, err := http.NewRequest(http.MethodPut, url, strings.NewReader(string(body)))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return res
}

func decodeErr(t *testing.T, res *http.Response) string {
	t.Helper()
	defer res.Body.Close()
	var e struct {
		Error string `json:"error"`
	}
	json.NewDecoder(res.Body).Decode(&e)
	return e.Error
}

// backupContains walks the backups tree and reports whether any file matches
// the given content — the versioned PUT must copy the previous version there.
func backupContains(t *testing.T, root, content string) bool {
	t.Helper()
	found := false
	filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return nil
		}
		data, rerr := os.ReadFile(path)
		if rerr == nil && string(data) == content {
			found = true
		}
		return nil
	})
	return found
}
