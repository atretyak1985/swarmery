package ingest

// Startup data heal for Write-create diffs (HealStubSessions precedent):
// until fileChangeDiff synthesised a hunk from the result's `content`, a
// Write-create stored diff '' / +0 because Claude Code ships an EMPTY
// structuredPatch for it (§8). Unchanged transcripts are offset no-ops, so
// such rows would stay blank on the Diffs tab forever.
//
// HealCreateDiffs re-reads the transcript of every session that still owns a
// blank create row — the main file and its sidechains, since subagents write
// files too — matches each row's tool_use_id (the dedup_key suffix) against
// the tool_result records, and fills diff + additions in place. It runs on
// every Backfill pass; once nothing is blank it costs one query.

import (
	"bufio"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
)

// HealCreateDiffs fills diff/additions on Write-create rows that have none,
// from the transcripts under projectsRoots. Returns how many rows it filled.
// Sessions whose transcript is gone are left alone; a row whose tool_result
// cannot be found (or is not a create) stays blank and is retried next pass.
func HealCreateDiffs(db *sql.DB, projectsRoots []string) (int, error) {
	rows, err := db.Query(
		`SELECT fc.id, e.dedup_key, s.session_uuid
		 FROM file_changes fc
		 JOIN events e ON e.id = fc.event_id
		 JOIN sessions s ON s.id = fc.session_id
		 WHERE fc.change_type = 'create' AND (fc.diff IS NULL OR fc.diff = '')`)
	if err != nil {
		return 0, err
	}
	bySession := map[string]map[string]int64{} // session uuid → tool_use_id → file_changes.id
	for rows.Next() {
		var id int64
		var dedup, uuid string
		if err := rows.Scan(&id, &dedup, &uuid); err != nil {
			rows.Close()
			return 0, err
		}
		toolUseID := dedup[strings.LastIndex(dedup, "#")+1:]
		if toolUseID == "" {
			continue
		}
		if bySession[uuid] == nil {
			bySession[uuid] = map[string]int64{}
		}
		bySession[uuid][toolUseID] = id
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}

	healed := 0
	for uuid, pending := range bySession {
		main := FindTranscript(projectsRoots, uuid)
		if main == "" {
			continue
		}
		files := []string{main}
		sidechains, _ := filepath.Glob(filepath.Join(strings.TrimSuffix(main, ".jsonl"), "subagents", "agent-*.jsonl"))
		files = append(files, sidechains...)
		for _, f := range files {
			if len(pending) == 0 {
				break
			}
			n, err := healCreateDiffsIn(db, f, pending)
			if err != nil {
				return healed, err
			}
			healed += n
		}
	}
	return healed, nil
}

// healCreateDiffsIn scans one transcript for the tool_results in pending and
// updates their rows; matched ids are removed from pending. Malformed lines
// are skipped — the heal is best-effort by design.
func healCreateDiffsIn(db *sql.DB, path string, pending map[string]int64) (int, error) {
	f, err := os.Open(path)
	if err != nil {
		return 0, nil // transcript vanished between the glob and the open
	}
	defer f.Close()

	healed := 0
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), maxLineBytes)
	for sc.Scan() && len(pending) > 0 {
		var r record
		if err := json.Unmarshal(sc.Bytes(), &r); err != nil || r.Type != "user" || len(r.ToolUseResult) == 0 {
			continue
		}
		var am apiMessage
		if json.Unmarshal(r.Message, &am) != nil {
			continue
		}
		var blocks []contentBlock
		if json.Unmarshal(am.Content, &blocks) != nil {
			continue
		}
		for _, b := range blocks {
			id, ok := pending[b.ToolUseID]
			if b.Type != "tool_result" || !ok || b.IsError {
				continue
			}
			var fc fileChangeResult
			if json.Unmarshal(r.ToolUseResult, &fc) != nil || fc.Type != "create" {
				continue
			}
			diff, additions, deletions := fileChangeDiff(fc)
			if _, err := db.Exec(
				`UPDATE file_changes SET additions = ?, deletions = ?, diff = ? WHERE id = ?`,
				additions, deletions, diff, id); err != nil {
				return healed, err
			}
			delete(pending, b.ToolUseID)
			healed++
		}
	}
	return healed, nil
}
