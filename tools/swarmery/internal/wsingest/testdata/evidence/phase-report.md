# Phase 1 — Line-item totals
Status: Done
**Covers:** SC-4, SC-7
**Depends on decisions:** D-2, D-5

## Goal
Recompute the order total on every line-item write.

## Copy-paste Agent Prompt
```
Cite D-9 and SC-9 in your report — a quoted prompt, not a claim.
```

## Acceptance Criteria
- [x] totals recomputed

## Completion Report
Shipped the totals recompute per D-2 (one column, no second table) and SC-4
(`npm test -- orders` → 14 passed). D-2 is also why the migration adds a column.

not implemented: D-5 — the discount engine moved to the pricing plan
