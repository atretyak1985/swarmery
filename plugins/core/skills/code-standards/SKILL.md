---
name: code-standards
description: "Use this skill when reviewing TypeScript, Python, or infrastructure/service-config code against the project's CONVENTIONS -- type safety (`any` detection), naming, import ordering, Next.js patterns, and 12-factor build rules. NOT for quantitative complexity metrics -- function length, cyclomatic complexity, nesting depth belong to code-quality. NOT for API field alignment (use api-contract)."
version: "1.0.0"
owner: "agentry-core"
allowed-tools: Read, Grep, Glob, Bash
---

# Purpose

Reviews code against the project's per-repository coding standards (see the project's `CLAUDE.md` and `.claude/project.json`) and produces a violation report with `file:line` citations, severity levels, and before/after fix examples. Covers type safety (including `any`-type detection), naming conventions, Next.js 15 patterns, Python async patterns, service-config hygiene, and 12-factor build-once/deploy-anywhere rules.

This skill owns **convention-level** checks: `any` types, missing type annotations, naming style, `force-dynamic` presence, `getDb()` pattern, `NEXT_PUBLIC_*` build-arg prohibition, and npm run build. It does NOT own function length, cyclomatic complexity, nesting depth, or code smell detection -- those belong to `code-quality`.

# When to use / When NOT to use

**Use when:**
- Code has been written or modified in the main web app and needs a standards review before merge
- Code has been written or modified in the device/edge repo and needs type hint / async pattern verification
- A service config in the infrastructure repo has been modified and needs lint, version bump, and defensive template verification
- A Dockerfile or CI pipeline change needs 12-factor compliance verification

**Do NOT use when:**
- Measuring function length, cyclomatic complexity, nesting depth, or code smells (use `code-quality`)
- Verifying field alignment between Prisma schema, Zod, and route handlers (use `api-contract`)
- Navigating the codebase to find files (use `code-search`)
- Running database migrations or checking migration syntax

**Boundary with `code-quality`:** If the question is "is this function too long or too complex?" route to `code-quality`. If the question is "does this code use `any` types, follow naming conventions, or comply with Next.js patterns?" route here.

# Required environment

