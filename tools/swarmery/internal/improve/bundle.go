package improve

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/advisor"
)

// Size caps: each transcript excerpt is truncated to excerptCap bytes; the
// assembled bundle never exceeds bundleCap (hard truncation with a marker).
const (
	excerptCap = 1536      // 1.5KB per excerpt
	bundleCap  = 30 * 1024 // ~30KB total evidence
)

// assessmentLimit caps the ledger-assessment rows quoted in the bundle.
const assessmentLimit = 20

// excerptGroups caps how many behavior-fixable error groups get a transcript
// excerpt.
const excerptGroups = 3

// doneRe — twin of advisor r5DoneRe (rules.go): an improvement whose status
// matches is closed, everything else counts as open. Keep in lockstep.
var doneRe = regexp.MustCompile(`(?i)done|closed|виконано`)

// Evidence is buildEvidence's output: the markdown bundle plus the agent
// source coordinates the prompt and the proposal row need. BaseSHA256 is the
// sha256 of AgentContent — the content the diff is generated against.
type Evidence struct {
	Bundle       string
	AgentPath    string
	AgentContent string
	BaseSHA256   string
}

// buildEvidence assembles the phase-3 evidence bundle for one (normalized)
// agent key: agent source via the sysscan registry, the advisor scorecard
// slice, low-quality ledger assessments, open improvements mentioning the
// agent, and transcript excerpts around the worst behavior-fixable errors.
// repo is unused by generation (reserved for the phase-4 apply pipeline).
func buildEvidence(db *sql.DB, agent, repo string) (*Evidence, error) {
	_ = repo
	src, err := resolveAgent(db, agent)
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256([]byte(src.content))
	ev := &Evidence{
		AgentPath:    src.path,
		AgentContent: src.content,
		BaseSHA256:   hex.EncodeToString(sum[:]),
	}

	now := time.Now()
	var b strings.Builder
	fmt.Fprintf(&b, "# Evidence — agent %s\n\nSource: %s (sha256 %s)\n", agent, src.path, ev.BaseSHA256)

	sc, err := advisor.ScorecardFor(db, agent, now)
	if err != nil {
		return nil, err
	}
	writeScorecard(&b, sc)

	if err := writeAssessments(&b, db, agent); err != nil {
		return nil, err
	}
	if err := writeImprovements(&b, db, agent); err != nil {
		return nil, err
	}
	if err := writeExcerpts(&b, db, sc, now); err != nil {
		return nil, err
	}

	ev.Bundle = capBundle(b.String())
	return ev, nil
}

// agentSrc is one resolved registry agent: current path + version content.
type agentSrc struct {
	path    string
	content string
}

// resolveAgent finds the live registry row whose folded name matches the
// (normalized) agent key, joined to its current version content. On a name
// collision a local agent beats a plugin one (the harness override rule);
// ties break on the lowest id for determinism.
func resolveAgent(db *sql.DB, agent string) (agentSrc, error) {
	rows, err := db.Query(`
		SELECT a.name, a.origin, a.file_path, v.content
		  FROM agents a
		  JOIN agent_versions v ON v.id = a.current_version_id
		 WHERE a.deleted = 0
		 ORDER BY a.id`)
	if err != nil {
		return agentSrc{}, err
	}
	defer rows.Close()
	var best agentSrc
	found, bestLocal := false, false
	for rows.Next() {
		var name, origin, path, content string
		if err := rows.Scan(&name, &origin, &path, &content); err != nil {
			return agentSrc{}, err
		}
		if advisor.NormAgent(name) != agent {
			continue
		}
		local := origin == "local"
		if !found || (local && !bestLocal) {
			best = agentSrc{path: path, content: content}
			found, bestLocal = true, local
		}
	}
	if err := rows.Err(); err != nil {
		return agentSrc{}, err
	}
	if !found {
		return agentSrc{}, fmt.Errorf("%w: %q", ErrAgentNotFound, agent)
	}
	return best, nil
}

