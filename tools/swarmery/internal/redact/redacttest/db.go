package redacttest

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
)

// Leaks walks every table in sqlite_master and reports each table.column
// whose value (text or blob) still contains a sample's Needle. A report names
// the place and the class, never the value.
func Leaks(db *sql.DB, samples []Sample) ([]string, error) {
	rows, err := db.Query(`SELECT name FROM sqlite_master WHERE type = 'table'`)
	if err != nil {
		return nil, err
	}
	var tables []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			rows.Close()
			return nil, err
		}
		tables = append(tables, name)
	}
	rows.Close()

	var leaks []string
	for _, tbl := range tables {
		r, err := db.Query(`SELECT * FROM "` + tbl + `"`)
		if err != nil {
			continue // virtual-table internals that refuse a plain scan
		}
		cols, _ := r.Columns()
		for r.Next() {
			vals := make([]any, len(cols))
			ptrs := make([]any, len(cols))
			for i := range vals {
				ptrs[i] = &vals[i]
			}
			if err := r.Scan(ptrs...); err != nil {
				r.Close()
				return nil, err
			}
			for i, v := range vals {
				var s string
				switch x := v.(type) {
				case string:
					s = x
				case []byte:
					s = string(x)
				default:
					continue
				}
				for _, smp := range samples {
					if strings.Contains(s, smp.Needle) {
						leaks = append(leaks, tbl+"."+cols[i]+" holds a "+smp.Class+" value")
					}
				}
			}
		}
		r.Close()
	}
	return leaks, nil
}

// SeedStats counts what Seed wrote.
type SeedStats struct {
	Rows       int // rows written across all tables
	SecretRows int // rows whose text carries the samples
}

// Seed fills a migrated store with about rows rows of transcript-shaped data,
// written raw as a build before ingest redaction would have stored them:
// sessions, turns, events (Bash and Edit tool calls) and file_changes, every
// tenth row carrying every sample (Joined; LiteralJoined inside code). Edit
// payloads and diffs also hold SourceAssignment, which a rescrub must leave as
// is. One transaction.
func Seed(db *sql.DB, rows int) (SeedStats, error) {
	var st SeedStats
	tx, err := db.Begin()
	if err != nil {
		return st, err
	}
	defer tx.Rollback() //nolint:errcheck // a no-op after Commit
	res, err := tx.Exec(`INSERT INTO projects (path, slug, name, first_seen) VALUES
		('/Users/user/work/seed-app', 'seed-app', 'seed-app', '2026-10-22T00:00:00Z')`)
	if err != nil {
		return st, fmt.Errorf("seed project: %w", err)
	}
	projectID, _ := res.LastInsertId()

	secrets := Joined()
	clean := "ordinary output line: build ok, 42 tests passed\n"
	code := SourceAssignment()
	perSession := 100
	n := 0
	text := func() (string, bool) {
		n++
		if n%10 == 0 {
			return clean + secrets, true
		}
		return clean, false
	}
	count := func(secret bool) {
		st.Rows++
		if secret {
			st.SecretRows++
		}
	}
	for s := 0; st.Rows < rows; s++ {
		title, secret := text()
		res, err := tx.Exec(`INSERT INTO sessions (project_id, session_uuid, cwd, status, started_at, title)
			VALUES (?, ?, '/Users/user/work/seed-app', 'completed', ?, ?)`,
			projectID, fmt.Sprintf("5eed0000-0000-4000-8000-%012d", s), "2026-10-22T00:00:00Z", title)
		if err != nil {
			return st, fmt.Errorf("seed session: %w", err)
		}
		count(secret)
		sessionID, _ := res.LastInsertId()
		for i := 0; i < perSession && st.Rows < rows; i++ {
			ts := fmt.Sprintf("2026-10-22T%02d:%02d:%02dZ", (i/3600)%24, (i/60)%60, i%60)
			body, secret := text()
			res, err := tx.Exec(`INSERT INTO turns (session_id, seq, role, started_at, text) VALUES (?, ?, 'assistant', ?, ?)`,
				sessionID, i, ts, body)
			if err != nil {
				return st, fmt.Errorf("seed turn: %w", err)
			}
			count(secret)
			turnID, _ := res.LastInsertId()

			out, secret := text()
			payload, _ := json.Marshal(map[string]any{
				"input":  map[string]any{"command": "make test", "tool_use_id": fmt.Sprintf("toolu_b%d_%d", s, i)},
				"result": map[string]any{"stdout": out, "stderr": ""},
			})
			if _, err := tx.Exec(`INSERT INTO events (session_id, turn_id, ts, type, tool_name, status, payload, dedup_key)
				VALUES (?, ?, ?, 'tool_call', 'Bash', 'ok', ?, ?)`,
				sessionID, turnID, ts, string(payload), fmt.Sprintf("seed-b-%d-%d", s, i)); err != nil {
				return st, fmt.Errorf("seed bash event: %w", err)
			}
			count(secret)

			if i%5 != 0 {
				continue
			}
			// Code carries only the literal shapes: the contextual classes
			// are left alone inside code by design (see LiteralJoined).
			_, secret = text()
			newString := code + clean
			if secret {
				newString = code + LiteralJoined() + "\n"
			}
			payload, _ = json.Marshal(map[string]any{
				"input": map[string]any{"file_path": "/Users/user/work/seed-app/main.go",
					"old_string": "\told()\n", "new_string": newString},
			})
			res, err = tx.Exec(`INSERT INTO events (session_id, turn_id, ts, type, tool_name, status, payload, dedup_key)
				VALUES (?, ?, ?, 'tool_call', 'Edit', 'ok', ?, ?)`,
				sessionID, turnID, ts, string(payload), fmt.Sprintf("seed-e-%d-%d", s, i))
			if err != nil {
				return st, fmt.Errorf("seed edit event: %w", err)
			}
			count(secret)
			eventID, _ := res.LastInsertId()
			diff := "@@ -1,1 +1,2 @@\n-\told()\n+" + strings.ReplaceAll(strings.TrimSuffix(newString, "\n"), "\n", "\n+") + "\n"
			if _, err := tx.Exec(`INSERT INTO file_changes (event_id, session_id, file_path, change_type, additions, deletions, diff)
				VALUES (?, ?, '/Users/user/work/seed-app/main.go', 'edit', 2, 1, ?)`, eventID, sessionID, diff); err != nil {
				return st, fmt.Errorf("seed file change: %w", err)
			}
			count(secret)
		}
	}
	return st, tx.Commit()
}
