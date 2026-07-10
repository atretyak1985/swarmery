# Onboarding a project onto swarmery

## The one-command way

From the new project's root:

```bash
bash <swarmery-repo>/scripts/init.sh <project-slug> [pack ...]
# e.g.  bash /Volumes/Work/swarmery/scripts/init.sh my-shop web-pack
```

It scaffolds `.claude/settings.json` (marketplace + core + chosen packs + env + safety denies),
a `.claude/project.json` skeleton (fill the TODOs), and the workspace namespace. Then start a
fresh session and accept the trust prompt. Idempotent — existing files are never overwritten.

**Optional, once per machine:** register the marketplace at user level (`~/.claude/settings.json`
→ same `extraKnownMarketplaces` block) so every project on the machine already knows `swarmery`
and per-project settings shrink to `enabledPlugins` + `env`.

The manual steps below describe what init.sh does, for when you need to customize.

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
  "extraKnownMarketplaces": { "swarmery": { "source": { "source": "github", "repo": "atretyak1985/swarmery" } } },
  "enabledPlugins": { "core@swarmery": true, "<pack>@swarmery": true },
  "env": { "AGENT_PROJECT": "<project>", "AGENT_WORKSPACE_ROOT": "/path/to/swarmery-workspace" }
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
This is the whole reason swarmery exists — verify it explicitly once ≥2 consumers are live.
