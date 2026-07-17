---
domain: 3 - Claude Code Configuration & Workflows
module: "3.5"
title: "Iterative Refinement Techniques"
---

# 3.5 Iterative Refinement Techniques

## Overview

Working with Claude Code is iterative. The first output is rarely the final product. There are specific techniques for guiding Claude Code toward the right result — and, critically, which technique to reach for first in different situations. When reviewing an implementation or workflow, check that the technique matched to each class of problem is the right one, because the wrong technique wastes iterations and produces inconsistent results.

### The Technique Hierarchy

Not all refinement techniques are equal. There is a clear hierarchy of effectiveness:

**1. Concrete input/output examples (most effective for inconsistent interpretation)**

When you describe a code transformation in prose and Claude Code interprets it differently each time, the fix is not more prose. The fix is concrete examples.

Provide 2-3 examples showing the exact input and the exact expected output:

```text
Input:
  getUserData(userId: string): Promise<UserData>

Expected output:
  getUserData(userId: string): Promise<Result<UserData, ApiError>>
```

```text
Input:
  fetchOrders(customerId: string): Promise<Order[]>

Expected output:
  fetchOrders(customerId: string): Promise<Result<Order[], ApiError>>
```

The model generalises from these examples more reliably than from any prose description. Two or three concrete examples establish the pattern; the model applies it to novel cases. This is the first technique to reach for when interpretation is inconsistent.

**2. Test-driven iteration (most effective for complex transformations)**

Write the tests first. Define the expected behaviour through test cases covering:

- Happy path (the standard expected transformation)
- Edge cases (null values, empty inputs, boundary conditions)
- Performance requirements (if applicable)

Then share the test failures with Claude Code. The failures provide concrete, unambiguous feedback about what needs fixing. There is no room for interpretation when the test output says "Expected X, got Y."

```text
FAIL: testMigrationHandlesNullValues
  Expected: null preserved in output JSON
  Actual: null replaced with empty string ""
```

This failure message tells Claude Code exactly what to fix. No prose explanation needed.

**3. Interview pattern (most effective for unfamiliar domains)**

When working in a domain where you lack expertise, have Claude ask questions before implementing. This surfaces considerations you would miss.

Instead of prescribing a solution:

> "Build me a caching layer for the API"

Use the interview pattern:

> "I need a caching layer for the API. Before implementing, ask me questions about the requirements, edge cases, and constraints I should consider."

Claude might ask about cache invalidation strategies, TTL policies, consistency requirements, and failure modes — considerations that an expert would know to address but that you might overlook.

> **Key Concept**
> The interview pattern is for unfamiliar domains where the developer might miss important considerations. Concrete examples are for when the developer knows the exact transformation but the model interprets it inconsistently. Do not confuse the two — they solve different problems.

### Batch vs Sequential Feedback

How you deliver feedback matters. The rule:

**Single message (batch) when fixes interact with each other:**

If changing the error handling pattern also affects the logging format and the response structure, provide all three pieces of feedback in one message. The model needs to see all the interacting constraints at once to produce a coherent fix.

```text
Three changes needed (they interact with each other):
1. Error responses must include an error code field
2. Logging must include the error code in structured format
3. The client SDK type definitions must reflect the new error code field
```

**Sequential iteration when issues are independent:**

If the naming convention issue and the indentation issue do not affect each other, fix them one at a time. Batching independent issues can confuse the model about which feedback applies to which part of the code.

```text
First iteration: "Fix the function naming — use camelCase throughout"
[Wait for result]
Second iteration: "Now update the indentation to use 2 spaces"
```

### Example-Based Communication in Practice

When prose descriptions produce inconsistent results, the switch to examples follows a clear pattern:

1. **Observe inconsistency:** You describe a transformation, Claude Code does it differently each time.
2. **Switch to examples:** Provide 2-3 concrete before/after pairs showing the exact transformation.
3. **Verify generalisation:** Test on a new case to confirm the model generalises the pattern correctly.
4. **Add edge case examples if needed:** If the model handles the standard case but misses edge cases, add examples specifically showing edge case handling.

This is not about providing more examples. Two or three well-chosen examples that cover the standard case and a key edge case are sufficient. The model generalises the pattern; you do not need to provide every possible case.

### When Each Technique Applies

| Situation | Technique |
| --- | --- |
| Prose description interpreted differently each time | Concrete input/output examples |
| Complex transformation with many edge cases | Test-driven iteration |
| Working in an unfamiliar domain | Interview pattern |
| Multiple issues that affect each other | Batch feedback (one message) |
| Multiple independent issues | Sequential feedback |

## Audit Checklist

- [ ] When prose descriptions are interpreted inconsistently, the response is concrete input/output examples — not more prose.
- [ ] Two to three well-chosen examples are used to establish a pattern; the set is not padded out to cover every possible case.
- [ ] Complex transformations with many edge cases are driven by test-first iteration, with the raw test failures fed back to Claude Code rather than a prose restatement.
- [ ] Test cases cover the happy path, edge cases (null values, empty inputs, boundary conditions), and performance requirements where applicable.
- [ ] The interview pattern is reserved for unfamiliar domains and is not confused with concrete examples, which are for known transformations that the model interprets inconsistently.
- [ ] Feedback on fixes that interact (e.g. error handling, logging format, response structure) is delivered in a single batched message so the model sees all constraints at once.
- [ ] Feedback on independent issues is delivered sequentially, one iteration at a time, to avoid confusing which feedback applies to which part of the code.
- [ ] After switching to examples, generalisation is verified on a fresh case, and edge-case examples are added only when the model handles the standard case but misses the edges.

## Sources

- [Claude Code Iterative Development Documentation](https://code.claude.com/docs/en/best-practices) — Anthropic
