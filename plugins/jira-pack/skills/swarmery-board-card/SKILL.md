---
name: swarmery-board-card
description: "Create and drive a /board card as a mirror of a jira-task-runner run: born in triage, moved straight to in_progress, finished at in_review or done. The card MUST NEVER pass through the todo column -- see 'The load-bearing rule' below for why. Covers projectId resolution, the exact POST/PATCH bodies, idempotent reuse via the jira-ticket label, the daemon-unavailable fallback, and dry-run request-body printing. NOT for talking to Jira itself (that's jira-tasks / the Atlassian MCP tools) and NOT for working-repo/config resolution (that's jira-config)."
version: "0.1.0"
owner: "swarmery-core"
docs:
  status: reviewed
  source_sha: e80f8ef2f225
  updated: 2026-08-06
---

# Purpose

`jira-task-runner` mirrors its own progress on the swarmery board (dashboard Plans → Board) so a
human watching the board sees the same lifecycle the run is going through, without that card
ever being able to trigger a second, competing run of the same job. This skill is the only place that talks to
`/api/board/tasks`; every other jira-pack skill treats the card as a side effect of this one.

# The load-bearing rule: this skill never sets `boardColumn: "todo"`

`tools/swarmery/internal/dispatch/service.go:343` selects dispatch candidates with exactly:

```sql
WHERE t.source='queue' AND t.board_column='todo'
```

`POST /api/board/tasks` always writes `source='queue'` (`tools/swarmery/internal/api/tasks_board.go:603`,
the `INSERT INTO tasks` call inside `createBoardTask`), and the handler carries its own comment
right after the insert: "A task created directly into todo is immediately dispatchable — trigger
the event fast path" (`tasks_board.go:617`). A card that lands in `todo` is therefore picked up by
the dispatcher on its very next poke, which spawns a **second** `jira-task-runner` agent in its
own worktree doing the exact same job the current run is already doing. This is a correctness bug
— two agents racing to comment on and transition the same ticket — not a style preference.

Consequence: **no step below may ever request `boardColumn: "todo"`.** The card is minted directly
in `triage` and moved to `in_progress` on the very next call, skipping `todo` entirely. The
lifecycle is exactly:

```
triage --(PATCH boardColumn=in_progress, immediately after creation)--> in_progress --(final PATCH)--> in_review | done
```

# 1. Resolve `projectId`

`GET http://127.0.0.1:7777/api/projects` returns the project registry as a list of `projectDTO`
(`tools/swarmery/internal/api/handlers.go:48-74`: `id`, `path`, `slug`, …). Match the run's
resolved working repo (the root `jira-config`'s working-repo resolution already validated — see
Related) against each row's `path` first (exact match), falling back to `slug`. No match at all →
this skill cannot create a card; follow "5. Fallback: daemon unavailable" below with reason
`"no project registered for <path>"` — a card is observability, not a correctness gate, so a
missing project registration must not block the run.

# 2. Idempotency check (always run BEFORE creating)

`GET http://127.0.0.1:7777/api/board/tasks?label=jira-ticket` (the `?label=` filter from Phase 1;
`listBoardTasks`, `tasks_board.go:401-459`) returns every queue card carrying the `jira-ticket`
label, across every project. Filter to the current `projectId` and scan for a card whose `title`
starts with `"<KEY>: "`.

- **Found, `boardColumn` is `triage`, `in_progress`, or `in_review`** (an open run still in
  flight) → reuse it: skip the POST in §3 entirely, keep its `id`/`externalId`, and go straight
  to whichever PATCH transition matches the run's current stage (§4). Record "reused card
  `<externalId>`" in the run's final report. This is what stops a second `/jira-fix` on the same
  ticket from minting a duplicate card.
- **Found, `boardColumn` is `done` or `archived`** (a closed previous run) → treat it the same as
  "not found": mint a fresh card via §3, never revive it. `done` is a normal terminal state, not
  an edge case — §4's own table sends both the cannot-reproduce and already-fixed endings there —
  so reusing a `done` card would go straight to the `in_progress` PATCH in §4, and
  `legalTransition` (`tasks_board.go:169-177`) rejects exactly that edge, `done → in_progress`,
  with a 400. Do not narrow this back to excluding only `archived`.
- **Not found** → create a new card (§3).

# 3. Create the card

