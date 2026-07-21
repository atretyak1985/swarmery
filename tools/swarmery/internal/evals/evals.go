// Package evals imports promptfoo results.json files into the eval_* tables
// (retro improvement loop, phase 2) so the Retro scorecards can show a
// pass/fail chip per agent.
//
// Tolerant of promptfoo schema drift by contract: per result only the
// `success` bool is required — evalId, timestamps, stats, prompt shape and
// grading reasons are all best-effort with sensible fallbacks.
package evals

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"
)

// notesCap truncates failure reasons stored in eval_results.notes.
const notesCap = 500

// Result summarizes one import.
type Result struct {
	Agent     string
	Suite     string
	SuiteID   int64
	RunID     int64
	Passed    int
	Failed    int
	Cases     int
	StartedAt string
	// Skipped is true when a run with the same (suite, started_at) already
	// exists — the import is an idempotent no-op then.
	Skipped bool
}

// ── promptfoo results.json (best-effort shape) ─────────────────────────────

type pfGrading struct {
	Reason string `json:"reason"`
}

type pfResult struct {
	Success       *bool           `json:"success"`
	Error         string          `json:"error"`
	GradingResult pfGrading       `json:"gradingResult"`
	Prompt        json.RawMessage `json:"prompt"`
	Description   string          `json:"description"`
}

type pfStats struct {
	Successes *int `json:"successes"`
	Failures  *int `json:"failures"`
}

type pfResults struct {
	Timestamp string     `json:"timestamp"`
	Results   []pfResult `json:"results"`
	Stats     *pfStats   `json:"stats"`
}

type pfFile struct {
	EvalID    string    `json:"evalId"`
	Timestamp string    `json:"timestamp"`
	Results   pfResults `json:"results"`
}

// promptText resolves the eval_cases key for one result: prompt.raw, then
// prompt.label, then a plain string prompt, then the description, then a
// positional fallback — the importer never drops a case over a shape change.
func promptText(r pfResult, idx int) string {
	if len(r.Prompt) > 0 {
		var obj struct {
			Raw   string `json:"raw"`
			Label string `json:"label"`
		}
		if err := json.Unmarshal(r.Prompt, &obj); err == nil {
			if obj.Raw != "" {
				return obj.Raw
			}
			if obj.Label != "" {
				return obj.Label
			}
		}
		var s string
		if err := json.Unmarshal(r.Prompt, &s); err == nil && s != "" {
			return s
		}
	}
	if r.Description != "" {
		return r.Description
	}
	return fmt.Sprintf("case-%d", idx+1)
}

// caseStatus classifies one result: pass | fail | error.
func caseStatus(r pfResult) string {
	if r.Success != nil && *r.Success {
		return "pass"
	}
	if r.Error != "" {
		return "error"
	}
	return "fail"
}

// caseNotes returns the truncated failure reason (grading reason first, then
// the raw error); empty for passing cases.
func caseNotes(r pfResult) string {
	if r.Success != nil && *r.Success {
		return ""
	}
	reason := r.GradingResult.Reason
	if reason == "" {
		reason = r.Error
	}
	if rn := []rune(reason); len(rn) > notesCap {
		reason = string(rn[:notesCap])
	}
	return reason
}

