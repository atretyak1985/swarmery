# Aggregated Audit Checklists — Tool Design & MCP Integration

Run these against the implementation under review. For the reasoning behind any item, read the full module file.

## 2.1 Tool Interface Design
Full module: `references/2-1-tool-interface-design.md`

- [ ] Every tool description covers all five elements: what it does, expected inputs (types, formats, constraints, required vs optional), example queries, edge cases and limitations, and explicit boundaries versus similar tools
- [ ] No two tools carry overlapping or near-identical descriptions that could cause selection confusion
- [ ] Each description states when NOT to use the tool and names the sibling tool to use instead
- [ ] Generic, broad-responsibility tools are split into purpose-specific tools with defined input/output contracts
- [ ] Confusingly similar tool names are renamed to eliminate functional overlap at the interface level
- [ ] Misrouting is addressed first by improving descriptions, not by adding few-shot examples, a routing classifier, or tool consolidation
- [ ] System prompts are reviewed for keyword-sensitive instructions that could override tool descriptions and create unintended tool associations
- [ ] Fixes for tool-selection issues favour low-effort, high-leverage changes before infrastructure (better descriptions before routing classifiers)

## 2.2 Structured Error Responses
Full module: `references/2-2-structured-error-responses.md`

- [ ] Tool failures set the MCP `isError` flag so the agent distinguishes failures from successful results, rather than returning error text as a normal result.
- [ ] Every failure response tags an `errorCategory` — transient, validation, business, or permission — instead of a generic "Operation failed" message.
- [ ] Responses carry an `isRetryable` flag plus a human-readable `description` stating what went wrong and the recovery path.
- [ ] Business and permission errors are marked `isRetryable: false`, and the agent escalates or switches workflow rather than retrying them.
- [ ] Validation errors are marked `isRetryable: true`, signalling the agent to correct the input (e.g. reformat `order-abc` to `#12345`) and retry.
- [ ] A successful query returning no matches responds with `isError: false` (e.g. `resultCount: 0`), visibly distinct from an access failure.
- [ ] Access failures (timeout, auth, service down) return `isError: true` with a retryable category and never look like a valid empty result.
- [ ] Subagents perform local recovery for transient failures before propagating errors up to the coordinator.
- [ ] Propagated errors include partial results and a record of what was attempted, not a bare failure.
- [ ] The system neither silently suppresses errors (empty-as-success) nor terminates an entire workflow on a single recoverable failure.

## 2.3 Tool Distribution & Tool Choice
Full module: `references/2-3-tool-distribution-and-tool-choice.md`

- [ ] Each agent has roughly 4-5 tools, scoped to a single role — no agent carries a toolkit that spans multiple specialisations.
- [ ] No agent holds tools outside its role (e.g. a synthesis agent has no `web_search`, a search agent has no document-analysis tools).
- [ ] Each step's `tool_choice` matches intent: `"auto"` where conversational replies are valid, `"any"` where a structured tool call is mandatory but the tool is the model's choice.
- [ ] Mandatory first steps use forced selection (`{ "type": "tool", "name": ... }`) rather than relying on prompt instructions to enforce ordering.
- [ ] High-frequency, simple cross-role lookups use a scoped cross-role tool on the requesting agent instead of a coordinator round trip; only complex cases route through the coordinator.
- [ ] Generic, open-ended tools (`fetch_url`, `run_query`) are replaced with constrained equivalents (`load_document`) that enforce least privilege.
- [ ] The coordinator controls workflow (spawn, review, request revision) without holding domain-specific tools.

## 2.4 MCP Server Integration
Full module: `references/2-4-mcp-server-integration.md`

- [ ] Team-wide MCP servers are configured in version-controlled `.mcp.json` at the project root, not in a single developer's `~/.claude.json`
- [ ] Personal, experimental, or in-testing servers live in `~/.claude.json` and are not committed to the repository
- [ ] `.mcp.json` credentials use `${ENV_VAR}` expansion only — no literal token or secret values that would leak through repository history
- [ ] Each developer supplies their own tokens locally (shell profile, `.env`, or secrets manager), so token rotation needs no config change
- [ ] Content catalogs (issue summaries, documentation hierarchies, database schemas) are exposed as MCP resources rather than forcing exploratory discovery tool calls
- [ ] Community servers were evaluated before building custom; a custom server is justified only by team-specific workflows, embedded business logic, or a proprietary system with no community equivalent
- [ ] MCP tool descriptions detail capabilities, outputs, and when to use them versus built-in tools, so the agent does not default to built-in tools like Grep when the MCP tool is more capable

## 2.5 Built-in Tools
Full module: `references/2-5-built-in-tools.md`

- [ ] Content searches (function callers, error messages, imports) use Grep, not Glob
- [ ] File-by-path searches (by name, extension, or directory) use Glob, not Grep
- [ ] Edit is the default for file modifications; Read + Write is treated as a last resort
- [ ] On a non-unique Edit match, `old_string` is widened with more context (or `replace_all: true` is used) rather than falling back to Read + Write
- [ ] Codebase exploration is incremental — Grep to find entry points, Read to trace flows, Grep to trace usage — with no reading of all files upfront
- [ ] Function-usage traces follow re-exports and barrel files, not just the original function name
- [ ] Deprecation-style searches run Grep, then Glob for sibling test files, then Grep again for wrapper names — never Glob first
