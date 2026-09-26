---
name: code-standards
description: "Review code against CONVENTIONS -- type safety (`any` detection), naming, imports, Next.js patterns, 12-factor. NOT for complexity metrics (use code-quality) or API field alignment (use api-contract)."
version: "1.0.0"
owner: "swarmery-core"
allowed-tools: Read, Grep, Glob, Bash
docs:
  status: reviewed
  source_sha: 07f13623e2e2
  updated: 2026-08-06
---

# Purpose

Review code against the project's per-repository conventions and return a
violation report with `file:line` citations, severities, and before/after fixes.
Covers type safety (`any` detection, annotations), naming, Next.js 15 patterns,
Python typing and async style, service-config hygiene, and 12-factor
build-once/deploy-anywhere rules.

"Is this function too long or too complex?" → `code-quality`. "Does this use
`any`, follow naming, or comply with framework patterns?" → here.

# Rules (never violate)

1. Review only — never fix in place; fixes go to `@implementation-agent`.
2. Every violation cites `file:line` and carries a severity; Critical and High
   findings include before/after code.
3. `NEXT_PUBLIC_*` is allowed in source, prohibited as a Docker `--build-arg`.
4. Module-scope `process.env.X` is a violation; lazy access inside a function is not.
5. Never emit length, nesting, or complexity findings — `code-quality` owns those.
6. Close the report with the `STANDARDS-VIOLATIONS:` trailer; cap it at 300 lines.

# Resources

- Read `resources/review-procedure.md` before reviewing: the 8-step procedure,
  severity criteria, output template, self-check, escalation triggers, failure
  modes, and a worked example.
- Read `resources/checklists.md` for the rule set matching `repo_type` — web app,
  device/edge, service config, and the 12-factor build rules.
- Read `resources/sandbox-preflight.md` when the run edits files from a git
  worktree isolate: one operation per Bash call, and the checks to make before
  the first read or write.

# How to use

## What it does

Checks code against your conventions and hands back an actionable report: type
safety including `any` types, naming, framework patterns, Python type hints and
async style, service-config hygiene, and 12-factor build rules — each finding
with a citation, a severity, and corrected code for the serious ones.

## When to use it

- TypeScript/Next.js code changed and wants a standards pass before merge.
- Python device or edge code needs type hints and async patterns verified.
- A service config changed and needs lint, version bump, and template guards checked.
- A Dockerfile or CI pipeline changed and needs 12-factor compliance verified.

Not for: length, nesting, or complexity (`code-quality`); schema ↔ handler field
alignment (`api-contract`); locating files (`code-search`); or applying fixes —
this skill reviews and refuses in-place edits.

## How to invoke

```
Skill(skill: "core:code-standards")
scope: apps/<mainApp>/src/app/api/orders/route.ts
repo_type: web-app
```

`scope` and `repo_type` (`web-app` | `device` | `service-config` |
`infrastructure`) are both required; the matching checklist is selected for you.

## Worked example

The route above calls `auth()` without `export const dynamic = 'force-dynamic'`
and annotates a query result `: any`. You get two rows — one Critical, one High —
each with the line, the offending snippet, the corrected snippet, and:

```
STANDARDS-VIOLATIONS: 2 | CRITICAL: 1 | HIGH: 1 | MEDIUM: 0 | LOW: 0
```

Both fixes are single-line edits you apply yourself. A file with more than 20
Critical findings stops the review and asks instead.

## Related

- `code-quality` — length and complexity; composes with this skill as a depth-1
  fan-out under `@code-reviewer` over the same scope.
- `api-contract` — field-level alignment across the data layer.
- `code-search` — run first when the files to review are still unknown.
