# agentry

> The craft of agents. A vendor-neutral, multi-project **Claude Code** agent framework, distributed as a
> [plugin marketplace](https://code.claude.com/docs/en/plugins). One shared **`core`** plus
> opt-in **domain packs** — a framework improvement is published once and adopted by every
> consumer project via `/plugin update`, instead of being hand-ported between them.

## Why this exists

Copying an agent system between projects with token-rewrites rots fast: mis-substitutions pile up,
files drift, and every improvement has to be ported N times. agentry replaces that with the native
Claude Code plugin/marketplace mechanism: **semver-versioned**, **namespaced** (`core:tech-lead`),
and **updatable** (`/plugin update`). Projects pin a known-good version and adopt on bump —
controlled blast-radius, no more manual porting.

## Layout

```
.claude-plugin/marketplace.json   # this repo is a marketplace
plugins/
  core/                           # vendor-neutral: generic agents, skills, commands, hooks, CLI
  uav-pack/                       # UAV/drone domain: telemetry protocols, mission planning, embedded
  iot-pack/                       # IoT domain: BLE, device telemetry, health metrics
  web-pack/                       # marketing: SEO, i18n, landing CRO, figma-style
overlays/
  _schema/project.schema.json     # per-project flavor config schema
  example/                        # sample overlay (project.json + settings snippet)
docs/                             # framework documentation (NEUTRALITY.md, ONBOARDING.md)
```

Each plugin holds its components (`agents/`, `skills/`, `commands/`, `hooks/`, `bin/`,
`templates/`) at its **root**; only `plugin.json` lives under `.claude-plugin/`.

## Consuming it (per project)

In a project's `.claude/settings.json` (see `overlays/example/settings.snippet.json`):

```jsonc
{
  "extraKnownMarketplaces": {
    "agentry": { "source": { "source": "github", "repo": "atretyak1985/agentry" } }
  },
  "enabledPlugins": {
    "core@agentry": true,
    "web-pack@agentry": true
  },
  "env": { "AGENT_PROJECT": "your-project" }
}
```

Then deploy your flavor config to `.claude/project.json` (schema in `overlays/_schema/`).
Project-specific agents in the project's `.claude/agents/` override plugin agents by name
(native base + overlay). Core agents, skills, and hooks read `project.json` at runtime for
repos, the main app, device/edge repo, cloud settings, and domain terms — nothing is baked in
(policy: `docs/NEUTRALITY.md`, checker: `scripts/scan-flavor.sh`).

## Design decisions

- **Framework ≠ workspace.** Work artifacts (plans/sessions/wiki) live in a separate private
  workspace repo, never here. The CLI (`plugins/core/bin/agent-work.sh`) resolves the workspace
  via `AGENT_PROJECT` + `AGENT_WORKSPACE_ROOT`.
- **Vendor-neutral core.** No consumer project is privileged; flavor is runtime config.
- **Explicit semver** in each `plugin.json`; consumers adopt on bump.
- **`core` + opt-in domain packs**; projects enable only the packs they need. Domain vocabulary
  is legitimate inside its own pack, forbidden in core.

## License

MIT
