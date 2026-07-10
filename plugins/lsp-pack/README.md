# lsp-pack

Serena LSP MCP server for symbol-level code navigation (references, definitions,
semantic search) — complements text search.

## ⚠️ Machine prerequisite

The pack only *configures* the MCP server. The **serena binary must be installed
on the machine** (otherwise every session logs a failed MCP launch):

```bash
uv tool install serena-agent     # or: uvx --from serena-agent serena --help
which serena                     # must resolve
```

## Enable per project

```jsonc
"enabledPlugins": { "lsp-pack@swarmery": true }
```

Default `--project .` indexes the project root. To index a sub-app instead
(monorepos), override in the project's own `.mcp.json`:

```json
{ "mcpServers": { "serena": { "command": "serena",
  "args": ["start-mcp-server", "--context", "claude-code", "--project", "apps/my-main-app"] } } }
```

First session indexes the project (takes a minute on big repos); the index lives in `.serena/` (gitignore it).
