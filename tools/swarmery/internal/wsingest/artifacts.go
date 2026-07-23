// Artifact parsers (retro improvement loop, phase 2): for every indexed task
// card dir — working AND archive — the scanner also parses three optional
// per-task artifacts into structured rows:
//
//	phases/09-retrospective.md → task_retros + retro_lessons + retro_improvements
//	ORCHESTRATION.md           → task_loops (quality-gate re-dispatch journal)
//	logs/agents.md             → task_delegations (one-line-per-delegation ledger)
//
// Same tolerant contract as parseCard: a missing artifact is normal, malformed
// input degrades to a warn + skip, and nothing here ever fails the scan. A
// SHA-256 content-hash gate (task_artifacts) makes rescans cheap: an unchanged
// file is skipped outright; a changed one deletes + reinserts its child rows
// in one transaction.
package wsingest

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// artifactTextCap bounds free-text fields captured from workspace files
// (loop failed/brief_delta) so one runaway journal can't bloat the DB.
const artifactTextCap = 2000

var (
	// `| **Duration** | 6h | 8h | +33% |` — the metrics-table row.
	retroDurationRe = regexp.MustCompile(`(?i)^\|\s*\*{0,2}duration\*{0,2}\s*\|`)
	hoursRe         = regexp.MustCompile(`([0-9.]+)\s*h`)
	pctRe           = regexp.MustCompile(`(-?[0-9.]+)\s*%`)
	// Section heads tolerate the template's emoji prefixes (`## 💡 Lessons
	// Learned`) by matching anywhere after the hashes.
	lessonsHeadRe = regexp.MustCompile(`(?i)^#{2,3}.*lessons learned`)
	improveHeadRe = regexp.MustCompile(`(?i)^#{2,3}.*process improvements`)
	// Lesson entries are h3-only by contract (`### Lesson N: …`, per the retro
	// template): sectionEndRe below terminates the section on any h1/h2 first,
	// so an h2 lesson head could never be reached anyway — the pattern encodes
	// that. (The improvements section has no per-row headings — table rows only
	// — so the same concern does not apply there.)
	lessonRe = regexp.MustCompile(`(?i)^#{3}\s*lesson\s+(\d+)\s*:\s*(.+?)\s*$`)
	actionRe = regexp.MustCompile(`(?i)^\*\*action\*\*\s*:\s*(.*)$`)
	// h1/h2 ends a section; any heading or hr ends a lesson body.
	sectionEndRe = regexp.MustCompile(`^#{1,2}\s`)
	anyHeadRe    = regexp.MustCompile(`^#{1,6}\s`)
	hrRe         = regexp.MustCompile(`^\s*---+\s*$`)
	// `## Loop {N} — corrected instructions` journal sections (tech-lead spec).
	loopHeadRe   = regexp.MustCompile(`(?i)^##\s*loop\s+(\d+)`)
	loopFailedRe = regexp.MustCompile(`(?i)^-\s*failed\s*:\s*(.*)$`)
	loopDeltaRe  = regexp.MustCompile(`(?i)^-\s*brief delta\s*:\s*(.*)$`)
	// Unfilled `{{TEMPLATE}}` cells and `---` table dividers.
	placeholderRe  = regexp.MustCompile(`^\{\{.*\}\}$`)
	tableDividerRe = regexp.MustCompile(`^:?-+:?$`)
)

// scanArtifacts runs the three artifact passes for one task dir. Every stumble
// is a warn — the card scan already succeeded and must not be undone.
func (s *Scanner) scanArtifacts(taskID int64, dir string, warn func(string, ...any)) {
	s.artifactPass(taskID, "retro", filepath.Join(dir, "phases", "09-retrospective.md"), warn, s.applyRetro)
	s.artifactPass(taskID, "orchestration", filepath.Join(dir, "ORCHESTRATION.md"), warn, s.applyLoops)
	s.artifactPass(taskID, "agents_log", filepath.Join(dir, "logs", "agents.md"), warn, s.applyDelegations)
}

