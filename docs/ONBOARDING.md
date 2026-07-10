# Onboarding a project onto agentry

## 1. Create the project's flavor config
Copy `overlays/example/` as a model:
- `project.json` — flavor config (schema: `overlays/_schema/project.schema.json`): repos, main app,
  device/edge repo, cloud settings, domain terms, commit scopes, enabled packs.
- `settings.snippet.json` — the `.claude/settings.json` block (marketplace + enabledPlugins + `env.AGENT_PROJECT`).

Keep the *real* filled-in configs in your project (or a private workspace repo) — not in this public repo.

## 2. Wire the project's `.claude/`
Merge the snippet into the project's `.claude/settings.json`:
```jsonc
{
  "extraKnownMarketplaces": { "agentry": { "source": { "source": "github", "repo": "atretyak1985/agentry" } } },
  "enabledPlugins": { "core@agentry": true, "<pack>@agentry": true },
  "env": { "AGENT_PROJECT": "<project>", "AGENT_WORKSPACE_ROOT": "/path/to/agentry-workspace" }
}
```
Deploy the flavor config to `<project>/.claude/project.json`.
Project agents in `.claude/agents/` override plugin agents by name (native base + overlay).

> **Cutover caution:** if the project previously ran a file-based copy of this agent system with
> hooks registered in its `settings.json`, remove that legacy hook wiring in the same change that
> enables the plugins — otherwise every hook fires twice. Do the switch in a fresh session.

## 3. Workspace
`agent-work.sh` reads `AGENT_PROJECT` + `AGENT_WORKSPACE_ROOT` and writes to
`<workspace-root>/<project>/workspace/…` automatically. Add the project dir under the workspace repo if new.

## The payoff test (prove porting is dead)
1. Bump `plugins/core` minor version; push.
2. In each consumer: `/plugin update`.
3. Confirm the change lands in every project with **zero per-project file copying**.
This is the whole reason agentry exists — verify it explicitly once ≥2 consumers are live.
