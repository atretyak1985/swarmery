# The agent workflow

swarmery's `core` isn't a single chatbot — it's an **orchestrated fleet**. `@tech-lead`
is the agent you invoke directly; it understands the task, surfaces the questions only
you can answer, routes the work to executors sized to the job, gates the result behind
an independent review, and closes with a summary in your workspace. This is what the
control plane's session **Timeline** is showing you: delegations, review verdicts, and
the tool calls each delegate makes.

Since core 3.0 the fleet is **13 judgment-style agents** (down from 42 rule-machines);
each agent's role is a page, not a rulebook, and the "how" lives in progressively
disclosed skills. The old→new mapping is in `plugins/core/AGENTS.md`.

## Routing by size (judgment, not ceremony)

| Task shape | Route |
|---|---|
| **Bug** | `@debugger` root-causes BEFORE anyone plans a fix |
| **Small** — single file, well understood | `@implementation-agent` with one focused brief |
| **Medium** — a few files, needs sequencing | `@planner` writes a short plan → 2–3 executors run it |
| **Large** — multi-repo, schema changes | `@architect` designs first; native dynamic-workflow orchestration for codebase-wide fan-out |

Every route ends the same way: an independent read-only **`@code-reviewer`** pass over
the diff before commit (plus `@security-auditor` when the change touches auth, input
handling, secrets, or infra), then closing artifacts via the `session-closeout` skill.

## The fleet (13)

| Agent | Class | What it does |
|---|---|---|
| `@tech-lead` | orchestrator | routes, delegates, gates, closes |
| `@planner` | planning | workspace plans: phase docs, criteria, executor prompts |
| `@architect` | design | system/API/data design, migration safety, rollout |
| `@researcher` | read-only | tech evaluation, impact analysis, context synthesis |
| `@implementation-agent` | executor | code execution (leaf) or plan-execution orchestration |
| `@ui-developer` | executor | frontend with type/token/a11y/state gates |
| `@debugger` | executor | root cause first, minimal proven fix |
| `@test-writer` | executor | behavior-pinning tests |
| `@test-runner` | read-only | run suites, report faithfully |
| `@code-reviewer` | read-only | multi-lens review, `VERDICT:` line |
| `@security-auditor` | read-only | OWASP + STRIDE with attack paths |
| `@verification-agent` | read-only | deterministic checks (build/type/lint/test) |
| `@system-improver` | meta | evidence-cited diagnosis of the whole system |

Read-only agents carry `tools:` allowlists without Edit/Write. Verdict-emitting agents
end with the machine grammar `VERDICT: PASS | FAIL | INCONCLUSIVE` — the same line the
control plane's verify engine parses.

## Model tiers (the cost ladder)

Each agent runs on the cheapest tier that fits — visible on the dashboard's **Health → Cost & tokens** tab and
in each session's cost header. Models are always aliases (`opus`/`sonnet`/`haiku`), so
model rotations never strand the fleet.

| Tier | Model | Role |
|---|---|---|
| **T0/T1** | Opus | orchestration, planning, design, review judgment |
| **T2** | Sonnet | fleet default — execution, debugging, research |
| **T3** | Haiku | fast mechanical checks and reporting |

## Human-in-the-loop gates

The workflow pauses for you — surfaced in the control plane's **Inbox** —
before: git commits/pushes · database migrations · breaking API changes ·
security-sensitive changes · production deployments. Risky actions get a structured
go/no-go from the `guardrails` skill (Impact × Reversibility; Critical never
auto-approves). Autonomous runs resolve user-only questions BEFORE the fan-out starts.
These gates always wait for you: a triage agent may label the classifier's guesses and
close purely informational items from the Inbox (undoable for seven days), but it never
answers an approval, an agent change or an alert.

## Where this lives

`@tech-lead` and every delegate ship in `core` — enable it and the workflow is
available in any project. Project-local agents with the same name override the core
ones (see [extending](extending)); per-project flavor comes from `project.json` (see
[neutrality](neutrality)).
