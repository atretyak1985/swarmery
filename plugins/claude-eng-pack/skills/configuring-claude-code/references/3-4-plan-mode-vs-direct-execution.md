---
domain: 3 - Claude Code Configuration & Workflows
module: "3.4"
title: "Plan Mode vs Direct Execution"
---

# 3.4 Plan Mode vs Direct Execution

## Overview

Claude Code operates in two primary modes: plan mode and direct execution. Choosing the correct mode for a given task is a design decision, not a matter of preference — there are clear criteria for when each mode is appropriate. When reviewing an implementation or workflow, check that the mode selected matches the task's ambiguity and scope rather than its perceived difficulty.

### Plan Mode: When to Use It

Plan mode is for complex tasks where you need to explore the codebase, evaluate multiple approaches, and design a strategy before making changes. Prefer plan mode when:

- **Large-scale changes are involved.** Restructuring a monolith into microservices, reorganising a module system, or refactoring a core abstraction all require understanding the existing structure before changing it.
- **Multiple valid approaches exist.** When there are different ways to solve the problem (e.g., different integration architectures with different infrastructure requirements), you need to evaluate them before committing.
- **Architectural decisions are required.** Service boundaries, module dependencies, API contracts — these decisions have downstream consequences. Planning prevents costly rework.
- **Multi-file modifications are needed.** A library migration affecting 45+ files requires a consistent strategy. Without a plan, you risk applying the migration inconsistently across files.
- **Codebase exploration is necessary.** When you need to understand dependencies, trace data flows, or map the existing structure before changing anything.

Plan mode enables safe exploration and design. Claude reads the codebase, analyses dependencies, and proposes an approach — all without modifying any files.

### Direct Execution: When to Use It

Direct execution is for well-understood changes with clear, limited scope. Prefer direct execution when:

- **The change is well-scoped.** A single-file bug fix with a clear stack trace. Adding a date validation conditional. Updating a configuration value.
- **The correct approach is already known.** You know what needs to change, where, and how. There is no design decision to make.
- **The scope is limited.** One function, one file, one clear modification.

Direct execution skips the planning phase and makes changes immediately. For simple, well-defined tasks, the planning phase adds no value.

> **Key Concept**
> The decision is not about difficulty but about ambiguity. A difficult but well-defined bug fix (clear stack trace, single function, known cause) is direct execution. A seemingly simple feature request that could be implemented three different ways and affects multiple modules is plan mode.

### The Explore Subagent

The Explore subagent isolates verbose discovery output from the main conversation. During multi-phase tasks, codebase exploration produces extensive output: file listings, dependency graphs, code excerpts, and analysis notes. If this output flows into the main conversation, it fills the context window and degrades the quality of subsequent responses.

The Explore subagent:

1. Runs the exploration in isolation
2. Produces summaries of its findings
3. Returns those summaries to the main conversation
4. Keeps the main context window clean for the actual implementation work

Use the Explore subagent during multi-phase tasks where the discovery phase is verbose but the implementation phase needs focused context.

### The Hybrid Approach: Plan Then Execute

Combining plan mode for investigation with direct execution for implementation is common in practice. The pattern:

1. **Plan phase:** Use plan mode to explore the codebase, understand dependencies, evaluate approaches, and design the implementation strategy.
2. **Execute phase:** Switch to direct execution to implement the planned approach, file by file, with the strategy already decided.

For example, migrating from one logging library to another across 30 files:

- **Plan:** Identify all files importing the old library, map the API differences between old and new, design the migration pattern, check for edge cases.
- **Execute:** Apply the migration pattern to each file using the planned approach.

This hybrid is not plan OR direct — it is plan THEN direct. When reviewing a workflow for a multi-file migration or a task with an investigation phase followed by mechanical application, check that both phases are present rather than a single mode forced across the whole task.

### Decision Framework Summary

| Task characteristics | Mode |
| --- | --- |
| Architectural restructuring | Plan mode |
| Library migration (many files) | Plan mode (then direct execution) |
| Multiple valid implementation approaches | Plan mode |
| Codebase exploration needed | Plan mode (with Explore subagent) |
| Single-file bug fix with clear stack trace | Direct execution |
| Adding a validation check to one function | Direct execution |
| Configuration value update | Direct execution |
| Known fix, known location, known approach | Direct execution |

### Recognising Complexity Upfront

When the requirements already state that the task is complex (e.g., "restructure the monolith into microservices"), plan mode should be chosen immediately. The complexity is not something that might emerge later — it is stated in the task description. Choosing plan mode upfront avoids the wasted work of starting in direct execution and unwinding partial changes once the complexity becomes unavoidable.

> **Common Mistake**
> Defaulting to direct execution and planning to switch to plan mode only if complexity emerges. This looks reasonable — start simple, escalate when needed — but reject it when the requirements already declare the task complex or open-ended. Waiting for surprises means you begin implementing without a strategy and pay for the rework. Read the stated scope first; if it names architectural restructuring, a multi-file migration, or multiple valid approaches, start in plan mode.

## Audit Checklist

- [ ] Mode selection is driven by ambiguity and scope, not by the task's perceived difficulty.
- [ ] Plan mode is used for large-scale changes, architectural decisions, multi-file modifications, and cases with multiple valid approaches.
- [ ] Direct execution is used only for well-scoped changes with a known approach and known location (single function, single file, clear stack trace).
- [ ] Plan mode performs exploration and design without modifying any files.
- [ ] Verbose discovery is routed through the Explore subagent so file listings and dependency graphs do not flood the main context window; only summaries return to the main conversation.
- [ ] Multi-file migrations and investigation-then-implementation tasks use the plan-then-execute hybrid rather than a single mode forced across the whole task.
- [ ] Tasks whose requirements already state complexity start in plan mode immediately, rather than starting in direct execution and switching only if complexity emerges.

## Sources

- [Claude Code Plan Mode Documentation](https://code.claude.com/docs/en/commands#plan) — Anthropic
