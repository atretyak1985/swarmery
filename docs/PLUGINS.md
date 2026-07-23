# Plugins

The swarmery marketplace ships one mandatory **core** plugin and eight opt-in packs. Everything here is vendor-neutral: project-specific flavor comes from each consumer's `.claude/project.json` overlay at runtime, never from the plugins themselves.

## Enabling and disabling

Per project, three equivalent ways:

1. **Control-plane dashboard** — open the project (`/projects/:id`) → **plugins** card → flip the toggle. The daemon performs merge-only surgery on the project's `.claude/settings.json` (`enabledPlugins["<pack>@swarmery"]`), backs the file up to `settings.json.bak`, and never overwrites a malformed file. `core` is locked there — its lifecycle (hooks, statusline, project.json) belongs to attach/detach.
2. **Bootstrap** — `scripts/init.sh` writes the initial `settings.json` with core plus the packs you pick.
3. **By hand** — add `"<pack>@swarmery": true` under `enabledPlugins` in the project's `.claude/settings.json`.

Changes take effect in the **next Claude Code session**: on startup Claude Code installs enabled packs from the marketplace into its plugin cache. Every pack requires `core`. Disabling a pack removes its key (same end state detach leaves).

---

## core — the mandatory baseline

**What it is.** The vendor-neutral agent-development framework: 41 agents, 29 skills, 16 commands, 16 hooks, plus the project-aware `agent-work` workspace CLI and the statusline.

**What it can do.**
- **Orchestration** — `@tech-lead` drives the 9-phase workflow (gap analysis → plan → implement → verify → retro) with executor agents: `implementation-agent`, `verification-agent`, `quality-checker`, `plan-reviewer`, `task-planner` / `implementation-planner`, `retrospective-agent`, and more.
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
| architecture-pack | tool (architecture map) | — | sidebar **Architecture** page: embedded `architecture-map.html` |
| uav-pack | domain | — | — |
| iot-pack | domain | — | — |
| web-pack | domain | — | — |
| infra-pack | domain | — | — |
| claude-eng-pack | meta | — | — |

The TOOLS section of the sidebar appears only while at least one project has the corresponding pack enabled.
