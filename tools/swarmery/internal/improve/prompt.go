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

// renderPrompt fills the improvePrompt placeholders.
func renderPrompt(path, content, bundle string) string {
	return strings.NewReplacer(
		"{path}", path,
		"{content}", content,
		"{bundle}", bundle,
	).Replace(improvePrompt)
}
