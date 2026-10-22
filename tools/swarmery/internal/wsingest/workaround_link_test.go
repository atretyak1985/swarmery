package wsingest

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/ingest"
)

// An interactive run-plan session is tailed before this scanner links it to its
// plan. The NEW link scans it once — the executor's sidechain prose included —
// and re-upserting an existing link does not scan again.
func TestNewLinkScansWorkaroundWords(t *testing.T) {
	db := testDB(t)
	dir := t.TempDir()
	const sid = "cccccccc-0000-4000-8000-000000000001"
	line := func(rec map[string]any) string {
		rec["sessionId"], rec["cwd"], rec["version"], rec["gitBranch"] = sid, "/tmp/wlproj", "2.1.170", "main"
		b, _ := json.Marshal(rec)
		return string(b) + "\n"
	}
	write := func(path, body string) {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	asst := func(id, text string) map[string]any {
		return map[string]any{"model": "claude-fable-5", "id": id, "role": "assistant",
			"content": []any{map[string]string{"type": "text", "text": text}}, "usage": map[string]int{"input_tokens": 1}}
	}
	mainPath := filepath.Join(dir, sid+".jsonl")
	write(mainPath, line(map[string]any{"type": "user", "uuid": "u1", "timestamp": "2026-10-09T10:00:00.000Z",
		"message": map[string]any{"role": "user", "content": "run the plan"}}))
	sc := filepath.Join(dir, sid, "subagents", "agent-e1")
	write(sc+".meta.json", `{"agentType":"core:implementation-agent","description":"Phase 1 build"}`)
	write(sc+".jsonl", line(map[string]any{"type": "assistant", "uuid": "s1", "timestamp": "2026-10-09T10:00:01.000Z",
		"isSidechain": true, "agentId": "e1", "message": asst("msg_WL1", "The test was flaky, so I skipped it for now.")}))
	if _, err := ingest.File(db, mainPath); err != nil {
		t.Fatal(err)
	}

	var projectID, sessionID int64
	if err := db.QueryRow(`SELECT project_id, id FROM sessions WHERE session_uuid = ?`, sid).Scan(&projectID, &sessionID); err != nil {
		t.Fatal(err)
	}
	res, err := db.Exec(`INSERT INTO tasks (project_id, title, prompt, created_at, source, external_id)
		VALUES (?, 'plan', 'p', '2026-10-09T00:00:00Z', 'workspace', '2026-10-09-wl')`, projectID)
	if err != nil {
		t.Fatal(err)
	}
	taskID, _ := res.LastInsertId()
	res, err = db.Exec(`INSERT INTO epic_phases (workspace_task_id, seq, name, doc_path) VALUES (?, 1, 'Phase 1', '/ws/p1.md')`, taskID)
	if err != nil {
		t.Fatal(err)
	}
	phaseID, _ := res.LastInsertId()
	hits := func() (n, onPhase int) {
		t.Helper()
		if err := db.QueryRow(`SELECT COUNT(*), COALESCE(SUM(phase_id = ?), 0) FROM workaround_hits`, phaseID).Scan(&n, &onPhase); err != nil {
			t.Fatal(err)
		}
		return n, onPhase
	}
	if n, _ := hits(); n != 0 {
		t.Fatalf("%d hits before the link", n)
	}

	s := New(db, Config{})
	conf := 0.9
	if err := s.upsertLink(taskID, sessionID, "heuristic", &conf); err != nil {
		t.Fatal(err)
	}
	// flaky, skipped, for now — one sentence, three words.
	if n, onPhase := hits(); n != 3 || onPhase != 3 {
		t.Fatalf("after the new link: %d hits (%d on the phase), want 3 on phase %d", n, onPhase, phaseID)
	}

	if _, err := db.Exec(`DELETE FROM workaround_hits`); err != nil {
		t.Fatal(err)
	}
	if err := s.upsertLink(taskID, sessionID, "heuristic", &conf); err != nil {
		t.Fatal(err)
	}
	if n, _ := hits(); n != 0 {
		t.Errorf("an existing link re-scanned the session (%d hits)", n)
	}
}
