# The agent workflow

swarmery's `core` isn't a single chatbot — it's an **orchestrated fleet**. `@tech-lead`
is the only agent you invoke directly; it drives a **9-phase workflow** (Understanding →
Documentation) by delegating to specialized executor agents and gating risky steps back to
you. This is what the control plane's session **Timeline** is showing you: phase transitions,
parallel groups, and the tool calls each delegate makes.

> Methodology background: the [Agentsway paper](https://arxiv.org/html/2510.23664v1).

## Activation modes (chosen before Phase 1)

`@tech-lead` sizes the task first and runs only the phases that scope warrants:

| Mode | Scope | Phases |
|---|---|---|
| **Micro** | <30 LOC, <30 min, single file | 1 · 3.6 · 4 · 5 (verify only) · 8+9 · 10 |
| **Sprint** *(default)* | 30–500 LOC, <8h | all phases |
| **Full** | >500 LOC, monorepo, schema changes | all phases **+ 3.5 Design** |
| **Dynamic** | codebase-wide audit / migration / "from every angle" | event-driven gates; fan out 10s–100s of subagents with adversarial verification |

## The phases

| Phase | Owner / delegates | What happens |
|---|---|---|
| **1 Understanding** | `@tech-lead` | Gap analysis — partitions unknowns into Known / Unknown-codebase / Unknown-research / Unknown-user. **User-only gaps must be resolved before Phase 3.** |
| **2 Context** | parallel trio: `@context-gatherer` · `@tech-researcher` · `@downstream-analyzer` | Gather code + research context for the gaps found in Phase 1. |
| **3 Planning** | `@task-planner` (<1 wk) or `@implementation-planner` (>1 wk) | Break the task into phased step files with acceptance criteria. |
| **3.5 Design** | `@architecture-designer` · `@api-designer` · `@database-designer` · `@ui-designer` | *Full mode only.* Produce contracts the implementer consumes. |
| **3.6 Pre-mortem** | `@tech-lead` | Self-correction: Risk / Likelihood / Impact / Mitigation table; iterate the plan at least once. |
| **4 Implementation** | `@implementation-agent` (or a specialist) | Execute the plan against the correct repo. |
| **5 Quality gate** | parallel quartet: `@verification-agent` · `@quality-checker` · `@security-auditor` · `@contract-validator` | Build/typecheck/lint/test, LLM-as-judge review, security, contract tracing. |
| **6 Downstream** | `@downstream-analyzer` (edit-capable) | Fix callers, tests, and imports the change affected. |
| **7 Tracking** | `@tech-lead` | Update task state and the delegation log. |
| **8 + 9 Closing** | parallel pair: `@summary-generator` · `@retrospective-agent` | Canonical `SUMMARY.md`; lessons-learned + bias check. |
| **10 Documentation** | `@task-documenter` | Structured phase files, manifest, indexes. |

Independent phases run **in parallel, launched in a single message** (the Phase 2 trio, Phase 5
quartet, Phase 8+9 pair). Dependent phases never parallelize (3→2, 4→3, 6→4, 10→8+9).

## Model tiers (the cost ladder)

Each agent runs on the cheapest tier that fits the job — you can see the mix on the **Analytics**
page and in each session's cost header.

| Tier | Model | Role |
|---|---|---|
| **T0** | Opus | orchestrator (`@tech-lead`) — never bulk-executes; ~5–10% of task tokens |
| **T1** | Opus | complex reasoning / judgment (incl. pinned `@security-auditor`) |
| **T2** | Sonnet | fleet default — design, analysis, most execution |
| **T3** | Haiku | fast mechanical checks, context gathering, commit messages |

Escalate one tier after **two** quality-gate failures on the same subtask; never auto-jump to T0.

## Human-in-the-loop gates

The workflow pauses for you — surfaced in the control plane's **Approvals** queue — before:
git commits/pushes · database migrations · breaking API changes · security-sensitive changes ·
production deployments. In Dynamic mode, subagents run non-interactively (`acceptEdits`) and
**cannot** prompt, so every user-only gap must be resolved in Phase 1 before the fan-out starts.

## Where this lives

`@tech-lead` and every delegate ship in `core` — enable it and the workflow is available in any
project. Project-local agents with the same name override the core ones (see
[extending](extending)); per-project flavor comes from `project.json` (see [neutrality](neutrality)).
