// Package trajjudge is the advisory LLM-judge (Verification Contour v2, Phase 2).
// It scores real agent trajectories on a 4-dimension rubric via headless
// claude -p and persists verdicts to trajectory_judgments. Advisory only: no
// verdict ever gates a merge. Best-effort by contract — a failed or unparseable
// verdict skips the candidate rather than persisting garbage or panicking.
package trajjudge

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"os/exec"
	"strings"
	"sync/atomic"
	"time"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/claudebin"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/systemspawn"
)

// Runner executes one judge prompt and returns the model's raw stdout.
// Mocked in tests; production is ClaudeRunner. Twin of internal/improve.Runner.
type Runner interface {
	Run(ctx context.Context, prompt string) (string, error)
}

// judgment is one parsed rubric verdict (scores are 1..5, higher = better).
type judgment struct {
	EndResult             int     `json:"end_result"`
	InstructionCompliance int     `json:"instruction_compliance"`
	Pitfalls              int     `json:"pitfalls"`
	ToolCalls             int     `json:"tool_calls"`
	Review                string  `json:"review"`
	Overall               float64 `json:"-"`
}

func inRange(n int) bool { return n >= 1 && n <= 5 }

// parseJudgment extracts the first JSON object from raw model output and
// validates every dimension is 1..5 and the review is non-empty. Any failure
// is an error — the caller skips the candidate, never persists a partial row.
func parseJudgment(raw string) (judgment, error) {
	var j judgment
	start := strings.IndexByte(raw, '{')
	end := strings.LastIndexByte(raw, '}')
	if start < 0 || end <= start {
		return j, fmt.Errorf("no JSON object in judge output")
	}
	if err := json.Unmarshal([]byte(raw[start:end+1]), &j); err != nil {
		return j, fmt.Errorf("unmarshal judge output: %w", err)
	}
	if !inRange(j.EndResult) || !inRange(j.InstructionCompliance) ||
		!inRange(j.Pitfalls) || !inRange(j.ToolCalls) {
		return j, fmt.Errorf("judge score out of 1..5 range: %+v", j)
	}
	if strings.TrimSpace(j.Review) == "" {
		return j, fmt.Errorf("judge review is empty")
	}
	j.Overall = float64(j.EndResult+j.InstructionCompliance+j.Pitfalls+j.ToolCalls) / 4.0
	return j, nil
}

const judgeCtxTimeout = 90 * time.Second

// inFlight serializes Score batches. The daemon tick and the manual retro
// trigger can overlap for up to capN×judgeCtxTimeout; candidates only leave
// the pool at persist time, so a concurrent batch would re-judge — and re-pay
// for — the same sessions. Skipping is safe: the next tick picks them up.
var inFlight atomic.Bool

// Score judges up to capN un-judged (session, agent) candidates for the given
// judge model, flagged-first then a bounded random sample. Best-effort: any
// candidate failure is logged and skipped; the batch never aborts. Advisory
// only — nothing here can block a merge.
func Score(db *sql.DB, runner Runner, model string, now time.Time, capN int) error {
	if capN <= 0 {
		return nil
	}
	if !inFlight.CompareAndSwap(false, true) {
		log.Printf("trajjudge: previous batch still in flight, skipping")
		return nil
	}
	defer inFlight.Store(false)
	cands, err := selectCandidates(db, model, capN)
	if err != nil {
		return err
	}
	for _, c := range cands {
		evs, err := loadAgentEvents(db, c.sessionID, c.agent)
		if err != nil || len(evs) == 0 {
			continue
		}
		prompt := buildRubricPrompt(summarizeTrajectory(evs))
		ctx, cancel := context.WithTimeout(context.Background(), judgeCtxTimeout)
		out, err := runner.Run(ctx, prompt)
		cancel()
		if err != nil {
			log.Printf("trajjudge: runner failed for session=%d agent=%s: %v", c.sessionID, c.agent, err)
			continue
		}
		j, err := parseJudgment(out)
		if err != nil {
			log.Printf("trajjudge: unparseable verdict for session=%d agent=%s: %v", c.sessionID, c.agent, err)
			continue
		}
		if err := persist(db, c.sessionID, c.agent, model, j, now); err != nil {
			log.Printf("trajjudge: persist failed for session=%d agent=%s: %v", c.sessionID, c.agent, err)
			continue
		}
	}
	return nil
}

// JudgedWithin reports whether any judgment for this model was persisted
// within window before now. main.go gates the startup Score batch on it so a
// dev day full of daemon restarts (`make install`) doesn't fire a full capN
// batch per restart. Fail-open by contract: an empty table or a query/parse
// error returns false, so a fresh install still gets its first batch. The
// manual "Analyze now" endpoint bypasses this gate on purpose.
func JudgedWithin(db *sql.DB, model string, now time.Time, window time.Duration) bool {
	var last sql.NullString
	err := db.QueryRow(
		`SELECT MAX(judged_at) FROM trajectory_judgments WHERE model = ?`, model).Scan(&last)
	if err != nil || !last.Valid {
		return false
	}
	t, err := time.Parse(time.RFC3339, last.String)
	if err != nil {
		return false
	}
	return now.Sub(t) < window
}

