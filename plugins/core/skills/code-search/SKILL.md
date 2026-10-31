---
name: code-search
description: "Select the right search tool (Grep, Glob, codebase-retrieval) to locate existing code, symbols, or files across the project's monorepo. Don't use it for creating new files, implementing features, or reviewing code quality."
version: "1.0.0"
owner: "swarmery-core"
allowed-tools: Grep, Glob, Read
disable-model-invocation: true
docs:
  status: reviewed
  source_sha: ccc8c6baa5c1
  updated: 2026-08-06
---

# Purpose

Selects the most efficient search tool — Grep for exact symbol lookup, Glob for file-name patterns, codebase-retrieval for semantic/architectural questions — to locate code across the project's repositories (`.claude/project.json` → `repos`). Produces matching file paths with line numbers and context. Read-only, tool-selection skill. Placeholders: `<mainApp>`, `<device>`, `<infrastructure-repo>` come from project.json.

# Rules (never violate)

- Grep for exact symbols, Glob for name patterns, codebase-retrieval for "how does X work?" — never swap them.
- Every result carries a `file:line` citation, never a bare file path; max 50 results per search.
- Verify the top 2 codebase-retrieval results with Read; anything mismatched is marked `[POTENTIALLY-STALE]`, never presented as authoritative.
- No hardcoded absolute paths, never search the filesystem root — scope to the workspace; repo names come from `.claude/project.json` → `repos`.
- Never run an unscoped `path: "."` Grep over a large repo without excluding `node_modules`/`vendor`/`dist`/`build`/`.git` — that combination is the usual cause of a ripgrep timeout.
- Zero-result searches report the alternatives attempted before saying "not found".
- Refuse searches outside the project workspace; stop and ask on >50 results or an ambiguous query.

# Resources

- Read `resources/search-procedure.md` when running a search — tool selection table, procedure, scope defaults, self-check, mistakes, escalation, failure modes.
- Read `resources/query-patterns.md` when composing queries — per-repo Grep/Glob patterns and two worked examples.

# How to use

## What it does

Picks the right search tool for a lookup and runs it, so you stop guessing between exact match, file-name pattern, and "how does this work?". It classifies your query, scopes it to the most likely repository, runs Grep, Glob, or semantic retrieval, and returns matches with `file:line` citations and surrounding context. It never edits anything.

## When to use it

- You need every occurrence of a function, type, or variable across the workspace.
- You need files following a naming convention: route handlers, test files, values files.
- You are starting a task cold, or have a natural-language question about a cross-repo data flow.

Not for writing code (`api-integration`), judging code (`code-quality`/`code-standards`), files you already know, or conceptual questions answered by documentation.

## How to invoke

```
Skill(skill: "core:code-search")
```

State your `query` in plain words; optionally add `search_type` (`exact`, `pattern`, `semantic` — inferred when omitted) and a `scope` path restriction.

## Worked example

```
Skill(skill: "core:code-search")
query: "telemetryEmitter", search_type: "exact", scope: "apps/<mainApp>/src/"
```

Classified as an exact symbol lookup, Grep runs over `*.ts` under that path. Five matches across two files come back: the emitter declared at `ws-client.ts:5`, emitted at line 12, then imported and subscribed in `stream/route.ts` at lines 2, 11, and 14 — the definition and every consumer in one pass.

## Related

- `code-quality`, `api-contract`, `code-standards` — compose downstream: search first, then hand them the `file:line:snippet` tuples for audit, contract verification, or convention review.
