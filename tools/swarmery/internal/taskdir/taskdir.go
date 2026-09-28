// Package taskdir writes workspace task dirs — the first Go code in this daemon
// that does.
//
// Everything else treats the workspace as read-only: internal/wsingest scans it
// (its single write is a checkbox tick), internal/taskcap only stores what it
// finds, and the api layer edits docs that already exist. The reason to break that
// is the honesty contract: a PLAN's outcome is evidence — checkboxes ticked in a
// doc and a `## Completion Report` the executor wrote — while a BOARD card's
// outcome was whatever column someone dragged it to. Two units of work, two
// standards of proof, and the weaker one covers the work the daemon does most of.
//
// A dispatched card therefore materializes a single-phase micro-plan: the same
// tree wsingest already parses, the same checkboxes the Plans page already
// renders, the same Completion Report a phase doc carries. The card and the
// micro-plan are two views of ONE unit of work, joined by tasks.workspace_dir.
//
// What this package deliberately does NOT do is write DB rows. wsingest stays the
// only writer of workspace task rows — this writes files and lets the scanner
// discover them, so there is exactly one path from a task dir to a row and no way
// for the two to disagree.
package taskdir

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// Card is what a dispatched board card contributes to its micro-plan. Only these
// four fields, on purpose: the doc is generated deterministically, so anything
// that would need a model to produce (a decomposition of the prompt into real
// phases) is out of scope by construction.
type Card struct {
	ExternalID string // the card's external id ("T-42") — the join key and the branch name
	Title      string
	Prompt     string // the card's body, copied verbatim into the doc's Objective
	RepoPath   string // the project repo the run executes in (informational, in the header)
}

// slugPrefix marks a task dir as minted from a board card. It is in the dir NAME
// (and therefore in the workspace task's derived external_id) rather than only in
// the DB, so an operator reading the workspace on disk can tell a hand-written
// plan from a materialized card without a database.
const slugPrefix = "card-"

// unsafeSlugChars is everything that must not reach a directory name. External ids
// are already tame (T-42, verify-fix-3), but the dir name is also the workspace
// task's external_id and a path segment, so it is sanitized rather than trusted.
var unsafeSlugChars = regexp.MustCompile(`[^a-z0-9._-]+`)

// Slug is the leaf dir name for a card's micro-plan: "card-<external id>",
// lowercased and path-safe. Deterministic, so a re-dispatch of the same card
// resolves to the same directory instead of littering one per run.
func Slug(externalID string) string {
	s := unsafeSlugChars.ReplaceAllString(strings.ToLower(strings.TrimSpace(externalID)), "-")
	s = strings.Trim(s, "-.")
	if s == "" {
		s = "unnamed"
	}
	return slugPrefix + s
}

// Dir is where a card's micro-plan lives:
// <wsRoot>/<project>/workspace/working/YYYY/MM/DD/card-<external id>.
//
// The date is the DISPATCH day, and it is part of the path because wsingest
// derives a workspace task's external_id from it (YYYY-MM-DD-<slug>) — which is
// also why Dir must be given the same `now` that minted the tree when a later
// caller wants to find it again. Callers that cannot know it read
// tasks.workspace_dir instead, which is why that column exists.
func Dir(wsRoot, project string, externalID string, now time.Time) string {
	return DirIn(filepath.Join(wsRoot, project), externalID, now)
}

// DirIn is Dir addressed by the project's workspace namespace dir itself
// (workspaces.root_path) instead of <wsRoot> + <project>.
//
// The two spellings are NOT interchangeable in general. <wsRoot>/<project>
// reconstructs the namespace from a slug, and the daemon holds two slugs that
// upstream documents as never matching (internal/onboard/onboard.go: the
// registry slug is path-derived, the onboarding slug is the operator's kebab
// name). Onboarding carves the namespace under the ONBOARDING slug, so a caller
// holding only the registry slug rebuilds a path that does not exist and mints
// a second tree beside the real one. A caller that already knows the namespace
// dir must therefore pass it through unchanged rather than re-deriving it.
func DirIn(wsDir string, externalID string, now time.Time) string {
	d := now.UTC()
	return filepath.Join(wsDir, "workspace", "working",
		fmt.Sprintf("%04d", d.Year()), fmt.Sprintf("%02d", int(d.Month())), fmt.Sprintf("%02d", d.Day()),
		Slug(externalID))
}

