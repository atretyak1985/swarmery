package wsingest

// Decision ids (verify-not-trust phase 2). A plan README carries a `## Decisions`
// section with one line per interview decision:
//
//	- **D-3** (expensive, undecided) — Which store backs the cache? → undecided (reason: a migration to undo)
//
// and a phase doc that cannot start before a decision is taken declares it in
// its header block:
//
//	**Depends on decisions:** D-1, D-3
//
// This file is the ONE definition of both formats: FormatDecisionLine is what
// the planning service hands the planner at PROCEED (so the README carries
// exactly what the daemon recorded), ParseDecisions / ParseDecisionDeps are what
// the scan reads back. A plan without the section yields nothing and no warning —
// every plan written before this existed.

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// DecisionLine is one parsed `## Decisions` entry.
type DecisionLine struct {
	Seq      int    // 3 for D-3
	Cost     string // cheap | expensive
	Status   string // decided | undecided | recommended
	Question string
	Answer   string // the chosen answer, or "undecided"
	Reason   string // the cost reason; "" when the line carries none
}

// ID is the `D-n` id of the line.
func (d DecisionLine) ID() string { return "D-" + strconv.Itoa(d.Seq) }

// UndecidedAnswer is the answer text of a skipped expensive decision.
const UndecidedAnswer = "undecided"

// RecommendedSuffix marks a skipped cheap decision taken at the planner's
// recommended option (the plan's operator-facing language is Ukrainian; the
// suffix is part of the format, not a translation).
const RecommendedSuffix = "(без відповіді, рекомендований)"

// FormatDecisionLine renders one `## Decisions` entry in the exact shape
// ParseDecisions reads back.
func FormatDecisionLine(d DecisionLine) string {
	q := oneLine(d.Question)
	a := oneLine(d.Answer)
	if a == "" {
		a = UndecidedAnswer
	}
	line := fmt.Sprintf("- **%s** (%s, %s) — %s → %s", d.ID(), d.Cost, d.Status, q, a)
	if r := oneLine(d.Reason); r != "" {
		line += " (reason: " + r + ")"
	}
	return line
}

// oneLine collapses whitespace runs (newlines included) so a multi-line
// question cannot break the one-entry-per-line format.
func oneLine(s string) string { return strings.Join(strings.Fields(s), " ") }

var (
	decisionsHeadingRe = regexp.MustCompile(`(?i)^##\s+Decisions\s*$`)
	// `- **D-3** (expensive, undecided) — question → answer (reason: …)`. The dash
	// between the tuple and the question may be an em/en dash or a hyphen, and the
	// arrow `→` or `->`, because hand-edited READMEs drift.
	decisionLineRe = regexp.MustCompile(
		`^\s*[-*]\s+\*\*D-(\d+)\*\*\s*\(\s*(cheap|expensive)\s*,\s*(decided|undecided|recommended)\s*\)\s*(?:—|–|-{1,2})\s*(.*)$`)
	decisionReasonRe = regexp.MustCompile(`\s*\(reason:\s*(.*)\)\s*$`)
	// `**Depends on decisions:** D-1, D-3`, alone or inline in a `·`-joined header
	// row; `| **Depends on decisions** | D-1 |` in a header table.
	decisionDepsRe   = regexp.MustCompile(`(?i)\*\*Depends on decisions:?\*\*\s*:?\s*\|?\s*([^·|\n]*)`)
	decisionIDTokRe  = regexp.MustCompile(`(?i)\bD-(\d+)\b`)
	decisionArrowSep = []string{"→", "->"}
)

