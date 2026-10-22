package ingest

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/workaround"
)

const waCwd = "/tmp/waproj"

// waLine renders one transcript record of session sid. blocks is the content
// array of an assistant message (msgID set) or a user message (msgID "").
func waLine(sid, uuid, msgID, ts, blocks string, extra map[string]any) string {
	rec := map[string]any{
		"uuid": uuid, "timestamp": ts, "sessionId": sid, "cwd": waCwd, "version": "2.1.170", "gitBranch": "main",
	}
	if msgID != "" {
		rec["type"] = "assistant"
		rec["message"] = json.RawMessage(`{"model":"claude-fable-5","id":"` + msgID + `","role":"assistant","content":[` + blocks +
			`],"usage":{"input_tokens":1,"output_tokens":1,"cache_creation_input_tokens":0,"cache_read_input_tokens":0}}`)
	} else {
		rec["type"] = "user"
		rec["message"] = json.RawMessage(`{"role":"user","content":[` + blocks + `]}`)
	}
	for k, v := range extra {
		rec[k] = v
	}
	b, _ := json.Marshal(rec)
	return string(b) + "\n"
}

func waText(s string) string {
	b, _ := json.Marshal(map[string]string{"type": "text", "text": s})
	return string(b)
}

func waPrompt(sid string) string {
	return waLine(sid, "p-"+sid, "", "2026-10-09T10:00:00.000Z", waText("Run the phase."), nil)
}

// waPlan seeds a project, a workspace task (the plan) and two phase rows.
func waPlan(t *testing.T, db *sql.DB) (taskID, phase1, phase2 int64) {
	t.Helper()
	exec := func(q string, args ...any) int64 {
		t.Helper()
		res, err := db.Exec(q, args...)
		if err != nil {
			t.Fatalf("%s: %v", q, err)
		}
		id, _ := res.LastInsertId()
		return id
	}
	proj := exec(`INSERT INTO projects (path, slug, first_seen) VALUES (?, '-tmp-waproj', '2026-10-09T00:00:00Z')`, waCwd)
	taskID = exec(`INSERT INTO tasks (project_id, title, prompt, created_at, source, external_id)
		VALUES (?, 'plan', 'p', '2026-10-09T00:00:00Z', 'workspace', '2026-10-09-plan')`, proj)
	phase1 = exec(`INSERT INTO epic_phases (workspace_task_id, seq, name, doc_path) VALUES (?, 1, 'Phase 1', '/ws/plan/phase-1.md')`, taskID)
	phase2 = exec(`INSERT INTO epic_phases (workspace_task_id, seq, name, doc_path) VALUES (?, 2, 'Phase 2', '/ws/plan/phase-2.md')`, taskID)
	return taskID, phase1, phase2
}

func writeFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func hitsOf(t *testing.T, db *sql.DB, sid string) []workaround.Row {
	t.Helper()
	rows, err := db.Query(`SELECT turn_id, COALESCE(phase_id, 0), COALESCE(agent_name, ''), word, sentence, context_before, context_after
		FROM workaround_hits WHERE session_uuid = ? ORDER BY id`, sid)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []workaround.Row
	for rows.Next() {
		var r workaround.Row
		if err := rows.Scan(&r.TurnID, &r.PhaseID, &r.AgentName, &r.Word, &r.Sentence, &r.Before, &r.After); err != nil {
			t.Fatal(err)
		}
		r.SessionUUID = sid
		out = append(out, r)
	}
	return out
}

// A daemon phase run: the tail stores the hits of its assistant prose, with
// the phase, and never scans tool output; a replay adds nothing.
func TestWorkaroundHitsForPhaseRun(t *testing.T) {
	db := testDB(t)
	_, phase1, _ := waPlan(t, db)
	const sid = "aaaaaaaa-0000-4000-8000-000000000001"
	if _, err := db.Exec(`UPDATE epic_phases SET run_session_uuid = ? WHERE id = ?`, sid, phase1); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), sid+".jsonl")
	body := waPrompt(sid) +
		waLine(sid, "a1", "msg_WA1", "2026-10-09T10:00:01.000Z",
			waText("The reclaim refused the branch. I checked it out instead of re-cutting it. Tests pass."), nil) +
		waLine(sid, "a2", "msg_WA2", "2026-10-09T10:00:02.000Z",
			`{"type":"tool_use","id":"toolu_WA1","name":"Bash","input":{"command":"echo instead"}}`, nil) +
		waLine(sid, "u2", "", "2026-10-09T10:00:03.000Z",
			`{"type":"tool_result","tool_use_id":"toolu_WA1","content":"we skip this for now"}`, nil)
	writeFile(t, path, body)

	if _, err := TailFile(db, path, "", Thresholds{}); err != nil {
		t.Fatal(err)
	}
	hits := hitsOf(t, db, sid)
	if len(hits) != 1 {
		t.Fatalf("hits = %+v, want exactly the prose one (tool input/output are never scanned)", hits)
	}
	h := hits[0]
	if h.PhaseID != phase1 || h.AgentName != "" || h.Word != "instead" ||
		h.Sentence != "I checked it out instead of re-cutting it." ||
		h.Before != "The reclaim refused the branch." || h.After != "Tests pass." {
		t.Errorf("hit = %+v", h)
	}
	var turnText string
	if err := db.QueryRow(`SELECT text FROM turns WHERE id = ?`, h.TurnID).Scan(&turnText); err != nil || !strings.Contains(turnText, "instead") {
		t.Errorf("turn_id %d does not point at the turn holding the prose (%q, %v)", h.TurnID, turnText, err)
	}

	// A full re-read (File) replays every line: nothing new.
	if _, err := File(db, path); err != nil {
		t.Fatal(err)
	}
	if n := count(t, db, `SELECT COUNT(*) FROM workaround_hits`); n != 1 {
		t.Errorf("after a replay: %d hits, want 1", n)
	}
}

