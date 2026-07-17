---
name: managing-context-reliability
description: Use when reviewing long-running or multi-agent Claude systems for reliability — context window management and summarisation, escalation and ambiguity resolution, error propagation between agents, codebase exploration and context degradation, human review and confidence calibration, or information provenance and multi-source synthesis. Also when quality degrades over long sessions, agents silently swallow failures, mid-context details get lost, or synthesised answers lose attribution.
---

# Managing Context and Reliability

## Overview

Best-practice reference for the reliability layer: keeping context healthy over long sessions, escalating instead of guessing, propagating errors with structure, calibrating automation against human review, and preserving provenance. Each reference module states the correct pattern, the named anti-patterns, and ends with an **Audit Checklist** of verifiable conditions.

## When to use

- Reviewing a long-running agent, a multi-agent pipeline's failure handling, or a synthesis/research system
- Debugging: degrading answer quality over time, lost mid-context details, silently dropped errors, unattributed claims
- Deciding: summarisation strategy, escalation criteria, sampling strategy for human review, when to trust automation

Not for the loop/orchestration structure itself (use auditing-agent-architecture).

## Quick reference

| Module | Read when the question is about |
|---|---|
| `references/5-1-context-window-management.md` | Summarisation traps, lost-in-the-middle, tool-result trimming, prompt caching |
| `references/5-2-escalation-and-ambiguity-resolution.md` | Valid vs unreliable escalation triggers, explicit escalation criteria |
| `references/5-3-error-propagation-in-multi-agent-systems.md` | Structured error context, coverage annotations, local recovery |
| `references/5-4-codebase-exploration-and-context-degradation.md` | Scratchpads, subagent delegation, summary injection, `/compact`, crash recovery |
| `references/5-5-human-review-and-confidence-calibration.md` | Stratified sampling, field-level calibration, validation before automation |
| `references/5-6-information-provenance-and-multi-source-synthesis.md` | Claim-source mapping, conflicts, temporal awareness, attribution |

## How to audit

1. Match the system under review to modules in the table; read those files.
2. Run each module's **Audit Checklist** item by item against the actual code/config.
3. Report every unchecked item as a finding with `file:line` evidence and the module's recommended fix.

For a fast full-domain sweep, `checklists.md` aggregates all six checklists.

## Related

Loop and orchestration structure: auditing-agent-architecture. Review-pipeline calibration: engineering-prompts-and-output (`4-6`).
