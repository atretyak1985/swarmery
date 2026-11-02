package improve

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
)

// ErrNoChange is the sentinel for an empty diff block under a FORM other than
// KEEP_BASE. KEEP_BASE is the sanctioned "no change" answer since harness²
// phase 3; an empty diff that does not say so is still a no-change outcome and
// lands on the row as 'failed' (retriable), exactly as before.
var ErrNoChange = errors.New("model returned an empty diff (no justified change)")

// Form values — the FORM line (form ladder rung, or KEEP_BASE), stored
// lowercase in agent_change_proposals.form (migration 0134).
const (
	FormSkill    = "skill"
	FormScript   = "script"
	FormSubagent = "subagent"
	FormHook     = "hook"
	FormAlwaysOn = "always-on"
	FormKeepBase = "keep_base"
)

// Lens values — every hunk carries one; a proposal's lens is the single hunk
// lens or LensMixed (migration 0134).
const (
	LensProcedure = "procedure"
	LensKnowledge = "knowledge"
	LensMixed     = "mixed"
)

// OutcomeKeptBase is agent_change_proposals.outcome for a KEEP_BASE answer.
const OutcomeKeptBase = "kept_base"

// ladderForms is the closed FORM vocabulary a diff (or a follow-up) may name.
var ladderForms = map[string]bool{
	FormSkill: true, FormScript: true, FormSubagent: true, FormHook: true, FormAlwaysOn: true,
}

// Followup is one change the model judged outside the one-file rewriter's
// reach (a script, a hook, a subagent, another skill) — shown to the operator,
// never applied.
type Followup struct {
	Form string `json:"form"`
	Text string `json:"text"`
}

// Parsed is a model answer that satisfied the output contract.
type Parsed struct {
	// Form is one of the Form* constants.
	Form string
	// KeepBase is the KEEP_BASE answer: Diff and Lens are empty, Rationale is
	// the reason.
	KeepBase  bool
	Diff      string
	Rationale string
	// Lens is the single hunk lens, or LensMixed; "" for KEEP_BASE.
	Lens      string
	Followups []Followup
}

// Section headings of the output contract.
const (
	hForm      = "## Form"
	hDiff      = "## Diff"
	hRationale = "## Rationale"
	hFollowups = "## Follow-ups"
)

var contractHeadings = []string{hForm, hDiff, hRationale, hFollowups}

// parseOutput enforces the outputContract on raw model stdout, strictly (D8):
//
//   - "## Form" is required; its first line names a ladder form or KEEP_BASE.
//   - KEEP_BASE needs a non-empty "## Rationale" (the reason) and no diff.
//   - Otherwise "## Diff" holds EXACTLY ONE fenced ```diff block, and EVERY
//     hunk header carries `lens: procedure|knowledge` after its closing @@
//     (git apply ignores that trailer, so the diff stays applicable as-is).
//   - "## Rationale" is required and non-empty; "## Follow-ups" is optional.
//
// Leading chatter before the first heading is tolerated. Any deviation is an
// error, which the caller stores on the row as status 'failed' — retriable.
// An empty diff block under a non-KEEP_BASE form returns ErrNoChange.
func parseOutput(out string) (*Parsed, error) {
	sec := splitSections(out)

	formSec, ok := sec[hForm]
	if !ok {
		return nil, errors.New(`missing "## Form" section`)
	}
	form, err := parseForm(formSec)
	if err != nil {
		return nil, err
	}

	rationaleSec, ok := sec[hRationale]
	if !ok {
		if form == FormKeepBase {
			return nil, errors.New(`KEEP_BASE without a reason: missing "## Rationale" section`)
		}
		return nil, errors.New(`missing "## Rationale" section`)
	}
	rationale := strings.TrimSpace(rationaleSec)
	if rationale == "" {
		if form == FormKeepBase {
			return nil, errors.New(`KEEP_BASE without a reason: empty "## Rationale" section`)
		}
		return nil, errors.New(`empty "## Rationale" section`)
	}
	p := &Parsed{Form: form, Rationale: rationale, Followups: parseFollowups(sec[hFollowups])}

	diffSec, hasDiff := sec[hDiff]
	if form == FormKeepBase {
		if hasDiff {
			d, err := extractDiffFence(diffSec)
			if err == nil && strings.TrimSpace(d) != "" {
				return nil, errors.New("KEEP_BASE with a non-empty diff")
			}
		}
		p.KeepBase = true
		return p, nil
	}

	if !hasDiff {
		return nil, errors.New(`missing "## Diff" section`)
	}
	diff, err := extractDiffFence(diffSec)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(diff) == "" {
		return nil, ErrNoChange
	}
	lens, err := diffLens(diff)
	if err != nil {
		return nil, err
	}
	p.Diff, p.Lens = diff, lens
	return p, nil
}

// splitSections cuts the output into contract sections keyed by heading. A
// heading counts only at the start of a line (a diff line never starts with
// "##": context lines start with a space). The first occurrence of a heading
// wins; text before the first heading is dropped.
func splitSections(out string) map[string]string {
	sec := map[string]string{}
	var cur string
	var b strings.Builder
	flush := func() {
		if cur != "" {
			if _, seen := sec[cur]; !seen {
				sec[cur] = b.String()
			}
		}
		b.Reset()
	}
	for _, line := range strings.SplitAfter(out, "\n") {
		if h := headingOf(line); h != "" {
			flush()
			cur = h
			continue
		}
		if cur != "" {
			b.WriteString(line)
		}
	}
	flush()
	return sec
}

