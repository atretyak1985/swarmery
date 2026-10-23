I read the export writer and its tests. The fix for the retry criterion needs a
change to the writer's test, and the phase guard protects every `*_test.go`.

An earlier draft of this note quoted the format; that quote is not the stop:

```yaml
kind: stop
trigger: out-of-scope
reason: superseded draft
options: []
```

```yaml
kind: stop
trigger: protected-path
reason: >
  The retry criterion needs pkg/export/writer_test.go, which the guard
  protects.
paths:
  - pkg/export/writer_test.go
options:
  - id: allow-add-to-scope
    label: Let me edit the writer test
    consequence: the test changes together with the code it covers
  - id: skip-test-change
    label: Leave the test alone
    consequence: the retry criterion stays unticked
```

PHASE BLOCKED: protected-path
