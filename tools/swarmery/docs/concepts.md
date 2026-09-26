# Concepts

What the dashboard's vocabulary means, and what to do about each thing. Short definitions of the same terms appear in the UI itself behind the `?` and `!` buttons; this page is the long form.

`?` marks reference material. `!` marks a state that is asking you to do something.

## Handoff

When a live session's context footprint crosses **150k tokens**, the daemon spends one cheap model run writing a continuation brief — goal, current state, files touched, key decisions, next step — to `~/.swarmery/handoffs/<session_uuid>.md`. The session's card then shows a violet `Handoff` chip.

This is a parachute, not an alarm. Nothing is broken; the daemon has simply made it cheap for you to start over on a clean context whenever you want to.

**Why it exists.** At a near-full window every further turn re-reads almost the whole window. A long session does not just get slower — it gets monotonically more expensive per unit of work, and the model's attention is spread across a lot of history that stopped being relevant hours ago.

**Where the brief comes from.** The generator reads the daemon's **own SQLite database**, never the raw transcript. Feeding a 400k-token transcript to a model to explain why a 400k-token session is expensive would make the cure cost as much as the disease. It takes a bounded digest instead: the most recent assistant and user turns, truncated, plus the touched-file list.

**Using it.** Open the session, expand the rail's `handoff` section, read the brief, press *copy resume command*, then stop the session and paste the command into a fresh terminal. Commit or stash first: the brief describes the state of your work, it does not save it.

**Mechanics.**

| Setting | Value |
|---|---|
| Trigger | newest assistant turn's `tokens_in + cache_read + cache_write` ≥ 150k |
| Liveness | only sessions whose newest turn ended within the last 2 hours are candidates |
| Regeneration | not until the context grows another 75k past the last brief's footprint |
| Per-tick cap | 3 briefs per 30-minute tick; overflow is counted and logged, never silently dropped |
| Model | `SWARMERY_HANDOFF_MODEL`, default `claude-sonnet-5` |
| Off switch | `SWARMERY_HANDOFF=off` |

The regeneration gate matters: without it, a still-open fat session would pay for a new brief every tick forever.

## Context footprint

The `Nk ctx` chip on a session row is how full the model's window was on the newest assistant turn — input tokens plus cache reads plus cache writes. It is the single best predictor of what a session is costing you.

The chip is deliberately absent below 150k: a healthy context is not news. It appears **amber at 150k** and turns **red at 300k**, the same danger line the advisor's R9 fat-session rule uses.

