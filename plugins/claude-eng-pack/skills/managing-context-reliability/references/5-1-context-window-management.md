---
domain: 5 - Context Management & Reliability
module: "5.1"
title: "Context Window Management"
---

# 5.1 Context Window Management

## Overview

Context window management is the foundation of reliable Claude-based systems. Every multi-turn conversation, every multi-agent pipeline, and every long-document extraction task depends on how well you manage what goes into the context window. Get this wrong and your customer support agent forgets refund amounts, your research pipeline drops citations, and your extraction system loses precision on the very fields that matter.

When auditing an implementation, treat this module as a set of concrete patterns to look for and anti-patterns to reject. The subsections below each describe a failure mode, the technical mechanism behind it, and the fix a production implementation should apply.

### The Progressive Summarisation Trap

When conversations grow long, a common strategy is to summarise earlier turns to free up token budget. This is a trap. Progressive summarisation systematically destroys the most critical information in customer-facing and data-processing systems: numerical values, dates, percentages, and customer-stated expectations.

Consider a real example. A customer contacts support about a refund:

```
Turn 3: "I'd like a refund of $247.83 for order #8891 placed on March 3rd"
```

After summarisation, this becomes:

```
Summary: "Customer wants a refund for a recent order"
```

The amount, order number, and date — the three facts the agent needs to process the refund — are gone. This is not a hypothetical failure mode; it is the default behaviour of summarisation applied to transactional data.

**The fix: persistent case facts blocks.** Extract transactional facts (amounts, dates, order numbers, statuses) into a structured block that is included in every prompt, outside the summarised history. This block is never summarised. It persists across every turn regardless of what happens to the conversation history.

```json
{
  "caseFactsBlock": {
    "customerId": "C-4421",
    "issues": [
      {
        "orderId": "#8891",
        "orderDate": "2024-03-03",
        "refundAmount": "$247.83",
        "status": "pending_refund",
        "itemDescription": "Wireless headphones — defective"
      }
    ]
  }
}
```

For multi-issue sessions where a customer raises several problems in one conversation, extract and persist structured issue data into a separate context layer. Each issue gets its own entry with order IDs, amounts, and statuses. This prevents cross-contamination between issues during summarisation.

> **Common Mistake**
> Relying on plain summarisation to keep a long conversation inside the token budget looks like a reasonable optimisation, but it silently discards exactly the transactional facts the system depends on. Reject it in favour of a persistent facts block: summarisation and fact preservation are separate concerns, and summarisation alone cannot serve both.

### The "Lost in the Middle" Effect

Models process information at the beginning and end of long inputs reliably. Findings buried in the middle of a long context may be missed or given less weight. This is a well-documented phenomenon in large language models and it directly affects how you structure aggregated inputs.

**The fix is structural, not prompt-based.** Place key findings summaries at the beginning of aggregated inputs. Organise detailed results with explicit section headers throughout. If you are feeding a synthesis agent the output of three research subagents, start with a "Key Findings Summary" section, then provide the detailed outputs with clear section boundaries.

```
## Key Findings Summary
- Source A: 12% market growth in renewable sector (2023)
- Source B: Patent filings increased 34% year-on-year
- Source C: Regulatory framework delayed until Q3 2025

## Detailed Findings

### Source A: Market Analysis Report
[Full details here...]

### Source B: Patent Database Analysis
[Full details here...]

### Source C: Regulatory Review
[Full details here...]
```

When reviewing an implementation, check that the fix lives in how inputs are assembled — leading summary plus explicit section headers — rather than in a prompt instruction asking the model to "pay attention to the middle". Prompt wording does not overcome the positional bias; structure does.

### Tool Result Trimming

Tool results are a silent context budget killer. An order lookup might return 40+ fields: internal audit timestamps, warehouse codes, shipping carrier IDs, fulfilment centre identifiers, and dozens of other fields irrelevant to the customer's refund request. You need 5 fields. Those other 35 fields consume tokens in every subsequent turn as the conversation history grows.

**Trim verbose tool outputs to only relevant fields before they accumulate in context.** This is not optional optimisation — it is essential for multi-turn systems where tool results stack up across the conversation.

```python
def trim_order_result(raw_result, relevant_fields=None):
    if relevant_fields is None:
        relevant_fields = [
            "order_id", "order_date", "total_amount",
            "return_eligible", "item_description"
        ]
    return {k: v for k, v in raw_result.items() if k in relevant_fields}
```

