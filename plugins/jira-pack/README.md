# jira-pack

Issue-tracker pack: `/jira-fix` takes a ticket link of **any type**, verifies tracker access,
classifies the ticket, produces real evidence for it, routes the work to core executors,
comments back with that evidence, and moves the ticket to the QA column. Opt-in — most projects
don't need it, and it requires setup before first use.

## Two ticket classes, two kinds of evidence

A tracker holds two kinds of work, and only one of them reproduces. `jira-triage` decides which
one it is **before** running anything, and the class picks the evidence:

| | `class: defect` | `class: change` |
|---|---|---|
| The ticket describes | behavior that exists and is wrong | behavior that does not exist yet |
| Evidence | the reproduction runs **red** | green baseline + absence proof + testable acceptance criteria; the red evidence is a **failing test written before any implementation** (`jira-delivery` Step 2a) |
| Branch / commit | `fix/<KEY>-<slug>` / `fix(...)` | `feat/<KEY>-<slug>` / `feat(...)` |
| Comment | `comment-fix-summary.md` | `comment-change-summary.md` |
| `cannot-reproduce` | possible | **impossible, by rule** |

That last row is the load-bearing one. A feature ticket's suite is green because the feature was
never built; reading that green as "the reported behavior did not occur" would comment on and
transition **unimplemented work** into the QA column, and nothing in this pack can undo a tracker
write. So on a change ticket the admissible verdicts are `needs-fix`, `already-fixed`,
`needs-info`, and `too-large` — never `cannot-reproduce`.

The verdict set itself is unchanged (five verdicts); the class is orthogonal to it and travels
with it through delivery, the comment template, and the comment's hidden marker.

## Enable per project

```jsonc
"enabledPlugins": { "jira-pack@swarmery": true }
```

## Required config

`/jira-fix` runs with `autonomy: auto` — there's no one to ask mid-run, so everything it can't
read off the ticket itself must be declared in the project's `.claude/project.json` under a
`jira` block. Missing keys are a loud stop (see `skills/jira-config/SKILL.md`), never a guess.

### From the dashboard

The pack declares this block in its own `requirements.json`, so the dashboard can **ask** you
for it instead of leaving you to discover it from a failed run:

1. **Enable the pack** for the project. Its row on the project's plugins panel then shows a
   `needs-config` chip — the declared key is missing or incomplete in `.claude/project.json`.
   A real install problem (`missing`, `behind`, `orphaned`) always wins over that chip: fix the
   install first, configure second.
2. **Press `configure`.** The modal renders the form straight from
   `plugins/jira-pack/requirements.json` — the same fragment `overlays/_schema/project.schema.json`
   validates against, so the form cannot ask for a shape the schema would reject.
3. **Press `probe`** to fill the fields the pack nominated (`qaStatus`, `repro.test`). It hands
   the pack's prompt to a `claude` session that already holds your tracker connectors, and comes
   back with the status names that actually exist on your board and the test commands that
   actually exist in your repo — the obvious guess is usually wrong. Suggestions stay typeable,
   never a fixed dropdown, because what a probe can reach is not guaranteed to be the whole
   truth. A probe writes nothing; when it fails you lose the suggestions and nothing else.
4. **Press `save`.** Exactly the `jira` key is written into `.claude/project.json`, merged into
   what is already there — every other key and the file's formatting survive — and the previous
   contents are kept beside it as `project.json.bak`.

### By hand

The same block, the same schema — edit `.claude/project.json` directly if you prefer:

```json
"jira": {
  "baseUrl": "<jira-base-url>",
  "projectKey": "<PROJECT-KEY>",
  "qaStatus": "<exact-status-name>",
  "repro": { "setup": "npm ci", "test": "npm test" },
  "budget": { "maxFiles": 5, "maxAttempts": 3 }
}
```

- `baseUrl`, `projectKey`, `qaStatus`, `repro.test` are required. `repro.setup` is optional
  (skipped, not an error, when absent).
- `qaStatus` must match the board's status name **exactly**, character for character — that is
  the field `probe` exists to get right.
- `budget` defaults to `maxFiles: 5`, `maxAttempts: 3` — the same stop conditions
  `plugins/core/agents/debugger.md` already enforces, so the two never disagree.
- Schema: `overlays/_schema/project.schema.json` (`jira` property), mirrored in this pack's
  `requirements.json`; `scripts/check-plugin-requirements.sh` fails CI if they drift. Example:
  `overlays/example/project.json`.

> **A valid config block is not a safety review.** The modal only makes this agent *runnable*;
> nothing about it makes the agent *safer*, and it adds no human gate to anything below. The
> first invocation on any new project is still always `--dry-run` — see
> [Before you trust it on a real project](#before-you-trust-it-on-a-real-project).

## MCP provider requirement

This pack ships **no `.mcp.json`** — it deliberately doesn't bundle an Atlassian MCP server.
An Atlassian MCP provider (Jira access) must already be configured and authorized on the
machine before `/jira-fix` can do anything beyond config validation. See Phase 4 of the
implementation plan for the provider-resolution details.

## Before you trust it on a real project

**Onboarding ritual: the first invocation on any new project is always `--dry-run`.**

```
/jira-fix <ticket-url-or-key> --dry-run
```

It runs every read this agent would run for real — config, tracker access, the ticket itself,
the reproduction command, the transition list — and shows you the proposed fix and comment
without writing anything to Jira, the swarmery board, or git. Review that output before you ever
run the same ticket without `--dry-run`.

**This agent is autonomous (`autonomy: auto`).** Once `.claude/project.json`'s `jira` block is
valid and you drop `--dry-run`, it takes the following actions **without any per-action
confirmation prompt** — there is no human gate anywhere in its flow:

- posting a comment to a real Jira ticket (`addCommentToJiraIssue`);
- transitioning a real Jira ticket's status (`transitionJiraIssue`);
- creating or moving a swarmery board card — dashboard Plans → Board (`swarmery-board-card`'s `POST`/`PATCH`);
- on the `needs-fix` path only: creating an isolated git branch/worktree, writing code and
  tests into it, `git push`, and opening a PR (`jira-delivery`) — gated on a green
  `@verification-agent` verdict, but never on human approval.

None of this is reversible by the agent itself — a wrongly-posted comment or transition is
corrected by a human via Jira directly, and an escalated fix attempt's branch/worktree is the
only thing this pack ever removes on its own (`jira-escalation`'s own cleanup, not a general
undo). Don't point it at a project's real tracker until you've reviewed a dry run.

See `skills/jira-access-preflight/references/setup.md` for the full walkthrough of enabling
and verifying Jira access (both supported channels) before that first `--dry-run`.
