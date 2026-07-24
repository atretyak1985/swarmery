package verify

import (
	"strings"
	"text/template"
)

// promptTemplate is the read-only verifier prompt (phase-6 spec — normative
// skeleton, verbatim). text/template so {{.Title}}/{{.Prompt}}/{{.StartPoint}}
// interpolate without format-bug risk; the literal wording is fixed. The
// contract is READ-ONLY: the verifier may build/test/read but must not mutate
// git state, and INCONCLUSIVE (not FAIL) is the answer when it cannot conclude.
var promptTemplate = template.Must(template.New("verify").Parse(
	`You are a read-only verification agent. You are in a git worktree containing completed work for this task.
TASK: {{.Title}}
ACCEPTANCE CRITERIA / ORIGINAL CONTRACT:
{{.Prompt}}

Rules:
- READ ONLY: you may run builds/tests/linters and read any file; you MUST NOT edit files, commit, or mutate git state.
- Judge only whether the acceptance criteria are met by the work on this branch (diff vs {{.StartPoint}}).
- Behavioral criteria default to FAIL unless you can confirm the behavior by running the relevant command/test here.
- If you cannot run what's needed to conclude (missing deps, broken env), the answer is INCONCLUSIVE — not FAIL.
End your reply with reason bullets and a final line exactly: VERDICT: PASS | FAIL | INCONCLUSIVE`))

// promptData is the template payload.
type promptData struct {
	Title      string
	Prompt     string
	StartPoint string
}

// BuildPrompt renders the verifier prompt for a task. startPoint is the base
// ref the work forked from (for the "diff vs" instruction); when unknown, a
// neutral literal keeps the sentence well-formed.
func BuildPrompt(title, prompt, startPoint string) string {
	if strings.TrimSpace(startPoint) == "" {
		startPoint = "the base branch"
	}
	var b strings.Builder
	// Execution on a fixed template with string data cannot fail; a failure would
	// leave an empty prompt which the caller would still not spawn usefully —
	// ignored deliberately (belt-and-braces, unreachable).
	_ = promptTemplate.Execute(&b, promptData{
		Title:      strings.TrimSpace(title),
		Prompt:     strings.TrimRight(prompt, "\n"),
		StartPoint: startPoint,
	})
	return b.String()
}
