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
	"log"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/mdfence"
)

var (
	// A `- [ ]` / `- [x]` (or `* [X]`) acceptance-criteria checkbox line.
	checkboxRe = regexp.MustCompile(`(?i)^\s*[-*]\s+\[( |x)\]\s`)
	// Phase/step doc filenames: phase-<n>-<slug>.md or step-<nn>-<name>.md.
	phaseDocRe = regexp.MustCompile(`(?i)^(?:phase|step)-.*\.md$`)
	// The Doc column cell wraps the filename in backticks: `phase-1-x.md`.
	backtickDocRe = regexp.MustCompile("`([^`]+\\.md)`")
	// …or writes it as a markdown link: [phase-1-x.md](./phase-1-x.md). The
	// capture is the TARGET; a `#anchor` or query tail is trimmed off.
	linkDocRe = regexp.MustCompile(`\]\(\s*([^()\s#?]+\.md)`)
	// Leading integers in the "Depends on" cell: "1, 2", "1 (API), 3 (live)".
	// Deliberately liberal: real plans also write "01", "1-5", "5–6", "ph.1",
	// "0 (spike)", "step-01", or free prose like "rebase after 4/14" — none of
	// which carry a "Phase" prefix a stricter pattern could anchor on, so this
	// stays a bare-integer scan. The cost is that it also picks up unrelated
	// numbers quoted in the same cell (a decision id, an issue number, a
	// footnote); pruneDanglingDeps is the second-layer gate that catches those
	// after parsing, by checking every collected seq against the phases that
	// actually exist in the plan (see issue #190).
	leadingIntRe = regexp.MustCompile(`\b(\d+)\b`)
	// First markdown H1 (`# Title`) — the phase's display name fallback.
	h1Re = regexp.MustCompile(`(?m)^#\s+(.+?)\s*$`)
)

// epicPhase is one parsed phase (a README table row joined to its doc file).
type epicPhase struct {
	seq             int
	name            string
	docPath         string // absolute path to the phase/step doc (may not exist on disk yet)
	dependsOn       []int  // seq numbers this phase depends on
	checkboxesDone  int
	checkboxesTotal int
	docStatus       string // normalized `Status:` header marker; "" when absent
	docUpdatedAt    string // RFC3339 mtime of the doc file; "" when unresolved
	// repo is the RAW declared Repo cell ("`sk-next` (`/abs/sk-next`)", "sk-next
	// (+ helm)"), never a resolved path: turning it into a run root depends on the
	// filesystem and on project.json, which is the run surface's decision at Start
	// time (internal/repopath). "" when the plan declares nothing.
	repo             string
	completionReport string   // `## Completion Report` section body; "" when absent
	covers           []string // spec-criteria ids (`**Covers:** SC-1, SC-3`) the doc declares it delivers; nil when absent
	// verifyMode is the doc's opt-in to post-run verification (`**Verify:** strict`),
	// normalized to off|normal|strict. Doc-owned like everything else here: the plan
	// author decides which phases are worth grading, and a rescan re-derives it.
	verifyMode string
	// docModel is the RAW model the doc declares for its own runs (`**Model:** opus`,
	// ParseModel), never validated here — rung 2 of the phase-run ladder judges it at
	// admission so an unknown value fails the run instead of being dropped by a scan
	// that must never fail. "" when the doc declares nothing.
	docModel string
}

var (
	completionHeadingRe = regexp.MustCompile(`(?mi)^##\s+Completion Report\s*$`)
	nextSectionRe       = regexp.MustCompile(`(?m)^##\s`)
)

// parseCompletionReport extracts the body of the doc's `## Completion Report`
// section (from the heading to the next `## ` heading or EOF), trimmed.
// "" when the section is absent or empty — e.g. a template stub with nothing
// filled in yet. Pure; unit-tested.
func parseCompletionReport(text string) string {
	loc := completionHeadingRe.FindStringIndex(text)
	if loc == nil {
		return ""
	}
	body := text[loc[1]:]
	if next := nextSectionRe.FindStringIndex(body); next != nil {
		body = body[:next[0]]
	}
	return strings.TrimSpace(body)
}

// docStatusHeaderLines bounds the `Status:` scan to the doc's header block so a
// `Status: Pending` inside an embedded template/code fence further down can't
// masquerade as the phase's own marker.
const docStatusHeaderLines = 15

var docStatusRe = regexp.MustCompile(`(?i)^Status:\s*(.+?)\s*$`)

// parseDocStatus extracts the phase doc's own `Status:` marker from its header
// block (first docStatusHeaderLines lines, stopping at the first `##` section)
// and normalizes it to pending|in_progress|done. "" when absent or
// unrecognized. Pure; unit-tested.
func parseDocStatus(text string) string {
	lines := strings.Split(text, "\n")
	if len(lines) > docStatusHeaderLines {
		lines = lines[:docStatusHeaderLines]
	}
	for _, line := range lines {
		if strings.HasPrefix(line, "## ") {
			break // header block over — sections may quote status templates
		}
		m := docStatusRe.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		v := strings.ToLower(strings.TrimSpace(m[1]))
		v = strings.NewReplacer("-", " ", "_", " ").Replace(v)
		switch v {
		case "in progress", "wip", "active", "running", "started":
			return "in_progress"
		case "done", "complete", "completed":
			return "done"
		case "pending", "todo", "not started":
			return "pending"
		}
		return "" // an explicit but unrecognized marker — ignore
	}
	return ""
}

var (
	// The phase doc's header table row: `| **Repo** | `sk-next` (`/abs/sk-next`) |`.
	docRepoRowRe = regexp.MustCompile(`(?i)^\|\s*\*\*Repos?\*\*\s*\|\s*(.+?)\s*\|\s*$`)
	// The prose header form: `**Repo:** `/Volumes/Work/swarmery``.
	docRepoLineRe = regexp.MustCompile(`(?i)^\s*\*\*Repos?:\*\*\s*(.+?)\s*$`)
)