// artifactPass is the shared hash gate: read → SHA-256 → compare against
// task_artifacts → on change, apply (delete + reinsert child rows) and upsert
// the gate row in ONE transaction, so a failed parse never leaves half-state.
func (s *Scanner) artifactPass(taskID int64, kind, path string,
	warn func(string, ...any), apply func(tx *sql.Tx, taskID int64, text string) error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return // no artifact — the common case, not a warning
	}
	sum := sha256.Sum256(raw)
	hash := hex.EncodeToString(sum[:])

	var prev, prevPath string
	err = s.db.QueryRow(
		`SELECT content_hash, path FROM task_artifacts WHERE task_id = ? AND kind = ?`,
		taskID, kind).Scan(&prev, &prevPath)
	switch {
	case err == nil && prev == hash:
		// Unchanged content — skip the parse entirely. The stored path can
		// still be stale: agent-work.sh complete moves the task dir working →
		// archive without touching file contents, so refresh it when it moved.
		if prevPath != path {
			if _, uerr := s.db.Exec(
				`UPDATE task_artifacts SET path = ? WHERE task_id = ? AND kind = ?`,
				path, taskID, kind); uerr != nil {
				warn("artifact %s task#%d: path refresh: %v", kind, taskID, uerr)
			}
		}
		return
	case err != nil && err != sql.ErrNoRows:
		warn("artifact %s task#%d: hash lookup: %v", kind, taskID, err)
		return
	}

	tx, err := s.db.Begin()
	if err != nil {
		warn("artifact %s task#%d: begin: %v", kind, taskID, err)
		return
	}
	if err := apply(tx, taskID, string(raw)); err != nil {
		tx.Rollback()
		warn("artifact %s task#%d (%s): %v", kind, taskID, path, err)
		return
	}
	if _, err := tx.Exec(`
		INSERT INTO task_artifacts (task_id, kind, path, content_hash, parsed_at)
		VALUES (?, ?, ?, ?, ?)
		ON CONFLICT(task_id, kind) DO UPDATE SET
			path = excluded.path, content_hash = excluded.content_hash,
			parsed_at = excluded.parsed_at`,
		taskID, kind, path, hash, time.Now().UTC().Format(time.RFC3339)); err != nil {
		tx.Rollback()
		warn("artifact %s task#%d: gate upsert: %v", kind, taskID, err)
		return
	}
	if err := tx.Commit(); err != nil {
		warn("artifact %s task#%d: commit: %v", kind, taskID, err)
	}
}

// ─── 09-retrospective.md ────────────────────────────────────────────────────

type retroLesson struct {
	seq                 int
	title, body, action string
}

type retroImprovement struct {
	text, priority, owner, status string
}

type retroDoc struct {
	est, act, variance *float64
	lessons            []retroLesson
	improvements       []retroImprovement
}

// tableCells splits `| a | b | c |` into trimmed cells.
func tableCells(line string) []string {
	cells := strings.Split(strings.Trim(strings.TrimSpace(line), "|"), "|")
	for i := range cells {
		cells[i] = strings.TrimSpace(cells[i])
	}
	return cells
}

// parseFloatMatch extracts the first regex capture as a float; nil when the
// cell holds a `{{PLACEHOLDER}}`, an em-dash, or anything else non-numeric —
// each cell is independently nullable.
func parseFloatMatch(re *regexp.Regexp, cell string) *float64 {
	m := re.FindStringSubmatch(cell)
	if m == nil {
		return nil
	}
	v, err := strconv.ParseFloat(m[1], 64)
	if err != nil {
		return nil
	}
	return &v
}

// capText trims and rune-caps free text (no mid-rune truncation).
func capText(s string) string {
	s = strings.TrimSpace(s)
	if r := []rune(s); len(r) > artifactTextCap {
		return string(r[:artifactTextCap])
	}
	return s
}

