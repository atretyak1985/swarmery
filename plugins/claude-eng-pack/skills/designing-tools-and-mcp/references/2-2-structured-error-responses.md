---
domain: 2 - Tool Design & MCP Integration
module: "2.2"
title: "Structured Error Responses"
---

# 2.2 Structured Error Responses

## Overview

When an MCP tool fails, the error response it returns determines whether the agent can recover intelligently or fail blindly. Generic error messages like "Operation failed" are useless to an LLM — they provide no signal about what went wrong, whether to retry, or what alternative path to take. When reviewing a tool integration, treat a generic failure string as a defect: it strips the agent of every cue it needs to recover.

The MCP protocol provides the `isError` flag specifically for communicating tool failures back to the agent. Setting this flag tells the model that the tool execution failed, enabling it to reason about recovery rather than treating error text as a normal successful result.

### The Four Error Categories

Every tool failure falls into one of four categories. Each demands a different recovery strategy, and the agent needs structured metadata to distinguish them. When auditing a tool, check that its failure responses tag which category they belong to rather than collapsing everything into one opaque message.

**1. Transient Errors**
Timeouts, service unavailability, rate limits. The underlying system is temporarily unreachable but the request itself is valid. Recovery: retry after a brief delay.

```json
{
  "isError": true,
  "content": [{
    "type": "text",
    "text": "Service temporarily unavailable"
  }],
  "errorCategory": "transient",
  "isRetryable": true,
  "description": "The order database is experiencing high load. The request is valid and should succeed on retry."
}
```

**2. Validation Errors**
Invalid input format, missing required fields, out-of-range values. The request itself is malformed. Recovery: fix the input and retry.

```json
{
  "isError": true,
  "content": [{
    "type": "text",
    "text": "Invalid order ID format"
  }],
  "errorCategory": "validation",
  "isRetryable": true,
  "description": "Order ID must be in format #NNNNN (e.g. #12345). Received: 'order-abc'. Please reformat and retry."
}
```

**3. Business Errors**
Policy violations, limit exceedances, business rule conflicts. The request is technically valid but violates a business constraint. Recovery: do NOT retry — the same request will always fail. The agent needs an alternative workflow.

```json
{
  "isError": true,
  "content": [{
    "type": "text",
    "text": "Refund exceeds policy limit"
  }],
  "errorCategory": "business",
  "isRetryable": false,
  "description": "Refund amount of £750 exceeds the £500 automatic refund limit. This requires manager approval. Please escalate to a human agent with the refund details."
}
```

Note the `isRetryable: false` flag. This is critical. Business errors are never resolved by retrying. The agent must take a fundamentally different path — typically escalation or an alternative workflow. Including a customer-friendly explanation enables the agent to communicate appropriately.

**4. Permission Errors**
Access denied, insufficient credentials, authorisation failures. The tool cannot execute because the caller lacks the required permissions. Recovery: escalate or use different credentials.

```json
{
  "isError": true,
  "content": [{
    "type": "text",
    "text": "Access denied"
  }],
  "errorCategory": "permission",
  "isRetryable": false,
  "description": "The current service account does not have permission to access financial records. Escalate to a senior agent with financial system access."
}
```

### What `isRetryable` Really Signals

`isRetryable` answers one question: is there any path to success through retrying? It does not promise that the same request will succeed unchanged. Two categories share `isRetryable: true` yet need different recoveries. A transient error retries as-is once the system recovers. A validation error retries only after the agent fixes the input, reformatting `order-abc` to `#12345`. Business and permission errors are `isRetryable: false` for the mirror reason: no retry helps. A business rule blocks the request outright, and a permission error needs a different principal, an account with the right access, not a reworded call. Read the flag as "can a retry ever work", then read `errorCategory` to know how: resend, self-correct, escalate, or take an alternative route.

> **Common Mistake**
> Marking a validation error `isRetryable: false` because "the request failed" conflates the flag with "will the identical call succeed". The flag means "can a retry ever work" — and for a validation error it can, once the input is corrected. Setting it to `false` here tells the agent to give up on a request it could fix itself.

