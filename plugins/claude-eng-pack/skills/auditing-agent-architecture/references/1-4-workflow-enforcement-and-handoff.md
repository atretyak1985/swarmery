---
domain: 1 - Agentic Architecture & Orchestration
module: "1.4"
title: "Workflow Enforcement and Handoff"
---

# 1.4 Workflow Enforcement and Handoff

## Overview

There is a hard line between two approaches to controlling agent behaviour: prompt-based guidance and programmatic enforcement. Confusing the two on high-stakes scenarios is a common and costly mistake. When reviewing an implementation, this distinction is the first thing to check.

### The Enforcement Spectrum

There are two fundamentally different ways to enforce workflow ordering in an agentic system:

**Prompt-based guidance** means including instructions in the system prompt. For example: "Always verify the customer's identity before processing a refund." This works most of the time — perhaps 90-95% of cases. But it has a **non-zero failure rate**. The model is probabilistic. Sometimes it will skip steps, reorder them, or interpret instructions loosely. For low-stakes operations, this failure rate is acceptable.

**Programmatic enforcement** means implementing hooks, prerequisite gates, or code-level checks that physically block downstream tools until prerequisites complete. For example: the `process_refund` tool cannot execute until `get_customer` has returned a verified customer ID. This works **every time**. It is deterministic, not probabilistic. No matter what the model decides to do, the gate prevents the wrong execution order.

> **Key Concept**
> Prompt-based guidance is probabilistic — it works most of the time. Programmatic enforcement is deterministic — it works every time. The decision rule: if a single failure would cause financial loss, security breach, or compliance violation, use programmatic enforcement.

### The Decision Rule

A consistent decision rule applies across scenarios:

- **Financial operations** (refunds, transfers, payments): programmatic enforcement. A single unverified refund to the wrong account is a financial loss.
- **Security operations** (identity verification, access control): programmatic enforcement. A single bypass of identity verification is a security breach.
- **Compliance operations** (AML checks, regulatory requirements): programmatic enforcement. A single missed compliance check can result in legal penalties.
- **Low-stakes operations** (formatting preferences, style guidelines, output ordering): prompt-based guidance is acceptable. A formatting inconsistency is not a business risk.

For high-stakes scenarios, prompt-based solutions are tempting but wrong — **reject them**. Enhanced system prompts, few-shot examples, and stronger instructions all improve accuracy but none provide deterministic guarantees. When the scenario involves money, security, or compliance, the answer is always programmatic enforcement.

> **Common Mistake**
> Reaching for "add stronger instructions to the system prompt" or "include few-shot examples showing the correct workflow" is a frequent misstep in high-stakes scenarios. These changes improve probability but do not eliminate the failure rate. For financial, security, and compliance operations, only programmatic enforcement is correct.

### Prerequisite Gates in Practice

A prerequisite gate is a programmatic check that blocks a tool from executing until a prior condition is met. In a customer support agent:

- The agent has access to `get_customer`, `lookup_order`, and `process_refund` tools.
- A prerequisite gate checks: has `get_customer` returned a verified customer ID for this session?
- If yes, `process_refund` executes normally.
- If no, `process_refund` returns an error message: "Cannot process refund — customer identity not verified. Please call get_customer first."

The gate is code, not a prompt instruction. The model cannot bypass it by deciding to skip verification. Even if the model attempts to call `process_refund` directly, the gate blocks the call and returns an error that forces the model to verify identity first.

### Subagent Lifecycle Hooks: SubagentStart and SubagentStop

The Claude Agent SDK provides lifecycle hook events specifically for subagent management. These complement the PreToolUse and PostToolUse hooks (see module 1.5, Agent SDK Hooks).

**SubagentStart** fires when a subagent is spawned via the Task tool (renamed Agent in current Claude Code). It provides the coordinator with a hook point to log, validate, or modify the subagent invocation before the subagent begins execution. Use cases include enforcing rate limits on subagent spawning, logging which subagents are invoked for observability, and validating that the coordinator is passing required context.

**SubagentStop** fires when a subagent finishes execution and returns its results to the coordinator. This hook lets you intercept, validate, or transform subagent output before the coordinator processes it. Use cases include validating that subagent output conforms to expected schemas, stripping sensitive data from subagent results, and logging subagent completion for performance monitoring.

