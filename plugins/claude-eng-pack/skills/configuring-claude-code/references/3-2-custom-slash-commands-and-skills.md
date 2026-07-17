---
domain: 3 - Claude Code Configuration & Workflows
module: "3.2"
title: "Custom Slash Commands and Skills"
---

# 3.2 Custom Slash Commands and Skills

## Overview

Custom commands and skills have been merged into a single unified system: the **Skills system**. The two locations, `.claude/skills/` and `.claude/commands/`, create `/commands` that behave the same way, but their file structures differ. A skill is a **directory containing a `SKILL.md` file** (`.claude/skills/deploy/SKILL.md`); a command is a **flat Markdown file** (`.claude/commands/deploy.md`). A flat file placed directly inside `.claude/skills/` does not create a command. The `.claude/skills/` path is the canonical location. `.claude/commands/` still works for backward compatibility. When auditing an implementation, confirm the file shapes match these rules — a flat `.md` dropped into `.claude/skills/` is a common cause of a command silently failing to register.

### The Unified Skills System

Both paths produce the same result — a `/command` that developers can invoke:

- `.claude/commands/deploy.md` creates `/deploy` — a flat file whose filename becomes the command name
- `.claude/skills/deploy/SKILL.md` also creates `/deploy` — one directory per skill, named after the command, with `SKILL.md` as the required entrypoint inside it

The skills path is the recommended one because it adds features the commands alias does not: a supporting-files directory alongside the SKILL.md, automatic discovery so Claude can load a skill when it matches your intent, and precedence when a skill and a command share the same name (the skill wins). Both paths support the same YAML frontmatter (`context: fork`, `allowed-tools`, `argument-hint`) and both produce the same `/command`, so existing `.claude/commands/` files keep working unchanged.

### Two Scoping Levels

**Project-scoped (shared via git):**

Place skills in `.claude/skills/` (canonical) or `.claude/commands/` (alias) inside your repository. Both are version-controlled and shared via git. Every developer who clones or pulls the repository gets these commands automatically. Use for team-wide workflows: `/review`, `/deploy-check`, `/lint`, `/migration-guide`.

```markdown
<!-- .claude/commands/review.md — creates /review -->
Review the staged changes against our team checklist:
1. Check error handling patterns
2. Verify test coverage for new functions
3. Confirm API naming conventions
4. Flag any hardcoded credentials or secrets
```

**User-scoped (personal):**

Place skills in `~/.claude/skills/` (canonical) or `~/.claude/commands/` (alias). These are personal and not version-controlled or shared. Use for individual productivity workflows that other team members do not need.

> **Key Concept**
> The scoping pattern is consistent across Claude Code: project-level (`.claude/`) is shared via git; user-level (`~/.claude/`) is personal. This applies to CLAUDE.md, commands/skills, and rules. The same pattern recurs across every configuration surface in this domain, so recognising it makes audits faster. Both `.claude/commands/` and `.claude/skills/` are project-scoped and create the same commands; `.claude/skills/` is the canonical, fuller-featured path. Keep the file shapes straight, though: skills are directories with a `SKILL.md` inside; commands are flat `.md` files.

### Skills Frontmatter: Optional Configuration

Skills in `.claude/skills/` with `SKILL.md` files support optional YAML frontmatter configuration. This frontmatter also works with `.claude/commands/` files, but `.claude/skills/` is the canonical location for configured skills. Skills are task-specific workflows invoked on demand — they are not loaded automatically like CLAUDE.md.

The three critical frontmatter options:

**`context: fork`**

Runs the skill in an isolated sub-agent context. All verbose output stays contained in the fork. The main conversation remains clean and uncluttered. This is essential for:

- Codebase analysis (produces extensive file listings and code excerpts)
- Brainstorming (generates many alternatives and evaluations)
- Any task that produces noisy, exploratory output

Without `context: fork`, skill output flows into the main conversation and consumes context window tokens. For verbose skills, this degrades the quality of subsequent responses. When reviewing an implementation, flag any noisy, exploratory skill that omits `context: fork` — it is quietly spending the main context budget.

The frontmatter sits at the top of the skill's `SKILL.md`. For a skill invoked as `/analyse-feature`, that file lives at `.claude/skills/analyse-feature/SKILL.md`:

```yaml
---
context: fork
allowed-tools:
  - Read
  - Grep
  - Glob
argument-hint: "Provide a feature description or area of the codebase to analyse"
---
```

**`allowed-tools`**

Pre-approves the listed tools so Claude can use them without a permission prompt while the skill is active. It does not restrict which tools are available: every other tool remains callable, and your normal permission settings still govern anything that is not listed. Use it to let a trusted workflow run without stopping to ask on each call.

