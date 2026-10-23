# Phase 2 — Line-item totals
Status: Pending
**Repo:** `/repo` · **Branch:** `feat/line-item-totals` · **Depends on:** 1 · **Estimate:** S
**Verify:** off
**Review:** on

## Goal

Recompute an order's total from its line items.

## Copy-paste Agent Prompt

```
Repo: /repo. The format quotes the line it wants, which must not count:
- Blast radius: 3 callers in internal/orders — internal/orders/api.go:12 (sources: rg)
```

## Acceptance Criteria

- [ ] `go test ./internal/orders/...` passes

## Risks

- The totals cache may serve a stale value for one request.
- The format is `Blast radius: <N> callers in <pkgs> — <file:line, …>`, cited mid-sentence.

## Notes

- `Blast radius: …` quoted as inline code at the start of a line is a citation, not a measurement.

## Forecast

```yaml
kind: prior
written_at: 2026-10-21T20:30:00Z
areas: [internal/orders]
files: [internal/orders/totals.go, internal/orders/cache.go]
size_band: S
duration_band: 30-90m
outcome: done
confidence: 0.7
```

## Completion Report
