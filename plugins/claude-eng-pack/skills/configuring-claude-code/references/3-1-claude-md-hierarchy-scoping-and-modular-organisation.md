---
domain: 3 - Claude Code Configuration & Workflows
module: "3.1"
title: "CLAUDE.md Hierarchy, Scoping, and Modular Organisation"
---

# 3.1 CLAUDE.md Hierarchy, Scoping, and Modular Organisation

## Overview

Claude Code reads configuration from CLAUDE.md files at three distinct levels. Understanding which level applies where — and diagnosing when the wrong level has been used — is a foundational skill when auditing a Claude Code setup for correctness.

### The Three-Level Hierarchy

**User-level: `~/.claude/CLAUDE.md`**

This file applies only to you. It is stored in your home directory, outside any repository. It is not version-controlled. It is not shared via git. When a new team member clones your repository, they do not get these instructions. Use this level for strictly personal preferences — verbosity settings, preferred output style, personal shortcuts.

**Project-level: `.claude/CLAUDE.md` or root `CLAUDE.md`**

This file applies to everyone working on the project. It lives inside the repository and is version-controlled. Every developer who clones or pulls the repo receives these instructions automatically. Team-wide standards belong here: naming conventions, error handling patterns, testing requirements, architecture decisions, code review checklists.

Both `.claude/CLAUDE.md` (inside the `.claude` directory) and a `CLAUDE.md` at the repository root are valid project-level locations. When reviewing a repo, treat either path as project-level.

**Directory-level: subdirectory `CLAUDE.md` files**

These files apply when working in that specific directory. Use them for package-specific conventions that differ from the project root. For example, a `/packages/api/CLAUDE.md` might contain REST-specific conventions that do not apply to the frontend package.

### Loading Order and Conflict Handling

CLAUDE.md files are not a strict-precedence config. The Anthropic memory docs are explicit: "All discovered files are concatenated into context rather than overriding each other." Every applicable file is loaded into the same context window — none replaces another.

The docs describe a documented **load order**, not a precedence chain:

1. Files are ordered from **broadest scope to most specific**. A project instruction appears in context after a user instruction. Across the directory tree, "content is ordered from the filesystem root down to your working directory," so "instructions closer to where you launched Claude are read last."
2. Within a directory, `CLAUDE.local.md` is appended after `CLAUDE.md`, so your personal notes are the last thing Claude reads at that level.

This is not a winner-take-all hierarchy. The docs make this explicit: **"if two rules contradict each other, Claude may pick one arbitrarily."** CLAUDE.md is delivered as a user message — not as part of the system prompt — and Anthropic says "there's no guarantee of strict compliance." Treat CLAUDE.md as guidance the model usually follows, not as a configuration layer with deterministic overrides.

Practical consequence: if a rule **must** be honoured on every run (a blocked tool, a required formatter, a permission policy), do not rely on CLAUDE.md scoping to enforce it. Encode it in `settings.json` (which the client enforces regardless of what Claude decides) or in a hook (which fires at a fixed lifecycle event). The Anthropic docs spell this out directly: "Settings rules are enforced by the client regardless of what Claude decides to do. CLAUDE.md instructions shape Claude's behavior but are not a hard enforcement layer."

> **Don't confuse CLAUDE.md with settings.json**
>
> `settings.json` has a strict precedence chain (managed policy > local > project > user, with managed always winning). CLAUDE.md does not — files are concatenated and conflicts may resolve arbitrarily. When you need to answer "which CLAUDE.md wins on a conflict?", the docs-honest answer is "neither is guaranteed to — move the rule to `settings.json` or a hook." Be wary of claims that "more specific scope wins" or "user-level overrides project-level": both are paraphrases the official docs never make.

### Modular Organisation with @ path imports

Past a few hundred lines, one CLAUDE.md becomes a slog to maintain. The `@` syntax lets you split it across files and reference them from the main one. The directive is just `@` followed by a path. There is no `@import` keyword, even though half the docs you'll find online write it that way.

The syntax in your CLAUDE.md:

```markdown
# .claude/CLAUDE.md

Coding standards:

@./standards/naming-conventions.md
@./standards/error-handling.md
@./standards/testing-requirements.md
```

Each `@<path>` line gets that file inlined into the CLAUDE.md at load time. Per-package CLAUDE.md files can import only the standards that apply to them. The API package pulls in API conventions, the frontend pulls in component rules. No duplication.

