# Changelog

All notable changes to this repository are recorded here, in
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/) format.

Two things ship from this repository on separate clocks:

- **The control plane** (`tools/swarmery/`) is released by pushing a `swarmery-v*`
  tag. The version headings below are its releases.
- **Marketplace plugins** each carry their own semver in
  `plugins/<name>/.claude-plugin/plugin.json` and reach consumers through
  `/plugin update`, not through these tags. Current: `core` 3.1.0,
  `infra-pack` 1.3.1, `architecture-pack` 1.2.0, `iot-pack` 1.2.1,
  `uav-pack` 1.2.1, `web-pack` 1.2.1, `claude-eng-pack` 1.1.0,
  `graphify-pack` 1.1.0, `lsp-pack` 1.0.0, `jira-pack` 0.6.1,
  `design-pack` 0.4.0, `accounts-pack` 0.2.1. The marketplace's
  `metadata.version` tracks `core`.

## [Unreleased]

### Added

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

[Unreleased]: https://github.com/atretyak1985/swarmery/compare/swarmery-v0.2.0...HEAD
[0.2.0]: https://github.com/atretyak1985/swarmery/releases/tag/swarmery-v0.2.0
