package api

// phase 4: system, Stage 2 (step-09) — PUT + rollback for agents & skills.
// Unlike system_test.go (SQL-seeded fake paths), every test here runs against
// a REAL tmpdir claude tree converged by a real scanner, because the write
// path resolves and touches actual files through sysedit.

import (
	"bytes"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/store"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/sysedit"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/sysscan"
)

// Clean fixtures: description present + a Boundaries section → zero lint.
const (
	wAgentV1 = "---\nname: walpha\ndescription: write-test fixture agent\n---\n\n# Walpha\n\n## Boundaries\n\nbody v1\n"
	wAgentV2 = "---\nname: walpha\ndescription: write-test fixture agent\n---\n\n# Walpha\n\n## Boundaries\n\nbody v2\n"
	wSkillV1 = "---\nname: wskill\ndescription: write-test fixture skill with a long enough description\n---\n\nskill v1\n"
	wSkillV2 = "---\nname: wskill\ndescription: write-test fixture skill with a long enough description\n---\n\nskill v2\n"
)

// systemWriteServer builds the isolated write world: tmp claude tree (agent
// walpha + agent wbeta + skill wskill with a resource file), migrated tmp DB
// converged by one scan pass, sysedit editor attached, httptest server.
func systemWriteServer(t *testing.T) (*httptest.Server, *sql.DB, string) {
	t.Helper()
	root := t.TempDir()
	claude := filepath.Join(root, "claude")
	writeFile(t, filepath.Join(claude, "agents", "walpha.md"), wAgentV1)
	writeFile(t, filepath.Join(claude, "agents", "wbeta.md"),
		"---\nname: wbeta\ndescription: the other agent\n---\n\n## Boundaries\n\nbeta body\n")
	writeFile(t, filepath.Join(claude, "skills", "wskill", "SKILL.md"), wSkillV1)
	writeFile(t, filepath.Join(claude, "skills", "wskill", "helper.txt"), "resource — must survive writes")

	db, err := store.Open(filepath.Join(t.TempDir(), "write.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	scanner := sysscan.New(db, sysscan.Config{ClaudeDir: claude, RescanInterval: time.Hour}, nil)
	if _, err := scanner.Scan(); err != nil {
		t.Fatalf("initial scan: %v", err)
	}

	AttachSysEditor(sysedit.New(db, scanner,
		sysedit.Config{ClaudeDir: claude, BackupsDir: filepath.Join(root, "backups")}))
	t.Cleanup(func() { AttachSysEditor(nil) })

	h, err := NewServer(db, false)
	if err != nil {
		t.Fatalf("new server: %v", err)
	}
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return srv, db, claude
}

func sha256hex(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

// itemID resolves a registry row id by name.
func itemID(t *testing.T, db *sql.DB, table, name string) int64 {
	t.Helper()
	var id int64
	if err := db.QueryRow(`SELECT id FROM `+table+` WHERE name = ? AND deleted = 0`, name).Scan(&id); err != nil {
		t.Fatalf("%s %q id: %v", table, name, err)
	}
	return id
}

// doJSON sends one JSON request and decodes the JSON response, asserting the
// status code.
func doJSON(t *testing.T, method, url string, body any, wantStatus int) map[string]any {
	t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	req, err := http.NewRequest(method, url, bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, url, err)
	}
	defer resp.Body.Close()
	out := map[string]any{}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("%s %s: decode: %v", method, url, err)
	}
	if resp.StatusCode != wantStatus {
		t.Fatalf("%s %s: status %d, want %d (body: %v)", method, url, resp.StatusCode, wantStatus, out)
	}
	return out
}

func readDisk(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(b)
}

// ── happy path: new version, file updated on disk, change_note stamped ──────

func TestPutSystemAgentHappyPath(t *testing.T) {
	srv, db, claude := systemWriteServer(t)
	id := itemID(t, db, "agents", "walpha")
	path := filepath.Join(claude, "agents", "walpha.md")

	out := doJSON(t, http.MethodPut, srv.URL+"/api/system/agents/"+itoa(id), map[string]any{
		"content": wAgentV2, "base_hash": sha256hex(wAgentV1), "change_note": "tweak body",
	}, http.StatusOK)

	vid := int64(out["version_id"].(float64))
	if vid == 0 {
		t.Fatal("want a non-zero version_id")
	}
	if lint := out["lint"].([]any); len(lint) != 0 {
		t.Errorf("clean content must lint empty, got %v", lint)
	}
	if got := readDisk(t, path); got != wAgentV2 {
		t.Errorf("disk not updated:\n%s", got)
	}
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM agent_versions WHERE agent_id = ?`, id).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Errorf("versions = %d, want 2 (initial scan + this write)", n)
	}
	var note sql.NullString
	if err := db.QueryRow(`SELECT change_note FROM agent_versions WHERE id = ?`, vid).Scan(&note); err != nil {
		t.Fatal(err)
	}
	if !note.Valid || note.String != "tweak body" {
		t.Errorf("change_note = %v, want 'tweak body'", note)
	}
	var current int64
	if err := db.QueryRow(`SELECT current_version_id FROM agents WHERE id = ?`, id).Scan(&current); err != nil {
		t.Fatal(err)
	}
	if current != vid {
		t.Errorf("current_version_id = %d, want the returned %d", current, vid)
	}
}

// ── 409: stale base_hash → fresh base→disk diff, disk untouched ─────────────

func TestPutSystemAgentConflict(t *testing.T) {
	srv, db, claude := systemWriteServer(t)
	id := itemID(t, db, "agents", "walpha")
	path := filepath.Join(claude, "agents", "walpha.md")

	// The edit is based on v1; the disk drifts under it.
	external := strings.Replace(wAgentV1, "body v1", "body v1 external-drift", 1)
	writeFile(t, path, external)

	out := doJSON(t, http.MethodPut, srv.URL+"/api/system/agents/"+itoa(id), map[string]any{
		"content": wAgentV2, "base_hash": sha256hex(wAgentV1),
	}, http.StatusConflict)

	if out["disk_hash"] != sha256hex(external) || out["base_hash"] != sha256hex(wAgentV1) {
		t.Errorf("conflict hashes = %v/%v", out["disk_hash"], out["base_hash"])
	}
	diff := out["diff"].(string)
	if !strings.Contains(diff, "+body v1 external-drift") || !strings.Contains(diff, "-body v1") {
		t.Errorf("diff must show base→disk drift, got:\n%s", diff)
	}
	if got := readDisk(t, path); got != external {
		t.Errorf("conflict must not modify the disk:\n%s", got)
	}
}

// ── 422: broken frontmatter blocks BEFORE any write ─────────────────────────

func TestPutSystemAgentParseError(t *testing.T) {
	srv, db, claude := systemWriteServer(t)
	id := itemID(t, db, "agents", "walpha")
	path := filepath.Join(claude, "agents", "walpha.md")

	out := doJSON(t, http.MethodPut, srv.URL+"/api/system/agents/"+itoa(id), map[string]any{
		"content": "---\nname: [broken\n---\n\nbody\n", "base_hash": sha256hex(wAgentV1),
	}, http.StatusUnprocessableEntity)
	if !strings.Contains(out["error"].(string), "parse error") {
		t.Errorf("error = %v, want a frontmatter parse error", out["error"])
	}

	// Missing frontmatter entirely is a parse error too.
	doJSON(t, http.MethodPut, srv.URL+"/api/system/agents/"+itoa(id), map[string]any{
		"content": "just a body, no frontmatter\n", "base_hash": sha256hex(wAgentV1),
	}, http.StatusUnprocessableEntity)

	if got := readDisk(t, path); got != wAgentV1 {
		t.Errorf("blocked write must not touch the disk:\n%s", got)
	}
}

// ── 422: name duplicated within the same scope/project ──────────────────────

func TestPutSystemAgentDuplicateName(t *testing.T) {
	srv, db, claude := systemWriteServer(t)
	id := itemID(t, db, "agents", "walpha")
	path := filepath.Join(claude, "agents", "walpha.md")

	renamed := strings.Replace(wAgentV1, "name: walpha", "name: wbeta", 1)
	out := doJSON(t, http.MethodPut, srv.URL+"/api/system/agents/"+itoa(id), map[string]any{
		"content": renamed, "base_hash": sha256hex(wAgentV1),
	}, http.StatusUnprocessableEntity)
	if !strings.Contains(out["error"].(string), "wbeta") {
		t.Errorf("error = %v, want the clashing name", out["error"])
	}
	if got := readDisk(t, path); got != wAgentV1 {
		t.Errorf("blocked write must not touch the disk:\n%s", got)
	}

	// Keeping one's OWN name is not a clash.
	doJSON(t, http.MethodPut, srv.URL+"/api/system/agents/"+itoa(id), map[string]any{
		"content": wAgentV2, "base_hash": sha256hex(wAgentV1),
	}, http.StatusOK)
}

// ── 403: plugin-managed and readonly ─────────────────────────────────────────

func TestPutSystemAgentPluginManaged(t *testing.T) {
	srv, db, claude := systemWriteServer(t)
	path := filepath.Join(claude, "plugins", "cache", "mkt", "pl", "1.0.0", "agents", "p.md")
	// A distinct frontmatter name — validation must pass so the request
	// reaches sysedit's provenance gate (403), not the name fence (422).
	pluginV1 := strings.Replace(wAgentV1, "name: walpha", "name: plugp", 1)
	writeFile(t, path, pluginV1)
	res, err := db.Exec(
		`INSERT INTO agents (name, scope, project_id, file_path, origin, plugin_name, deleted)
		 VALUES ('pl:p', 'global', NULL, ?, 'plugin', 'pl', 0)`, path)
	if err != nil {
		t.Fatal(err)
	}
	id, _ := res.LastInsertId()

	doJSON(t, http.MethodPut, srv.URL+"/api/system/agents/"+itoa(id), map[string]any{
		"content":   strings.Replace(wAgentV2, "name: walpha", "name: plugp", 1),
		"base_hash": sha256hex(pluginV1),
	}, http.StatusForbidden)
	if got := readDisk(t, path); got != pluginV1 {
		t.Errorf("plugin file was modified:\n%s", got)
	}
}

func TestPutSystemAgentReadonly(t *testing.T) {
	srv, db, claude := systemWriteServer(t)
	id := itemID(t, db, "agents", "walpha")
	t.Setenv(sysedit.EnvReadOnly, "1")

	doJSON(t, http.MethodPut, srv.URL+"/api/system/agents/"+itoa(id), map[string]any{
		"content": wAgentV2, "base_hash": sha256hex(wAgentV1),
	}, http.StatusForbidden)
	if got := readDisk(t, filepath.Join(claude, "agents", "walpha.md")); got != wAgentV1 {
		t.Errorf("readonly mode still modified the file:\n%s", got)
	}
}

// ── lint warnings never block: agent without Boundaries saves fine ──────────

func TestPutSystemAgentLintWarningNotBlocking(t *testing.T) {
	srv, db, claude := systemWriteServer(t)
	id := itemID(t, db, "agents", "walpha")

	noBoundaries := "---\nname: walpha\ndescription: still described\n---\n\n# Walpha\n\nbody without the section\n"
	out := doJSON(t, http.MethodPut, srv.URL+"/api/system/agents/"+itoa(id), map[string]any{
		"content": noBoundaries, "base_hash": sha256hex(wAgentV1),
	}, http.StatusOK)

	lint := out["lint"].([]any)
	if len(lint) != 1 {
		t.Fatalf("lint = %v, want exactly the boundaries warning", lint)
	}
	f := lint[0].(map[string]any)
	if f["rule"] != sysscan.RuleAgentNoBoundaries || f["severity"] != "warn" {
		t.Errorf("lint[0] = %v", f)
	}
	if got := readDisk(t, filepath.Join(claude, "agents", "walpha.md")); got != noBoundaries {
		t.Errorf("warned write must still land:\n%s", got)
	}
}

// ── rollback: ordinary write through sysedit; disk byte-for-byte old content ─
//
// NOTE the *_versions UNIQUE(fk, content_hash) content-addressing: restoring
// v1's exact bytes re-points current_version_id at the EXISTING v1 row (no
// duplicate row is minted — the plan's "produces v4" is not representable in
// this schema). History stays append-only: nothing is deleted or rewritten.
func TestRollbackSystemAgent(t *testing.T) {
	srv, db, claude := systemWriteServer(t)
	id := itemID(t, db, "agents", "walpha")
	path := filepath.Join(claude, "agents", "walpha.md")

	// Advance to v2 first.
	doJSON(t, http.MethodPut, srv.URL+"/api/system/agents/"+itoa(id), map[string]any{
		"content": wAgentV2, "base_hash": sha256hex(wAgentV1),
	}, http.StatusOK)

	var v1ID int64
	if err := db.QueryRow(`SELECT id FROM agent_versions WHERE agent_id = ? AND content_hash = ?`,
		id, sha256hex(wAgentV1)).Scan(&v1ID); err != nil {
		t.Fatalf("v1 row: %v", err)
	}
	var before int
	if err := db.QueryRow(`SELECT COUNT(*) FROM agent_versions WHERE agent_id = ?`, id).Scan(&before); err != nil {
		t.Fatal(err)
	}

	out := doJSON(t, http.MethodPost, srv.URL+"/api/system/agents/"+itoa(id)+"/rollback", map[string]any{
		"version_id": v1ID, "base_hash": sha256hex(wAgentV2),
	}, http.StatusOK)

	if got := readDisk(t, path); got != wAgentV1 {
		t.Errorf("rollback must restore the old content byte-for-byte:\n%s", got)
	}
	if vid := int64(out["version_id"].(float64)); vid != v1ID {
		t.Errorf("version_id = %d, want the content-addressed v1 row %d", vid, v1ID)
	}
	var after int
	var current int64
	if err := db.QueryRow(`SELECT COUNT(*) FROM agent_versions WHERE agent_id = ?`, id).Scan(&after); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT current_version_id FROM agents WHERE id = ?`, id).Scan(&current); err != nil {
		t.Fatal(err)
	}
	if after != before {
		t.Errorf("history must stay append-only: %d rows → %d", before, after)
	}
	if current != v1ID {
		t.Errorf("current_version_id = %d, want %d", current, v1ID)
	}
	// The reused v1 row's history metadata is never clobbered by the rollback.
	var note sql.NullString
	if err := db.QueryRow(`SELECT change_note FROM agent_versions WHERE id = ?`, v1ID).Scan(&note); err != nil {
		t.Fatal(err)
	}
	if note.Valid {
		t.Errorf("rollback must not rewrite the reused row's change_note, got %q", note.String)
	}

	// A rollback with a stale base_hash is the same 409 as PUT.
	doJSON(t, http.MethodPost, srv.URL+"/api/system/agents/"+itoa(id)+"/rollback", map[string]any{
		"version_id": v1ID, "base_hash": sha256hex(wAgentV2), // disk is v1 again
	}, http.StatusConflict)

	// A foreign/unknown version id is 404.
	doJSON(t, http.MethodPost, srv.URL+"/api/system/agents/"+itoa(id)+"/rollback", map[string]any{
		"version_id": 99999, "base_hash": sha256hex(wAgentV1),
	}, http.StatusNotFound)
}