// VerifyOff / VerifyNormal / VerifyStrict are the phase-doc verification modes.
// Exported because internal/phaserun switches on them and epic_phases stores them;
// one spelling, one place.
const (
	VerifyOff    = "off"
	VerifyNormal = "normal"
	VerifyStrict = "strict"
)

// docVerifyRe matches the header line `**Verify:** strict`.
var docVerifyRe = regexp.MustCompile(`(?i)^\s*\*\*Verify:\*\*\s*(.+?)\s*$`)

// ParseDocVerify extracts the phase doc's opt-in to verification from its header
// block, normalized to off|normal|strict.
//
// Bounded by docStatusHeaderLines and stopping at the first `## ` section, for the
// reason parseDocStatus is: phase docs quote agent prompts and templates further
// down, and a `**Verify:**` line inside a quoted prompt is describing someone else's
// phase.
//
// Default and fallback are both `off`, deliberately in opposite directions: absent
// means the plan never asked (plans keep today's behaviour), and an UNRECOGNIZED
// value means the author asked for something this daemon does not understand — and
// running a grader nobody specified is worse than not running one. The tolerance
// contract for phase docs is "degrade with a warning, never fail the scan".
func ParseDocVerify(text string) string {
	lines := strings.Split(text, "\n")
	if len(lines) > docStatusHeaderLines {
		lines = lines[:docStatusHeaderLines]
	}
	for _, line := range lines {
		if strings.HasPrefix(line, "## ") {
			break
		}
		m := docVerifyRe.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		switch v := strings.ToLower(strings.TrimSpace(m[1])); v {
		case VerifyStrict:
			return VerifyStrict
		case VerifyNormal, "on", "yes", "true":
			return VerifyNormal
		case VerifyOff, "no", "false", "none":
			return VerifyOff
		default:
			log.Printf("warn: wsingest: unrecognized **Verify:** %q in a phase doc header — verification stays off", v)
			return VerifyOff
		}
	}
	return VerifyOff
}

// parseDocRepo extracts the phase doc's own declared repo cell from its header
// block. Bounded by docStatusHeaderLines and stopping at the first `## ` section
// for the same reason parseDocStatus is: phase docs quote agent prompts and
// templates further down, and a `**Repo:**` line inside a quoted prompt describes
// someone else's work, not this phase's. "" when absent. Pure; unit-tested.
func parseDocRepo(text string) string {
	lines := strings.Split(text, "\n")
	if len(lines) > docStatusHeaderLines {
		lines = lines[:docStatusHeaderLines]
	}
	for _, line := range lines {
		if strings.HasPrefix(line, "## ") {
			break
		}
		if m := docRepoRowRe.FindStringSubmatch(strings.TrimSpace(line)); m != nil {
			return strings.TrimSpace(m[1])
		}
		if m := docRepoLineRe.FindStringSubmatch(line); m != nil {
			return strings.TrimSpace(m[1])
		}
	}
	return ""
}

// SpecCriterion is one SC-tagged acceptance-criterion checkbox from plan/spec.md.
type SpecCriterion struct {
	Cid  string // "SC-1"
	Text string
	Done bool
	Line int // 0-based line index in spec.md
}

// specCriterionRe: `- [ ] **SC-1** — text` (also `*` bullets, `[x]`, and
// `:` / `-` / `–` / `—` separators).
var specCriterionRe = regexp.MustCompile(`(?i)^\s*[-*]\s+\[( |x)\]\s+\*\*(SC-\d+)\*\*\s*[:–—-]?\s*(.*)$`)

// ParseSpecCriteria extracts SC-tagged checkbox criteria. Checkboxes without an
// **SC-n** marker are NOT criteria (plain narrative checkboxes stay legal in
// spec.md). Duplicate cids: first occurrence wins, later ones are dropped (the
// scan-side caller warns per drop — see parseSpec). Pure; unit-tested.
//
// Exported for the same reason CountCheckboxes is: this is the single definition
// of what a spec criterion IS, and the plan-run gate (internal/planrun) re-reads
// spec.md with it at run-admission time — a second parser would drift and the
// two readers would disagree about the same file.
func ParseSpecCriteria(md string) []SpecCriterion {
	crits, _ := parseSpecCriteria(md)
	return crits
}

// parseSpecCriteria is ParseSpecCriteria plus the duplicate cids it dropped, so
// the scan can warn about them without re-parsing the format.
func parseSpecCriteria(md string) (crits []SpecCriterion, dups []string) {
	seen := map[string]bool{}
	for i, line := range strings.Split(md, "\n") {
		m := specCriterionRe.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		cid := strings.ToUpper(m[2])
		if seen[cid] {
			dups = append(dups, cid)
			continue
		}
		seen[cid] = true
		crits = append(crits, SpecCriterion{
			Cid:  cid,
			Text: strings.TrimSpace(m[3]),
			Done: strings.EqualFold(m[1], "x"),
			Line: i,
		})
	}
	return crits, dups
}

var (
	// The phase doc's header table row: `| **Covers** | SC-1, SC-3 |`.
	coversRowRe = regexp.MustCompile(`(?i)^\|\s*\*\*Covers:?\*\*\s*\|\s*(.+?)\s*\|\s*$`)
	// The prose header form: `**Covers:** SC-1, SC-3`.
	coversLineRe = regexp.MustCompile(`(?i)^\s*\*\*Covers:\*\*\s*(.+?)\s*$`)
	// SC-id tokens inside a Covers cell; anything else in the cell is ignored.
	coversTokenRe = regexp.MustCompile(`\bSC-\d+\b`)
)