At amber, `/compact` or splitting the work is usually enough. At red, every further turn re-reads a near-full window, and a restart is the cheap option rather than the drastic one — expect a [Handoff](#handoff) brief to already be waiting, since the two share the 150k trigger. Structurally, the fix is to stop planning one marathon session and start planning several short ones.

## Orphaned / dead

**orphaned** — the transcript is still being written but the daemon can no longer see a process that matches it. **dead** — the process is gone.

Either way, nothing is advancing this session, and it will sit in the list looking almost alive until you close it out.

**What to do.** If the terminal that started it is still open, reattach there. Otherwise close it out from the row's action button — but note that which button you get is decided by how recently the session was active, not by the badge. A freshly orphaned row still reads as live and offers **Stop**; only once it has gone quiet long enough to read as stuck does it offer **Kill**. (The kill endpoint accepts an orphaned session throughout; the button is simply not the one on offer.) A dead row offers neither: it shows a dimmed `exited` label, because there is no longer a process to signal. If the work matters, resume from the session's [Handoff](#handoff) brief rather than trying to revive the process.

## Stop vs Kill

Two different promises. The difference is **which row status you end up with**, not how hard the signal hits.

**Stop** is the graceful sibling. It records the session as **completed** and it succeeds *even with no known PID*, which is what lets it close out a zombie row that Kill cannot touch. When the process is alive and provably the same `claude` process, Stop sends `SIGTERM` and escalates to `SIGKILL` after the grace period; when the identity guard cannot confirm the process, it silently downgrades to marking the row. `completed` is **not terminal** in ingest — a stopped session that later produces transcript records legitimately resurrects to active.

**Kill** requires a known PID and a `running` or `orphaned` process state, and it refuses outright if the PID no longer belongs to a `claude` process or its start time does not match (PID reuse). A plain Kill also sends `SIGTERM` with the same escalation — the "immediate" path is the explicit **Force kill**, which the button offers after 10 seconds and which sends `SIGKILL` straight away with no escalation timer. Kill records the row as **killed**, and unlike `completed`, `killed` is terminal: neither procwatch nor ingest ever reverts it.

> [!TIP]
> Prefer Stop. The grace period is what lets an agent finish the file it is halfway through writing, and `completed` leaves the row honest if the session turns out to still have life in it.

| | Stop | Kill |
|---|---|---|
| Recorded status | `completed` (revertible) | `killed` (terminal) |
| Needs a PID | no | yes |
| Allowed proc states | any unfinished session | `running`, `orphaned` |
| Default signal | `SIGTERM` → `SIGKILL` after the grace | `SIGTERM` → `SIGKILL` after the grace |
| Immediate `SIGKILL` | — | via *Force kill* |
| Grace period | `SWARMERY_KILL_ESCALATION`, default 5s | same |

On a session row the dashboard picks one: a stuck row whose process is confirmed alive keeps the hard Kill; any other live row gets Stop.

## Attach / Detach

**Attach** wires a project into swarmery: merged entries in the project's `.claude/settings.json` (`enabledPlugins`, the swarmery marketplace, the two `AGENT_*` env vars, the workspace `additionalDirectories` entry), `project.json`, the opt-in statusline, and the hooks the control plane needs. It **merges** — it adds only what is missing and never overwrites a foreign value.

**Detach** is the inverse. It prunes exactly the keys and values onboarding wrote and leaves every other key intact, never deletes the file, and is idempotent: a second run finds nothing to remove. Its `Full` variant also removes `.claude/project.json` and the statusline scripts. Project-local components (`agents/`, `skills/`, `commands/`) are never touched — those are yours.

Both write a `.bak` before any real write (`settings.json.bak`, and `project.json.bak` under a full detach), and Attach restores `project.json` from that backup when the file itself is gone — an existing file always wins over the backup.

Both also run a **dry run first** and show you every file that would change before anything is written. Lines beginning with `!` in that preview flag foreign values the merge refused to overwrite — a conflicting `env.AGENT_PROJECT`, a custom `statusLine` where swarmery's would go, a key with an unexpected shape. The tool will not clobber something you configured by hand, so those are yours to resolve.

Detach is fenced to the `SWARMERY_ONBOARD_ROOTS` allow-list; a project outside it shows the action as unavailable with that reason.

## Session outcome

Your verdict on a finished session: **success**, **fail**, or **abandoned**. It is set by hand and never inferred, which is exactly what makes it worth trusting in the analytics. Clicking the already-selected chip clears the verdict back to unset.

Use *abandoned* for work you walked away from. It is a different signal from *fail*: one says the approach did not work, the other says you never found out. Analytics treats them differently too — `abandoned` sessions are excluded from success-rate denominators rather than counted as failures. Sessions left unlabelled are invisible to outcome analytics entirely.

## Playbooks

A playbook is a selectable execution recipe: an ordered chain of stages, each run as its own headless pass, all sharing the task's single [worktree](#task-worktree). A playbook is a markdown file — frontmatter plus one or more `## Stage:` sections.

1. **Pick a recipe on the board.** Every board task can select a playbook in its drawer or at quick-entry. No selection means *auto*: at dispatch the daemon profiles the card — a prompt over 1500 characters or any declared dependency earns `plan-first`, everything else runs `standard` — and stamps the choice back onto the row, so the card's chip always names the recipe that actually ran. `review-heavy` is never auto-selected; it stays a deliberate opt-in.
2. **Stages run sequentially.** `{task_prompt}` injects the task text and `{previous_stage_output}` hands one stage's full reply to the next. That is how `plan-first` feeds its plan into the implementation stage, and how `review-heavy` critiques the diff it just produced. The full set of template variables is `{task_prompt}`, `{previous_stage_output}`, `{start_point}`, `{branch}`, `{task_id}` and `{file_scope}`. An unrecognised one is rejected at parse time, so a typo fails on load instead of reaching a live prompt as literal `{typo}` — but only when it *looks* like a variable. Validation treats a brace pair as a placeholder solely when it wraps a bare lowercase `[a-z0-9_]` token, which is what lets prose and JSON braces through untouched; the cost is that `{Typo}` is read as prose and passes silently.
3. **The verify knob sets the bar.** See [Verify level](#verify-level).
4. **Make it your own.** Three built-ins ship inside the daemon and are read-only: `standard`, `plan-first`, `review-heavy`. *Duplicate to project* copies the markdown into `<project>/.claude/playbooks/`, where the prompts become editable; a project file with the same name overrides the built-in — the graduation rule, same as any other component. Frontmatter takes `name`, `description`, `verify`, and two optional run knobs: `model` (the card's own model override still wins over it, and the global default is the last resort) and `permission_mode` (`bypassPermissions` | `acceptEdits` | `default`; omitted inherits the daemon's global knob). A fourth built-in, `quick-fix`, was retired — it shipped byte-identical to `standard` and is now an alias: the name still resolves, so stored cards keep working, but it is no longer offered and a write of it stores `standard`.

## Verify level

How hard the trajectory verifier judges a stage's work. Declared per playbook in frontmatter; omitted means `normal`.

The verifier's contract is fixed at every level: it is **read-only** (it may build, test and read, but must not edit files or mutate git state), behavioral criteria default to FAIL unless it can confirm the behavior by running something, and it must end on `VERDICT: PASS | FAIL | INCONCLUSIVE`. The knob moves exactly **one line** of the prompt — the bar, never the verdict vocabulary.

- **strict** — adds a tightening clause: every acceptance criterion must be positively demonstrated rather than merely plausible, and any un-run behavioral check, any change outside the declared scope, or any ambiguity is a reason to withhold PASS. Use it where a false pass is the expensive error, such as a review stage. `review-heavy` uses it.
- **normal** — the default bar. The tightening clause is simply absent.
- **off** — the verification run is skipped entirely, before a prompt is ever built, and **no verdict is stamped at all**. "off" does not mean "passed"; it means nobody looked, and the task keeps whatever verdict it already had.

## Task worktree

Each board task gets one git worktree on its own `swarm/<task-id>` branch, and every stage of its playbook runs in that same worktree — which is what lets a later stage see an earlier stage's edits. Worktrees live under `~/.swarmery/worktrees/<project-slug>/<task-id>` by default.

The branch is always cut from an **explicit start point**, never from whatever HEAD happened to be — a task's diff is therefore always meaningful against a known base.

Two refusals are deliberate:

- If the task branch is already checked out in another worktree, dispatch fails with a busy-branch error instead of silently renaming anything. Conflicts are yours to resolve.
- A worktree path that would sit inside the repo root — in either direction, symlinks resolved — is refused outright. A task never gets handed the repo root.

Reuse is conservative in the same spirit: an existing worktree is reused only when its branch matches, recreated on a *proven* mismatch, and never destroyed because a probe happened to fail. Before acquiring, a stale `index.lock` older than 10 minutes is swept, so one crashed run does not block every future one.

`git worktree add` materializes only *committed* files, so a fresh worktree is lent two things the checkout has but git does not track:

| Lent | What | Knob |
|---|---|---|
| Project config | `.claude/settings.json`, `settings.local.json`, `project.json` — copied when the worktree has none, so the run keeps the project's plugins and permissions | — |
| Installed dependencies | `node_modules`, `.venv`, `vendor` — **symlinked** to the source checkout's copies, so the run's build/test commands have a toolchain instead of failing on a missing module | `SWARMERY_WORKTREE_LEND` (comma-separated relative paths; `off` to lend nothing) |

Dependencies are symlinked rather than copied because such a tree is routinely gigabytes. The consequence is explicit and stated in every run contract: the tree is **shared with your working copy**, so runs are told never to reinstall packages.

## Headless permission mode

A headless run has no one to answer a permission prompt, so any tool call that would ask is auto-**denied** — and the process still exits 0. Left unset, that produces the worst possible outcome: a run recorded as a clean success that wrote nothing, committed nothing and ticked no checkbox.

Every writing spawn site (board dispatch, plan run, phase run, the planner) therefore passes `--permission-mode`, defaulting to `bypassPermissions`. The planner is included for the same reason as the rest: its whole product is the plan dir it writes, so a denied `Write` leaves the plan nowhere but in a reply nobody stores. Verification is the exception — a read-only judge has nothing to be granted. Measured, not assumed: `acceptEdits` lets a run edit files but still refuses `git commit` and any un-allowlisted command, so it cannot finish a phase; only `bypassPermissions` can.

That default is scoped by the surrounding design rather than by the flag: the three execution sites run in a throwaway worktree on a `swarm/` branch, their contracts forbid push/PR/merge, and a project's `permissions.deny` rules **still apply** — `bypassPermissions` skips the ask, not the deny list, so a denied `.env` stays denied. The planner is the one site whose cwd is the real project repo rather than a worktree, so there the guardrail is its contract — research the repo read-only, write only the plan dir, no branches, no edits — plus the same deny rules. Pin a different mode per site with `SWARMERY_DISPATCH_PERMISSION_MODE`, `SWARMERY_PLANRUN_PERMISSION_MODE`, `SWARMERY_PHASERUN_PERMISSION_MODE`, `SWARMERY_PLANNING_PERMISSION_MODE`, or all of them at once with `SWARMERY_PERMISSION_MODE`; the value `off` omits the flag entirely.

## Planning Mode

A headless planner interviews you one question at a time and writes a phased plan into the private workspace.

1. **Describe the idea.** A headless planner session starts in this project's repo — it sees the code, `CLAUDE.md`, and the core-pack planning agents.
2. **Answer structured questions.** One question at a time; pick an option or write your own, while the running plan rebuilds beside it after every answer. If a reply fails the structured-protocol parse, the page falls back to showing the raw prose with a free-text box that answers through the same endpoint, so the interview never dead-ends.
3. **Refine or proceed.** "Refine" steers the plan and the questions that follow; "Continue with the plan" ends the interview and the planner writes the full plan.
4. **The plan lands in the workspace.** A `plan/README.md` (objective, real file paths, phase sequencing table, risks, Definition of Done) plus `phase-N` docs with acceptance checkboxes are written to the private workspace — never into the repo — and appear on the Plans page within seconds.

## Claude account

A Claude Code identity installed on this machine: one config directory plus one credential. The
default account lives in `~/.claude`; every other account lives in `~/.claude-<key>` and is named
by that suffix — the directory `~/.claude-work` **is** the account `work`. There is no second
naming scheme anywhere: sessions, quotas and spawned runs all resolve the account from the config
dir by this one rule.

**Adding one.** Settings → accounts → *+ add account* reserves the key and shows a login command.
swarmery deliberately never runs it for you: you paste it into your own terminal and complete
`/login` there (use a private browser window if it is a different subscription — an already-logged-in
browser will silently reuse the account you are trying to get away from). The credential is written
by the Claude CLI itself, into a Keychain item suffixed for that config dir; swarmery never touches
another program's credential store.

**What the dots mean.** Each account row shows a three-state connection dot: green = connected,
red = not connected, grey = unknown (the daemon was started with `SWARMERY_USAGE_OAUTH=0`, so the
question could not be asked at all). The default account has no *remove* button — it is what every
unbound project falls back to, so there is nothing meaningful to remove it to.

## Account binding

The project-level choice of which [Claude account](#claude-account) its sessions run under. Set it
from the project's settings page (the *account* card) or with `/account use <key>` in a session;
either way it is written to the project's `.claude/settings.local.json` — machine-local and
gitignored, so your choice never lands in the repo or on your teammates.

**When it takes effect.** The account is resolved once, at process spawn time. Every surface —
dispatched runs, verification, planning, the terminal dock, the `claude` shell function — reads
the binding when it starts a process; a run already in flight keeps the account it started with
until it finishes. Changing the binding is therefore always safe, and never instant.

**A binding that git tracks is ignored.** The binding file is meant to be machine-local. A
repository that commits `.claude/settings.local.json` would otherwise choose the account, and
unlock that account's secret store, for everyone who clones it. So swarmery checks where the
file comes from before it trusts it:
- **Honoured:** a file that git does not track, including a gitignored one, and a file that is
  not inside any repository.
- **Ignored:** a file that git tracks. The project then runs under the default account, with no
  secret-store variables.
- **Symlinks:** every symlink on the way to the file is checked in its own repository. A
  committed link cannot borrow another project's untracked binding.
- **Can't tell:** if git is missing from `PATH`, times out after 2 s, or reports an error, the
  binding is also ignored. Swarmery fails closed rather than guessing.

Swarmery logs one warning per path, naming the cause (for example `git not found on PATH`). The
account card on the project's settings page shows the same reason under the picker. A launch is
never blocked: the run just goes out under the default account. To fix it, untrack the file
(`git rm --cached .claude/settings.local.json`) and gitignore it.

**The default is absence.** A project with no binding gets no `CLAUDE_CONFIG_DIR` at all and
behaves byte-for-byte as it did before multi-account existed. Removing an account that projects
still point at is allowed; those projects fall back to the default account and the settings page
shows a dismiss-only warning listing them.

## Retro page

The page you go to when you want the agent system to get *better*, not just to see what it did.
Everything on it is one window — 14 local days by default — folded out of session transcripts,
events and workspace artifacts that are already in SQLite. Nothing here is sampled or estimated
from elsewhere.

It is organised as one loop:

1. **Measure.** The KPI card, the scorecards, the friction board, the lessons feed and the
   estimation table describe the window from five angles.
2. **Analyze.** [Analyze now](#analyze-now) re-runs a deterministic rule engine over that data.
   No model is called.
3. **Recommend.** The rules emit [recommendations](#advisor-recommendations) carrying the numbers
   and sessions they fired on.
4. **Review.** You accept or dismiss. Accepting snapshots a baseline that verification measures
   against a week later.
5. **Improve.** A scorecard's [Improve](#agent-improve) rewrites exactly one agent file. The
   page-level Improve reads the whole report and writes an analysis of the system — agents,
   skills, commands, hooks, processes — which becomes a plan only after you accept it.

`GET /api/retro/report` serves every section as one consistent snapshot plus a deterministic
markdown digest of it. That digest is what the improver reads: every evidence line in it ends in
an `[E:kind:id]` citation marker, so an analysis can only point at things that actually happened.

## Retro KPIs

The lead card: what the window cost, how many runs it took, and how many of those runs hit an
error — each with an arrow comparing it to the **previous window of the same length**. Read the
arrow rather than the number; the absolute totals move with how much you happened to work.

The orchestrator is counted separately from subagents. It never emits a `subagent_start` of its
own, so it has no run count at all — only cost, tokens and errors. When the window overlaps
rolled-up (pruned) days an "approximate" hint appears: per-event detail there is gone, so counts
are lower bounds.

## Agent scorecard

One card per subagent in the window. `runs` are `subagent_start` events, folded across naming
notations so `core:tech-lead` and `tech-lead` are one agent.

The number worth understanding is the **error rate**. It is not error events per run. It is the
share of distinct *runs* that carried at least one **behavior-fixable** error — the classification
the advisor uses. One run spraying twenty tool errors counts once, and infrastructure noise (a
dropped connection, a harness mechanic) does not count at all. What is left is the part of the
failure surface a better prompt could plausibly move, which is why this is the grain the advisor's
R2 rule fires on and the grain the Improve button is worth spending on.

`re-dispatch` comes from the `task_delegations` ledger your retro docs wrote, not from telemetry:
it is how often work handed to this agent had to be handed to someone else.

## Advisor recommendations

Conclusions from `internal/advisor`, a **deterministic rule engine** (R1–R9, no LLM) that folds
the aggregates already in SQLite into evidenced findings. Each row carries the numbers it fired on
and the session ids that produced them, so a recommendation is checkable rather than merely
plausible.

Identity is `rule:target`, aggregated across projects — a fleet-wide view by construction. A
project-scoped page filters on the *evidence*, so fleet-level rules with no session attribution
(process, config) correctly drop out of a single project's view.

**Lifecycle:** `proposed → accepted | dismissed → adopted → verified`.

- **accepted** is your intent, and it snapshots the metric as a baseline.
- **dismissed** suppresses re-proposal for 30 days.
- **adopted** is detected automatically, and only for some targets: an *agent* whose registry
  version changed after acceptance, a *tool* that gained an enabled approval rule, a *process*
  whose referenced improvement flipped to done. An **error group or config recommendation has no
  detectable adoption signal and never shows "adopted"** — it verifies straight from accepted.
- **verified** needs the metric to be at least 20% better than the baseline, at least 7 days later.
  A post-window below the metric's activity floor never verifies: absence of data is not
  improvement.

## Analyze now

Re-runs the rule engine (R1–R9) and the local trajectory evaluator over data already in the
database, then rewrites the recommendations rail. It **calls no model**. It is free, it is
repeatable, and on unchanged data it produces an unchanged result — so there is no reason to
ration it, and no reason to expect a different answer from pressing it twice.

It deliberately does *not* fire the LLM judge. Doing so used to spawn a burst of headless runs per
click, which read as mystery sessions and spent tokens on what the operator experienced as a
refresh. The judge now runs only on the daemon's 24-hour schedule.

The run is always fleet-wide, even from a project-scoped page: the rules compute cross-project
rates, and narrowing the *input* to one project would produce statistically wrong numbers.
Narrowing happens on the read side instead.

## Improve the system

The page-level counterpart to [Agent improve](#agent-improve). It takes the whole window — every
section of the report, as one consistent snapshot — renders it as a deterministic digest, and has
an agent write an analysis in three parts: what hurts, why, and what to change. Its mandate covers
agents, skills, commands, hooks and processes, which is exactly the ground the per-agent rewriter
structurally cannot reach.

Unlike [Analyze now](#analyze-now), **this one calls a model and costs tokens**. One analysis runs
at a time; a second press answers "already running" rather than starting a competing one.

**Citation is the contract, enforced in code.** Every evidence line in the digest carries an
`[E:kind:id]` marker, and every claim in the analysis must end in one copied from it. An analysis
that cites nothing, or that cites an identifier the digest never offered, is stored as **failed**
with the reason — never as a valid proposal. Prose about a system is easy to write and hard to
check, so unverifiable advice wearing the same badge as evidence is worse than no analysis at all.
The rejected text is kept on the failed row, because a refusal you cannot inspect is one you
cannot learn from.

**Nothing is written until you accept.** The lifecycle is
`running → proposed → accepted | dismissed`, and then `accepted → planned`. Only an accepted
analysis can start a planning session, and only its "what I would change" section travels — as the
idea for a normal [Planning Mode](#planning-mode) interview, in a project you pick explicitly. That
choice is deliberate rather than inherited from the page scope: the changes land in the agent
system's own repository, which is usually not the project whose sessions produced the evidence.

If that project already has a planning run in flight, the card says so and links to it.

## Agent improve

The button on a scorecard. It generates a **minimal unified diff to exactly one agent definition
file** — one `plugins/<pack>/agents/<name>.md`, resolved from the marketplace clone at
`origin/main` — out of that agent's own evidence bundle. It does not touch skills, commands,
hooks, other agents, or anything about how work is routed. Those live outside its mandate by
design; the page-level improver is what covers them.

The prompt demands a minimal change and targets 120 changed lines or fewer, because a diff a human
will not read is a diff a human cannot approve.

Opening it shows the **evidence first**, not the diff: the scorecard slice, the ledger
assessments, open improvements and transcript excerpts the model will reason over.

## Agent proposals

The diffs Improve has produced and you have not yet decided on. Lifecycle:
`proposed → approved → applied | rejected`, with `failed` terminal-but-retriable.

Each proposal is pinned to the sha256 of the agent file it was generated against, so a diff that
has gone stale fails to apply instead of quietly clobbering newer edits. One invariant keeps the
rail honest: **one open proposal per agent** — decide the current one before generating another.

Approving applies the change against the marketplace clone, not your working tree.

## Trajectory judgments

An LLM judge's 1–5 scores for completed sessions across a few dimensions. It is **advisory**: it
feeds the success rate on the scorecards and informs nothing that gates or blocks. Only sessions
the judge has actually scored appear, and it runs on the daemon's 24-hour schedule — never from
[Analyze now](#analyze-now).

## Friction board

Where the system stalls rather than fails.

- **Denied tools** — `tool_call` / `skill_use` / `subagent_start` events with `status=denied`,
  with a flag for whether an enabled approval rule already covers the tool. A repeatedly denied
  tool with no rule is usually the cheapest fix on the page, and the rule can be created inline.
- **Top error groups** — the same folding `/api/stats/errors` uses, so the numbers agree across
  pages.
- **Approvals** — resolve times computed from `permission_requests`. The *pending* count is
  deliberately **not** window-filtered: a request opened last month still blocks work today.

## Lessons learned

Lessons your own retrospectives recorded, parsed from `09-retrospective.md` docs in the private
workspace and joined to the tasks that produced them. This is written knowledge — by agents and by
you — not something inferred from telemetry, which makes it the one block on the page that can
tell you *why*.

Filtered on the task's start date, newest first, capped at 100. An empty feed means the tasks in
range wrote no retrospective, not that nothing was learned.

## Estimation accuracy

Estimated versus actual hours per workspace task, with the loop count and the delegation ledger
beside it. Only tasks that produced at least one parsed artifact (a retro doc, a loop journal or a
ledger) appear; capped at 200, newest first.

Read the columns together. A large variance next to many loops usually means the task was
underspecified rather than underestimated. A re-dispatch verdict is routing feedback — the wrong
agent was picked — not evidence that the agent is bad.

---

Plugin and marketplace mechanics — how an enabled pack physically reaches a session, why a semver
bump is mandatory for consumers to adopt a change, and what `--plugin-dir` is for — live in
[PLUGINS.md](PLUGINS.md#how-a-plugin-reaches-your-session).

The two routes behind a pack row's `configure` modal — what the save writes into
`.claude/project.json`, what the probe may and may not do, and the fence both share — live in
[api-project-config.md](api-project-config.md).
