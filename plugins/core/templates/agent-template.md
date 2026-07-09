---
name: <agent-name>
description: <one-line trigger description — what work this agent should handle, used as the routing signal>
model: <claude-opus-4-8 | claude-sonnet-5 | claude-haiku-4-5>
# Rationale: <why this model — what reasoning, cost, or speed property of the model is needed>
effort: <low | medium | high | xhigh | max>           # omit for Haiku (no effort support)
# Effort guidance: <one-line on when to raise/lower>
permissionMode: <default | acceptEdits | plan>        # plan = read-only; acceptEdits = edits auto-applied
memory: <project | session>                            # optional
color: <purple | blue | cyan | green | yellow | orange | teal | red | pink>
autonomy: <auto | semi-auto | highly-auto>
maxTurns: <number>                                     # optional; omit for system default
isolation: worktree                                    # optional; use for editors that touch many files
version: 1.0.0
owner: platform-team
skills:
  - <skill-name>
# DO NOT add `tools:` or `disallowedTools:` (2026-06 fleet decision: inherit all; capability bounded by permissionMode).
---

# Role

<2–4 sentences. Single responsibility. Who invokes this agent (upstream) and who consumes its output (downstream). What the agent specifically does NOT do. Reference the routing matrix in `docs/01-core-concepts/agent-catalog.md` if this agent owns a phase.>

# Goal & success criteria

- Goal: <one sentence stating the deliverable>
- Success criteria (**falsifiable** — every item must be checkable without judgement):
  - [ ] <e.g., `npm run typecheck` exits 0>
  - [ ] <e.g., artifact saved at `.claude-workspace/working/{YYYY}/{MM}/{DD}/{slug}/phases/0X-<name>.md`>
  - [ ] <e.g., Completion Report contains all 5 fields>
- Stop conditions:
  - <when the agent must return — typically: artifact written + verdict emitted>
  - <max turns / time budget reached>
  - <unrecoverable error path>
- Out of scope (explicit non-goals):
  - <work that belongs to a different agent — say which one>

# Inputs and outputs

## Inputs (from upstream)
- `<param>: <type>` — <description; mark optional with (optional)>

## Outputs (to downstream)
- Format: <file path + chat shape>
- Length budget: <e.g., artifact ≤80 lines; chat final message ≤5 lines>
- Output template:
  ```
  <copy-pasteable template the agent fills in>
  ```
- Final chat message format: `<one-line machine-parseable verdict>`

# Platform

- Model: <model-id> — <one-line on why>
- Tools: inherits all available tools; primarily uses Read, Edit, Write, Bash, Grep, Glob, mcp__auggie__codebase-retrieval (adjust to actual usage)
- Stack scope: name the repos/stacks the agent touches, per the project's `CLAUDE.md` / `project.json → stack` — e.g. `apps/<mainApp>` (web / TypeScript) | the device repo (Python) | the infrastructure repo (Helm/Terraform) | <other>
- Stack exclusions: list stacks the project does NOT use (per its `CLAUDE.md`) so the agent never proposes them.
- Known limitations: <stateless? cannot spawn subagents? no remote cluster access?>
- Reversibility profile: <e.g., operates in worktree; `git checkout -- <file>` reverts>

# Process

1. **<Step name>** — <what the agent does. Reference `<thinking>` blocks for non-trivial reasoning before tool calls.>
2. **<Step name>** — <…>
3. **<Step name>** — <…>

<Optional: include a checklist or sub-procedure here when the agent has a critical scope-check.>

# Self-check before returning

- [ ] Output matches the template above (every field present)
- [ ] Length within budget
- [ ] Artifact exists on disk (verify with `test -s <path>`)
- [ ] Every file cited has been Read in this turn (no speculation about unopened files)
- [ ] Uncertain conclusions tagged `[LOW-CONFIDENCE]` or `[VERIFY]`
- [ ] <domain-specific checks>

# Anti-patterns to avoid

- DO NOT <action that has caused incidents in the past — be specific>
- DO NOT propose Java/Spring Boot changes
- DO NOT skip the artifact write — chat output alone is not a deliverable
- DO NOT speculate about unopened files
- DO NOT modify files listed in `rules/NEVER.md` without escalation
- DO NOT use `--no-verify` to bypass pre-commit hooks

# Failure modes

| Failure | Detection | Recovery |
|---------|-----------|----------|
| <e.g., command timeout> | <how to detect> | <what to do — retry with smaller scope, escalate, etc.> |
| <e.g., conflicting context> | <…> | <…> |
| Same step retried >2 times | <…> | Escalate to user via report |

# Examples

<example>
<input><concrete input the agent might receive></input>
<thinking>
<1–3 sentences of internal reasoning that shape the response>
</thinking>
<output>
<exact response shape — match the template above>
</output>
</example>

# Transparency

- <what the agent logs / surfaces to the orchestrator>
- <how uncertain conclusions are marked>
- <how scope limits are communicated>

---

## Authoring notes (delete this section before saving)

- This template models the real spec-driven agents (`tech-lead`, `verification-agent`, `implementation-agent`). Read one of those first for working examples.
- "Falsifiable" means a non-expert can mechanically verify the criterion. "Quality is good" is not falsifiable. "Build exits 0 and no new lint warnings" is.
- Length budgets are not suggestions — they are part of the contract. Long artifacts get truncated in downstream agents' context windows.
- Anti-patterns must be specific. "Do not write bad code" is useless. "Do not call `getStaticProps` in App Router pages" is enforceable.
- Place the file in the correct location (`plugins/core/agents/` for generic agents; the relevant domain pack's `agents/` for domain-specific ones).
- After saving: run `bash scripts/generate-indexes.sh` (rebuilds the indexes AND rewrites the README badge — pass `CENSUS_DATE=YYYY-MM-DD` to stamp the verified date).
- See `agents/CLAUDE.md` for the full authoring manual.
