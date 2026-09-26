---
name: <agent-name>
description: <one-line trigger description — what work this agent should handle; this is the routing signal, make it distinct from every sibling agent>
model: <opus | sonnet | haiku>            # ALIASES ONLY — pinned ids fail the reference-integrity CI gate
# Rationale: <why this model — what reasoning, cost, or speed property is needed>
effort: <low | medium | high | xhigh | max>   # CI-checked closed set; omit for haiku
color: <purple | blue | cyan | green | yellow | orange | teal | red | pink>
maxTurns: <number>                        # optional; omit for system default
memory: project                           # optional — only when the agent must learn across sessions
isolation: worktree                       # optional — for editors that touch many files
tools: Read, Glob, Grep, Bash, TodoWrite  # REQUIRED for read-only roles (no Edit/Write/NotebookEdit); omit for editors
skills:
  - <skill-name>                          # must exist in this plugin (packs may also use core skills) — CI-checked
docs:
  status: draft
  updated: <YYYY-MM-DD>
# Never add: permissionMode / autonomy / owner / version — Claude Code ignores
# them on plugin subagents and the CI whitelist rejects them. Ownership
# metadata lives in plugins/core/AGENTS.md.
---

# Role

<2–4 sentences. Who this agent is, when it's invoked, what it returns, and
what it must NOT do. Judgment over rules: give the agent its goal and its
boundaries, not a phase machine.>

# <Operating section — name it for the work>

<The 3–6 rules that actually matter, each earning its place: output contracts
other systems parse (state them in ≤3 lines and point to the owning skill —
e.g. verdict-emitting agents end with the exact final line
`VERDICT: PASS | FAIL | INCONCLUSIVE`, the only grammar the platform's verify
engine reads), hard safety boundaries, and the honesty rules ("report checks
as they ran", "tag unverified claims [LOW-CONFIDENCE]"). Everything
procedural — checklists, templates, worked processes — belongs in a skill
with resources/, loaded on demand, not here. Before adding any
"always/never/minimum N" line, answer: what breaks if it's absent? If the
answer is "nothing, current models handle it", leave it out.>

<BUDGET: the instruction body above `# How to use` stays ≤500 words. If you
need more, you are writing a skill, not an agent.>

# How to use

## What it does

<2–3 sentences a user reads to know what they get. Required subsection — the
docs-coverage CI gate checks all four, each ≥40 characters.>

## When to use it

- <trigger 1>
- <trigger 2>

## When not to use it

- <the neighboring agent or skill to prefer, and when>

## How to invoke

```
@<plugin>:<agent-name> <example invocation>
```

## What you get back

<The deliverable and its form — artifacts, verdict lines, edited files.>

## Worked example

```
<one realistic invocation and a condensed realistic response>
```

## Related

- `@<plugin>:<sibling>` — <one line on the boundary between them>
