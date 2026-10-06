# Changelog

All notable changes to this repository are recorded here, in
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/) format.

Two things ship from this repository on separate clocks:

- **The control plane** (`tools/swarmery/`) is released by pushing a `swarmery-v*`
  tag. The version headings below are its releases.
- **Marketplace plugins** each carry their own semver in
  `plugins/<name>/.claude-plugin/plugin.json` and reach consumers through
  `/plugin update`, not through these tags. Current: `core` 3.11.0,
  `infra-pack` 1.5.0, `architecture-pack` 1.6.0, `iot-pack` 1.3.0,
  `uav-pack` 1.4.0, `web-pack` 1.4.0, `claude-eng-pack` 1.1.1,
  `graphify-pack` 1.1.1, `lsp-pack` 1.0.0, `jira-pack` 0.7.0,
  `design-pack` 0.5.0, `accounts-pack` 0.3.4, `graft-pack` 0.1.0. The
  marketplace's `metadata.version` tracks `core`.

## [Unreleased]

### Security

- **A binding file tracked by git no longer picks the account (#392).** Every
  consumer of the binding now ignores a `.claude/settings.local.json` that git
  tracks: spawns, `swarmery account which|env|exec` and the dashboard. The
  project runs under the default account and gets no secret-store variables.
  The check fails closed. It also covers every symlink on the way to the file,
  and it is ignored the same way when git is missing, times out after 2 s or
  errors. The warning names the cause, and the dashboard's account card shows it
  (`ignoredReason` on the binding DTO). Docs: `accounts-pack` 0.3.4 (README
  Known edges, `/account`).

### Added

- **Production deploys ask first, even when allowlisted or in bypass mode (core 3.11.0,
  #480, #483, #485).** A new `PreToolUse(Bash)` hook, `prod-deploy-guard.sh`, answers
  `permissionDecision: "ask"` for any command that matches a production-deploy pattern,
  so Claude Code shows its own confirmation dialog in the terminal regardless of allow
  rules, auto mode or `bypassPermissions`. In a headless `-p` run nobody can answer, so
  the call is denied, which is the intended result — **this includes daemon plan and
  phase runs, dispatched cards and scheduled `-p` jobs**: a deploy step they used to run
  unattended now fails, and there is no switch to turn the guard off (project patterns
  only add). Run such deploys from an interactive session. Patterns come from
  `hooks/lib/prod-deploy-patterns.txt` plus the project's
  `approvals.prodDeployPatterns` in `.claude/project.json`, in the daemon's
  `Tool(argGlob)` grammar. A Go parity test keeps that list identical to the daemon's
  `DefaultProdDeployPatterns`, and both matchers run the same
  `scripts/tests/fixtures/prod-deploy-patterns/cases.tsv`. Every failure path asks
  rather than allows: a missing defaults file asks on every Bash call, a malformed
  `project.json` falls back to the defaults, and without `jq` the hook matches against
  the raw payload. The hook never emits `allow`. The mode table comes from
  `scripts/tests/prod-deploy-ask-probe.sh` (claude 2.1.291, headless legs measured,
  interactive legs pending). Burn-in follows `docs/GATE-HARDENING.md`: rule `ask` is
  enforced from this release, and rule `deny-fallback` stays in `warn`. Decisions are
  logged to `prod-deploy-guard.jsonl`, which `scripts/guard-hits.sh --log` reads.

- **The task modal's agent picker groups agents by role (core 3.10.0, design-pack 0.5.0,
  infra-pack 1.5.0, iot-pack 1.3.0, jira-pack 0.7.0, uav-pack 1.4.0, web-pack 1.4.0).**
  Every pack with agents now ships `agents/roles.json` (`{"<agent>": "<role>"}`, role ∈
  `orchestrate | implement | review | research | ops | domain`); agent frontmatter is
  unchanged. A project can add its own `.claude/agents/roles.json`. The daemon's
  `GET /api/agents/hub` reads the file next to each agent (`role`, `domain` when missing)
  and reports `enabledInProject` against the project's effective `enabledPlugins`. The
  picker's first option is now `Без агента — лише стейджі playbook`, agents sit in one
  `<optgroup>` per role, packs disabled in the project are hidden, a project override
  gets a badge, the `· global` suffix is gone, and a search box appears past 12 agents.
  `scripts/tests/agent-roles.test.sh` fails CI when an agent has no role or a role names
  a missing agent.

- **Explainer pages from a document: `/visualize` and the `visual-explainer` skill (architecture-pack 1.6.0).**
  The skill turns a markdown file, several files or an explanation from the conversation into
  one self-contained HTML page: a plain-language thesis, one diagram, chart or table per
  chapter, glossary tooltips, and a side drawer for the technical depth. Pages start from
  `templates/explainer-shell.html` — overridable per project at
  `.claude/templates/explainer-shell.html` — which owns navigation, theming, the drawer,
  tooltips, tabs, a step-through player, scenario switches and filters, and never parses a
  string as HTML. `scripts/check.mjs` (static) and `scripts/probe.js` (inside the page) measure
  the result at 1440 and 400 px in light and dark; `scripts/serve.mjs` serves just that one page
  on 127.0.0.1 and stops by itself. System fonts by default, so the file works offline. Covered
  by `scripts/tests/visual-explainer.test.sh`, which runs `probe.js` in headless Chrome.

- **Plain-language plan summary (core 3.9.2).** `session-closeout` now also
  writes `plan/SUMMARY.md`: the overview the dashboard shows at the top of a
  plan's Summary tab. It covers what the task was, what was built and how it
  works, where to see it, and what is left, written for the operator rather than
  as an engineering record (`resources/plan-summary-format.md`).

- **An "onboarded only" filter on the Projects page (#388).** It is on by default.
  The daemon computes a per-project `onboarded` bit (on `GET /api/projects`):
  the project is managed and not nested under another managed project. `/`,
  `$HOME` and the onboarding roots never count as that parent. When the filter
  hides everything, the empty state says how many projects are hidden. The
  Health table follows the filter, and the System project stays visible.
- **Channel probes G1–G3 and a `--setting-sources` census (#393).** They cover
  which config channels Claude Code actually reads for plugin MCP servers, and
  which twelve daemon spawn seams close the user settings tier. Details:
  `tools/swarmery/docs/claude-cli-config-channels.md`.

### Fixed

- **The SessionStart hooks find the active task card again (core 3.9.7).**
  `session-start.sh`, `task-session-log.sh` and `session-context-bridge.sh`
  selected the active task by grepping its README for the substring `Status:`,
  but the card `agent-work.sh init` writes said `- **Статус**: active` and the
  daemon's said `- **Status**: active` — neither contains that substring (the
  colon follows the closing `**`), so the in-flight banner stayed empty, the
  explicit task↔session link was never written and the bridge could carry a
  finished task's NEXT.md into a cold session. The canonical status line is now
  `- **Status**: <value>`: `agent-work.sh init` and the README card template
  write it, and the daemon's lifecycle inserts it on a card that has none.
  Every reader accepts it alongside the legacy `**Статус**` label and the
  hand-written `Status:` / `**Status:**` forms: the three hooks,
  `agent-work.sh pause|resume|complete|restore|list|index|metrics` (which edit
  a legacy card in place and keep its label) and the daemon's card parser.
  Guarded by `scripts/tests/task-card-status.test.sh`, which drives the real
  `init` against the real hooks.
- **Linux `make install` restarted the service on the old binary.** The
  Makefile installed the rebuilt daemon into `~/.local/bin/swarmery`, while the
  `systemd --user` unit that `swarmery install` writes and the hook entries that
  `swarmery hooks install` writes both run `~/.swarmery/bin/swarmery` — so the
  restart brought the previous build back up and every hook kept using it.
  `make install` now installs into `~/.swarmery/bin` on both platforms, with the
  same copy-then-rename as macOS, restarts the unit whenever the user manager
  has it loaded, and prints the build the service came back up on. The
  control-plane README now says that a release install (`scripts/install.sh`
  only replaces `~/.local/bin/swarmery`) needs a `swarmery install` afterwards
  so the service and hooks pick up the new binary — the installer prints that
  reminder when it finds a service definition — and its Rollback section no
  longer claims an older binary refuses a newer database: migrations are
  forward-only and an older binary opens the newer schema without complaint,
  so an incompatibility shows up as runtime SQL errors and the clean rollback
  is restoring the pre-upgrade snapshot.
- **Micro-plans go into the onboarded workspace (#386).** A dispatched card's
  micro-plan used to go into a duplicate tree. It now goes into the project's own
  workspace namespace: the most recently scanned mapping, the same one its repo
  is resolved from. The path is fenced to the workspace root, with
  `<root>/<slug>` as the fallback. Dashboard onboarding registers a new project
  under the onboarding slug, and it never renames an existing project's slug,
  because worktree names and session attribution depend on it.
- **Modal exits no longer lose typed input silently (#389).** All eight
  dashboard modals now go through one `useDiscardGuard` hook, and "dirty" means
  "differs from the opened or pre-filled value". The confirm dialog takes focus,
  keeps Tab inside itself, and closes only itself on Esc or a backdrop click.
  The task modal closes only once its save has succeeded.
- **The statusline's memory count uses Claude Code's own project-directory
  encoding (#393, core 3.9.3).** Memory is no longer counted for the wrong
  account.
- **`agent-work.sh` on Linux (core 3.9.1).** Task mtimes are read with the
  probed `stat` dialect instead of an `||` fallback that GNU `stat -f` never
  reached, so `list`/`index` stop reporting filesystem blocks as dates (#373).

### Changed

- **planner switches from reading to writing before its turn ceiling (core
  3.10.1).** A new "Turn budget" section caps code reading at ~60% of
  `maxTurns`, has the agent write `plan/README.md` first and then one phase doc
  per turn, and records still-unknown facts as open questions. Before this, a
  wide brief could spend all 40 turns on research and leave no file on disk.
- **Agent effort from a measured sweep (core 3.9.0).** An Opus 5.5 effort sweep
  over the eval suites (low / medium / high) set: tech-lead, planner,
  code-reviewer and security-auditor to `medium` (same pass rate as `high`,
  25–35% fewer output tokens); implementation-agent stays `high` (the only
  effort that passed the checkbox-progress contract in both runs); debugger
  moves from sonnet/high to opus/medium (100% vs 0%); architect stays `high`
  (no suite yet). The sweep runs on a Claude subscription via
  `evals/providers/claude-cli.js` and `evals/sweep/run.sh`.

## [0.2.1] - 2026-09-24

### Added

- **Predictive learning loop + local decision classifier (shadow).** Swarmery
  now learns from where its agents' expectations were wrong, even though the
  model's weights do not change. Everything is advisory and local, and nothing
  gates a run or a merge on it.
  - *Forecasts.* A phase doc may carry a `## Forecast` block. The planner writes
    a prior and the executor writes a posterior before its first edit, covering
    areas, files, size and duration bands, outcome, risks and confidence. It is a
    prediction, not a limit. A posterior written after the run's first edit is
    stamped post hoc and excluded from calibration.
  - *Actuals and surprise.* Every phase run records what it actually did
    (`phase_actuals`): git numstat of the run branch, cost, outcome, verify
    verdict, unexpected test failures, continuations and model fallback. A
    deterministic surprise vector and a 0..1 index (`phase_surprise`) compare
    that record with the forecast. A Plans chip and a "Forecast vs actual" tab
    show the index; above `SWARMERY_SURPRISE_NOTIFY` it raises a notify event,
    and an opt-in auto-verify can fire. The retro digest cites
    `[E:phase:<id>]`.
  - *Lessons.* A surprising run that explains its divergence yields 0–2
    area-scoped lesson candidates, each citing evidence present in its input.
    Only the operator's accept makes a lesson active. Active lessons are
    injected into headless phase and plan prompts whose forecast areas overlap
    them, under a hard token budget, and every injection is recorded. A lesson
    can be promoted into the area's nested `CLAUDE.md` on a review branch.
    Effectiveness (area surprise before vs after activation) drives ranking and
    retirement proposals: ineffective, stale, unused or superseded. A proposal
    auto-retires after 14 days without a response.
  - *Calibration.* `/api/calibration` and `swarmery calibration` report forecast
    accuracy per agent, model, effort and project, hiding groups under 20
    samples. The monthly `model-upgrade` routine reads it.
  - *Decision classifier.* `internal/decide` asks cheap typed questions around
    runs, never inside them. The backends are rules, then a local
    OpenAI-compatible server. A claude haiku backend exists but is off, and
    it answers only questions that opt in to leaving the machine; D1 and D2
    don't opt in yet. It asks D1 (how did
    this run end?) after the completion loop's rules, and D2 (session labels)
    for advisor and retro. Both default to shadow, every call is audited in
    `decisions`, and with `SWARMERY_DECIDE_URL` unset nothing changes. A
    third question, D3 (why did a surprising run diverge?), labels the run's
    own divergence paragraph; it is shadow by default (`SWARMERY_DECIDE_D3`).
  - *Core.* The new read-only `area-lessons` skill, plus forecast contracts in
    the planner and executor prompts (core 3.8.0).

- **Opus 5.5 readiness.** Eight phases of work for a model that thinks on every
  turn and follows named stops. The short version: costs are honest again,
  every headless run says out loud which model and how hard, "done" is decided
  by evidence instead of an exit code, a safeguard downgrade is a visible event
  rather than a silent one, and the prompt layer stopped asking Claude to
  narrate its reasoning.
  - *Cost.* Cache writes are priced per TTL — Claude Code writes 1h cache, and
    billing all of it at the 5m rate under-charged every such write by 37.5%.
    Fast-mode turns bill at their own `-fast` SKU. Old transcripts price exactly
    as before until `swarmery backfill --rebuild-text` replays them to learn
    their split, after which `swarmery recost` reprices them.
  - *Runs.* All 14 headless spawn sites pin `--model` and `--effort`; `routines`
    had been sending no model at all, so every tick ran on the account default.
    A run that exits 0 with criteria unticked and no blocked line is resumed in
    the same session, at most twice, inside its own wall-clock budget.
  - *Safeguards.* `model_refusal_fallback` is ingested, turns carry
    `stop_reason`, and a run that ends in a refusal is stamped blocked with the
    reason. The hooks stopped reporting a fallback on nearly every dispatch, and
    stopped vetoing the downgrades they were never meant to block.
  - *Prompts.* One stop/continue rule across `CLAUDE.md`, tech-lead,
    implementation-agent and run-plan; subagent claims are accepted on evidence
    rather than on an artifact existing; review returns blockers only. The
    domain packs lost 23 `<thinking>` instructions and 97 `[PE/…]` tags.

- **Three new packs.** `jira-pack` — ticket triage with mandatory reproduction,
  writeback and code delivery, gated by its own CI contract test.
  `design-pack` — a `/design-implement` workflow with a self-contained pixel
  verification runtime. `accounts-pack` — a multi-account terminal surface and
  the `swarmery account` CLI.
- **Multi-account support end to end.** Account discovery and per-project
  binding (`internal/claudeacct`), an OAuth connect flow with a write-once
  credential handoff, per-account quota fan-out in the usage modal, and a
  statusline chip that warns when a session is running under the wrong account.
  Every spawned `claude` process now runs under the project's bound account.
- **Plan revision.** Saved plans can be revised through the dashboard: a
  revise-mode planning session stages a proposal, and the operator reviews a
  diff and applies it under a conflict guard. Markdown stays the source of truth.
- **Specs and coverage.** A plan may carry a `spec.md` of `SC-n` criteria; phase
  docs declare which they cover, and an uncovered criterion gates the plan run.
- **`internal/runcore`.** One spawn primitive and one shared run budget behind
  the five engines that used to each own their own; a busy pool answers 409.
- **Phase verification.** A phase run can ask to be graded before its worktree
  goes; a failed grade reads as a blocker beside the outcome, never as a second
  status. An unverified phase reads as unverified rather than as done.
- **Task board.** Three lanes, cards captured from a session, dispatch as a
  chosen agent, labels, and a review section with diff, re-run, land and discard.
- **Worktree janitor.** Classifier, sweep, salvage and journal, an inventory
  endpoint, a settings panel, and `swarmery worktrees clean`.
- **Retrospectives.** The retro window exports as one report plus a
  deterministic digest, and the `@system-improver` agent turns that digest into
  a cited, human-gated analysis that can become a plan.
- **Documentation as a gate.** Every registrable agent, skill and command now
  carries a `# How to use` block; `scripts/docgen/` generates and checks them,
  and CI fails on a gap (106/106 today).
- **A public landing page** under `site/`, deployed by GitHub Pages, with Open
  Graph assets so shared links render a preview card.
- **A binary installer.** `scripts/install.sh` detects os/arch, resolves the
  latest release, verifies `SHA256SUMS` and installs to `~/.local/bin` — no Go
  or Node toolchain. `scripts/install-swarmery.sh` remains the source path.
- **Generated counts.** `scripts/docgen/counts.sh` derives every published
  number from the corpus, `apply-counts.sh` splices them into the README and the
  landing page, and CI fails on drift.
- **New CI gates.** Component reference integrity (dead skill/doc refs, pinned
  models, ignored frontmatter), a Claude Code version floor, pack-requirements
  schema sync, a portable-shell scanner, and the counts drift check. The shell
  test suite is now discovered rather than listed.
- **Model-upgrade governance.** A `PreModelSwitch` gate and `PostModelSwitch`
  recorder, a monthly model-upgrade routine, and a version-floor gate.

### Changed

- **`design-pack` 0.4.0.** The pack's main skill is renamed `design-implement`
  → `design-build`, because a plugin that ships both a command and a skill under
  the same name registers them under one key and one of the two is dropped —
  `design-pack` was the only pack in the marketplace with that collision, and
  the casualty was `/design-implement` no longer being offered in the slash-command
  picker. `/design-implement` stays the entry point and its arguments are
  unchanged; only the skill it hands control to has a new name.

- **License split.** The plugin framework (`plugins/`, `scripts/`, `overlays/`,
  `docs/`, `site/` and the repository root) is now **Apache-2.0**; the control
  plane under `tools/swarmery/` stays **PolyForm Noncommercial 1.0.0**, with its
  text moved to `tools/swarmery/LICENSE`. The root `NOTICE` names both regions.
- **`core` 3.0.0 (breaking).** Forty-two agents consolidated into thirteen
  judgment-style agents; eight duplicate commands retired; skills moved to
  progressive disclosure. See `docs/MIGRATION-core-3.md`.
- Executors must write a phase Completion Report into the phase doc itself —
  a hard gate, because a report elsewhere is invisible to the operator.
- Planners must emit per-step tickable acceptance criteria: one dispatch, one
  checkbox; aggregates only as final gates.
- `SubagentStop` writes the delegation ledger, so orchestrators stop
  hand-keeping it.
- A Bash shape guard refuses malformed commands, worktree escapes and ambiguous
  git calls before the classifier sees them.
- Restricted agent classes are enforced rather than declared, and every model
  reference is an alias rather than a pinned id.

### Fixed

- The retro judge was scoring its own scoring runs.
- The task ↔ session link had no live writer; sessions were never attached.
- Headless runs could not write, test or commit — every spawn now declares its
  permission mode, guarded by a test.
- Session queries materialised a full turns scan every time.
- A plan or phase ran in the project root rather than in its declared repo.
- Two migration files could claim the same version; the daemon now refuses to
  start instead.
- Checkboxes inside code fences were counted as plan progress.
- Phantom sessions were minted from title-only transcripts.
- BSD-only shell shapes broke on Linux, so several suites had never run green
  off macOS.
- Open Dependabot and CodeQL alerts closed.

## [0.2.0] - 2026-07-31

First tagged release of the control plane, with binaries for
`{darwin,linux}-{amd64,arm64}` and a `SHA256SUMS` manifest.

### Added

- Session indexing from `~/.claude/projects/` into local SQLite, served as a
  dashboard on `:7777` from a single Go binary with an embedded React SPA.
- A permission-approval queue: hook shim, long-poll backend, approvals screen,
  live nav badge, and `AskUserQuestion` answered from the dashboard.
- Task dispatch — headless spawns into git worktrees, with gates, sentinels and
  auto-verification.
- Planning mode, epics rollup with a dependency graph, routines (cron, webhook
  and manual), playbooks, an embedded per-worktree terminal, an agent hub, a
  system hub, memory and per-project insights, and six curated themes.
- Cost and usage analytics, retro scorecards with a friction board and advisor
  recommendations, and per-agent run history.
- The marketplace itself: `core` plus the first domain packs, the workspace CLI
  (`agent-work.sh`), the neutrality ratchet, and the overlay schema.

[Unreleased]: https://github.com/atretyak1985/swarmery/compare/swarmery-v0.2.1...HEAD
[0.2.1]: https://github.com/atretyak1985/swarmery/compare/swarmery-v0.2.0...swarmery-v0.2.1
[0.2.0]: https://github.com/atretyak1985/swarmery/releases/tag/swarmery-v0.2.0