**Subagent-scoped hooks:** Subagents can define their own PreToolUse and PostToolUse hooks in their AgentDefinition frontmatter. These hooks are scoped to the subagent — they only intercept tool calls made by that specific subagent, not the coordinator or other subagents. This enables per-subagent policy enforcement (for example, a billing subagent might have a PreToolUse hook that blocks refunds above a threshold, while a technical support subagent has no such restriction).

**Stop hook auto-conversion:** When a subagent's frontmatter defines Stop hooks, these are automatically converted to SubagentStop events at runtime. This means you can define cleanup or validation logic in the subagent's own configuration, and the SDK ensures it runs as a SubagentStop lifecycle event when the subagent completes.

> **Key Concept**
> SubagentStart and SubagentStop are lifecycle hooks for observing and controlling subagent invocation. Subagents can define their own PreToolUse/PostToolUse hooks scoped to their execution, and Stop hooks in subagent frontmatter auto-convert to SubagentStop events at runtime.

### Multi-Concern Request Handling

Customers frequently submit requests with multiple issues: "I want to return my order, update my shipping address, and ask about my loyalty points." When reviewing an implementation, check how the agent handles these compound requests.

The correct approach:

- **Decompose** the request into distinct items (return, address update, loyalty inquiry).
- **Investigate each in parallel** using shared context (the customer's account information is relevant to all three).
- **Synthesise a unified resolution** that addresses all items in a single response.

The wrong approach is to handle them sequentially with separate conversations, or to address only the first item and forget the rest.

### Structured Handoff Protocols

When an agent cannot resolve an issue and must escalate to a human agent, the handoff must follow a structured protocol. The critical constraint: **the human agent does NOT have access to the conversation transcript.** They cannot scroll through the chat history to understand the issue.

A proper handoff summary must be self-contained and include:

- **Customer ID** — so the human agent can pull up the account.
- **Conversation summary** — what the customer asked for and what has been attempted.
- **Root cause analysis** — the agent's assessment of the underlying issue.
- **Refund amount** (if applicable) — the specific financial figure, not a vague reference.
- **Recommended action** — what the agent believes the human agent should do.

This summary is the only information the human agent receives. If it is incomplete, the human agent must ask the customer to repeat everything, creating a poor experience.

### Practical Example: The 8% Failure Rate

Production data shows a customer support agent processes refunds without verifying account ownership in 8% of cases. The system prompt instructs: "Always verify the customer's identity before processing any refund." The prompt works 92% of the time but fails 8% of the time.

The 8% failure rate has already resulted in refunds processed on wrong accounts. This is a financial operation with real monetary consequences.

The fix is a programmatic prerequisite gate. Before `process_refund` can execute, the system checks that `get_customer` has returned a verified customer ID in the current session. This eliminates the 8% failure rate entirely — not by improving the prompt, but by physically preventing the incorrect execution order.

## Audit Checklist

- [ ] High-stakes workflow ordering (financial, security, compliance operations) is enforced programmatically via hooks or prerequisite gates, not through system-prompt instructions alone.
- [ ] Prompt-based guidance is reserved for low-stakes operations (formatting, style, output ordering) where a non-zero failure rate is acceptable.
- [ ] Prerequisite gates are implemented in code and return a blocking error until the prior condition is met (e.g. `process_refund` blocked until `get_customer` returns a verified customer ID).
- [ ] A gate cannot be bypassed by the model choosing to skip the prerequisite — a direct call to the gated tool is rejected, not silently allowed.
- [ ] SubagentStart / SubagentStop lifecycle hooks are used wherever subagent invocations or outputs need to be logged, validated, rate-limited, or transformed.
- [ ] Subagent-scoped PreToolUse/PostToolUse hooks intercept only that subagent's tool calls, enabling per-subagent policy (e.g. a billing subagent blocking refunds above a threshold).
- [ ] Stop hooks defined in subagent frontmatter are relied upon to auto-convert to SubagentStop events at runtime.
- [ ] Multi-concern requests are decomposed into distinct items, investigated in parallel with shared context, and synthesised into a single unified resolution — not handled sequentially or partially.
- [ ] Human-escalation handoff summaries are self-contained (customer ID, conversation summary, root cause, refund amount if applicable, recommended action), because the human agent has no access to the conversation transcript.

## Sources

- [Claude Agent SDK Overview](https://platform.claude.com/docs/en/agent-sdk/overview) — Anthropic