type candidate struct {
	sessionID int64
	agent     string
}

// selectCandidates returns up to capN (session, agent) rows from
// trajectory_scores that have no judgment for this model, ordered
// flagged-first (has a trajectory_findings row) then by session recency.
// Recency, not random sampling: Retro surfaces the most recent completed
// sessions, so judging newest-first is what makes verdicts visible instead
// of draining the pool oldest-first.
func selectCandidates(db *sql.DB, model string, capN int) ([]candidate, error) {
	// The judge must not score its own scoring runs. ClaudeRunner executes with
	// cwd ~/.swarmery, so every judge call is ingested as an ordinary 2-turn
	// session; left in the candidate pool the judge grades its own output, and
	// since those transcripts contain no work trajectory it rates them at the
	// floor. Measured before this exclusion: 29 such rows averaged 1.20 against
	// 3.01 for real work, and 27 of them landed on one model, making that model
	// look catastrophically bad on evidence that was never about the model.
	rows, err := db.Query(`
		SELECT s.session_id, s.agent
		FROM trajectory_scores s
		JOIN sessions sess ON sess.id = s.session_id
		LEFT JOIN trajectory_judgments j
		  ON j.session_id = s.session_id AND j.agent = s.agent AND j.model = ?
		WHERE j.id IS NULL
		  AND NOT EXISTS (
		        SELECT 1 FROM turns t
		         WHERE t.session_id = s.session_id
		           AND t.text LIKE ? || '%')
		ORDER BY
		  (SELECT COUNT(*) FROM trajectory_findings f WHERE f.score_id = s.id) DESC,
		  sess.started_at DESC,
		  s.session_id DESC, s.agent
		LIMIT ?`, model, RubricPreamble, capN)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []candidate
	for rows.Next() {
		var c candidate
		if err := rows.Scan(&c.sessionID, &c.agent); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// loadAgentEvents returns one agent's events in a session, ordered, using the
// same normalized-agent fold as trajeval so the join lines up.
func loadAgentEvents(db *sql.DB, sessionID int64, agent string) ([]event, error) {
	rows, err := db.Query(`
		SELECT e.id, e.type, COALESCE(e.tool_name,'')
		FROM events e
		LEFT JOIN turns t ON t.id = e.turn_id
		WHERE e.session_id = ? AND COALESCE(t.agent_name,'main') = ?
		ORDER BY e.id`, sessionID, agent)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []event
	for rows.Next() {
		var e event
		if err := rows.Scan(&e.seq, &e.typ, &e.tool); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// persist inserts one verdict; UNIQUE(session,agent,model) makes re-runs no-ops.
func persist(db *sql.DB, sessionID int64, agent, model string, j judgment, now time.Time) error {
	_, err := db.Exec(`
		INSERT INTO trajectory_judgments
		  (session_id, agent, model, judged_at, end_result, instruction_compliance, pitfalls, tool_calls, overall, review)
		VALUES (?,?,?,?,?,?,?,?,?,?)
		ON CONFLICT(session_id, agent, model) DO NOTHING`,
		sessionID, agent, model, now.UTC().Format(time.RFC3339),
		j.EndResult, j.InstructionCompliance, j.Pitfalls, j.ToolCalls, j.Overall, j.Review)
	return err
}

// ClaudeRunner runs `claude -p --model <id> --output-format text` with the
// prompt on stdin. Binary resolution is a plain PATH lookup (same as
// internal/toolproc). One of three twins — internal/improve.ClaudeRunner,
// internal/trajjudge.ClaudeRunner, and internal/handoff.ClaudeRunner — keep the
// flag order, stdin, System-cwd chdir, and stderr handling in lockstep.
type ClaudeRunner struct {
	Model string
}

func (r ClaudeRunner) Run(ctx context.Context, prompt string) (string, error) {
	// launchd hands the daemon a minimal PATH (/usr/bin:/bin:/usr/sbin:/sbin) that
	// omits every usual install dir, so a bare exec of "claude" fails with ENOENT
	// under the service while working in the operator's shell. Resolve explicitly.
	bin, err := claudebin.Resolve()
	if err != nil {
		return "", err
	}

	// --setting-sources project,local: skip user-level settings (global plugin
	// stack) — headless runs don't need them; project plugins and OAuth are
	// unaffected. Keep the flag order identical to the improve twin.
	cmd := exec.CommandContext(ctx, bin, "-p", "--model", r.Model, "--output-format", "text", "--setting-sources", "project,local")
	// Cwd and account in one decision, both taken from the System project home:
	// see internal/systemspawn for why they are inseparable and why a missing
	// home means neither.
	systemspawn.Attach(cmd)
	// One error path for all five runners: stdout is quoted alongside stderr,
	// because the CLI prints some failures there and exits with an empty stderr.
	return systemspawn.Run(ctx, cmd, prompt)
}
