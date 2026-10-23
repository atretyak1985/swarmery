The staging console refuses my token.

```yaml
kind: stop
trigger: login-expired
reason: the deploy check needs a console session and the stored login has expired
options:
  - id: relogin
    label: Log in again and re-run
    consequence: the re-run performs the deploy check
```

PHASE BLOCKED: access-error
