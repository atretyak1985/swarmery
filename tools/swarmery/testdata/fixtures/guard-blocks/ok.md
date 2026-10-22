# Phase 2 — Orders export
Status: Pending
**Repo:** `/path/to/repo` · **Branch:** `feat/orders-export` · **Depends on:** —

## Goal

Export order line items as CSV.

## Copy-paste Agent Prompt

```
A quoted example the parser must not read as this phase's guard:
kind: guard
protected_paths: [nothing]
```

## Guard

```yaml
kind: guard
protected_paths:
  - ".github/workflows/*.yml"
  - "plugins/*/hooks/hooks.json"
  - "**/*_test.go"
  - "**/*.test.ts"
  - "**/*.test.tsx"
  - "scripts/tests/**/*"
stop_if:
  - out-of-scope change
  - protected path
  - access or login error
  - criterion impossible
```

## Acceptance Criteria

- [ ] export works

## Completion Report
