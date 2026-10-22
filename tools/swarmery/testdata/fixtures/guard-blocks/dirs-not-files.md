# Phase 4 — Directory globs
Status: Pending

## Guard

```yaml
kind: guard
protected_paths:
  - ".github/workflows/**"
  - "scripts/tests/"
  - "**/*_test.go"
stop_if: [protected path]
```

## Completion Report
