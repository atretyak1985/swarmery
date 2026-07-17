# Aggregated Audit Checklists — Agentic Architecture & Orchestration

Run these against the implementation under review. For the reasoning behind any item, read the full module file.

## 1.1 Agentic Loops
Full module: `references/1-1-agentic-loops.md`

- [ ] Loop termination branches on `stop_reason`, not on natural-language parsing, text-content checks, or iteration counts.
- [ ] The loop continues on `stop_reason == "tool_use"` and terminates on `stop_reason == "end_turn"`.
- [ ] Tool results are appended to conversation history before the next request is sent, so the model can reason over them.
- [ ] `stop_reason` values beyond `tool_use` and `end_turn` — `pause_turn`, `max_tokens`, `stop_sequence`, `refusal` — are handled explicitly, not treated as `tool_use` by default.
- [ ] Any value other than `end_turn` is treated as "not finished, check why" rather than assumed complete.
- [ ] Tool selection is model-driven, except where business logic (financial, security, regulatory) requires deterministic programmatic enforcement.
- [ ] Iteration caps, if present, serve only as a runaway safety net — never as the primary stopping mechanism.
- [ ] Completion is never inferred from `response.content[0].type == "text"`, since text can accompany a `tool_use` block.

## 1.2 Multi-Agent Orchestration
Full module: `references/1-2-multi-agent-orchestration.md`

- [ ] All inter-subagent communication routes through the coordinator; there is no direct subagent-to-subagent (peer) messaging.
- [ ] Every subagent invocation receives all context it needs explicitly in its prompt — nothing relies on inherited conversation history, shared memory, or global state.
- [ ] Repeated invocations of the same subagent do not assume knowledge from prior invocations (each invocation is treated as independent).
- [ ] The coordinator selects subagents dynamically per query rather than routing every query through the full pipeline.
- [ ] Research scope is partitioned across subagents so they cover distinct subtopics or source types without duplicating work.
- [ ] The coordinator runs iterative refinement — it evaluates synthesis output for gaps and re-delegates until coverage is sufficient, rather than single-shot.
- [ ] Failure diagnosis traces incomplete or incorrect output to its origin (usually coordinator decomposition or the context passed in), not to the subagent that emitted it.
- [ ] Task decomposition covers the full breadth of the topic, so no whole categories are silently omitted (scope gaps, not depth gaps).

## 1.3 Subagent Invocation and Context Passing
Full module: `references/1-3-subagent-invocation-and-context-passing.md`

- [ ] The coordinator's `allowedTools` includes `"Task"` (or `"Agent"`) — without it the coordinator cannot spawn any subagent.
- [ ] Each subagent has an AgentDefinition specifying description, system prompt, and role-scoped tool restrictions.
- [ ] The coordinator passes complete findings from prior agents in full — subagents do not rely on "looking up" prior results.
- [ ] Inter-agent payloads use a structured format that carries metadata (source URL, document name, page number) alongside content, so downstream agents can attribute claims to sources.
- [ ] Coordinator prompts specify goals and quality criteria, not step-by-step procedures.
- [ ] Independent subagent tasks are spawned in parallel via multiple Task calls in a single response, not sequentially across turns.
- [ ] `fork_session` is used for divergent exploration from a shared baseline, and `--resume` for continuing a named session — the two are not conflated.
- [ ] Unsourced-output bugs are traced to coordinator context passing, not misattributed to the synthesis agent's prompt or fixed by granting it direct tool access.

## 1.4 Workflow Enforcement and Handoff
Full module: `references/1-4-workflow-enforcement-and-handoff.md`

