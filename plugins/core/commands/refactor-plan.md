---
description: Generate comprehensive refactoring plan with impact analysis
color: red
---

# Refactoring Plan

Produce a structured, plan-only refactoring document -- current state analysis, cross-repo impact, step-by-step migration order, risk assessment, effort estimate, and rollback plan. No code changes are made.

Follow the playbook in `skills/refactor-plan/SKILL.md` (auto-loaded skill `refactor-plan`); apply it to $ARGUMENTS if provided.

For the **cross-repo impact** section, use GitNexus rather than grep: `gitnexus_impact`
(blast radius), `gitnexus_api_impact` for API routes in the main app, and `gitnexus_rename` (dry-run) to
scope a rename — always passing `repo` (more than one repo may be indexed). See `skills/gitnexus`.
If the staleness hook reports the index is behind HEAD, run `/reindex-gitnexus` first.
GitNexus does not graph `devops/*` — use `rg` there.

To execute pure-function refactors directly, use the `functional-design` skill instead.
