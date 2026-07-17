---
name: auditing-agent-architecture
description: Use when designing or reviewing Claude-based agent systems — agentic loops and stop_reason handling, multi-agent orchestration, coordinators, subagent invocation and context passing, workflow enforcement, Agent SDK hooks (PreToolUse/PostToolUse), task decomposition, or session state and resumption. Also when an agent terminates prematurely, loops forever, repeats work, or subagents return inconsistent or incomplete results.
---

# Auditing Agent Architecture

## Overview

Best-practice reference for the architecture layer of Claude-based agents: the execution loop, orchestration topology, subagent contracts, enforcement gates, hooks, decomposition, and session state. Each reference module states the correct pattern, the named anti-patterns, and ends with an **Audit Checklist** of verifiable conditions.

## When to use

- Reviewing or designing an agent loop, coordinator, or subagent structure
- Debugging: premature termination, infinite loops, dropped tool results, stalled or repeating agents, coverage gaps between subagents
- Deciding: prompt guidance vs hard gates, hooks vs prompts, fixed pipeline vs dynamic decomposition

Not for tool schemas or MCP servers (use designing-tools-and-mcp) or context-window degradation (use managing-context-reliability).

## Quick reference

| Module | Read when the question is about |
|---|---|
| `references/1-1-agentic-loops.md` | Loop lifecycle, `stop_reason` branching, termination anti-patterns |
| `references/1-2-multi-agent-orchestration.md` | Hub-and-spoke coordination, context isolation, decomposition coverage gaps |
| `references/1-3-subagent-invocation-and-context-passing.md` | Task tool, structured metadata handoff, parallel spawning, `fork_session` |
| `references/1-4-workflow-enforcement-and-handoff.md` | Prompt guidance vs programmatic gates, prerequisite gates, handoff protocols |
| `references/1-5-agent-sdk-hooks.md` | PreToolUse/PostToolUse policy enforcement, hooks vs prompt instructions |
| `references/1-6-task-decomposition-strategies.md` | Fixed sequential vs dynamic decomposition, attention dilution |
| `references/1-7-session-state-and-resumption.md` | Session persistence, stale context, targeted re-analysis |

## How to audit

1. Match the system under review to modules in the table; read those files.
2. Run each module's **Audit Checklist** item by item against the actual code/config.
3. Report every unchecked item as a finding with `file:line` evidence and the module's recommended fix.

For a fast full-domain sweep, `checklists.md` aggregates all seven checklists.

## Related

Error propagation between agents: managing-context-reliability (`5-3`). Review pipelines built on multiple instances: engineering-prompts-and-output (`4-6`).
