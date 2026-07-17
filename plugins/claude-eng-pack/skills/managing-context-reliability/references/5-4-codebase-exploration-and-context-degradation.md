---
domain: 5 - Context Management & Reliability
module: "5.4"
title: "Codebase Exploration & Context Degradation"
---

# 5.4 Codebase Exploration & Context Degradation

## Overview

Large codebase exploration is one of the most context-intensive tasks a Claude-based agent performs. Whether an agent is exploring an unfamiliar repository, tracing dependency chains, or understanding legacy systems, extended sessions create a specific failure mode: context degradation. This is not about running out of tokens. It is about the model losing grip on earlier findings as the context fills with verbose discovery output.

When reviewing an implementation that runs long exploration sessions, treat context degradation as a first-class failure mode to design against — not an edge case that only appears at the token limit.

### Context Degradation

Context degradation manifests as a specific, observable behaviour: the model starts referencing "typical patterns" instead of the specific classes, methods, and dependency chains it discovered earlier in the session. After investigating several modules, the agent might say "this follows the typical repository pattern" instead of "the OrderRepository class at src/repos/order.ts implements the base Repository<T> interface with custom caching in the findById method."

This is the signal to watch for when auditing an exploration agent's transcripts: a drift from specific, file-and-class-level references toward generic pattern language.

This happens because:

- Each exploration step generates verbose output (file contents, search results, directory listings).
- This output accumulates in the conversation context.
- Earlier, precise discoveries are pushed further into the context while more recent verbose output dominates.
- The model's attention shifts to recent output and it loses specific references to earlier findings.

The critical insight is that context degradation is not a token limit problem. Increasing the context window does not fix it. The model is not running out of space — it is losing track of specific details as they get buried under newer, more verbose output.

> **Common Mistake**
> Enlarging the context window looks like a plausible fix for context degradation — reject this. A larger window gives the model more room to bury early findings under later verbose output; it does not restore the model's grip on those findings. The fix is to persist discoveries outside the context, not to expand it.

### Scratchpad Files

The primary mitigation for context degradation is scratchpad files. The agent writes key findings to a file and references it for subsequent questions. This persists knowledge outside the conversation context, making it immune to context degradation.

```markdown
# Exploration Scratchpad — Order Service

## Key Classes
- `OrderRepository` (src/repos/order.ts) — implements Repository<T>, custom findById caching
- `OrderService` (src/services/order.ts) — orchestrates OrderRepository + PaymentGateway
- `RefundProcessor` (src/services/refund.ts) — depends on OrderService.getOrderWithItems()

## Dependency Chain
RefundProcessor → OrderService → OrderRepository → PostgreSQL
RefundProcessor → PaymentGateway → Stripe API

## Critical Findings
- RefundProcessor has no retry logic for Stripe API failures
- OrderRepository caches by orderId but cache invalidation on status change is missing
- Test coverage: OrderService has 87% coverage, RefundProcessor has 12%
```

When the agent needs to reference earlier discoveries, it reads the scratchpad file instead of relying on conversation context. This is a deliberate strategy, not a fallback — agents should be instructed to maintain scratchpad files from the start of any extended exploration session. When auditing, check that scratchpad maintenance is instructed up front rather than triggered reactively once the context is already degraded.

### Subagent Delegation

Spawning subagents for specific investigation tasks is the second major mitigation strategy. Instead of the main agent doing all exploration directly (filling its context with verbose output from every file read and search), delegate specific questions to subagents:

- "Find all test files for the order service and report their coverage status"
- "Trace the refund flow from API endpoint to database and list all intermediate services"
- "Identify all external API integrations and their error handling patterns"

Each subagent operates with its own isolated context. It can explore verbosely without polluting the main agent's context. It returns a structured summary to the coordinator, which keeps only the key findings.

This is not just about parallelisation — it is about context isolation. The main agent's context stays clean for high-level coordination while subagents handle the verbose exploration. When reviewing a multi-agent exploration design, confirm that the coordinator retains summaries rather than raw exploration output; a coordinator that ingests full subagent transcripts loses the isolation benefit.

### Summary Injection Between Phases

