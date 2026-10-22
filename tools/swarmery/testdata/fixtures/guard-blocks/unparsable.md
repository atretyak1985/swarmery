# Phase 3 — Unquoted globs
Status: Pending

## Guard

```yaml
kind: guard
protected_paths:
  - .github/workflows/*.yml
  - **/*_test.go
stop_if:
  - protected path
```

## Completion Report