// parseRetroDoc tolerantly extracts the Duration metrics row, the Lessons
// Learned entries, and the Process Improvements table. Anything missing or
// malformed simply yields fewer rows.
func parseRetroDoc(text string) retroDoc {
	var doc retroDoc
	lines := strings.Split(text, "\n")

	// Duration row: est/act/variance cells, each independently nullable.
	for _, line := range lines {
		if !retroDurationRe.MatchString(strings.TrimSpace(line)) {
			continue
		}
		cells := tableCells(line)
		if len(cells) > 1 {
			doc.est = parseFloatMatch(hoursRe, cells[1])
		}
		if len(cells) > 2 {
			doc.act = parseFloatMatch(hoursRe, cells[2])
		}
		if len(cells) > 3 {
			doc.variance = parseFloatMatch(pctRe, cells[3])
		}
		break
	}

	// Lessons: `### Lesson N: <title>` entries under the Lessons Learned head;
	// body runs until the next heading/hr, the `**Action**:` line is captured
	// separately (and excluded from the body).
	for i := 0; i < len(lines); i++ {
		if !lessonsHeadRe.MatchString(lines[i]) {
			continue
		}
		for j := i + 1; j < len(lines) && !sectionEndRe.MatchString(lines[j]); j++ {
			m := lessonRe.FindStringSubmatch(lines[j])
			if m == nil {
				continue
			}
			seq, _ := strconv.Atoi(m[1])
			l := retroLesson{seq: seq, title: capText(m[2])}
			if placeholderRe.MatchString(l.title) {
				continue // unfilled template entry
			}
			var body []string
			for k := j + 1; k < len(lines) && !anyHeadRe.MatchString(lines[k]) && !hrRe.MatchString(lines[k]); k++ {
				if am := actionRe.FindStringSubmatch(strings.TrimSpace(lines[k])); am != nil {
					l.action = capText(am[1])
					continue
				}
				body = append(body, lines[k])
				j = k
			}
			l.body = capText(strings.Join(body, "\n"))
			doc.lessons = append(doc.lessons, l)
		}
		break
	}

	// Improvements: 4-col table rows under the Process Improvements head,
	// skipping the header, the divider, empty-text rows, `{{...}}` placeholder
	// rows, and malformed (< 4 cell) rows.
	for i := 0; i < len(lines); i++ {
		if !improveHeadRe.MatchString(lines[i]) {
			continue
		}
		for j := i + 1; j < len(lines) && !sectionEndRe.MatchString(lines[j]); j++ {
			line := strings.TrimSpace(lines[j])
			if !strings.HasPrefix(line, "|") {
				continue
			}
			cells := tableCells(line)
			if len(cells) < 4 {
				continue // malformed row — tolerated, skipped
			}
			text := capText(cells[0])
			if text == "" || placeholderRe.MatchString(text) ||
				tableDividerRe.MatchString(text) || strings.EqualFold(text, "improvement") {
				continue
			}
			doc.improvements = append(doc.improvements, retroImprovement{
				text: text, priority: capText(cells[1]), owner: capText(cells[2]), status: capText(cells[3]),
			})
		}
		break
	}
	return doc
}

// applyRetro replaces the task's retro rows: the task_retros header is
// upserted (stable id), lessons/improvements are deleted + reinserted.
func (s *Scanner) applyRetro(tx *sql.Tx, taskID int64, text string) error {
	doc := parseRetroDoc(text)
	if _, err := tx.Exec(`
		INSERT INTO task_retros (task_id, estimated_hours, actual_hours, variance_pct, ingested_at)
		VALUES (?, ?, ?, ?, ?)
		ON CONFLICT(task_id) DO UPDATE SET
			estimated_hours = excluded.estimated_hours,
			actual_hours    = excluded.actual_hours,
			variance_pct    = excluded.variance_pct,
			ingested_at     = excluded.ingested_at`,
		taskID, nullFloat(doc.est), nullFloat(doc.act), nullFloat(doc.variance),
		time.Now().UTC().Format(time.RFC3339)); err != nil {
		return err
	}
	var retroID int64
	if err := tx.QueryRow(`SELECT id FROM task_retros WHERE task_id = ?`, taskID).Scan(&retroID); err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM retro_lessons WHERE retro_id = ?`, retroID); err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM retro_improvements WHERE retro_id = ?`, retroID); err != nil {
		return err
	}
	for _, l := range doc.lessons {
		if _, err := tx.Exec(`
			INSERT INTO retro_lessons (retro_id, seq, title, body, action)
			VALUES (?, ?, ?, ?, ?)`,
			retroID, l.seq, l.title, nullStr(l.body), nullStr(l.action)); err != nil {
			return err
		}
	}
	for _, im := range doc.improvements {
		if _, err := tx.Exec(`
			INSERT INTO retro_improvements (retro_id, text, priority, owner, status)
			VALUES (?, ?, ?, ?, ?)`,
			retroID, im.text, nullStr(im.priority), nullStr(im.owner), nullStr(im.status)); err != nil {
			return err
		}
	}
	return nil
}

// ─── ORCHESTRATION.md ───────────────────────────────────────────────────────

type loopEntry struct {
	n             int
	failed, delta string
}

// parseLoops extracts `## Loop {N}` journal sections with their `- Failed:`
// and `- Brief delta:` lines (each optional, trimmed, capped).
func parseLoops(text string) []loopEntry {
	var out []loopEntry
	var cur *loopEntry
	for _, line := range strings.Split(text, "\n") {
		if m := loopHeadRe.FindStringSubmatch(line); m != nil {
			n, _ := strconv.Atoi(m[1])
			out = append(out, loopEntry{n: n})
			cur = &out[len(out)-1]
			continue
		}
		if cur == nil {
			continue
		}
		if anyHeadRe.MatchString(line) {
			cur = nil // any other heading — stop attributing lines
			continue
		}
		trimmed := strings.TrimSpace(line)
		if m := loopFailedRe.FindStringSubmatch(trimmed); m != nil {
			cur.failed = capText(m[1])
		} else if m := loopDeltaRe.FindStringSubmatch(trimmed); m != nil {
			cur.delta = capText(m[1])
		}
	}
	return out
}

