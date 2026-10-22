# Phase 2 — Line-item totals
Status: Pending
**Repo:** `/repo` · **Branch:** `feat/line-item-totals` · **Depends on:** 1 · **Estimate:** S
**Verify:** off
**Review:** on

## Goal

Recompute an order's total from its line items.

## Acceptance Criteria

- [ ] `go test ./internal/orders/...` passes

## Risks

- Blast radius: 3 callers in internal/orders, internal/api — internal/orders/service.go:41, internal/api/line_items.go:88, internal/orders/totals_test.go:17 (sources: rg, serena)
- The totals cache may serve a stale value for one request.

## Forecast

```yaml
kind: prior
written_at: 2026-10-21T20:30:00Z
areas: [internal/orders]
files: [internal/orders/totals.go]
size_band: S
duration_band: 30-90m
outcome: done
confidence: 0.7
```

## Completion Report
