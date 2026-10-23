# Phase 1 — Orders export
Status: done
**Covers:** SC-3, SC-4
**Depends on decisions:** D-2, D-5

## Goal

Export orders as CSV.

## Acceptance Criteria

- [x] export endpoint
- [x] writer streams rows

## Completion Report

Shipped the streaming writer as decided in D-2 (CSV, not XLSX); the endpoint
returns 413 past the row cap, which closes SC-4.

```
go test ./... # D-9 inside a fence is quoted output, not a citation
```

- not implemented: D-5 — the S3 mirror waits on the bucket policy
- D-5 is mentioned again in passing; the line above still wins
