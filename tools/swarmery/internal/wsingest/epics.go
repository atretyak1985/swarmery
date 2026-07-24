// Epic parser (fusion phase 10): a workspace plan dir (…/{slug}/plan/ with a
// README.md + phase-*.md / step-*.md docs) IS an epic; the README
// phase-sequencing table rows ARE the phases, and each phase doc's
// acceptance-criteria checkboxes drive progress. For every indexed task dir
// that contains a plan/ subdir, scanEpics parses README.md + the phase docs and
// upserts epic_phases behind the same content-hash gate as the other artifacts
// (task_artifacts kind 'plan', keyed on the README's hash — a phase-doc edit
// that flips a checkbox changes the doc but not the README, so the gate keys on
// a combined hash of every plan file, see scanEpics).
//
// Tolerant by contract, exactly like parseCard/parseRetroDoc: a plan/ without a
// README, a README without a table, a doc with zero checkboxes, or a table row
// pointing at a missing file each degrade to sensible defaults and never fail
// the scan.

package wsingest

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

var (
	// A `- [ ]` / `- [x]` (or `* [X]`) acceptance-criteria checkbox line.
	checkboxRe = regexp.MustCompile(`(?i)^\s*[-*]\s+\[( |x)\]\s`)
	// Phase/step doc filenames: phase-<n>-<slug>.md or step-<nn>-<name>.md.
	phaseDocRe = regexp.MustCompile(`(?i)^(?:phase|step)-.*\.md$`)
	// The Doc column cell wraps the filename in backticks: `phase-1-x.md`.
	backtickDocRe = regexp.MustCompile("`([^`]+\\.md)`")
	// Leading integers in the "Depends on" cell: "1, 2", "1 (API), 3 (live)".
	leadingIntRe = regexp.MustCompile(`\b(\d+)\b`)
	// First markdown H1 (`# Title`) — the phase's display name fallback.
	h1Re = regexp.MustCompile(`(?m)^#\s+(.+?)\s*$`)
)

// epicPhase is one parsed phase (a README table row joined to its doc file).
type epicPhase struct {
	seq             int
	name            string
	docPath         string // absolute path to the phase/step doc ("" when unresolved)
	dependsOn       []int  // seq numbers this phase depends on
	checkboxesDone  int
	checkboxesTotal int
}

// countCheckboxes counts acceptance-criteria checkboxes in a doc, returning
// (done, total). Pure; unit-tested. A doc with none yields (0, 0).
func countCheckboxes(text string) (done, total int) {
	for _, line := range strings.Split(text, "\n") {
		m := checkboxRe.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		total++
		if strings.EqualFold(m[1], "x") {
			done++
		}
	}
	return done, total
}

// phaseTableCols locates the phase-sequencing table's header row and returns the
// 0-based column indices for the seq (#), name (Phase), doc (Doc), and
// depends-on (Depends on) columns. ok=false when no such header is present.
// Pure; unit-tested.
func phaseTableCols(cells []string) (seqCol, nameCol, docCol, depCol int, ok bool) {
	seqCol, nameCol, docCol, depCol = -1, -1, -1, -1
	for i, c := range cells {
		switch h := strings.ToLower(strings.TrimSpace(c)); {
		case h == "#" || h == "seq":
			seqCol = i
		case h == "phase" || h == "name":
			nameCol = i
		case h == "doc" || h == "file":
			docCol = i
		case strings.HasPrefix(h, "depends"):
			depCol = i
		}
	}
	// The doc column is the one indispensable anchor (it names the phase file);
	// a name column is required to label the phase. seq/depends degrade to
	// positional/empty when absent.
	ok = docCol >= 0 && nameCol >= 0
	return seqCol, nameCol, docCol, depCol, ok
}

// parseLeadingInts extracts every integer token from a "Depends on" cell,
// dropping an em-dash / "none". Pure; unit-tested.
func parseLeadingInts(cell string) []int {
	cell = strings.TrimSpace(cell)
	if cell == "" || cell == "—" || cell == "-" || strings.EqualFold(cell, "none") {
		return nil
	}
	var out []int
	for _, m := range leadingIntRe.FindAllStringSubmatch(cell, -1) {
		if n, err := strconv.Atoi(m[1]); err == nil {
			out = append(out, n)
		}
	}
	return out
}