One thing the docs are quiet about: imports load eagerly. The referenced file gets inlined the moment Claude reads your CLAUDE.md, exactly as if you'd pasted it in. So splitting a 600-line CLAUDE.md into six 100-line imports makes the source nicer to work in, but the context Claude actually sees is the same size. If you want to shrink per-session context, the tool for the job is `.claude/rules/` with path-scoped frontmatter (see module 3.3, Path-Specific Rules for Conditional Convention Loading). Those files only load when Claude is working in matching paths.

### CLAUDE.local.md, local-only overrides

`CLAUDE.local.md` lives next to `CLAUDE.md` at any level in the hierarchy and loads the same way, with three small differences worth knowing:

- **Loading order.** `CLAUDE.local.md` is appended after `CLAUDE.md` at the same level, so it has the last word on conflicts within that directory.
- **Gitignored by convention.** The `.local` suffix flags files you don't want committed. Most teams add `CLAUDE.local.md` to `.gitignore` so personal tweaks stay personal.
- **What it's for.** The shared `CLAUDE.md` is the team's rules. The `CLAUDE.local.md` next to it is your own quirks for this repo: a favourite scratchpad path, a verbose explanation you keep needing to re-paste, a temporary debugging note you'll delete next week.

Think of `CLAUDE.local.md` as a project-scoped version of `~/.claude/CLAUDE.md`: same idea, narrower scope. If you find yourself reaching for it to express a team rule, that rule belongs in `CLAUDE.md` instead.

### The .claude/rules/ Directory

As an alternative to a single CLAUDE.md file, the `.claude/rules/` directory holds topic-specific rule files:

- `testing.md` — test naming, assertion patterns, fixture usage
- `api-conventions.md` — endpoint naming, request/response schemas
- `deployment.md` — deployment checklist, environment configuration

Each file can optionally include YAML frontmatter with path scoping (covered in detail in module 3.3, Path-Specific Rules for Conditional Convention Loading). Without frontmatter, rules files load for all sessions.

### The /memory Command

The `/memory` command verifies which memory files are currently loaded in your session. This is the debugging tool for inconsistent behaviour across sessions or developers. When Claude Code behaves differently for different team members, `/memory` reveals whether the expected configuration files are actually loaded.

> **Key Concept**
>
> The `/memory` command does not load configuration files — it reveals which ones are already loaded. Configuration files load automatically based on their level and location. Use `/memory` to diagnose, not to activate.

### Diagnostic Scenario: New Team Member Not Receiving Instructions

This is one of the most common misconfigurations to check for. The scenario presents itself as follows:

Developer A has been on the team for months. Claude Code follows all the team's conventions perfectly — API naming, test structure, error handling. Developer B joins the team, clones the repository, and Claude Code produces inconsistent results that ignore the conventions.

The root cause is always the same: the conventions are stored in Developer A's user-level config (`~/.claude/CLAUDE.md`) instead of the project-level config (`.claude/CLAUDE.md` or root `CLAUDE.md`). User-level config is not shared via git. Developer B never received the instructions.

The fix: move instructions from user-level to project-level configuration.

When reviewing an implementation, diagnose this quickly: whenever you see "new team member" paired with "inconsistent behaviour," check where the configuration lives.

## Audit Checklist

- [ ] Team-wide conventions (naming, error handling, testing, architecture) live in project-level config (`.claude/CLAUDE.md` or root `CLAUDE.md`), not in a developer's user-level `~/.claude/CLAUDE.md`.
- [ ] Rules that must be honoured on every run (blocked tools, required formatters, permission policies) are encoded in `settings.json` or a hook, not left to CLAUDE.md scoping.
- [ ] No configuration relies on "more specific scope wins" or "user-level overrides project-level" behaviour, since contradictory CLAUDE.md rules may resolve arbitrarily.
- [ ] `@` path imports use a bare `@<path>` directive, not an `@import` keyword.
- [ ] `@` imports are not being used to shrink per-session context (they load eagerly); path-scoped `.claude/rules/` is used for conditional loading instead.
- [ ] `CLAUDE.local.md` contains only personal, repo-specific quirks and is gitignored; anything that is a team rule lives in `CLAUDE.md`.
- [ ] Directory-level `CLAUDE.md` files are reserved for package-specific conventions that differ from the project root.
- [ ] `/memory` is used to diagnose which memory files are loaded, not treated as a mechanism that activates them.

## Sources

- [How Claude remembers your project (CLAUDE.md and auto memory)](https://code.claude.com/docs/en/memory) — Anthropic
