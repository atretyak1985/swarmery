# code-search — tool selection, procedure, and checks

## Required environment

- Runtime: `.claude/skills/code-search/SKILL.md`
- Tools: Grep, Glob, Read, mcp__auggie__codebase-retrieval (MCP tool, not listed in allowed-tools because it is an external MCP server tool; it is available at runtime)
- Frontmatter flag `disable-model-invocation: true` keeps this skill user/agent-explicit (it does not auto-trigger on matching phrases). As a knowledge module it never dispatches subagents in any case; depth control lives at the agent layer (see ARCHITECTURE.md -> Delegation depth).
- File system assumptions:
  - The project's monorepo or worktree is at the current working directory
  - Repository paths are relative to the workspace root (e.g., `apps/<mainApp>/`, `<device>/`, `<infrastructure-repo>/`)

## Inputs

- `query: string` — what to search for (symbol name, file pattern, or natural language question)
- `search_type: "exact" | "pattern" | "semantic"` — determines which tool to use
- `scope: string` — optional path restriction (e.g., `apps/<mainApp>/src/`)

## Outputs

- **Format:** list of matches with `file:line` citations and surrounding context (3 lines)
- **Length budget:** max 50 results per search; paginate or narrow scope if more
- **Downstream handoff:** When passing results to a downstream skill (`code-quality`, `api-contract`, `code-standards`), format as a list of `file:line:snippet` tuples that the downstream skill can consume directly.

## Tool Selection Guide

| Need | Tool | When to use | Example |
|------|------|-------------|---------|
| Find ALL occurrences of a symbol | Grep | You know the exact name (function, class, variable, import) | `Grep("getDb", glob: "*.ts", path: "apps/<mainApp>/src")` |
| Find files by name pattern | Glob | You know the file naming convention | `Glob("apps/<mainApp>/src/app/api/**/route.ts")` |
| Understand what code does | codebase-retrieval | You have a natural language question about architecture or data flow | `"How does telemetry flow from the device service to the browser?"` |
| Read a known file section | Read with offset/limit | You know the file and approximate line range | `Read("src/lib/db/schema.ts", offset: 40, limit: 30)` |

## Procedure

1. **Classify the query** — Determine whether the user needs exact symbol lookup (Grep), file pattern matching (Glob), or semantic understanding (codebase-retrieval). Checkpoint: tool selected.

2. **Determine scope** — If no scope is specified, default to the repository most likely to contain the result based on the query content:
   - TypeScript/React/Next.js terms -> `apps/<mainApp>/`
   - Python/device/hardware terms -> `<device>/`
   - Deployment/chart terms -> `<infrastructure-repo>/`
   - SQL/migration/infrastructure terms -> `<infrastructure-repo>/`
   In a large repo (monorepo root, or any workspace with vendored/build output), never run Grep with `path: "."` and no `glob` — pick the most likely repo/subtree first. Ripgrep itself has a hard timeout (commonly 20s); an unscoped search over a big tree hits it before narrowing would help.
   For **Glob**, scope comes from `path`, not from the literal prefix already baked into `pattern` — Glob's directory walk is driven by `path`, so a narrow pattern at a broad `path` still walks the whole tree underneath `path` before filtering. Push `path` down to the pattern's narrowest literal ancestor directory instead of leaving a wildcard segment to do the narrowing: for `apps/<mainApp>/migrations/01[23]*.sql`, set `path: "apps/<mainApp>/migrations"` and `pattern: "01[23]*.sql"`, not `path: "apps/<mainApp>"` (or the repo root) with the longer pattern. Checkpoint: scope determined.

3. **Exclude noise directories** — Before executing, confirm the scope does not sweep `node_modules/`, `vendor/`, `dist/`, `build/`, `.git/`, or other generated/dependency trees: narrow `path` below them, or add a negated `glob` (e.g. `glob: "!**/node_modules/**"`) when the tool call supports it. These directories are the usual reason a broad search matches far more files than intended and runs into the timeout. This step does not apply when one of those trees is the deliberate search target (e.g. hunting for a specific package inside `node_modules/.pnpm`) — excluding it would exclude the thing being searched for; that case is Mode 4b below, not this step. Checkpoint: noise directories excluded (or confirmed inapplicable because the tree is the target).

