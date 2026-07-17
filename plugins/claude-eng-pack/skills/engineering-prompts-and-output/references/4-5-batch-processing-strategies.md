---
domain: 4 - Prompt Engineering & Structured Output
module: "4.5"
title: "Batch Processing Strategies"
---

# 4.5 Batch Processing Strategies

## Overview

The Message Batches API is a cost optimisation tool with hard constraints. Knowing when to use it — and when not to — is the core design decision for any processing pipeline. When reviewing an implementation, the central question is whether latency-tolerant work has been moved to batch while blocking work stays synchronous.

### Message Batches API: The Facts

These are the hard constraints every batch design must respect:

- **50% cost savings** compared to synchronous API calls
- **Up to 24-hour processing window** — results may arrive in minutes or take up to 24 hours
- **No guaranteed latency SLA** — you cannot rely on results arriving within any specific timeframe
- **No multi-turn tool calling** within a single batch request — the model cannot execute tools mid-request and use the results to continue processing
- **`custom_id` fields** for correlating request/response pairs — each request in a batch gets a unique identifier used to match it with its response

### The Matching Rule

The core decision is to match the API to the workflow's latency tolerance:

**Synchronous API:** For blocking workflows where someone or something is waiting for the result. Pre-merge checks in CI/CD, real-time code review feedback, any workflow where developers are blocked pending completion.

**Batch API:** For latency-tolerant workflows where results are consumed later. Overnight technical debt reports, weekly code audit summaries, nightly test generation runs, batch document extraction.

> **Common Mistake**
>
> Switching everything to batch processing to capture the 50% cost savings. This looks compelling on a cost spreadsheet, but batch has no latency SLA — blocking workflows (pre-merge checks, real-time review feedback) would stall developers waiting up to 24 hours for a result. The correct approach keeps blocking workflows synchronous and moves only latency-tolerant workflows to batch.

```typescript
// Synchronous — developer is waiting for this
const preMergeReview = await client.messages.create({
  model: "claude-sonnet-5",
  max_tokens: 4096,
  messages: [{ role: "user", content: prDiffContent }]
});

// Batch — results consumed tomorrow morning
const batchRequest = await client.batches.create({
  requests: technicalDebtDocuments.map((doc, i) => ({
    custom_id: `debt-report-${i}`,
    params: {
      model: "claude-sonnet-5",
      max_tokens: 4096,
      messages: [{ role: "user", content: doc }]
    }
  }))
});
```

### SLA Calculation

When designing batch processing schedules, you must account for the 24-hour maximum processing window. If your organisation requires a 30-hour SLA for a report:

- The Batch API guarantees results within 24 hours, so the final batch must be submitted no later than 24 hours before the deadline
- 30 hours total SLA minus 24 hours processing = 6 hours of buffer for collecting requests, validating inputs, or absorbing operational delays
- Submit batches every 4-6 hours within that buffer window so a fresh batch is always in flight

When reviewing a scheduling design, work backwards from the SLA to determine the required submission frequency.

### Batch Failure Handling

Not all documents in a batch succeed. The correct failure handling pattern has three steps:

**1. Identify failures by `custom_id`.** Each request has a unique identifier. Parse the batch results to find which `custom_id` values failed.

**2. Resubmit only failures with modifications.** Do not resubmit the entire batch. Common modifications include:

- Chunking oversized documents that exceeded context limits
- Simplifying extraction prompts for documents with unusual structures
- Adding format-specific few-shot examples for documents that failed due to structural variety

**3. Refine prompts on a sample set BEFORE batch processing.** This is the proactive step that maximises first-pass success and reduces resubmission costs. Test your prompts against a representative sample (5-10 documents covering the range of formats and edge cases) before processing the full batch.

```typescript
// Parse batch results and identify failures
const results = await client.batches.results(batchId);
const failures = results.filter(r => r.result.type === "errored");
const failedIds = failures.map(f => f.custom_id);

// Resubmit only failures with modifications
const retryRequests = failedIds.map(id => {
  const originalDoc = documentsById[id];
  return {
    custom_id: `${id}-retry-1`,
    params: {
      model: "claude-sonnet-5",
      max_tokens: 8192,  // increased for oversized docs
      messages: [{
        role: "user",
        content: chunkIfNeeded(originalDoc)
      }]
    }
  };
});
```

### Multi-Turn Tool Calling Limitation

The batch API does not support multi-turn tool calling within a single request. This means you cannot:

- Define tools and have the model call them mid-request
- Process tool results and continue the conversation within the same batch item
- Run agentic loops within a single batch request

If your workflow requires tool execution mid-processing, you must use the synchronous API. When reviewing an implementation, watch for this: if a batch workflow needs to call external tools during processing, that step must run on the synchronous API instead.

> **Key Concept**
>
> The Message Batches API provides 50% cost savings with an up to 24-hour processing window and no latency SLA. Use it only for latency-tolerant workflows (overnight reports, weekly audits). Blocking workflows (pre-merge checks) must remain synchronous. Always refine prompts on a sample set before submitting large batches.

### Prompt Optimisation Before Batch Submission

The most cost-effective batch processing strategy is to invest time in prompt refinement before submitting large volumes:

- **Sample set testing:** Take 5-10 representative documents covering the range of formats, edge cases, and document types in your batch
- **Iterate on the sample:** Refine your extraction prompts, add few-shot examples, adjust schema design until the sample set achieves high accuracy
- **Submit the full batch:** With refined prompts, your first-pass success rate will be significantly higher
- **Handle failures:** Resubmit only the failed documents with targeted modifications

This workflow dramatically reduces total cost. A 90% first-pass success rate on 1,000 documents means only 100 retries. A 60% first-pass rate means 400 retries — four times the resubmission cost, plus the batch processing cost for those retries.

## Audit Checklist

- [ ] Blocking workflows (pre-merge checks, real-time review feedback) run on the synchronous API, never on batch.
- [ ] Batch is reserved for latency-tolerant workflows only (overnight reports, weekly audits, nightly runs, bulk document extraction).
- [ ] Batch scheduling accounts for the up-to-24-hour processing window and works backwards from the SLA (e.g. final batch submitted at least 24 hours before the deadline, with buffer for collection and validation).
- [ ] Every batch request carries a unique `custom_id` used to correlate requests with their responses.
- [ ] Failure handling identifies failures by `custom_id` and resubmits only the failed items with targeted modifications — not the whole batch.
- [ ] Prompts are refined against a representative 5-10 document sample set before the full batch is submitted.
- [ ] No batch item depends on multi-turn tool calling or agentic loops; any step needing tool execution mid-processing runs on the synchronous API.

## Sources

- [Message Batches API](https://platform.claude.com/docs/en/build-with-claude/batch-processing) — Anthropic
