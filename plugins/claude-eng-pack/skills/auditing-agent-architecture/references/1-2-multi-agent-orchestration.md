---
domain: 1 - Agentic Architecture & Orchestration
module: "1.2"
title: "Multi-Agent Orchestration"
---

# 1.2 Multi-Agent Orchestration

## Overview

Multi-agent orchestration is how you build systems where multiple Claude agents work together on complex tasks. The recommended architecture is not arbitrary: prefer **hub-and-spoke** with a coordinator at the centre. When reviewing an implementation, this is the pattern to check for.

### Hub-and-Spoke Architecture

The architecture has two roles:

- **Coordinator agent**: sits at the centre. Receives the initial task, decomposes it, decides which subagents to invoke, passes context to them, aggregates their results, handles errors, and routes information between them.
- **Subagents**: the spokes. Each one handles a specialised task (web search, document analysis, synthesis, report generation). They receive instructions from the coordinator and return results to it.

The cardinal rule: **ALL communication flows through the coordinator.** Subagents never communicate directly with each other. Never. Not for efficiency, not for convenience, not for any reason. Every piece of information that moves between subagents passes through the coordinator.

> **Current state**
> The strict hub-and-spoke model is the recommended baseline. In current Claude Code a sub-agent can itself spawn sub-agents (nested parent-child delegation), so the "never, for any reason" absolute is a simplification rather than a hard product limit. When reviewing an implementation, treat direct subagent-to-subagent communication as an anti-pattern to flag: even where nesting is supported, delegation still flows through a parent, not laterally between peers.

This centralisation provides three things worth protecting:

1. **Observability** — you can log and monitor every message in one place.
2. **Consistent error handling** — the coordinator applies uniform error recovery policies.
3. **Controlled information flow** — the coordinator decides what context each subagent receives.

> **Key Concept**
> All inter-subagent communication flows through the coordinator. Subagents never communicate directly with each other. This is the foundational architectural constraint of hub-and-spoke orchestration.

### The Critical Isolation Principle

This is the single most commonly misunderstood concept in multi-agent systems, and it is where implementations most often go wrong.

**Subagents do NOT automatically inherit the coordinator's conversation history.** When the coordinator spawns a subagent, that subagent starts with only what the coordinator explicitly includes in its prompt. It has no access to:

- The coordinator's system prompt (unless explicitly included)
- Previous messages in the coordinator's conversation
- Results from other subagents (unless the coordinator passes them)
- Any "shared memory" or global state

**Subagents do NOT share memory between invocations.** If the coordinator calls the web search subagent twice, the second invocation has no knowledge of the first. Every invocation is independent.

This means the coordinator must be deliberate about context. Every piece of information a subagent needs must be explicitly included in its prompt. If the synthesis agent needs web search results, the coordinator must pass those results explicitly — the synthesis agent cannot "look them up" from a shared store. (Context passing is covered in depth in module 1.3, Subagent Invocation and Context Passing.)

> **Common Mistake**
> When a multi-agent system produces incomplete or incorrect output, the instinct is to blame the subagent that produced it and try to harden that subagent. Trace the failure to its origin instead — check whether the coordinator gave it the right input. Improving the wrong component leaves the actual defect in place.

### Coordinator Responsibilities

The coordinator has four key responsibilities. When auditing a system, verify each is actually implemented:

**1. Dynamic subagent selection.** The coordinator analyses query requirements and dynamically selects which subagents to invoke. It does NOT always route through the full pipeline. A simple factual question might only need the web search subagent, not the full research-analysis-synthesis chain. Routing every query through every subagent wastes time and resources.

**2. Research scope partitioning.** When delegating to multiple subagents, the coordinator partitions the research scope to minimise duplication. It assigns distinct subtopics or source types to each agent. For example, one agent searches academic papers while another searches news articles — they do not both search the same sources.

**3. Iterative refinement loops.** The coordinator evaluates synthesis output for gaps. If the synthesis is incomplete, it re-delegates to search and analysis subagents with targeted queries. It re-invokes synthesis until coverage is sufficient. This is not a single-shot process — it is an iterative cycle.

**4. Centralised communication routing.** All subagent communication routes through the coordinator for observability, consistent error handling, and controlled information flow.

### The Narrow Decomposition Failure

This is a specific failure pattern worth recognising. A representative case: a coordinator decomposes "impact of AI on creative industries" into only visual arts subtopics, missing music, writing, and film entirely.

The root cause is **the coordinator's task decomposition**, not any downstream agent. The web search agent searched thoroughly for what it was assigned. The synthesis agent synthesised everything it received. But the coordinator only assigned visual arts topics, so music, writing, and film were never researched.

The correct diagnostic move is to **trace failures to their origin**. When a multi-agent system produces a report that misses entire categories, do not blame the subagents — check the coordinator's decomposition.

This pattern applies broadly: if the output is incomplete in scope (not depth), the coordinator's decomposition is almost always the root cause. (Decomposition strategy is treated on its own in module 1.6, Task Decomposition Strategies.)

### Practical Example: Research System Coverage Gap

A multi-agent research system is tasked with "renewable energy technologies." The coordinator decomposes this into "solar panel efficiency" and "wind turbine design." Each subagent produces thorough, well-sourced research on its assigned topic.

The final report is comprehensive on solar and wind but says nothing about geothermal, tidal, biomass, or nuclear fusion. The coverage gap is not because the search was poor or the synthesis was weak — it is because the coordinator never assigned those subtopics.

The fix is not better search queries, not a more capable synthesis agent, and not more subagents. The fix is better coordinator decomposition that covers the full breadth of the topic.

## Audit Checklist

- [ ] All inter-subagent communication routes through the coordinator; there is no direct subagent-to-subagent (peer) messaging.
- [ ] Every subagent invocation receives all context it needs explicitly in its prompt — nothing relies on inherited conversation history, shared memory, or global state.
- [ ] Repeated invocations of the same subagent do not assume knowledge from prior invocations (each invocation is treated as independent).
- [ ] The coordinator selects subagents dynamically per query rather than routing every query through the full pipeline.
- [ ] Research scope is partitioned across subagents so they cover distinct subtopics or source types without duplicating work.
- [ ] The coordinator runs iterative refinement — it evaluates synthesis output for gaps and re-delegates until coverage is sufficient, rather than single-shot.
- [ ] Failure diagnosis traces incomplete or incorrect output to its origin (usually coordinator decomposition or the context passed in), not to the subagent that emitted it.
- [ ] Task decomposition covers the full breadth of the topic, so no whole categories are silently omitted (scope gaps, not depth gaps).

## Sources

- [Claude Agent SDK Overview](https://platform.claude.com/docs/en/agent-sdk/overview) — Anthropic
