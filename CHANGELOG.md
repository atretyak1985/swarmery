# Changelog

All notable changes to this repository are recorded here, in
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/) format.

Two things ship from this repository on separate clocks:

- **The control plane** (`tools/swarmery/`) is released by pushing a `swarmery-v*`
  tag. The version headings below are its releases.
- **Marketplace plugins** each carry their own semver in
  `plugins/<name>/.claude-plugin/plugin.json` and reach consumers through
  `/plugin update`, not through these tags. Current: `core` 3.9.0,
  `infra-pack` 1.4.0, `architecture-pack` 1.5.0, `iot-pack` 1.2.1,
  `uav-pack` 1.3.0, `web-pack` 1.3.0, `claude-eng-pack` 1.1.1,
  `graphify-pack` 1.1.1, `lsp-pack` 1.0.0, `jira-pack` 0.6.2,
  `design-pack` 0.4.1, `accounts-pack` 0.3.2, `graft-pack` 0.1.0. The
  marketplace's `metadata.version` tracks `core`.

## [Unreleased]

### Changed

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
