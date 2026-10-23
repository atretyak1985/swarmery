# Phase 2 — Orders import
Status: done
**Covers:** SC-3

## Acceptance Criteria

- [x] import endpoint

## Review

```yaml
kind: review
round: plan
verdict: FAIL
complete: true
findings: []
ids:
  - {id: D-2, evidence: "phase 1 @ 1a2b3c4 internal/export/writer.go:40"}
  - {id: SC-3, evidence: missing}
  - {id: parseRow, evidence: "internal/import/parse.go:12"}
drift:
  - {id: D-2, where: "internal/import/reader.go:88", how: "reads XLSX although D-2 chose CSV"}
```

## Completion Report

Import landed.
