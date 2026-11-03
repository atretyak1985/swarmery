package probe

// The blind judge's prompt (D10). The judge sees the task and two answers
// labelled A and B — nothing else: no file names, no agent names, no timing,
// no cost, no word that says which side is the edited one. Which answer sits
// at A is a coin flip per case (Service.flip).
//
// The comparison order and the "keeping the unmodified version is a valid,
// expected outcome" framing are adapted from google-research/harness2
// (Apache 2.0) — its blind A/B check of an edited harness against the
// original.

import (
	"errors"
	"regexp"
	"strings"
)

// JudgePreamble opens every judge prompt; ingest-side filters can key on it.
const JudgePreamble = "You are comparing two answers to the same task."

const judgeTemplate = JudgePreamble + `

Both answers were written by an assistant working from the same instructions,
or from two slightly different versions of them. One of them may come from
the instructions as they were before any edit. Preferring that one, or calling
the two equal, is a valid and expected result — do not assume a change is an
improvement.

Decide in this order, and stop at the first point that separates them:
1. Delivery — does the answer do what the task asked?
2. Fidelity — does it stay within what the task and its instructions allow,
   without inventing facts, steps or results?
3. Internal consistency — does it agree with itself?
4. Usable as-is — could the person who asked act on it without fixing it?
5. Finish — clarity, structure, length.

Judge only the text below. Its order on the page means nothing.

<task>
{{TASK}}
</task>

<answer id="A">
{{A}}
</answer>

<answer id="B">
{{B}}
</answer>

Reply with at most 6 short lines of reasoning, then one final line that is
exactly one of:
CHOICE: A
CHOICE: B
CHOICE: TIE`

// RenderJudgePrompt fills the template. Plain replacement, in one pass, so
// text inside the task or an answer that looks like a placeholder stays text.
func RenderJudgePrompt(task, a, b string) string {
	return strings.NewReplacer("{{TASK}}", strings.TrimSpace(task), "{{A}}", strings.TrimSpace(a),
		"{{B}}", strings.TrimSpace(b)).Replace(judgeTemplate)
}

// maxReasonLines bounds the stored reason (D10: ≤6 lines).
const maxReasonLines = 6

var choiceRe = regexp.MustCompile(`(?i)^\s*\**\s*CHOICE\s*:\s*\**\s*(A|B|TIE)\s*\**\s*$`)

// ParseVerdict reads the LAST "CHOICE:" line and returns A, B or TIE plus the
// reasoning above it, trimmed to maxReasonLines non-empty lines.
func ParseVerdict(raw string) (choice, reason string, err error) {
	lines := strings.Split(strings.ReplaceAll(raw, "\r\n", "\n"), "\n")
	at := -1
	for i := len(lines) - 1; i >= 0; i-- {
		if m := choiceRe.FindStringSubmatch(lines[i]); m != nil {
			choice, at = strings.ToUpper(m[1]), i
			break
		}
	}
	if at < 0 {
		return "", "", errors.New("no CHOICE: A|B|TIE line")
	}
	var kept []string
	for _, l := range lines[:at] {
		if l = strings.TrimSpace(l); l != "" {
			kept = append(kept, l)
		}
	}
	if len(kept) > maxReasonLines {
		kept = kept[len(kept)-maxReasonLines:]
	}
	return choice, clip(strings.Join(kept, "\n"), 1200), nil
}