// ParseDecisions reads the README's `## Decisions` section (heading to the next
// `## ` heading, fenced blocks skipped). Lines that do not match the format are
// ignored; a repeated D-n keeps its first line. nil when the section is absent.
// Pure; unit-tested.
func ParseDecisions(readme string) []DecisionLine {
	var out []DecisionLine
	in := false
	seen := map[int]bool{}
	forEachLineOutsideFences(readme, func(_ int, line string) {
		t := strings.TrimSpace(line)
		if strings.HasPrefix(t, "## ") {
			in = decisionsHeadingRe.MatchString(t)
			return
		}
		if !in {
			return
		}
		m := decisionLineRe.FindStringSubmatch(line)
		if m == nil {
			return
		}
		seq, err := strconv.Atoi(m[1])
		if err != nil || seq <= 0 || seen[seq] {
			return
		}
		seen[seq] = true
		d := DecisionLine{Seq: seq, Cost: strings.ToLower(m[2]), Status: strings.ToLower(m[3])}
		rest := m[4]
		if rm := decisionReasonRe.FindStringSubmatchIndex(rest); rm != nil {
			d.Reason = strings.TrimSpace(rest[rm[2]:rm[3]])
			rest = rest[:rm[0]]
		}
		d.Question, d.Answer = splitArrow(rest)
		out = append(out, d)
	})
	return out
}

func splitArrow(s string) (question, answer string) {
	for _, sep := range decisionArrowSep {
		if i := strings.Index(s, sep); i >= 0 {
			return strings.TrimSpace(s[:i]), strings.TrimSpace(s[i+len(sep):])
		}
	}
	return strings.TrimSpace(s), ""
}

// ParseDecisionDeps returns the `D-n` ids a phase doc's header block declares on
// its `**Depends on decisions:**` line — bounded to the header like ParseCovers
// (an embedded agent prompt further down may quote someone else's line), first
// declaration wins, de-duplicated, in written order. nil when absent.
func ParseDecisionDeps(md string) []string {
	lines := strings.Split(md, "\n")
	if len(lines) > docStatusHeaderLines {
		lines = lines[:docStatusHeaderLines]
	}
	for _, line := range lines {
		if strings.HasPrefix(line, "## ") {
			break
		}
		m := decisionDepsRe.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		var out []string
		seen := map[string]bool{}
		for _, tok := range decisionIDTokRe.FindAllStringSubmatch(m[1], -1) {
			id := "D-" + strings.TrimLeft(tok[1], "0")
			if id == "D-" || seen[id] {
				continue
			}
			seen[id] = true
			out = append(out, id)
		}
		return out
	}
	return nil
}

// parseDecisionsFile reads <planDir>/README.md's decisions; nil when absent.
func parseDecisionsFile(planDir string) []DecisionLine {
	b, err := os.ReadFile(filepath.Join(planDir, "README.md"))
	if err != nil {
		return nil
	}
	return ParseDecisions(string(b))
}

// applyDecisions folds a plan's decisions into phase_decisions:
//
//  1. ADOPT the rows PROCEED recorded for the planning session that wrote this
//     plan (keyed by planning_session_id while no task existed) onto the task —
//     unless the task already has a row for that D-n.
//  2. UPSERT every README line on (workspace_task_id, decision_id). The README is
//     the source of truth for what it states (question, cost, status, answer,
//     reason); daemon-owned fields (source, question_id, option ids, the session
//     link, created_at) survive, so a rescan never duplicates and never forgets.
//
// A README without the section adopts but writes nothing else and prunes nothing.
func applyDecisions(tx *sql.Tx, taskID int64, planDir string, lines []DecisionLine) error {
	taskDir := filepath.Dir(planDir)
	var sessionID int64
	err := tx.QueryRow(`
		SELECT id FROM planning_sessions
		 WHERE plan_dir IN (?, ?, ?, ?) ORDER BY id DESC LIMIT 1`,
		planDir, planDir+"/", taskDir, taskDir+"/").Scan(&sessionID)
	switch {
	case err == nil:
		if _, err := tx.Exec(`
			UPDATE phase_decisions SET workspace_task_id = ?
			 WHERE planning_session_id = ? AND workspace_task_id IS NULL
			   AND decision_id NOT IN (SELECT decision_id FROM phase_decisions WHERE workspace_task_id = ?)`,
			taskID, sessionID, taskID); err != nil {
			return err
		}
	case err != sql.ErrNoRows:
		return err
	}

	now := time.Now().UTC().Format(time.RFC3339)
	for _, d := range lines {
		var decidedAt any
		if d.Status == "decided" {
			decidedAt = now
		}
		if _, err := tx.Exec(`
			INSERT INTO phase_decisions
				(workspace_task_id, decision_id, seq, source, question, cost, cost_reason,
				 status, answer, created_at, decided_at)
			VALUES (?, ?, ?, 'interview', ?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT(workspace_task_id, decision_id) DO UPDATE SET
				seq = excluded.seq,
				question = excluded.question,
				cost = excluded.cost,
				cost_reason = excluded.cost_reason,
				status = excluded.status,
				answer = excluded.answer,
				decided_at = CASE WHEN excluded.status = 'decided'
				                  THEN COALESCE(phase_decisions.decided_at, excluded.decided_at)
				                  ELSE NULL END`,
			taskID, d.ID(), d.Seq, d.Question, d.Cost, d.Reason, d.Status, d.Answer, now, decidedAt); err != nil {
			return err
		}
	}
	return nil
}

