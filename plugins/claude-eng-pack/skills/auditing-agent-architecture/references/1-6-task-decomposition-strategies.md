---
domain: 1 - Agentic Architecture & Orchestration
module: "1.6"
title: "Task Decomposition Strategies"
---

# 1.6 Task Decomposition Strategies

## Overview

Task decomposition is how you break complex work into manageable pieces for an agentic system. There are two distinct patterns, and a correct implementation selects between them based on the characteristics of the task. There is also a specific failure mode — attention dilution — that occurs when decomposition is insufficient. When reviewing an implementation, check that the pattern matches the task and that the design guards against attention dilution.

### Pattern 1: Fixed Sequential Pipelines (Prompt Chaining)

Fixed sequential pipelines break work into predetermined steps that execute in order. Each step takes the output of the previous step as input.

**How it works:** The workflow is defined in advance. Step 1 runs, its output feeds into Step 2, Step 2's output feeds into Step 3, and so on. The sequence does not change based on intermediate results.

**Example — Code review pipeline:**

- For each file, run a local analysis pass (style, bugs, complexity).
- After all local passes, run a cross-file integration pass (data flow, API consistency, import chains).
- Compile results into a unified review report.

**Best for:** Predictable, structured tasks where the steps are known in advance. Code reviews, document processing, data extraction pipelines, and compliance checks all fit this pattern.

**Advantages:** Consistent and reliable. The same input always follows the same path. Easy to debug — you know exactly which step produced which output. Easy to monitor — you can log the output of each step.

**Limitations:** Cannot adapt to unexpected findings. If Step 2 discovers something that should change the approach for Step 3, the pipeline cannot adjust. The steps are fixed regardless of what is discovered along the way.

> **Key Concept**
> Fixed sequential pipelines (prompt chaining) are best for predictable, structured tasks. They provide consistency and reliability but cannot adapt to unexpected findings during execution.

### Pattern 2: Dynamic Adaptive Decomposition

Dynamic adaptive decomposition generates subtasks based on what is discovered at each step. The plan evolves as the agent learns more about the problem.

**How it works:** The agent starts with a high-level goal, performs initial investigation, and generates a plan based on what it finds. As it executes the plan, it discovers new information that may change the remaining steps. The agent adapts the plan accordingly.

**Example — Adding tests to a legacy codebase:**

- Map the codebase structure (directories, modules, dependencies).
- Identify high-impact areas (most-used modules, modules with the most bugs, untested critical paths).
- Create a prioritised test plan based on the mapping.
- Start writing tests. Discover that Module A depends on Module B, which has no tests.
- Reprioritise: test Module B first so Module A's tests can rely on it.
- Continue adapting as new dependencies and issues emerge.

**Best for:** Open-ended investigation tasks where the full scope is not known at the start. Legacy system exploration, security audits, research projects, and debugging unfamiliar codebases all benefit from this pattern.

**Advantages:** Adapts to the problem. Can discover and respond to unexpected complexity. Produces more thorough results for open-ended tasks because it does not force-fit a predetermined plan.

**Limitations:** Less predictable. Execution time varies depending on what is discovered. Harder to estimate completion time or resource usage. More difficult to debug when things go wrong.

### Selecting the Right Pattern

When reviewing an implementation, match the pattern to the task characteristics:

| Task Characteristics | Pattern | Reasoning |
| --- | --- | --- |
| Steps known in advance, structured input | Fixed pipeline | Consistency and reliability outweigh adaptability |
| Open-ended, unknown scope | Dynamic decomposition | Adaptability is essential when the problem is not fully defined |
| Multi-file code review | Fixed pipeline | Per-file analysis + cross-file integration is predictable |
| Legacy codebase exploration | Dynamic decomposition | Dependencies and issues emerge during investigation |
| Document extraction | Fixed pipeline | Fields and format are predetermined |
| Debugging an unfamiliar system | Dynamic decomposition | Root cause is unknown; investigation must adapt |

