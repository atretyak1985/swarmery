package improve

import "strings"

// improvePrompt is the normative prompt contract (phase-3 plan, verbatim
// skeleton). splitDiffRationale (parse.go) enforces the two-section output
// shape it demands.
const improvePrompt = `You are improving ONE Claude Code agent definition file. Produce a MINIMAL change.

<agent-file path="{path}">{content}</agent-file>
<evidence>{bundle}</evidence>

Rules:
- Output EXACTLY two sections: "## Diff" containing ONE fenced ` + "```diff" + ` block with a valid
  unified diff against the file above (correct @@ hunk headers, a/ b/ paths), then
  "## Rationale" explaining each hunk in ≤3 sentences, citing evidence lines.
- Address ONLY problems present in the evidence. No rewrites, no restructuring, no new sections
  unless a specific evidence item demands one. Target ≤120 changed lines.
- Keep YAML frontmatter valid; name and description MUST remain within the first 15 lines.
- Vendor neutrality: never add company/product/env/repo names; use neutral placeholders.
- If the evidence does not justify any change, output "## Diff" with an empty diff block and say why.
`

// improveSkillPrompt is the phase-5 variant for a SKILL.md target. It differs
// from improvePrompt in the three places where a skill is not an agent:
//
//   - the ASK is to edit the PROCEDURE so the next run reads the lesson, not to
//     tune an agent's persona;
//   - the SCOPE is hard — the diff may touch this one file and nothing else. A
//     skill's sibling resources/*.md look editable and are not: the apply gate
//     rejects the whole proposal if one is touched, so the model is told before
//     it wastes a run;
//   - the FRONTMATTER contract is stated explicitly, because a SKILL.md's
//     frontmatter carries more keys (version, owner, color, docs) than an
//     agent's and a model "tidying" it would fail the gate.
const improveSkillPrompt = `You are improving ONE Claude Code SKILL.md procedure file. Produce a MINIMAL change.

<skill-file path="{path}">{content}</skill-file>
<evidence>{bundle}</evidence>

The evidence is a lesson the fleet learned in at least three separate tasks. It was written down
each time and never made it into the procedure the next run reads. Your job is to put it there.

Rules:
- Output EXACTLY two sections: "## Diff" containing ONE fenced ` + "```diff" + ` block with a valid
  unified diff against the file above (correct @@ hunk headers, a/ b/ paths), then
  "## Rationale" explaining each hunk in ≤3 sentences, citing evidence lines.
- Edit the PROCEDURE: the rules, steps, or checks a run follows. Do not restructure the document,
  do not add sections the evidence does not demand, do not rewrite prose that already works.
  Target ≤120 changed lines.
- The diff may touch ONLY {path}. A diff that also touches a sibling resources/ file, a manifest,
  or any other path is rejected whole — fold what you need into this file instead.
- Keep the YAML frontmatter valid and intact: leading ---, and name: plus description: within the
  first 15 lines. Do not reorder or drop existing frontmatter keys.
- Vendor neutrality: never add company/product/env/repo names; use neutral placeholders.
- If the evidence does not justify any change, output "## Diff" with an empty diff block and say why.
`

// renderPrompt fills the improvePrompt placeholders.
func renderPrompt(path, content, bundle string) string {
	return fillPrompt(improvePrompt, path, content, bundle)
}

// renderSkillPrompt fills the improveSkillPrompt placeholders.
func renderSkillPrompt(path, content, bundle string) string {
	return fillPrompt(improveSkillPrompt, path, content, bundle)
}

func fillPrompt(tmpl, path, content, bundle string) string {
	return strings.NewReplacer(
		"{path}", path,
		"{content}", content,
		"{bundle}", bundle,
	).Replace(tmpl)
}
