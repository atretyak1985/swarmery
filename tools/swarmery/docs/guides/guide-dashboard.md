# The dashboard

The dashboard at `http://localhost:7777` is organised as ten **places** in one
sidebar. A place is a destination, not a page: each one gathers everything about one
question under tabs, so you look for *what you want to know*, not for which screen
happened to grow the feature.

```stats
10 | places in the sidebar | hot
2 | scopes — all projects, or one
1 | inbox for everything waiting on you
⌘K | search and jump from anywhere
```

## Two scopes

Every place is read in one of two scopes:

| Scope | Address | What you see |
|---|---|---|
| **All projects** | `/`, `/sessions`, `/health`, … | The whole fleet on this machine |
| **One project** | `/p/<slug>/…` | The same places, narrowed to that project |

The **project switcher** at the top of the sidebar is the scope control: pick a
project to enter its `/p/<slug>/…` pages, or *All projects* to leave. Inside a
project the address *is* the filter, so there is nothing to keep in sync. Some fleet
pages (Sessions, for one) also carry a project chip (`● all projects ▾`) in their own
filter row, to narrow the list without leaving the fleet.

**Plans** and **Knowledge** only make sense inside one project. Under *All projects*
their rows are dimmed and open the project you visited last (or the project list,
if you have never opened one). **Docs** is the opposite: it documents swarmery
itself, so it always opens the same fleet-wide `/docs`.

## The places

The sidebar has three groups. The main group is the daily work:

| Place | Answers | Tabs |
|---|---|---|
| **Today** | What do I do now? | — |
| **Inbox** | What is waiting on my decision? | all · approvals · lessons · advisor · proposals · classifier · stop using a lesson? |
| **Sessions** | What are my agents doing, and what did it cost? | — |
| **Plans** | What is planned, queued and running in this project? | New plan · Plans · Board · Playbooks |

The **Improve** group is where the system gets better, not just busier:

| Place | Answers | Tabs |
|---|---|---|
| **Health** | How is the agent fleet doing, and what should change? | Overview · Agents · Friction · Estimates · Advisor · Cost & tokens |
| **Learning** | What has the system learned, and can it be trusted? | Lessons · The classifier · Forecast honesty · Proof |
| **Knowledge** | What does this project know about itself? | Memory · Architecture · Serena · Graphify |

The bottom group holds the machinery:

| Place | Answers | Tabs |
|---|---|---|
| **Docs** | How does swarmery work? | — (these guides and the reference docs) |
| **System** | What agents, skills, plugins, hooks and routines exist? | Agents · Skills · Plugins · Hooks · Routines · Insights |
| **Settings** | How is this machine set up? | Appearance · Accounts · Notifications · Projects |

A place's tab lives in the address (`?tab=…`, or `/system/<tab>`), so every view can
be linked and bookmarked. Only two rows carry a signal: **Inbox** shows how many
decisions wait on you, and **Sessions** shows a live dot while a session runs.

## Today

The home of both scopes. A clock line, then **The loop, this week** — five joined
stages, *Plan → Run → Measure → Learn → Change* — where any stage with something
waiting on you turns amber and links straight to it. Beside it, **Waiting on you**
(the top of the Inbox) and **Live now** (running sessions). Below, **Today in
detail** keeps the full day view: how long agents waited on you, which tools caused
it (with an inline *stop asking* that writes an auto-approve rule), and the day's
notable sessions.

## Inbox

One list for every decision that waits on you, whatever produced it: permission
**approvals**, **lessons** proposed from surprising runs, **advisor**
recommendations, agent-change **proposals**, **classifier** answers to check, and
active lessons proposed for retirement (**stop using a lesson?**). The list is on the left, the selected item on the
right, and the keyboard does the work:

| Key | Action |
|---|---|
| `j` / `k` | Next / previous item |
| `e` | The item's primary action — approve, accept |
| `x` | Deny or dismiss |
| `s` | Skip for now |

Approval rules and the decision history live one link away, on the
**manage** page (`/inbox` → *approvals/manage*). How approvals behave — the timeout,
the fail-open fallback, the rule glob — is covered in the [Sessions guide](guide-sessions.md).

## Sessions

Every transcript on the machine, grouped by day, live over a WebSocket. A plan run
collapses into one card inside its day and fans out on click. Search, status and
account chips narrow the list; a session opens into chat, timeline and diffs. The
[Sessions guide](guide-sessions.md) covers what the daemon reads and how cost is computed.

## Plans

Everything before and around a run: the Planning Mode interview (**New plan**),
the plans and their phases (**Plans**), single cards and the dispatcher (**Board**),
and the recipes cards run under (**Playbooks**). The [Plans guide](guide-plans.md) covers all four.

## Health