> **Common Mistake**
> Using a fixed pipeline for an open-ended investigation task, or dynamic decomposition for a structured processing task, is a frequent design error. Reaching for dynamic decomposition because it "sounds more sophisticated" adds unpredictability and debugging cost with no benefit when the steps are already known. Match the pattern to the task characteristics, not to what sounds more capable.

### The Attention Dilution Problem

Attention dilution is a specific failure mode that occurs when an agent processes too many items in a single pass. The result is inconsistent depth — the agent produces thorough analysis for some items and misses obvious issues in others.

**The telltale symptoms:**

- Detailed feedback for the first few files, increasingly shallow analysis for later files.
- A pattern flagged as problematic in one file while identical code is approved in another file.
- Obvious bugs missed in some files while minor style issues are caught in others.

**Why it happens:** The model allocates attention across all items in the context. When there are too many items, attention per item decreases. Early items get disproportionate attention; later items get skimmed.

**The fix: Multi-pass architecture.** Split the work into two layers:

- **Per-item local analysis passes**: analyse each file (or document, or module) individually in its own pass. Each pass has the full attention budget focused on a single item.
- **Cross-item integration pass**: after all local passes complete, run a separate pass that looks across all items for cross-cutting concerns (data flow issues, inconsistent pattern usage, cross-file dependencies).

The per-item passes catch local issues consistently because each item gets dedicated attention. The integration pass catches cross-item issues because it focuses specifically on relationships between items rather than trying to do everything at once.

### Practical Example: The 14-File Code Review

A code review agent processes 14 files in a single pass. The results:

- Files 1-5: detailed feedback with specific line references, bug identification, and improvement suggestions.
- Files 6-9: moderate feedback with some issues identified but less thorough analysis.
- Files 10-14: superficial feedback that misses obvious null pointer bugs and SQL injection vulnerabilities.
- A `forEach` loop flagged as inefficient in File 3, while identical code in File 11 receives no comment.

This is attention dilution. The fix is not a better model, a larger context window, or a more detailed prompt. The fix is structural: split into 14 per-file analysis passes (each focused on one file) plus a cross-file integration pass (checking for data flow issues and pattern consistency across all files).

The multi-pass approach catches the null pointer bugs in Files 10-14 (because each file gets its own dedicated pass) and identifies the inconsistent `forEach` evaluation (because the integration pass specifically checks for cross-file pattern consistency).

## Audit Checklist

- [ ] Each decomposition uses the pattern that matches the task: fixed sequential pipeline for predictable, structured work; dynamic adaptive decomposition for open-ended work with unknown scope.
- [ ] Dynamic decomposition is not used where the steps are known in advance (avoid unnecessary unpredictability and debugging cost).
- [ ] Fixed pipelines are not used for open-ended investigation tasks where the plan must evolve as findings emerge.
- [ ] Multi-item work (many files, documents, or modules) is not processed in a single pass — check for attention-dilution symptoms such as decreasing depth across items or the same pattern flagged in one item but approved in another.
- [ ] Multi-item analysis is structured as per-item local passes plus a separate cross-item integration pass, so each item gets its full attention budget.
- [ ] The cross-item integration pass explicitly checks cross-cutting concerns: data flow, API consistency, cross-file dependencies, and pattern consistency across items.
- [ ] Fixes for inconsistent-depth results are structural (multi-pass decomposition), not attempts to compensate with a bigger model, larger context window, or a more detailed single-pass prompt.
- [ ] Fixed pipelines that need to react to intermediate findings are re-examined — this is a signal the task may actually require dynamic decomposition or handoff (see module 1.4 (Workflow Enforcement and Handoff)).

## Sources

- [Claude Agent SDK Overview](https://platform.claude.com/docs/en/agent-sdk/overview) — Anthropic
- [Anthropic Prompt Engineering Guide](https://platform.claude.com/docs/en/build-with-claude/prompt-engineering/overview) — Anthropic
