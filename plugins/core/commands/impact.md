---
description: Cross-repo impact analysis — graph-aware (Graphify) with a live ripgrep fallback
color: red
---

# Impact Analysis Command

Find everything affected by a change to: $ARGUMENTS

## Primary path — Graphify (graph-aware, most accurate)

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

## Fallback path — ripgrep (always live; covers anything the graph misses)

Use ripgrep whenever the graph is stale or absent, and to double-check infra config
(deploy manifests/YAML) if it is not in the graph. Run from the workspace root, listing
the repos from `project.json → repos`:

```bash
rg -n --no-heading "$ARGUMENTS" \
  apps/<mainApp> <device-repo> \
  <infrastructure-repo>
```

## What to report

1. Total occurrences + per-repo breakdown.
2. File paths with line numbers and surrounding context.
3. For graph results: depth (`d=1` WILL BREAK / `d=2` LIKELY / `d=3` MAYBE) and the
   edge confidence tag (`EXTRACTED` vs `INFERRED`).
4. Recommended update order if the symbol changes (interfaces → impls → callers → tests).
5. **Cross-tier flag:** if the symbol crosses the main app ↔ the device/edge repo, call out the
   manual contract (no shared schema) and the coordinated-merge requirement.

## Response format

```markdown
## Impact Analysis for "$ARGUMENTS"

### Summary
- Total occurrences: X · Repositories affected: Y · Source: Graphify graph / ripgrep

### <mainApp> (Z)
- d=1 (WILL BREAK): src/app/api/things/route.ts:45 — POST handler [calls, EXTRACTED]

### <device repo> (N)
- src/send_data.py:78 — payload["status"] = order_status

### Recommendations
- [update order + tests to run]
```

Now analyze impact of: $ARGUMENTS