One date range for the whole place, shown in a status strip that answers three
questions: what this window looks like, what is waiting on you, and what changed
because of you. Then:

| Tab | What it shows |
|---|---|
| **Overview** | One sentence about the fleet, the agents that moved it, and the friction you can remove right now |
| **Agents** | Per-agent scorecards — runs, error rate, success rate, cost, p95 duration — each with **Improve** |
| **Friction** | Denied tools (with a one-click allow rule), the most repeated errors, approval wait times |
| **Estimates** | Estimated versus actual hours per workspace task, and the lessons your retrospectives recorded |
| **Advisor** | Evidenced recommendations from the deterministic rule engine, and the agent-change proposals awaiting a decision |
| **Cost & tokens** | Cost, tokens, runs and cache over time, broken down by project, model, account, agent and skill |

The [Retro reference](retro.md) explains every metric and advisor rule.

### The auto mode permission check

In auto mode, Claude Code clears every tool call with a permission check that runs
on Claude's servers. When that check fails to answer, the call is refused with
*"The server-side auto mode classifier gave no verdict"*, and after ten such
answers in a row the turn stops by itself. The cause is outside swarmery and nothing
here retries or resumes a session — the dashboard only makes the outage visible
while it is happening:

- **Overview** carries one line, *Auto mode permission check*: how many checks went
  without a verdict in the last hour, in how many sessions, and when the last one
  was. It reads *answering* when there were none.
- Three or more within ten minutes raise **one alert in the Inbox** (*alerts* tab)
  for the whole burst. It has no button: it closes on its own after thirty minutes
  without a failed check. `SWARMERY_AUTOMODE_ALERT_MIN` changes the threshold
  (default `3`; read when the daemon evaluates, once a minute).

`GET /api/health` reports the same facts in `autoModeClassifier`:

| Field | Meaning |
|---|---|
| `noVerdictLastHour` | Tool calls refused for want of a verdict in the last hour |
| `sessionsLastHour` | Distinct sessions those calls belong to |
| `lastAt` | Timestamp of the newest such call; `null` when there is none |
| `alerting` | `true` while the `auto_mode_no_verdict` alert is open |

The numbers are recomputed at most every 30 seconds, and a read that fails reports
zeroes rather than failing the health endpoint.

## Learning

| Tab | What it shows |
|---|---|
| **Lessons** | Lessons drawn from runs that landed far from their forecast. Nothing becomes active without your accept; each active lesson carries a measured effect, and ineffective, stale or unused ones are proposed for retirement |
| **The classifier** | The optional local-model classifier: each question, its mode (off · watching · acting) and how often it agrees with what happened. The [Decisions guide](guide-decisions.md) covers setting it up |
| **Forecast honesty** | How well forecasts match outcomes, per group once a group has enough runs, and the routing report |
| **Proof** | The decisions you made and their measured effect |

Learning is fleet-wide: inside a project it shows the same data, and only its links
into the Inbox follow the project.

## Knowledge

What one project knows about itself: its Claude Code auto-**Memory**, its
**Architecture** map, and the **Serena** and **Graphify** tool dashboards when those
packs are enabled.

## Docs, System and Settings

**Docs** is this page — guides first, then the reference docs.

**System** is the registry of the agent system itself: **Agents**, **Skills** (the
skills, commands and templates catalog), **Plugins** (a project's pack toggles),
**Hooks**, **Routines** (scheduled and webhook automation) and **Insights**.

**Settings** is machine-wide: **Appearance** (theme, daemon health, worktrees,
connectors), **Accounts** (your Claude accounts), **Notifications** (background
toasts) and **Projects** (the project list). A project's own settings — pack toggles,
archive, detach — are at `/p/<slug>/settings`.

## Search from anywhere

**⌘K** (Ctrl+K elsewhere) opens a palette that searches sessions, message text,
files and projects as you type, plus a navigation section. Picking a file lists the
sessions that touched it — that is the reverse lookup from a file to the work that
changed it. ↑/↓ and Enter to go, Escape to close.

## Old addresses

Pages that became tabs keep their old addresses as redirects, so bookmarks and links
in older notes still land in the right place:

| Old address | Now |
|---|---|
| `/approvals` | Inbox → approvals |
| `/analytics` | Health → Cost & tokens |
| `/retro` | Health → Agents |
| `/lessons`, `/decisions` | Learning → Lessons, The classifier |
| `/routines` | System → Routines |
| `/projects` | Settings → Projects |
| `/p/<slug>/board`, `/planning`, `/playbooks` | Plans → Board, New plan, Playbooks |
| `/p/<slug>/memory`, `/architecture`, `/serena`, `/graphify` | Knowledge → the matching tab |
| `/system/toolkit` | System → Skills |