// MintMicroPlan creates the micro-plan tree for a dispatched card and returns its
// task dir.
//
// Idempotent by directory existence: a dir that is already there is returned
// as-is and NEVER rewritten. That is not just about re-dispatch — verify's fix
// task carries the root card's external id on purpose, so two cards legitimately
// share one micro-plan, and by the time the second one runs the doc may already
// carry ticks and a Completion Report. Regenerating would erase the record of the
// work while claiming to set it up.
func MintMicroPlan(wsRoot, project string, card Card, now time.Time) (string, error) {
	if wsRoot == "" || project == "" {
		return "", fmt.Errorf("taskdir: workspace root and project are required")
	}
	return MintMicroPlanIn(filepath.Join(wsRoot, project), card, now)
}

// MintMicroPlanIn is MintMicroPlan addressed by the workspace namespace dir
// itself — see DirIn for why a caller that already holds that dir must not
// rebuild it from a slug.
func MintMicroPlanIn(wsDir string, card Card, now time.Time) (string, error) {
	if wsDir == "" {
		return "", fmt.Errorf("taskdir: workspace dir is required")
	}
	if strings.TrimSpace(card.ExternalID) == "" {
		return "", fmt.Errorf("taskdir: card has no external id")
	}

	dir := DirIn(wsDir, card.ExternalID, now)
	if fi, err := os.Stat(dir); err == nil {
		if !fi.IsDir() {
			return "", fmt.Errorf("taskdir: %s exists and is not a directory", dir)
		}
		return dir, nil // already minted — leave every tick and report alone
	} else if !os.IsNotExist(err) {
		return "", err
	}

	planDir := filepath.Join(dir, "plan")
	if err := os.MkdirAll(planDir, 0o755); err != nil {
		return "", err
	}
	files := map[string]string{
		filepath.Join(dir, "README.md"):      cardReadme(card, now),
		filepath.Join(planDir, "README.md"):  planReadme(card),
		filepath.Join(planDir, PhaseDocName): phaseDoc(card),
	}
	for path, body := range files {
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			return "", err
		}
	}
	// Nudge the date parent's mtime so a watcher that has it registered notices the
	// new child promptly. Best-effort: the 60s rescan is what GUARANTEES discovery,
	// and a failure here only costs latency.
	stamp := now.UTC()
	_ = os.Chtimes(filepath.Dir(dir), stamp, stamp)
	return dir, nil
}

// PhaseDocName is the micro-plan's single phase doc. Referenced by the generated
// plan README's table and by the executor's contract, so it is a constant rather
// than a literal in three places.
const PhaseDocName = "phase-1-task.md"

// PhaseDocPath is the absolute path of a task dir's micro-phase doc — what the
// dispatched executor is told to tick and write its report into.
func PhaseDocPath(taskDir string) string {
	return filepath.Join(taskDir, "plan", PhaseDocName)
}

// cardReadme is the task-dir README wsingest parses (title, status, goal) and the
// lifecycle actions rewrite.
//
// `- **Status**: active` satisfies both readers deliberately: wsingest's statusRe
// accepts Status or Статус case-insensitively, and the lifecycle's
// upsertCardStatus rewrites whichever spelling it finds. A generated file should
// use the one the rest of this Go code is written in without breaking either.
func cardReadme(card Card, now time.Time) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# %s\n\n", oneLine(card.Title, card.ExternalID))
	fmt.Fprintf(&b, "- **Status**: active\n")
	fmt.Fprintf(&b, "- **Goal**: %s\n", oneLine(firstLine(card.Prompt), "see plan/"+PhaseDocName))
	fmt.Fprintf(&b, "- **Board card**: `%s`\n", card.ExternalID)
	fmt.Fprintf(&b, "- **Dispatched**: %s\n\n", now.UTC().Format("2006-01-02"))
	b.WriteString("Materialized by swarmery when the board card was dispatched. The plan/ dir\n")
	b.WriteString("below is the record of what the run actually did — its checkboxes and its\n")
	b.WriteString("Completion Report, not the column the card sits in.\n")
	return b.String()
}

