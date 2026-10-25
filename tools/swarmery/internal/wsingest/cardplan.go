// Card micro-plan closing: a board card's own single-phase plan (minted by
// internal/taskdir at dispatch, joined to the card through tasks.workspace_dir)
// reads Done only when its checkboxes are ticked AND its Completion Report says
// what happened.
//
// TickPhaseChecklist covers a card minted FROM a plan phase
// (epic_phases.activated_board_task_id); nothing covered the card's OWN
// micro-plan, so a card closed as done — by a no-change sentinel, by the
// operator's close-no-change, or by a drag — left its micro-plan reading 0/2
// forever. The rule here is stricter than TickPhaseChecklist's on purpose: a
// micro-plan is ticked only when its `## Completion Report` is non-blank, because
// criterion 1's no-change arm is satisfied by written evidence and nothing else.
// A drag to done over a blank report therefore ticks nothing.

package wsingest

import (
	"database/sql"
	"errors"
	"os"
	"strings"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/taskdir"
)

// completionReportHeading is the section the dashboard renders as a phase's
// Summary, and the one a card's no-change evidence is written into.
const completionReportHeading = "Completion Report"

// closedWithoutCommitHeading introduces no-change evidence appended under a
// Completion Report that already carries the executor's own words.
const closedWithoutCommitHeading = "### Closed without a commit"

// CardMicroPlanDoc returns the path of the board card's micro-plan phase doc
// (tasks.workspace_dir + "/plan/" + taskdir.PhaseDocName), or "" when the card
// has no workspace_dir or the doc is not on disk. An unknown card id is "" too:
// callers fire on every done transition and most cards have no micro-plan.
func CardMicroPlanDoc(db *sql.DB, boardTaskID int64) (string, error) {
	var dir sql.NullString
	err := db.QueryRow(`SELECT workspace_dir FROM tasks WHERE id = ?`, boardTaskID).Scan(&dir)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	if !dir.Valid || strings.TrimSpace(dir.String) == "" {
		return "", nil
	}
	doc := taskdir.PhaseDocPath(dir.String)
	fi, err := os.Stat(doc)
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil
		}
		return "", err
	}
	if !fi.Mode().IsRegular() {
		return "", nil
	}
	return doc, nil
}

// completionReportBounds locates the doc's LAST unfenced `## Completion Report`
// heading and the end of its section. heading is that line's index (-1 when the
// doc has none); end is the exclusive index of the next level-1/level-2 heading
// outside a fence, or len(lines).
//
// LAST, not first: a micro-plan quotes the card's prompt verbatim in its
// Objective, and a doc can quote the heading inside a fenced agent prompt too.
// The real section is the one the generator writes at the end, so an earlier
// mention — fenced or not — must never be mistaken for it.
func completionReportBounds(text string) (lines []string, heading, end int) {
	lines = strings.Split(text, "\n")
	heading = -1
	forEachLineOutsideFences(text, func(i int, line string) {
		level, title, ok := atxHeading(line)
		if ok && level == 2 && strings.EqualFold(title, completionReportHeading) {
			heading = i
		}
	})
	if heading < 0 {
		return lines, -1, len(lines)
	}
	end = len(lines)
	forEachLineOutsideFences(text, func(i int, line string) {
		if i <= heading || end != len(lines) {
			return
		}
		if level, _, ok := atxHeading(line); ok && level <= 2 {
			end = i
		}
	})
	return lines, heading, end
}

// completionReportBody is the trimmed content of the doc's last unfenced
// `## Completion Report` section, up to the next level-1/level-2 heading. ""
// when the section is absent or blank. Pure; unit-tested.
func completionReportBody(text string) string {
	lines, heading, end := completionReportBounds(text)
	if heading < 0 {
		return ""
	}
	return strings.TrimSpace(strings.Join(lines[heading+1:end], "\n"))
}

// CompletionReportBlank reports whether the doc at docPath has no Completion
// Report content yet. A missing file is an error, so a caller can tell "nothing
// written" from "nothing to write into".
func CompletionReportBlank(docPath string) (bool, error) {
	raw, err := os.ReadFile(docPath)
	if err != nil {
		return false, err
	}
	return completionReportBody(string(raw)) == "", nil
}

// TickCardMicroPlan ticks every unchecked box in the board card's micro-plan
// doc, but ONLY when that doc's Completion Report is non-blank. Returns how many
// boxes it ticked. No micro-plan, or a blank report, is a (0, nil) no-op — a
// card moved to done with nothing written about why must not read as a verified
// plan.
func TickCardMicroPlan(db *sql.DB, boardTaskID int64) (int, error) {
	doc, err := CardMicroPlanDoc(db, boardTaskID)
	if err != nil || doc == "" {
		return 0, err
	}
	raw, err := os.ReadFile(doc)
	if err != nil {
		return 0, err
	}
	text := string(raw)
	if completionReportBody(text) == "" {
		return 0, nil
	}
	out, n := tickAllCheckboxes(text)
	if n == 0 {
		return 0, nil
	}
	if err := os.WriteFile(doc, []byte(out), 0o644); err != nil {
		return 0, err
	}
	return n, nil
}

// AppendCompletionReport writes body into the doc's Completion Report.
//
// A blank report gets body as its content. A report that already has content is
// never overwritten: body is appended under a "### Closed without a commit"
// sub-heading. Idempotent — a body the report already contains is not appended a
// second time. A doc with no Completion Report section gains one at its end. A
// blank body is a no-op.
func AppendCompletionReport(docPath, body string) error {
	body = strings.TrimSpace(body)
	if body == "" {
		return nil
	}
	raw, err := os.ReadFile(docPath)
	if err != nil {
		return err
	}
	out, changed := appendCompletionReport(string(raw), body)
	if !changed {
		return nil
	}
	return os.WriteFile(docPath, []byte(out), 0o644)
}

// appendCompletionReport is AppendCompletionReport's pure half: the new text and
// whether it differs. body is already trimmed and non-blank.
func appendCompletionReport(text, body string) (string, bool) {
	lines, heading, end := completionReportBounds(text)
	if heading < 0 {
		out := strings.TrimRight(text, "\n") + "\n\n## " + completionReportHeading + "\n\n" + body + "\n"
		return out, true
	}
	existing := strings.TrimSpace(strings.Join(lines[heading+1:end], "\n"))
	if existing != "" && strings.Contains(existing, body) {
		return text, false
	}

	var b strings.Builder
	b.WriteString(strings.Join(lines[:heading+1], "\n"))
	b.WriteString("\n\n")
	if existing == "" {
		b.WriteString(body)
	} else {
		b.WriteString(existing)
		b.WriteString("\n\n" + closedWithoutCommitHeading + "\n\n")
		b.WriteString(body)
	}
	b.WriteString("\n")
	if end < len(lines) {
		b.WriteString("\n")
		b.WriteString(strings.Join(lines[end:], "\n"))
	}
	return b.String(), true
}