// parsePlanTable extracts phase rows from the README phase-sequencing table.
// Returns nil when there is no recognizable table (the caller then falls back to
// one-phase-per-doc). Rows without a doc cell are skipped; the seq defaults to
// the row's 1-based position when the # column is missing/non-numeric. Pure;
// unit-tested.
func parsePlanTable(readme string) []epicPhase {
	lines := strings.Split(readme, "\n")
	var (
		cols    struct{ seq, name, doc, dep int }
		haveHdr bool
		out     []epicPhase
	)
	for _, line := range lines {
		t := strings.TrimSpace(line)
		if !strings.HasPrefix(t, "|") {
			haveHdr = false // a non-table line ends the current table block
			continue
		}
		cells := tableCells(t)
		if !haveHdr {
			if s, n, d, dp, ok := phaseTableCols(cells); ok {
				cols.seq, cols.name, cols.doc, cols.dep = s, n, d, dp
				haveHdr = true
			}
			continue
		}
		// A divider row (|---|---|) between header and body — skip it.
		if isTableDivider(cells) {
			continue
		}
		doc := ""
		if cols.doc < len(cells) {
			doc = docFromCell(cells[cols.doc])
		}
		if doc == "" {
			continue // a row that names no phase doc is not a phase
		}
		name := ""
		if cols.name < len(cells) {
			name = capText(cells[cols.name])
		}
		seq := len(out) + 1
		if cols.seq >= 0 && cols.seq < len(cells) {
			if n, err := strconv.Atoi(strings.TrimSpace(cells[cols.seq])); err == nil {
				seq = n
			}
		}
		var dep []int
		if cols.dep >= 0 && cols.dep < len(cells) {
			dep = parseLeadingInts(cells[cols.dep])
		}
		if name == "" {
			name = strings.TrimSuffix(doc, ".md")
		}
		out = append(out, epicPhase{seq: seq, name: name, docPath: doc, dependsOn: dep})
	}
	return out
}

// isTableDivider reports whether every cell is a markdown alignment divider
// (`---`, `:--`, …). Pure.
func isTableDivider(cells []string) bool {
	for _, c := range cells {
		if !tableDividerRe.MatchString(strings.TrimSpace(c)) {
			return false
		}
	}
	return len(cells) > 0
}

// docFromCell pulls the `.md` filename out of a Doc cell, preferring the
// backtick-wrapped form. Falls back to the first bare token ending in .md.
// Returns "" when the cell names no doc. Pure.
func docFromCell(cell string) string {
	if m := backtickDocRe.FindStringSubmatch(cell); m != nil {
		return filepath.Base(strings.TrimSpace(m[1]))
	}
	for _, tok := range strings.Fields(cell) {
		tok = strings.Trim(tok, "`*")
		if strings.HasSuffix(strings.ToLower(tok), ".md") {
			return filepath.Base(tok)
		}
	}
	return ""
}

// listPhaseDocs returns the plan dir's phase-*.md / step-*.md files (basenames)
// sorted, excluding README.md — the fallback source when there is no table.
func listPhaseDocs(planDir string) []string {
	entries, err := os.ReadDir(planDir)
	if err != nil {
		return nil
	}
	var docs []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		if phaseDocRe.MatchString(e.Name()) {
			docs = append(docs, e.Name())
		}
	}
	sort.Strings(docs)
	return docs
}

// parsePlan reads a plan dir into ordered phases: README table rows joined to
// their doc files (checkbox counts folded in), or — when there is no table —
// one phase per phase-*.md/step-*.md file, seq by filename sort. Every docPath
// is resolved to an absolute path under planDir; a row pointing at a missing
// file keeps its table metadata with zero checkboxes. Pure w.r.t. the DB
// (touches only the filesystem); the workhorse behind applyEpics and the
// table-driven tests.
func parsePlan(planDir string) []epicPhase {
	readme, _ := os.ReadFile(filepath.Join(planDir, "README.md")) // "" when absent
	phases := parsePlanTable(string(readme))

	if len(phases) == 0 {
		// Fallback: one phase per doc file, seq by sort order.
		for i, name := range listPhaseDocs(planDir) {
			phases = append(phases, epicPhase{seq: i + 1, name: strings.TrimSuffix(name, ".md"), docPath: name})
		}
	}

	for i := range phases {
		doc := phases[i].docPath
		if doc == "" {
			continue
		}
		abs := filepath.Join(planDir, doc)
		phases[i].docPath = abs
		body, err := os.ReadFile(abs)
		if err != nil {
			phases[i].docPath = "" // unresolved — keep table metadata, no counts
			continue
		}
		// Prefer the doc's own H1 as the display name over a terse table label.
		if m := h1Re.FindSubmatch(body); m != nil {
			if title := capText(string(m[1])); title != "" {
				phases[i].name = title
			}
		}
		phases[i].checkboxesDone, phases[i].checkboxesTotal = countCheckboxes(string(body))
	}
	return phases
}

