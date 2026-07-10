---
name: jira-tasks
description: "Read-only Jira access for the project's tracker — answer 'what am I assigned', list open tickets, check <PROJECT-KEY>-<n> status, pull the jira backlog, or link a workspace task to a ticket. Triggers: my tickets, open tickets, what am I assigned, jira backlog, link task to ticket, ticket status, recently updated tickets, tickets by label. NOT for creating, transitioning, commenting on, or logging work against tickets — those are write ops the user must explicitly request each time, never as a side effect. NOT for Confluence (pages, spaces, comments)."
version: "1.0.0"
owner: "swarmery-core"
---

# Purpose

Read Jira to answer "what am I working on", "what's the status of `<PROJECT-KEY>-115`", or to
reconcile workspace tasks against tickets. **Read-only by default** — every write (create /
transition / comment / worklog) is a separate, explicit, user-requested action (see
[Write-op policy](#write-op-policy)).

**Site and project key:** this skill uses `<jira-base-url>` (e.g. `yourteam.atlassian.net`)
and `<PROJECT-KEY>` as placeholders. Check the consumer project's `CLAUDE.md` (or
`.claude/project.json`) for the actual Jira base URL, project key, and any pinned cloudId.
If none is documented, ask the user once and suggest recording it in `CLAUDE.md`.

# Tool flow

The Atlassian MCP tools are **deferred** — their schemas are not loaded at start. Load them
before the first call, or the call fails with `InputValidationError` (the tool-name prefix
depends on how the Atlassian MCP server is registered on the host):

```
ToolSearch  query: "select:mcp__plugin_atlassian_atlassian__getAccessibleAtlassianResources,mcp__plugin_atlassian_atlassian__searchJiraIssuesUsingJql,mcp__plugin_atlassian_atlassian__getJiraIssue"
```

Every Jira call needs a **cloudId**. Resolve it by calling
`getAccessibleAtlassianResources` (no args) and taking the `id` of the `<jira-base-url>`
resource. If the project's `CLAUDE.md` pins a cloudId, use it directly — but **if any call
404s** (site migrated, token re-scoped), re-resolve rather than retrying a stale id.

- **Search / list** → `searchJiraIssuesUsingJql` (`cloudId`, `jql`, `fields`, `maxResults`).
  Request only the fields you'll print (`key,status,summary,updated,assignee`) — don't pull
  full descriptions for a list.
- **Single ticket** → `getJiraIssue` (`cloudId`, `issueIdOrKey: "<PROJECT-KEY>-115"`).

# JQL recipes

| Intent | JQL |
|--------|-----|
| My open work | `assignee = currentUser() AND project = <PROJECT-KEY> AND statusCategory != Done ORDER BY updated DESC` |
| Recently updated (last week) | `project = <PROJECT-KEY> AND updated >= -7d ORDER BY updated DESC` |
| By label | `project = <PROJECT-KEY> AND labels = <label> ORDER BY updated DESC` |
| In-progress across the team | `project = <PROJECT-KEY> AND statusCategory = "In Progress" ORDER BY updated DESC` |
| Single ticket (or use `getJiraIssue`) | `key = <PROJECT-KEY>-115` |

`currentUser()` resolves to the caller's Atlassian account — no need to hardcode an accountId.
`statusCategory != Done` is more robust than listing status names, which vary per board.

# Join-key convention (tickets ↔ workspace tasks)

Workspace task cards link themselves to tickets via a **`Tickets:`** line in the task-card
`README.md` (under `working/YYYY/MM/DD/<slug>/` in the agent workspace — resolved by
`agent-work.sh` from `AGENT_PROJECT`), e.g. `Tickets: <PROJECT-KEY>-115, <PROJECT-KEY>-93`.
This is the join key in **both** directions:

- **Reporting a ticket?** Grep for an existing task dir first:
  `rg -l "<PROJECT-KEY>-115" <workspace>/working` — if one exists, mention the task slug +
  its SUMMARY.md so the user sees prior work, not a cold ticket.
- **Reporting a task?** Read its `Tickets:` line and pull those keys' live status so the
  card's status and Jira agree.

When you start work that maps to a ticket, add the `Tickets: <PROJECT-KEY>-<n>` line to the
task README (that's a workspace-file edit, not a Jira write — always fine).

# Output shape

Render a **compact table**, most-recently-updated first — never dump raw issue JSON:

```
| Key      | Status       | Summary                                  | Updated    |
|----------|--------------|------------------------------------------|------------|
| ABC-115  | In Progress  | Image retention policy relax             | 2026-07-03 |
| ABC-93   | In Review    | Memory-leak audit                        | 2026-07-02 |
```

Truncate long summaries to one line. Link known task dirs inline. For a single ticket,
a short field list (status, assignee, labels, updated, description gist) beats the full blob.

# Write-op policy

Creating issues, transitioning status, adding comments, and logging worklogs are **write
operations**. Per the `rules/ASK.md` posture (default to NO; surface what/why/blast-radius
before acting), each one requires an **explicit user request in the current conversation** —
never as a side effect of a read, a report, or "while I was in there". A request to *list* or
*check* tickets never authorises a mutation.

- Don't auto-transition a ticket because a fix merged.
- Don't comment "done" on the user's behalf unprompted.
- The **one blessed pattern**: after shipping a fix, the user may ask you to drop an
  **MR-links comment** (branch/MR URLs + one-line result) on the ticket. Still surface it
  first — it's user-visible and user-requested, not silent.

Write tools (`createJiraIssue`, `transitionJiraIssue`, `addCommentToJiraIssue`,
`addWorklogToJiraIssue`) are loadable via ToolSearch the same way, but only reach for them
once the user has explicitly asked for that specific change.

# Related

- `rules/ASK.md` — human-confirmation posture this skill defers to for any write.
- `rules/ALWAYS.md` — the `Tickets:` task-card convention and workspace layout.
- Atlassian plugin skills (`atlassian:generate-status-report`,
  `atlassian:capture-tasks-from-meeting-notes`) — heavier report/write workflows; this skill
  is the lightweight read path.
