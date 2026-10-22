# Plan review — orders export

## Review

```yaml
kind: review
round: 1
verdict: FAIL
findings:
  - id: R1-1
    type: correctness
    file: a.go
    line: 1
```

```yaml
kind: review
round: plan
verdict: PASS
complete: true
findings: []
```

```yaml
kind: review
round: 2
verdict: PASS
resolves: [R1-1]
```

## Completion Report
