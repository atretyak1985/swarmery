package api

// phase 4: system, Stage 2 (step-11) — agent create + soft delete + restore.
// Same real-tmpdir discipline as system_write_test.go: a real claude tree,
// a real scanner, a real sysedit editor — never the machine's ~/.claude.

import (
	"database/sql"
	"encoding/json"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/store"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/sysedit"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/sysscan"
)

// systemCreateServer builds the create-test world. Unlike systemWriteServer
// the claude tree has NO agents/ directory — §0 of the format doc: the user
// tier may lack it entirely, and a scope=global create must mkdir it.
func systemCreateServer(t *testing.T) (srv *httptest.Server, db *sql.DB, claude, backups string) {
	t.Helper()
	root := t.TempDir()
	claude = filepath.Join(root, "claude")
	backups = filepath.Join(root, "backups")
	// One unrelated skill so the claude root exists without an agents/ dir.
	writeFile(t, filepath.Join(claude, "skills", "cskill", "SKILL.md"),
		"---\nname: cskill\ndescription: create-test fixture skill with a long enough description\n---\n\nbody\n")

	var err error
	db, err = store.Open(filepath.Join(t.TempDir(), "create.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	scanner := sysscan.New(db, sysscan.Config{ClaudeDir: claude, RescanInterval: time.Hour}, nil)
	if _, err := scanner.Scan(); err != nil {
		t.Fatalf("initial scan: %v", err)
	}

	AttachSysEditor(sysedit.New(db, scanner,
		sysedit.Config{ClaudeDir: claude, BackupsDir: backups}))
	t.Cleanup(func() { AttachSysEditor(nil) })

	h, err := NewServer(db, false)
	if err != nil {
		t.Fatalf("new server: %v", err)
	}
	srv = httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return srv, db, claude, backups
}

// createBody is the canonical happy-path form.
func createBody() map[string]any {
	return map[string]any{
		"name":        "new-agent",
		"scope":       "global",
		"description": "a create-test agent",
		"model":       "claude-sonnet-5",
		"tools":       []string{"Read", "Bash"},
		"boundaries":  "Never modify files outside the workspace.",
	}
}

// listAgentNames fetches GET /api/system/agents and returns the names.
func listAgentNames(t *testing.T, url string) []string {
	t.Helper()
	resp, err := http.Get(url + "/api/system/agents")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var items []map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&items); err != nil {
		t.Fatalf("decode list: %v", err)
	}
	names := make([]string, 0, len(items))
	for _, it := range items {
		names = append(names, it["name"].(string))
	}
	return names
}

// findInBackups walks the backups tree for a file basename.
func findInBackups(t *testing.T, backups, base string) string {
	t.Helper()
	var found string
	_ = filepath.WalkDir(backups, func(path string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() && d.Name() == base {
			found = path
		}
		return nil
	})
	return found
}

// ── create happy path: dir auto-created, file scanned, ONE version ──────────

func TestCreateSystemAgentGlobal(t *testing.T) {
	srv, db, claude, _ := systemCreateServer(t)
	agentsDir := filepath.Join(claude, "agents")
	if _, err := os.Stat(agentsDir); !os.IsNotExist(err) {
		t.Fatalf("precondition: agents dir must not exist yet")
	}

	out := doJSON(t, http.MethodPost, srv.URL+"/api/system/agents", createBody(), http.StatusCreated)

	// The missing agents/ dir was created by the sysedit mkdir helper.
	info, err := os.Stat(agentsDir)
	if err != nil || !info.IsDir() {
		t.Fatalf("agents dir not created: %v", err)
	}
	if info.Mode().Perm()&0o700 != 0o700 {
		t.Errorf("agents dir mode = %v, want owner rwx", info.Mode().Perm())
	}

	// The generated file parses with OUR OWN scanner gate, boundaries present
	// → zero findings.
	disk := readDisk(t, filepath.Join(agentsDir, "new-agent.md"))
	name, findings, lerr := sysscan.LintContent(sysscan.KindAgent, []byte(disk))
	if lerr != nil {
		t.Fatalf("generated file does not parse: %v\n%s", lerr, disk)
	}
	if name != "new-agent" {
		t.Errorf("frontmatter name = %q, want new-agent", name)
	}
	if len(findings) != 0 {
		t.Errorf("boundaries provided → lint must be clean, got %v", findings)
	}
	if lint := out["lint"].([]any); len(lint) != 0 {
		t.Errorf("response lint = %v, want empty", lint)
	}

	// Registry: exactly one row, exactly ONE version, ids match the response.
	id := int64(out["id"].(float64))
	vid := int64(out["version_id"].(float64))
	if id == 0 || vid == 0 {
		t.Fatalf("want non-zero id/version_id, got %d/%d", id, vid)
	}
	var scope, origin string
	var current int64
	if err := db.QueryRow(`SELECT scope, origin, current_version_id FROM agents WHERE id = ? AND deleted = 0`,
		id).Scan(&scope, &origin, &current); err != nil {
		t.Fatalf("agent row: %v", err)
	}
	if scope != "global" || origin != "local" || current != vid {
		t.Errorf("row = scope %q origin %q current %d, want global/local/%d", scope, origin, current, vid)
	}
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM agent_versions WHERE agent_id = ?`, id).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("versions = %d, want exactly the first one", n)
	}
	// Frontmatter columns landed via the scanner.
	var model, toolsJSON sql.NullString
	if err := db.QueryRow(`SELECT model, tools_json FROM agents WHERE id = ?`, id).Scan(&model, &toolsJSON); err != nil {
		t.Fatal(err)
	}
	if model.String != "claude-sonnet-5" || !strings.Contains(toolsJSON.String, "Bash") {
		t.Errorf("model/tools = %q / %q", model.String, toolsJSON.String)
	}
}

// ── no boundaries in the form → the intended TODO lint warning, nothing more ─

func TestCreateSystemAgentBoundariesTODO(t *testing.T) {
	srv, _, claude, _ := systemCreateServer(t)
	body := createBody()
	delete(body, "boundaries")

	out := doJSON(t, http.MethodPost, srv.URL+"/api/system/agents", body, http.StatusCreated)

	lint := out["lint"].([]any)
	if len(lint) != 1 {
		t.Fatalf("lint = %v, want exactly the boundaries warning", lint)
	}
	f := lint[0].(map[string]any)
	if f["rule"] != sysscan.RuleAgentNoBoundaries || f["severity"] != "warn" {
		t.Errorf("lint[0] = %v", f)
	}

	// The disk copy carries the TODO stub and re-lints to the SAME single
	// warning — "passes the linter with at most the intended Boundaries-TODO".
	disk := readDisk(t, filepath.Join(claude, "agents", "new-agent.md"))
	if !strings.Contains(disk, "TODO") {
		t.Errorf("template must leave a TODO stub:\n%s", disk)
	}
	_, findings, lerr := sysscan.LintContent(sysscan.KindAgent, []byte(disk))
	if lerr != nil {
		t.Fatalf("generated file does not parse: %v", lerr)
	}
	if len(findings) != 1 || findings[0].Rule != sysscan.RuleAgentNoBoundaries {
		t.Errorf("disk lint = %v, want only %s", findings, sysscan.RuleAgentNoBoundaries)
	}
}

// ── scope=project: file lands under <project>/.claude/agents/ ────────────────

func TestCreateSystemAgentProjectScope(t *testing.T) {
	srv, db, _, _ := systemCreateServer(t)
	projPath := t.TempDir()
	res, err := db.Exec(`INSERT INTO projects (path, slug, first_seen) VALUES (?, 'proj-a', '2026-07-13T00:00:00Z')`, projPath)
	if err != nil {
		t.Fatal(err)
	}
	projID, _ := res.LastInsertId()

	body := createBody()
	body["scope"] = "project"
	body["project_id"] = projID
	out := doJSON(t, http.MethodPost, srv.URL+"/api/system/agents", body, http.StatusCreated)

	path := filepath.Join(projPath, ".claude", "agents", "new-agent.md")
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("project-scope file missing: %v", err)
	}
	id := int64(out["id"].(float64))
	var scope string
	var gotProj int64
	if err := db.QueryRow(`SELECT scope, project_id FROM agents WHERE id = ?`, id).Scan(&scope, &gotProj); err != nil {
		t.Fatal(err)
	}
	if scope != "project" || gotProj != projID {
		t.Errorf("row = scope %q project %d, want project/%d", scope, gotProj, projID)
	}

	// Unknown project id → 404, nothing created.
	body["project_id"] = int64(99999)
	body["name"] = "other-agent"
	doJSON(t, http.MethodPost, srv.URL+"/api/system/agents", body, http.StatusNotFound)
}

// ── 409: duplicate name — live AND soft-deleted (restore hint) ───────────────

func TestCreateSystemAgentDuplicateName(t *testing.T) {
	srv, _, _, _ := systemCreateServer(t)
	out := doJSON(t, http.MethodPost, srv.URL+"/api/system/agents", createBody(), http.StatusCreated)
	id := int64(out["id"].(float64))

	// Live duplicate.
	dup := doJSON(t, http.MethodPost, srv.URL+"/api/system/agents", createBody(), http.StatusConflict)
	if !strings.Contains(dup["error"].(string), "already exists") {
		t.Errorf("error = %v", dup["error"])
	}

	// Soft-delete, then create again: STILL 409 — the name is not freed;
	// the error points at restore instead.
	doJSON(t, http.MethodDelete, srv.URL+"/api/system/agents/"+itoa(id), nil, http.StatusOK)
	del := doJSON(t, http.MethodPost, srv.URL+"/api/system/agents", createBody(), http.StatusConflict)
	if !strings.Contains(del["error"].(string), "restore") {
		t.Errorf("deleted-name conflict must hint at restore, got %v", del["error"])
	}
	if int64(del["restore_id"].(float64)) != id {
		t.Errorf("restore_id = %v, want %d", del["restore_id"], id)
	}
}

// ── 409: orphan file on disk (present, unscanned) — never clobbered ─────────

func TestCreateSystemAgentOrphanFile(t *testing.T) {
	srv, _, claude, _ := systemCreateServer(t)
	orphan := filepath.Join(claude, "agents", "new-agent.md")
	writeFile(t, orphan, "unscanned local edit — must survive\n")

	out := doJSON(t, http.MethodPost, srv.URL+"/api/system/agents", createBody(), http.StatusConflict)
	if !strings.Contains(out["error"].(string), "rescan") {
		t.Errorf("orphan-file conflict must tell the user to rescan, got %v", out["error"])
	}
	if got := readDisk(t, orphan); got != "unscanned local edit — must survive\n" {
		t.Errorf("orphan file was clobbered:\n%s", got)
	}
}

// ── 400: malformed create forms ──────────────────────────────────────────────

func TestCreateSystemAgentBadRequests(t *testing.T) {
	srv, _, _, _ := systemCreateServer(t)
	for name, mutate := range map[string]func(map[string]any){
		"non-kebab name":      func(b map[string]any) { b["name"] = "New_Agent" },
		"empty name":          func(b map[string]any) { b["name"] = "" },
		"bad scope":           func(b map[string]any) { b["scope"] = "user" },
		"missing description": func(b map[string]any) { delete(b, "description") },
		"multiline model":     func(b map[string]any) { b["model"] = "claude\ninjected: yes" },
		"project sans id":     func(b map[string]any) { b["scope"] = "project" },
		"global with id":      func(b map[string]any) { b["project_id"] = int64(1) },
	} {
		body := createBody()
		mutate(body)
		if out := doJSON(t, http.MethodPost, srv.URL+"/api/system/agents", body, http.StatusBadRequest); out == nil {
			t.Errorf("%s: want 400", name)
		}
	}
}

// ── delete: file → backups, deleted=1, hidden from the list ─────────────────

func TestDeleteSystemAgent(t *testing.T) {
	srv, db, claude, backups := systemCreateServer(t)
	out := doJSON(t, http.MethodPost, srv.URL+"/api/system/agents", createBody(), http.StatusCreated)
	id := int64(out["id"].(float64))
	path := filepath.Join(claude, "agents", "new-agent.md")
	content := readDisk(t, path)

	doJSON(t, http.MethodDelete, srv.URL+"/api/system/agents/"+itoa(id), nil, http.StatusOK)

	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("deleted file must be MOVED off its path, stat err = %v", err)
	}
	bak := findInBackups(t, backups, "new-agent.md")
	if bak == "" {
		t.Fatal("deleted file not found under config-backups")
	}
	if got := readDisk(t, bak); got != content {
		t.Errorf("backup content mismatch:\n%s", got)
	}
	var deleted int
	if err := db.QueryRow(`SELECT deleted FROM agents WHERE id = ?`, id).Scan(&deleted); err != nil {
		t.Fatal(err)
	}
	if deleted != 1 {
		t.Errorf("deleted = %d, want 1", deleted)
	}
	// The default list hides soft-deleted rows (Stage 1 contract).
	for _, n := range listAgentNames(t, srv.URL) {
		if n == "new-agent" {
			t.Error("soft-deleted agent still served by the list")
		}
	}
	// Deleting again: the row resolves as soft-deleted → 404.
	doJSON(t, http.MethodDelete, srv.URL+"/api/system/agents/"+itoa(id), nil, http.StatusNotFound)
}

// ── delete: plugin-managed rows are fenced (403) ─────────────────────────────

func TestDeleteSystemAgentPluginManaged(t *testing.T) {
	srv, db, claude, _ := systemCreateServer(t)
	path := filepath.Join(claude, "plugins", "cache", "mkt", "pl", "1.0.0", "agents", "p.md")
	writeFile(t, path, "---\nname: plugp\ndescription: plugin agent\n---\n\n## Boundaries\n\nbody\n")
	res, err := db.Exec(
		`INSERT INTO agents (name, scope, project_id, file_path, origin, plugin_name, deleted)
		 VALUES ('pl:p', 'global', NULL, ?, 'plugin', 'pl', 0)`, path)
	if err != nil {
		t.Fatal(err)
	}
	id, _ := res.LastInsertId()

	doJSON(t, http.MethodDelete, srv.URL+"/api/system/agents/"+itoa(id), nil, http.StatusForbidden)
	if _, err := os.Stat(path); err != nil {
		t.Errorf("plugin file must be untouched: %v", err)
	}
}

// ── restore: file back byte-for-byte, deleted=0, history untouched ──────────

func TestRestoreSystemAgent(t *testing.T) {
	srv, db, claude, _ := systemCreateServer(t)
	out := doJSON(t, http.MethodPost, srv.URL+"/api/system/agents", createBody(), http.StatusCreated)
	id := int64(out["id"].(float64))
	vid := int64(out["version_id"].(float64))
	path := filepath.Join(claude, "agents", "new-agent.md")
	content := readDisk(t, path)

	// Restoring a LIVE agent is a 409.
	doJSON(t, http.MethodPost, srv.URL+"/api/system/agents/"+itoa(id)+"/restore", nil, http.StatusConflict)

	doJSON(t, http.MethodDelete, srv.URL+"/api/system/agents/"+itoa(id), nil, http.StatusOK)
	res := doJSON(t, http.MethodPost, srv.URL+"/api/system/agents/"+itoa(id)+"/restore", nil, http.StatusOK)

	if int64(res["id"].(float64)) != id || int64(res["version_id"].(float64)) != vid {
		t.Errorf("restore response = %v, want id %d version %d", res, id, vid)
	}
	if got := readDisk(t, path); got != content {
		t.Errorf("restore must bring the latest version back byte-for-byte:\n%s", got)
	}
	var deleted int
	var current int64
	if err := db.QueryRow(`SELECT deleted, current_version_id FROM agents WHERE id = ?`, id).
		Scan(&deleted, &current); err != nil {
		t.Fatal(err)
	}
	if deleted != 0 || current != vid {
		t.Errorf("row = deleted %d current %d, want 0/%d", deleted, current, vid)
	}
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM agent_versions WHERE agent_id = ?`, id).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("restore must not mint versions (content-addressed): %d rows", n)
	}
	// Back in the default list.
	names := listAgentNames(t, srv.URL)
	found := false
	for _, nm := range names {
		if nm == "new-agent" {
			found = true
		}
	}
	if !found {
		t.Errorf("restored agent missing from the list: %v", names)
	}
}

// ── D4 + readonly fences on the new endpoints ────────────────────────────────

func TestCreateDeleteRestoreFences(t *testing.T) {
	srv, db, _, _ := systemCreateServer(t)

	// Foreign browser Origin → 403 before any handler logic.
	for _, m := range []struct{ method, path string }{
		{http.MethodPost, "/api/system/agents"},
		{http.MethodDelete, "/api/system/agents/1"},
		{http.MethodPost, "/api/system/agents/1/restore"},
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
			t.Errorf("%s %s with foreign Origin: %d, want 403", m.method, m.path, resp.StatusCode)
		}
	}

	// Kill-switch → 403 on create.
	t.Setenv(sysedit.EnvReadOnly, "1")
	doJSON(t, http.MethodPost, srv.URL+"/api/system/agents", createBody(), http.StatusForbidden)
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM agents`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Errorf("readonly create still minted %d rows", n)
	}
}
