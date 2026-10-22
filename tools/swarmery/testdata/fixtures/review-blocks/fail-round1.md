# Phase 2 — Orders export
Status: In progress
**Review:** on

## Acceptance Criteria

- [x] export works
- [x] upload failure is reported

## Review

```yaml
kind: review
round: 1
verdict: FAIL
complete: true
findings:
  - id: R1-1
    type: silent-failure
    file: src/lib/export/writer.ts
    line: 84
    criterion: upload failure is reported
    test_caught: false
    evidence: "catch {} swallows the putObject error and the job is marked done"
    note: |
      Fail the upload call in a test: the handler still returns 200
      and hands back a presigned URL for a missing object.
  - id: R1-2
    type: contract
    file: src/app/api/export/route.ts
    line: 31
    test_caught: true
    evidence: route returns `url`, the client reads `downloadUrl`
drift:
  - criterion: upload failure is reported
    file: src/lib/export/writer.ts
    note: ticked, but the failure is swallowed
```

## Completion Report
