# Extending swarmery: where project-specific things live

swarmery is **one global system + a thin native overlay per project** — never a separate
"sub-agent-system" inside a project. Claude Code merges both layers in every session:
enabled plugins supply the global components; the project's own `.claude/` supplies local
ones; **on a name collision the project-local component wins** (native base + overlay).

## Decision tree for every new skill / agent / command / template / script

| The thing is… | It goes to… |
|---|---|
| useful to any project | `plugins/core` (bump semver → consumers adopt via `/plugin update`) |
| useful to ≥2 projects of one domain or capability | the matching pack — domain (`uav-pack` / `iot-pack` / `web-pack`) or capability (`infra-pack` / `lsp-pack`) |
| unique to one project | **the project's own `.claude/{agents,skills,commands,templates}/`** — versioned with the project's code, because it evolves with the product |
| configuration, not logic (repo lists, env names, commit scopes, domain nouns) | the project's `.claude/project.json` |

## The graduation rule (flow goes UP only)

New things are born **project-local**. When a *second* project needs the same thing, promote it
to a pack; when every project needs it, promote to core. Never copy downward — copying framework
files into projects recreates the fork-and-sync rot this repo exists to eliminate.

Promotion checklist:
1. De-flavor it (see `docs/NEUTRALITY.md`) — values move to `project.json` reads.
2. Move the file into the pack/core; bump that plugin's semver.
3. Delete the project-local copy in the consumer that donated it (the plugin now supplies it).

## Overriding core behavior

A project may ship a component with the **same name** as a core one in its `.claude/agents/`
(etc.) — the local one wins in that project only. Use this for project-specific variants of a
core agent instead of forking the framework.

## Template & script resolution convention

- **Templates:** agents look in `${CLAUDE_PROJECT_DIR}/.claude/templates/` first, then fall back
  to the plugin's `${CLAUDE_PLUGIN_ROOT}/templates/`. Project-specific report/summary formats
  live with the project; generic ones ship with core.
- **Scripts:** project automation stays in the project repo (`scripts/` or `.claude/scripts/`).
  Core ships only project-agnostic tooling (`plugins/core/bin/`), which reads `project.json`
  for anything project-shaped.

## What a consumer project's `.claude/` looks like

```
<project>/.claude/
├── settings.json        # enables core@swarmery + the packs it needs (+ AGENT_PROJECT env)
├── project.json         # the flavor config (schema: overlays/_schema/project.schema.json)
├── agents/              # project-unique agents + intentional overrides (often empty)
├── skills/              # project-unique skills (often just a few)
├── templates/           # project-specific templates (checked before plugin templates)
└── scripts/             # project automation (optional)
```

Thin is healthy: if this directory starts growing generic content, something wants promoting.

## Versioning the overlay itself

A single-repo project versions `.claude/` naturally — it lives inside the project repo.
**Multi-repo workspaces** (no single root repo) should make the overlay its own small repo:

```
<workspace>/agents/        ← git repo holding ONLY the project-specific overlay
<workspace>/.claude -> agents   (symlink; sub-repo .claude symlinks point here too)
```

This keeps rules/, project-specific skills/agents/commands, agent-memory and project.json
under version control and shareable across machines, while the generic framework still
arrives via plugins. If the workspace previously hosted a full pre-swarmery agent framework
repo, reuse it: push the thin overlay as a successor branch — history stays, layout shrinks.