// ParseCovers returns the deduped, order-preserving SC ids a phase doc declares
// via a `**Covers:** SC-1, SC-3` header line or a `| **Covers** | … |` header
// table row — scanned the same way parseDocRepo scans **Repo**: bounded to the
// doc's header block, first match wins, so a `**Covers:**` line quoted inside an
// embedded agent prompt further down describes someone else's phase, not this
// one's. nil when the doc declares nothing. Pure; unit-tested.
//
// Exported alongside ParseSpecCriteria as the single definition of the Covers
// format (internal/planrun's gate re-reads phase docs with it).
func ParseCovers(md string) []string {
	lines := strings.Split(md, "\n")
	if len(lines) > docStatusHeaderLines {
		lines = lines[:docStatusHeaderLines]
	}
	for _, line := range lines {
		if strings.HasPrefix(line, "## ") {
			break
		}
		cell := ""
		if m := coversRowRe.FindStringSubmatch(strings.TrimSpace(line)); m != nil {
			cell = m[1]
		} else if m := coversLineRe.FindStringSubmatch(line); m != nil {
			cell = m[1]
		}
		if cell == "" {
			continue
		}
		var out []string
		seen := map[string]bool{}
		for _, tok := range coversTokenRe.FindAllString(strings.ToUpper(cell), -1) {
			if seen[tok] {
				continue
			}
			seen[tok] = true
			out = append(out, tok)
		}
		return out // first Covers declaration wins, even when it names no ids
	}
	return nil
}

var (
	// The phase doc's header table row: `| **Model** | opus |`.
	docModelRowRe = regexp.MustCompile(`(?i)^\|\s*\*\*Model:?\*\*\s*\|\s*(.+?)\s*\|\s*$`)
	// The prose header form, beside the existing `**Covers:**` line: `**Model:** opus`.
	docModelLineRe = regexp.MustCompile(`(?i)^\s*\*\*Model:\*\*\s*(.+?)\s*$`)
	// Markdown decoration the author may wrap the value in: `**Model:** `opus``.
	docModelTrimSet = "`*_ \t"
)

// ParseModel returns the model a phase doc DECLARES for its own runs via a
// `**Model:** opus` header line or a `| **Model** | opus |` header table row —
// scanned exactly the way ParseCovers and ParseDocVerify scan: bounded to the
// doc's header block, stopping at the first `## ` section, first declaration
// wins. That bound is load-bearing here, not decorative: every phase doc in this
// workspace embeds a copy-paste agent prompt further down, and a `**Model:**`
// line quoted inside one is describing someone else's phase.
//
// "" when the doc declares nothing — which is what keeps a plan that never opted
// in behaving exactly as it did before this field existed.
//
// The value is returned VERBATIM (trimmed, and stripped of the markdown
// decoration an author may wrap it in), NOT normalized to a known model and NOT
// rejected here. This is the deliberate difference from ParseDocVerify, which
// folds an unrecognized value to `off` with a warning:
//
//   - the scan's contract is "degrade with a warning, never fail" — it runs on a
//     debounce over every plan on the machine and must not be stoppable by one
//     author's typo;
//   - rung 2 of the phase-run ladder has the OPPOSITE contract — an unknown model
//     in a document is a defect in the plan and must fail the run, naming the
//     document (internal/phaserun.Service.Start → resolveModel).
//
// Both hold only if this parser reports what the author wrote and leaves the
// judgement to the single resolution site. Folding an unknown value to "" here
// would re-create the bug internal/dispatch/service.go:979 records for playbooks:
// a `model:` chip rendered in the UI for several phases while dispatch ignored
// it, so the chip named a model no run ever used.
//
// Exported alongside ParseCovers as the single definition of the `**Model:**`
// format. Pure; unit-tested.
func ParseModel(md string) string {
	lines := strings.Split(md, "\n")
	if len(lines) > docStatusHeaderLines {
		lines = lines[:docStatusHeaderLines]
	}
	for _, line := range lines {
		if strings.HasPrefix(line, "## ") {
			break
		}
		cell := ""
		if m := docModelRowRe.FindStringSubmatch(strings.TrimSpace(line)); m != nil {
			cell = m[1]
		} else if m := docModelLineRe.FindStringSubmatch(line); m != nil {
			cell = m[1]
		} else {
			continue
		}
		// First Model declaration wins, even when it is empty after trimming
		// (`**Model:** ``` ` is an author writing "no opinion" the long way).
		return strings.Trim(cell, docModelTrimSet)
	}
	return ""
}

var (
	// The phase doc's header table row: `| **Effort** | high |`.
	docEffortRowRe = regexp.MustCompile(`(?i)^\|\s*\*\*Effort:?\*\*\s*\|\s*(.+?)\s*\|\s*$`)
	// The prose header form, beside `**Model:**`: `**Effort:** high`.
	docEffortLineRe = regexp.MustCompile(`(?i)^\s*\*\*Effort:\*\*\s*(.+?)\s*$`)
)

// ParseEffort returns the reasoning depth a phase doc DECLARES for its own runs
// via an `**Effort:** high` header line or a `| **Effort** | high |` header
// table row. Same scan, same bound and same contract as ParseModel — including
// the header-block bound, which is load-bearing for the identical reason: every
// phase doc in this workspace embeds a copy-paste agent prompt further down, and
// an `**Effort:**` line quoted inside one is describing someone else's phase.
//
// "" when the doc declares nothing, which is what keeps a plan that never opted
// in behaving exactly as it did before this field existed.
//
// The value is returned VERBATIM (trimmed, decoration stripped), NOT normalized
// and NOT rejected here — see ParseModel for why the scan degrades and the
// single resolution site judges (phaserun.resolveEffort).
//
// Pure; unit-tested.
func ParseEffort(md string) string {
	lines := strings.Split(md, "\n")
	if len(lines) > docStatusHeaderLines {
		lines = lines[:docStatusHeaderLines]
	}
	for _, line := range lines {
		if strings.HasPrefix(line, "## ") {
			break
		}
		cell := ""
		if m := docEffortRowRe.FindStringSubmatch(strings.TrimSpace(line)); m != nil {
			cell = m[1]
		} else if m := docEffortLineRe.FindStringSubmatch(line); m != nil {
			cell = m[1]
		} else {
			continue
		}
		// First Effort declaration wins, even when it is empty after trimming.
		return strings.Trim(cell, docModelTrimSet)
	}
	return ""
}