// writeScorecard renders the advisor window slice (same numbers as the Retro
// scorecards).
func writeScorecard(b *strings.Builder, sc advisor.AgentScorecard) {
	fmt.Fprintf(b, "\n## Scorecard (window %s → %s)\n", sc.From, sc.To)
	fmt.Fprintf(b, "runs: %d; failed runs: %d (behavior-fixable: %d); error events: %d\n",
		sc.Runs, sc.FailedRuns, sc.BehaviorFailedRuns, sc.Errors)
	if len(sc.ErrorsByClass) > 0 {
		b.WriteString("errors_by_class:")
		for _, c := range []advisor.ErrClass{
			advisor.BehaviorFixable, advisor.HarnessRecoverable,
			advisor.InfraNoise, advisor.OutcomeFailure,
		} {
			if n := sc.ErrorsByClass[c]; n > 0 {
				fmt.Fprintf(b, " %s=%d", c, n)
			}
		}
		b.WriteString("\n")
	}
	if len(sc.TopGroups) > 0 {
		b.WriteString("top error groups:\n")
		for _, g := range sc.TopGroups {
			fmt.Fprintf(b, "- %q ×%d (%s)\n", g.Key, g.Count, g.Class)
		}
	}
}

// writeAssessments quotes the ledger rows where the tech-lead judged this
// agent poorly: quality ≤ 3 OR non-empty mistakes, newest first, ≤
// assessmentLimit rows. Ledger agent cells carry both "core:x" and "x"
// notations, so the fold happens in Go.
func writeAssessments(b *strings.Builder, db *sql.DB, agent string) error {
	rows, err := db.Query(`
		SELECT COALESCE(t.external_id, CAST(t.id AS TEXT)),
		       COALESCE(d.phase, ''), COALESCE(d.verdict, ''),
		       d.loops, d.quality, COALESCE(d.mistakes, ''), d.agent
		  FROM task_delegations d
		  JOIN tasks t ON t.id = d.task_id
		 WHERE d.quality <= 3 OR (d.mistakes IS NOT NULL AND d.mistakes != '')
		 ORDER BY d.id DESC`)
	if err != nil {
		return err
	}
	defer rows.Close()
	b.WriteString("\n## Ledger assessments (quality ≤ 3 or mistakes noted)\n")
	n := 0
	for rows.Next() {
		var task, phase, verdict, mistakes, rowAgent string
		var loops, quality sql.NullInt64
		if err := rows.Scan(&task, &phase, &verdict, &loops, &quality, &mistakes, &rowAgent); err != nil {
			return err
		}
		if advisor.NormAgent(rowAgent) != agent || n >= assessmentLimit {
			continue
		}
		n++
		fmt.Fprintf(b, "- task %s, phase %q: verdict %q", task, phase, verdict)
		if loops.Valid {
			fmt.Fprintf(b, ", loops %d", loops.Int64)
		}
		if quality.Valid {
			fmt.Fprintf(b, ", quality %d/5", quality.Int64)
		}
		if mistakes != "" {
			fmt.Fprintf(b, ", mistakes: %s", mistakes)
		}
		b.WriteString("\n")
	}
	if n == 0 {
		b.WriteString("(none)\n")
	}
	return rows.Err()
}

// writeImprovements lists the OPEN retro_improvements rows whose text
// mentions the agent (case-insensitive substring).
func writeImprovements(b *strings.Builder, db *sql.DB, agent string) error {
	rows, err := db.Query(`
		SELECT ri.text, COALESCE(ri.priority, ''), COALESCE(ri.status, ''),
		       COALESCE(t.external_id, CAST(t.id AS TEXT))
		  FROM retro_improvements ri
		  JOIN task_retros tr ON tr.id = ri.retro_id
		  JOIN tasks t ON t.id = tr.task_id
		 WHERE ri.text LIKE ? COLLATE NOCASE
		 ORDER BY ri.id DESC`, "%"+agent+"%")
	if err != nil {
		return err
	}
	defer rows.Close()
	b.WriteString("\n## Open improvements mentioning the agent\n")
	n := 0
	for rows.Next() {
		var text, priority, status, task string
		if err := rows.Scan(&text, &priority, &status, &task); err != nil {
			return err
		}
		if doneRe.MatchString(status) {
			continue // closed — not actionable evidence
		}
		n++
		fmt.Fprintf(b, "- %s", text)
		if priority != "" {
			fmt.Fprintf(b, " (priority %s)", priority)
		}
		fmt.Fprintf(b, " [task %s]\n", task)
	}
	if n == 0 {
		b.WriteString("(none)\n")
	}
	return rows.Err()
}

