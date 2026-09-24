---
name: area-lessons
version: "1.0.0"
owner: "swarmery-core"
description: "Use this skill when the operator wants to see the active lessons learned by earlier headless runs for the directory they are working in, pulled on demand from the local swarmery daemon. Read-only. Don't use it to accept, edit or promote lessons (that is the daemon's Lessons page) or to search code (use code-search)."
disable-model-invocation: true
allowed-tools: Bash
color: teal
docs:
  status: draft
  updated: 2026-09-23
---

# Purpose

List the ACTIVE lessons whose area globs overlap the current directory, read
from the local swarmery daemon (`GET /api/lessons?status=active&area=<dir>`).
Headless phase and plan runs already receive these lessons in their prompt;
interactive sessions get them only when the operator asks, through this skill.
Nothing here writes a file, a memory entry, or a hook.

# Rules (never violate)

1. Read-only: one `curl` GET against the daemon on `localhost:7777`, nothing else
   that changes state.
2. Never copy lessons into `MEMORY.md`, `CLAUDE.md`, settings, or any hook. A
   lesson that belongs in `CLAUDE.md` is promoted from the daemon's Lessons page,
   which commits it on a new branch for review.
3. Quote each lesson with its id (`[L-12]`), so a later reply can cite the one
   it relied on.
4. When the daemon is not reachable, say so in one line and stop. Never guess
   lessons.

# Procedure

1. Resolve the directory relative to the repository root:
   `git rev-parse --show-prefix` (empty at the root; then use `.`).
2. Query the daemon, URL-encoding the directory:
   ```bash
   curl -s --globoff "http://localhost:7777/api/lessons?status=active&area=<dir>"
   ```
3. Render `lessons[]` as one line each, `- [L-<id>] <guidance>  (areas: <areaGlobs>)`,
   newest first as returned. An empty list is a valid answer: "No active lessons
   for `<dir>`."

# How to use

## What it does

Shows the lessons that earlier headless runs in this project learned about the
directory you are in: one line per lesson with its citable id, its guidance and
the areas it applies to. It reads them from the local daemon and changes
nothing. The same matcher decides which lessons a headless run receives.

## When to use it

- You are about to work by hand in a directory that headless runs touched before,
  and want what they learned.
- You want to check which lessons a phase in this area would be handed.

Not for reviewing, accepting, retiring or promoting lessons: that is the daemon's
Lessons page. Not for code search either (`code-search`).

## How to invoke

```
Skill(skill: "core:area-lessons")
```

No arguments: it uses the current directory. Name another directory in plain
words to look there instead, for example "lessons for `internal/<pkg>`".

## Worked example

Working in `apps/<mainApp>/src/orders`, the skill resolves the prefix, calls
`/api/lessons?status=active&area=apps/<mainApp>/src/orders`, and prints:

```
- [L-12] Read the line-items schema before writing an orders migration.  (areas: apps/<mainApp>/src/orders/**)
```

Cite `[L-12]` in your reply if you act on it.

## Related

- `code-search` — find the code the lesson talks about.
- `workspace-plans` — plan docs whose `## Forecast` areas decide which lessons a
  headless run is handed.
