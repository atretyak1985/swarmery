---
domain: 2 - Tool Design & MCP Integration
module: "2.4"
title: "MCP Server Integration"
---

# 2.4 MCP Server Integration

## Overview

MCP (Model Context Protocol) servers extend Claude's capabilities by connecting it to external systems — databases, APIs, development tools, issue trackers. Configuring them correctly determines whether your team shares a consistent toolset or descends into configuration chaos. When reviewing an implementation, the questions below are the ones that separate a robust integration from a fragile one.

### The Scoping Hierarchy

MCP server configuration exists at two levels, and confusing them is a common source of problems.

**Project-level: `.mcp.json`**
Lives in the project repository root. Version-controlled. Shared with every team member who clones or pulls the repository. Use this for servers that the entire team needs — your Jira integration, your GitHub tools, your internal API connectors.

```json
{
  "mcpServers": {
    "github": {
      "command": "npx",
      "args": ["-y", "@modelcontextprotocol/server-github"],
      "env": {
        "GITHUB_TOKEN": "${GITHUB_TOKEN}"
      }
    },
    "jira": {
      "command": "npx",
      "args": ["-y", "@community/mcp-server-jira"],
      "env": {
        "JIRA_URL": "${JIRA_URL}",
        "JIRA_TOKEN": "${JIRA_TOKEN}"
      }
    }
  }
}
```

**User-level: `~/.claude.json`**
Lives in the user's home directory. Personal. NOT version-controlled. NOT shared with teammates. Use this for experimental servers, personal integrations, or servers you are testing before proposing them to the team.

**Key principle**: all tools from all configured servers (both project-level and user-level) are discovered at connection time and available simultaneously. There is no manual activation step — if a server is configured and reachable, its tools appear in the agent's toolkit. When auditing, check that a server intended to be team-wide actually lives in `.mcp.json` and not in a single developer's `~/.claude.json`, where teammates would silently lack its tools.

### Environment Variable Expansion

The `.mcp.json` file supports `${VARIABLE_NAME}` syntax for environment variable expansion. This is how you keep credentials out of version control whilst still sharing server configuration with your team.

```json
{
  "env": {
    "GITHUB_TOKEN": "${GITHUB_TOKEN}",
    "DATABASE_URL": "${DATABASE_URL}"
  }
}
```

Each developer sets their own tokens locally (in their shell profile, `.env` file, or secrets manager). The `.mcp.json` file references the variable names, not the values. This means:

- The configuration file is safe to commit to version control
- Each developer authenticates with their own credentials
- Token rotation does not require config file changes
- No secrets leak through repository history

When reviewing an implementation, confirm that `.mcp.json` contains only `${ENV_VAR}` references and never literal token values — a hard-coded secret here leaks through the full repository history.

### MCP Resources

MCP resources are a mechanism for exposing content catalogs to agents without requiring exploratory tool calls. Instead of the agent calling a tool to discover what data is available, resources present that information upfront.

Examples of what to expose as resources:

- **Issue summaries** — a list of current Jira issues with titles and statuses
- **Documentation hierarchies** — a table of contents for your internal docs
- **Database schemas** — table names, column types, and relationships

The benefit is reduced unnecessary queries. Without resources, an agent might call `list_tables`, then `describe_table` for each table, consuming multiple tool calls just to understand the data landscape. With a database schema resource, that information is available immediately.

Resources give agents visibility into available data. Tools let agents act on that data. The combination means fewer wasted calls and more targeted operations. When auditing an integration that burns tool calls on discovery, check whether a resource could present that catalog upfront instead.

### The Build-vs-Use Decision

This is one of the most practical decisions in MCP integration. When your team needs to integrate with an external system, the question is: build a custom MCP server or use an existing community server?

**Use community servers for standard integrations:**

- Jira, GitHub, Slack, linear, Notion — these all have maintained community MCP servers
- They cover standard use cases, are tested by the community, and receive updates
- Using them saves development time and maintenance burden

**Build custom servers only when:**

- Your team has specific workflows that community servers cannot handle
- You need custom business logic embedded in the tool layer
- You require integration with proprietary internal systems that have no community server

The pragmatic default is to evaluate community servers first — that is the right call whenever a standard integration is involved. Building a custom server is only justified when there are team-specific requirements that community servers genuinely cannot meet.

> **Common Mistake**
> Reaching for a custom server because it feels more "controlled" or bespoke is the wrong instinct for a standard integration. A hand-rolled Jira or GitHub server duplicates work the community already maintains and tests, and it becomes an ongoing maintenance burden. Reserve custom builds for genuinely team-specific workflows, embedded business logic, or proprietary systems with no community equivalent.

### Enhancing MCP Tool Descriptions

A subtle but important problem: when an MCP tool has a sparse description, the agent may prefer built-in tools (like Grep) even when the MCP tool is more capable. This happens because the model has better context about built-in tools — their descriptions are rich and detailed.

The fix: enhance your MCP tool descriptions to explain capabilities and outputs in detail. Instead of:

```
search_codebase: "Searches code"
```

Write:

```
search_codebase: "Performs semantic code search across the
entire repository using AST-aware indexing. Returns matching
functions, classes, and methods with full context including
file path, line numbers, and surrounding code. More accurate
than text-based grep for finding code by intent rather than
exact string match. Use this instead of Grep when searching
for code by what it does rather than what it contains."
```

The enhanced description gives the model enough context to prefer the MCP tool when it is genuinely more capable than the built-in alternative. When auditing, look for one-line MCP tool descriptions and check whether the agent is defaulting to built-in tools it should be delegating to the MCP server.

> **Key Concept**
> Project-level `.mcp.json` is version-controlled and shared with the team. User-level `~/.claude.json` is personal and not shared. Use `${ENV_VAR}` syntax to keep credentials out of version control.

## Audit Checklist

- [ ] Team-wide MCP servers are configured in version-controlled `.mcp.json` at the project root, not in a single developer's `~/.claude.json`
- [ ] Personal, experimental, or in-testing servers live in `~/.claude.json` and are not committed to the repository
- [ ] `.mcp.json` credentials use `${ENV_VAR}` expansion only — no literal token or secret values that would leak through repository history
- [ ] Each developer supplies their own tokens locally (shell profile, `.env`, or secrets manager), so token rotation needs no config change
- [ ] Content catalogs (issue summaries, documentation hierarchies, database schemas) are exposed as MCP resources rather than forcing exploratory discovery tool calls
- [ ] Community servers were evaluated before building custom; a custom server is justified only by team-specific workflows, embedded business logic, or a proprietary system with no community equivalent
- [ ] MCP tool descriptions detail capabilities, outputs, and when to use them versus built-in tools, so the agent does not default to built-in tools like Grep when the MCP tool is more capable

## Sources

- MCP Server Configuration — Claude Code Documentation — Anthropic
- Model Context Protocol — Resources — Model Context Protocol
