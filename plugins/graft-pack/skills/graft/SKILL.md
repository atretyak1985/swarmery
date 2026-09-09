---
name: graft
description: "Use when you need to locate, understand, or assess the blast radius of code in a repo that has a graft/ index — \"where is X handled\", \"what does this file expose\", \"who calls this before I rename it\", or first-time orientation in an unfamiliar tree. Answers come from the context graph as ranked file:line hits instead of a grep sweep. NOT for editing code, and NOT for repos with no graft/ index (build one first, or use /search)."
docs:
  status: draft
  updated: 2026-09-09
---

# graft

`graft` is a context graph over the repo: every file and symbol as a node, `calls`/`imports`/`contains` as edges, kept next to the code in `graft/`. Its whole point is that finding code should cost one ranked answer, not a widening sequence of greps.

Requires the `graft` CLI on the machine (`npm i -g @nanonets/graft`). No API key and no model call for anything below — every command here is a local graph read.

## Pick the command from the question

| The question you actually have | Command |
|---|---|
| "How does X work?" / "Where is X handled?" | `graft ask "<question>" --source -n 5` |
| "Every place this string/pattern appears" | `graft grep "<pattern>"` |
| "What does this file expose?" | `graft skeleton <path>` |
| "Who calls this?" — before a rename or delete | `graft callers <symbol> --depth 2` |
| "What does this call?" | `graft callers <symbol> --direction out` |
| "I have never seen this repo" | `graft map` |

`--source` is the flag that matters on `ask`: it inlines the code at each hit, so the
answer replaces opening the files rather than pointing at them. Reach for `--full`
only when a crux excerpt genuinely cut something you need.

## Rules

1. **No index, no graft.** If `graft/` is absent, say so and use `/search`; do not build one mid-task without asking.
2. **`ask` before `grep`.** `ask` is ranked and graph-aware; `grep` is exhaustive and unranked. Use `grep` when you need *every* occurrence, not to start a hunt.
3. **Depth is a claim about risk.** On `callers`, `d=1` breaks, `d=2` probably breaks, deeper is a maybe. `--depth all` gives the full closure for a delete.
4. **Confidence is not decoration.** `extracted` edges come from the syntax tree; `inferred` edges were resolved heuristically — verify one before calling it a break.
5. **A stale graph lies by omission.** New callers it never saw read as "safe to change". `graft check` fails on staleness; a plain `graft build` is incremental.
6. Never report tokens saved. The graph is how you work, not a result to announce.

Narrow any command to a subtree with `--in <path>`, and add `--json` when you need to
parse rather than read.

See `references/commands.md` for the full flag surface and `references/graph-shape.md`
for the on-disk format the hooks read.

# How to use

## What it does

Turns "where is this handled?" into one ranked answer with exact `file:line` locators, read from a context graph built from the repository's own syntax trees. It also answers who calls a symbol, what a file exposes, and what the repo's shape is — each from the same local graph, with no model call and no API key.

## When to use it

- You need to find code in a repo that has a `graft/` index and you would otherwise start grepping for a name you are guessing at.
- You are about to rename, change the signature of, or delete a symbol and need its callers before you touch it.
- You want a file's API surface without reading the file.
- You have just landed in an unfamiliar repository and need orientation before picking a starting point.

## When not to use it

- The repo has no `graft/` directory — there is no graph to read, so use `/search` instead.
- You want to edit code; this skill only reads.
- You need a rendered architecture diagram rather than locators — use `/architecture-map`.

## How to invoke

```
graft ask "where are inbound webhooks verified" --source -n 5
```

Run the CLI directly from the repository root. The skill is guidance for choosing the right subcommand, not a wrapper — there is no `/graft` command to type. The pack's two hooks call the same CLI for you on prompts and after edits.

## Inputs

- **question or symbol** — required. A plain-language question for `ask`, a bare or qualified symbol name for `callers`, a repo-relative path for `skeleton`, a regex for `grep`.
- **`--in <path>`** — optional path prefix that narrows matches to one subtree before scoring.
- **`--depth <n|all>`** — optional on `callers`; how many hops of blast radius to walk.

## What you get back

Ranked hits printed to the terminal, each with a `file:LSTART-LEND` pointer, the enclosing symbol's signature, and — with `--source` — the code itself, so the output is usually the answer rather than a lead. `callers` returns the dependents grouped by hop distance with an `extracted`/`inferred` confidence tag per edge. Every command takes `--json` for the same content as a parseable object. Nothing is written and no file is modified.

## Worked example

```
graft callers OrderStatus --depth 2

→ d=1  src/orders/route.ts:45     createOrder          [calls, extracted]
  d=1  src/orders/schema.ts:12    OrderPayload         [imports, extracted]
  d=2  src/api/webhooks.ts:88     handleStatusUpdate   [calls, inferred]
```

You learn before editing that two call sites break immediately, that a third is a
heuristic match worth opening, and that the rename is a three-file change rather
than the one-file change it looked like.

## Related

- `/impact` — full cross-repo blast-radius report; prefers `graft callers` when the CLI is present.
- `/search` — plain ripgrep, for repos with no graph.
- `/architecture-map` — rendered architecture overview rather than locators.
