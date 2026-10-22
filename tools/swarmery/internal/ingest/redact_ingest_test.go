package ingest

import (
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/redact"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/redact/redacttest"
)

const redactSessionUUID = "5e55a0a5-0000-4000-8000-00000000e501"

// redactTranscript writes a transcript carrying every secret class (built at
// run time, see redacttest) into each place ingest stores text from: the
// user prompt, assistant prose, Bash input and output, a Read result, an Edit
// (input, result and diff), a TodoWrite, an unknown record type, a
// compact_boundary and an api_error system record.
func redactTranscript(t *testing.T) string {
	t.Helper()
	secrets := redacttest.Joined()
	github := redacttest.Samples()[1].Value
	newString := redacttest.SourceAssignment() + "\tkey := \"" + github + "\"\n"

	n := 0
	env := func(typ string, m map[string]any) map[string]any {
		n++
		m["type"] = typ
		m["uuid"] = "00000000-0000-4000-8000-0000000e5" + leftPad(n)
		m["timestamp"] = "2026-10-22T10:00:" + leftPad(n)[1:] + ".000Z"
		m["sessionId"] = redactSessionUUID
		m["cwd"] = "/Users/user/work/redact-app"
		m["gitBranch"] = "main"
		m["version"] = "2.1.300"
		m["entrypoint"] = "cli"
		m["isSidechain"] = false
		return m
	}
	usage := map[string]any{"input_tokens": 10, "output_tokens": 5}
	assistant := func(id string, content ...map[string]any) map[string]any {
		return env("assistant", map[string]any{"message": map[string]any{
			"model": "claude-fable-5", "id": id, "type": "message", "role": "assistant",
			"content": content, "usage": usage,
		}})
	}
	toolUse := func(id, name string, input map[string]any) map[string]any {
		return map[string]any{"type": "tool_use", "id": id, "name": name, "input": input}
	}
	toolResult := func(id string, result any) map[string]any {
		return env("user", map[string]any{
			"message": map[string]any{"role": "user", "content": []map[string]any{
				{"type": "tool_result", "tool_use_id": id, "content": "done"},
			}},
			"toolUseResult": result,
		})
	}

	recs := []map[string]any{
		env("user", map[string]any{"message": map[string]any{"role": "user", "content": "please use " + secrets}}),
		assistant("msg_e501", map[string]any{"type": "text", "text": "noted: " + secrets}),
		assistant("msg_e501", toolUse("toolu_e501", "Bash", map[string]any{
			"command": "deploy --env prod <<EOF\n" + secrets + "\nEOF", "description": "deploy"})),
		toolResult("toolu_e501", map[string]any{"stdout": secrets, "stderr": "", "interrupted": false}),
		assistant("msg_e502", toolUse("toolu_e502", "Read", map[string]any{"file_path": "/Users/user/work/redact-app/.env"})),
		toolResult("toolu_e502", map[string]any{"type": "text", "file": map[string]any{
			"filePath": "/Users/user/work/redact-app/.env", "content": secrets, "numLines": 14}}),
		assistant("msg_e503", toolUse("toolu_e503", "Edit", map[string]any{
			"file_path": "/Users/user/work/redact-app/main.go", "old_string": "\told()\n", "new_string": newString})),
		toolResult("toolu_e503", map[string]any{
			"filePath": "/Users/user/work/redact-app/main.go", "oldString": "\told()\n", "newString": newString,
			"originalFile": "package main\n\told()\n",
			"structuredPatch": []map[string]any{{
				"oldStart": 2, "oldLines": 1, "newStart": 2, "newLines": 2,
				"lines": []string{"-\told()", "+" + strings.TrimSuffix(redacttest.SourceAssignment(), "\n"), "+\tkey := \"" + github + "\""},
			}},
			"userModified": false, "replaceAll": false,
		}),
		assistant("msg_e504", toolUse("toolu_e504", "TodoWrite", map[string]any{"todos": []map[string]any{
			{"content": "rotate " + github, "status": "pending", "activeForm": "rotating"}}})),
		toolResult("toolu_e504", map[string]any{"oldTodos": []any{}, "newTodos": []any{}}),
		env("mystery-record", map[string]any{"note": secrets}),
		env("system", map[string]any{"subtype": "compact_boundary", "content": secrets}),
		env("system", map[string]any{"subtype": "api_error", "error": map[string]any{"message": secrets}}),
	}

	var b strings.Builder
	for _, r := range recs {
		line, err := json.Marshal(r)
		if err != nil {
			t.Fatal(err)
		}
		b.Write(line)
		b.WriteByte('\n')
	}
	dir := filepath.Join(t.TempDir(), "-Users-user-work-redact-app")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, redactSessionUUID+".jsonl")
	if err := os.WriteFile(path, []byte(b.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func leftPad(n int) string {
	s := "0" + string(rune('0'+n/10)) + string(rune('0'+n%10))
	return s[len(s)-3:]
}

// leakedColumns is redacttest.Leaks (every table of sqlite_master, text and
// blob values) failing the test on a read error.
func leakedColumns(t *testing.T, db *sql.DB, samples []redacttest.Sample) []string {
	t.Helper()
	leaks, err := redacttest.Leaks(db, samples)
	if err != nil {
		t.Fatal(err)
	}
	return leaks
}

// TestIngestStoresNoSecrets: secret-shaped text in any part of a transcript
// is masked before it reaches a table, while ordinary code inside an Edit is
// stored byte for byte.
func TestIngestStoresNoSecrets(t *testing.T) {
	db := testDB(t)
	if _, err := File(db, redactTranscript(t)); err != nil {
		t.Fatalf("ingest: %v", err)
	}
	if got := count(t, db, `SELECT COUNT(*) FROM events`); got < 8 {
		t.Fatalf("events = %d, want the transcript ingested (≥ 8)", got)
	}
	for _, leak := range leakedColumns(t, db, redacttest.Samples()) {
		t.Error(leak)
	}

	// D6: an Edit's code keeps the assignment to a variable named token; only
	// the literal token shape inside it is masked.
	var payload string
	if err := db.QueryRow(
		`SELECT payload FROM events WHERE tool_name = 'Edit' AND type = 'tool_call'`).Scan(&payload); err != nil {
		t.Fatal(err)
	}
	var p struct {
		Input struct {
			NewString string `json:"new_string"`
		} `json:"input"`
		Result struct {
			NewString string `json:"newString"`
		} `json:"result"`
	}
	if err := json.Unmarshal([]byte(payload), &p); err != nil {
		t.Fatal(err)
	}
	want := redacttest.SourceAssignment() + "\tkey := \"" + redact.Mark + "\"\n"
	if p.Input.NewString != want {
		t.Errorf("Edit input new_string = %q, want %q", p.Input.NewString, want)
	}
	if p.Result.NewString != want {
		t.Errorf("Edit result newString = %q, want %q", p.Result.NewString, want)
	}
	var diff string
	if err := db.QueryRow(`SELECT diff FROM file_changes`).Scan(&diff); err != nil {
		t.Fatal(err)
	}
	if line := "+" + strings.TrimSuffix(redacttest.SourceAssignment(), "\n"); !strings.Contains(diff, line) {
		t.Errorf("file_changes.diff lost the source line %q:\n%s", line, diff)
	}

	// A second full read of the same transcript is still a no-op: the redacted
	// prose is a prefix of itself, so turns.text is replaced, not appended.
	var before string
	if err := db.QueryRow(`SELECT COALESCE(group_concat(text, '|'), '') FROM turns`).Scan(&before); err != nil {
		t.Fatal(err)
	}
	if _, err := File(db, redactTranscript(t)); err != nil {
		t.Fatalf("re-ingest: %v", err)
	}
	var after string
	if err := db.QueryRow(`SELECT COALESCE(group_concat(text, '|'), '') FROM turns`).Scan(&after); err != nil {
		t.Fatal(err)
	}
	if after != before {
		t.Errorf("re-ingest changed turns.text (the extend rule appended):\nbefore %q\nafter  %q", before, after)
	}
}

// TestRereadOverRawStoredText: a row stored before ingest redacted (raw
// prose in turns.text) converges on a full re-read — the text is replaced by
// the redacted batch, not appended to the raw one.
func TestRereadOverRawStoredText(t *testing.T) {
	db := testDB(t)
	path := redactTranscript(t)
	if _, err := File(db, path); err != nil {
		t.Fatalf("ingest: %v", err)
	}
	var id int64
	var text string
	if err := db.QueryRow(`SELECT id, text FROM turns WHERE role = 'user' ORDER BY seq LIMIT 1`).Scan(&id, &text); err != nil {
		t.Fatal(err)
	}
	raw := "please use " + redacttest.Joined()
	if _, err := db.Exec(`UPDATE turns SET text = ? WHERE id = ?`, raw, id); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`DELETE FROM file_offsets`); err != nil {
		t.Fatal(err)
	}
	if _, err := File(db, path); err != nil {
		t.Fatalf("re-ingest: %v", err)
	}
	var got string
	if err := db.QueryRow(`SELECT text FROM turns WHERE id = ?`, id).Scan(&got); err != nil {
		t.Fatal(err)
	}
	if got != text {
		t.Errorf("turns.text after re-read = %q, want the redacted text %q", got, text)
	}
}
