---
domain: 1 - Agentic Architecture & Orchestration
module: "1.3"
title: "Subagent Invocation and Context Passing"
---

# 1.3 Subagent Invocation and Context Passing

## Overview

This module covers the mechanics of how a coordinator actually invokes subagents and passes information between them. Module 1.2 (Multi-Agent Orchestration) covers the architecture; this module covers the wiring.

### The Task Tool

The **Task tool** is the mechanism for spawning subagents from a coordinator. It is not a suggestion or a convention — it is the specific API mechanism that makes multi-agent orchestration work in the Claude Agent SDK. Current Claude Code (v2.1.63, June 2026) renamed it to `Agent`; the name `Task` still works as an alias, and the Agent SDK emits `Agent` in tool-use blocks. When reviewing an implementation, expect to see either name — `Task` in older or SDK-facing code, `Agent` in current Claude Code — and treat them as the same mechanism.

There is a critical configuration requirement: **the coordinator's `allowedTools` must include `"Task"`** (or `"Agent"`, its current name in Claude Code). Without this, the coordinator physically cannot spawn subagents. This is a binary gate, not a soft preference. If neither `Task` nor `Agent` is in `allowedTools`, the coordinator has no way to invoke subagents at all.

Each subagent is defined by an **AgentDefinition** that specifies three things:

- **Description** — what the subagent does (used by the coordinator to decide when to invoke it).
- **System prompt** — the instructions the subagent follows.
- **Tool restrictions** — which tools the subagent can access (scoped to its role).

> **Key Concept**
> The coordinator's `allowedTools` must include `"Task"` (or `"Agent"`, its current name) to spawn subagents. This is a hard requirement. Without it, the coordinator cannot invoke any subagent regardless of how they are defined.

### Context Passing: The Make-or-Break Detail

Context passing is where most multi-agent systems fail in practice. The principle from module 1.2 (Multi-Agent Orchestration) applies directly here: subagents have isolated context. They receive only what the coordinator explicitly includes in their prompt.

There are three rules for effective context passing:

**Rule 1: Include complete findings from prior agents.** If the synthesis subagent needs web search results and document analysis output, the coordinator must pass both — in full — in the synthesis subagent's prompt. Do not assume the synthesis agent can "look up" prior results. It cannot.

**Rule 2: Use structured data formats that separate content from metadata.** When passing research findings between agents, the data must include both the content (the claim, the fact, the analysis) and the metadata (source URL, document name, page number). If you pass content without metadata, the downstream agent cannot attribute claims to sources.

This is a common failure pattern: a synthesis agent produces a report with unsourced claims. The web search and document analysis subagents are working correctly. The root cause is that the coordinator passed content without structured metadata — the synthesis agent literally had no source information to include.

**Rule 3: Design coordinator prompts that specify goals, not procedures.** The coordinator prompt should tell subagents what to achieve and what quality criteria to meet, not step-by-step instructions for how to do it. Goal-oriented prompts enable subagent adaptability. Procedural instructions constrain subagents and prevent them from adjusting their approach when they encounter unexpected situations.

> **Common Mistake**
> When a synthesis agent produces unsourced claims, the reflex is to blame the synthesis agent's prompt or to give it direct tool access — reject both. The real defect is upstream: the coordinator passed content without the structured metadata the synthesis agent needs to cite. Fix the context passing, not the synthesis agent.

### Structured Metadata Format

The structured data format for inter-agent context passing should separate content from metadata cleanly. A practical format looks like this:

```json
{
  "findings": [
    {
      "claim": "Solar panel efficiency has increased 25% in the last decade",
      "source_url": "https://example.com/solar-report",
      "document_name": "Annual Solar Industry Report 2024",
      "page_number": 14,
      "confidence": "high",
      "retrieved_by": "web_search_agent"
    }
  ]
}
```

Each finding carries its source attribution as metadata. When the synthesis agent receives this structured data, it has everything it needs to produce a properly cited report.

### Parallel Spawning

When a coordinator needs to invoke multiple subagents for independent tasks, it should **emit multiple Task tool calls in a single response** rather than invoking them one at a time across separate turns.

Sequential spawning (one subagent per coordinator turn) introduces unnecessary latency. If the web search agent and document analysis agent can work independently, there is no reason to wait for one to finish before starting the other.

When reviewing an implementation for latency, check independent subagent tasks: they should be spawned in parallel, "in a single response" or "simultaneously". Sequential invocation of independent work is a latency anti-pattern.

> **Key Concept**
> Spawn independent subagents in parallel by emitting multiple Task tool calls in a single coordinator response. This reduces latency compared to sequential invocation across separate turns.

### fork_session

`fork_session` creates **independent branches from a shared analysis baseline**. After a coordinator has completed an initial analysis (reading a codebase, understanding a problem), it can fork the session to explore divergent approaches.

Example: after analysing a codebase, the coordinator forks to compare two testing strategies. Each fork operates independently after the branching point — they do not see each other's results, and changes in one fork do not affect the other.

**fork_session is not the same as --resume.** Resume continues a specific named session. Fork creates a new independent branch. This distinction matters when auditing session handling: use fork when you need divergent exploration from a shared starting point, and use resume when you want to continue the same line of investigation. Using one where the other is required is a design error.

### Practical Example: Attribution Failure

A multi-agent research system has three agents: web search, document analysis, and synthesis. The web search agent returns well-sourced results with URLs and titles. The document analysis agent returns detailed analysis with page references.

The coordinator passes the content from both agents to the synthesis agent but strips the metadata — it sends the claims and analysis text without source URLs, document names, or page numbers. The synthesis agent produces an excellent summary with no source attribution.

The fix is not to modify the synthesis agent's prompt (it cannot cite sources it does not have). The fix is to require the coordinator to pass structured metadata alongside content, preserving the source URL, document name, and page number for every finding.

## Audit Checklist

- [ ] The coordinator's `allowedTools` includes `"Task"` (or `"Agent"`) — without it the coordinator cannot spawn any subagent.
- [ ] Each subagent has an AgentDefinition specifying description, system prompt, and role-scoped tool restrictions.
- [ ] The coordinator passes complete findings from prior agents in full — subagents do not rely on "looking up" prior results.
- [ ] Inter-agent payloads use a structured format that carries metadata (source URL, document name, page number) alongside content, so downstream agents can attribute claims to sources.
- [ ] Coordinator prompts specify goals and quality criteria, not step-by-step procedures.
- [ ] Independent subagent tasks are spawned in parallel via multiple Task calls in a single response, not sequentially across turns.
- [ ] `fork_session` is used for divergent exploration from a shared baseline, and `--resume` for continuing a named session — the two are not conflated.
- [ ] Unsourced-output bugs are traced to coordinator context passing, not misattributed to the synthesis agent's prompt or fixed by granting it direct tool access.

## Sources

- [Claude Agent SDK Overview](https://platform.claude.com/docs/en/agent-sdk/overview) — Anthropic
- [MCP Specification](https://github.com/modelcontextprotocol) — Anthropic / MCP
