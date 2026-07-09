---
name: code-quality
description: "Use this skill for QUANTITATIVE structural metrics on TypeScript or Python source -- function length, cyclomatic complexity, nesting depth, duplication, code smells -- producing a scored report. NOT for conventions, naming, or `any`-type checks (use code-standards), API contract alignment (use api-contract), or deployment config (use deployment)."
version: "1.0.0"
owner: "agentry-core"
allowed-tools: Read, Grep, Glob, Bash
disable-model-invocation: true
---

# Purpose

Scans specified TypeScript or Python source files and produces a scored report identifying functions exceeding length thresholds, deep nesting, duplicate code blocks, and project-specific anti-patterns (e.g., eager DB init, missing `force-dynamic`, missing `async`/`await`). The report assigns a 0-100 score per category and lists every finding with `file:line` citations.

This skill owns **structural** quality: function length, cyclomatic complexity, nesting depth, code smells, and duplicate code. It does NOT own `any`-type detection, missing type annotations, or naming conventions -- those belong to `code-standards`.

# When to use / When NOT to use

**Use when:**
- Someone asks you to audit a file or module for function length, complexity, nesting depth, or code smells
- A PR needs a structural quality assessment (function size, duplication, nesting)
- A periodic quality audit of a repository or module is requested
- After a refactoring, to verify that structural quality metrics improved

**Do NOT use when:**
- Reviewing code for naming conventions, `any` types, import ordering, or style compliance (use `code-standards`)
- Verifying that DB schema fields match validation schemas and route handlers (use `api-contract`)
- Auditing deployment YAML or manifests (use `deployment`)
- Listing files or navigating the codebase without quality assessment intent (use `code-search`)

**Boundary with `code-standards`:** If the question is "does this code follow conventions?" route to `code-standards`. If the question is "is this function too long / too complex / too deeply nested?" route here.

# Required environment

- Runtime: `.claude/skills/code-quality/SKILL.md`
- Tools: Read, Grep, Glob, Bash
- Frontmatter flag `disable-model-invocation: true` keeps this skill user/agent-explicit (it does not auto-trigger on matching phrases). As a knowledge module it never dispatches subagents in any case; depth control lives at the agent layer (see ARCHITECTURE.md -> Delegation depth).
- File system assumptions:
  - The main app (`apps/<mainApp>/src/`, where `<mainApp>` = `project.json → mainApp`) contains TypeScript source files
  - The device/edge repo (`<device>/src/`, where `<device>` = `project.json → device`) contains Python source files
  - Target scope (file, directory, or repo) is specified by the caller

# Inputs

- `scope: string` -- path to the file, directory, or repo to audit (e.g., `apps/<mainApp>/src/app/api/<resource>/route.ts` or `apps/<mainApp>/src/lib/`)
- `repo_type: "typescript" | "python"` -- determines which threshold set to apply

# Outputs

- **Format:** Markdown report following the output template below
- **Length budget:** max 200 lines for a single file audit, max 500 lines for a directory audit
- **Downstream handoff:** Emit a machine-readable header at the top of the report: `QUALITY-SCORE: {overall}/100 | ERRORS: {n} | WARNINGS: {n}` for consumption by CI gates or downstream agents.

## Output template

```markdown
QUALITY-SCORE: {overall}/100 | ERRORS: {n} | WARNINGS: {n}

## Code Quality Report

**Scope:** {file/module/directory path}
**Repo Type:** {typescript | python}
**Overall Score:** X/100

| Category | Score | Status |
|----------|-------|--------|
| Function Length | /100 | {count} errors, {count} warnings |
| Nesting | /100 | {count} errors, {count} warnings |
| Code Smells | /100 | {count} findings |
| Project-Specific | /100 | {count} errors |

### Error-Level Issues ({count})
[Numbered list with file:line, description, fix suggestion]

### Warning-Level Issues ({count})
[Numbered list with file:line, description, fix suggestion]

### Action Plan
[Prioritized list with effort estimates: High/Medium/Low]
```

# Thresholds

| Metric | TypeScript | Python |
|--------|---------------------|----------------------|
| Function length (warning) | >30 lines | >30 lines |
| Function length (error) | >50 lines | >50 lines |
| React component length (error) | >150 lines | N/A |
| Module/file length (warning) | >200 lines (lib), >150 lines (component) | >300 lines |
| Nesting depth (error) | >2 levels | >2 levels |
| Cyclomatic complexity (warning) | >10 | >10 |

These thresholds are authoritative for this skill. If the project adopts different thresholds in `.eslintrc` or `pyproject.toml`, update this table to match.

# Procedure

**Success criteria:** The report contains a scored assessment of every function in scope, every finding has a `file:line` citation, and the overall score is calculated per the formula below.

1. **Determine scope and repo type** -- Read the target path to identify whether it is TypeScript (typically the main app) or Python (typically the device repo). Apply the matching threshold set. Checkpoint: repo type confirmed, thresholds selected.