- [ ] High-stakes workflow ordering (financial, security, compliance operations) is enforced programmatically via hooks or prerequisite gates, not through system-prompt instructions alone.
- [ ] Prompt-based guidance is reserved for low-stakes operations (formatting, style, output ordering) where a non-zero failure rate is acceptable.
- [ ] Prerequisite gates are implemented in code and return a blocking error until the prior condition is met (e.g. `process_refund` blocked until `get_customer` returns a verified customer ID).
- [ ] A gate cannot be bypassed by the model choosing to skip the prerequisite — a direct call to the gated tool is rejected, not silently allowed.
- [ ] SubagentStart / SubagentStop lifecycle hooks are used wherever subagent invocations or outputs need to be logged, validated, rate-limited, or transformed.
- [ ] Subagent-scoped PreToolUse/PostToolUse hooks intercept only that subagent's tool calls, enabling per-subagent policy (e.g. a billing subagent blocking refunds above a threshold).
- [ ] Stop hooks defined in subagent frontmatter are relied upon to auto-convert to SubagentStop events at runtime.
- [ ] Multi-concern requests are decomposed into distinct items, investigated in parallel with shared context, and synthesised into a single unified resolution — not handled sequentially or partially.
- [ ] Human-escalation handoff summaries are self-contained (customer ID, conversation summary, root cause, refund amount if applicable, recommended action), because the human agent has no access to the conversation transcript.

## 1.5 Agent SDK Hooks
Full module: `references/1-5-agent-sdk-hooks.md`

- [ ] Every hard requirement (financial, legal, compliance) is enforced by a hook, not by prompt instructions alone.
- [ ] Actions that must be *prevented* are blocked by PreToolUse hooks (pre-execution), never by PostToolUse hooks.
- [ ] No PostToolUse hook is being relied on to block or reject a policy-violating action after it has already run.
- [ ] Prerequisite gates (e.g. AML check before transfer_funds) are implemented as PreToolUse hooks that block until the prerequisite passes.
- [ ] Threshold-based approvals (refunds above $500, discounts above 20%) intercept the tool call and route to human escalation before execution.
- [ ] Heterogeneous tool outputs (dates, status codes, currency) are normalised by a PostToolUse hook so the model never parses raw, inconsistent formats.
- [ ] Formatting or style preferences are handled by prompts, not hooks, to avoid unnecessary deterministic overhead.
- [ ] Each hook is placed on the correct side of execution for its intent: transform-after uses PostToolUse, enforce-before uses PreToolUse.

## 1.6 Task Decomposition Strategies
Full module: `references/1-6-task-decomposition-strategies.md`

- [ ] Each decomposition uses the pattern that matches the task: fixed sequential pipeline for predictable, structured work; dynamic adaptive decomposition for open-ended work with unknown scope.
- [ ] Dynamic decomposition is not used where the steps are known in advance (avoid unnecessary unpredictability and debugging cost).
- [ ] Fixed pipelines are not used for open-ended investigation tasks where the plan must evolve as findings emerge.
- [ ] Multi-item work (many files, documents, or modules) is not processed in a single pass — check for attention-dilution symptoms such as decreasing depth across items or the same pattern flagged in one item but approved in another.
- [ ] Multi-item analysis is structured as per-item local passes plus a separate cross-item integration pass, so each item gets its full attention budget.
- [ ] The cross-item integration pass explicitly checks cross-cutting concerns: data flow, API consistency, cross-file dependencies, and pattern consistency across items.
- [ ] Fixes for inconsistent-depth results are structural (multi-pass decomposition), not attempts to compensate with a bigger model, larger context window, or a more detailed single-pass prompt.
- [ ] Fixed pipelines that need to react to intermediate findings are re-examined — this is a signal the task may actually require dynamic decomposition or handoff (see module 1.4 (Workflow Enforcement and Handoff)).

## 1.7 Session State and Resumption
Full module: `references/1-7-session-state-and-resumption.md`

- [ ] `--resume` is used only when files are unchanged since the prior session and the full history is still valid
- [ ] `fork_session` is reserved for comparing divergent approaches from a shared baseline, never for plain continuation
- [ ] When files have changed between sessions, a fresh start with structured summary injection is used instead of resume
- [ ] Injected summaries name the specific files that changed so the agent performs targeted re-analysis, not full re-exploration
- [ ] "Resume then re-read the changed files" is not treated as a complete fix — stale tool results remaining in history are recognised as a source of contradictory reasoning
- [ ] Resuming after dependency updates is handled as a stale-context risk, since multiple files may have changed indirectly
- [ ] Long sessions with cluttered or degraded history are reset with a fresh start plus summary rather than continued indefinitely
- [ ] Fresh-session summaries preserve prior findings for unchanged files so knowledge is not lost when starting clean
