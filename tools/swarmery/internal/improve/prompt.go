package improve

import "strings"

// improvePrompt is the normative prompt contract (phase-3 plan, verbatim
// skeleton). parseOutput (parse.go) enforces the output shape it demands —
// see outputContract.
var improvePrompt = expandShared(`You are improving ONE Claude Code agent definition file. Produce a MINIMAL change.

<agent-file path="{path}">{content}</agent-file>
<evidence>{bundle}</evidence>

{form}
Rules:
- Address ONLY problems present in the evidence. No rewrites, no restructuring, no new sections
  unless a specific evidence item demands one. Target ≤120 changed lines.
- Keep YAML frontmatter valid; name and description MUST remain within the first 15 lines.
- Vendor neutrality: never add company/product/env/repo names; use neutral placeholders.
{output}`)

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
var improveSkillPrompt = expandShared(`You are improving ONE Claude Code SKILL.md procedure file. Produce a MINIMAL change.

<skill-file path="{path}">{content}</skill-file>
<evidence>{bundle}</evidence>

The evidence is a lesson the fleet learned in at least three separate tasks. It was written down
each time and never made it into the procedure the next run reads. Your job is to put it there.

{form}
Rules:
- Edit the PROCEDURE: the rules, steps, or checks a run follows. Do not restructure the document,
  do not add sections the evidence does not demand, do not rewrite prose that already works.
  Target ≤120 changed lines.
- The diff may touch ONLY {path}. A diff that also touches a sibling resources/ file, a manifest,
  or any other path is rejected whole — fold what you need into this file instead.
- Keep the YAML frontmatter valid and intact: leading ---, and name: plus description: within the
  first 15 lines. Do not reorder or drop existing frontmatter keys.
- Vendor neutrality: never add company/product/env/repo names; use neutral placeholders.
{output}`)

// formSection is the FORM part both prompts share (harness² phase 3, D7).
//
// Attribution: the form ladder and the cut-to-pay rule follow the Harness²
// finding the plan builds on — most of a harness improvement's gain comes from
// putting a change in the RIGHT PLACE (a skill, a script, a subagent, a hook)
// rather than from writing more always-on text. The rewriter edits exactly one
// file, so a change whose right form is out of its reach is reported as a
// follow-up for a human instead of being inlined here as prose.
const formSection = `# FORM
Most of the gain comes from putting a change in the right place, not from writing more text.
Choose the FIRST rung of this ladder that fits the problem the evidence shows:
1. skill — a recognisable trigger starts a procedure: the procedure belongs in a SKILL.md.
2. script — a deterministic step a run keeps doing by hand, or doing wrong: it belongs in a script
   under the owning plugin's bin/.
3. subagent — a large input that boils down to a small answer: it belongs in a subagent.
4. hook — mechanical, recurring, and must not be skippable: it belongs in a hook.
5. always-on — text in an agent definition or CLAUDE.md, read on every run. LAST resort.
Cut to pay: if your diff adds always-on lines it must remove at least as many, or justify the net
growth in one sentence in the Rationale.
You edit ONE file, {path}. A change whose right form is outside that file goes under
"## Follow-ups" — never inline it here as prose.
KEEP_BASE is a real answer: when the evidence does not justify a change to this file, answer
KEEP_BASE with no diff and one paragraph saying why.
`

// outputContract is the output shape both prompts demand; parseOutput
// (parse.go) enforces it strictly (D8): a missing FORM, a hunk without a lens,
// or a KEEP_BASE without a reason fails the row (status 'failed', retriable).
const outputContract = `
Output these sections, in this order:
- "## Form" — one line: the form of your change (skill | script | subagent | hook | always-on),
  or KEEP_BASE, then " — " and one sentence why.
- "## Diff" — ONE fenced ` + "```diff" + ` block with a valid unified diff against the file above
  (correct @@ hunk headers, a/ b/ paths). Tag EVERY hunk with its lens after the closing @@,
  e.g. "@@ -10,3 +10,4 @@ lens: procedure". procedure = how to act (steps, rules, checks);
  knowledge = what is true (facts, paths, conventions). Leave this section out for KEEP_BASE.
- "## Rationale" — each hunk in ≤3 sentences, citing evidence lines; for KEEP_BASE, one paragraph
  saying why the file already does the job.
- "## Follow-ups" (optional) — one bullet per change outside your reach:
  "- <skill|script|subagent|hook>: <what, and where it belongs>".
`

// expandShared splices the shared FORM and output-contract text into a prompt
// template once, at init, so the per-call fill is a single replacer pass (and
// {path} inside the shared text is filled like any other occurrence).
func expandShared(tmpl string) string {
	return strings.NewReplacer("{form}", formSection, "{output}", outputContract).Replace(tmpl)
}

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