2. **Read target files** -- Read all files in scope once. Cache file contents for reuse across subsequent checks (steps 3-6 all operate on the same file content). Checkpoint: files read, content available.

3. **Steps 3-6 are independent and can be executed in parallel.**

   **Run function length check** -- For each function/method in the cached content:
   - TypeScript: flag functions exceeding 50 lines (warning at 30, error at 50)
   - Python: flag functions exceeding 50 lines (warning at 30, error at 50)
   - React components: flag components exceeding 150 lines
   - Do NOT count blank lines or comment-only lines toward function length.
   Checkpoint: list of oversized functions with `file:line` and line count.

4. **Run nesting depth check** -- Identify blocks nested deeper than 2 levels (3+ levels of indentation within a function body). Checkpoint: list of deep-nesting locations with `file:line`.

5. **Run code smell check** -- Look for:
   - Duplicate code blocks (3+ lines repeated verbatim in different locations)
   - TODO/FIXME comments (count and list -- but do NOT flag TODO in test files as smells)
   - Dead code / unused imports
   - Missing error handling (try/catch or error returns)
   - Magic numbers/strings (unexplained literals that should be named constants)
   - Missing guard clauses / early returns where they would flatten nesting
   Checkpoint: list of code smells with `file:line`.

6. **Run project-specific checks** -- Consult the consumer project's `CLAUDE.md` for its conventions. Typical checks for a Next.js main app and a Python device repo:
   - Main app: lazy DB init pattern used (no eager `export const db = ...`)
   - Main app: `export const dynamic = 'force-dynamic'` on routes importing `auth()`
   - Main app: no `next/font/google` usage
   - Main app: typed env helpers used for env access (no scattered raw `process.env.*`)
   - Main app: schema validation (e.g., Zod) present at route-handler boundaries
   - Main app: route handlers stay thin -- business logic lives in `src/lib/`
   - Main app (React): `key` props in rendered lists, complete `useEffect` dependency arrays, error boundaries around risky subtrees
   - Device repo: `async`/`await` on all I/O operations (no blocking calls inside async functions)
   - Device repo: no bare `except:` clauses
   - Device repo: resource cleanup via context managers for hardware handles (e.g., BLE/camera)
   - Device repo: mock-mode (`MOCK_MODE`) coverage exists for hardware-dependent code paths
   Checkpoint: list of project-specific violations with `file:line`.

7. **Calculate scores** -- For each category, calculate a 0-100 score:
   - 100 = zero violations
   - Deduct 10 points per error-level finding, 5 points per warning-level finding
   - Floor at 0
   - Overall score = average of all category scores (rounded to nearest integer)
   Checkpoint: all category scores calculated.

8. **Produce the report** -- Fill in the output template with all findings and scores. Every finding must have a `file:line` citation. Begin the report with the `QUALITY-SCORE:` header line. Checkpoint: report complete.

9. **Final acceptance check** -- Verify every finding has `file:line`, every score is calculated, no placeholder text remains, and the `QUALITY-SCORE:` header is populated.

# Self-check before returning

- [ ] Every finding cites a specific `file:line` location
- [ ] Every finding has a severity level (Error or Warning)
- [ ] Scores are calculated per the formula (100 minus deductions, floor at 0)
- [ ] No findings emitted below 80% confidence without `[LOW-CONFIDENCE]` marker
- [ ] TypeScript file size thresholds distinguish between component files (150 lines) and lib/utility files (200 lines)
- [ ] The `QUALITY-SCORE:` machine-readable header is present
- [ ] The report contains zero placeholder text
- [ ] No `any`-type grep was performed (that check belongs to `code-standards`)

# Common mistakes to avoid

- DO NOT grep for `any` types or missing type annotations -- that check belongs to `code-standards`, not this skill
- DO NOT flag auto-generated files (e.g., `*.generated.ts`, migration files) as quality violations
- DO NOT flag vendored or third-party code
- DO NOT apply TypeScript thresholds to Python files or vice versa
- DO NOT count blank lines or comment-only lines toward function length
- DO NOT report `TODO` comments in test files as code smells (they are legitimate markers for test expansion)
- DO NOT conflate component file size (150-line threshold for React components) with utility file size (200-line threshold for lib files)

# Escalation

- **Stop and ask when:** more than 50 findings are detected in a single file (likely a structural issue requiring architectural review, not piecemeal fixes)
- **Stop and ask when:** the target scope is an entire repository with more than 500 files (confirm the user wants a full scan)
- **Refuse and explain when:** asked to auto-fix quality issues (this is an audit skill; fixes require `@implementation-agent`)

# Examples

<example>
## Worked example: auditing a TypeScript route handler

**Input:** `scope: apps/<mainApp>/src/app/api/orders/route.ts`, `repo_type: typescript`

**Findings:**

