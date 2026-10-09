# Phase 2 — Line-item CRUD
Status: Done
**Repo:** `apps/<mainApp>` · **Branch:** `feat/line-items` · **Depends on:** 1 · **Estimate:** M

## Goal
Let an operator add, edit and remove line items on an order.

The order total is recomputed on every change.

### Out of scope
Discount rules.

## Files to Create
- `src/orders/line-items.ts`

## Copy-paste Agent Prompt
```
## Goal
quoted template goal that must not leak into the change request
- [x] quoted criterion that must not count
```

## Acceptance Criteria
- [x] `POST /orders/{id}/line-items` creates a line item and returns 201.
- [ ] `DELETE` returns 404 for an unknown line item.
- [x] The order total is recomputed after every write (`npm test -- orders`).

## Completion Report
Shipped `src/orders/line-items.ts` and its route in commit abc1234.

Verification: `npm test -- orders` → 14 passed.
Deviation: DELETE 404 deferred to phase 3.
