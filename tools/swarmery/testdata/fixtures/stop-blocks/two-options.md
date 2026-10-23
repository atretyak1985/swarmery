I read the guard and the failing criterion. Making it pass needs an edit to a
file the guard protects, so I stopped instead of working around it.

```yaml
kind: stop
trigger: protected-path
reason: the criterion needs scripts/tests/run.sh to call the new suite, and the guard protects scripts/tests/**/*
paths:
  - "scripts/tests/run.sh"
options:
  - id: allow
    label: Allow editing scripts/tests/run.sh
    consequence: the re-run adds the suite to the script and ticks the criterion
  - id: skip
    label: Keep the script protected
    consequence: the suite is not wired into CI; the criterion stays open
```

PHASE BLOCKED: protected-path
