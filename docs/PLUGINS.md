# Plugins

The swarmery marketplace ships one mandatory **core** plugin and eleven opt-in packs. Everything here is vendor-neutral: project-specific flavor comes from each consumer's `.claude/project.json` overlay at runtime, never from the plugins themselves.

> [!NOTE]
> `.claude-plugin/marketplace.json` is the source of truth for what ships. The
> per-pack sections below cover the long-standing packs; a pack listed in the
> manifest but not described here is still installable.

## Enabling and disabling

Per project, three equivalent ways:

1. **Control-plane dashboard** — open the project (`/projects/:id`) → **plugins** card → flip the toggle. The daemon performs merge-only surgery on the project's `.claude/settings.json` (`enabledPlugins["<pack>@swarmery"]`), backs the file up to `settings.json.bak`, and never overwrites a malformed file. `core` is locked there — its lifecycle (hooks, statusline, project.json) belongs to attach/detach.
2. **Bootstrap** — `scripts/init.sh` writes the initial `settings.json` with core plus the packs you pick.
3. **By hand** — add `"<pack>@swarmery": true` under `enabledPlugins` in the project's `.claude/settings.json`.

Changes take effect in the **next Claude Code session**: on startup Claude Code installs enabled packs from the marketplace into its plugin cache. Every pack requires `core`. Disabling a pack removes its key (same end state detach leaves).

## How a plugin reaches your session

Enabling a pack does not copy any file into your project. The path from this repo to a running session has four steps, and knowing them explains almost every "my change did not take effect" report.

1. **The marketplace manifest** — `.claude-plugin/marketplace.json` in this repo lists every plugin and the directory it is published from. Each plugin's own version lives in `plugins/<pack>/.claude-plugin/plugin.json`.
2. **Your project opts in** — `enabledPlugins` in the project's `.claude/settings.json` names the packs it wants, as `"<pack>@swarmery": true`.
3. **Claude Code installs into its cache** — on session startup, enabled packs are fetched from the marketplace into `~/.claude/plugins/cache`.
4. **The session loads from the cache** — never from a local checkout.

**Step 4 is the one that surprises people.** Editing `plugins/core/agents/<agent>.md` in a clone of this repo changes nothing about a running session, because the session is reading its own cached copy.

### Getting your changes into a session

| You want | Do this |
|---|---|
| Test an uncommitted plugin change, right now | `claude --plugin-dir plugins/core` — repeatable, one `--plugin-dir` per pack. This is the way to run work that is not committed and released, and it affects only that session. |
| Ship a change to every consumer | Bump the plugin's `version` in `plugins/<pack>/.claude-plugin/plugin.json`, push, then run `/plugin update` in each consumer. |
| Refresh your own machine's cache after committing locally | `scripts/sync-cache.sh` rsyncs each `plugins/<pack>/` over the matching installed version directories under `~/.claude/plugins/cache/swarmery/` (packs that are not installed are skipped; `.claude-plugin/` is excluded). Designed to run from a `post-commit` hook, but it rewrites the shared cache for **every** project on the machine — prefer `--plugin-dir` when you only want to try something out. |

**Bump the semver.** Consumers adopt a plugin by version. A change pushed without a version bump is a change no consumer will ever pull, and the failure is silent — `/plugin update` simply reports nothing to do.

### When enabled and installed disagree

`enabledPlugins` records intent; the cache records reality. They come apart in ordinary ways:

- a pack was enabled in `settings.json` but no session has started since, so it was never fetched;
- a pack was renamed or removed from the marketplace while a consumer still lists it;
- a project was copied from another project along with its `settings.json`.

The symptom is an agent or command that "should exist" and does not. Check what is actually installed under `~/.claude/plugins/cache` before debugging the pack itself.

### Overriding instead of forking

