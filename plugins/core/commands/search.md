---
description: Fast code search across the project's repositories using ripgrep
allowed-tools:
  - code_text_search
  - code_file_search
  - code_batch_search
  - WebFetch
color: red
---

# Code Search Command

Search for: $ARGUMENTS

## Instructions

You are a code search assistant. Use the code search tools to find the requested information quickly.

### Available Tools

1. **code_text_search** - Search for text in code
   ```javascript
   code_text_search({
     query: "search term",
     repo: "<repo>",            // one of the project's repos (see project.json → repos)
     filePattern: "*.ts",       // optional filter
     wholeWord: true             // optional exact match
   })
   ```

2. **code_file_search** - Find files by pattern
   ```javascript
   code_file_search({
     pattern: "*Order*.tsx",
     repo: "<repo>"
   })
   ```

3. **code_batch_search** - Search all repos at once
   ```javascript
   code_batch_search({
     query: "OrderStatus"
     // searches all repos by default
   })
   ```

### Fallback (if MCP tools not available)

Use WebFetch to call the REST API:

```javascript
WebFetch({ url: "http://localhost:4001/search/text?q=QUERY&repo=<codePath>/<repo>" })
```
(`codePath` and the repo list come from the project's `.claude/project.json`.)

### Repositories

Read the searchable repos from the project's `.claude/project.json` (`repos`, plus `mainApp` and
`device` when present). Do not assume a fixed repo layout — it varies per project.

### Search guidance

- Default to the project's `mainApp` (from `project.json`) for web app + API work.
- Keep the `device`/edge repo in scope for telemetry, edge-runtime, or firmware issues, if the project defines one.
- Keep infrastructure/deployment repos in scope for Cloud Run / cluster deployment work.

### Response Format

Provide results in this format:

```markdown
## Search Results for "[query]"

Found X matches in Y repositories:

### [repo_name] (Z matches)

📄 path/to/file.ts:123
   > matching line content

📄 path/to/another.ts:456
   > matching line content
```

---

Now search for: $ARGUMENTS