// planHash combines every plan file's bytes into one content hash, so the gate
// re-parses when the README OR any phase doc changes (a checkbox flip lives in a
// phase doc, not the README). Returns ("", false) when the dir is unreadable.
func planHash(planDir string) (string, bool) {
	entries, err := os.ReadDir(planDir)
	if err != nil {
		return "", false
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(strings.ToLower(e.Name()), ".md") {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)
	h := sha256.New()
	for _, n := range names {
		b, err := os.ReadFile(filepath.Join(planDir, n))
		if err != nil {
			continue
		}
		h.Write([]byte(n))
		h.Write([]byte{0})
		h.Write(b)
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil)), true
}

// scanEpics parses one task's plan/ dir (when present) into epic_phases, behind
// the task_artifacts 'plan' content-hash gate. Mirrors artifactPass but hashes
// the whole plan dir (not a single file) so a phase-doc checkbox flip is picked
// up. Every stumble is a warn — the card scan already succeeded.
func (s *Scanner) scanEpics(taskID int64, dir string, warn func(string, ...any)) {
	planDir := filepath.Join(dir, "plan")
	fi, err := os.Stat(planDir)
	if err != nil || !fi.IsDir() {
		return // no plan/ — the common case, not a warning
	}
	hash, ok := planHash(planDir)
	if !ok {
		return
	}

	var prev string
	err = s.db.QueryRow(
		`SELECT content_hash FROM task_artifacts WHERE task_id = ? AND kind = 'plan'`,
		taskID).Scan(&prev)
	switch {
	case err == nil && prev == hash:
		return // unchanged — skip the parse entirely
	case err != nil && err != sql.ErrNoRows:
		warn("epics task#%d: hash lookup: %v", taskID, err)
		return
	}

	phases := parsePlan(planDir)

	tx, err := s.db.Begin()
	if err != nil {
		warn("epics task#%d: begin: %v", taskID, err)
		return
	}
	if err := applyEpics(tx, taskID, phases); err != nil {
		tx.Rollback()
		warn("epics task#%d (%s): %v", taskID, planDir, err)
		return
	}
	if _, err := tx.Exec(`
		INSERT INTO task_artifacts (task_id, kind, path, content_hash, parsed_at)
		VALUES (?, 'plan', ?, ?, ?)
		ON CONFLICT(task_id, kind) DO UPDATE SET
			path = excluded.path, content_hash = excluded.content_hash,
			parsed_at = excluded.parsed_at`,
		taskID, planDir, hash, time.Now().UTC().Format(time.RFC3339)); err != nil {
		tx.Rollback()
		warn("epics task#%d: gate upsert: %v", taskID, err)
		return
	}
	if err := tx.Commit(); err != nil {
		warn("epics task#%d: commit: %v", taskID, err)
	}
}

// applyEpics replaces the task's epic_phases rows. Activation state
// (activated_at + activated_board_task_id) is preserved across a rescan by
// re-reading it per doc_path BEFORE the delete, then restoring it on reinsert —
// a checkbox flip must not un-activate a phase whose board task already exists.
func applyEpics(tx *sql.Tx, taskID int64, phases []epicPhase) error {
	// Snapshot prior activation state keyed by doc_path.
	type act struct {
		at   sql.NullString
		task sql.NullInt64
	}
	prior := map[string]act{}
	rows, err := tx.Query(
		`SELECT doc_path, activated_at, activated_board_task_id FROM epic_phases WHERE workspace_task_id = ?`,
		taskID)
	if err != nil {
		return err
	}
	for rows.Next() {
		var dp string
		var a act
		if err := rows.Scan(&dp, &a.at, &a.task); err != nil {
			rows.Close()
			return err
		}
		prior[dp] = a
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}

	if _, err := tx.Exec(`DELETE FROM epic_phases WHERE workspace_task_id = ?`, taskID); err != nil {
		return err
	}
	for _, p := range phases {
		depJSON, err := json.Marshal(p.dependsOn)
		if err != nil {
			return err
		}
		if p.dependsOn == nil {
			depJSON = []byte("[]")
		}
		var activatedAt any
		var activatedTask any
		if a, ok := prior[p.docPath]; ok {
			if a.at.Valid {
				activatedAt = a.at.String
			}
			if a.task.Valid {
				activatedTask = a.task.Int64
			}
		}
		if _, err := tx.Exec(`
			INSERT INTO epic_phases
				(workspace_task_id, seq, name, doc_path, depends_on,
				 checkboxes_total, checkboxes_done, activated_at, activated_board_task_id)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			taskID, p.seq, p.name, p.docPath, string(depJSON),
			p.checkboxesTotal, p.checkboxesDone, activatedAt, activatedTask); err != nil {
			return err
		}
	}
	return nil
}