When exploration happens in phases (Phase 1: understand the architecture, Phase 2: investigate specific components), summarise key findings from Phase 1 before spawning Phase 2 subagents. Inject these summaries into the initial context of Phase 2 subagents.

This prevents the "cold start" problem where Phase 2 subagents duplicate Phase 1 exploration because they were not given the previous findings. It also ensures that Phase 2 agents have the architectural understanding needed to ask the right questions.

```
Phase 1 Summary (injected into Phase 2 subagent prompts):
- The system follows a layered architecture: Controllers → Services → Repositories → Database
- The refund flow passes through: RefundController → RefundProcessor → OrderService → PaymentGateway
- Key concern: RefundProcessor has no retry logic for external API failures
- Phase 2 objective: Investigate error handling in RefundProcessor and PaymentGateway
```

When auditing a phased exploration pipeline, look for the summary hand-off between phases. Its absence is what produces redundant re-exploration and phase-two agents that lack the context to ask targeted questions.

### The /compact Command

Claude Code provides a `/compact` command specifically for reducing context usage during extended sessions. When context fills with verbose discovery output — file contents, search results, directory listings — `/compact` summarises the conversation to free up space while preserving key information.

Use `/compact` proactively during extended exploration sessions, not just when you hit context limits. It is a tool for maintaining context quality, not just context quantity. An implementation that only reaches for `/compact` at the token limit is treating it as an overflow valve; the better pattern is to compact routinely to keep context quality high throughout the session.

### Crash Recovery via Structured State Manifests

Extended exploration sessions can fail due to session crashes, network interruptions, or context exhaustion. Without recovery mechanisms, all exploration progress is lost.

The fix is structured state persistence. Each agent exports its current state to a known file location (a manifest). This manifest includes:

- What has been explored (files read, searches performed)
- Key findings discovered so far
- Current phase and next steps
- Any pending questions or unresolved issues

```json
{
  "sessionId": "explore-order-service-001",
  "phase": 2,
  "exploredPaths": [
    "src/repos/order.ts",
    "src/services/order.ts",
    "src/services/refund.ts"
  ],
  "keyFindings": {
    "architecture": "Layered: Controllers → Services → Repositories → DB",
    "criticalIssue": "RefundProcessor has no retry logic for Stripe API failures",
    "testCoverage": {"OrderService": "87%", "RefundProcessor": "12%"}
  },
  "nextSteps": [
    "Investigate PaymentGateway error handling",
    "Review RefundProcessor test files",
    "Check cache invalidation logic in OrderRepository"
  ]
}
```

On resume, the coordinator loads this manifest and injects it into agent prompts. The agent picks up where it left off without repeating earlier exploration. When reviewing an implementation, verify both halves of this loop exist: state is exported to a known location during the session, and the manifest is actually loaded and injected on resume. A manifest that is written but never read back provides no recovery.

> **Key Concept**
> Context degradation is not a token limit problem — it is the model losing grip on specific findings as verbose output accumulates. Scratchpad files persist key discoveries outside the context. Subagent delegation isolates verbose exploration. Crash recovery manifests prevent progress loss across sessions.

## Audit Checklist

- [ ] Context degradation is treated as an attention/recall problem, not a token-limit one — enlarging the context window is not relied on as the fix.
- [ ] Transcripts are monitored for drift from specific references (class names, file paths, dependency chains) toward generic "typical pattern" language.
- [ ] Extended exploration agents write key findings to scratchpad files and read them back rather than relying on conversation context.
- [ ] Scratchpad maintenance is instructed from the start of a session, not triggered reactively after context has already degraded.
- [ ] Verbose exploration is delegated to subagents with isolated context, and the coordinator retains only structured summaries — not raw subagent transcripts.
- [ ] Phased exploration injects prior-phase summaries into new subagent prompts to prevent cold-start re-exploration.
- [ ] `/compact` is used proactively during long sessions, not only when context limits are reached.
- [ ] Each agent exports a structured state manifest (explored paths, key findings, current phase, next steps) to a known location for crash recovery.
- [ ] On resume, the coordinator loads the manifest and injects it so agents continue without repeating prior exploration.

## Sources

- Claude Code Documentation — Context Management — Anthropic
- Claude Code Documentation — Commands — Anthropic
