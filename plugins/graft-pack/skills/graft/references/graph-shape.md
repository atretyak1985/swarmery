# The on-disk graph, and the two numbers the hooks gate on

Verified against `@nanonets/graft` **0.16.0** by building an index and reading it
back. Both hooks in this pack depend on these shapes; if a graft upgrade changes
one, `scripts/tests/graft-blast.test.sh` and `graft-prompt-context.test.sh` are
where it shows up.

## `<graphDir>/.graph/wiring.json`

`graphDir` is `graft` unless the project's `.claude/project.json` sets
`graft.graphDir`. The file is one JSON object:

```json
{
  "meta":  { "version": 1, "nodeCount": 7, "edgeCount": 10, "languages": ["typescript"], "scopes": [] },
  "nodes": [ … ],
  "edges": [ … ]
}
```

A **node** is a file or a symbol:

```json
{ "id": "src/a.ts",        "name": "a.ts",  "kind": "file",     "path": "src/a.ts", "span": "L1-L3", "signature": null,   "exported": true, "origin": "ast", … }
{ "id": "src/a.ts#alpha",  "name": "alpha", "kind": "function", "path": "src/a.ts", "span": "L1-L1", "signature": "function alpha(n: number): number", … }
```

The id of a symbol node is `<path>#<name>`, so `path` is the field that groups a
file's nodes together — that grouping is exactly the per-file blast radius.

An **edge** is `{source, target, relation, confidence}` with three relations:

| relation | meaning | note |
|---|---|---|
| `contains` | file → symbol it declares | structural, not a dependency |
| `imports` | file → file | |
| `calls` | symbol → symbol | |

`confidence` is `extracted` (from the syntax tree) or `inferred` (resolved
heuristically). Cross-file `calls` edges are frequently `inferred`.

### How `graft-blast.sh` reads it

For an edited file `P`: collect every node with `path == P` into an id set, take
the edges whose `target` is in that set, drop `relation == "contains"` (a file
containing its own symbols is not a dependent), drop edges whose source also
lives in `P` (intra-file), and report the distinct source paths. That is "what
depends on this file", capped at `graft.maxDependents`.

The hook parses this file directly rather than shelling out to `graft callers`
for two reasons: `callers` is symbol-scoped and has no `--file` flag, and a
read-only JSON parse cannot trigger a rebuild on the interactive path.

## `graft ask --json`

`ask` answers in one of two **modes**, and they do not share a response shape.
The hook's gate turns on telling them apart.

### Lexical mode — a ranked search over the corpus

```json
{ "query": "…", "mode": "…", "hits": [ … ], "coverage": 0.1395, "coverageStrong": 0.1395 }
```

Each hit: `{ kind, title, pointer, snippet, score }`. `pointer` is
`path:LSTART-LEND`; `title` is `<name> · <kind>`.

`coverage` and `coverageStrong` measure how much of the query the graph actually
answers — `coverageStrong` counts only strong symbol-NAME matches.

### Structural mode — a resolved symbol

```json
{ "query": "what does gamma call", "mode": "structural", "subject": "gamma",
  "hits": [ { "kind": "callee", "title": "alpha", "pointer": "src/a.ts:L1-L1",
              "snippet": "…", "relation": "calls", "score": 1 } ],
  "note": "outgoing edges from gamma" }
```

There are **no coverage fields at all**, and a `subject` and `note` appear
instead. This is not a degraded answer — it is graft's most certain one: the
query resolved to a real node and the hits are its actual edges.

## The gate `graft-prompt-context.sh` applies

Reproduced from graft's own `relevantRetrieval()` (`dist/claude/format.js`) so a
project that calibrated under `graft init` sees the same behaviour under the pack:

```
if (neither coverage nor coverageStrong is a number)  → inject   # structural
else if (coverageStrong >= 0.1 || coverage >= 0.5)    → inject   # lexical
else                                                  → nudge
```

Both numbers were read from the installed CLI, not from a spec:
`STRONG_FLOOR = 0.1` and `HIGH_FLOOR = 0.5` are exported from
`dist/ask/fuse.js` in **0.16.0**. Override them per project with
`SWARMERY_GRAFT_STRONG_FLOOR` / `SWARMERY_GRAFT_HIGH_FLOOR` if a graft upgrade
moves them before the pack catches up.

The structural branch matters more than it looks. Scoring a missing `coverage`
as `0` would send every resolved-symbol answer down the weak path — suppressing
exactly the answers graft is most confident about. Graft's own source says why
the fields are absent: *"the resolved intent is itself the relevance signal"*.

Below the gate the hook says nothing about *content* — it has nothing
trustworthy to say. It emits a short nudge to run `graft ask … --source` before
grepping, at most twice in a session. Two rather than one: the first is often
read past while the agent is mid-plan, and a nudge repeated on every prompt is a
context tax nobody acts on.

## Not reproduced: graft's novelty gate

Graft's own hook also drops hits whose `pointer` was already injected earlier in
the session, keeping a rolling list of shown pointers in its session state. This
pack does not, so two similar prompts can inject overlapping locators. It is a
deliberate omission, not an oversight — the pack's marker files hold a single
counter, and carrying a pointer list would make them a state store. Worth adding
if repeat injection turns out to cost anything measurable.