A component in a project's own `.claude/` **wins** over a plugin component with the same name. That is the supported way to change a pack's behaviour for one project — not editing the cache, and not forking the pack. When a second project needs the same change, promote it upward instead; see [EXTENDING.md](EXTENDING.md).

---

## core — the mandatory baseline

**What it is.** The vendor-neutral agent-development framework: a 13-agent judgment-style fleet, 36 progressively-disclosed skills, 8 commands, 18 hooks, plus the project-aware `agent-work` workspace CLI and the statusline.

**What it can do.**
- **Orchestration** — `@tech-lead` routes work by size (understand → plan → implement → independent review → close) across a 13-agent fleet: `planner`, `architect`, `researcher`, `implementation-agent`, `ui-developer`, `debugger`, `test-writer`, `test-runner`, `code-reviewer`, `security-auditor`, `verification-agent`, `system-improver` (see `plugins/core/AGENTS.md`).
- **Everyday commands** — `/search`, `/find`, `/impact` (graph-aware with ripgrep fallback), `/code-quality`, `/test-coverage`, `/security-audit`, `/deps-check`, `/env-check`, `/migration-check`, `/refactor-plan`, `/run-plan`, `/new-feature-branch`, `/dashboard`, `/land`.
- **Guardrails** — hooks for sensitive-file protection, approvals/liveness wiring for the control plane, and the graduation rule tooling that keeps components flowing project → pack → core.

