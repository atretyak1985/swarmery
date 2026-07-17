---
domain: 1 - Agentic Architecture & Orchestration
module: "1.5"
title: "Agent SDK Hooks"
---

# 1.5 Agent SDK Hooks

## Overview

Agent SDK hooks are the mechanism for injecting deterministic behaviour into an otherwise probabilistic system. They sit at the boundary between the model's decisions and the real world, intercepting tool calls and results to enforce business rules and normalise data. Hooks are how programmatic enforcement is implemented in practice — they are the concrete counterpart to the enforcement spectrum described in module 1.4 (Workflow Enforcement and Handoff).

### Two Types of Hooks

The Agent SDK provides hooks at two points in the tool execution lifecycle:

**PostToolUse hooks** run **after** a tool executes but **before** the model processes the result. They intercept tool results and transform them before the model sees them. The model receives clean, normalised data regardless of which tool produced it.

**PreToolUse hooks** (sometimes described as tool-call interception) run **before** a tool executes. They intercept the outgoing tool call and can block it, modify it, or redirect it to an alternative workflow. The tool never runs if the hook decides to block it.

> **Key Concept**
> PostToolUse hooks transform data **after** execution. PreToolUse hooks enforce policy **before** execution. When reviewing an implementation, confirm each hook is placed on the correct side of execution for its purpose — the direction it operates in determines whether it can actually do the job it is assigned.

### PostToolUse Hooks: Data Normalisation

Different MCP tools return data in different formats. A customer database might return Unix timestamps (1710489600). An order management system might return ISO 8601 dates ("2024-03-15T12:00:00Z"). A status API might return numeric codes (200, 404, 500) while another returns strings ("active", "cancelled", "pending").

Without normalisation, the model must interpret these heterogeneous formats on every iteration. This introduces inconsistency — the model might correctly parse a Unix timestamp one time and misinterpret it the next.

A PostToolUse hook solves this by normalising all formats before the model processes them:

- Unix timestamps → ISO 8601 dates
- Numeric status codes → human-readable strings
- Currency values → consistent decimal format with currency code
- Date strings in various regional formats → a single standard format

The model receives clean, consistent data every time, regardless of which tool or backend system produced it.

### PreToolUse Hooks: Policy Enforcement

PreToolUse hooks are the implementation mechanism for the prerequisite gates described in module 1.4 (Workflow Enforcement and Handoff). They intercept outgoing tool calls before execution and apply business rules:

**Use case: Refund threshold enforcement.** A hook intercepts all calls to process_refund. If the refund amount exceeds $500, the hook blocks the call and redirects to a human escalation workflow. The refund tool never executes — the hook prevents it before it can run.

**Use case: Compliance prerequisite gates.** A hook intercepts calls to transfer_funds. If the required anti-money laundering (AML) check has not been completed for this session, the hook blocks the call and returns an error message directing the agent to complete the AML check first.

**Use case: Manager approval workflow.** A hook intercepts calls to approve_discount for discounts above 20%. The hook pauses execution and routes the request to a manager approval queue. Only after manager approval does the tool execute.

> **Common Mistake**
> Reaching for a PostToolUse hook to block a policy-violating action. This does not work: PostToolUse runs **after** execution, so by the time it fires the non-compliant action has already occurred — the refund is processed, the funds are transferred. Blocking must happen pre-execution, which means a PreToolUse hook. When auditing an implementation, check that anything intended to *prevent* an action is a PreToolUse hook, not a PostToolUse one.

### The Decision Framework

Use this framework to decide, for any given requirement, whether it belongs in a hook or a prompt:

| Requirement | Mechanism | Guarantee |
| --- | --- | --- |
| Must be followed 100% of the time | Hooks | Deterministic |
| Preferred but occasional deviation is acceptable | Prompts | Probabilistic |

**If the business would lose money from a single failure** → use a hook.
**If the business would face legal risk from a single failure** → use a hook.
**If it is a formatting preference or style guideline** → prompt-based guidance is fine.

A common anti-pattern is to rely on a prompt-based solution for a scenario that requires deterministic enforcement. The decision is not about whether prompts are "good enough" — it is about whether the consequence of a single failure justifies deterministic guarantees. When reviewing an implementation, flag any hard requirement (financial, legal, compliance) that is enforced only through prompt instructions.

### Hooks vs Prompts: Side-by-Side Comparison

**Scenario: International transfers must pass AML checks.**

- Prompt approach: "Always complete AML verification before processing international transfers." Works 95% of the time. The 5% failure rate means some transfers skip AML checks — a regulatory violation.
- Hook approach: A PreToolUse hook blocks transfer_funds until aml_check returns a pass. Works 100% of the time. No transfer can execute without AML verification.

**Scenario: Responses should be formatted in markdown.**

- Prompt approach: "Format all responses using markdown with headers and bullet points." Works most of the time. Occasional plain-text responses are not a business risk.
- Hook approach: Unnecessary overhead. Formatting preferences do not require deterministic enforcement.

**Scenario: Refunds above $500 require human approval.**

- Prompt approach: "For refunds above $500, escalate to a human agent." Works most of the time. A single failure means a large refund processed without approval.
- Hook approach: Intercept process_refund, check the amount, block if above $500 and route to human escalation. Works 100% of the time.

### Practical Example: Data Format Chaos

A customer support agent uses three MCP tools:

- get_customer returns dates as Unix timestamps and status as numeric codes.
- lookup_order returns dates as ISO 8601 strings and status as English strings.
- check_shipping returns dates as "DD/MM/YYYY" and status as single-character codes ("S" for shipped, "P" for pending).

Without a PostToolUse hook, the model must interpret three different date formats and three different status representations on every iteration. Sometimes it correctly converts a Unix timestamp; sometimes it confuses the day/month order in "DD/MM/YYYY"; sometimes it misinterprets "P" as "processed" instead of "pending."

With a PostToolUse hook, all tool results are normalised before the model sees them:

- All dates → ISO 8601 ("2024-03-15T12:00:00Z")
- All status codes → human-readable strings ("shipped", "pending", "delivered")

The model always receives consistent data, eliminating interpretation errors entirely.

## Audit Checklist

- [ ] Every hard requirement (financial, legal, compliance) is enforced by a hook, not by prompt instructions alone.
- [ ] Actions that must be *prevented* are blocked by PreToolUse hooks (pre-execution), never by PostToolUse hooks.
- [ ] No PostToolUse hook is being relied on to block or reject a policy-violating action after it has already run.
- [ ] Prerequisite gates (e.g. AML check before transfer_funds) are implemented as PreToolUse hooks that block until the prerequisite passes.
- [ ] Threshold-based approvals (refunds above $500, discounts above 20%) intercept the tool call and route to human escalation before execution.
- [ ] Heterogeneous tool outputs (dates, status codes, currency) are normalised by a PostToolUse hook so the model never parses raw, inconsistent formats.
- [ ] Formatting or style preferences are handled by prompts, not hooks, to avoid unnecessary deterministic overhead.
- [ ] Each hook is placed on the correct side of execution for its intent: transform-after uses PostToolUse, enforce-before uses PreToolUse.

## Sources

- [Claude Agent SDK Overview](https://platform.claude.com/docs/en/agent-sdk/overview) — Anthropic
- [Claude Agent SDK Hooks Documentation](https://platform.claude.com/docs/en/agent-sdk/hooks) — Anthropic
