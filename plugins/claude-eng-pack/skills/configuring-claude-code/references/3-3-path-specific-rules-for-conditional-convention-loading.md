---
domain: 3 - Claude Code Configuration & Workflows
module: "3.3"
title: "Path-Specific Rules for Conditional Convention Loading"
---

# 3.3 Path-Specific Rules for Conditional Convention Loading

## Overview

Path-specific rules are the mechanism for applying conventions conditionally based on which files are being edited. They solve a problem that neither root CLAUDE.md nor directory-level CLAUDE.md files handle well: conventions that must apply to files of a specific type spread across many directories.

### How Path-Specific Rules Work

Rule files live in the `.claude/rules/` directory. Each file contains YAML frontmatter with a `paths` field specifying glob patterns. The rules inside the file load only when the file being edited matches those patterns.

```yaml
---
paths: ["terraform/**/*"]
---
# Terraform Conventions

- Use snake_case for all resource names
- Tag every resource with environment and team labels
- Never hardcode AMI IDs — use data sources
- All modules must have a variables.tf, outputs.tf, and README.md
```

When editing a file matching `terraform/**/*`, these rules load automatically. When editing a React component or an API handler, they do not. The rules are invisible until relevant.

### Glob Patterns Match Across the Entire Codebase

This is the critical advantage. A glob pattern like `**/*.test.tsx` catches every test file in the codebase, regardless of which directory it sits in. Consider a typical project structure:

```
src/
  components/
    Button.tsx
    Button.test.tsx
  api/
    auth.ts
    auth.test.ts
  utils/
    format.ts
    format.test.ts
  pages/
    dashboard/
      Dashboard.tsx
      Dashboard.test.tsx
```

Test files are co-located with their source files across four different directories. A path-specific rule with `paths: ["**/*.test.tsx", "**/*.test.ts"]` applies the same test conventions to every one of these files, automatically.

### Why Not Directory-Level CLAUDE.md?

A directory-level CLAUDE.md applies to files in that one directory. To apply test conventions to test files spread across 50+ directories, you would need to place a CLAUDE.md file in every single directory containing tests. That means:

- 50+ copies of the same conventions
- Every new directory with tests needs a new copy
- Any convention change requires updating all 50+ files
- Inevitable drift as some copies fall behind

Path-specific rules with glob patterns eliminate this entirely. One file, one pattern, universal coverage.

### Why Not Root CLAUDE.md?

Root CLAUDE.md loads for every session, regardless of which files are being edited. If Terraform conventions live in the root CLAUDE.md, they consume tokens even when editing React components. If test conventions live in the root, they load when writing API handlers.

> **Key Concept**
> Path-scoped rules are more token-efficient than root CLAUDE.md because they load ONLY when editing matching files. This reduces irrelevant context and keeps the model focused on conventions that actually apply to the current work. In large projects with many convention categories, this efficiency gain is substantial.

> **Common Mistake**
> Putting type-specific conventions (tests, IaC, API handlers) in the root CLAUDE.md because it is the most familiar place looks convenient, but it is the wrong scope: root CLAUDE.md loads on every session and pays the token cost even when none of those files are open. Reserve the root file for standards that genuinely apply to all code, and push conditional conventions into path-scoped rules.

### Practical Rule File Examples

**Test conventions across the entire codebase:**

```yaml
---
paths: ["**/*.test.ts", "**/*.test.tsx", "**/*.spec.ts", "**/*.spec.tsx"]
---
# Test Conventions

- Use describe/it blocks with descriptive names that read as sentences
- Each test file must have at least one happy path and one error case
- Use factory functions for test data, not inline object literals
- Mock external services at the module boundary, not individual functions
- Assert behaviour, not implementation details
```

**API conventions for any route handler:**

```yaml
---
paths: ["src/api/**/*", "**/routes/**/*", "**/*.controller.ts"]
---
# API Conventions

- All endpoints return { data, error, metadata } response shape
- Use Zod schemas for request validation at the handler boundary
- Log request ID on every error response
- Rate limiting configuration must be explicit, not inherited from defaults
```

**Infrastructure-as-code conventions:**

```yaml
---
paths: ["terraform/**/*", "**/*.tf", "infrastructure/**/*"]
---
# Infrastructure Conventions

- State files must reference remote backends, never local
- Use workspaces for environment separation
- Every module must be versioned with a CHANGELOG
```

### When to Use Each Approach

| Scenario | Best approach |
| --- | --- |
| Universal team standards that apply to all code | Root CLAUDE.md |
| Conventions for one specific package directory | Directory-level CLAUDE.md |
| Conventions for a file type spread across many directories | Path-specific rules with glob patterns |
| Task-specific workflows invoked on demand | Skills in .claude/skills/ |

When reviewing an implementation, the recurring pattern to recognise is test files co-located with source files across many directories. The correct fit is path-specific rules with glob patterns — a single rule file scoped by a `**/*.test.*` glob rather than a per-directory CLAUDE.md in every folder.

## Audit Checklist

- [ ] Conventions that apply to a file type spread across many directories are implemented as path-specific rules with glob patterns, not duplicated CLAUDE.md files per directory.
- [ ] Rule files live in `.claude/rules/` and declare their scope with a `paths` glob field in YAML frontmatter.
- [ ] Glob patterns use `**/` prefixes so they match files anywhere in the tree, not just one directory.
- [ ] Type-specific conventions (tests, API handlers, IaC) are kept out of root CLAUDE.md so they only consume context when matching files are being edited.
- [ ] Root CLAUDE.md is reserved for universal standards that apply to all code.
- [ ] Conventions scoped to a single directory use a directory-level CLAUDE.md rather than a glob rule.
- [ ] On-demand, task-specific workflows are implemented as skills in `.claude/skills/`, not as path rules.
- [ ] Changing a convention for a file type requires editing one rule file, not many copies, so no drift can accumulate.

## Sources

- [Claude Code Memory and Rules Documentation](https://code.claude.com/docs/en/memory) — Anthropic