// ── skills mirror: PUT writes SKILL.md only; resources untouched ─────────────

func TestPutAndRollbackSystemSkill(t *testing.T) {
	srv, db, claude := systemWriteServer(t)
	id := itemID(t, db, "skills", "wskill")
	skillMD := filepath.Join(claude, "skills", "wskill", "SKILL.md")
	resource := filepath.Join(claude, "skills", "wskill", "helper.txt")

	out := doJSON(t, http.MethodPut, srv.URL+"/api/system/skills/"+itoa(id), map[string]any{
		"content": wSkillV2, "base_hash": sha256hex(wSkillV1), "change_note": "skill tweak",
	}, http.StatusOK)
	if int64(out["version_id"].(float64)) == 0 {
		t.Fatal("want a skill_versions id")
	}
	if lint := out["lint"].([]any); len(lint) != 0 {
		t.Errorf("clean skill must lint empty, got %v", lint)
	}
	if got := readDisk(t, skillMD); got != wSkillV2 {
		t.Errorf("SKILL.md not updated:\n%s", got)
	}
	if got := readDisk(t, resource); got != "resource — must survive writes" {
		t.Errorf("skill resource file was touched:\n%s", got)
	}

	var v1ID int64
	if err := db.QueryRow(`SELECT id FROM skill_versions WHERE skill_id = ? AND content_hash = ?`,
		id, sha256hex(wSkillV1)).Scan(&v1ID); err != nil {
		t.Fatalf("skill v1 row: %v", err)
	}
	doJSON(t, http.MethodPost, srv.URL+"/api/system/skills/"+itoa(id)+"/rollback", map[string]any{
		"version_id": v1ID, "base_hash": sha256hex(wSkillV2),
	}, http.StatusOK)
	if got := readDisk(t, skillMD); got != wSkillV1 {
		t.Errorf("skill rollback must restore bytes:\n%s", got)
	}
	if got := readDisk(t, resource); got != "resource — must survive writes" {
		t.Errorf("skill resource file was touched by rollback:\n%s", got)
	}
}