4. **Execute the search** — Run the selected tool with the query and scope. **For codebase-retrieval results:** after receiving results, verify the top 2 returned file paths exist and contain the cited content by running Read on them. Flag any result whose file path does not exist or whose content does not match as `[POTENTIALLY-STALE]`. **On a ripgrep timeout** ("Ripgrep search timed out after N seconds"), do not surface the bare error as the final answer and do not blindly retry the same call: identify which failure mode applies (Mode 4a: pattern narrow but `path` too broad — narrow `path` to the literal ancestor and retry once; Mode 4b: target is intentionally inside a dependency/build tree — go straight to `Bash` with an explicit timeout, no built-in retry; Mode 4c: incidental noise-directory sweep — retry once with the scope narrowed and noise excluded per step 3). If a Mode 4a/4c retry still times out, report which scope(s) were attempted and ask the user for a narrower path instead of giving up silently. Checkpoint: results returned and verified, or a narrower retry/Bash fallback was attempted and reported. Note: `plugins/core/hooks/search-scope-guard.sh` now fires in real time as a `PreToolUse` hook on every `Grep`/`Glob` call project-wide (`warn` mode — it logs and prints, it does not block), so this guidance reaches sessions that never read this skill (this skill carries `disable-model-invocation: true`, so only `researcher`/`implementation-agent`-class agents load it explicitly); the hook's stderr message and this step describe the same three modes.

5. **Handle zero results** — If the search returns no results:
   a. Broaden the scope (remove path restriction)
   b. Try an alternative tool (e.g., switch from Grep to codebase-retrieval)
   c. Try variant spellings (camelCase vs snake_case vs PascalCase)
   d. If all attempts fail, report "no results found" with the queries attempted
   Checkpoint: either results found or all alternatives exhausted.

6. **Format results** — Present each match with `file:line` and 3 lines of surrounding context. Sort by relevance (exact matches first, then partial matches). Checkpoint: formatted output ready.

7. **Final acceptance check** — Results have `file:line` citations, search tool and query are documented, no `[POTENTIALLY-STALE]` results are presented as authoritative.

## Self-check before returning

- [ ] Every result includes a `file:line` citation (not just a file path)
- [ ] The search tool used is documented (Grep, Glob, or codebase-retrieval)
- [ ] The query executed is documented (exact string or natural language)
- [ ] Zero-result searches include the alternatives attempted before reporting "not found"
- [ ] No hardcoded absolute paths (all paths are relative to workspace root)
- [ ] Results are sorted by relevance (exact matches first)
- [ ] codebase-retrieval results have been spot-checked with Read (top 2 verified)
- [ ] No `[POTENTIALLY-STALE]` result is presented without that marker
- [ ] No Grep ran unscoped (`path: "."`, no `glob`) over a large repo or monorepo root without first excluding `node_modules`/`vendor`/`dist`/`build`/`.git`
- [ ] No Glob ran with `path` set above the pattern's literal ancestor directory (a narrow `pattern` at a broad `path` still walks the whole tree) — unless the target deliberately sits inside a dependency/build tree, in which case Bash was used instead

## Common mistakes to avoid

- DO NOT use hardcoded absolute paths like `/absolute/path/to/project` — use relative paths from the workspace root or the current working directory
- DO NOT use Grep for semantic questions ("how does X work?") — use codebase-retrieval instead
- DO NOT use codebase-retrieval for exact symbol lookup ("find all uses of getDb") — use Grep instead
- DO NOT return file paths without line numbers — always include the line where the match occurs
- DO NOT search `/` (filesystem root) — always scope to the workspace or a specific repository
- DO NOT assume stale repository names — use the current canonical repo names from `.claude/project.json` → `repos`
- DO NOT present codebase-retrieval results as authoritative without verifying at least the top 2 with Read
- DO NOT run Grep across an entire monorepo (`path: "."`, no `glob`) when a specific repo or subtree is implied by the query — scope first, exclude `node_modules`/`vendor`/`dist`/`build` second, search third
- DO NOT leave `path` at a broad root and expect a narrow `pattern` to do the scoping for Glob — Glob's walk is driven by `path`, not by the literal prefix already in `pattern`; push `path` down to the pattern's literal ancestor directory instead
- DO NOT retry a Mode 4b timeout (target intentionally inside `node_modules`/`.pnpm`/`vendor`/build caches) by calling the same built-in tool again — go to `Bash` with an explicit timeout instead, since no exclusion is possible when the tree itself is the target

