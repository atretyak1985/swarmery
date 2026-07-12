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
