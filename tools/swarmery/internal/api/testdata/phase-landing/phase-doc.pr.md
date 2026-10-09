## Goal

Let an operator add, edit and remove line items on an order.

The order total is recomputed on every change.

### Out of scope
Discount rules.

## Completion Report

Shipped `src/orders/line-items.ts` and its route in commit abc1234.

Verification: `npm test -- orders` → 14 passed.
Deviation: DELETE 404 deferred to phase 3.

### How to verify

- [x] `POST /orders/{id}/line-items` creates a line item and returns 201.
- [x] The order total is recomputed after every write (`npm test -- orders`).

---

Swarm-Phase: 41/207
Plan: working/2026/10/09/order-line-items/plan/phase-2-line-item-crud.md