// Import loads one promptfoo results.json for the named registry agent.
// Unknown agent names are a hard error listing the known ones; re-importing
// the same run (same suite + started_at) is a skip, not a duplicate.
func Import(db *sql.DB, agentName, path string) (Result, error) {
	var res Result
	res.Agent = agentName

	raw, err := os.ReadFile(path)
	if err != nil {
		return res, err
	}
	var f pfFile
	if err := json.Unmarshal(raw, &f); err != nil {
		return res, fmt.Errorf("parse %s: %w", path, err)
	}
	if f.Results.Results == nil {
		return res, fmt.Errorf("%s: no results.results[] array — not a promptfoo results file?", path)
	}

	agentID, versionID, err := resolveAgent(db, agentName)
	if err != nil {
		return res, err
	}

	// Suite: one per (agent, evalId), created on first sight.
	res.Suite = f.EvalID
	if res.Suite == "" {
		res.Suite = "promptfoo"
	}
	now := time.Now().UTC().Format(time.RFC3339)
	err = db.QueryRow(`SELECT id FROM eval_suites WHERE agent_id = ? AND name = ?`,
		agentID, res.Suite).Scan(&res.SuiteID)
	if err == sql.ErrNoRows {
		r, ierr := db.Exec(`INSERT INTO eval_suites (agent_id, name, created_at) VALUES (?, ?, ?)`,
			agentID, res.Suite, now)
		if ierr != nil {
			return res, ierr
		}
		res.SuiteID, err = r.LastInsertId()
	}
	if err != nil {
		return res, err
	}

	// Timestamps: file-level fields when present, else file mtime.
	res.StartedAt = f.Results.Timestamp
	if res.StartedAt == "" {
		res.StartedAt = f.Timestamp
	}
	if res.StartedAt == "" {
		if fi, serr := os.Stat(path); serr == nil {
			res.StartedAt = fi.ModTime().UTC().Format(time.RFC3339)
		} else {
			res.StartedAt = now
		}
	}

	// Idempotency: the same (suite, started_at) run is already imported.
	var existing int64
	err = db.QueryRow(`SELECT id FROM eval_runs WHERE suite_id = ? AND started_at = ?`,
		res.SuiteID, res.StartedAt).Scan(&existing)
	if err == nil {
		res.RunID, res.Skipped = existing, true
		return res, nil
	}
	if err != sql.ErrNoRows {
		return res, err
	}

	// passed/failed: results.stats when present, else counted from the cases.
	for _, r := range f.Results.Results {
		if r.Success != nil && *r.Success {
			res.Passed++
		} else {
			res.Failed++
		}
	}
	if s := f.Results.Stats; s != nil {
		if s.Successes != nil {
			res.Passed = *s.Successes
		}
		if s.Failures != nil {
			res.Failed = *s.Failures
		}
	}

	tx, err := db.Begin()
	if err != nil {
		return res, err
	}
	defer tx.Rollback()

	run, err := tx.Exec(`
		INSERT INTO eval_runs (suite_id, agent_version_id, started_at, finished_at, passed, failed)
		VALUES (?, ?, ?, ?, ?, ?)`,
		res.SuiteID, versionID, res.StartedAt, res.StartedAt, res.Passed, res.Failed)
	if err != nil {
		return res, err
	}
	if res.RunID, err = run.LastInsertId(); err != nil {
		return res, err
	}

	for i, r := range f.Results.Results {
		prompt := promptText(r, i)
		var caseID int64
		err := tx.QueryRow(`SELECT id FROM eval_cases WHERE suite_id = ? AND prompt = ?`,
			res.SuiteID, prompt).Scan(&caseID)
		if err == sql.ErrNoRows {
			cr, ierr := tx.Exec(`INSERT INTO eval_cases (suite_id, prompt) VALUES (?, ?)`,
				res.SuiteID, prompt)
			if ierr != nil {
				return res, ierr
			}
			caseID, err = cr.LastInsertId()
		}
		if err != nil {
			return res, err
		}
		notes := caseNotes(r)
		var notesArg any
		if notes != "" {
			notesArg = notes
		}
		if _, err := tx.Exec(`
			INSERT INTO eval_results (run_id, case_id, status, notes) VALUES (?, ?, ?, ?)`,
			res.RunID, caseID, caseStatus(r), notesArg); err != nil {
			return res, err
		}
		res.Cases++
	}
	return res, tx.Commit()
}

// resolveAgent maps an --agent name to (agent_id, agent_version_id). Global
// registry entries win over project-scoped ones on a name collision. A missing
// current_version_id falls back to the newest recorded version.
func resolveAgent(db *sql.DB, name string) (agentID, versionID int64, err error) {
	var cur sql.NullInt64
	err = db.QueryRow(`
		SELECT id, current_version_id FROM agents
		 WHERE deleted = 0 AND name = ?
		 ORDER BY scope = 'global' DESC, id LIMIT 1`, name).Scan(&agentID, &cur)
	if err == sql.ErrNoRows {
		return 0, 0, fmt.Errorf("unknown agent %q — known agents: %s", name, knownAgents(db))
	}
	if err != nil {
		return 0, 0, err
	}
	if cur.Valid {
		return agentID, cur.Int64, nil
	}
	err = db.QueryRow(`SELECT MAX(id) FROM agent_versions WHERE agent_id = ?`, agentID).Scan(&cur)
	if err != nil || !cur.Valid {
		return 0, 0, fmt.Errorf("agent %q has no recorded version — run a system scan first", name)
	}
	return agentID, cur.Int64, nil
}

// knownAgents renders the non-deleted registry names for the unknown-agent
// error message.
func knownAgents(db *sql.DB) string {
	rows, err := db.Query(`SELECT DISTINCT name FROM agents WHERE deleted = 0 ORDER BY name`)
	if err != nil {
		return "(unavailable)"
	}
	defer rows.Close()
	var names []string
	for rows.Next() {
		var n string
		if rows.Scan(&n) == nil {
			names = append(names, n)
		}
	}
	if len(names) == 0 {
		return "(none in the registry — run a system scan first)"
	}
	return strings.Join(names, ", ")
}
