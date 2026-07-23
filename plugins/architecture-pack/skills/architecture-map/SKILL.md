---
name: architecture-map
description: Generate or refresh the repo-wide architecture map — architecture-out/architecture-map.json (machine contract with named flows) + architecture-map.html (self-contained viewer). Use when the user asks for an architecture map, repo map, "/architecture-map", or when an agent needs a fresh machine-readable architecture overview. NOT for per-epic C4 deep-dives (use c4-architecture-docs) and NOT for building the knowledge graph itself (use /graphify).
---

# Architecture Map

Produce `<repo>/architecture-out/architecture-map.json` + `.html`. The JSON is the
source of truth; the HTML is rendered from it by `scripts/build.sh` — never write
HTML by hand.

## 0. Freshness gate (always first)

```bash
HEAD=$(git rev-parse HEAD)
LAST=$(node -e "try{const v=JSON.parse(require('fs').readFileSync('architecture-out/architecture-map.json')).analyzedAtCommit;console.log(typeof v==='string'&&v.length>=7?v:'')}catch{console.log('')}")
```

- `LAST == HEAD` → report "architecture map is up to date (commit <short>)" and STOP.
- `LAST` non-empty → **incremental mode**: `git diff --name-only $LAST..HEAD` →
  map changed paths onto existing `modules[].path` prefixes; re-describe ONLY
  touched modules, re-check only flows whose steps reference them; keep the rest.
- `LAST` empty → full analysis.

## 1. Inventory (ground truth, no invention)

- `.claude/project.json` — name, repos, stack, domainTerms.
- Root `CLAUDE.md` — layout section, commands, hard rules → `conventions` + `importantNotes`.
- `graphify-out/graph.json` if present (nodes have `community`/`community_name`,
  top-level `built_at_commit`): communities are *candidate* module groupings,
  god nodes are *candidate* hubs. Curate — target 15–40 modules, never 1:1 with
  communities. If graphify's `built_at_commit` trails HEAD, note it in
  `importantNotes` and lean on direct exploration instead.
- Manifests (`package.json`, `go.mod`, `plugin.json`, workflow YAML) → techStack,
  entryPoints, externalServices.

## 2. Layers

Pick 3–7 layers that fit THIS repo (do not force presentation/domain/infra onto
a repo that is a plugin marketplace or a CLI). `order` = left-to-right viewer
columns, upstream (actors/entrypoints) first.

## 3. Modules (fan out)

Dispatch parallel read-only subagents, one per layer (or per module group for
big layers). Each returns, per module: `responsibility` (1–2 sentences),
`keyFiles` (3–7 real paths — verify each exists), `exports` (public surface:
commands, endpoints, functions), `dependencies` (ids of modules it imports/calls).
Real paths only — a file that does not exist is a hard failure.

## 4. Flows (the point of the map)

5–10 named end-to-end scenarios a developer actually asks about ("what happens
when X"). Each step: `from`/`to` module ids, `action`, `file` anchor (at least
one per flow), `payload` where meaningful. Prefer flows crossing ≥ 3 modules.

## 5. Synthesize + validate + render

Assemble the full JSON (`schemaVersion: 1`, `analyzedAt` = today,
`analyzedAtCommit` = HEAD). Then:

Bundled files (schema, validator, renderer) live under ${CLAUDE_PLUGIN_ROOT}/skills/architecture-map/ — never copy them into the project.

```bash
mkdir -p architecture-out
node "${CLAUDE_PLUGIN_ROOT}/skills/architecture-map/scripts/validate.mjs" architecture-out/architecture-map.json
bash "${CLAUDE_PLUGIN_ROOT}/skills/architecture-map/scripts/build.sh" \
  --json architecture-out/architecture-map.json \
  --out  architecture-out/architecture-map.html
```

Fix every validator error before rendering. Finish by reporting: module/flow
counts, commit stamp, and the two artifact paths.