### Access Failure vs Valid Empty Result

This distinction is one of the most consequential in error-response design. When reviewing an implementation, verify it treats these two cases as fundamentally different responses.

**Access failure**: The tool could not reach the data source. A timeout occurred, authentication failed, or the service was down. The data might exist, but the tool could not check. The agent needs to decide whether to retry.

**Valid empty result**: The tool successfully queried the data source and found no matches. The query executed correctly — there simply is no data matching the criteria. The agent should NOT retry. The answer is "no results found."

Confusing these two breaks recovery logic entirely. Consider this scenario:

> A tool returns an empty array after a customer lookup. The agent retries 3 times, then escalates to a human. Analysis reveals the customer's account simply does not exist.

The tool succeeded. It queried the database, found no matching customer, and correctly returned an empty result. But because the response does not distinguish between "I could not reach the database" and "I reached the database and found nothing", the agent treats both the same way — as a failure worth retrying.

The fix: structure your tool responses so that a successful query with no results looks fundamentally different from a failed query.

```json
// Valid empty result — NOT an error
{
  "isError": false,
  "content": [{
    "type": "text",
    "text": "No customer found matching email 'john@example.com'. The query executed successfully but returned no matches."
  }],
  "resultCount": 0
}

// Access failure — IS an error
{
  "isError": true,
  "content": [{
    "type": "text",
    "text": "Could not reach customer database"
  }],
  "errorCategory": "transient",
  "isRetryable": true,
  "description": "Connection to the customer database timed out after 5 seconds. The query did not execute."
}
```

### Error Propagation in Multi-Agent Systems

In multi-agent architectures, error handling follows a principle of local recovery with selective propagation:

1. **Subagents implement local recovery for transient failures.** If a web search times out, the search subagent retries before bothering the coordinator.
2. **Only propagate errors that cannot be resolved locally.** If all retries fail, the subagent reports the failure upward.
3. **Include partial results and what was attempted.** The coordinator needs context: "I searched 3 of 5 sources successfully. Sources 4 and 5 timed out. Here are partial results from the 3 successful sources."

This prevents two anti-patterns: silently suppressing errors (returning empty results as success) and terminating entire workflows on a single failure. Both destroy the coordinator's ability to make intelligent decisions. Handoff and propagation conventions between agents are covered in module 1.4 (Workflow Enforcement and Handoff).

> **Key Concept**
> The distinction between access failures (tool could not reach the data source) and valid empty results (tool successfully queried and found nothing) is critical. Confusing the two causes wasted retries and incorrect escalations.

## Audit Checklist

- [ ] Tool failures set the MCP `isError` flag so the agent distinguishes failures from successful results, rather than returning error text as a normal result.
- [ ] Every failure response tags an `errorCategory` — transient, validation, business, or permission — instead of a generic "Operation failed" message.
- [ ] Responses carry an `isRetryable` flag plus a human-readable `description` stating what went wrong and the recovery path.
- [ ] Business and permission errors are marked `isRetryable: false`, and the agent escalates or switches workflow rather than retrying them.
- [ ] Validation errors are marked `isRetryable: true`, signalling the agent to correct the input (e.g. reformat `order-abc` to `#12345`) and retry.
- [ ] A successful query returning no matches responds with `isError: false` (e.g. `resultCount: 0`), visibly distinct from an access failure.
- [ ] Access failures (timeout, auth, service down) return `isError: true` with a retryable category and never look like a valid empty result.
- [ ] Subagents perform local recovery for transient failures before propagating errors up to the coordinator.
- [ ] Propagated errors include partial results and a record of what was attempted, not a bare failure.
- [ ] The system neither silently suppresses errors (empty-as-success) nor terminates an entire workflow on a single recoverable failure.

## Sources

- [MCP Specification — Tool Results](https://modelcontextprotocol.io/docs/concepts/tools) — Model Context Protocol
- [Building Effective Agents — Anthropic](https://www.anthropic.com/research/building-effective-agents) — Anthropic