func headingOf(line string) string {
	t := strings.TrimSpace(line)
	for _, h := range contractHeadings {
		if strings.EqualFold(t, h) {
			return h
		}
	}
	return ""
}

// parseForm reads the first non-empty line of the Form section: the form token
// (case-insensitive; "keep_base"/"keep base" accepted for KEEP_BASE), optionally
// followed by " — why".
func parseForm(section string) (string, error) {
	line := ""
	for _, l := range strings.Split(section, "\n") {
		if t := strings.TrimSpace(l); t != "" {
			line = t
			break
		}
	}
	if line == "" {
		return "", errors.New(`empty "## Form" section`)
	}
	tok := strings.ToLower(strings.Trim(formTokenRe.FindString(line), "`*"))
	switch {
	case tok == "keep_base" || tok == "keep-base" || tok == "keep base":
		return FormKeepBase, nil
	case ladderForms[tok]:
		return tok, nil
	}
	return "", fmt.Errorf("unknown form %q (want skill|script|subagent|hook|always-on|KEEP_BASE)", line)
}

// formTokenRe takes the leading token of the Form line: a word that may
// contain - or _ ("always-on", "KEEP_BASE"), or the two-word "keep base",
// optionally wrapped in backticks or bold markers.
var formTokenRe = regexp.MustCompile("^[`*]*(?i:keep base|[a-z_-]+)[`*]*")

// hunkHeaderRe matches a unified-diff hunk header and captures the trailer
// after the closing @@.
var hunkHeaderRe = regexp.MustCompile(`^@@ -\d+(?:,\d+)? \+\d+(?:,\d+)? @@(.*)$`)

// lensRe finds the lens tag in a hunk trailer.
var lensRe = regexp.MustCompile(`(?i)\blens:\s*(procedure|knowledge)\b`)

// diffLens checks that every hunk carries a lens and returns the proposal
// lens: the single lens, or LensMixed. A diff with no hunk header at all
// cannot carry a lens and is an error.
func diffLens(diff string) (string, error) {
	seen := map[string]bool{}
	hunks := 0
	for _, line := range strings.Split(diff, "\n") {
		if !strings.HasPrefix(line, "@@") {
			continue
		}
		hunks++
		m := hunkHeaderRe.FindStringSubmatch(strings.TrimRight(line, "\r"))
		if m == nil {
			return "", fmt.Errorf("hunk %d: malformed hunk header %q", hunks, line)
		}
		lm := lensRe.FindStringSubmatch(m[1])
		if lm == nil {
			return "", fmt.Errorf("hunk %d has no lens (want \"@@ … @@ lens: procedure|knowledge\")", hunks)
		}
		seen[strings.ToLower(lm[1])] = true
	}
	switch {
	case hunks == 0:
		return "", errors.New("diff has no @@ hunk header")
	case len(seen) > 1:
		return LensMixed, nil
	}
	for l := range seen {
		return l, nil
	}
	return "", nil // unreachable: hunks > 0 implies one lens seen
}

// parseFollowups reads "- <form>: <text>" bullets. A bullet whose prefix is
// not a ladder form keeps its whole text with an empty form — follow-ups are
// advice for a human, not a contract the row can fail on.
func parseFollowups(section string) []Followup {
	out := []Followup{}
	for _, l := range strings.Split(section, "\n") {
		t := strings.TrimSpace(l)
		if !strings.HasPrefix(t, "- ") && !strings.HasPrefix(t, "* ") {
			continue
		}
		t = strings.TrimSpace(t[2:])
		if t == "" {
			continue
		}
		f := Followup{Text: t}
		if i := strings.Index(t, ":"); i > 0 {
			if form := strings.ToLower(strings.Trim(strings.TrimSpace(t[:i]), "`*")); ladderForms[form] {
				f.Form, f.Text = form, strings.TrimSpace(t[i+1:])
			}
		}
		if f.Text != "" {
			out = append(out, f)
		}
	}
	return out
}

// extractDiffFence pulls the body of the single ```diff fenced block out of
// the Diff section. No fence, more than one fence, or an unterminated fence
// is a contract violation.
func extractDiffFence(section string) (string, error) {
	const open = "```diff"
	first := strings.Index(section, open)
	if first < 0 {
		return "", errors.New("no fenced ```diff block in the Diff section")
	}
	body := section[first+len(open):]
	// The fence opener must end its line.
	if nl := strings.IndexByte(body, '\n'); nl >= 0 {
		if strings.TrimSpace(body[:nl]) != "" {
			return "", fmt.Errorf("malformed ```diff fence opener: %q", open+body[:nl])
		}
		body = body[nl+1:]
	} else {
		return "", errors.New("unterminated ```diff fence")
	}
	closeIdx := strings.Index(body, "```")
	if closeIdx < 0 {
		return "", errors.New("unterminated ```diff fence")
	}
	after := body[closeIdx+3:]
	if strings.Contains(after, open) {
		return "", errors.New("more than one ```diff block in the Diff section")
	}
	return body[:closeIdx], nil
}
