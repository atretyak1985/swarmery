---
name: designing-tools-and-mcp
description: Use when creating or reviewing tool definitions for Claude — tool names, descriptions, input schemas, structured error responses, tool_choice configuration, MCP server setup and scoping, or built-in tool usage (Grep, Glob, Read, Edit). Also when Claude picks the wrong tool, confuses similar tools, retries hopeless failures, treats empty results as errors, or degrades because too many tools are exposed.
---

# Designing Tools and MCP

## Overview

Best-practice reference for the tool layer: how tools are named, described, split, scoped, and how their errors are structured so the model can act on them. Each reference module states the correct pattern, the named anti-patterns, and ends with an **Audit Checklist** of verifiable conditions.

## When to use

- Writing or reviewing tool schemas, descriptions, or an MCP server
- Debugging: misrouted tool calls, retry loops on permanent errors, "no results" handled as failure, tool overload
- Deciding: one tool or several, `tool_choice` mode, build vs use an existing MCP server, which built-in tool fits

Not for the agent loop itself (use auditing-agent-architecture) or output schemas for extraction (use engineering-prompts-and-output).

## Quick reference

| Module | Read when the question is about |
|---|---|
| `references/2-1-tool-interface-design.md` | Descriptions that route correctly, splitting overloaded tools, naming |
| `references/2-2-structured-error-responses.md` | Error categories, `isRetryable`, access failure vs valid empty result |
| `references/2-3-tool-distribution-and-tool-choice.md` | Tool overload, `tool_choice` config, role-scoped tool sets |
| `references/2-4-mcp-server-integration.md` | MCP scoping hierarchy, env-var expansion, resources, build-vs-use |
| `references/2-5-built-in-tools.md` | Grep vs Glob, Read/Write/Edit, incremental codebase understanding |

## How to audit

1. Match the tool surface under review to modules in the table; read those files.
2. Run each module's **Audit Checklist** item by item against the actual definitions/config.
3. Report every unchecked item as a finding with `file:line` evidence and the module's recommended fix.

For a fast full-domain sweep, `checklists.md` aggregates all five checklists.

## Related

How tool errors should propagate between agents: managing-context-reliability (`5-3`). Forcing structured output via tools: engineering-prompts-and-output (`4-3`).