// writeExcerpts pulls a transcript excerpt (±2 events of context, truncated
// to excerptCap) around the newest event of each of the agent's worst
// behavior-fixable error groups (≤ excerptGroups).
func writeExcerpts(b *strings.Builder, db *sql.DB, sc advisor.AgentScorecard, now time.Time) error {
	b.WriteString("\n## Transcript excerpts (worst behavior-fixable errors)\n")
	n := 0
	for _, g := range sc.TopGroups {
		if g.Class != advisor.BehaviorFixable || n >= excerptGroups {
			continue
		}
		events, err := advisor.ErrGroupEvents(db, g.Key, now, 1)
		if err != nil {
			return err
		}
		if len(events) == 0 {
			continue
		}
		excerpt, err := eventContext(db, events[0])
		if err != nil {
			return err
		}
		n++
		fmt.Fprintf(b, "\n### group %q\n%s\n", g.Key, capBytes(excerpt, excerptCap))
	}
	if n == 0 {
		b.WriteString("(none)\n")
	}
	return nil
}

// eventContext renders the error event with ±2 surrounding events of the
// same session, in (ts, id) order, the error line marked.
func eventContext(db *sql.DB, ev advisor.ErrEvent) (string, error) {
	sel := `SELECT id, ts, type, COALESCE(tool_name, ''), COALESCE(status, ''), COALESCE(payload, '')
	          FROM events WHERE session_id = ?`
	before, err := contextRows(db,
		sel+` AND (ts < ? OR (ts = ? AND id < ?)) ORDER BY ts DESC, id DESC LIMIT 2`,
		ev.SessionID, ev.TS, ev.TS, ev.EventID)
	if err != nil {
		return "", err
	}
	// Reverse into chronological order.
	for i, j := 0, len(before)-1; i < j; i, j = i+1, j-1 {
		before[i], before[j] = before[j], before[i]
	}
	self, err := contextRows(db, sel+` AND id = ?`, ev.SessionID, ev.EventID)
	if err != nil {
		return "", err
	}
	for i := range self {
		self[i] = ">>> " + self[i]
	}
	after, err := contextRows(db,
		sel+` AND (ts > ? OR (ts = ? AND id > ?)) ORDER BY ts, id LIMIT 2`,
		ev.SessionID, ev.TS, ev.TS, ev.EventID)
	if err != nil {
		return "", err
	}
	lines := append(append(before, self...), after...)
	return strings.Join(lines, "\n"), nil
}

// contextRows runs one events projection query and formats each row as a
// single excerpt line.
func contextRows(db *sql.DB, query string, args ...any) ([]string, error) {
	rows, err := db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var id int64
		var ts, typ, tool, status, payload string
		if err := rows.Scan(&id, &ts, &typ, &tool, &status, &payload); err != nil {
			return nil, err
		}
		line := fmt.Sprintf("%s %s", ts, typ)
		if tool != "" {
			line += " " + tool
		}
		if status != "" {
			line += " [" + status + "]"
		}
		if payload != "" {
			line += " " + payload
		}
		out = append(out, line)
	}
	return out, rows.Err()
}

// capBundle enforces the total bundle cap with an explicit truncation marker.
func capBundle(s string) string {
	const marker = "\n… [evidence truncated]"
	if len(s) <= bundleCap {
		return s
	}
	return capBytes(s, bundleCap-len(marker)) + marker
}

// capBytes truncates s to ≤ n bytes without splitting a UTF-8 rune.
func capBytes(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8Start(s[n]) {
		n--
	}
	return s[:n]
}

// utf8Start reports whether b is a UTF-8 sequence start (not a continuation
// byte).
func utf8Start(b byte) bool { return b&0xC0 != 0x80 }