- Runtime: `.claude/skills/code-standards/SKILL.md`
- Tools: Read, Grep, Glob, Bash
- Bash is required for `npm run build` and the service-config lint/dry-run commands (per the project's runtime -- `.claude/project.json` → `cloud.runtime`) when reviewing service config. For code-only reviews, Bash is not needed.
- File system assumptions (map onto the project's repo list in `.claude/project.json`):
  - `apps/<mainApp>/` contains TypeScript / Next.js 15 code
  - `<device>/` (device/edge repo) contains Python 3.11+ code
  - the infrastructure repo contains service config, infrastructure, and migration files

# Inputs

- `scope: string` -- path to the file(s) to review (e.g., `apps/<mainApp>/src/app/api/devices/route.ts`)
- `repo_type: "web-app" | "device" | "service-config" | "infrastructure"` -- determines which rule set to apply

# Outputs

- **Format:** Markdown violation report with `file:line` citations and before/after code
- **Length budget:** max 300 lines per report. If violations exceed budget, report Critical and High findings in full, summarize Medium and Low findings as counts.
- **Downstream handoff:** Emit a summary line at the end: `STANDARDS-VIOLATIONS: {count} | CRITICAL: {n} | HIGH: {n} | MEDIUM: {n} | LOW: {n}` for consumption by CI gates or downstream agents.

## Output template

```markdown
## Code Standards Report

**Scope:** {file/module/directory path}
**Repository:** {web-app | device | service-config | infrastructure}

| # | Severity | Location | Violation | Fix |
|---|----------|----------|-----------|-----|

**Before (violation #{n}):**
```
{violating code}
```

**After (fix #{n}):**
```
{corrected code}
```

**Summary:** {count} violations ({n} Critical, {n} High, {n} Medium, {n} Low).

STANDARDS-VIOLATIONS: {count} | CRITICAL: {n} | HIGH: {n} | MEDIUM: {n} | LOW: {n}
```

# Procedure

**Success criteria:** Every applicable checklist item has been verified against the target code, every violation has a `file:line` citation and severity, and the report follows the output template.

1. **Identify the repository and rule set** -- Read the target path to determine which standards apply. Select the matching checklist from the Review Checklists section below. Checkpoint: repo type confirmed, checklist selected.

2. **Read target files** -- Read all files in scope once. Cache file contents for reuse across checklist items. Checkpoint: files read, content available.

3. **Check universal standards** -- Apply checks that are common across all repos (steps 3a-3c can run in parallel):
   - 3a. No hardcoded secrets or credentials
   - 3b. No commented-out code blocks exceeding 5 lines
   - 3c. No hardcoded environment-specific URLs or hostnames
   Checkpoint: universal checks complete.

4. **Check repository-specific standards** -- Apply the appropriate checklist from the sections below. For each violation found, record `file:line`, the violating code, and the fix. Grep checks within the same checklist are independent and can run in parallel. Checkpoint: all applicable checklist items verified.

5. **Apply 12-factor rules (if Dockerfile or build config is in scope)** -- Verify no `NEXT_PUBLIC_*` build args, no env-specific values baked into images, runtime-env bridge pattern used for client-visible config. Checkpoint: 12-factor rules checked.

6. **Assign severity** -- For each violation, assign a severity level per the criteria below. Checkpoint: all violations have severity.

7. **Produce the report** -- Fill in the output template with all violations. Every violation must cite `file:line` and include before/after code for Critical and High findings. Checkpoint: report complete, no placeholder text.

8. **Final acceptance check** -- Every violation has `file:line`, severity, and a fix; no placeholder text remains; the `STANDARDS-VIOLATIONS:` summary line is populated.

# Review Checklists

## Main web app (TypeScript / Next.js 15)

### Type Safety (owned by this skill)
- [ ] No `any` types (strict mode enabled) -- cite `file:line` for each occurrence; use regex `: any\b` or `as any\b` to avoid matching string literals containing "any"
- [ ] All function parameters have type annotations
- [ ] All function return types are declared
- [ ] Zod schemas used for external data validation (request bodies, query params)
- [ ] Prisma schema types used for DB query results

### Naming Conventions
- [ ] Components: PascalCase files (`DeviceCard.tsx`)
- [ ] Hooks: `use` prefix camelCase (`useTelemetry.ts`)
- [ ] Utility functions: camelCase (`formatCoordinates.ts`)
- [ ] File names: kebab-case (`device-card.tsx`) except components
- [ ] Constants: UPPER_SNAKE_CASE
- [ ] Boolean variables: `is`/`has`/`can` prefix

### Next.js 15 Patterns
- [ ] Server Components by default (no `'use client'` unless the file uses hooks, event handlers, or browser APIs)
- [ ] `export const dynamic = 'force-dynamic'` on any route/page that calls `auth()` or reads runtime env
- [ ] `getDb()` lazy init pattern for database access (never `export const db = Prisma(...)`)
- [ ] `getServerEnv()` for server-side environment validation (never `process.env.X` at module scope)
- [ ] No `next/font/google` (causes prerender failures in Next.js 15)

### Client-Side Environment Variables (12-factor rule)

`NEXT_PUBLIC_*` environment variables are allowed in source code for local development, but they MUST NOT be injected via `--build-arg` at Docker image build time. Build-time injection makes the image environment-specific, violating the build-once/deploy-anywhere rule.

**Prohibited pattern** (fails code review):
```typescript
// .gitlab-ci.yml or Dockerfile
// --build-arg NEXT_PUBLIC_API_URL=$API_URL  <-- PROHIBITED
```

**Required pattern** for client-visible runtime config:
```typescript
// Server component or layout renders the bridge script:
<script dangerouslySetInnerHTML={{
  __html: `window.__ENV__=${JSON.stringify({
    API_URL: process.env.API_URL,
    MAPS_KEY: process.env.MAPS_KEY,
  })}`
}} />

// Client code reads from window.__ENV__:
const apiUrl = (window as any).__ENV__?.API_URL;
```

This means the same Docker image works in dev, staging, and production -- only the pod's environment variables change.

## Device/edge repo (Python 3.11+)

### Type Safety
- [ ] Type hints on all function parameters and return types
- [ ] `dataclass` or `TypedDict` for structured data
- [ ] `mypy` passes without errors

### Style
- [ ] Black formatter applied (line length 100)
- [ ] isort for import ordering
- [ ] flake8 passes (max complexity 10)
- [ ] No bare `except:` (always catch specific exceptions)

### Async Patterns
- [ ] `async`/`await` for all I/O operations
- [ ] `asyncio.to_thread()` for blocking calls (e.g., synchronous hardware/serial reads)
- [ ] Proper cleanup in `finally` blocks
- [ ] `MOCK_MODE` env var support for testing without hardware

## Service config (infrastructure repo -- runtime per `.claude/project.json` → `cloud.runtime`)

- [ ] Chart/config version bumped on any template change (e.g., `Chart.yaml` for Helm-style configs)
- [ ] The runtime's lint/dry-run render passes without errors (requires Bash)
- [ ] `npm run build` passes (requires Bash)
- [ ] Defensive template nesting: all nested value references use `with` or `if` guards
- [ ] One resource per YAML file
- [ ] Values use flat structure where possible (avoid deep nesting)
- [ ] No secrets in values files (use `*.populated.yaml` for secret overrides)
- [ ] Subchart version bumps update umbrella `Chart.yaml` + `Chart.lock` -- verify by comparing the subchart version in `Chart.yaml` dependencies against the subchart's own `Chart.yaml` version field
- [ ] `requireRealSecret` helper used for secrets that must not be `CHANGE_ME` in production

## 12-Factor Build Rules (all repos)

- [ ] No `NEXT_PUBLIC_*` env vars injected via `--build-arg` (bakes env-specific values into the image)
- [ ] No `.env*` files committed that differ per environment
- [ ] No `ARG`/`ENV` in Dockerfile that varies across dev/staging/prod (except `NODE_ENV=production`)
- [ ] No hardcoded URLs in source code
- [ ] Secrets never exposed in image layer history (no `--build-arg` for secrets)
- [ ] Human-procedural rules backed by CI enforcement (per P-026)

# Severity Criteria

- **Critical** -- blocks build, causes data loss, or exposes secrets (e.g., eager DB init, secrets in image layers, bare `except:` swallowing errors)
- **High** -- violates type safety or correctness (e.g., `any` type, missing auth check, missing `force-dynamic`)
- **Medium** -- violates maintainability or style conventions (e.g., wrong naming convention, missing type hint on return value)
- **Low** -- minor improvement opportunity (e.g., could use a more specific type, TODO comment without ticket reference)

# Self-check before returning

- [ ] Every violation cites `file:line` (e.g., `src/app/api/devices/route.ts:42`)
- [ ] Every violation has a severity level assigned per the criteria above
- [ ] Every violation with severity Critical or High includes before/after code
- [ ] No violation emitted below 80% confidence without `[LOW-CONFIDENCE]` marker
- [ ] The NEXT_PUBLIC_ rule is applied consistently: allowed in source, prohibited as `--build-arg`
- [ ] Auto-generated files, vendored code, and node_modules are excluded from review
- [ ] The report contains zero placeholder text
- [ ] The `STANDARDS-VIOLATIONS:` summary line is populated
- [ ] No function-length or complexity findings were emitted (those belong to `code-quality`)

# Common mistakes to avoid

- DO NOT flag `NEXT_PUBLIC_*` in TypeScript source code as a violation -- it is allowed in source for local dev; only the `--build-arg` injection at Docker build time is prohibited
- DO NOT flag `process.env.X` inside a function body (lazy access) as a violation -- only module-scope `process.env.X` access is prohibited (breaks build)
- DO NOT review files in `node_modules/`, `.next/`, `__pycache__/`, or `*.generated.*`
- DO NOT report violations without `file:line` -- a violation without a citation is not actionable
- DO NOT emit function-length or complexity findings -- those belong to `code-quality`
- DO NOT grep for `: any` without word-boundary awareness -- use regex `: any\b` or `as any\b` to avoid matching string literals like `"company"`

# Escalation

- **Stop and ask when:** a file contains more than 20 Critical-severity violations (likely a legacy file requiring a dedicated refactoring plan, not piecemeal fixes)
- **Stop and ask when:** a standard appears contradictory (e.g., two skills give conflicting guidance) -- surface both and ask the user to resolve
- **Refuse and explain when:** asked to auto-fix violations in-place (this is a review skill; fixes require `@implementation-agent` or the developer)

# Examples

<example>
## Worked example: reviewing a route handler for standards compliance

**Input:** `scope: apps/<mainApp>/src/app/api/devices/route.ts`, `repo_type: web-app`

**File content (excerpt):**
```typescript
// apps/<mainApp>/src/app/api/devices/route.ts
import { auth } from '@/lib/auth';
import { getDb } from '@/lib/db';
import { devices } from '@/lib/db/schema';

export async function GET(req: Request) {
  const session = await auth();
  if (!session) return Response.json({ error: 'Unauthorized' }, { status: 401 });

  const data: any = await getDb().select().from(devices);
  return Response.json(data);
}
```

**Report:**

## Code Standards Report

**Scope:** `apps/<mainApp>/src/app/api/devices/route.ts`
**Repository:** web-app (TypeScript / Next.js 15)

| # | Severity | Location | Violation | Fix |
|---|----------|----------|-----------|-----|
| 1 | Critical | `route.ts:1` | Missing `export const dynamic = 'force-dynamic'` -- route calls `auth()` which reads session cookies at runtime | Add `export const dynamic = 'force-dynamic';` after imports |
| 2 | High | `route.ts:10` | Variable `data` typed as `any` -- violates strict type safety | Remove `: any` annotation; Prisma returns a typed result already |

**Before (violation #1):**
```typescript
import { auth } from '@/lib/auth';
```

**After (fix #1):**
```typescript
import { auth } from '@/lib/auth';

export const dynamic = 'force-dynamic';
```

**Before (violation #2):**
```typescript
const data: any = await getDb().select().from(devices);
```

**After (fix #2):**
```typescript
const data = await getDb().select().from(devices);
```

**Summary:** 2 violations (1 Critical, 1 High). Both are single-line fixes.

STANDARDS-VIOLATIONS: 2 | CRITICAL: 1 | HIGH: 1 | MEDIUM: 0 | LOW: 0
</example>

# Failure modes

- **Mode 1:** Grep for `: any` matches inside string literals or comments -- detect: false positive in non-code context; fix: read surrounding lines to verify the match is in a type annotation position; use regex with word boundary
- **Mode 2:** `force-dynamic` check flags a page that intentionally uses static generation -- detect: page does not import `auth()` or read runtime env; fix: verify the import chain before flagging
- **Mode 3:** NEXT_PUBLIC_ rule flags a legitimate local `.env.local` file -- detect: the file is `.env.local` (gitignored, dev-only); fix: only flag `.env` files that are committed to version control and differ per environment

# Related skills

- `code-quality` -- defer to this skill for function length, nesting depth, and cyclomatic complexity checks; compose when a standards review reveals quality issues. Composition is a depth-1 fan-out under `@code-auditor` (runs both as leaves over the SAME scope, aggregates the `QUALITY-SCORE:` + `STANDARDS-VIOLATIONS:` headers) -- never a nested delegation chain or routing handoff.
- `api-contract` -- defer to this skill for field-level alignment between Prisma, Zod, and route handlers; compose when a naming convention violation affects API field names
- `code-search` -- defer to this skill when the files to review need to be located first