This trimming should happen in a `PostToolUse` hook or in the tool implementation itself, before the result enters the conversation history. Once verbose data is in the context, it stays there for every subsequent turn.

### Full Conversation History

The Claude API is stateless. Each request must include the complete conversation history. If you omit earlier messages, the model loses conversational coherence. There is no session state on the server side — every turn must include everything the model needs to understand the full conversation.

This creates a tension with context limits: you need the full history for coherence, but the history grows with every turn. The persistent case facts block resolves this by separating critical facts from summarisable narrative, letting you summarise the conversation flow while preserving every transactional detail.

### Upstream Agent Optimisation

In multi-agent systems, upstream agents often return verbose reasoning chains and raw content that downstream agents do not need. When a research subagent sends its full thought process to a synthesis agent with a limited context budget, the synthesis agent wastes tokens on reasoning it cannot use.

**Modify upstream agents to return structured data** — key facts, citations, relevance scores — instead of verbose content and reasoning chains. Require subagents to include metadata (dates, source locations, methodological context) in structured outputs to support accurate downstream synthesis.

```json
{
  "findings": [
    {
      "claim": "Renewable energy investment grew 12% in 2023",
      "source": "IEA World Energy Report 2024",
      "sourceUrl": "https://example.com/report",
      "relevanceScore": 0.92,
      "publicationDate": "2024-01-15"
    }
  ]
}
```

This is not just about saving tokens. Structured outputs from upstream agents enable downstream agents to process findings without re-parsing verbose prose. For how subagents receive and pass context in the first place, see module 1.3 (Subagent Invocation and Context Passing).

> **Key Concept**
> The persistent case facts block is the single most important pattern in context window management. Extract transactional facts (amounts, dates, order numbers) into a structured block that is included in every prompt and never summarised. This is the fix for progressive summarisation and the foundation for reliable multi-turn systems.

### Prompt Caching

Prompt caching is the other half of context economics. Instead of trimming what the model sees, you avoid paying to reprocess the parts that do not change. When you mark a stable prefix with a `cache_control` breakpoint, the API stores that processed prefix and reuses it on the next request, charging a fraction of the input cost for the cached tokens.

Caching matches from the start of the prompt, prefix by prefix, so layout decides whether you get a hit. Put the content that stays constant first: system instructions, tool definitions, long reference documents. Place the `cache_control` breakpoint at the end of that static block. Put the volatile content, the user's latest message and anything that changes per request, after the breakpoint.

```python
messages = [
    {
        "role": "system",
        "content": [
            {"type": "text", "text": LONG_STATIC_INSTRUCTIONS},
            {"type": "text", "text": REFERENCE_DOC,
             "cache_control": {"type": "ephemeral"}},
        ],
    },
    {"role": "user", "content": dynamic_user_message},
]
```

Get the order wrong and you lose the benefit entirely. If dynamic content sits before the static block, the prefix changes on every request, nothing matches, and every call pays full price. The cache is also short-lived: an `ephemeral` breakpoint lasts about five minutes since last use, so caching pays off for bursts of related requests, not for content reused hours apart.

## Audit Checklist

- [ ] Transactional facts (amounts, dates, order numbers, statuses) are extracted into a persistent case facts block that is included in every prompt and never summarised.
- [ ] Multi-issue sessions persist each issue as its own structured entry, preventing cross-contamination between issues during summarisation.
- [ ] Aggregated inputs lead with a "Key Findings Summary" and use explicit section headers, so critical facts are not buried in the middle where the model may miss them.
- [ ] The "lost in the middle" fix is structural (input layout), not a prompt instruction asking the model to attend to the middle.
- [ ] Verbose tool results are trimmed to only the relevant fields before entering conversation history, ideally in a `PostToolUse` hook or the tool implementation itself.
- [ ] Every API request includes the full conversation history, since the API is stateless with no server-side session state.
- [ ] Upstream agents return structured data (key facts, citations, relevance scores, metadata) rather than verbose reasoning chains for downstream consumers.
- [ ] Prompt caching places static content (system instructions, tool definitions, reference docs) first with the `cache_control` breakpoint at the end of the static block, and volatile content after it.
- [ ] The caching strategy accounts for the ~5-minute ephemeral cache lifetime, targeting bursts of related requests rather than content reused hours apart.

## Sources

- [Anthropic API Documentation — Messages](https://docs.anthropic.com/en/api/messages) — Anthropic
- [Anthropic Prompt Engineering — Long Context Tips](https://platform.claude.com/docs/en/build-with-claude/prompt-engineering/claude-prompting-best-practices) — Anthropic
