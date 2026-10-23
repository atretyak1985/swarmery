The staging database refuses the login the phase doc names.

```yaml
kind: stop
trigger: database-down
reason: the staging login is refused
options:
  - id: retry-later
    label: Retry once the login works
    consequence: the phase waits
```

PHASE BLOCKED: access-error
