# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## What this repo is

swarmery is a **Claude Code plugin marketplace** (`.claude-plugin/marketplace.json`), not an application. It ships one vendor-neutral **`core`** plugin plus opt-in domain packs (`uav-pack`, `iot-pack`, `web-pack`). Consumer projects enable plugins via their own `.claude/settings.json` and supply per-project flavor at runtime through `.claude/project.json` — nothing project-specific is ever baked into this repo.

There is no build step for the marketplace itself. "Source code" here is agent/skill/command markdown, bash hooks and CLI scripts, and JSON manifests.

**Exception — `tools/swarmery/`**: a Go + React control plane for monitoring Claude Code agent sessions (own module `github.com/atretyak1985/swarmery/tools/swarmery`, own build via `make build`, dedicated CI `.github/workflows/swarmery-ci.yml`). It is NOT a plugin and is excluded from marketplace rules (neutrality scan covers `plugins/**` only). The committed `tools/swarmery/docs/plan/` tree is the **historical record of already-shipped phases only** — NEW plans, specs, and design docs go to the private workspace (see "Work artifacts" below), never into this repo.

## Commands

Local equivalents of CI (`.github/workflows/ci.yml`):

```bash
# Validate all JSON manifests
node -e "JSON.parse(require('fs').readFileSync('<file>'))"   # marketplace.json, plugin.json, hooks.json, overlays/*.json

# Shell syntax check on all scripts
find plugins scripts -name '*.sh' -exec bash -n {} \;

# Neutrality scan — must report "✓ clean" (token patterns come from gitignored
# .flavor-tokens / .flavor-tokens-domain files or FLAVOR_BRAND / FLAVOR_DOMAIN env vars)
bash scripts/scan-flavor.sh
```

Agent evals (promptfoo golden tests for `tech-lead`, `commit-message`, `guardrail-checker` — not in CI, costs API tokens):

```bash
cd evals
export ANTHROPIC_API_KEY=…
npx promptfoo@latest eval        # run suite
npx promptfoo@latest view        # inspect results
```

CI also enforces that every `plugins/*/agents/*.md` has `name:` and `description:` frontmatter within the first 15 lines, starting with a `---` line.

## Layout

- `.claude-plugin/marketplace.json` — marketplace manifest listing all plugins.
- `plugins/<name>/.claude-plugin/plugin.json` — each plugin's manifest with **explicit semver** (bump it on any change so consumers adopt via `/plugin update`).
- `plugins/<name>/{agents,skills,commands,hooks,bin,templates}/` — components live at the plugin **root**, only `plugin.json` is under `.claude-plugin/`.
- `overlays/_schema/project.schema.json` — schema for consumers' `.claude/project.json`; `overlays/example/` is the reference overlay.
- `scripts/init.sh` — one-command consumer bootstrap (settings.json + project.json skeleton + workspace namespace).
- `plugins/core/bin/agent-work.sh` — project-aware workspace CLI (`setup|init|phase|complete|index|list|search|view|metrics|cleanup`). Resolves the workspace via `AGENT_WORKSPACE_ROOT` + `AGENT_PROJECT` env; work artifacts (plans/sessions/tasks) live in a separate private workspace repo, **never here**.

### Work artifacts (hard rule)

ALL new plans, specs, and design docs — including ones for `tools/swarmery` — are written to the private workspace repo, **date-grouped** like `working/`: `<workspace>/swarmery/workspace/plans/{YYYY}/{MM}/{DD}/{slug}/` (for this self-hosted repo the workspace is `/Volumes/Work/swarmery-workspace`). Never create planning artifacts under `tools/swarmery/docs/plan/` or anywhere else in this repo; the in-repo plan tree is a frozen record of shipped phases.
- `tools/swarmery/` — Go + React session-monitoring control plane (see exception note above): `cmd/`, `internal/{store,ingest,api}`, `web/`, `testdata/fixtures/`, `docs/{jsonl-format.md,plan/}`.

## Hard rules

### Vendor neutrality (docs/NEUTRALITY.md)

- **Brand tokens** (company/product names, internal repo names, env aliases, cloud regions) are forbidden **everywhere** in `plugins/**`.
- **Domain vocabulary** (drones, wearables, …) is legitimate only inside its own domain pack, forbidden in `core`.
- Scripts and hooks read flavor from `${CLAUDE_PROJECT_DIR}/.claude/project.json` at runtime; never hard-code paths or project names.
- Prose examples use neutral placeholders (`apps/<mainApp>`, `<device>`, `<envAlias>`) or neutral example domains (`orders/line-items`).
- Agent frontmatter identity is `swarmery-core`.
- `scripts/scan-flavor.sh` is the ratchet: the count must stay at zero.

### Graduation rule — flow goes UP only (docs/EXTENDING.md)

New components are born project-local (in a consumer's `.claude/`). When a second project needs one, promote it to a domain pack; when every project needs it, promote to `core`. Never copy framework files downward into projects. Promotion = de-flavor it → move into pack/core → bump that plugin's semver → delete the donor's local copy.

On a name collision, a consumer's project-local component overrides the plugin's — that's the intended override mechanism, not forking.

### Template resolution

Agents look in `${CLAUDE_PROJECT_DIR}/.claude/templates/` first, then fall back to `${CLAUDE_PLUGIN_ROOT}/templates/`. Generic templates ship with core; project-specific ones stay with the project.

## Self-hosting (dogfooding)

This repo is itself a swarmery consumer: `.claude/` is a **plain committed directory**
(no `agents/` repo + symlink — that pattern exists only for multi-repo consumer
workspaces sharing one overlay). `settings.json` enables `core@swarmery` from the
GitHub marketplace like any consumer; `project.json` sets `AGENT_PROJECT=swarmery`
(workspace: `swarmery-workspace/swarmery/`); the statusline runs straight from
source at `plugins/core/statusline/`.

Installed plugins come from the **cache** (`~/.claude/plugins/cache`), so local edits
to `plugins/**` are NOT what the session runs. To test in-progress plugin changes,
load them live for one session:

```bash
claude --plugin-dir plugins/core                 # repeatable: --plugin-dir plugins/infra-pack …
```

Never re-register the local checkout as a marketplace: `marketplace.json` `name` is
`swarmery`, and a local-path registration would **replace** the GitHub source globally,
breaking the `/plugin update` distribution path all consumers rely on.

## Conventions

- Conventional commits (`feat:`, `refactor!:`, `chore:`); semver bumps in `plugin.json` accompany plugin changes, with the marketplace `metadata.version` tracking the core version.
- Every real agent routing bug or output-contract regression should become a promptfoo test case in `evals/` (prefer `contains`/`regex` for hard contracts, `llm-rubric` for judgment calls).
