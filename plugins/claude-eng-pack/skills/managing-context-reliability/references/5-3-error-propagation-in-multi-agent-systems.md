---
domain: 5 - Context Management & Reliability
module: "5.3"
title: "Error Propagation in Multi-Agent Systems"
---

# 5.3 Error Propagation in Multi-Agent Systems

## Overview

Error propagation determines whether a multi-agent system recovers gracefully or fails silently. When a subagent encounters a failure — a timeout, a permission error, an invalid query — how that failure information flows back to the coordinator dictates the system's reliability. When reviewing an implementation, focus on three things: whether subagents return structured error context, whether either of the two critical anti-patterns is present, and whether the system correctly distinguishes access failures from valid empty results — the distinction most developers get wrong.

### Structured Error Context

When a subagent fails, it must return structured error context that enables the coordinator to make intelligent recovery decisions. This context must include four elements:

**1. Failure type.** Categorise the failure: transient (timeout, rate limit — may succeed on retry), validation (bad input — fix the query), business (rule violation — escalate or find alternative), or permission (access denied — cannot be retried without authorisation changes).

**2. What was attempted.** The specific query, parameters used, and target system. "Searched academic database for 'renewable energy policy' with date range 2022-2024" is actionable. "Search failed" is not.

**3. Partial results gathered before failure.** If the subagent retrieved three of five sources before timing out, those three results are valuable. Discarding them because the overall operation failed is wasteful.

**4. Potential alternative approaches.** The subagent knows its domain. If an academic database is down, it might suggest trying a different database, broadening the search terms, or checking cached results. These suggestions help the coordinator decide on recovery strategy.

```json
{
  "status": "partial_failure",
  "failureType": "transient",
  "attemptedAction": {
    "tool": "search_academic_db",
    "query": "renewable energy policy",
    "dateRange": "2022-2024"
  },
  "partialResults": [
    {
      "title": "EU Renewable Energy Directive 2023",
      "source": "EUR-Lex",
      "retrieved": true
    }
  ],
  "alternativeApproaches": [
    "Retry with narrower date range (2023-2024)",
    "Search alternative database: government_publications",
    "Use cached results from previous research session"
  ]
}
```

This structure gives the coordinator everything it needs to decide: retry the same query, try an alternative, proceed with partial results, or escalate.

### The Two Anti-Patterns

These are the two anti-patterns to check for when auditing error handling. Both are catastrophic in different ways:

**Silent suppression: returning empty results marked as success.** This is the worst anti-pattern. The subagent encounters a timeout but returns `{ "results": [], "status": "success" }`. The coordinator believes the search ran and found nothing. It will not retry, will not try alternatives, and will produce a synthesis that silently omits an entire research area. The final output appears complete but is missing critical content.

Silent suppression is especially dangerous because it is invisible. The output looks correct — it just has gaps that nobody can detect. In a customer support context, it might mean the agent reports "no orders found" when the order lookup system was actually down, leading the agent to tell the customer they have no account.

**Workflow termination: killing the entire pipeline on a single failure.** One subagent times out and the entire research pipeline crashes. The other four subagents completed successfully, but their results are thrown away. This is a disproportionate response that wastes completed work and provides no recovery path.

The correct middle ground is structured error propagation: the failing subagent reports what happened, the coordinator assesses the damage, and the system continues with partial results or targeted recovery.

### Access Failure vs Valid Empty Result

This distinction is critical, and conflating the two is a common source of subtle bugs:

**Access failure:** The tool could not reach the data source. A timeout, a connection error, a permission denial. The search did not execute. Consider retry with the same or modified parameters.

**Valid empty result:** The tool reached the source and executed the query. It found no matches. This IS the answer. No retry is needed because the system worked correctly — there simply are no results for this query.

Conflating these leads to two problems:

- Treating access failures as valid empty results means you never retry when you should.
- Treating valid empty results as access failures means you waste time retrying a query that will always return nothing.

```python
# Access failure — consider retry
{
    "status": "error",
    "failureType": "transient",
    "message": "Connection timeout after 30s",
    "shouldRetry": True
}

# Valid empty result — no retry needed
{
    "status": "success",
    "results": [],
    "message": "Query executed successfully. No matching records found.",
    "shouldRetry": False
}
```

### Coverage Annotations

When a synthesis agent combines findings from multiple subagents, the output should note which topic areas are well-supported and which have gaps. If one subagent failed to retrieve sources on geothermal energy, the synthesis should say:

"Section on geothermal energy is limited due to unavailable journal access during research."

This is far better than silently omitting the topic. Coverage annotations let the consumer know what the report covers fully and where there are known limitations. Without them, a gap in the synthesis looks like the topic was not relevant rather than the source being unavailable.

### Local Recovery for Transient Failures

Subagents should implement local recovery for transient failures — retry logic, fallback sources, degraded responses — before propagating errors to the coordinator. Only propagate errors the subagent cannot resolve locally. When propagating, always include what was attempted and any partial results gathered.

This reduces coordinator complexity. The coordinator does not need to manage retry logic for every possible transient failure across every subagent. Each subagent handles its own transient failures and only escalates persistent ones.

> **Key Concept**
> Structured error context (failure type, attempted action, partial results, alternatives) enables intelligent coordinator recovery. The two anti-patterns are silent suppression (empty results as success) and workflow termination (killing the pipeline on one failure). Access failures need retry consideration; valid empty results do not.

## Audit Checklist

- [ ] Subagent error responses carry structured context with all four elements: failure type, attempted action, partial results, and alternative approaches.
- [ ] Failure type is categorised as transient, validation, business, or permission so the coordinator can pick the right recovery path.
- [ ] No subagent returns empty results marked as `success` when the operation actually failed (silent suppression).
- [ ] A single subagent failure does not terminate the whole pipeline; the coordinator continues with partial results or targeted recovery (no workflow termination).
- [ ] Access failures (timeout, connection error, permission denial) are distinguished from valid empty results, with `shouldRetry` set accordingly.
- [ ] Valid empty results are treated as the answer and not retried; access failures are eligible for retry.
- [ ] Partial results gathered before a failure are preserved and passed to the coordinator rather than discarded.
- [ ] Subagents attempt local recovery for transient failures (retry, fallback sources, degraded responses) before escalating to the coordinator.
- [ ] Synthesis output includes coverage annotations that flag topic areas limited by unavailable sources rather than silently omitting them.

## Sources

- [Anthropic Multi-Agent Patterns](https://www.anthropic.com/engineering/built-multi-agent-research-system) — Anthropic
