---
name: architecture-map
description: Generate or refresh the repo-wide architecture map — architecture-out/architecture-map.json (machine contract with named flows) + architecture-map.html (self-contained viewer). Use when the user asks for an architecture map, repo map, "/architecture-map", or when an agent needs a fresh machine-readable architecture overview. NOT for per-epic C4 deep-dives (use c4-architecture-docs) and NOT for building the knowledge graph itself (use /graphify).
docs:
  status: reviewed
  source_sha: ff1a7f56a769
  updated: 2026-08-06
---

# Architecture Map

Produce `<repo>/architecture-out/architecture-map.json` + `.html`. The JSON is the
source of truth; the HTML is rendered from it by `scripts/build.sh` — never write
HTML by hand.

## 0. Freshness gate (always first)

Which of the two shapes you are in decides everything below: a **single repo**
(the working directory is itself a checkout — `.git` exists) or a **multi-repo
workspace** (no `.git` at the root; the member checkouts are listed in
`.claude/project.json` → `repos[]`, and module paths are workspace-relative,
`<repo>/<path-inside-it>`).

### Single repo

```bash
HEAD=$(git rev-parse HEAD)
LAST=$(node -e "try{const v=JSON.parse(require('fs').readFileSync('architecture-out/architecture-map.json')).analyzedAtCommit;console.log(typeof v==='string'&&v.length>=7?v:'')}catch{console.log('')}")
```

- `LAST == HEAD` → report "architecture map is up to date (commit <short>)" and STOP.
- `LAST` non-empty → **incremental mode**: `git diff --name-only $LAST..HEAD` →
  map changed paths onto existing `modules[].path` prefixes; re-describe ONLY
  touched modules, re-check only flows whose steps reference them; keep the rest.
- `LAST` empty → full analysis.

### Multi-repo workspace

There is no single HEAD to compare against, so compare PER MEMBER against the
map's optional `analyzedAtCommits` object (`{"<repo>": "<sha>"}`):

```bash
# One member per LINE — a member directory may contain spaces.
node -e "try{const r=JSON.parse(require('fs').readFileSync('.claude/project.json')).repos;if(Array.isArray(r))console.log(r.join('\n'))}catch{}" |
while IFS= read -r repo; do
  [ -n "$repo" ] || continue
  [ -e "$repo/.git" ] || { echo "$repo: not a checkout — skipping"; continue; }
  head=$(cd "$repo" && git rev-parse HEAD)
  last=$(node -e "try{const m=JSON.parse(require('fs').readFileSync('architecture-out/architecture-map.json')).analyzedAtCommits||{};const v=m[process.argv[1]];console.log(typeof v==='string'&&v.length>=7?v:'')}catch{console.log('')}" "$repo")
  echo "$repo $last $head"
done
```

- Every member reports `last == head` → up to date, STOP.
- Some members moved → **incremental mode for those members only**: diff each one
  with `(cd "$repo" && git diff --name-only "$last".."$head")` and prefix every
  path with `<repo>/` before matching it against `modules[].path`. Members whose
  `last == head` are left alone.
- **A member whose `last` is EMPTY gets a FULL analysis of that member**, even when
  other members are merely incremental. Never diff it: an empty `last` makes the
  range `..$head`, which git reads as `HEAD..$head` and reports as *no changes* — so
  a newly added member (or one that was unreadable last run and therefore omitted
  from the stamp per step 5) would be silently treated as analysed-and-current and
  stamped as such in step 5, freezing it out of every future run. A member you have
  no baseline for is unknown, not unchanged.
- No `analyzedAtCommits` at all (every `last` empty) → full analysis. An older map
  carries one scalar `analyzedAtCommit` that cannot say WHICH member it belongs
  to, so it is not a usable baseline — writing `analyzedAtCommits` in step 5 is
  what makes the next run incremental.
- A declared member with no `.git` is reported and skipped, never silently
  dropped: a member you could not read is unknown, not unchanged.

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
`analyzedAtCommit` = HEAD).

In a **multi-repo workspace** also write `analyzedAtCommits` — `{"<repo>":
"<sha>"}` for every member you actually analysed, using the HEAD you read in
step 0. `analyzedAtCommit` stays required: set it to the main app's member HEAD
(or the first member you resolved) so older consumers keep working.
`schemaVersion` stays `1` — the field is optional and additive. Omit members you
could not read rather than stamping a guess: a wrong stamp makes a stale repo
report as current forever.

```jsonc
{
  "schemaVersion": 1,
  "analyzedAt": "2026-09-08",
  "analyzedAtCommit": "<mainApp member HEAD>",
  "analyzedAtCommits": { "<repo-a>": "<sha>", "<repo-b>": "<sha>" }
}
```

Then:

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

# How to use

## What it does

This skill builds a whole-repository architecture map: a machine-readable JSON file with layers, modules, and named end-to-end flows, plus a self-contained HTML viewer rendered from that JSON. It reads the repository as it actually is — manifests, entry points, real file paths — so the map describes shipped code rather than an idealized design. Re-running it is cheap: if the map is already stamped with the current commit it stops, and if it trails behind it only re-describes the modules the diff touched.

## When to use it

- You want a repo-wide picture of layers, modules, and how a request travels across them.
- Someone new needs to answer "what happens when X" without reading every directory.
- The map exists but the repo has moved on, and you want it refreshed against the current commit.
- Another agent needs a machine-readable architecture overview to plan against.

## When not to use it

- You need a deep C4 breakdown of one epic or feature — use the `c4-architecture-docs` skill instead.
- You need the underlying knowledge graph built or queried — use the `graphify` skill.
- You only want to render an existing Mermaid diagram — use the `mermaid-viewer` skill.

## How to invoke

```
Skill(skill: "architecture-pack:architecture-map")
```

Run it from the repository root you want mapped; everything else is discovered from the repo itself.

## Inputs

- Repository — the working directory the skill runs in — required. Either a git checkout (the map is stamped with its `HEAD`) or a multi-repo workspace whose member checkouts are declared in `.claude/project.json` `repos[]` (the map is stamped per member, and `.claude/project.json` stops being optional).
- `.claude/project.json` — project name, repos, stack, domain terms — optional for a single repo, REQUIRED for a multi-repo workspace since `repos[]` is the only list of members there is.
- `graphify-out/graph.json` — an existing knowledge graph — optional, used only as candidate module groupings.

## What you get back

Two files under `architecture-out/` in the repository: `architecture-map.json` (the source of truth, schema version 1, stamped with the analysis date and commit) and `architecture-map.html` (a self-contained viewer built from that JSON). The JSON is validated before the HTML is rendered, and every validator error is fixed first. The final message reports module and flow counts, the commit stamp, and both artifact paths.

## Worked example

```
Skill(skill: "architecture-pack:architecture-map")

→ freshness gate: stored commit 4f2a19c, HEAD 9bc7e01 → incremental mode
→ 6 layers, 23 modules re-checked against the diff, 8 named flows
→ validate.mjs passed, viewer rendered

architecture-out/architecture-map.json
architecture-out/architecture-map.html
```

Open the HTML file in a browser to walk the layers left to right and step through each flow with its file anchors.

## Related

- `c4-architecture-docs` — when the subject is one epic or feature, not the whole repository.
- `graphify` — when you need the knowledge graph itself, or want to query relationships directly.
- `impact` — when the question is which code a specific change ripples into.