```yaml
---
allowed-tools:
  - Read
  - Grep
  - Glob
---
```

To remove tools from Claude's pool while a skill runs, which is the actual security boundary, list them in `disallowed-tools` instead, or add deny rules in your permission settings.

> **Common Mistake**
> `allowed-tools` looks like a way to lock a skill down to a safe subset of tools — treat that reading as a bug. It only pre-approves the listed tools to skip permission prompts; every other tool stays callable. The real restriction is `disallowed-tools` (or deny rules in permission settings). An implementation that relies on `allowed-tools` as a security boundary is not actually restricting anything.

**`argument-hint`**

Prompts the developer for required parameters when the skill is invoked without arguments. Improves the developer experience by making inputs explicit rather than relying on the developer to remember what the skill needs.

```yaml
---
argument-hint: "Specify the module path to analyse (e.g., src/api/auth)"
---
```

### Skills vs CLAUDE.md: The Critical Distinction

This is the distinction most often gotten wrong, so when reviewing an implementation, verify it directly:

- **Skills** = on-demand, task-specific workflows. Their descriptions are always in context so Claude knows they exist, but the full skill body loads only when invoked. Invocation can be explicit (`/skill-name`) or automatic: Claude picks up skills whose `description` matches the user's intent, or skills with a `paths` frontmatter field when you're working on matching files. Skills with `disable-model-invocation: true` require explicit user invocation.
- **CLAUDE.md** = always-loaded, universal standards. Applied automatically to every session, with no invocation step.

The rule: do not put task-specific procedures in CLAUDE.md. Do not put always-on reference material in skills.

API naming conventions that must apply to every code generation task belong in CLAUDE.md (or `.claude/rules/`). A multi-step codebase analysis workflow that a developer runs occasionally belongs in a skill. For conventions that apply to a specific file type — like test files — path-scoped `.claude/rules/` are the best fit because they load as always-on context alongside matching files.

### Personal Skill Customisation

Create personal variants in `~/.claude/skills/` (or `~/.claude/commands/`) with different names to avoid affecting teammates. If the team has a standard `/analyse` skill but you prefer a more verbose version, create your own in `~/.claude/skills/` with a different name (e.g., `/deep-analyse`). Your personal skill does not override or conflict with the team version.

### Where to Place Custom Commands: Quick Reference

| Need | Canonical location | Also works | Scoping |
| --- | --- | --- | --- |
| Team-wide command | `.claude/skills/<name>/SKILL.md` | `.claude/commands/<name>.md` | Project (shared via git) |
| Team-wide command with frontmatter config | `.claude/skills/<name>/SKILL.md` | `.claude/commands/<name>.md` | Project (shared via git) |
| Personal command | `~/.claude/skills/<name>/SKILL.md` | `~/.claude/commands/<name>.md` | User (not shared) |
| Universal standards | `.claude/CLAUDE.md` or root `CLAUDE.md` | — | Project (always loaded) |
| Personal preferences | `~/.claude/CLAUDE.md` | — | User (not shared) |

## Audit Checklist

- [ ] Skills are directories containing a `SKILL.md` entrypoint; commands are flat `.md` files — a flat file placed directly in `.claude/skills/` does not register a command.
- [ ] New commands prefer the canonical `.claude/skills/<name>/SKILL.md` path over the `.claude/commands/` alias to gain supporting files, automatic discovery, and name precedence.
- [ ] Team-wide commands live under project-scoped `.claude/` (version-controlled); personal-only workflows live under user-scoped `~/.claude/` and are not shared.
- [ ] Verbose or exploratory skills (codebase analysis, brainstorming) declare `context: fork` so their output stays out of the main conversation's context budget.
- [ ] `allowed-tools` is used only to pre-approve tools and skip prompts — it is never relied on as a security boundary; tool restriction uses `disallowed-tools` or permission deny rules.
- [ ] Skills that require parameters supply an `argument-hint` so inputs are explicit.
- [ ] Task-specific procedures live in skills, not CLAUDE.md; always-on universal standards live in CLAUDE.md (or `.claude/rules/`), not skills.
- [ ] File-type-specific conventions use path-scoped `.claude/rules/` so they load as always-on context alongside matching files.
- [ ] Personal skill variants use distinct names (e.g. `/deep-analyse` vs the team's `/analyse`) so they do not override or conflict with shared team skills.

## Sources

- [Claude Code Skills Documentation (custom slash commands are part of the unified Skills system)](https://code.claude.com/docs/en/skills) — Anthropic