// planReadme is the plan dir's README: an objective and the one-row sequencing
// table wsingest's parsePlanTable needs (a Doc column and a Phase column are the
// two it cannot do without).
func planReadme(card Card) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# %s\n\n", oneLine(card.Title, card.ExternalID))
	fmt.Fprintf(&b, "**Objective.** %s\n\n", oneLine(firstLine(card.Prompt), "Execute board card "+card.ExternalID+"."))
	fmt.Fprintf(&b, "Materialized from board card `%s` at dispatch. ONE phase, because a board card\n", card.ExternalID)
	b.WriteString("is one unit of work — what makes this a plan is not its size but its contract:\n")
	b.WriteString("the phase doc's checkboxes and Completion Report are the evidence of what\n")
	b.WriteString("happened, and nothing else is.\n\n")
	b.WriteString("## Phase sequencing\n\n")
	b.WriteString("| # | Phase | Doc | Depends on |\n")
	b.WriteString("|---|---|---|---|\n")
	fmt.Fprintf(&b, "| 1 | %s | `%s` | — |\n", tableCell(oneLine(card.Title, card.ExternalID)), PhaseDocName)
	return b.String()
}

// phaseDoc is the micro-phase itself.
//
// Two generic acceptance criteria, deliberately, rather than an LLM decomposition
// of the prompt: generation stays deterministic and free, and the criteria that
// matter for a board card are exactly these two — the work landed on its branch,
// and somebody or something accepted it. The `## Completion Report` section is
// the part that carries meaning, and it is the executor's to write.
func phaseDoc(card Card) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# Phase 1 — %s\n\n", oneLine(card.Title, card.ExternalID))
	if card.RepoPath != "" {
		fmt.Fprintf(&b, "**Repo:** %s\n", card.RepoPath)
	}
	fmt.Fprintf(&b, "**Branch:** swarm/%s\n", card.ExternalID)
	fmt.Fprintf(&b, "**Board card:** %s\n\n", card.ExternalID)
	b.WriteString("## Objective\n\n")
	prompt := strings.TrimSpace(card.Prompt)
	if prompt == "" {
		prompt = "(the card carried no prompt)"
	}
	b.WriteString(prompt + "\n\n")
	b.WriteString("## Acceptance criteria\n\n")
	fmt.Fprintf(&b, "- [ ] The task described above is implemented and committed on `swarm/%s` "+
		"with the `Swarm-Task-Id: %s` trailer.\n", card.ExternalID, card.ExternalID)
	b.WriteString("- [ ] Verification verdict is `pass`, or a reviewer explicitly accepted the card " +
		"from the Review lane.\n\n")
	b.WriteString("## Completion Report\n")
	return b.String()
}

// oneLine collapses a value to a single trimmed line, falling back when empty. Used
// for every value that lands in a markdown heading or table cell, where a newline
// would break the structure the scanner parses.
func oneLine(s, fallback string) string {
	s = strings.TrimSpace(strings.ReplaceAll(strings.ReplaceAll(s, "\r", " "), "\n", " "))
	s = strings.Join(strings.Fields(s), " ")
	if s == "" {
		return fallback
	}
	return s
}

// firstLine is the prompt's opening line — a card's prompt is a body, and only its
// first line belongs in a heading-adjacent field.
func firstLine(s string) string {
	if i := strings.IndexAny(s, "\r\n"); i >= 0 {
		return s[:i]
	}
	return s
}

// tableCell escapes the one character that would split a markdown table cell.
func tableCell(s string) string { return strings.ReplaceAll(s, "|", `\|`) }