`POST http://127.0.0.1:7777/api/board/tasks` (route: `tools/swarmery/internal/api/routes.go:122`,
wrapped in `requireLocalOrigin` — a same-host call that sends no `Origin` header, e.g. `curl` or an
agent's own HTTP client, passes `isLocalOrigin` unchallenged; `approvals.go:80-105`).

Request body:

```json
{
  "projectId": <id from step 1>,
  "title": "<KEY>: <summary from getJiraIssue>",
  "prompt": "<ticket link>\n\nTriage verdict: <...>\nWorking repo: <resolved repo root>\nJira provider: <pinned MCP tool prefix>",
  "priority": "normal",
  "agent": "jira-pack:jira-task-runner",
  "labels": ["jira-ticket"],
  "boardColumn": "triage"
}
```

Field notes, verified against `createBoardTask` (`tasks_board.go:499-622`):

- `title` MUST be `"<KEY>: "` followed by the ticket's real `summary` field as returned by
  `getJiraIssue` — never a paraphrase or a shortened version. The idempotency check in §2 depends
  on matching this exact prefix on a later run.
- `prompt` carries the ticket link, the triage verdict, the resolved working repo (from
  `jira-config`), and the pinned Jira MCP tool prefix (from `jira-access-preflight`) — enough
  context that anyone reading the card understands what the run decided without opening Jira.
- `labels: ["jira-ticket"]` — normalized server-side by `normalizeLabels` (lowercase, trim,
  dedupe, charset-checked; `tasks_board.go:112-140`); already lowercase and slug-shaped here, so it
  round-trips unchanged.
- `origin` is deliberately **absent** from this body. The endpoint accepts only the literal string
  `"manual"` for this field and 400s on anything else (`tasks_board.go:535-539`, error message
  "origin is capture-owned; only 'manual' may be created over HTTP"); omitting it is simpler and
  reaches the same server-side default as a hand-created card.
- `boardColumn: "triage"` is also `createBoardTask`'s own default when the field is omitted
  (`tasks_board.go:524-527`), but this skill sets it explicitly so the intent reads unambiguously
  in the request body itself.
- `agent: "jira-pack:jira-task-runner"` must resolve in the agent registry for this project
  (`resolveAgentName`, `tasks_board.go:332-366`). The registry scanner stores plugin agents under
  their pack-qualified name (`<pack>:<agent>`), so the bare `jira-task-runner` does NOT resolve —
  always send the full name. If this card-creation step runs against a project where the agent
  isn't registered yet, the POST 400s with `"unknown agent: ..."`; treat that the same as "daemon
  unavailable" (§5) rather than retrying with a different agent name.

The response is `201` with the full `boardTaskDTO` (`tasks_board.go:218-257`). Keep both `id` and
`externalId` (shaped `T-xxxxxx`) for every subsequent PATCH and for the run's final report.

# 4. Transitions (PATCH)

`PATCH http://127.0.0.1:7777/api/board/tasks/{id}` (route: `routes.go:123`), body
`{"boardColumn": "..."}` plus whatever else that moment also updates:

| Moment | `boardColumn` | Also updates |
|---|---|---|
| immediately after creation, before any real work starts | `in_progress` | — |
| final: fix made, PR opened | `in_review` | `prompt` gains the PR link and the ticket link |
| final: cannot-reproduce / already-fixed | `done` | `prompt` gains the verdict and a link to the posted Jira comment |
| final: needs-info or too-large | `in_review` | `prompt` gains the reason the run stopped |

Constraints verified in `legalTransition` (`tasks_board.go:169-177`): any column → `archived` is
always allowed; `done → in_progress` is rejected ("recovery is dispatcher-owned, not user-facing")
— the chain above never needs it, since every path through this table ends at `done` or
`in_review` and never transitions a card again afterward. `patchBoardTask`
(`tasks_board.go:628-832`) also 400s any attempt to patch `origin`, `captureKey`, or
`originSessionId` (`tasks_board.go:660-664`, "capture-owned and cannot be patched") — this skill
never sends those fields in a PATCH body.

# 5. Fallback: daemon unavailable

The board is an observability aid, not a correctness gate. If `127.0.0.1:7777` doesn't respond,
`GET /api/projects` can't resolve a project for the working repo, or a POST/PATCH call errors for
a reason outside this skill's own logic (network partition, unregistered project, unregistered
agent) — **not** a `legalTransition` rejection, which §2's `done`/`archived` exclusion exists to
make unreachable; a 400 there means this skill's idempotency check has a bug and should surface
as a failure, not fold silently into this fallback:

- the run **continues** — the ticket matters more than the card;
- the final report gets exactly one extra line: `BOARD: unavailable (<reason>)`;
- that line never reaches the Jira comment — the team reading the ticket doesn't need
  board-plumbing detail, and a broken local daemon says nothing about whether the ticket got fixed.

# 6. Dry-run mode

