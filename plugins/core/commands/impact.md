---
description: Cross-repo impact analysis — graph-aware (Graft, then Graphify) with a live ripgrep fallback
color: red
docs:
  status: reviewed
  source_sha: fc326bb79534
  updated: 2026-09-09
---

# Impact Analysis Command

Find everything affected by a change to: $ARGUMENTS

## Which path to take

Three sources, in this order. Take the first one the project actually has —
they are ordered by how directly each one answers "who depends on this", not by
preference:

| # | Source | Available when | Why it ranks here |
|---|---|---|---|
| 1 | **Graft** | `graft` on PATH **and** `<repo>/graft/.graph/wiring.json` exists | Call/import edges straight from the syntax tree, with a transitive walk built in. No model call, no API key, and the fastest of the three. |
| 2 | **Graphify** | `<repo>/graphify-out/graph.json` exists | A richer graph spanning docs and media as well as code, with community detection. Use it when there is no graft index, or to cross-check a graft answer on a repo that has both. |
| 3 | **ripgrep** | always | Text, not structure. It cannot see an indirect caller, but it also cannot go stale, and it reaches files no graph indexed (deploy manifests, YAML, generated code). |

Whatever the graph says, **finish with the ripgrep sweep** and reconcile. A graph
answers "what depends on this symbol"; ripgrep answers "where does this string
appear". A hit only ripgrep found is either a config reference the graph never
indexed or a caller the graph missed — both are worth knowing about, and both are
invisible if you stop at step 1.

## Primary path — Graft (graph-aware, cheapest, no key)

Graft keeps a wiring graph at `<repo>/graft/.graph/wiring.json`. Run from inside
the repo you are analyzing.

1. **Direct callers** — `graft callers "$ARGUMENTS" --depth 2` — everything that
   references the symbol within two hops. `d=1` breaks, `d=2` probably breaks.
2. **Full closure** — `graft callers "$ARGUMENTS" --depth all` — the whole
   connected blast radius. This is the one to run before a delete.
3. **The other direction** — `graft callers "$ARGUMENTS" --direction out` — what
   the symbol itself calls, when you need to know what a change might break
   *underneath* it rather than above it.
4. **Where it lives** — `graft ask "$ARGUMENTS" --source -n 5` — the definition
   and its immediate context inline, so you can read the signature you are about
   to change without opening files.
5. Weigh edge confidence exactly as below: `extracted` edges come from the AST
   (trust them); `inferred` edges were resolved heuristically (verify before
   calling them WILL BREAK).

> Narrow to one subtree with `--in <path>`, and add `--json` when you want to
> post-process rather than read. If `graft check` reports the index stale, a plain
> `graft build` is incremental — do it before trusting an empty result, because a
> stale graph's silence looks exactly like "nothing depends on this".

## Second path — Graphify (graph-aware, spans docs and media)

Graphify builds a per-repo knowledge graph at `<repo>/graphify-out/graph.json`. Each repo from
`project.json → repos` has its own graph — **run the commands from inside the repo you are
analyzing** (or pass `--graph <repo>/graphify-out/graph.json` explicitly).

1. **Blast radius** — `graphify affected "$ARGUMENTS" --depth 3` — reverse traversal listing
   everything that depends on the symbol (run per repo: the main app from `project.json → mainApp`,
   then the device/edge repo from `project.json → device` if the project has one).
2. **Connection trace** — `graphify query "what depends on $ARGUMENTS?"` for a BFS answer, or
   `graphify path "$ARGUMENTS" "<other symbol>"` to prove/disprove a specific dependency.
3. **Symbol context** — `graphify explain "$ARGUMENTS"` — the node, its neighbors, and its
   community, with file:line citations.
4. Weigh edge confidence: `EXTRACTED` edges come straight from the AST (trust them);
   `INFERRED` edges were model-resolved (verify before calling them WILL BREAK).

> If the staleness hook warns the graph is behind HEAD, run `graphify update .` first
> (add `--force` after refactors that deleted files) — otherwise the blast radius may
> omit new callers (a false "safe to change").

## Final path — ripgrep (always live; covers anything the graphs miss)

Use ripgrep whenever both graphs are stale or absent, and **always** as the closing
cross-check — to catch infra config (deploy manifests/YAML) and anything else no
index covers. Run from the workspace root, listing the repos from
`project.json → repos`:

