---
name: configuring-claude-code
description: Use when setting up or auditing Claude Code in a repository — CLAUDE.md hierarchy and imports, .claude/rules, custom slash commands and skills, path-specific rules, plan mode vs direct execution, iterative refinement workflow, or CI/CD integration with headless -p mode. Also when instructions get ignored, conventions load in the wrong scope, CLAUDE.md has grown bloated, or a CI pipeline needs non-interactive Claude runs.
---

# Configuring Claude Code

## Overview

Best-practice reference for repository-level Claude Code setup: memory files, skills/commands, conditional convention loading, execution modes, and CI. Each reference module states the correct pattern, the named anti-patterns, and ends with an **Audit Checklist** of verifiable conditions.

## When to use

- Setting up a repo: CLAUDE.md structure, `.claude/rules/`, skills, slash commands
- Debugging: ignored instructions, conventions applied in the wrong directories, oversized memory files
- Deciding: plan mode vs direct execution, where a rule belongs, batch vs real-time review in CI

Not for prompt content of the tasks themselves (use engineering-prompts-and-output).

## Quick reference

| Module | Read when the question is about |
|---|---|
| `references/3-1-claude-md-hierarchy-scoping-and-modular-organisation.md` | Memory hierarchy, loading/conflict order, `@` imports, modular organisation |
| `references/3-2-custom-slash-commands-and-skills.md` | Skills system, scoping levels, frontmatter, skills vs CLAUDE.md |
| `references/3-3-path-specific-rules-for-conditional-convention-loading.md` | Glob-based rules vs directory/root CLAUDE.md |
| `references/3-4-plan-mode-vs-direct-execution.md` | When to plan, Explore subagent, plan-then-execute hybrid |
| `references/3-5-iterative-refinement-techniques.md` | Refinement hierarchy, batch vs sequential feedback, example-based communication |
| `references/3-6-ci-cd-integration.md` | Headless `-p` mode, structured output in CI, session isolation, batch vs real-time |

## How to audit

1. Match the repo setup under review to modules in the table; read those files.
2. Run each module's **Audit Checklist** item by item against the actual files (`CLAUDE.md`, `.claude/`, CI config).
3. Report every unchecked item as a finding with `file:line` evidence and the module's recommended fix.

For a fast full-domain sweep, `checklists.md` aggregates all six checklists.

## Related

Agent-side hooks and enforcement: auditing-agent-architecture (`1-4`, `1-5`). Batch API for CI-scale processing: engineering-prompts-and-output (`4-5`).