In `--dry-run`, this skill issues **no** HTTP call that has a side effect. `GET` calls (the
`projectId` resolution in §1, the idempotency check in §2) are still allowed — they're read-only
and let dry-run report a realistic outcome ("would reuse card T-xxxxxx" vs. "would create a new
card"). In place of every `POST` or `PATCH`, print the exact request body that would have been
sent, on a line starting with the literal string `DRY-RUN board POST` or `DRY-RUN board PATCH`:

```
DRY-RUN board POST /api/board/tasks
{ "projectId": 3, "title": "<KEY>: …", "labels": ["jira-ticket"], "boardColumn": "triage", … }
DRY-RUN board PATCH /api/board/tasks/{id} { "boardColumn": "in_progress" }
```

These two prefixes are a contract, not a suggestion: Phase 8's bash test greps for them verbatim.
Do not reword, reorder, translate, or drop them — `DRY-RUN board POST` / `DRY-RUN board PATCH`
must be the literal start of the line, followed by the endpoint and then the full JSON body that
would have been sent (single-line or pretty-printed doesn't matter — only the prefix string is
contractual).

# Placeholders / neutrality

Any example in this skill uses only `<jira-base-url>`-style placeholders, `<KEY>`, and
`<PROJECT-KEY>` — no real ticket key, project slug, hostname, or team name
(`docs/NEUTRALITY.md`; `scripts/scan-flavor.sh` must stay `✓ clean` for `plugins/**`).

# Related

- `plugins/jira-pack/skills/jira-config/SKILL.md` — resolves the working repo and the
  `.claude/project.json` `jira` block; run before this skill so `prompt`'s "Working repo" line has
  a value.
- `plugins/jira-pack/skills/jira-access-preflight/SKILL.md` — resolves and pins the Jira MCP tool
  prefix this skill's `prompt` cites as "Jira provider".
- `tools/swarmery/internal/api/tasks_board.go` — `createBoardTask`, `patchBoardTask`,
  `boardTaskDTO`, `legalTransition`, `normalizeLabels`.
- `tools/swarmery/internal/dispatch/service.go:343` — the dispatch-candidate query that is the
  reason `todo` is forbidden (see "The load-bearing rule" above).

# How to use

## What it does

This skill is the only place in jira-pack that talks to the swarmery board API. It mirrors a ticket run as a card on the swarmery board (Plans → Board) so a person watching the board sees the same lifecycle the run is going through. Crucially, it mints the card straight into `triage` and never into `todo` — a `todo` card is dispatchable, and the dispatcher would spawn a second agent racing the current run on the same ticket.

## When to use it

- A ticket run has passed triage and you want its progress visible on the board.
- You need to move an existing run card forward: `in_progress` at the start of real work, then `in_review` or `done` at the end.
- A second run starts on a ticket that may already have a card, and you need the idempotent reuse check before creating anything.
- You are running with `--dry-run` and need to show the exact request bodies without touching the board.

## When not to use it

- Reading or writing the ticket itself — use `jira-tasks` or the Atlassian MCP tools.
- Resolving the working repo or the `jira` config block — use `jira-config`.
- Posting the verdict comment or transitioning the ticket — use `jira-writeback`.

## How to invoke

```
Skill(skill: "jira-pack:swarmery-board-card")
```

Invoke it from inside a ticket run, after the working repo and the Jira tool prefix are already resolved, at each point the card should be created or moved.

## Inputs

- Ticket key and summary — used verbatim as the card title `"<KEY>: <summary>"` — required.
- Resolved working repo root — matched against the project registry to find `projectId`, and cited in the card prompt — required.
- Triage verdict and the pinned Jira tool prefix — written into the card prompt — required at creation.
- Dry-run flag — suppresses every write call — optional.

## What you get back

On creation you get a `201` with the card's `id` and `externalId` (shaped `T-xxxxxx`); keep both for later transitions and the run's final report. Each transition is a `PATCH` that also updates the card prompt with the PR link, verdict, or stop reason. If the local daemon is unreachable or no project matches, the run continues and the final report gains one line: `BOARD: unavailable (<reason>)` — that line never reaches the ticket comment.

## Worked example

```
Skill(skill: "jira-pack:swarmery-board-card")

# Run is at "needs-fix", repo root resolved, no existing card for this key.
GET  /api/projects                      -> projectId 3 matches the repo path
GET  /api/board/tasks?label=jira-ticket -> no open card titled "<KEY>: ..."
POST /api/board/tasks                   -> 201, T-xxxxxx, boardColumn "triage"
PATCH /api/board/tasks/{id}             -> boardColumn "in_progress"
# ...fix lands, PR opened...
PATCH /api/board/tasks/{id}             -> boardColumn "in_review", prompt gains the PR link
```

You end up with one card that tracked the whole run, never sat in `todo`, and is reused rather than duplicated if the same ticket is run again.

## Related

- `jira-config` — run first, so the card prompt's "Working repo" line has a real value.
- `jira-access-preflight` — pins the Jira tool prefix the card prompt cites as "Jira provider".
- `jira-writeback` — for the ticket-side comment and status transition, which this skill never performs.
