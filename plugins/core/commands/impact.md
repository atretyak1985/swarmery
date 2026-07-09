---
description: Cross-repo impact analysis — graph-aware (GitNexus) with a live ripgrep fallback
color: red
---

# Impact Analysis Command

Find everything affected by a change to: $ARGUMENTS

## Primary path — GitNexus (graph-aware, most accurate)

GitNexus indexes the Tier-1 code repos (see `project.json → repos`). **Pass `repo` explicitly** —
more than one repo may be indexed, so it cannot be inferred. Read `skills/gitnexus` for the full model.

1. **The main app** (`project.json → mainApp`) — `gitnexus_impact({ repo: "<mainApp>", target: "$ARGUMENTS", direction: "upstream", maxDepth: 3 })`
2. **The device/edge repo** (`project.json → device`, if the project has one) — `gitnexus_impact({ repo: "<device>", target: "$ARGUMENTS", direction: "upstream", maxDepth: 3 })`
3. If `$ARGUMENTS` is an **API route in the main app**, prefer
   `gitnexus_api_impact({ repo: "<mainApp>", route: "$ARGUMENTS" })` — consumers + response
   shape + middleware in one report.
4. Map uncommitted changes — `gitnexus_detect_changes({ repo: "<repo>", scope: "all" })`.

> If the staleness hook warns the index is behind HEAD, run `/reindex-gitnexus` first —
> otherwise the blast radius may omit new callers (a false "safe to change").

## Fallback path — ripgrep (always live; covers infra config which GitNexus does NOT graph)

GitNexus does not graph infrastructure config (deploy manifests/YAML/Terraform). Use ripgrep
there, and whenever the index is stale or unavailable. Run from the workspace root, listing
the repos from `project.json → repos`:

```bash
rg -n --no-heading "$ARGUMENTS" \
  apps/<mainApp> <device-repo> \
  <infrastructure-repo>
```

## What to report

1. Total occurrences + per-repo breakdown.
2. File paths with line numbers and surrounding context.
3. For graph results: depth (`d=1` WILL BREAK / `d=2` LIKELY / `d=3` MAYBE) and confidence.
4. Recommended update order if the symbol changes (interfaces → impls → callers → tests).
5. **Cross-tier flag:** if the symbol crosses the main app ↔ the device/edge repo, call out the
   manual contract (no shared schema) and the coordinated-merge requirement.

## Response format

```markdown
## Impact Analysis for "$ARGUMENTS"

### Summary
- Total occurrences: X · Repositories affected: Y · Source: GitNexus graph / ripgrep

### <mainApp> (Z)
- d=1 (WILL BREAK): src/app/api/things/route.ts:45 — POST handler [CALLS, 100%]

### <device repo> (N)
- src/send_data.py:78 — payload["status"] = order_status

### Recommendations
- [update order + tests to run]
```

Now analyze impact of: $ARGUMENTS