```
[ERROR] Function length: GET() is 62 lines (threshold: 50)
  Location: apps/<mainApp>/src/app/api/orders/route.ts:15
  Fix: Extract order query logic into a separate function in src/lib/services/order-service.ts

[WARNING] Function length: POST() is 38 lines (threshold: 30)
  Location: apps/<mainApp>/src/app/api/orders/route.ts:78
  Fix: Extract validation logic into a reusable validator

[WARNING] Nesting depth: 3 levels of nesting in POST()
  Location: apps/<mainApp>/src/app/api/orders/route.ts:95
  Fix: Use guard clauses to reduce nesting

[ERROR] Project-specific: missing 'export const dynamic = "force-dynamic"' but route imports auth()
  Location: apps/<mainApp>/src/app/api/orders/route.ts:1
  Fix: Add 'export const dynamic = "force-dynamic"' after imports
```

**Report:**

QUALITY-SCORE: 68/100 | ERRORS: 2 | WARNINGS: 2

## Code Quality Report

**Scope:** `apps/<mainApp>/src/app/api/orders/route.ts`
**Repo Type:** typescript
**Overall Score:** 68/100

| Category | Score | Status |
|----------|-------|--------|
| Function Length | 60/100 | 1 error, 1 warning |
| Nesting | 90/100 | 1 warning |
| Code Smells | 100/100 | none |
| Project-Specific | 80/100 | 1 error (missing force-dynamic) |

### Error-Level Issues (2)
1. `route.ts:15` -- GET() is 62 lines; extract query logic to service layer
2. `route.ts:1` -- missing `export const dynamic = 'force-dynamic'`; required because route imports `auth()`

### Warning-Level Issues (2)
1. `route.ts:78` -- POST() is 38 lines; consider extracting validation
2. `route.ts:95` -- 3 levels of nesting; use guard clauses

### Action Plan
1. [High effort] Extract GET() query logic into `src/lib/services/order-service.ts` -- reduces function length and improves testability
2. [Low effort] Add `export const dynamic = 'force-dynamic'` -- one-line fix
</example>

<example>
## Worked example: auditing a Python device-service module

**Input:** `scope: <device>/src/telemetry/protocol_handler.py`, `repo_type: python`

**Findings:**

```
[ERROR] Function length: handle_telemetry() is 68 lines (threshold: 50)
  Location: <device>/src/telemetry/protocol_handler.py:42
  Fix: Extract message parsing into separate functions per message type

[ERROR] Nesting depth: 4 levels of nesting in handle_telemetry()
  Location: <device>/src/telemetry/protocol_handler.py:55
  Fix: Use early returns and guard clauses

[ERROR] Project-specific: bare except: clause swallows all exceptions
  Location: <device>/src/telemetry/protocol_handler.py:90
  Fix: Catch specific exceptions (e.g., ConnectionError, TimeoutError)
```

**Report:**

QUALITY-SCORE: 50/100 | ERRORS: 3 | WARNINGS: 0

## Code Quality Report

**Scope:** `<device>/src/telemetry/protocol_handler.py`
**Repo Type:** python
**Overall Score:** 50/100

| Category | Score | Status |
|----------|-------|--------|
| Function Length | 60/100 | 1 error |
| Nesting | 80/100 | 1 error |
| Code Smells | 100/100 | none |
| Project-Specific | 80/100 | 1 error (bare except) |

### Error-Level Issues (3)
1. `protocol_handler.py:42` -- handle_telemetry() is 68 lines; extract per-message-type parsing
2. `protocol_handler.py:55` -- 4 levels of nesting; use guard clauses
3. `protocol_handler.py:90` -- bare `except:` clause; catch `ConnectionError, TimeoutError` instead

### Action Plan
1. [High effort] Decompose handle_telemetry() by message type -- reduces length and nesting simultaneously
2. [Low effort] Replace bare `except:` with specific exception types -- one-line fix
</example>

# Failure modes

- **Mode 1:** Function length count includes multi-line JSX template literals -- detect: inflated line count on React components; fix: count logical lines (statements) rather than physical lines for component render functions
- **Mode 2:** Scope is a symlinked directory that resolves outside the repo -- detect: file paths don't match expected repo structure; fix: resolve symlinks before scanning and verify paths are within the repo root
- **Mode 3:** Directory audit exceeds 500 files and produces an unmanageable report -- detect: file count exceeds threshold; fix: trigger escalation and ask the user to narrow scope

# Related skills

- `code-standards` -- defer to this skill for `any`-type detection, naming conventions, import ordering, and style rules; compose when a quality audit reveals convention violations alongside structural issues. Composition is a depth-1 fan-out under `@code-auditor` (runs both as leaves over the SAME scope, aggregates the `QUALITY-SCORE:` + `STANDARDS-VIOLATIONS:` headers) -- never a nested delegation chain or routing handoff.
- `deployment` -- owns deployment config quality checks (required-values validation, resource limits, health probes, security context); route any config-quality request there
- `api-contract` -- defer to this skill for field-level alignment checks; compose when a quality audit of a route handler reveals potential contract mismatches
- `code-search` -- defer to this skill when the audit scope needs to be determined (finding which files to audit)
