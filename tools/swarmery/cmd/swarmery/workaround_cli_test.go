package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/ingest"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/store"
)

func wbLine(t *testing.T, rec map[string]any) string {
	t.Helper()
	b, err := json.Marshal(rec)
	if err != nil {
		t.Fatal(err)
	}
	return string(b) + "\n"
}

func wbWrite(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// A run-plan session ingested BEFORE task_sessions linked it to the plan: the
// tail stored nothing; the backfill finds the executor's sidechain prose on
// disk, attributes it to the phase its dispatch names, and a second run adds
// nothing.
func TestWorkaroundBackfillIsIdempotent(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "swarmery.db")
	db, err := store.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	const sid = "bbbbbbbb-0000-4000-8000-000000000001"
	ts := time.Now().UTC().Add(-48 * time.Hour)
	at := func(sec int) string { return ts.Add(time.Duration(sec) * time.Second).Format("2006-01-02T15:04:05.000Z") }
	env := func(typ, uuid string, sec int, msg any, extra map[string]any) map[string]any {
		r := map[string]any{"type": typ, "uuid": uuid, "timestamp": at(sec), "sessionId": sid,
			"cwd": "/tmp/wbproj", "version": "2.1.170", "gitBranch": "main", "message": msg}
		for k, v := range extra {
			r[k] = v
		}
		return r
	}
	asst := func(id string, blocks ...any) map[string]any {
		return map[string]any{"model": "claude-fable-5", "id": id, "role": "assistant", "content": blocks,
			"usage": map[string]int{"input_tokens": 1, "output_tokens": 1}}
	}
	text := func(s string) map[string]any { return map[string]any{"type": "text", "text": s} }

	mainPath := filepath.Join(dir, "projects", "-tmp-wbproj", sid+".jsonl")
	wbWrite(t, mainPath,
		wbLine(t, env("user", "u1", 0, map[string]any{"role": "user", "content": "run the plan"}, nil))+
			wbLine(t, env("assistant", "a1", 1, asst("msg_WB1", text("Dispatching phase 1.")), nil)))
	side := map[string]any{"isSidechain": true, "agentId": "exec1"}
	scBase := filepath.Join(dir, "projects", "-tmp-wbproj", sid, "subagents", "agent-exec1")
	wbWrite(t, scBase+".meta.json", `{"agentType":"core:implementation-agent","description":"Phase 1 build it"}`)
	wbWrite(t, scBase+".jsonl",
		wbLine(t, env("assistant", "s1", 2, asst("msg_WB2", text("The lint hits are pre-existing.")), side))+
			wbLine(t, env("assistant", "s2", 3, asst("msg_WB3", map[string]any{"type": "tool_use", "id": "toolu_HB",
				"name": "SubagentHandback", "input": map[string]string{"message": "The guard blocked it, so I used Edit instead."}}), side)))

	if _, err := ingest.File(db, mainPath); err != nil {
		t.Fatal(err)
	}
	countHits := func() int {
		var n int
		if err := db.QueryRow(`SELECT COUNT(*) FROM workaround_hits`).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	if n := countHits(); n != 0 {
		t.Fatalf("the session was not linked at ingest, yet %d hits were stored", n)
	}

	// Link the session to a plan with a phase 1, after the fact.
	mustExec := func(q string, args ...any) int64 {
		t.Helper()
		res, err := db.Exec(q, args...)
		if err != nil {
			t.Fatalf("%s: %v", q, err)
		}
		id, _ := res.LastInsertId()
		return id
	}
	var projectID, sessionID int64
	if err := db.QueryRow(`SELECT project_id, id FROM sessions WHERE session_uuid = ?`, sid).Scan(&projectID, &sessionID); err != nil {
		t.Fatal(err)
	}
	taskID := mustExec(`INSERT INTO tasks (project_id, title, prompt, created_at, source, external_id)
		VALUES (?, 'plan', 'p', ?, 'workspace', '2026-10-20-wb-plan')`, projectID, at(0))
	phaseID := mustExec(`INSERT INTO epic_phases (workspace_task_id, seq, name, doc_path) VALUES (?, 1, 'Phase 1 — build it', '/ws/p1.md')`, taskID)
	mustExec(`INSERT INTO task_sessions (task_id, session_id, link_source, confidence) VALUES (?, ?, 'heuristic', 0.9)`, taskID, sessionID)

	var outs []string
	for run := 0; run < 2; run++ {
		var out bytes.Buffer
		if err := cmdWorkaroundBackfill([]string{"--db", dbPath, "--since", "30d"}, &out); err != nil {
			t.Fatalf("run %d: %v", run, err)
		}
		outs = append(outs, out.String())
		if n := countHits(); n != 2 {
			t.Fatalf("run %d: %d hits, want 2 (pre-existing + instead)\n%s", run, n, out.String())
		}
	}
	if !strings.Contains(outs[0], "2 new hits") || !strings.Contains(outs[1], "0 new hits") {
		t.Errorf("first run must add 2, the second 0:\n%s\n%s", outs[0], outs[1])
	}
	if !strings.Contains(outs[1], "2026-10-20-wb-plan  phase 1") || !strings.Contains(outs[1], "2 hits") {
		t.Errorf("the per-phase count is missing:\n%s", outs[1])
	}
	var attributed int
	if err := db.QueryRow(`SELECT COUNT(*) FROM workaround_hits WHERE phase_id = ? AND agent_name = 'core:implementation-agent'`,
		phaseID).Scan(&attributed); err != nil || attributed != 2 {
		t.Errorf("%d hits attributed to the phase and its executor (%v), want 2", attributed, err)
	}
}

func TestWorkaroundBackfillRejectsBadSince(t *testing.T) {
	var out bytes.Buffer
	err := cmdWorkaroundBackfill([]string{"--db", filepath.Join(t.TempDir(), "x.db"), "--since", "soon"}, &out)
	if !isUsage(err) {
		t.Errorf("--since soon: err = %v, want a usage error", err)
	}
	for _, ok := range []string{"30d", "1d", "36h"} {
		if _, err := parseSince(ok); err != nil {
			t.Errorf("parseSince(%q): %v", ok, err)
		}
	}
	for _, bad := range []string{"0d", "-3d", "d", "-1h"} {
		if _, err := parseSince(bad); err == nil {
			t.Errorf("parseSince(%q) accepted", bad)
		}
	}
}

