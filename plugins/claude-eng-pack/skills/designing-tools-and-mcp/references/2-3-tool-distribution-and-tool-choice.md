---
domain: 2 - Tool Design & MCP Integration
module: "2.3"
title: "Tool Distribution & Tool Choice"
---

# 2.3 Tool Distribution & Tool Choice

## Overview

The number of tools you give an agent directly affects how reliably it selects the right one. This is not a minor implementation detail — it is a core architectural decision that determines whether your multi-agent system works in production. When auditing an agent architecture, treat tool distribution and `tool_choice` configuration as first-class review items, not afterthoughts.

### The Tool Overload Problem

Giving a single agent 18 tools degrades selection reliability. The model faces increased decision complexity with every additional tool, and error rates climb as the toolkit grows. The optimal range is **4-5 tools per agent**, scoped to that agent's specific role.

This is not just about quantity — it is about relevance. A synthesis agent should NOT have web search tools. A web search agent should NOT have document analysis tools. When agents have tools outside their specialisation, they tend to misuse them. A synthesis agent with access to `web_search` might decide to run its own searches instead of using the search results already provided to it, duplicating work and wasting context.

The principle to check for: each agent gets only the tools it needs for its defined role. Nothing more. When reviewing an implementation, count the tools attached to each agent and flag any agent whose toolkit spans more than one role.

### The tool_choice Configuration

The `tool_choice` parameter controls how the model interacts with available tools. There are three settings, and each serves a distinct purpose. When reviewing an implementation, confirm the setting matches the intent of the step.

**`"auto"` (default)**

The model decides whether to call a tool or return text. Use this for general operation where the model needs flexibility to respond conversationally when no tool call is appropriate.

```json
{
  "tool_choice": { "type": "auto" }
}
```

**`"any"`**

The model MUST call a tool but chooses which one. Use this when you need guaranteed structured output from one of multiple schemas — the model will always produce a tool call, never plain text.

```json
{
  "tool_choice": { "type": "any" }
}
```

This is particularly valuable in extraction pipelines. If you have multiple extraction schemas (one for invoices, one for receipts, one for contracts) and the document type is unknown, `"any"` guarantees the model picks one and produces structured output rather than returning a conversational response.

**Forced selection**

The model MUST call a specific named tool. Use this to enforce mandatory first steps — the model cannot skip or reorder the required operation.

```json
{
  "tool_choice": { "type": "tool", "name": "extract_metadata" }
}
```

This is the correct mechanism for enforcing workflow ordering. If metadata extraction must happen before any enrichment tools run, forced selection guarantees it. The model cannot decide to skip `extract_metadata` and jump straight to enrichment. After the forced call completes, subsequent turns can use `"auto"` for the remaining steps.

### Scoped Cross-Role Tools

Sometimes an agent needs occasional access to a capability that belongs to another role. The naive approach is to route every such request through the coordinator. The problem: this adds 2-3 round trips per request and can increase latency by 40% or more.

The solution is a **scoped cross-role tool**: a constrained version of the capability, given directly to the agent that needs it.

Consider a synthesis agent that frequently needs to verify simple facts during report generation. The naive design routes all verification requests back to the coordinator, which delegates to the search agent, waits for results, and returns them. For 85% of verifications — simple lookups that take milliseconds — this round-trip overhead is wasteful.

The fix: give the synthesis agent a scoped `verify_fact` tool that handles simple lookups directly. Complex verifications (requiring multiple sources, cross-referencing, or nuanced judgement) still route through the coordinator. The 85% simple case is handled locally; the 15% complex case uses the full pipeline.

When reviewing an implementation, check for this pattern: a high-frequency, simple cross-role capability that is being routed through the coordinator on every call is a latency anti-pattern. The fix is a scoped cross-role tool for the common case, with the coordinator retained only for the complex minority.

### Replacing Generic Tools with Constrained Alternatives

Instead of giving a subagent `fetch_url` (which can fetch anything from anywhere), give it `load_document` that validates document URLs only. The constrained tool:

- Prevents misuse (the agent cannot fetch arbitrary URLs)
- Makes the tool's purpose clearer (the description is specific, not generic)
- Reduces the risk of unintended side effects (no fetching of non-document resources)

This is the principle of **least privilege applied to tool design**. Each tool should do exactly what the agent needs and nothing more. When reviewing an implementation, flag generic, open-ended tools (`fetch_url`, `run_query`, `execute`) handed to specialised agents where a constrained equivalent would suffice.

### Role-Specific Tool Scoping in Practice

Here is how tool distribution looks in a well-designed multi-agent research system:

| Agent | Tools (4-5 each) |
| --- | --- |
| Web Search | `search_web`, `fetch_page`, `extract_links`, `save_snippet` |
| Document Analysis | `extract_metadata`, `extract_data_points`, `summarize_content`, `verify_claim` |
| Synthesis | `compile_report`, `verify_fact` (scoped), `format_citation`, `assess_coverage` |
| Coordinator | `Agent` (formerly `Task`, used to spawn subagents), `review_output`, `request_revision` |

Each agent has exactly the tools it needs. The synthesis agent has a scoped `verify_fact` for simple lookups. The coordinator controls the workflow without having access to domain-specific tools.

> **Key Concept**
> The optimal range is 4-5 tools per agent, scoped to its role. For high-frequency simple operations, add a scoped cross-role tool directly to the agent that needs it — this avoids coordinator round-trip latency for the common case.

## Audit Checklist

- [ ] Each agent has roughly 4-5 tools, scoped to a single role — no agent carries a toolkit that spans multiple specialisations.
- [ ] No agent holds tools outside its role (e.g. a synthesis agent has no `web_search`, a search agent has no document-analysis tools).
- [ ] Each step's `tool_choice` matches intent: `"auto"` where conversational replies are valid, `"any"` where a structured tool call is mandatory but the tool is the model's choice.
- [ ] Mandatory first steps use forced selection (`{ "type": "tool", "name": ... }`) rather than relying on prompt instructions to enforce ordering.
- [ ] High-frequency, simple cross-role lookups use a scoped cross-role tool on the requesting agent instead of a coordinator round trip; only complex cases route through the coordinator.
- [ ] Generic, open-ended tools (`fetch_url`, `run_query`) are replaced with constrained equivalents (`load_document`) that enforce least privilege.
- [ ] The coordinator controls workflow (spawn, review, request revision) without holding domain-specific tools.

## Sources

- [Tool use — Anthropic API Documentation](https://platform.claude.com/docs/en/build-with-claude/tool-use) — Anthropic
- [Claude Agent SDK — Tool Configuration](https://platform.claude.com/docs/en/agent-sdk/overview) — Anthropic
