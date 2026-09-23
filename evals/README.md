# swarmery evals

Golden regression tests for critical core agents (promptfoo). Editing
`plugins/core/agents/*.md` must not silently break routing, output contracts,
or vendor-neutrality — these tests make such breakage fail loudly.

```bash
cd evals
export ANTHROPIC_API_KEY=…            # runners (haiku + opus) + judge (sonnet)
npx promptfoo@latest validate config  # config-only, no API calls, costs nothing
npx promptfoo@latest eval             # run the suite (spends tokens)
npx promptfoo@latest view             # inspect results in the browser
```

## Provider tiers

Each suite runs on the tier matching the `model:` frontmatter of the agent it
exercises — an opus-tier output contract only measured on Haiku is not measured.

| Label        | Model                       | Settings                                                 | Suites                                                                                                              |
| ------------ | --------------------------- | -------------------------------------------------------- | ------------------------------------------------------------------------------------------------------------------- |
| `opus-tier`  | `claude-opus-5-5`           | `max_tokens: 16000`, `effort: high`, no `thinking` block | tech-lead, planner, implementation-agent, code-reviewer, security-auditor, jira-task-runner, no-reasoning-in-output |
| `haiku-tier` | `claude-haiku-4-5-20251001` | `max_tokens: 1024`                                       | verification-agent, git-commit, guardrails                                                                          |

Every test case declares its tier with `providers: [<label>]`. A case that
declares none runs on **both** — costly, never silently wrong — and an unknown
label aborts the run at load time. Skills (git-commit, guardrails) carry no
`model:` of their own and stay on the cheap tier.

Opus 5.5 runs adaptive thinking when `thinking` is omitted, and thinking tokens
come out of `max_tokens` — hence 16000, not the 1024 that serves Haiku. `effort`
reaches the API as `output_config.effort`; `high` mirrors the provisional value
those agents carry in frontmatter today and is **not** a swept result.

Two authoring constraints worth knowing before adding assertions:

- `regex` / `not-regex` values are compiled with `new RegExp(value)` and **no
  flags**. An inline `(?i)` / `(?m)` prefix is a `SyntaxError`, which promptfoo
  scores as a _failed_ assertion — so it silently turns a hard contract into a
  permanent red. Use `(^|\n)` for `^`-per-line and character classes for case
  tolerance.
- promptfoo 0.121.20 predates Opus 5.5 and logs
  `Using unknown Anthropic model: claude-opus-5-5`. It is warn-only — the id is
  sent to the API unchanged — but promptfoo's own cost column may not price it.

## What's covered (core 3.0)

- **tech-lead** — routing sanity (schema work → architect, bug → debugger first),
  the reviewer-before-commit gate, read-only investigation mode, consulting
  `project.json`/CLAUDE.md instead of assuming a stack, and the 7-cell ledger row.
- **planner** — plan-format contract: `phase-N-<slug>.md` naming, the parseable
  sequencing table, the `## Completion Report` stub, ask-user-first discipline.
- **verification-agent / security-auditor** — the machine verdict grammar
  (`VERDICT: PASS | FAIL | INCONCLUSIVE`) plus honest NOT RUN reporting and
  OWASP finding quality.
- **implementation-agent** — leaf-executor scope discipline and the
  tick-then-Completion-Report progress contract.
- **git-commit skill** — conventional format with scopes from `project.json → commitScopes`.
- **guardrails skill** — APPROVED/REJECTED contract, read-only short-circuit,
  critical actions never auto-approved.
- **jira-task-runner** (jira-pack) — no tracker writes on failed runs;
  needs-info vs cannot-reproduce.

## Added in core 3.6 (phase 5 contracts)

- **code-reviewer** — blockers-only output: merge-blocking findings ranked
  `P0`/`P1` with `file:line` and a way to show the failure, cosmetic nits
  demoted to at most one line marked non-blocking, zero findings stated plainly
  instead of padded, and exactly one trailing `VERDICT:` line with nothing
  after it.
- **tech-lead** — the delegation contract: a fan-out closes with one
  `| agent | claim | evidence | accepted? |` table whose accept/reject calls
  follow the evidence (a file existing on disk is not evidence), and every
  delegate brief ends with the `Unverified:` closing ask.
- **no-reasoning-in-output** — the narration that
  `templates/verbose-instructions.md` used to demand ("Plan:" / "Reasoning:" /
  "Step N: what I'm doing now", and any leaked `<thinking>`-style tag) must not
  return to tech-lead, implementation-agent, or code-reviewer. The tier sets
  `showThinking: false`, so these assertions see the visible answer only.

## Growing the corpus

Every real routing bug or contract regression should become a test case here
(same philosophy as unit tests). Prefer `contains`/`regex` for hard contracts and
`llm-rubric` for judgment calls.

## CI

Not wired into CI yet — the suite needs an API key and costs tokens. Wire it as a
manual/nightly job once the corpus stabilizes; the structural checks in
`.github/workflows/ci.yml` (JSON, bash -n, frontmatter, neutrality scan) stay on every push.
