package ingest

// Startup data heal for stub sessions (HealProjectNames precedent): sessions
// minted before their cwd was known — a hook POST that beat the JSONL tail,
// or a first tail batch of only header records — sit on the '(unknown)'
// project with empty cwd/started_at forever, because unchanged transcripts
// are offset no-ops and the per-batch heal never runs again for them.
//
// HealStubSessions re-reads each stub's transcript (located by session uuid
// under the projects root) and backfills project attribution, cwd,
// started_at, git_branch, and model. It runs on every Backfill pass, so
// existing databases converge on the first daemon restart after upgrading.

import (
	"bufio"
	"database/sql"
	"encoding/json"
	"log"
	"os"
	"path/filepath"
)

// transcriptMeta is the attribution subset of a transcript: the fields a stub
// session is missing.
type transcriptMeta struct {
	CWD       string
	FirstTS   string
	GitBranch string
	Model     string
}

// scanTranscriptMeta reads a transcript top-down and stops as soon as every
// attribution field is known (cwd/branch come from the first envelope
// records, the first timestamp from any record, model from the first
// assistant record). Malformed lines are skipped — attribution is
// best-effort by design.
func scanTranscriptMeta(path string) (transcriptMeta, error) {
	var m transcriptMeta
	f, err := os.Open(path)
	if err != nil {
		return m, err
	}
	defer f.Close()

	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), maxLineBytes)
	for sc.Scan() {
		var r record
		if err := json.Unmarshal(sc.Bytes(), &r); err != nil {
			continue
		}
		if m.FirstTS == "" && r.Timestamp != "" {
			m.FirstTS = r.Timestamp
		}
		if r.UUID != "" { // envelope record
			if m.CWD == "" && r.CWD != "" {
				m.CWD = r.CWD
			}
			if m.GitBranch == "" && r.GitBranch != "" {
				m.GitBranch = r.GitBranch
			}
		}
		if m.Model == "" && r.Type == "assistant" {
			var am apiMessage
			if json.Unmarshal(r.Message, &am) == nil && am.Model != "" {
				m.Model = am.Model
			}
		}
		if m.CWD != "" && m.FirstTS != "" && m.GitBranch != "" && m.Model != "" {
			break
		}
	}
	return m, sc.Err()
}

// stubSession is one heal candidate.
type stubSession struct {
	id          int64
	uuid        string
	cwd         sql.NullString
	startedAt   string
	projectPath string
}

// HealStubSessions re-attributes stub sessions — rows on the '(unknown)'
// project, or with an empty/placeholder cwd or empty started_at — from their
// transcript files (located by session uuid under projectsRoot). Good values
// are NEVER overwritten; sessions whose transcript cannot be found are left
// alone. Once no session references it any more, the '(unknown)' projects
// row is deleted. Returns the ids of healed sessions so callers can emit
// session_updated and dashboards converge without a reload.
func HealStubSessions(db *sql.DB, projectsRoot string) ([]int64, error) {
	rows, err := db.Query(
		`SELECT s.id, s.session_uuid, s.cwd, s.started_at, p.path
		 FROM sessions s JOIN projects p ON p.id = s.project_id
		 WHERE p.path = ? OR s.cwd IS NULL OR s.cwd IN ('', ?)
		    OR s.started_at = ''`,
		UnknownProjectPath, UnknownProjectPath)
	if err != nil {
		return nil, err
	}
	var todo []stubSession
	for rows.Next() {
		var s stubSession
		if err := rows.Scan(&s.id, &s.uuid, &s.cwd, &s.startedAt, &s.projectPath); err != nil {
			rows.Close()
			return nil, err
		}
		todo = append(todo, s)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}

	var healed []int64
	for _, s := range todo {
		matches, _ := filepath.Glob(filepath.Join(projectsRoot, "*", s.uuid+".jsonl"))
		if len(matches) == 0 {
			continue // transcript gone (or hook-only session) — keep the stub
		}
		meta, err := scanTranscriptMeta(matches[0])
		if err != nil {
			log.Printf("warn: ingest: heal session %s: %v", s.uuid, err)
			continue
		}
		changed := false

		if meta.CWD != "" && s.projectPath == UnknownProjectPath {
			projectID, _, err := UpsertProject(db, meta.CWD, meta.FirstTS, "")
			if err != nil {
				return healed, err
			}
			if _, err := db.Exec(
				`UPDATE sessions SET project_id = ? WHERE id = ?`, projectID, s.id); err != nil {
				return healed, err
			}
			changed = true
		}
		if meta.CWD != "" && (!s.cwd.Valid || s.cwd.String == "" || s.cwd.String == UnknownProjectPath) {
			if _, err := db.Exec(
				`UPDATE sessions SET cwd = ? WHERE id = ?`, meta.CWD, s.id); err != nil {
				return healed, err
			}
			changed = true
		}
		res, err := db.Exec(
			`UPDATE sessions
			 SET started_at = CASE WHEN started_at = '' AND ? != '' THEN ? ELSE started_at END,
			     git_branch = COALESCE(git_branch, ?),
			     model      = COALESCE(model, ?)
			 WHERE id = ? AND (
			       (started_at = '' AND ? != '')
			    OR (git_branch IS NULL AND ? IS NOT NULL)
			    OR (model IS NULL AND ? IS NOT NULL))`,
			meta.FirstTS, meta.FirstTS, nullStr(meta.GitBranch), nullStr(meta.Model),
			s.id, meta.FirstTS, nullStr(meta.GitBranch), nullStr(meta.Model))
		if err != nil {
			return healed, err
		}
		if n, _ := res.RowsAffected(); n > 0 {
			changed = true
		}
		if changed {
			healed = append(healed, s.id)
		}
	}

	// Drop the '(unknown)' placeholder project once nothing references it.
	if _, err := db.Exec(
		`DELETE FROM projects WHERE path = ?
		 AND NOT EXISTS (SELECT 1 FROM sessions      WHERE project_id = projects.id)
		 AND NOT EXISTS (SELECT 1 FROM tasks         WHERE project_id = projects.id)
		 AND NOT EXISTS (SELECT 1 FROM agents        WHERE project_id = projects.id)
		 AND NOT EXISTS (SELECT 1 FROM skills        WHERE project_id = projects.id)
		 AND NOT EXISTS (SELECT 1 FROM daily_rollups WHERE project_id = projects.id)
		 AND NOT EXISTS (SELECT 1 FROM workspaces    WHERE project_id = projects.id)`,
		UnknownProjectPath); err != nil {
		return healed, err
	}
	return healed, nil
}
