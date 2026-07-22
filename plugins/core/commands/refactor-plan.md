---
description: Generate comprehensive refactoring plan with impact analysis
color: red
---

# Refactoring Plan

Produce a structured, plan-only refactoring document -- current state analysis, cross-repo impact, step-by-step migration order, risk assessment, effort estimate, and rollback plan. No code changes are made.

Follow the playbook in `skills/refactor-plan/SKILL.md` (auto-loaded skill `refactor-plan`); apply it to $ARGUMENTS if provided.

For the **cross-repo impact** section, use Graphify rather than grep: `graphify affected "<symbol>"`
(blast radius), `graphify path "<A>" "<B>"` to prove a specific dependency, and
`graphify explain "<symbol>"` for the node's neighborhood — each repo has its own graph at
`<repo>/graphify-out/graph.json`, so run per repo (or pass `--graph` explicitly).
If the staleness hook reports the graph is behind HEAD, run `graphify update .` first.
For anything not in the graph (e.g. `devops/*` if unindexed) — use `rg` there.

To execute pure-function refactors directly, use the `functional-design` skill instead.
