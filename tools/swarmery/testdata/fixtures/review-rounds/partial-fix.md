Round 2. R1-1 is fixed: `writer.go` now returns the putObject error and the
handler maps it to a 502 (`TestUploadFailureIsReported` covers it).

No new blocking findings in the diff since round 1.

```yaml
kind: review
round: 2
verdict: PASS
complete: true
scope_left: []
findings: []
resolves: [R1-1]
ids: []
drift: []
checked_callers: []
```
VERDICT: PASS