func (s *Scanner) applyLoops(tx *sql.Tx, taskID int64, text string) error {
	if _, err := tx.Exec(`DELETE FROM task_loops WHERE task_id = ?`, taskID); err != nil {
		return err
	}
	for _, l := range parseLoops(text) {
		// A duplicated Loop N heading upserts (last wins) instead of tripping
		// the UNIQUE constraint — tolerant-parse contract.
		if _, err := tx.Exec(`
			INSERT INTO task_loops (task_id, loop_n, failed, brief_delta)
			VALUES (?, ?, ?, ?)
			ON CONFLICT(task_id, loop_n) DO UPDATE SET
				failed = excluded.failed, brief_delta = excluded.brief_delta`,
			taskID, l.n, nullStr(l.failed), nullStr(l.delta)); err != nil {
			return err
		}
	}
	return nil
}

// ─── logs/agents.md ─────────────────────────────────────────────────────────

type delegation struct {
	seq                             int
	agent, phase, verdict, artifact string
	loops, quality                  *int // NULL for legacy 4-cell rows / malformed cells
	mistakes                        string
}

// normalizeLedgerAgent folds ledger agent notations to the registry key:
// strip a leading '@', strip any `ns:` prefix ('core:' in practice — the same
// last-colon fold as the API's normAgentType), lowercase.
func normalizeLedgerAgent(a string) string {
	a = strings.TrimPrefix(strings.TrimSpace(a), "@")
	if i := strings.LastIndexByte(a, ':'); i >= 0 {
		a = a[i+1:]
	}
	return strings.ToLower(strings.TrimSpace(a))
}

// ledgerInt parses an assessment cell as an int; nil for anything non-numeric
// (`-`, "high", empty) — each cell is independently nullable.
func ledgerInt(cell string) *int {
	v, err := strconv.Atoi(strings.TrimSpace(cell))
	if err != nil {
		return nil
	}
	return &v
}

// parseLedger extracts delegation rows, tolerating both the Ukrainian
// (`| Агент | Фаза | Вердикт | Артефакт |`) and English headers. Two layouts:
//   - legacy 4-cell: agent | phase | verdict | artifact
//   - assessment 7-cell (tech-lead ≥ core 2.2.0):
//     agent | phase | verdict | loops | quality | mistakes | artifact
//
// Malformed loops/quality cells and out-of-range quality (<1 or >5) degrade to
// nil without dropping the row; mistakes of `-`/`—`/empty fold to "".
func parseLedger(text string) []delegation {
	var out []delegation
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "|") {
			continue
		}
		cells := tableCells(line)
		if len(cells) < 4 {
			continue // malformed row — tolerated, skipped
		}
		first := strings.ToLower(cells[0])
		if first == "агент" || first == "agent" || tableDividerRe.MatchString(first) {
			continue
		}
		agent := normalizeLedgerAgent(cells[0])
		if agent == "" {
			continue
		}
		d := delegation{
			seq: len(out) + 1, agent: agent,
			phase: capText(cells[1]), verdict: capText(cells[2]),
		}
		if len(cells) >= 7 {
			d.loops = ledgerInt(cells[3])
			if q := ledgerInt(cells[4]); q != nil && *q >= 1 && *q <= 5 {
				d.quality = q
			}
			if m := capText(cells[5]); m != "-" && m != "—" {
				d.mistakes = m
			}
			d.artifact = capText(cells[6])
		} else {
			d.artifact = capText(cells[3])
		}
		out = append(out, d)
	}
	return out
}

func (s *Scanner) applyDelegations(tx *sql.Tx, taskID int64, text string) error {
	if _, err := tx.Exec(`DELETE FROM task_delegations WHERE task_id = ?`, taskID); err != nil {
		return err
	}
	for _, d := range parseLedger(text) {
		if _, err := tx.Exec(`
			INSERT INTO task_delegations (task_id, seq, agent, phase, verdict, artifact, loops, quality, mistakes)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			taskID, d.seq, d.agent, nullStr(d.phase), nullStr(d.verdict), nullStr(d.artifact),
			nullInt(d.loops), nullInt(d.quality), nullStr(d.mistakes)); err != nil {
			return err
		}
	}
	return nil
}

func nullFloat(f *float64) any {
	if f == nil {
		return nil
	}
	return *f
}

func nullInt(i *int) any {
	if i == nil {
		return nil
	}
	return *i
}