// ── D4: foreign browser Origin is rejected before any handler logic ─────────

func TestSystemWriteOriginCheck(t *testing.T) {
	srv, db, _ := systemWriteServer(t)
	id := itemID(t, db, "agents", "walpha")

	for _, m := range []struct{ method, path string }{
		{http.MethodPut, "/api/system/agents/" + itoa(id)},
		{http.MethodPost, "/api/system/agents/" + itoa(id) + "/rollback"},
		{http.MethodPut, "/api/system/skills/" + itoa(id)},
		{http.MethodPost, "/api/system/skills/" + itoa(id) + "/rollback"},
	} {
		req, err := http.NewRequest(m.method, srv.URL+m.path, strings.NewReader(`{}`))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Origin", "https://evil.example")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusForbidden {
			t.Errorf("%s %s with foreign Origin: status %d, want 403", m.method, m.path, resp.StatusCode)
		}
	}
}

// ── 400: malformed / incomplete bodies ───────────────────────────────────────

func TestSystemWriteBadRequests(t *testing.T) {
	srv, db, _ := systemWriteServer(t)
	id := itemID(t, db, "agents", "walpha")

	// Missing required fields.
	doJSON(t, http.MethodPut, srv.URL+"/api/system/agents/"+itoa(id),
		map[string]any{"content": wAgentV2}, http.StatusBadRequest)
	doJSON(t, http.MethodPut, srv.URL+"/api/system/agents/"+itoa(id),
		map[string]any{"base_hash": sha256hex(wAgentV1)}, http.StatusBadRequest)
	doJSON(t, http.MethodPost, srv.URL+"/api/system/agents/"+itoa(id)+"/rollback",
		map[string]any{"base_hash": sha256hex(wAgentV1)}, http.StatusBadRequest)

	// Unknown item id → 404 out of sysedit's resolve.
	doJSON(t, http.MethodPut, srv.URL+"/api/system/agents/99999", map[string]any{
		"content": wAgentV2, "base_hash": sha256hex(wAgentV1),
	}, http.StatusNotFound)
}

func itoa(id int64) string { return strconv.FormatInt(id, 10) }
