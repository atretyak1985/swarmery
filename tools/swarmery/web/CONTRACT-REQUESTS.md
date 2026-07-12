# Contract change requests

`web/src/api/types.ts` is **frozen** (gate 05, tag `swarmery-contract-freeze-v1`).
Branch agents (A/ingest, B/frontend, C/metrics) must **not** edit it.

If your branch needs a contract change (new field, new endpoint shape, new WS
event), **append a request below** and keep working around it locally. Requests
are resolved at integration (step 10), where `types.ts` is updated once on the
integration branch.

Request format:

```
## <date> — <branch> — <short title>
- What: <field/type/endpoint to add or change>
- Why: <one or two lines>
- Proposed shape: <TypeScript snippet>
```

---

<!-- Append requests below this line. -->

## 2026-07-12 — feat/swarmery-metrics (wave C) — per-turn model for exact recost

- What: add `model: string | null` to `Turn` (backed by a new `turns.model` column).
- Why: cost is priced per turn, but the model id lives per API message (JSONL §6),
  not per session. Ingest prices with the exact per-message model; `swarmery recost`
  can only fall back to `sessions.model`, which drifts when a session switches
  models mid-file (rare, but real — e.g. fallback models). Persisting the model on
  the turn makes recost exact and lets the UI attribute cost per model.
- Proposed shape:
  ```ts
  export interface Turn {
    // …existing fields…
    model: string | null; // API message model; NULL for user turns
  }
  ```