```bash
rg -n --no-heading "$ARGUMENTS" \
  apps/<mainApp> <device-repo> \
  <infrastructure-repo>
```

## What to report

1. Total occurrences + per-repo breakdown.
2. File paths with line numbers and surrounding context.
3. For graph results: depth (`d=1` WILL BREAK / `d=2` LIKELY / `d=3` MAYBE) and the
   edge confidence tag (`EXTRACTED` vs `INFERRED`). Name which source produced each
   section — a graft hit and a ripgrep hit are not the same kind of evidence, and a
   reader who cannot tell them apart cannot weigh them.
4. Recommended update order if the symbol changes (interfaces → impls → callers → tests).
5. **Cross-tier flag:** if the symbol crosses the main app ↔ the device/edge repo, call out the
   manual contract (no shared schema) and the coordinated-merge requirement.

## Response format

```markdown
## Impact Analysis for "$ARGUMENTS"

### Summary
- Total occurrences: X · Repositories affected: Y · Source: Graft graph / Graphify graph / ripgrep

### <mainApp> (Z)
- d=1 (WILL BREAK): src/app/api/things/route.ts:45 — POST handler [calls, EXTRACTED]

### <device repo> (N)
- src/send_data.py:78 — payload["status"] = order_status

### Recommendations
- [update order + tests to run]
```

Now analyze impact of: $ARGUMENTS

# How to use

## What it does

Finds everything that would break if you change a symbol, across every repository in your project. It takes the most structural source the project has — the Graft wiring graph first, then a Graphify knowledge graph — for a real dependency traversal, and always closes with a live ripgrep sweep so config files and anything the index missed still show up. You get one report with per-repo hits, a break-likelihood rating, the source behind each section, and the order to update things in.

## When to use it

- You are about to rename or change the signature of a function, type, or field and need the full caller list first.
- A change touches a shared contract between the main app and a device or edge repo, and you need to know whether both sides move together.
- You want to prove or disprove that one symbol actually reaches another before assuming a dependency exists.
- A reviewer asks "what else does this affect?" and grep alone keeps missing indirect callers.

## When not to use it

- You just want to locate a string or file — use `/search` or `/find`, which are faster and do not build a report.
- You need a full architecture overview rather than one symbol's blast radius — use `/architecture-map`.
- You already have the affected list and want a change sequence written up — use `/refactor-plan`.

## How to invoke

```
/impact createOrder
```

Type the command followed by the symbol you are changing. Run it from inside the repo you want analyzed so its per-repo graph resolves — `graft/.graph/wiring.json` for graft, `graphify-out/graph.json` for graphify. With neither index present the ripgrep sweep still covers the repos listed in your project config, so the command always returns something.

## Inputs

- **symbol** — the function, type, field, or identifier you are about to change — required. Anything you can name in code works; the more specific the name, the tighter the result.

## What you get back

A single markdown report in the chat. It opens with a summary line (total occurrences, repositories affected, and which of the three sources produced the result), then a section per repository listing file paths with line numbers and context. Graph-sourced hits carry a depth rating — `d=1` WILL BREAK, `d=2` LIKELY, `d=3` MAYBE — and an edge-confidence tag (`EXTRACTED` from the syntax tree, `INFERRED` by a model or a heuristic and worth verifying). The report closes with a recommended update order: interfaces, then implementations, then callers, then tests. Nothing is written to disk and no files are edited.

## Worked example

```
/impact OrderStatus

→ ## Impact Analysis for "OrderStatus"
  ### Summary
  - Total occurrences: 14 · Repositories affected: 2 · Source: Graft graph + ripgrep sweep
  ### apps/<mainApp> (11)
  - d=1 (WILL BREAK): src/orders/line-items/route.ts:45 — POST handler [calls, EXTRACTED]
  ### <device repo> (3)
  - src/send_data.py:78 — payload["status"] = order_status
  ### Recommendations
  - Update the shared enum, then the route handler, then the contract tests.
  - Cross-tier: the device repo has no shared schema — coordinate the merge.
```

You end up knowing which eleven call sites break immediately, which three live behind a hand-maintained contract in another repo, and that the two changes have to land together.

## Related

- `/search` — plain ripgrep across repos when you want matches, not analysis.
- `/refactor-plan` — turns a known blast radius into a sequenced refactor plan.
- `/graphify` — build or refresh the Graphify knowledge graph this command reads from.
- `graft build` — build or refresh the Graft wiring graph this command prefers (graft-pack).
