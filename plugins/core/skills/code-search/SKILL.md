---
name: code-search
description: "Select the right search tool (Grep, Glob, codebase-retrieval) to locate existing code, symbols, or files across the project's monorepo. Don't use it for creating new files, implementing features, or reviewing code quality."
version: "1.0.0"
owner: "agentry-core"
allowed-tools: Grep, Glob, Read
disable-model-invocation: true
---

# Purpose

Selects the most efficient search tool (Grep for exact symbol lookup, Glob for file-name pattern matching, codebase-retrieval for semantic/architectural queries) to locate code across the project's repositories (see `.claude/project.json` → `repos`). Produces a list of matching file paths with line numbers and context. This is a read-only, tool-selection skill.

Placeholders: `<mainApp>` = `project.json → mainApp` (the main web app); `<device>` = `project.json → device` (the device/edge repo, if the project has one); `<infrastructure-repo>` = the project's infrastructure/deployment repo.

# When to use / When NOT to use

**Use when:**
- Need to find all occurrences of a specific function, type, or variable name
- Need to locate files matching a naming pattern (e.g., all route handlers, all test files)
- Need to understand how a feature or data flow is implemented across repos (semantic search)
- Starting a new task and need orientation on where relevant code lives

**Do NOT use when:**
- Creating new files or implementing features (use `api-integration` or relevant implementation skill)
- Reviewing code for quality, conventions, or security (use `code-quality`, `code-standards`)
- Running a known command or script (the search is not needed if you already know the path)
- Answering conceptual questions about the device protocol or deployment architecture (use documentation, not code search)

# Required environment

- Runtime: `.claude/skills/code-search/SKILL.md`
- Tools: Grep, Glob, Read, mcp__auggie__codebase-retrieval (MCP tool, not listed in allowed-tools because it is an external MCP server tool; it is available at runtime)
- Frontmatter flag `disable-model-invocation: true` keeps this skill user/agent-explicit (it does not auto-trigger on matching phrases). As a knowledge module it never dispatches subagents in any case; depth control lives at the agent layer (see ARCHITECTURE.md -> Delegation depth).
- File system assumptions:
  - The project's monorepo or worktree is at the current working directory
  - Repository paths are relative to the workspace root (e.g., `apps/<mainApp>/`, `<device>/`, `<infrastructure-repo>/`)

# Inputs

- `query: string` -- what to search for (symbol name, file pattern, or natural language question)
- `search_type: "exact" | "pattern" | "semantic"` -- determines which tool to use
- `scope: string` -- optional path restriction (e.g., `apps/<mainApp>/src/`)

# Outputs

- **Format:** list of matches with `file:line` citations and surrounding context (3 lines)
- **Length budget:** max 50 results per search; paginate or narrow scope if more
- **Downstream handoff:** When passing results to a downstream skill (`code-quality`, `api-contract`, `code-standards`), format as a list of `file:line:snippet` tuples that the downstream skill can consume directly.

# Tool Selection Guide

| Need | Tool | When to use | Example |
|------|------|-------------|---------|
| Find ALL occurrences of a symbol | Grep | You know the exact name (function, class, variable, import) | `Grep("getDb", glob: "*.ts", path: "apps/<mainApp>/src")` |
| Find files by name pattern | Glob | You know the file naming convention | `Glob("apps/<mainApp>/src/app/api/**/route.ts")` |
| Understand what code does | codebase-retrieval | You have a natural language question about architecture or data flow | `"How does telemetry flow from the device service to the browser?"` |
| Read a known file section | Read with offset/limit | You know the file and approximate line range | `Read("src/lib/db/schema.ts", offset: 40, limit: 30)` |

# Procedure

1. **Classify the query** -- Determine whether the user needs exact symbol lookup (Grep), file pattern matching (Glob), or semantic understanding (codebase-retrieval). Checkpoint: tool selected.

2. **Determine scope** -- If no scope is specified, default to the repository most likely to contain the result based on the query content:
   - TypeScript/React/Next.js terms -> `apps/<mainApp>/`
   - Python/device/hardware terms -> `<device>/`
   - Deployment/chart terms -> `<infrastructure-repo>/`
   - SQL/migration/infrastructure terms -> `<infrastructure-repo>/`
   Checkpoint: scope determined.

3. **Execute the search** -- Run the selected tool with the query and scope. **For codebase-retrieval results:** after receiving results, verify the top 2 returned file paths exist and contain the cited content by running Read on them. Flag any result whose file path does not exist or whose content does not match as `[POTENTIALLY-STALE]`. Checkpoint: results returned and verified.

4. **Handle zero results** -- If the search returns no results:
   a. Broaden the scope (remove path restriction)
   b. Try an alternative tool (e.g., switch from Grep to codebase-retrieval)
   c. Try variant spellings (camelCase vs snake_case vs PascalCase)
   d. If all attempts fail, report "no results found" with the queries attempted
   Checkpoint: either results found or all alternatives exhausted.

5. **Format results** -- Present each match with `file:line` and 3 lines of surrounding context. Sort by relevance (exact matches first, then partial matches). Checkpoint: formatted output ready.

6. **Final acceptance check** -- Results have `file:line` citations, search tool and query are documented, no `[POTENTIALLY-STALE]` results are presented as authoritative.

# Grep Patterns by Repository

### Main app (TypeScript)
```
Grep("getDb\\(\\)", glob: "*.ts", path: "apps/<mainApp>/src")
Grep("export default|export function", glob: "*.tsx", path: "apps/<mainApp>/src/app")
Grep("EventSource|useTelemetry", glob: "*.{ts,tsx}", path: "apps/<mainApp>/src")
```