// CountCheckboxes counts acceptance-criteria checkboxes in a doc, returning
// (done, total). Pure; unit-tested. A doc with none yields (0, 0).
//
// Exported because it defines what epic_phases.checkboxes_done MEANS, and this
// scanner is not the only reader that needs that number at an exact instant:
// phaserun's exit stamp closes its measurement interval from the doc, because the
// column is written only here, on a debounce, with nothing triggering a scan at run
// end. Anyone needing "how many criteria are ticked right now" must call this
// rather than re-parse the format — a second parser would drift and the two counts
// would disagree about the same file.
// Checkboxes inside FENCED CODE BLOCKS do not count. Phase docs quote things:
// the template a generator emits, a fixture of real output, an agent prompt that
// itself lists criteria. Those are illustrations of a checklist, not the doc's own
// work, and counting them makes a finished phase read as unfinished forever —
// observed on a shipped phase that displayed 7/11 with every real criterion ticked
// and the four strays living in two ```markdown examples.
func CountCheckboxes(text string) (done, total int) {
	forEachLineOutsideFences(text, func(_ int, line string) {
		m := checkboxRe.FindStringSubmatch(line)
		if m == nil {
			return
		}
		total++
		if strings.EqualFold(m[1], "x") {
			done++
		}
	})
	return done, total
}

// untickedLabelCap bounds one criterion's rendered label. Criteria run long —
// a whole paragraph with an embedded command is common — and the consumer is a
// continuation MESSAGE sent back into a session whose context window is already
// at its largest, so the list has to identify each criterion, not restate it.
const untickedLabelCap = 160

// UntickedCheckboxes returns the LABEL of every unticked acceptance-criteria
// checkbox, in document order, each trimmed of its `- [ ] ` marker, of markdown
// emphasis, and to untickedLabelCap runes.
//
// It walks the SAME line walker CountCheckboxes and tickAllCheckboxes use, which
// is the whole point of it living here rather than in the engine that wants it:
// "how many are unticked" and "which ones are unticked" must be answers about
// the same set of lines. A phase doc quoting a checklist inside a ``` fence has
// bitten this codebase before (a shipped phase stuck at 7/11 forever), and a
// second parser in phaserun would hand a continuation four criteria that live in
// a markdown example and can never be ticked.
//
// Used by the run completion loop: when a cleanly-exited run leaves criteria
// unticked, this is the list it is resumed with.
func UntickedCheckboxes(text string) []string {
	var out []string
	forEachLineOutsideFences(text, func(_ int, line string) {
		loc := checkboxRe.FindStringSubmatchIndex(line)
		if loc == nil {
			return
		}
		// loc[2]:loc[3] is the state character; anything at/after the match end is
		// the label.
		if strings.EqualFold(line[loc[2]:loc[3]], "x") {
			return
		}
		label := strings.TrimSpace(line[loc[1]:])
		label = strings.Trim(label, "*_` ")
		if label == "" {
			return
		}
		if r := []rune(label); len(r) > untickedLabelCap {
			label = string(r[:untickedLabelCap]) + "…"
		}
		out = append(out, label)
	})
	return out
}

// forEachLineOutsideFences calls fn for every line that is not inside a fenced
// code block.
//
// The single line walker for both checkbox readers: counting and ticking MUST
// agree about which lines are the doc's own, or an auto-tick would rewrite a
// checkbox inside a code sample and the count would then disagree with the file.
// It now lives in internal/mdfence because runcore's blocked-sentinel matcher
// needs the same rule and cannot import this package (wsingest imports runcore);
// the local name stays so every call site here reads as it always did.
func forEachLineOutsideFences(text string, fn func(i int, line string)) {
	mdfence.ForEachLine(text, fn)
}

// phaseCols is the 0-based column layout of a phase-sequencing table; -1 means
// the column is absent.
type phaseCols struct{ seq, name, doc, dep, repo int }