// applyDecisionDeps stores a phase's declared decision dependencies. A separate
// statement rather than a PhaseUpsertSQL column: that statement's parameter list
// is pinned by the store's migration tests, and decision_deps is doc-owned the
// same way — re-derived on every scan, back to [] when the line is deleted.
func applyDecisionDeps(tx *sql.Tx, taskID int64, p epicPhase) error {
	deps := p.decisionDeps
	if deps == nil {
		deps = []string{}
	}
	b, err := json.Marshal(deps)
	if err != nil {
		return err
	}
	_, err = tx.Exec(`UPDATE epic_phases SET decision_deps = ? WHERE workspace_task_id = ? AND doc_path = ?`,
		string(b), taskID, p.docPath)
	return err
}

// FormatStopDecisionLine renders a phase doc's `## Decisions` entry for an
// answered stop decision (verify-not-trust phase 4: a guard decision):
//
//   - **D-5** (stop) — The run touched protected paths: a_test.go. … → reject
//
// Phase docs are not read back by ParseDecisions (the README is the plan's
// decision record); the line is the human-readable trail the next run reads.
func FormatStopDecisionLine(seq int, question, answer string) string {
	return FormatPhaseDecisionLine(seq, "stop", question, answer)
}

// FormatPhaseDecisionLine is FormatStopDecisionLine for any per-phase decision
// source — `- **D-n** (<source>) — <question> → <answer>`; a review decision
// (verify-not-trust phase 9) writes `(review)`. Every part is flattened onto
// one line, the source included: an agent-declared stop's source carries the
// agent's trigger (`stop: <trigger>`), and the agent is untrusted — a newline in
// it must not write a checkbox or a fence into the doc.
func FormatPhaseDecisionLine(seq int, source, question, answer string) string {
	a := oneLine(answer)
	if a == "" {
		a = UndecidedAnswer
	}
	return fmt.Sprintf("- **D-%d** (%s) — %s → %s", seq, oneLine(source), oneLine(question), a)
}

// AppendPhaseDecision returns doc with line appended to its `## Decisions`
// section — after the section's last non-blank line — or, when the doc has no
// such section, with a new one inserted right before `## Completion Report` (at
// the end when that is missing too). Headings inside fenced blocks are text, not
// sections.
func AppendPhaseDecision(doc, line string) string {
	lines := strings.Split(doc, "\n")
	start, end := -1, len(lines)
	forEachLineOutsideFences(doc, func(i int, l string) {
		t := strings.TrimSpace(l)
		switch {
		case start < 0 && decisionsHeadingRe.MatchString(t):
			start = i
		case start >= 0 && end == len(lines) && i > start && strings.HasPrefix(t, "## "):
			end = i
		}
	})
	if start < 0 {
		return insertBeforeCompletionReport(doc, "## Decisions\n\n"+line+"\n")
	}
	last := start
	for i := start + 1; i < end; i++ {
		if strings.TrimSpace(lines[i]) != "" {
			last = i
		}
	}
	out := make([]string, 0, len(lines)+2)
	out = append(out, lines[:last+1]...)
	if last == start {
		out = append(out, "")
	}
	out = append(out, line)
	out = append(out, lines[last+1:]...)
	return strings.Join(out, "\n")
}

// WriteDocAtomic replaces the doc at path with text, keeping its permissions.
func WriteDocAtomic(path, text string) error {
	perm := os.FileMode(0o644)
	if st, err := os.Stat(path); err == nil {
		perm = st.Mode().Perm()
	}
	return writeFileAtomic(path, []byte(text), perm)
}