### Device repo (Python)
```
Grep("async def", glob: "*.py", path: "<device>/src")
Grep("telemetry", glob: "*.py", path: "<device>")
```

### Deployment config
```
Grep("image:", glob: "*.yaml", path: "<infrastructure-repo>/charts")
Grep("nodePort:", glob: "*.{yaml,tpl}", path: "<infrastructure-repo>")
```

### Cross-Repo
```
Grep("DeviceService", path: ".")
Grep("/ws/", path: ".", glob: "*.{py,ts,yaml,tpl}")
```

# Glob Patterns

```
# API route handlers
Glob("apps/<mainApp>/src/app/api/**/route.ts")

# React components
Glob("apps/<mainApp>/src/components/**/*.tsx")

# Test files
Glob("<device>/test/**/*.py")
Glob("apps/<mainApp>/src/**/*.test.{ts,tsx}")

# Deployment values
Glob("<infrastructure-repo>/**/values*.yaml")

# Database migrations
Glob("<infrastructure-repo>/files/backendMigration/*.sql")
```

# Self-check before returning

- [ ] Every result includes a `file:line` citation (not just a file path)
- [ ] The search tool used is documented (Grep, Glob, or codebase-retrieval)
- [ ] The query executed is documented (exact string or natural language)
- [ ] Zero-result searches include the alternatives attempted before reporting "not found"
- [ ] No hardcoded absolute paths (all paths are relative to workspace root)
- [ ] Results are sorted by relevance (exact matches first)
- [ ] codebase-retrieval results have been spot-checked with Read (top 2 verified)
- [ ] No `[POTENTIALLY-STALE]` result is presented without that marker

# Common mistakes to avoid

- DO NOT use hardcoded absolute paths like `/absolute/path/to/project` -- use relative paths from the workspace root or the current working directory
- DO NOT use Grep for semantic questions ("how does X work?") -- use codebase-retrieval instead
- DO NOT use codebase-retrieval for exact symbol lookup ("find all uses of getDb") -- use Grep instead
- DO NOT return file paths without line numbers -- always include the line where the match occurs
- DO NOT search `/` (filesystem root) -- always scope to the workspace or a specific repository
- DO NOT assume stale repository names -- use the current canonical repo names from `.claude/project.json` → `repos`
- DO NOT present codebase-retrieval results as authoritative without verifying at least the top 2 with Read

# Escalation

- **Stop and ask when:** the search returns more than 50 results and narrowing the scope is not obvious
- **Stop and ask when:** the query is ambiguous and could match multiple unrelated concepts (e.g., "service" could mean a deployment Service or a TypeScript service class)
- **Refuse and explain when:** asked to search external repositories or filesystems outside the project workspace

# Examples

<example>
## Worked example: finding all uses of the telemetry emitter

**Input:** `query: "telemetryEmitter"`, `search_type: "exact"`, `scope: "apps/<mainApp>/src/"`

**Step 1:** Classify as exact symbol lookup -> Grep

**Step 2:** Execute
```
Grep("telemetryEmitter", glob: "*.ts", path: "apps/<mainApp>/src")
```

**Results:**
```
apps/<mainApp>/src/lib/telemetry/ws-client.ts:5  export const telemetryEmitter = new EventEmitter();
apps/<mainApp>/src/lib/telemetry/ws-client.ts:12   telemetryEmitter.emit(`telemetry:${deviceId}`, parsed.data);
apps/<mainApp>/src/app/api/telemetry/stream/route.ts:2  import { telemetryEmitter } from '@/lib/telemetry/ws-client';
apps/<mainApp>/src/app/api/telemetry/stream/route.ts:11   telemetryEmitter.on(`telemetry:${deviceId}`, handler);
apps/<mainApp>/src/app/api/telemetry/stream/route.ts:14     telemetryEmitter.off(`telemetry:${deviceId}`, handler);
```

**Summary:** 5 occurrences in 2 files. Defined in `ws-client.ts:5`, consumed in `stream/route.ts`.
</example>

<example>
## Worked example: semantic search for a feature flow

**Input:** `query: "How does order creation work end to end?"`, `search_type: "semantic"`

**Step 1:** Classify as semantic question -> codebase-retrieval

**Step 2:** Execute
```
codebase-retrieval("How does order creation work in the main app, from route handler to database?", directory_path: "apps/<mainApp>")
```

**Step 3:** Verify top 2 results with Read -- confirmed `src/app/api/orders/route.ts` exists and contains POST handler at returned line number.

**Results:** codebase-retrieval returns relevant snippets from:
- `src/app/api/orders/route.ts` (POST handler)
- `src/lib/db/schema.ts` (orders table definition)
- `src/app/(dashboard)/orders/new/page.tsx` (order creation form)
</example>

# Failure modes

- **Mode 1:** Grep returns false positives from comments or string literals -- detect: matches are in comment or string context; fix: add surrounding context and let the user filter, or refine the regex to exclude comment lines
- **Mode 2:** codebase-retrieval returns stale results after recent file changes -- detect: Read verification shows file content does not match returned snippet; fix: mark as `[POTENTIALLY-STALE]` and re-search with Grep instead
- **Mode 3:** Glob pattern misses files due to incorrect nesting depth -- detect: known file not in results; fix: use `**` for recursive matching and verify the path prefix is correct

# Related skills

- `code-quality` -- compose when search results need to be audited for structural quality (search first, then audit); pass results as `file:line:snippet` tuples
- `api-contract` -- compose when search results reveal API layer files that need contract verification; pass results as `file:line:snippet` tuples
- `code-standards` -- compose when search results reveal code that needs convention review; pass results as `file:line:snippet` tuples