// phaseTableCols locates the phase-sequencing table's header row and returns its
// column layout. ok=false when no such header is present. Pure; unit-tested.
//
// `Repo` is matched EXACTLY (plus Repos/Repository): plans in the wild also carry
// a "Repo area" column that names a subsystem, not a checkout, and treating that
// as a run root would send a run into a directory that does not exist.
func phaseTableCols(cells []string) (cols phaseCols, ok bool) {
	cols = phaseCols{seq: -1, name: -1, doc: -1, dep: -1, repo: -1}
	for i, c := range cells {
		switch h := strings.ToLower(strings.TrimSpace(c)); {
		case h == "#" || h == "seq":
			cols.seq = i
		case h == "phase" || h == "name":
			cols.name = i
		case h == "doc" || h == "file":
			cols.doc = i
		case h == "repo" || h == "repos" || h == "repository":
			cols.repo = i
		case strings.HasPrefix(h, "depends"):
			cols.dep = i
		}
	}
	// The doc column is the one indispensable anchor (it names the phase file);
	// a name column is required to label the phase. seq/depends/repo degrade to
	// positional/empty when absent — a plan without a Repo column indexes exactly
	// as it did before the column existed.
	ok = cols.doc >= 0 && cols.name >= 0
	return cols, ok
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
		cols    phaseCols
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
			if c, ok := phaseTableCols(cells); ok {
				cols, haveHdr = c, true
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
		repo := ""
		if cols.repo >= 0 && cols.repo < len(cells) {
			repo = strings.TrimSpace(cells[cols.repo])
		}
		if name == "" {
			name = strings.TrimSuffix(doc, ".md")
		}
		out = append(out, epicPhase{seq: seq, name: name, docPath: doc, dependsOn: dep, repo: repo})
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
// backtick-wrapped form, then a markdown link, then the first bare token ending
// in .md. Returns "" when the cell names no doc. Pure.
//
// The link form is not a nicety: a planner writing a README table reaches for
// `[phase-1-x.md](./phase-1-x.md)` so the doc is clickable, and that cell is a
// single token ending in ")" — invisible to the bare-token scan. Every row then
// names no doc, the whole table parses to nothing, and the plan silently
// degrades to the one-phase-per-doc fallback, which knows no Depends on. The
// operator sees the right phases with an empty DAG, so a plan run fans all of
// them out at once.
func docFromCell(cell string) string {
	if m := backtickDocRe.FindStringSubmatch(cell); m != nil {
		return filepath.Base(strings.TrimSpace(m[1]))
	}
	// The link TARGET, not the label: the target is the path, and a mismatched
	// label is the author's typo, not the file the phase runs from.
	if m := linkDocRe.FindStringSubmatch(cell); m != nil {
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
// file keeps its table metadata (and that path) with zero checkboxes, and warns.
//
// The path is kept EVEN WHEN THE FILE IS ABSENT because doc_path is the natural
// key of an epic_phases row: blanking it to "" made every unresolved row in a plan
// collide on (task, ""), so a plan whose docs were not written yet indexed as one
// mislabelled phase instead of all of them. A doc can also be deleted between
// scans, so every reader already has to tolerate a path that no longer resolves.
//
// Pure w.r.t. the DB (touches only the filesystem); the workhorse behind
// applyEpics and the table-driven tests.
func parsePlan(planDir string, warn func(string, ...any)) []epicPhase {
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
			// Unresolved: keep the table metadata AND the path (the row's identity),
			// just no counts. Loud, because a plan naming a doc that isn't there is a
			// real authoring problem, not a shape to absorb silently.
			warn("epics plan %s: phase doc %s named by the sequencing table is missing", planDir, doc)
			continue
		}
		// Prefer the doc's own H1 as the display name over a terse table label.
		if m := h1Re.FindSubmatch(body); m != nil {
			if title := capText(string(m[1])); title != "" {
				phases[i].name = title
			}
		}
		phases[i].checkboxesDone, phases[i].checkboxesTotal = CountCheckboxes(string(body))
		phases[i].docStatus = parseDocStatus(string(body))
		phases[i].completionReport = parseCompletionReport(string(body))
		// The doc's own header outranks the README table cell: it is the more
		// specific statement, it lives next to the work, and it is the form that
		// carries an absolute path.
		if repo := parseDocRepo(string(body)); repo != "" {
			phases[i].repo = repo
		}
		phases[i].covers = ParseCovers(string(body))
		phases[i].verifyMode = ParseDocVerify(string(body))
		// Same single read of the doc body as every extraction above it — the doc is
		// opened once per scan and each parser is handed the bytes, never the path.
		phases[i].docModel = ParseModel(string(body))
		if fi, err := os.Stat(abs); err == nil {
			phases[i].docUpdatedAt = fi.ModTime().UTC().Format(time.RFC3339)
		}
	}
	pruneDanglingDeps(phases, planDir, warn)
	return phases
}

// pruneDanglingDeps drops depends_on entries whose seq is not among this
// plan's own phases. leadingIntRe's bare-integer scan of the "Depends on"
// cell is deliberately liberal (see its comment) because real plans express
// dependencies in prose, not just clean lists — but that same liberalism
// picks up a decision id, an issue number, or a footnote quoted in the same
// cell as if it named another phase. "Phase 2 (see decision D-11)" parses to
// [2, 11]; without this gate a phase 11 that will never exist becomes a
// dependency, and the referencing phase is blocked forever with no
// explanation (issue #190). This is the structural check that regex tuning
// alone cannot make safe: whatever the cell said, only seqs that actually
// appear in the plan survive. Pure; unit-tested.
func pruneDanglingDeps(phases []epicPhase, planDir string, warn func(string, ...any)) {
	seqs := make(map[int]bool, len(phases))
	for _, p := range phases {
		seqs[p.seq] = true
	}
	for i := range phases {
		if len(phases[i].dependsOn) == 0 {
			continue
		}
		kept := phases[i].dependsOn[:0:0] // fresh backing array — never alias the original
		for _, dep := range phases[i].dependsOn {
			if seqs[dep] {
				kept = append(kept, dep)
				continue
			}
			warn("epics plan %s: phase %d depends_on references phase %d, which does not exist in this plan — dropped (a stray number in the \"Depends on\" cell, e.g. a decision id or issue number?)",
				planDir, phases[i].seq, dep)
		}
		phases[i].dependsOn = kept
	}
}

// parseSpec reads <planDir>/spec.md into its SC-tagged criteria. A missing file
// is the common case (the spec is optional) — nil, no warn. Any other stumble
// warns and degrades to nil; the scan never fails over a spec.
func parseSpec(planDir string, warn func(string, ...any)) []SpecCriterion {
	b, err := os.ReadFile(filepath.Join(planDir, "spec.md"))
	if err != nil {
		if !os.IsNotExist(err) {
			warn("epics plan %s: spec.md unreadable: %v", planDir, err)
		}
		return nil
	}
	crits, dups := parseSpecCriteria(string(b))
	for _, cid := range dups {
		warn("epics plan %s: spec.md declares %s more than once — first occurrence wins", planDir, cid)
	}
	return crits
}

// parserVersion identifies WHAT parsePlan extracts. It is mixed into planHash so
// a parser that learns a new field re-parses plans whose bytes are unchanged.
// v2: epic_phases.repo (declared `Repo` column / phase doc header), migration 0046.
// v3: pruneDanglingDeps — depends_on no longer keeps a seq that names no phase
// in the plan (issue #190); already-indexed rows carrying a phantom dependency
// need a re-parse to shed it even though their plan bytes never changed.
// v4: spec.md criteria + phase Covers — plan/spec.md's SC-tagged checkboxes
// become spec_criteria rows and a phase doc's `**Covers:**` declaration lands in
// epic_phases.covers; already-indexed plans need a re-parse to populate both.
// v5: two extractions changed at once and neither bumped this on its own, which is
// exactly the failure this constant exists to prevent — an indexed plan keeps
// whatever the previous parser stored until its bytes happen to change:
//   - CountCheckboxes ignores checkboxes inside fenced code blocks, so a doc that
//     quotes a checklist template stops inflating its total (a shipped phase read
//     7/11 with all seven criteria ticked);
//   - epic_phases.verify_mode is parsed from the `**Verify:**` header line.
//
// v6: epic_phases.doc_model — the `**Model:**` header line (ParseModel, migration
// 0069). Without the bump an already-indexed plan whose author adds the line keeps
// a NULL doc_model until some OTHER byte of the plan changes, and rung 2 of the
// phase-run ladder would silently not apply — the exact class of failure this
// constant exists to prevent.
const parserVersion = "v6"

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
	// The parser version is part of the identity of a parse result, not just the
	// bytes it read. Without it, a release that teaches the parser a NEW field
	// (0046's declared `Repo`) leaves every already-indexed plan on its old row for
	// ever: the files did not change, the hash matched, and the gate skipped the
	// only pass that could have filled the column. Bump this whenever parsePlan
	// starts extracting something it did not extract before.
	h.Write([]byte(parserVersion))
	h.Write([]byte{0})
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

	// The gate keys on hash AND path: a working→archive zone move keeps the
	// content identical but relocates plan/ — the path must converge or the
	// doc endpoints keep resolving into the pruned working/ tree.
	var prevHash, prevPath string
	err = s.db.QueryRow(
		`SELECT content_hash, path FROM task_artifacts WHERE task_id = ? AND kind = 'plan'`,
		taskID).Scan(&prevHash, &prevPath)
	switch {
	case err == nil && prevHash == hash && prevPath == planDir:
		return // unchanged — skip the parse entirely
	case err != nil && err != sql.ErrNoRows:
		warn("epics task#%d: hash lookup: %v", taskID, err)
		return
	}

	phases := parsePlan(planDir, warn)
	crits := parseSpec(planDir, warn)

	// README presence gates the prune: a plan dir caught mid-`git checkout` (or
	// mid-archive-move) has neither README nor docs, and must not be read as
	// "the plan now has no phases". See applyEpics.
	readmePresent := false
	if _, err := os.Stat(filepath.Join(planDir, "README.md")); err == nil {
		readmePresent = true
	}

	tx, err := s.db.Begin()
	if err != nil {
		warn("epics task#%d: begin: %v", taskID, err)
		return
	}
	if err := applyEpics(tx, taskID, phases, readmePresent); err != nil {
		tx.Rollback()
		warn("epics task#%d (%s): %v", taskID, planDir, err)
		return
	}
	if err := applySpec(tx, taskID, crits); err != nil {
		tx.Rollback()
		warn("epics task#%d (%s): spec: %v", taskID, planDir, err)
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
		return
	}
	// plans-page-lifecycle phase 1: the plan really changed (hash/path gate
	// passed AND the upsert committed) — let the serve path publish
	// plan_updated. Nil on the one-shot scan subcommand.
	if s.cfg.NotifyPlan != nil {
		s.cfg.NotifyPlan(taskID)
	}
}

// applyEpics folds the parsed plan into the task's epic_phases rows by UPSERTING
// on the natural key UNIQUE(workspace_task_id, doc_path).
//
// The plan doc is authoritative for STRUCTURE only — seq, name, depends_on,
// covers, checkbox counts, doc status, completion report. It owns nothing else. Everything
// the daemon writes about a phase (activated_at / activated_board_task_id and the
// whole run_* family) is deliberately absent from the DO UPDATE SET list, so it
// survives a rescan untouched.
//
// This must never go back to delete + re-insert: the phase executor's own job is
// to edit its phase doc and tick its checkbox, which changes the plan hash and
// triggers exactly this path. A delete would wipe the run state the run is being
// measured by, and the new row id would orphan the swarm/phase-<id> branch the run
// just committed to — a run only kept its state if it achieved nothing.
//
// doc_path IS the identity, and a RENAMED phase doc is therefore a delete + insert.
// Matching on seq or name INSTEAD would silently merge genuinely different phases —
// the plan doc legitimately rewrites both — so identity stays doc_path. What changed
// is the assumption that used to make the consequence acceptable ("a rename is a rare,
// human, out-of-band edit"): plan regeneration renames the whole doc set machine-side,
// mid-run, and the delete took the run_* family and the run's branch with it.
//
// So the state moves instead of the key: carryAcrossRenames performs a one-shot
// hand-over from a vanished doc's row to the row that replaced it, inside this same
// transaction where both sets are visible and the match can be required to be 1:1.
// That is strictly weaker than making seq an identity — an ambiguous match carries
// nothing — and it is the only place where both halves are knowable at once.
//
// readmePresent gates the prune — see the guard below.
func applyEpics(tx *sql.Tx, taskID int64, phases []epicPhase, readmePresent bool) error {
	// Snapshot BEFORE the upserts: afterwards the inserts are indistinguishable from
	// rows that were already there, and "which doc paths existed a moment ago" is
	// exactly what identifies a rename.
	before, err := snapshotPhases(tx, taskID)
	if err != nil {
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
		coversJSON, err := json.Marshal(p.covers)
		if err != nil {
			return err
		}
		if p.covers == nil {
			coversJSON = []byte("[]")
		}
		var docStatus any
		if p.docStatus != "" {
			docStatus = p.docStatus
		}
		var docUpdatedAt any
		if p.docUpdatedAt != "" {
			docUpdatedAt = p.docUpdatedAt
		}
		var completionReport any
		if p.completionReport != "" {
			completionReport = p.completionReport
		}
		var repo any
		if p.repo != "" {
			repo = p.repo
		}
		// NOT NULL DEFAULT 'off' in the schema, so an empty parse must still write a
		// value rather than a NULL the column would reject.
		verifyMode := p.verifyMode
		if verifyMode == "" {
			verifyMode = VerifyOff
		}
		// Nullable (0069), unlike verify_mode: "the doc declares no model" and "the
		// doc declares the empty model" are the same statement, and NULL is how every
		// other doc-owned optional on this row spells it.
		var docModel any
		if p.docModel != "" {
			docModel = p.docModel
		}
		if _, err := tx.Exec(`
			INSERT INTO epic_phases
				(workspace_task_id, seq, name, doc_path, depends_on,
				 checkboxes_total, checkboxes_done, doc_status, doc_updated_at,
				 completion_report, repo, covers, verify_mode, doc_model)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT(workspace_task_id, doc_path) DO UPDATE SET
				seq               = excluded.seq,
				name              = excluded.name,
				depends_on        = excluded.depends_on,
				checkboxes_total  = excluded.checkboxes_total,
				checkboxes_done   = excluded.checkboxes_done,
				doc_status        = excluded.doc_status,
				doc_updated_at    = excluded.doc_updated_at,
				completion_report = excluded.completion_report,
				repo              = excluded.repo,
				covers            = excluded.covers,
				verify_mode       = excluded.verify_mode,
				-- Re-derived, INCLUDING back to NULL: deleting the **Model:** line
				-- from a doc must actually retract the declaration, the same way
				-- deleting a **Covers:** line does.
				doc_model         = excluded.doc_model`,
			taskID, p.seq, p.name, p.docPath, string(depJSON),
			p.checkboxesTotal, p.checkboxesDone, docStatus, docUpdatedAt,
			completionReport, repo, string(coversJSON), verifyMode, docModel); err != nil {
			return err
		}
	}

	// A plan dir with no README is not a plan with no phases — it is a plan we
	// caught mid-move: a `git checkout` swapping branches, or an `agent-work.sh
	// archive` relocating the task dir, both transiently empty plan/. planHash
	// happily hashes an empty dir, so without this guard that instant of emptiness
	// prunes every phase of the epic and destroys the whole run_* family
	// irreversibly — the one remaining way a rescan could still lose run state.
	// A plan whose README IS present but lists no phases is a real edit and prunes
	// normally below.
	if !readmePresent {
		var existing int
		if err := tx.QueryRow(
			`SELECT COUNT(*) FROM epic_phases WHERE workspace_task_id = ?`, taskID).
			Scan(&existing); err != nil {
			return err
		}
		if existing > 0 {
			return nil
		}
	}

	// Hand daemon-owned state from renamed-away docs to the rows that replaced them,
	// BEFORE the prune removes the evidence.
	carried, err := carryAcrossRenames(tx, taskID, phases, before)
	if err != nil {
		return err
	}

	// A phase doc removed from the plan README must not linger. Delete by
	// exclusion AFTER the upserts — never before, or the surviving rows lose
	// their identity (and their run state) on every rescan. With no phases at
	// all the guard collapses to an unconditional delete, which is correct.
	keep := make([]any, 0, len(phases)+len(carried)+1)
	keep = append(keep, taskID)
	ph := make([]string, 0, len(phases))
	for _, p := range phases {
		ph = append(ph, "?")
		keep = append(keep, p.docPath)
	}
	q := `DELETE FROM epic_phases WHERE workspace_task_id = ?`
	if len(ph) > 0 {
		q += ` AND doc_path NOT IN (` + strings.Join(ph, ",") + `)`
	}
	// A run in flight is never deleted. Its row is the only handle the daemon and the
	// UI have on that process — no row, no Cancel, no session link, and the executor
	// keeps running unreachable. A carried-over source IS deleted even while running:
	// its state now lives on the replacement row, and keeping both would leave two rows
	// claiming the same run.
	if len(carried) > 0 {
		cph := make([]string, 0, len(carried))
		for _, docPath := range carried {
			cph = append(cph, "?")
			keep = append(keep, docPath)
		}
		q += ` AND (run_state <> 'running' OR doc_path IN (` + strings.Join(cph, ",") + `))`
	} else {
		q += ` AND run_state <> 'running'`
	}

	// Whatever the guard spares must be visible: an orphaned running row is a real
	// inconsistency (its doc is gone), just a recoverable one, and silence here is how
	// it would be mistaken for a healthy phase.
	if err := logKeptRunningOrphans(tx, taskID, phases, carried); err != nil {
		return err
	}
	if _, err := tx.Exec(q, keep...); err != nil {
		return err
	}
	return nil
}

// applySpec folds the parsed plan/spec.md criteria into the task's spec_criteria
// rows by UPSERTING on the natural key UNIQUE(workspace_task_id, cid), then
// pruning by exclusion — an empty parse (spec.md removed, or emptied of SC
// checkboxes) deletes every row for the task. Unlike epic_phases there is no
// daemon-owned state on these rows: wsingest owns them outright, so the prune
// needs no running-guard and identity churn is harmless (readers key on cid).
func applySpec(tx *sql.Tx, taskID int64, crits []SpecCriterion) error {
	for i, c := range crits {
		done := 0
		if c.Done {
			done = 1
		}
		if _, err := tx.Exec(`
			INSERT INTO spec_criteria (workspace_task_id, pos, cid, text, done, line)
			VALUES (?, ?, ?, ?, ?, ?)
			ON CONFLICT(workspace_task_id, cid) DO UPDATE SET
				pos  = excluded.pos,
				text = excluded.text,
				done = excluded.done,
				line = excluded.line`,
			taskID, i, c.Cid, c.Text, done, c.Line); err != nil {
			return err
		}
	}
	args := make([]any, 0, len(crits)+1)
	args = append(args, taskID)
	q := `DELETE FROM spec_criteria WHERE workspace_task_id = ?`
	if len(crits) > 0 {
		ph := make([]string, 0, len(crits))
		for _, c := range crits {
			ph = append(ph, "?")
			args = append(args, c.Cid)
		}
		q += ` AND cid NOT IN (` + strings.Join(ph, ",") + `)`
	}
	_, err := tx.Exec(q, args...)
	return err
}

// phaseState is the daemon-owned half of an epic_phases row — everything the plan doc
// does NOT author. The plan doc owns structure (seq, name, depends_on, covers, checkbox
// counts, doc status, completion report); these columns are written by dispatch and by the run
// services, and a rescan must never be able to invent or destroy them.
type phaseState struct {
	id                   int64
	seq                  int
	docPath              string
	runState             string
	runSessionUUID       sql.NullString
	runStartedAt         sql.NullString
	runEndedAt           sql.NullString
	runError             sql.NullString
	runBranch            sql.NullString
	runCheckboxesBefore  sql.NullInt64
	runCheckboxesAfter   sql.NullInt64
	activatedAt          sql.NullString
	activatedBoardTaskID sql.NullInt64
	// Verification's own daemon-owned trio (0057). verify_mode is NOT here: the doc
	// authors that one, so a rescan re-derives it — these three are written by the
	// run and must survive a doc rename exactly like run_branch does.
	runStartPoint sql.NullString
	verifyVerdict sql.NullString
	verifyDetail  sql.NullString
}

// carriesState reports whether the row holds anything a rescan must not lose.
func (p phaseState) carriesState() bool {
	return p.runState != "idle" || p.runSessionUUID.Valid || p.activatedBoardTaskID.Valid ||
		// A verdict outlives its run: a phase whose row is otherwise idle can still
		// carry the grade of the run that produced it, and losing it on a doc rename
		// would silently downgrade "verified" to "never verified".
		p.verifyVerdict.Valid
}

// snapshotPhases reads every phase row of the task with its daemon-owned columns.
// ALL rows, not only the stateful ones: the stateless ones are what distinguish a
// renamed doc (its path vanished) from a genuinely new phase (its path is new).
func snapshotPhases(tx *sql.Tx, taskID int64) ([]phaseState, error) {
	rows, err := tx.Query(`
		SELECT id, seq, doc_path, run_state, run_session_uuid, run_started_at,
		       run_ended_at, run_error, run_branch, run_checkboxes_before,
		       run_checkboxes_after, activated_at, activated_board_task_id,
		       run_start_point, verify_verdict, verify_detail
		  FROM epic_phases
		 WHERE workspace_task_id = ?`, taskID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []phaseState
	for rows.Next() {
		var p phaseState
		if err := rows.Scan(&p.id, &p.seq, &p.docPath, &p.runState, &p.runSessionUUID,
			&p.runStartedAt, &p.runEndedAt, &p.runError, &p.runBranch,
			&p.runCheckboxesBefore, &p.runCheckboxesAfter, &p.activatedAt,
			&p.activatedBoardTaskID,
			&p.runStartPoint, &p.verifyVerdict, &p.verifyDetail); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// carryAcrossRenames moves daemon-owned state from rows whose doc path disappeared onto
// the rows freshly inserted for this task, matching on seq. It returns the doc paths of
// the sources it drained, so the prune can delete them unconditionally.
//
// The match is required to be 1:1 — exactly one vanished row and exactly one brand-new
// row for that seq. Anything else (two phases collapsed onto one seq, a renumbered plan)
// carries nothing and says so: merging two genuinely different phases is a worse outcome
// than losing a chip, which is the same reasoning that keeps seq out of the identity key.
func carryAcrossRenames(tx *sql.Tx, taskID int64, phases []epicPhase, before []phaseState) ([]string, error) {
	if len(before) == 0 || len(phases) == 0 {
		return nil, nil
	}
	beforePaths := make(map[string]bool, len(before))
	for _, p := range before {
		beforePaths[p.docPath] = true
	}
	nowPaths := make(map[string]bool, len(phases))
	for _, p := range phases {
		nowPaths[p.docPath] = true
	}

	// Vanished rows that hold state, and rows inserted by the upserts above, both
	// bucketed by seq.
	vanished := map[int][]phaseState{}
	for _, p := range before {
		if !nowPaths[p.docPath] && p.carriesState() {
			vanished[p.seq] = append(vanished[p.seq], p)
		}
	}
	if len(vanished) == 0 {
		return nil, nil
	}
	inserted := map[int][]epicPhase{}
	for _, p := range phases {
		if !beforePaths[p.docPath] {
			inserted[p.seq] = append(inserted[p.seq], p)
		}
	}

	var drained []string
	for seq, olds := range vanished {
		news := inserted[seq]
		if len(olds) != 1 || len(news) != 1 {
			log.Printf("warn: wsingest: task=%d phase seq=%d — %d vanished row(s) with run state and %d new doc(s); state NOT carried (ambiguous match)",
				taskID, seq, len(olds), len(news))
			continue
		}
		old, dst := olds[0], news[0]
		if _, err := tx.Exec(`
			UPDATE epic_phases
			   SET run_state=?, run_session_uuid=?, run_started_at=?, run_ended_at=?,
			       run_error=?, run_branch=?, run_checkboxes_before=?,
			       run_checkboxes_after=?, activated_at=?, activated_board_task_id=?,
			       run_start_point=?, verify_verdict=?, verify_detail=?
			 WHERE workspace_task_id = ? AND doc_path = ?`,
			old.runState, old.runSessionUUID, old.runStartedAt, old.runEndedAt,
			old.runError, old.runBranch, old.runCheckboxesBefore, old.runCheckboxesAfter,
			old.activatedAt, old.activatedBoardTaskID,
			old.runStartPoint, old.verifyVerdict, old.verifyDetail, taskID, dst.docPath); err != nil {
			return nil, err
		}
		drained = append(drained, old.docPath)
		log.Printf("wsingest: task=%d phase seq=%d carried run state across rename %s → %s (run_state=%s)",
			taskID, seq, filepath.Base(old.docPath), filepath.Base(dst.docPath), old.runState)
	}
	return drained, nil
}

// logKeptRunningOrphans reports the rows the prune's running-guard is about to spare:
// a run whose phase doc vanished with no replacement to carry it to.
func logKeptRunningOrphans(tx *sql.Tx, taskID int64, phases []epicPhase, carried []string) error {
	keepPaths := make(map[string]bool, len(phases)+len(carried))
	for _, p := range phases {
		keepPaths[p.docPath] = true
	}
	for _, docPath := range carried {
		keepPaths[docPath] = true
	}
	rows, err := tx.Query(
		`SELECT id, doc_path FROM epic_phases WHERE workspace_task_id = ? AND run_state = 'running'`, taskID)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var (
			id      int64
			docPath string
		)
		if err := rows.Scan(&id, &docPath); err != nil {
			return err
		}
		if !keepPaths[docPath] {
			log.Printf("warn: wsingest: task=%d phase %d is running but its doc %s vanished from the plan — row kept",
				taskID, id, docPath)
		}
	}
	return rows.Err()
}