**How to work with it.** Enabled everywhere by definition; a project without core is telemetry-only in the dashboard. Templates resolve project-first (`.claude/templates/` overrides the pack's `templates/`).

---

## lsp-pack — Serena semantic code navigation

**What it is.** Packages the [Serena](https://oraios.github.io/serena/) MCP server: symbol-level search, references, and refactoring context over an LSP backend — a complement to text search, not a replacement.

**Requires.** The `serena` binary installed on the machine (via `uv`). The pack's `.mcp.json` starts it per session: `serena start-mcp-server --context claude-code --project .`.

**What it can do.**
- In-session MCP tools: find symbol definitions/references, semantic edits, project memories.
- **Serena web dashboard** — live view of the active config, tool usage, execution queue and logs.

**How to work with it.**
1. Enable `lsp-pack` for the project (plugins card).
2. New Claude Code sessions in that project get the `serena` MCP tools automatically.
3. **Dashboard sidebar → TOOLS → Serena**: pick the project, press **start** — the swarmery daemon launches a managed serena process (SSE transport, web dashboard on) and embeds its dashboard once it reports running; **stop** terminates the whole process group. State, log tail and errors are shown honestly while starting/failed. The daemon kills all serena children on shutdown.

---

## graft-pack — the context graph, unasked

**What it is.** The `graft` skill plus the two hooks that make it act without being invoked. Graft keeps a wiring graph of the repo — every file and symbol as a node, `calls`/`imports`/`contains` as edges — under `<repo>/graft/`. The pack's premise is that a graph nobody remembers to query is a graph nobody uses, so it puts the answers in front of the agent instead.

**Requires.** The `graft` CLI on the machine (`npm i -g @nanonets/graft`). No API key: every command the pack runs is a local graph read.

**What it can do.**
- **Prompt injection** (`UserPromptSubmit`). Runs each prompt through `graft ask --json --no-refresh` and injects `file:line` locators when the graph demonstrably covers it. The gate is graft's own — a resolved-symbol answer always passes, a lexical answer needs `coverageStrong >= 0.1` or `coverage >= 0.5` — so a project that calibrated under `graft init` behaves identically under the pack. Below the gate it nudges toward `graft ask --source`, at most twice per session.
- **Blast radius** (`PostToolUse` on `Edit|Write`). After a file changes, names the files that depend on it, read straight from `graft/.graph/wiring.json`. Capped at `graft.maxDependents` (default 8) with a `+N more` tail.
- The skill's decision table: `ask --source` for how/where, `grep` for every occurrence, `skeleton` for a file's API, `callers --depth 2` before a rename, `map` for orientation.
- Core's `/impact` prefers `graft callers` when the CLI is present, then graphify, then ripgrep.

**Configuration.** All optional — every field has a default, so an enabled pack works with no config. In `.claude/project.json`:

```json
{ "graft": { "graphDir": "graft", "promptContext": true, "blastRadius": true, "maxDependents": 8 } }
```

Per-session kill switches: `SWARMERY_GRAFT_PROMPT=0` and `SWARMERY_GRAFT_BLAST=0`. Both hooks fail open — a missing CLI, a missing index, a slow or crashed call all exit 0 silently.

**How to work with it.**
1. Enable `graft-pack` for the project, then run `graft build` once in the repo.
2. **Dashboard sidebar → TOOLS → Graft**: the project's index with its node/edge counts and build time. `available: false` there means the pack is on but the CLI is missing.
3. Keep it fresh: `graft check` fails when the index lags the code, and a plain `graft build` is incremental. A stale graph's silence looks exactly like "nothing depends on this", which is the one failure mode worth watching for.

**If both graft-pack and graphify-pack are on**, they do not conflict — different directories, different hooks. Note that a project already wired by `graft init` carries graft's own hook entries in its `.claude/settings.json`; remove those when enabling the pack, or every prompt is processed twice.

---

## graphify-pack — knowledge graphs

**What it is.** The `/graphify` skill: turns any repo or folder (code, docs, SQL, media) into a persistent knowledge graph with community detection, an audit trail, and three outputs — interactive `graph.html`, GraphRAG-ready `graph.json`, and a plain-language `GRAPH_REPORT.md` under `<repo>/graphify-out/`.

**Requires.** The `graphify` CLI on the machine (PyPI `graphifyy`, via `uv`).

**What it can do.**
- Build/refresh graphs: `/graphify`, `/graphify <path> --update`, `--mode deep`, multi-repo merges, GitHub URLs.
- Query for agents and humans: `graphify query "what depends on X?"`, `graphify path A B`, `graphify affected <symbol> --depth 3`, `graphify god-nodes`.
- Exports: Neo4j/FalkorDB Cypher, GraphML, SVG; MCP stdio server (`--mcp`).
- Core's `/impact` and `/refactor-plan` use the graph automatically when `graphify-out/` exists and fall back to ripgrep when it doesn't.

**How to work with it.**
1. Enable `graphify-pack` for the project; run `/graphify` once in the repo to build the graph.
2. **Dashboard sidebar → TOOLS → Graphify**: pick the project — the static `graph.html` visualization renders inline (served read-only by the daemon from `graphify-out/`). If only `graph.json` exists you'll get a hint to rebuild without `--no-viz`; graphs over 5000 nodes need `GRAPHIFY_VIZ_NODE_LIMIT` raised.
3. Keep it fresh after refactors: `graphify update .` (add `--force` after mass deletions).

---

## architecture-pack — repo-wide architecture map

**What it is.** The `/architecture-map` skill: produces `architecture-out/architecture-map.json` — a machine-readable contract with named layers, modules, and file-anchored end-to-end flows — plus a self-contained `architecture-map.html` viewer.

**Requires.** No extra CLI. Works standalone; when `graphify-out/graph.json` is present the skill uses it as a curated grouping source and falls back to direct repo exploration if it trails HEAD.

**What it can do.**
- **Freshness-stamped, incremental refresh** — stores `analyzedAtCommit`; on re-run it re-describes only the modules touched since the last commit, keeping everything else.
- **Machine contract** — `architecture-map.json` (schema v1) is consumed by agents that need a repo-wide mental model: layers, modules with `keyFiles`/`exports`/`dependencies`, and 5–10 named flows.
- **Self-contained viewer** — `architecture-map.html` is rendered by the bundled `scripts/build.sh`; never write HTML by hand.
- **Artifacts** land in `architecture-out/` (git-ignored by convention).

**How to work with it.**
1. Enable `architecture-pack` for the project (plugins card or `settings.json`).
2. Run `/architecture-map` — or let any orchestration agent trigger it when a fresh map is needed.
3. **Dashboard sidebar → TOOLS → Architecture**: the swarmery daemon serves `architecture-map.json` and `architecture-map.html` read-only; the page embeds the viewer or shows a "run /architecture-map first" hint when the artifacts are absent.

---

## uav-pack — UAV / drones

**What it is.** Domain pack for drone platforms: MAVLink integration, mission planning, embedded and edge runtimes.

**Inside.** Agents `mavlink-specialist`, `telemetry-processor`, `embedded-systems`, `edge-python-specialist`; skills for MAVLink integration, mission creation, embedded systems.

**Use it when** a project speaks MAVLink-style telemetry, plans missions, or ships code to flight-adjacent edge hardware. Enable per project; the agents pick up device/topology specifics from `project.json → domainTerms`.

---

## iot-pack — IoT / wearables

**What it is.** Domain pack for device fleets: BLE communication, device telemetry ingestion, health-metrics processing.

**Inside.** Agent `iot-data-specialist` plus telemetry/BLE skills.

**Use it when** the project ingests wearable/sensor data or talks BLE. Same per-project enablement flow.

---

## web-pack — web / marketing

**What it is.** Domain pack for public-facing web: SEO optimization, i18n coverage, landing-page CRO, Figma-to-code styling.

**Inside.** Agents `seo-specialist`, `i18n-specialist`, `landing-page-specialist`; a styling command.

**Use it when** the project has marketing pages, localization, or design-system-to-code work.

---

## infra-pack — infrastructure & delivery

**What it is.** Domain pack for platform work: Kubernetes/Helm deployment, GitOps promotion, IaC, GitLab CI and GitHub Actions pipelines, GCP & AWS CI auth, Keycloak IdP operations.

**Inside.** Agents `helm-deployment`, `gitlab-ci-specialist`, `keycloak-specialist`; 11 skills (`kubernetes-deployment`, `helm-chart-expert`, `gitops-promotion`, `release-promotion`, `infrastructure-as-code`, `gitlab-ci-cd`, `github-actions-cicd`, `gcp-cicd-auth`, `aws-cicd-auth`, `keycloak`, `deployment`).

**Use it when** the project owns manifests, pipelines, or identity config. Cloud/env specifics come from `project.json → cloud`.

---

## claude-eng-pack — Claude engineering

**What it is.** Meta pack: best-practice skills for building and auditing Claude agent systems themselves.

**Inside.** Skills `auditing-agent-architecture`, `designing-tools-and-mcp`, `engineering-prompts-and-output`, `configuring-claude-code`, `managing-context-reliability`.

**Use it when** the work is about agents, prompts, MCP servers, or Claude Code configuration — e.g. developing this marketplace, or any consumer project growing its own local agents before graduating them.

---

## Quick reference

| Plugin | Kind | Needs on the machine | Dashboard surface |
|---|---|---|---|
| core | mandatory baseline | — | everything (sessions, plugins card, …) |
| lsp-pack | tool (Serena MCP) | `serena` (uv) | sidebar **Serena** page: start/stop + embedded dashboard |
| graphify-pack | tool (knowledge graph) | `graphify` CLI (uv) | sidebar **Graphify** page: embedded `graph.html` |
| graft-pack | tool (context graph) | `graft` CLI (npm) | sidebar **Graft** page: index size + freshness |
| architecture-pack | tool (architecture map) | — | sidebar **Architecture** page: embedded `architecture-map.html` |
| uav-pack | domain | — | — |
| iot-pack | domain | — | — |
| web-pack | domain | — | — |
| infra-pack | domain | — | — |
| claude-eng-pack | meta | — | — |

The TOOLS section of the sidebar appears only while at least one project has the corresponding pack enabled.
