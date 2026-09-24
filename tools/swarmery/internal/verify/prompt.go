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
// {{.StrictLine}} is the ONE line the playbook verify knob (fusion phase 13)
// moves — the verdict vocabulary + the READ-ONLY contract are fixed; only the
// BAR changes (strict tightens it, normal keeps the phase-6 default). The `off`
// knob never reaches here — the service skips the run entirely.
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
{{.StrictLine}}End your reply with reason bullets and a final line exactly: VERDICT: PASS | FAIL | INCONCLUSIVE`))

// Strictness is the verify-knob bar a playbook selects for its verification run
// (fusion phase 13). It maps to playbooks' verify frontmatter minus `off`
// (which skips the run before a prompt is built).
type Strictness string

const (
	StrictnessNormal Strictness = "normal"
	StrictnessStrict Strictness = "strict"
)

// strictClause is the extra rule line injected for a strict playbook. It
// tightens the bar WITHOUT touching the fixed verdict vocabulary or the
// read-only contract — a normal run omits it entirely.
const strictClause = "- STRICT REVIEW: hold the work to a high bar — every acceptance criterion must be positively demonstrated, not merely plausible; treat any un-run behavioral check, any change outside the declared scope, or any ambiguity as a reason to withhold PASS.\n"

// promptData is the template payload.
type promptData struct {
	Title      string
	Prompt     string
	StartPoint string
	StrictLine string
}

// BuildPrompt renders the verifier prompt for a task at the given strictness.
// startPoint is the base ref the work forked from (for the "diff vs"
// instruction); when unknown, a neutral literal keeps the sentence well-formed.
// An empty/unknown strictness falls back to normal — only StrictnessStrict adds
// the tightening clause; the verdict vocabulary is invariant across the knob.
func BuildPrompt(title, prompt, startPoint string, strictness Strictness) string {
	if strings.TrimSpace(startPoint) == "" {
		startPoint = "the base branch"
	}
	strictLine := ""
	if strictness == StrictnessStrict {
		strictLine = strictClause
	}
	var b strings.Builder
	// Execution on a fixed template with string data cannot fail; a failure would
	// leave an empty prompt which the caller would still not spawn usefully —
	// ignored deliberately (belt-and-braces, unreachable).
	_ = promptTemplate.Execute(&b, promptData{
		Title:      strings.TrimSpace(title),
		Prompt:     strings.TrimRight(prompt, "\n"),
		StartPoint: startPoint,
		StrictLine: strictLine,
	})
	return b.String()
}

// focusHintHeading labels the learning loop's hint inside the contract text, so
// the verifier can tell the hint from the criteria it grades.
const focusHintHeading = "FOCUS HINT (advisory — where this run diverged from its forecast; look here first, but grade EVERY criterion above):"

// WithFocusHint appends a focus hint to a verification contract. An empty hint
// returns the contract unchanged, so every verification that carries none (all of
// them, unless surprise auto-verification is enabled) renders byte-identically.
func WithFocusHint(contract, hint string) string {
	hint = strings.TrimSpace(hint)
	if hint == "" {
		return contract
	}
	return strings.TrimRight(contract, "\n") + "\n\n" + focusHintHeading + "\n" + hint
}