// A session nobody linked to a phase or a plan is not scanned at all.
func TestWorkaroundHitsSkipUnlinkedSession(t *testing.T) {
	db := testDB(t)
	waPlan(t, db)
	const sid = "aaaaaaaa-0000-4000-8000-000000000002"
	path := filepath.Join(t.TempDir(), sid+".jsonl")
	writeFile(t, path, waPrompt(sid)+waLine(sid, "a1", "msg_WB1", "2026-10-09T10:00:01.000Z",
		waText("I used a workaround for now."), nil))
	if _, err := TailFile(db, path, "", Thresholds{}); err != nil {
		t.Fatal(err)
	}
	if n := count(t, db, `SELECT COUNT(*) FROM workaround_hits`); n != 0 {
		t.Errorf("an unlinked session produced %d hits", n)
	}
}

// An interactive run-plan session: task_sessions links it to the plan; a
// subagent dispatched as "Phase 2 …" is attributed to phase 2 — its text AND
// its SubagentHandback report; a reviewer subagent is skipped; the
// orchestrator's own prose is kept with no phase.
func TestWorkaroundHitsRunPlanSidechains(t *testing.T) {
	db := testDB(t)
	taskID, _, phase2 := waPlan(t, db)
	const sid = "aaaaaaaa-0000-4000-8000-000000000003"
	dir := t.TempDir()
	path := filepath.Join(dir, sid+".jsonl")
	writeFile(t, path, waPrompt(sid))
	if _, err := File(db, path); err != nil {
		t.Fatal(err)
	}
	var sessionID int64
	if err := db.QueryRow(`SELECT id FROM sessions WHERE session_uuid = ?`, sid).Scan(&sessionID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO task_sessions (task_id, session_id, link_source, confidence) VALUES (?, ?, 'heuristic', 0.8)`,
		taskID, sessionID); err != nil {
		t.Fatal(err)
	}

	writeFile(t, path, waPrompt(sid)+waLine(sid, "a1", "msg_WC1", "2026-10-09T10:00:01.000Z",
		waText("Phases 1 and 2 run for now in parallel."), nil))
	sc := func(agentID, agentType, desc, body string) {
		base := filepath.Join(dir, sid, "subagents", "agent-"+agentID)
		writeFile(t, base+".meta.json", fmt.Sprintf(`{"agentType":%q,"description":%q}`, agentType, desc))
		writeFile(t, base+".jsonl", body)
	}
	side := map[string]any{"isSidechain": true, "agentId": "exec1"}
	sc("exec1", "core:implementation-agent", "Phase 2 build it",
		waLine(sid, "s1", "msg_WC2", "2026-10-09T10:00:02.000Z", waText("The test is flaky. Rerunning."), side)+
			waLine(sid, "s2", "msg_WC3", "2026-10-09T10:00:03.000Z",
				`{"type":"tool_use","id":"toolu_HB","name":"SubagentHandback","input":{"message":"## Report\nThe guard blocked the heredoc, so I used Edit instead.\nPHASE DONE"}}`, side))
	rev := map[string]any{"isSidechain": true, "agentId": "rev1"}
	sc("rev1", "core:code-reviewer", "Review Phase 2 diff",
		waLine(sid, "r1", "msg_WC4", "2026-10-09T10:00:04.000Z", waText("Blocked-guard bypass only? Yes."), rev))

	if _, err := File(db, path); err != nil {
		t.Fatal(err)
	}
	got := map[string]workaround.Row{}
	for _, h := range hitsOf(t, db, sid) {
		got[h.Word] = h
	}
	if len(got) != 3 {
		t.Fatalf("hits = %+v, want for now (orchestrator), flaky + instead (executor), none from the reviewer", got)
	}
	if h := got["for now"]; h.PhaseID != 0 || h.AgentName != "" {
		t.Errorf("orchestrator hit = %+v, want no phase and no agent", h)
	}
	if h := got["flaky"]; h.PhaseID != phase2 || h.AgentName != "core:implementation-agent" {
		t.Errorf("executor text hit = %+v, want phase %d", h, phase2)
	}
	if h := got["instead"]; h.PhaseID != phase2 || h.Sentence != "The guard blocked the heredoc, so I used Edit instead." ||
		h.Before != "## Report" || h.After != "PHASE DONE" {
		t.Errorf("handback hit = %+v", h)
	}
	if _, ok := got["bypass"]; ok {
		t.Error("the reviewer's prose was scanned")
	}

	// The API's reads see the phase's hits.
	ph, err := workaround.ForPhase(db, phase2, "")
	if err != nil || ph.Total != 2 || len(ph.Hits) != 2 || ph.Hits[0].Word != "flaky" {
		t.Errorf("ForPhase = %+v, %v", ph, err)
	}
	byTask, err := workaround.ForTask(db, taskID)
	if err != nil || byTask[phase2].Total != 2 || len(byTask) != 1 {
		t.Errorf("ForTask = %+v, %v", byTask, err)
	}
}

// Past 50 text-bearing hits a session's hits keep their key only: the count is
// exact, the prose is bounded, and a replay still adds nothing.
func TestWorkaroundHitsCapPerSession(t *testing.T) {
	db := testDB(t)
	_, phase1, _ := waPlan(t, db)
	const sid = "aaaaaaaa-0000-4000-8000-000000000004"
	if _, err := db.Exec(`UPDATE epic_phases SET run_session_uuid = ? WHERE id = ?`, sid, phase1); err != nil {
		t.Fatal(err)
	}
	var b strings.Builder
	b.WriteString(waPrompt(sid))
	const n = workaround.SessionCap + 10
	for i := 0; i < n; i++ {
		b.WriteString(waLine(sid, fmt.Sprintf("a%d", i), fmt.Sprintf("msg_CAP%03d", i),
			fmt.Sprintf("2026-10-09T10:%02d:%02d.000Z", 1+i/60, i%60), waText(fmt.Sprintf("Turn %d: I skip it.", i)), nil))
	}
	path := filepath.Join(t.TempDir(), sid+".jsonl")
	writeFile(t, path, b.String())
	for pass := 0; pass < 2; pass++ {
		if _, err := File(db, path); err != nil {
			t.Fatal(err)
		}
		if got := count(t, db, `SELECT COUNT(*) FROM workaround_hits WHERE session_uuid = ?`, sid); got != n {
			t.Errorf("pass %d: %d rows, want %d", pass, got, n)
		}
		if got := count(t, db, `SELECT COUNT(*) FROM workaround_hits WHERE sentence <> ''`); got != workaround.SessionCap {
			t.Errorf("pass %d: %d rows carry text, want the cap %d", pass, got, workaround.SessionCap)
		}
	}
	ph, err := workaround.ForPhase(db, phase1, sid)
	if err != nil || ph.Total != n || len(ph.Hits) != workaround.SessionCap {
		t.Errorf("ForPhase: total %d hits %d err %v", ph.Total, len(ph.Hits), err)
	}
}

// The hits ride the tail's transaction: a batch that rolls back leaves none.
func TestWorkaroundHitsRideTheTailTransaction(t *testing.T) {
	db := testDB(t)
	_, phase1, _ := waPlan(t, db)
	const sid = "aaaaaaaa-0000-4000-8000-000000000005"
	if _, err := db.Exec(`UPDATE epic_phases SET run_session_uuid = ? WHERE id = ?`, sid, phase1); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), sid+".jsonl")
	writeFile(t, path, waPrompt(sid)+waLine(sid, "a1", "msg_WE1", "2026-10-09T10:00:01.000Z", waText("A workaround."), nil))
	var stats Stats
	recs, _, err := readRecords(path, &stats)
	if err != nil {
		t.Fatal(err)
	}
	fi, _ := os.Stat(path)
	for _, commit := range []bool{false, true} {
		tx, err := db.Begin()
		if err != nil {
			t.Fatal(err)
		}
		in := &ingester{tx: tx, stats: &stats, thresholds: DefaultThresholds()}
		if err := in.upsertProjectAndSession(recs, fi.ModTime(), false); err != nil {
			t.Fatal(err)
		}
		if err := in.processRecords(recs, path, false, "", 0, ""); err != nil {
			t.Fatal(err)
		}
		var inTx int
		if err := tx.QueryRow(`SELECT COUNT(*) FROM workaround_hits`).Scan(&inTx); err != nil || inTx != 1 {
			t.Fatalf("inside the tx: %d hits (%v), want 1", inTx, err)
		}
		if commit {
			err = tx.Commit()
		} else {
			err = tx.Rollback()
		}
		if err != nil {
			t.Fatal(err)
		}
		want := 0
		if commit {
			want = 1
		}
		if got := count(t, db, `SELECT COUNT(*) FROM workaround_hits`); got != want {
			t.Errorf("commit=%t: %d hits after the tx, want %d", commit, got, want)
		}
	}
}
