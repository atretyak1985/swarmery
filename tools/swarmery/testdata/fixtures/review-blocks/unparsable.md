# Phase 6 — Broken review blocks
Status: In progress

## Review

```yaml
kind: review
round: 1
complete: true
findings: []
```

```yaml
kind: review
round: 2
verdict: PASS
findings:
  - just a string, not a map
```

```yaml
kind: review
round: 3
verdict: FAIL
findings: [{id: R3-1, type: quality, file: a.go, line: 3}]
```

## Completion Report
