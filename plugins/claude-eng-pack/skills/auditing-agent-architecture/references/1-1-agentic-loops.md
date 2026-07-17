---
domain: 1 - Agentic Architecture & Orchestration
module: "1.1"
title: "Agentic Loops"
---

# 1.1 Agentic Loops

## Overview

An agentic loop is the core execution cycle that powers every Claude-based agent. It is a deterministic control flow pattern — not a prompt trick, not a retry loop, and not a chatbot turn. Understanding this lifecycle is non-negotiable when building or auditing production systems: it is the first thing to verify when reviewing how an agent decides to keep working or stop.

### The Agentic Loop Lifecycle

The loop follows four steps, repeated until completion:

1. **Send a request** to Claude via the Messages API. This includes the conversation history (system prompt, prior messages, and any tool results from the previous iteration).
2. **Inspect the `stop_reason` field** in the response. This field is the authoritative signal for what happens next. It has two values relevant to agentic loops:
   - `"tool_use"` — Claude wants to call one or more tools. The loop continues.
   - `"end_turn"` — Claude has finished its work. The loop terminates.
3. **If `stop_reason` is `"tool_use"`**: execute the requested tool(s), append the tool results to the conversation history as a new message, and send the updated conversation back to Claude.
4. **If `stop_reason` is `"end_turn"`**: the agent has finished. Present the final response to the user.

The critical detail is step 3: tool results **must** be appended to conversation history. Without this, Claude cannot reason about the new information on the next iteration. The model needs to see what the tool returned in order to decide what to do next. When reviewing an implementation, confirm that tool results are written back into the history the next request is built from — a loop that drops them will stall or repeat work.

> **Key Concept**
> The `stop_reason` field is the **only** reliable signal for loop control. It is deterministic and unambiguous. Never use natural language parsing, text content checks, or arbitrary iteration caps as your primary stopping mechanism.

> **Basic loop vs production loop: handling every `stop_reason`**
> A basic loop branches on only two values, `tool_use` and `end_turn`. The live Messages API returns others that a production loop must handle: `pause_turn` (continue a long-running server-tool turn), `max_tokens`, `stop_sequence`, and `refusal` (current models such as Fable 5 can decline on an otherwise-normal 200 response). Treat any value other than `end_turn` as "not finished, check why" rather than assuming `tool_use`. When auditing a loop, check that unrecognised `stop_reason` values are handled explicitly rather than silently falling through to a `tool_use` or termination branch.

### Model-Driven Decision-Making

In an agentic loop, Claude decides which tool to call based on the current context. This is **model-driven decision-making** — the model reasons about the task, evaluates the available tools, and selects the appropriate one. This stands in contrast to **pre-configured decision trees** or **fixed tool sequences**, where the developer hard-codes which tool runs when.

Prefer model-driven approaches for flexibility. Claude can adapt to unexpected situations, handle edge cases, and chain tools in ways the developer did not anticipate. However, there is an important exception: when business logic requires deterministic compliance (financial operations, security checks, regulatory requirements), programmatic enforcement overrides model-driven flexibility. This principle is covered in module 1.4 (Workflow Enforcement and Handoff).

### The Three Anti-Patterns

These are the three specific anti-patterns for loop termination to check for when reviewing an implementation.

**Anti-Pattern 1: Parsing natural language signals.** Checking if Claude said "I'm done" or "task complete" to determine whether the loop should end. This is wrong because natural language is inherently ambiguous. Claude might say "I've finished analysing the first file" while intending to continue with more files. The `stop_reason` field exists precisely to eliminate this ambiguity.

**Anti-Pattern 2: Arbitrary iteration caps as the primary stopping mechanism.** Setting "stop after 10 loops" as the main way to terminate the agent. This is wrong because it either cuts off useful work (if the task genuinely needs 12 iterations) or runs unnecessary iterations (if the task finishes in 3). The model signals completion via `stop_reason` — use that signal. Iteration caps are acceptable as a safety net (a maximum bound to prevent runaway agents), but never as the primary control mechanism.

**Anti-Pattern 3: Checking for assistant text content as a completion indicator.** Using `response.content[0].type == "text"` to decide the loop is finished. This is wrong because Claude can return text alongside `tool_use` blocks. A response might contain explanatory text ("I'll now search for the customer's order history") immediately followed by a tool call. Checking for text presence does not tell you whether the agent is finished.

> **Common Mistake**
> Iteration caps look like a plausible fix for premature termination. Reject this reasoning: caps address runaway loops, not premature exits. If an agent stops before finishing, adding a cap does nothing — the fix for premature termination is always to check `stop_reason` correctly.

### Practical Example: The Premature Termination Bug

A developer builds a customer support agent. It works for simple queries but sometimes stops mid-task on complex requests. The code checks `if response.content[0].type == "text"` to determine completion.

The bug: Claude returns a text explanation ("Let me look up your order") alongside a `tool_use` block requesting the `lookup_order` tool. The code sees text in position [0], concludes the agent is finished, and returns the incomplete response to the user.

The fix: replace the content-type check with a `stop_reason` check. Continue the loop when `stop_reason == "tool_use"`, terminate when `stop_reason == "end_turn"`. This works regardless of what content types appear in the response.

## Audit Checklist

- [ ] Loop termination branches on `stop_reason`, not on natural-language parsing, text-content checks, or iteration counts.
- [ ] The loop continues on `stop_reason == "tool_use"` and terminates on `stop_reason == "end_turn"`.
- [ ] Tool results are appended to conversation history before the next request is sent, so the model can reason over them.
- [ ] `stop_reason` values beyond `tool_use` and `end_turn` — `pause_turn`, `max_tokens`, `stop_sequence`, `refusal` — are handled explicitly, not treated as `tool_use` by default.
- [ ] Any value other than `end_turn` is treated as "not finished, check why" rather than assumed complete.
- [ ] Tool selection is model-driven, except where business logic (financial, security, regulatory) requires deterministic programmatic enforcement.
- [ ] Iteration caps, if present, serve only as a runaway safety net — never as the primary stopping mechanism.
- [ ] Completion is never inferred from `response.content[0].type == "text"`, since text can accompany a `tool_use` block.

## Sources

- [Claude Agent SDK Overview](https://platform.claude.com/docs/en/agent-sdk/overview) — Anthropic
- [Messages API Reference](https://docs.anthropic.com/en/api/messages) — Anthropic