## Escalation

- **Stop and ask when:** the search returns more than 50 results and narrowing the scope is not obvious
- **Stop and ask when:** the query is ambiguous and could match multiple unrelated concepts (e.g., "service" could mean a deployment Service or a TypeScript service class)
- **Stop and ask when:** a scoped, noise-excluded retry still times out — report the scopes already tried and ask which subtree to search, rather than retrying a third time or giving up with a bare error
- **Escalate to Bash when:** a timeout matches Mode 4a (path too broad for an already-narrow pattern) or Mode 4b (target is intentionally inside a dependency/build tree) — go straight to `Bash` with an explicit timeout rather than blindly retrying the same built-in Grep/Glob call a second time
- **Heed the hook when:** a `Grep`/`Glob` call draws a `search-scope-guard` warning on stderr naming `glob-broad-path`, `glob-dependency-tree`, or `grep-unscoped-root` — it is this skill's own Mode 4a/4b/4c classification firing independently of whether this skill was loaded; apply the rewrite it suggests rather than re-issuing the same call
- **Refuse and explain when:** asked to search external repositories or filesystems outside the project workspace

## Failure modes

- **Mode 1:** Grep returns false positives from comments or string literals — detect: matches are in comment or string context; fix: add surrounding context and let the user filter, or refine the regex to exclude comment lines
- **Mode 2:** codebase-retrieval returns stale results after recent file changes — detect: Read verification shows file content does not match returned snippet; fix: mark as `[POTENTIALLY-STALE]` and re-search with Grep instead
- **Mode 3:** Glob pattern misses files due to incorrect nesting depth — detect: known file not in results; fix: use `**` for recursive matching and verify the path prefix is correct
- **Mode 4a:** Glob/Grep `pattern` is already narrow but `path` is too broad (e.g. a tight filename pattern run at the monorepo root) — detect: the pattern itself has few possible matches, yet the call still times out; fix: narrow `path` down to the pattern's literal ancestor directory (see step 2) so the directory walk itself is small, instead of relying on the pattern to filter a huge walk after the fact.
- **Mode 4b:** the target is intentionally inside a huge dependency tree (`node_modules/.pnpm`, `vendor`, build caches) where no exclusion helps, because the tree being walked *is* the target — detect: the search path necessarily includes a dependency/build directory and no narrower literal ancestor exists above the wildcard; fix: don't retry the same built-in call — fall back to `Bash` (`find`, or `rg --files` piped to `grep`) with an explicit timeout, since Bash's timeout is adjustable up to 600000ms while the built-in Grep/Glob's internal ripgrep timeout (~20s) is fixed and not configurable from this repo.
- **Mode 4c:** an incidental sweep through noise directories (`node_modules/`, `vendor/`, `dist/`, `build/`, `.git/`) that aren't the target but get walked anyway — detect: the error names ripgrep and a timeout, typically after a `path: "."` or repo-root search with no `glob`; fix: narrow `path` to the single most likely repo/subtree, exclude those noise directories via `path` or a negated `glob`, and retry once; if the narrowed retry still times out, report the scopes attempted and ask for a narrower path instead of returning the bare timeout

## Related skills

- `code-quality` — compose when search results need to be audited for structural quality (search first, then audit); pass results as `file:line:snippet` tuples
- `api-contract` — compose when search results reveal API layer files that need contract verification; pass results as `file:line:snippet` tuples
- `code-standards` — compose when search results reveal code that needs convention review; pass results as `file:line:snippet` tuples
