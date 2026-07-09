---
description: Find files by pattern across the project's repositories
allowed-tools:
  - code_file_search
  - WebFetch
color: red
---

# Find Files Command

Find files matching: $ARGUMENTS

## Instructions

Use `code_file_search` to find files by pattern.

### Usage

```javascript
code_file_search({
  pattern: "$ARGUMENTS",
  repo: "<repo>"  // one of the project's repos (project.json → repos); adjust based on context
})
```

### Examples

- `*Order*.tsx` - Find all files with "Order" in the name
- `*.spec.ts` - Find all test files
- `*.sql` - Find all SQL migration files
- `*Service*.ts` - Find all service files
- `values*.yaml` - Find deploy/values files

### Repositories

If the user doesn't specify a repo, pick the most likely one from the project's `.claude/project.json`
(`repos`, `mainApp`, `device`) based on the file pattern — e.g. web extensions (`.tsx`, `route.ts`)
map to the `mainApp`; `.py`/firmware patterns map to the `device` repo if one is defined; deploy
manifests map to the infrastructure repo. The layout varies per project, so read it — don't assume.

### Fallback

If MCP tools are not available:

```javascript
WebFetch({ url: "http://localhost:4001/search/files?pattern=PATTERN&repo=<codePath>/<repo>" })
```
(`codePath` from `project.json`.)

---

Now find files matching: $ARGUMENTS